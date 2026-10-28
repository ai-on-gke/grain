package guestforward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Transport opens a fresh byte stream to a guest's agent: in practice the
// stdio of `docker exec -i <container> granule forward-stdio` or its
// kubectl equivalent (see DockerExec, KubectlExec and CommandTransport),
// though anything that reaches the shim's forward-stdio mode will do --
// a client-go exec stream, say. Closing it must end the session.
type Transport func(ctx context.Context) (io.ReadWriteCloser, error)

// RemoteForward is a guest → caller mapping, ssh -R's shape: the agent
// listens on Guest inside the guest, and each connection made to it there
// is connected to Target on the caller's side.
type RemoteForward struct {
	Guest  string
	Target string

	// AnyAddr lets Guest be an address other than loopback. Off, the
	// agent refuses to listen anywhere a network peer could reach.
	AnyAddr bool
}

// LocalForward is a caller → guest mapping, ssh -L's shape: the caller
// listens on Listen, and each connection made to it is connected to
// Guest, dialled from inside the guest.
type LocalForward struct {
	Listen string
	Guest  string
}

// Options configures a Forwarder.
type Options struct {
	Transport Transport
	Remote    []RemoteForward
	Local     []LocalForward

	// Reconnect keeps the forwarder going when its transport drops --
	// as kubelet and API-server exec streams do, on control-plane
	// upgrades and idle timeouts -- by opening a new one, with backoff,
	// and asking the agent for every remote listener again. Connections
	// in flight when the transport dropped are lost either way; local
	// listeners stay bound throughout, and a connection made to one
	// while there is no transport waits up to ReadyTimeout for one.
	//
	// Without it the forwarder ends, with the transport's error, when the
	// transport does.
	Reconnect bool

	// MinBackoff and MaxBackoff bound the wait between reconnect
	// attempts, which doubles after each failure. Default 500ms and 30s.
	MinBackoff, MaxBackoff time.Duration

	// ReadyTimeout bounds how long one attempt waits for the agent to
	// answer and confirm every remote listener. The shim itself waits up
	// to its GRANULE_EXEC_CONNECT_TIMEOUT (30s by default) for a booting
	// guest, so this should be longer. Default 60s.
	ReadyTimeout time.Duration

	// DialTimeout bounds dialing a caller-side Target. Default 10s.
	DialTimeout time.Duration

	// MaxMappings caps len(Remote)+len(Local). Default 64.
	MaxMappings int

	// Session tunes the multiplexer, including the cap on concurrent
	// streams.
	Session Config

	// Logf, if set, hears about reconnects and connections that could
	// not be forwarded. Nil discards.
	Logf func(format string, args ...any)
}

// Binding is one established mapping, as a caller would want to print
// it: what is listening where, and where its connections go.
type Binding struct {
	// Direction is "local" (a caller-side listener, to a guest target)
	// or "remote" (a guest-side listener, to a caller target).
	Direction string `json:"direction"`

	// Listen is the address actually bound -- the port the system
	// picked, if the mapping asked for port 0. Remote listeners keep
	// this address across reconnects.
	Listen string `json:"listen"`
	Target string `json:"target"`
}

// Forwarder is a running set of mappings over one guest's agent.
type Forwarder struct {
	opts Options
	logf func(string, ...any)

	ctx    context.Context
	cancel context.CancelFunc

	locals      []net.Listener
	remoteAddrs []string // what to ask the agent for; pinned after the first bind

	mu       sync.Mutex
	cur      *Session
	listenID map[uint32]int // listen stream id → index into opts.Remote
	changed  chan struct{}

	wg   sync.WaitGroup
	done chan struct{}
	err  error
}

// Start binds every local listener, opens a transport, asks the agent
// for every remote listener, and returns once all of that is in place,
// or with an error saying which part was not -- a guest that is not up,
// a port that is taken, a target the agent refused. The forwarder then
// runs until ctx is cancelled, Close is called, or (without
// Options.Reconnect) the transport drops.
func Start(ctx context.Context, opts Options) (*Forwarder, error) {
	if opts.Transport == nil {
		return nil, errors.New("guestforward: no transport")
	}
	if opts.MinBackoff <= 0 {
		opts.MinBackoff = 500 * time.Millisecond
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 30 * time.Second
	}
	if opts.MaxBackoff < opts.MinBackoff {
		opts.MaxBackoff = opts.MinBackoff
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 60 * time.Second
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 10 * time.Second
	}
	if opts.MaxMappings <= 0 {
		opts.MaxMappings = 64
	}
	if n := len(opts.Remote) + len(opts.Local); n == 0 {
		return nil, errors.New("guestforward: no mappings to forward")
	} else if n > opts.MaxMappings {
		return nil, fmt.Errorf("guestforward: %d mappings exceeds the limit of %d", n, opts.MaxMappings)
	}
	for _, r := range opts.Remote {
		if _, _, err := ParseAddr(r.Guest); err != nil {
			return nil, fmt.Errorf("remote listen address: %w", err)
		}
		if _, _, err := ParseAddr(r.Target); err != nil {
			return nil, fmt.Errorf("remote target: %w", err)
		}
	}
	for _, l := range opts.Local {
		if _, _, err := ParseAddr(l.Guest); err != nil {
			return nil, fmt.Errorf("guest target: %w", err)
		}
	}

	f := &Forwarder{
		opts:    opts,
		logf:    opts.Logf,
		changed: make(chan struct{}),
		done:    make(chan struct{}),
	}
	if f.logf == nil {
		f.logf = func(string, ...any) {}
	}
	f.ctx, f.cancel = context.WithCancel(ctx)
	for _, r := range opts.Remote {
		f.remoteAddrs = append(f.remoteAddrs, r.Guest)
	}

	for _, l := range opts.Local {
		network, address, err := ParseAddr(l.Listen)
		if err != nil {
			f.closeLocals()
			f.cancel()
			return nil, fmt.Errorf("local listen address: %w", err)
		}
		ln, err := net.Listen(network, address)
		if err != nil {
			f.closeLocals()
			f.cancel()
			return nil, fmt.Errorf("listening on %s: %w", l.Listen, err)
		}
		f.locals = append(f.locals, ln)
	}

	sess, err := f.connect(f.ctx)
	if err != nil {
		f.closeLocals()
		f.cancel()
		return nil, err
	}

	for i, ln := range f.locals {
		f.wg.Add(1)
		go f.serveLocal(ln, opts.Local[i].Guest)
	}
	go f.run(sess)
	return f, nil
}

// Bindings reports every mapping and the address it is listening on.
func (f *Forwarder) Bindings() []Binding {
	var out []Binding
	for i, ln := range f.locals {
		out = append(out, Binding{Direction: "local", Listen: formatAddr(ln.Addr()), Target: f.opts.Local[i].Guest})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.opts.Remote {
		out = append(out, Binding{Direction: "remote", Listen: f.remoteAddrs[i], Target: r.Target})
	}
	return out
}

// Wait blocks until the forwarder has stopped and reports why: nil for
// Close or a cancelled context, the transport's error otherwise.
func (f *Forwarder) Wait() error {
	<-f.done
	return f.err
}

// Done is closed once the forwarder has stopped.
func (f *Forwarder) Done() <-chan struct{} { return f.done }

// Close stops the forwarder, closing every listener and connection.
func (f *Forwarder) Close() error {
	f.cancel()
	<-f.done
	return nil
}

func (f *Forwarder) closeLocals() {
	for _, ln := range f.locals {
		_ = ln.Close()
	}
}

// connect opens one transport and sets every remote listener up on it.
func (f *Forwarder) connect(ctx context.Context) (*Session, error) {
	rctx, cancel := context.WithTimeout(ctx, f.opts.ReadyTimeout)
	defer cancel()

	conn, err := f.opts.Transport(rctx)
	if err != nil {
		return nil, fmt.Errorf("opening the transport: %w", err)
	}
	sess := NewSession(conn, true, f.opts.Session)
	if _, err := sess.Ping(rctx); err != nil {
		sess.Close()
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("the guest's agent did not answer within %s", f.opts.ReadyTimeout)
		}
		return nil, fmt.Errorf("reaching the guest's agent: %w", err)
	}

	ids := make(map[uint32]int)
	for i, r := range f.opts.Remote {
		f.mu.Lock()
		addr := f.remoteAddrs[i]
		f.mu.Unlock()
		st, err := sess.Open(rctx, encode(openMsg{Op: opListen, Addr: addr, AnyAddr: r.AnyAddr}))
		if err != nil {
			sess.Close()
			return nil, fmt.Errorf("listening on %s in the guest: %w", addr, err)
		}
		var reply listenReply
		if err := json.Unmarshal(st.Reply(), &reply); err != nil || reply.Addr == "" {
			sess.Close()
			return nil, fmt.Errorf("listening on %s in the guest: malformed reply %q", addr, st.Reply())
		}
		f.mu.Lock()
		// Pinned: a port 0 the guest resolved the first time is asked
		// for by number on every reconnect, so the guest-side address a
		// caller was told stays true.
		f.remoteAddrs[i] = reply.Addr
		f.mu.Unlock()
		ids[st.ID()] = i
	}

	f.mu.Lock()
	f.cur = sess
	f.listenID = ids
	close(f.changed)
	f.changed = make(chan struct{})
	f.mu.Unlock()

	go f.serveRemote(sess, ids)
	return sess, nil
}

func (f *Forwarder) run(sess *Session) {
	defer func() {
		f.cancel()
		f.closeLocals()
		f.wg.Wait()
		close(f.done)
	}()

	backoff := f.opts.MinBackoff
	for {
		select {
		case <-sess.Done():
		case <-f.ctx.Done():
			sess.Close()
			return
		}
		f.mu.Lock()
		f.cur = nil
		f.mu.Unlock()
		if f.ctx.Err() != nil {
			return
		}
		if !f.opts.Reconnect {
			f.err = fmt.Errorf("transport to the guest ended: %w", sess.Err())
			return
		}
		f.logf("transport to the guest ended: %v; reconnecting", sess.Err())

		for {
			select {
			case <-time.After(backoff):
			case <-f.ctx.Done():
				return
			}
			next, err := f.connect(f.ctx)
			if err == nil {
				f.logf("reconnected")
				sess = next
				backoff = f.opts.MinBackoff
				break
			}
			if f.ctx.Err() != nil {
				return
			}
			backoff *= 2
			if backoff > f.opts.MaxBackoff {
				backoff = f.opts.MaxBackoff
			}
			f.logf("reconnecting: %v; retrying in %s", err, backoff)
		}
	}
}

// session returns the current session, waiting up to ReadyTimeout for
// one while a reconnect is under way.
func (f *Forwarder) session() *Session {
	timeout := time.NewTimer(f.opts.ReadyTimeout)
	defer timeout.Stop()
	for {
		f.mu.Lock()
		s, ch := f.cur, f.changed
		f.mu.Unlock()
		if s != nil && s.Err() == nil {
			return s
		}
		select {
		case <-ch:
		case <-timeout.C:
			return nil
		case <-f.ctx.Done():
			return nil
		}
	}
}

func (f *Forwarder) serveLocal(ln net.Listener, guest string) {
	defer f.wg.Done()
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || f.ctx.Err() != nil {
				return
			}
			f.logf("accepting on %s: %v", ln.Addr(), err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go func() {
			sess := f.session()
			if sess == nil {
				f.logf("connection to %s dropped: no transport to the guest", ln.Addr())
				_ = c.Close()
				return
			}
			st, err := sess.Open(f.ctx, encode(openMsg{Op: opConnect, Addr: guest}))
			if err != nil {
				f.logf("forwarding %s to %s in the guest: %v", ln.Addr(), guest, err)
				_ = c.Close()
				return
			}
			splice(st, c)
		}()
	}
}

func (f *Forwarder) serveRemote(sess *Session, ids map[uint32]int) {
	for {
		st, err := sess.Accept(f.ctx)
		if err != nil {
			return
		}
		var m openMsg
		if err := json.Unmarshal(st.Meta(), &m); err != nil || m.Op != opAccepted {
			_ = st.Reject("the caller only accepts forwarded connections")
			continue
		}
		i, ok := ids[m.Listener]
		if !ok {
			_ = st.Reject(fmt.Sprintf("no listener %d in this session", m.Listener))
			continue
		}
		target := f.opts.Remote[i].Target
		go func() {
			network, address, _ := ParseAddr(target)
			c, err := net.DialTimeout(network, address, f.opts.DialTimeout)
			if err != nil {
				f.logf("forwarding a connection from the guest to %s: %v", target, err)
				_ = st.Reject(fmt.Sprintf("dialing %s on the caller: %v", target, err))
				return
			}
			if err := st.Confirm(nil); err != nil {
				_ = c.Close()
				return
			}
			splice(st, c)
		}()
	}
}
