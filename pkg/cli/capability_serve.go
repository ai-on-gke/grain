package cli

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/bwsalmon/grain/pkg/capability/intree"
	"github.com/bwsalmon/grain/pkg/capability/protocol"
)

// servesCapability reports whether `grain capability ...` names an in-tree
// capability server to serve rather than the task command `grain
// capability <id> <capability-id> attach|detach`: its first argument is a
// server's name, and it is not that command's three-argument shape.
func servesCapability(args []string) bool {
	if len(args) < 1 {
		return false
	}
	if _, ok := intree.Lookup(args[0]); !ok {
		return false
	}
	if len(args) == 3 && (args[2] == "attach" || args[2] == "detach") {
		return false
	}
	return true
}

// capabilityServeCmd is `grain capability <name> [flags]`: serve one
// in-tree capability server's control plane on stdin/stdout, the stdio
// transport an mcpServers entry spawns it over (docs/capabilities-spec.md
// §3.1.1). Root credentials come from the environment alone; diagnostics
// go to stderr, since stdout is the protocol.
func capabilityServeCmd(args []string) {
	if err := protocol.DropCapabilities(); err != nil {
		fmt.Fprintln(os.Stderr, "grain capability: dropping capabilities: "+err.Error())
		os.Exit(1)
	}
	if err := serveCapability(context.Background(), args, os.Getenv, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "grain capability: "+err.Error())
		os.Exit(1)
	}
}

func serveCapability(ctx context.Context, args []string, env intree.Env, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: grain capability <%s> [flags]", strings.Join(intree.Names(), "|"))
	}
	b, ok := intree.Lookup(args[0])
	if !ok {
		return fmt.Errorf("no in-tree capability %q; there are: %s", args[0], strings.Join(intree.Names(), ", "))
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := log.New(errOut, "", log.LstdFlags)
	s, err := b.Build(ctx, args[1:], env, logger)
	if err != nil {
		return err
	}
	return s.Serve(ctx, in, out)
}
