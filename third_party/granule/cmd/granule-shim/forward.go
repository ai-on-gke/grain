package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bwsalmon/granule/pkg/execwire"
	"github.com/bwsalmon/granule/pkg/guestexec"
)

const forwardStdioUsage = `usage: granule forward-stdio

Relays a port-forwarding session between this process's stdin/stdout and
the guest's granule-agent. It is the far end of "granule forward" and of
pkg/guestforward's DockerExec and KubectlExec transports, which run it as

  docker exec -i <container> granule forward-stdio
  kubectl exec -i <pod> -c <container> -- granule forward-stdio

and speak the forwarding protocol over its stdio; it is not meant to be
run by hand. It exits when either side hangs up.`

// runForwardStdio connects to the guest's agent, asks it for a
// forwarding session (execwire.Request.Forward), and then copies bytes
// between this process's stdio and that connection, uninterpreted: the
// multiplexed protocol on top (pkg/guestforward) is spoken end to end by
// the caller and the agent, so flow control and keepalive are theirs and
// this is a pipe. That is also why it is identical under Docker and
// Kubernetes -- all either runtime contributes is the exec stream.
//
// Nothing here touches the guest's network. A forwarded port is a
// listener the agent holds on the guest's loopback, reached over vsock.
func runForwardStdio(args []string) error {
	for _, a := range args {
		if a == "-h" || a == "-help" || a == "--help" {
			fmt.Println(forwardStdioUsage)
			return nil
		}
	}
	if len(args) > 0 {
		return fmt.Errorf("takes no arguments, got %q\n%s", strings.Join(args, " "), forwardStdioUsage)
	}
	cfg, err := guestexec.FromEnv()
	if err != nil {
		return err
	}

	ctx, cancel := signalledContext()
	defer cancel()

	conn, err := guestexec.Dial(ctx, cfg)
	if err != nil {
		return fmt.Errorf("the guest is not reachable (is the VM up?): %w", err)
	}
	defer conn.Close()

	br, err := openForward(conn)
	if err != nil {
		return err
	}
	return relay(ctx, os.Stdin, os.Stdout, br, conn)
}

// openForward asks the agent on conn for a forwarding session and
// returns the reader the rest of the session is to be read through.
func openForward(conn io.ReadWriter) (*bufio.Reader, error) {
	req := execwire.Request{Forward: true}
	if err := execwire.WriteRequest(conn, req); err != nil {
		return nil, fmt.Errorf("asking the guest for a forwarding session: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := execwire.ReadResponse(br)
	if err != nil {
		return nil, fmt.Errorf("reading the guest's answer: %w", err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("the guest refused a forwarding session: %s", resp.Error)
	}
	if missing := resp.MissingFeatures(req); len(missing) > 0 {
		return nil, fmt.Errorf("the guest's granule-agent does not support %s; rebase the guest image onto this granule (granule guest rebase)", strings.Join(missing, ", "))
	}
	return br, nil
}

// relay copies in to the guest and the guest to out until either
// direction ends, which ends the session: stdin closing is the caller
// going away, and the guest's side closing is the agent ending it.
// Returning closes the connection (the caller's defer), which is what
// tells the agent to drop every mapping the session held.
func relay(ctx context.Context, in io.Reader, out io.Writer, fromGuest io.Reader, toGuest io.Writer) error {
	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(toGuest, in)
		errc <- err
	}()
	go func() {
		_, err := io.Copy(out, fromGuest)
		errc <- err
	}()
	select {
	case err := <-errc:
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		return nil
	}
}
