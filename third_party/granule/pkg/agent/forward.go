package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"golang.org/x/sys/unix"

	"github.com/bwsalmon/granule/pkg/execwire"
	"github.com/bwsalmon/granule/pkg/forward"
	"github.com/bwsalmon/granule/pkg/guestforward"
)

// serveForward answers a request that asked for a forwarding session
// rather than a command: it confirms, then serves pkg/guestforward's
// guest end on the rest of the connection until the caller hangs up.
//
// Forwarding runs as the agent itself, which is root, and needs no
// account: what it does -- bind a loopback port, dial an address -- is
// exactly what "granule exec" could already have any process in the
// guest do, so a caller trusted with exec is trusted with this. It never
// touches the guest's interfaces or routes.
func serveForward(ctx context.Context, br *bufio.Reader, conn io.ReadWriter, req execwire.Request) error {
	if req.Line != "" || req.User != "" || req.TTY || req.Dir != "" || len(req.Env) > 0 || req.Container || len(req.Argv) > 0 || req.Term != "" || req.Cols != 0 || req.Rows != 0 {
		_ = execwire.WriteResponse(conn, execwire.Response{OK: false, Error: "a forward request carries no command, account, terminal or environment", Features: execwire.Features})
		return nil
	}
	if len(req.ForwardSpecs) > 0 {
		return serveLegacyForward(ctx, br, conn, req)
	}
	if err := execwire.WriteResponse(conn, execwire.Response{OK: true, Features: execwire.Features}); err != nil {
		return err
	}
	// The request line was read through br, which may already hold the
	// first frames behind it, so reads go on through br.
	return guestforward.ServeAgent(ctx, forwardConn{Reader: br, conn: conn}, guestforward.AgentOptions{
		Logf: func(format string, args ...any) { log.Printf("forward: "+format, args...) },
	})
}

func serveLegacyForward(ctx context.Context, br *bufio.Reader, conn io.Writer, req execwire.Request) error {
	var specs []forward.Spec
	for _, raw := range req.ForwardSpecs {
		spec, err := forward.ParseSpec(forward.RemoteToLocal, raw)
		if err != nil {
			_ = execwire.WriteResponse(conn, execwire.Response{OK: false, Error: err.Error(), Features: execwire.Features})
			return nil
		}
		specs = append(specs, spec)
	}
	gl, err := forward.BindGuestListeners(specs)
	if err != nil {
		_ = execwire.WriteResponse(conn, execwire.Response{OK: false, Error: err.Error(), Features: execwire.Features})
		return nil
	}
	defer gl.Close()
	if err := execwire.WriteResponse(conn, execwire.Response{OK: true, Features: execwire.Features}); err != nil {
		return fmt.Errorf("writing the forward response: %w", err)
	}
	return gl.Serve(ctx, br, conn)
}

// forwardConn is the connection with reads going through the buffered
// reader the request was read with, and a Close that actually ends a
// read in progress.
type forwardConn struct {
	io.Reader
	conn io.ReadWriter
}

func (c forwardConn) Write(p []byte) (int, error) { return c.conn.Write(p) }

// Close shuts the socket down rather than only closing it. The agent's
// vsock descriptors are blocking (see cmd/granule-agent's accept), and
// closing a blocking descriptor does not wake a thread already inside
// read(2) on it, so a session ended from this side -- a keepalive
// timeout, say -- would otherwise leave its reader parked until the far
// end happened to send something. shutdown(2) does wake it. The
// descriptor itself is still closed by whoever owns it.
func (c forwardConn) Close() error {
	f, ok := c.conn.(*os.File)
	if !ok {
		if cl, ok := c.conn.(io.Closer); ok {
			return cl.Close()
		}
		return nil
	}
	sc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	return sc.Control(func(fd uintptr) { _ = unix.Shutdown(int(fd), unix.SHUT_RDWR) })
}
