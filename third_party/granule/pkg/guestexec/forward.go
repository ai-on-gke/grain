package guestexec

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/bwsalmon/granule/pkg/execwire"
	"github.com/bwsalmon/granule/pkg/forward"
)

// ForwardUsage is the synopsis for "granule forward" inside the VM container.
const ForwardUsage = `usage: granule forward [-L [bind:]port:host:port]... [-R [bind:]port:host:port]...

Bridges a multiplexed TCP port-forwarding session between this container's
stdin/stdout (e.g. a docker exec -i or kubectl exec -i stream) and the guest
agent over vsock:

  kubectl exec -i <pod> -c sandbox -- granule forward -R 127.0.0.1:9728:127.0.0.1:9728`

// ForwardOptions holds the parsed flags for "granule forward" in the container shim.
type ForwardOptions struct {
	Locals  []string
	Remotes []string
}

// ParseForwardArgs parses the arguments after "granule forward".
func ParseForwardArgs(args []string) (ForwardOptions, error) {
	fs := flag.NewFlagSet("granule forward", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var opts ForwardOptions
	fs.Var((*repeatedFlag)(&opts.Locals), "L", "forward local (host) port to guest port")
	fs.Var((*repeatedFlag)(&opts.Remotes), "R", "forward guest port to local (host) port")
	if err := fs.Parse(args); err != nil {
		return ForwardOptions{}, fmt.Errorf("%w\n\n%s", err, ForwardUsage)
	}
	if len(fs.Args()) > 0 {
		return ForwardOptions{}, fmt.Errorf("unexpected arguments %v\n\n%s", fs.Args(), ForwardUsage)
	}
	for _, raw := range opts.Locals {
		if _, err := forward.ParseSpec(forward.LocalToRemote, raw); err != nil {
			return ForwardOptions{}, fmt.Errorf("-L %s: %w", raw, err)
		}
	}
	for _, raw := range opts.Remotes {
		if _, err := forward.ParseSpec(forward.RemoteToLocal, raw); err != nil {
			return ForwardOptions{}, fmt.Errorf("-R %s: %w", raw, err)
		}
	}
	if len(opts.Locals) == 0 && len(opts.Remotes) == 0 {
		return ForwardOptions{}, fmt.Errorf("granule forward requires at least one -L or -R specification\n\n%s", ForwardUsage)
	}
	return opts, nil
}

// Forward connects to the guest agent over vsock, requests a port-forwarding
// session with the given guest-side (-R) specs, and bridges stdin/stdout with
// the vsock stream until EOF or ctx cancellation.
func Forward(ctx context.Context, cfg Config, remotes []string, stdin io.Reader, stdout io.Writer) error {
	if cfg.Socket == "" {
		cfg.Socket = DefaultSocket
	}
	conn, err := dialWithRetry(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()

	req := execwire.Request{
		Forward:      true,
		ForwardSpecs: remotes,
	}
	if err := execwire.WriteRequest(conn, req); err != nil {
		return fmt.Errorf("sending the forward request: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := execwire.ReadResponse(br)
	if err != nil {
		return fmt.Errorf("reading the guest's reply: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("guest refused forward session: %s", resp.Error)
	}
	if missing := resp.MissingFeatures(req); len(missing) > 0 {
		return fmt.Errorf("guest agent does not support %v", missing)
	}

	errCh := make(chan error, 2)
	go func() {
		_, err := io.Copy(conn, stdin)
		_ = conn.Close()
		errCh <- err
	}()
	go func() {
		_, err := io.Copy(stdout, br)
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		_ = conn.Close()
		return nil
	case <-errCh:
		_ = conn.Close()
		return nil
	}
}
