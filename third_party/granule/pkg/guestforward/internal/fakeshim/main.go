// Command fakeshim stands in for a VM container's granule binary in
// guestforward's Docker test: "fakeshim forward-stdio" serves the guest
// end of a forwarding session straight on its stdio, where the real shim
// would relay it over vsock to granule-agent in a guest. What the test
// exercises is everything around that hop -- docker exec's stdio as the
// transport, killing it, reconnecting -- inside a container with no
// network, which is the case forwarding exists for.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/bwsalmon/granule/pkg/guestforward"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: fakeshim sleep | forward-stdio")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "sleep":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		<-ch
	case guestforward.ShimMode:
		conn := struct {
			io.Reader
			io.Writer
		}{os.Stdin, os.Stdout}
		if err := guestforward.ServeAgent(context.Background(), conn, guestforward.AgentOptions{}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "fakeshim: unknown mode %q\n", os.Args[1])
		os.Exit(2)
	}
}
