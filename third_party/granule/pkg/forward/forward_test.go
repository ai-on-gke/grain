package forward

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseSpec(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		dir  Direction
		want string
	}{
		{"9728:127.0.0.1:9728", RemoteToLocal, "127.0.0.1:9728:127.0.0.1:9728"},
		{"127.0.0.1:9728:127.0.0.1:9729", RemoteToLocal, "127.0.0.1:9728:127.0.0.1:9729"},
		{"8080:80", LocalToRemote, "127.0.0.1:8080:127.0.0.1:80"},
	}
	for _, tc := range cases {
		got, err := ParseSpec(tc.dir, tc.raw)
		if err != nil {
			t.Fatalf("ParseSpec(%q): %v", tc.raw, err)
		}
		if got.String() != tc.want {
			t.Errorf("ParseSpec(%q).String() = %q, want %q", tc.raw, got.String(), tc.want)
		}
	}

	for _, bad := range []string{"", "9728", "0:127.0.0.1:80", "9728:127.0.0.1:70000", "a:b:c:d:e"} {
		if _, err := ParseSpec(RemoteToLocal, bad); err == nil {
			t.Errorf("ParseSpec(%q) succeeded, want error", bad)
		}
	}
}

func TestRemoteAndLocalForwardOverPipe(t *testing.T) {
	t.Parallel()

	// Controller HTTP server (reached from guest via -R).
	controllerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "controller:%s", r.URL.Path)
	}))
	defer controllerSrv.Close()
	controllerPort := controllerSrv.Listener.Addr().(*net.TCPAddr).Port

	// Guest HTTP server (reached from controller via -L).
	guestSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "guest:%s", r.URL.Path)
	}))
	defer guestSrv.Close()
	guestPort := guestSrv.Listener.Addr().(*net.TCPAddr).Port

	// Pick free ports for the two forward listeners.
	guestBindPort := freeTCPPort(t)
	controllerBindPort := freeTCPPort(t)

	remoteSpec := Spec{
		Direction:  RemoteToLocal,
		BindHost:   "127.0.0.1",
		BindPort:   guestBindPort,
		TargetHost: "127.0.0.1",
		TargetPort: controllerPort,
	}
	localSpec := Spec{
		Direction:  LocalToRemote,
		BindHost:   "127.0.0.1",
		BindPort:   controllerBindPort,
		TargetHost: "127.0.0.1",
		TargetPort: guestPort,
	}

	c2gR, c2gW := io.Pipe()
	g2cR, g2cW := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readyCh := make(chan struct{})
	errCh := make(chan error, 2)

	go func() {
		errCh <- ServeGuest(ctx, c2gR, g2cW, []Spec{remoteSpec})
	}()
	go func() {
		errCh <- RunClient(ctx, g2cR, c2gW, []Spec{remoteSpec, localSpec}, func() {
			close(readyCh)
		})
	}()

	select {
	case <-readyCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for forward session ready")
	}

	// Concurrent requests through -R (guest -> controller) and -L (controller -> guest).
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			url := fmt.Sprintf("http://127.0.0.1:%d/r/%d", guestBindPort, i)
			resp, err := http.Get(url)
			if err != nil {
				t.Errorf("GET %s: %v", url, err)
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if want := fmt.Sprintf("controller:/r/%d", i); string(body) != want {
				t.Errorf("GET %s = %q, want %q", url, body, want)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			url := fmt.Sprintf("http://127.0.0.1:%d/l/%d", controllerBindPort, i)
			resp, err := http.Get(url)
			if err != nil {
				t.Errorf("GET %s: %v", url, err)
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if want := fmt.Sprintf("guest:/l/%d", i); string(body) != want {
				t.Errorf("GET %s = %q, want %q", url, body, want)
			}
		}(i)
	}
	wg.Wait()

	cancel()
	_ = c2gW.Close()
	_ = g2cW.Close()
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// frameConn is the far end of a mux session driven by hand, frame by frame,
// the way a hostile peer would drive it.
type frameConn struct {
	t *testing.T
	r io.Reader
	w io.Writer
}

func (f frameConn) write(typ byte, id uint32, payload []byte) {
	f.t.Helper()
	var hdr [headerLen]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:5], id)
	binary.BigEndian.PutUint32(hdr[5:9], uint32(len(payload)))
	if _, err := f.w.Write(append(hdr[:], payload...)); err != nil {
		f.t.Fatalf("writing frame %d: %v", typ, err)
	}
}

func (f frameConn) read() (byte, uint32, []byte) {
	f.t.Helper()
	type frame struct {
		typ     byte
		id      uint32
		payload []byte
		err     error
	}
	ch := make(chan frame, 1)
	go func() {
		var hdr [headerLen]byte
		if _, err := io.ReadFull(f.r, hdr[:]); err != nil {
			ch <- frame{err: err}
			return
		}
		payload := make([]byte, binary.BigEndian.Uint32(hdr[5:9]))
		if _, err := io.ReadFull(f.r, payload); err != nil {
			ch <- frame{err: err}
			return
		}
		ch <- frame{typ: hdr[0], id: binary.BigEndian.Uint32(hdr[1:5]), payload: payload}
	}()
	select {
	case fr := <-ch:
		if fr.err != nil {
			f.t.Fatalf("reading frame: %v", fr.err)
		}
		return fr.typ, fr.id, fr.payload
	case <-time.After(5 * time.Second):
		f.t.Fatal("timed out waiting for a frame")
		return 0, 0, nil
	}
}

// readStream reads frames until one for stream id arrives, skipping the
// Close frames a stream an earlier step closed may still send.
func (f frameConn) readStream(id uint32) (byte, uint32, []byte) {
	f.t.Helper()
	for {
		typ, gotID, payload := f.read()
		if gotID == id || typ != frameClose {
			return typ, gotID, payload
		}
	}
}

// echoListener accepts connections on loopback, echoes what they send, and
// reports every connection it accepts on the returned channel.
func echoListener(t *testing.T) (int, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan struct{}, 16)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, accepted
}

// startClient runs RunClient with specs and returns the guest's end of it.
func startClient(t *testing.T, specs []Spec) frameConn {
	t.Helper()
	toClientR, toClientW := io.Pipe()
	fromClientR, fromClientW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = RunClient(ctx, toClientR, fromClientW, specs, nil)
	}()
	t.Cleanup(func() {
		cancel()
		_ = toClientW.Close()
		_ = fromClientR.Close()
		<-done
	})
	return frameConn{t: t, r: fromClientR, w: toClientW}
}

// startGuest runs GuestListeners.Serve with specs and returns the
// controller's end of it, past the Ready frame.
func startGuest(t *testing.T, specs []Spec) frameConn {
	t.Helper()
	gl, err := BindGuestListeners(specs)
	if err != nil {
		t.Fatal(err)
	}
	toGuestR, toGuestW := io.Pipe()
	fromGuestR, fromGuestW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = gl.Serve(ctx, toGuestR, fromGuestW)
	}()
	t.Cleanup(func() {
		cancel()
		_ = toGuestW.Close()
		_ = fromGuestR.Close()
		<-done
	})
	fc := frameConn{t: t, r: fromGuestR, w: toGuestW}
	if typ, _, _ := fc.read(); typ != frameReady {
		t.Fatalf("first guest frame = %d, want Ready", typ)
	}
	return fc
}

func assertOpenRefused(t *testing.T, fc frameConn, id uint32, target string, accepted <-chan struct{}) {
	t.Helper()
	fc.write(frameOpen, id, []byte(target))
	typ, gotID, payload := fc.readStream(id)
	if typ != frameOpenErr || gotID != id {
		t.Fatalf("Open %q: got frame %d for stream %d (%q), want OpenErr for stream %d", target, typ, gotID, payload, id)
	}
	if !strings.Contains(string(payload), "not") {
		t.Errorf("Open %q: OpenErr payload %q does not say why", target, payload)
	}
	select {
	case <-accepted:
		t.Fatalf("Open %q: the unlisted listener was connected to", target)
	case <-time.After(100 * time.Millisecond):
	}
}

func assertOpenEchoes(t *testing.T, fc frameConn, id uint32, target string, accepted <-chan struct{}) {
	t.Helper()
	fc.write(frameOpen, id, []byte(target))
	if typ, gotID, payload := fc.readStream(id); typ != frameOpenOK || gotID != id {
		t.Fatalf("Open %q: got frame %d for stream %d (%q), want OpenOK for stream %d", target, typ, gotID, payload, id)
	}
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatalf("Open %q: the listed target was never connected to", target)
	}
	fc.write(frameData, id, []byte("ping"))
	if typ, gotID, payload := fc.readStream(id); typ != frameData || gotID != id || string(payload) != "ping" {
		t.Fatalf("Open %q: got frame %d for stream %d (%q), want the echoed data", target, typ, gotID, payload)
	}
	fc.write(frameClose, id, nil)
}

func TestRunClientRefusesOpenForUnlistedTarget(t *testing.T) {
	t.Parallel()
	listedPort, _ := echoListener(t)
	unlistedPort, unlistedAccepted := echoListener(t)

	fc := startClient(t, []Spec{{
		Direction: RemoteToLocal, BindHost: "127.0.0.1", BindPort: freeTCPPort(t),
		TargetHost: "127.0.0.1", TargetPort: listedPort,
	}})

	assertOpenRefused(t, fc, 2, fmt.Sprintf("127.0.0.1:%d", unlistedPort), unlistedAccepted)
	// Other spellings of an address are refused too unless they normalise
	// to a listed target, and garbage never reaches the dialer.
	assertOpenRefused(t, fc, 4, fmt.Sprintf("localhost:%d", unlistedPort), unlistedAccepted)
	assertOpenRefused(t, fc, 6, fmt.Sprintf("[::ffff:127.0.0.1]:%d", unlistedPort), unlistedAccepted)
	assertOpenRefused(t, fc, 8, "169.254.169.254:80", unlistedAccepted)
	assertOpenRefused(t, fc, 10, "not-an-address", unlistedAccepted)
	assertOpenRefused(t, fc, 12, "", unlistedAccepted)
}

func TestRunClientDialsListedTarget(t *testing.T) {
	t.Parallel()
	listedPort, listedAccepted := echoListener(t)

	fc := startClient(t, []Spec{{
		Direction: RemoteToLocal, BindHost: "127.0.0.1", BindPort: freeTCPPort(t),
		TargetHost: "127.0.0.1", TargetPort: listedPort,
	}})

	assertOpenEchoes(t, fc, 2, fmt.Sprintf("127.0.0.1:%d", listedPort), listedAccepted)
	// A spelling that normalises to the listed target is dialed as the
	// spec says, not as the frame says.
	assertOpenEchoes(t, fc, 4, fmt.Sprintf("[::ffff:127.0.0.1]:%d", listedPort), listedAccepted)
}

func TestRunClientWithoutRemoteSpecsRefusesEveryOpen(t *testing.T) {
	t.Parallel()
	port, accepted := echoListener(t)
	fc := startClient(t, []Spec{{
		Direction: LocalToRemote, BindHost: "127.0.0.1", BindPort: freeTCPPort(t),
		TargetHost: "127.0.0.1", TargetPort: port,
	}})
	// The -L target is the guest's to dial, not the controller's.
	assertOpenRefused(t, fc, 2, fmt.Sprintf("127.0.0.1:%d", port), accepted)
}

func TestGuestServeRefusesOpenForUnlistedTarget(t *testing.T) {
	t.Parallel()
	listedPort, listedAccepted := echoListener(t)
	unlistedPort, unlistedAccepted := echoListener(t)

	fc := startGuest(t, []Spec{{
		Direction: LocalToRemote, BindHost: "127.0.0.1", BindPort: freeTCPPort(t),
		TargetHost: "127.0.0.1", TargetPort: listedPort,
	}})

	assertOpenRefused(t, fc, 1, fmt.Sprintf("127.0.0.1:%d", unlistedPort), unlistedAccepted)
	assertOpenEchoes(t, fc, 3, fmt.Sprintf("127.0.0.1:%d", listedPort), listedAccepted)
}

func TestNormaliseAddr(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"127.0.0.1:9728":          "127.0.0.1:9728",
		"[::ffff:127.0.0.1]:9728": "127.0.0.1:9728",
		"[0:0:0:0:0:0:0:1]:80":    "[::1]:80",
		"LocalHost:080":           "localhost:80",
	} {
		got, err := normaliseAddr(in)
		if err != nil || got != want {
			t.Errorf("normaliseAddr(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "9728", "host", ":9728", "host:0", "host:70000", "host:http"} {
		if got, err := normaliseAddr(bad); err == nil {
			t.Errorf("normaliseAddr(%q) = %q, want error", bad, got)
		}
	}
}

// assertOpenRefusedFor writes an Open for id and expects an OpenErr for it
// whose payload contains why, with nothing dialed.
func assertOpenRefusedFor(t *testing.T, fc frameConn, id uint32, target, why string, accepted <-chan struct{}) {
	t.Helper()
	fc.write(frameOpen, id, []byte(target))
	typ, gotID, payload := fc.readStream(id)
	if typ != frameOpenErr || gotID != id {
		t.Fatalf("Open of stream %d: got frame %d for stream %d (%q), want OpenErr", id, typ, gotID, payload)
	}
	if !strings.Contains(string(payload), why) {
		t.Errorf("Open of stream %d: OpenErr payload %q does not say %q", id, payload, why)
	}
	if accepted != nil {
		select {
		case <-accepted:
			t.Fatalf("Open of stream %d: the target was connected to", id)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func TestRunClientRefusesReusedStreamID(t *testing.T) {
	t.Parallel()
	port, accepted := echoListener(t)
	target := fmt.Sprintf("127.0.0.1:%d", port)
	fc := startClient(t, []Spec{{
		Direction: RemoteToLocal, BindHost: "127.0.0.1", BindPort: freeTCPPort(t),
		TargetHost: "127.0.0.1", TargetPort: port,
	}})

	fc.write(frameOpen, 2, []byte(target))
	if typ, gotID, payload := fc.readStream(2); typ != frameOpenOK || gotID != 2 {
		t.Fatalf("first Open: got frame %d for stream %d (%q), want OpenOK", typ, gotID, payload)
	}
	<-accepted
	for i := 0; i < 5; i++ {
		assertOpenRefusedFor(t, fc, 2, target, "already in use", accepted)
	}
	// The first stream is untouched by the refused reuses.
	fc.write(frameData, 2, []byte("ping"))
	if typ, gotID, payload := fc.readStream(2); typ != frameData || gotID != 2 || string(payload) != "ping" {
		t.Fatalf("got frame %d for stream %d (%q), want the echoed data", typ, gotID, payload)
	}
}

func TestRefusesOpenWithOwnParity(t *testing.T) {
	t.Parallel()
	port, accepted := echoListener(t)
	target := fmt.Sprintf("127.0.0.1:%d", port)

	client := startClient(t, []Spec{{
		Direction: RemoteToLocal, BindHost: "127.0.0.1", BindPort: freeTCPPort(t),
		TargetHost: "127.0.0.1", TargetPort: port,
	}})
	// The controller allocates odd ids; the guest's are even and nonzero.
	for _, id := range []uint32{1, 3, 0} {
		assertOpenRefusedFor(t, client, id, target, "not one the peer allocates", accepted)
	}

	guest := startGuest(t, []Spec{{
		Direction: LocalToRemote, BindHost: "127.0.0.1", BindPort: freeTCPPort(t),
		TargetHost: "127.0.0.1", TargetPort: port,
	}})
	for _, id := range []uint32{2, 4, 0} {
		assertOpenRefusedFor(t, guest, id, target, "not one the peer allocates", accepted)
	}
}

func TestRunClientCapsConcurrentStreams(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	target := fmt.Sprintf("127.0.0.1:%d", port)
	fc := startClient(t, []Spec{{
		Direction: RemoteToLocal, BindHost: "127.0.0.1", BindPort: freeTCPPort(t),
		TargetHost: "127.0.0.1", TargetPort: port,
	}})

	id := uint32(2)
	for i := 0; i < maxStreams; i++ {
		fc.write(frameOpen, id, []byte(target))
		if typ, gotID, payload := fc.readStream(id); typ != frameOpenOK || gotID != id {
			t.Fatalf("Open %d of %d: got frame %d for stream %d (%q), want OpenOK", i+1, maxStreams, typ, gotID, payload)
		}
		id += 2
	}
	assertOpenRefusedFor(t, fc, id, target, "streams are already open", nil)
	mu.Lock()
	dialed := len(conns)
	mu.Unlock()
	if dialed > maxStreams {
		t.Fatalf("the target was dialed %d times, want at most %d", dialed, maxStreams)
	}

	// Closing one stream makes room for another.
	fc.write(frameClose, 2, nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		id += 2
		fc.write(frameOpen, id, []byte(target))
		typ, gotID, payload := fc.readStream(id)
		if typ == frameOpenOK && gotID == id {
			break
		}
		if typ != frameOpenErr || gotID != id {
			t.Fatalf("got frame %d for stream %d (%q), want OpenOK or OpenErr for stream %d", typ, gotID, payload, id)
		}
		if time.Now().After(deadline) {
			t.Fatalf("an Open is still refused after a stream closed: %q", payload)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStreamQueueIsBoundedInBytes(t *testing.T) {
	t.Parallel()
	m := newMux(nil, io.Discard, true)
	t.Cleanup(m.closeAll)
	local, target := net.Pipe()
	t.Cleanup(func() { _ = target.Close() })
	st, err := m.registerStream(2, local, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.pumpStream(ctx, st)

	// The target reads nothing, so the writer blocks on the first frame,
	// the second fills the queue's byte budget and the third must wait.
	enqueued := make(chan struct{}, 4)
	go func() {
		for i := 0; i < 4; i++ {
			m.enqueue(st, frameMsg{typ: frameData, payload: make([]byte, maxPayloadSize)})
			enqueued <- struct{}{}
		}
	}()
	waitN := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			select {
			case <-enqueued:
			case <-time.After(5 * time.Second):
				t.Fatalf("only %d of %d frames were enqueued", i, n)
			}
		}
		select {
		case <-enqueued:
			t.Fatal("a frame was enqueued past the stream's byte budget")
		case <-time.After(200 * time.Millisecond):
		}
	}
	waitN(2)
	if q := st.queued.Load(); q > maxQueuedBytes {
		t.Fatalf("queued %d bytes, want at most %d", q, maxQueuedBytes)
	}
	// Draining one frame at the target admits exactly one more.
	if _, err := io.ReadFull(target, make([]byte, maxPayloadSize)); err != nil {
		t.Fatal(err)
	}
	waitN(1)
}

func TestSequentialCleanEOFReleasesStreamsBeyondMaxStreams(t *testing.T) {
	t.Parallel()

	// Target TCP server that reads until EOF, writes a reply, and closes
	// cleanly so every forwarded connection terminates via bidirectional
	// CloseWrite (io.EOF) rather than frameClose.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				data, _ := io.ReadAll(c)
				_, _ = c.Write(data)
			}(conn)
		}
	}()

	targetPort := ln.Addr().(*net.TCPAddr).Port
	guestBindPort := freeTCPPort(t)
	remoteSpec := Spec{
		Direction:  RemoteToLocal,
		BindHost:   "127.0.0.1",
		BindPort:   guestBindPort,
		TargetHost: "127.0.0.1",
		TargetPort: targetPort,
	}

	c2gR, c2gW := io.Pipe()
	g2cR, g2cW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = c2gW.Close()
		_ = g2cW.Close()
	})

	readyCh := make(chan struct{})
	go func() {
		_ = ServeGuest(ctx, c2gR, g2cW, []Spec{remoteSpec})
	}()
	go func() {
		_ = RunClient(ctx, g2cR, c2gW, []Spec{remoteSpec}, func() {
			close(readyCh)
		})
	}()

	select {
	case <-readyCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for forward session ready")
	}

	guestAddr := fmt.Sprintf("127.0.0.1:%d", guestBindPort)
	const totalConns = maxStreams + 64
	for i := 0; i < totalConns; i++ {
		conn, err := net.DialTimeout("tcp", guestAddr, 5*time.Second)
		if err != nil {
			t.Fatalf("conn %d: Dial: %v", i, err)
		}
		msg := fmt.Sprintf("hello-%d", i)
		if _, err := conn.Write([]byte(msg)); err != nil {
			_ = conn.Close()
			t.Fatalf("conn %d: Write: %v", i, err)
		}
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			if err := cw.CloseWrite(); err != nil {
				_ = conn.Close()
				t.Fatalf("conn %d: CloseWrite: %v", i, err)
			}
		}
		got, err := io.ReadAll(conn)
		_ = conn.Close()
		if err != nil {
			t.Fatalf("conn %d: ReadAll: %v", i, err)
		}
		if string(got) != msg {
			t.Fatalf("conn %d: got %q, want %q", i, got, msg)
		}
	}
}
