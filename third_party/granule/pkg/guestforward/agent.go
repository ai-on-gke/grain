package guestforward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"syscall"
	"time"
)

// AgentOptions tunes the guest end of a forwarding session.
type AgentOptions struct {
	// MaxListeners caps the listeners one session may hold open in the
	// guest. Default 64.
	MaxListeners int

	// DialTimeout bounds dialing a target inside the guest. Default 10s.
	DialTimeout time.Duration

	// Session tunes the multiplexer, including the cap on concurrent
	// streams (that is, forwarded connections plus listeners).
	Session Config

	// Logf, if set, is told about connections that could not be
	// forwarded. Nil discards.
	Logf func(format string, args ...any)
}

// ServeAgent serves one forwarding session on conn, the guest end of it:
// it binds the listeners the caller asks for, dials the targets the
// caller asks for, and splices. It returns when the session ends, having
// closed every listener and connection it opened -- mappings live
// exactly as long as the session that asked for them.
//
// A session that ends because the far end hung up is not an error.
func ServeAgent(ctx context.Context, conn io.ReadWriter, opts AgentOptions) error {
	if opts.MaxListeners <= 0 {
		opts.MaxListeners = 64
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 10 * time.Second
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	sess := NewSession(conn, false, opts.Session)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		sess.Close()
	}()

	a := &agent{sess: sess, opts: opts, logf: logf}
	for {
		st, err := sess.Accept(ctx)
		if err != nil {
			serr := sess.Err()
			if serr == nil || errors.Is(serr, io.EOF) || errors.Is(serr, ErrSessionClosed) {
				return nil
			}
			return serr
		}
		var m openMsg
		if err := json.Unmarshal(st.Meta(), &m); err != nil {
			_ = st.Reject(fmt.Sprintf("malformed open: %v", err))
			continue
		}
		switch m.Op {
		case opListen:
			go a.listen(ctx, st, m)
		case opConnect:
			go a.connect(st, m)
		default:
			_ = st.Reject(fmt.Sprintf("unknown operation %q", m.Op))
		}
	}
}

type agent struct {
	sess      *Session
	opts      AgentOptions
	logf      func(string, ...any)
	listeners atomic.Int32
}

func (a *agent) listen(ctx context.Context, st *Stream, m openMsg) {
	network, address, err := ParseAddr(m.Addr)
	if err != nil {
		_ = st.Reject(err.Error())
		return
	}
	if !m.AnyAddr && !isLoopback(network, address) {
		_ = st.Reject(fmt.Sprintf("refusing to listen on %s in the guest: forwarded listeners are loopback-only unless the caller explicitly allows any address", m.Addr))
		return
	}
	if n := a.listeners.Add(1); int(n) > a.opts.MaxListeners {
		a.listeners.Add(-1)
		_ = st.Reject(fmt.Sprintf("too many listeners in this session (limit %d)", a.opts.MaxListeners))
		return
	}
	defer a.listeners.Add(-1)

	ln, err := net.Listen(network, address)
	if err != nil {
		reason := fmt.Sprintf("listening in the guest: %v", err)
		if errors.Is(err, syscall.EADDRNOTAVAIL) && isLoopback(network, address) {
			// Not something forwarding will fix by itself: it does not
			// touch the guest's interfaces, and this guest's loopback
			// has no address -- lo is down. A networked guest gets lo
			// from the kernel's ip= setup; one without a network needs
			// its init to do it (pack-guest enables OpenRC's loopback
			// service for this; `granule guest rebase` lays that down).
			reason += " -- the guest's loopback interface has no address (lo is down); rebase the guest image so its init brings lo up"
		}
		_ = st.Reject(reason)
		return
	}
	defer ln.Close()
	if err := st.Confirm(encode(listenReply{Addr: formatAddr(ln.Addr())})); err != nil {
		return
	}

	// The listen stream carries nothing; it exists so that its end --
	// the caller closing it, or the session going -- is the listener's
	// end too.
	go func() {
		_, _ = io.Copy(io.Discard, st)
		_ = ln.Close()
	}()
	defer st.Close()

	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil || a.sess.Err() != nil {
				return
			}
			// EMFILE and friends: transient, and worth not tearing the
			// mapping down over.
			a.logf("accepting on %s: %v", m.Addr, err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go func() {
			out, err := a.sess.Open(ctx, encode(openMsg{Op: opAccepted, Listener: st.ID()}))
			if err != nil {
				a.logf("forwarding a connection to %s: %v", m.Addr, err)
				_ = c.Close()
				return
			}
			splice(out, c)
		}()
	}
}

func (a *agent) connect(st *Stream, m openMsg) {
	network, address, err := ParseAddr(m.Addr)
	if err != nil {
		_ = st.Reject(err.Error())
		return
	}
	c, err := net.DialTimeout(network, address, a.opts.DialTimeout)
	if err != nil {
		_ = st.Reject(fmt.Sprintf("dialing %s in the guest: %v", m.Addr, err))
		return
	}
	if err := st.Confirm(nil); err != nil {
		_ = c.Close()
		return
	}
	splice(st, c)
}

// splice copies between a stream and a connection in both directions
// until both are done, passing a half-close through in each direction:
// EOF from c becomes a fin on st, and EOF on st a CloseWrite on c. An
// error in either direction tears down both.
func splice(st *Stream, c net.Conn) {
	upDone := make(chan struct{})
	go func() {
		defer close(upDone)
		if _, err := io.Copy(st, c); err != nil {
			st.Reset(err.Error())
			_ = c.Close()
			return
		}
		_ = st.CloseWrite()
	}()

	if _, err := io.Copy(c, st); err != nil {
		_ = c.Close()
		st.Reset(err.Error())
	} else if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	} else {
		_ = c.Close()
	}
	<-upDone
	_ = c.Close()
	_ = st.Close()
}
