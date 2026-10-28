// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command grain-connect is the guest half of grain's dial into a sandbox.
//
// Grain has no route to a sandbox's guest -- see pkg/mcp/exec_conn.go and
// docs/task-lifecycle-sandbox.md's "Transport" -- so a server inside the
// guest is reached by exec'ing this there and treating the exec's stdio as
// a connection. It copies stdin to the address and the address back to
// stdout, and that is the whole of it: one connection per invocation, no
// framing, no protocol of its own.
//
// It is grain's, installed in the stock sandbox guest by the repository's
// Granulefile, and never something a declaration supplies. It is also provisional: grain has decided to adopt E2B's envd
// as its guest agent (docs/envd-adoption.md), and envd's process API is
// reached over exactly this dial, so the forwarder becomes one more thing
// envd provides. Which side of the socket grain arrives from is not
// something a repository writing a reducer can tell either way.
package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "grain-connect: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string, in io.Reader, out io.Writer) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return errors.New("usage: grain-connect <unix socket path | host:port>")
	}
	address := strings.TrimSpace(args[0])
	network := "tcp"
	if strings.HasPrefix(address, "/") || strings.HasPrefix(address, "@") || strings.HasPrefix(address, "./") || strings.HasSuffix(address, ".sock") {
		network = "unix"
	}
	conn, err := net.Dial(network, address)
	if err != nil {
		return fmt.Errorf("dialing %s %s: %w", network, address, err)
	}
	defer conn.Close()
	return splice(conn, in, out)
}

// splice copies in both directions until both are done.
//
// The half-close is the part worth writing carefully. An HTTP client that
// is finished with a request has not finished with the response, so stdin
// reaching EOF must shut down only the write half of the socket and leave
// the read half to drain -- closing the whole connection there would
// truncate the very answer this was opened to carry.
func splice(conn net.Conn, in io.Reader, out io.Writer) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failure error
	fail := func(err error) {
		if err == nil || errors.Is(err, io.EOF) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if failure == nil {
			failure = err
		}
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := io.Copy(conn, in)
		fail(err)
		if err != nil && !errors.Is(err, io.EOF) {
			_ = conn.Close()
			return
		}
		if half, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		} else {
			_ = conn.Close()
		}
	}()
	go func() {
		defer wg.Done()
		_, err := io.Copy(out, conn)
		fail(err)
		// Nothing more can arrive, so let the other direction stop
		// blocking on a read of a stdin nobody is going to write to.
		_ = conn.Close()
		if closer, ok := in.(io.Closer); ok {
			_ = closer.Close()
		}
	}()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if failure != nil && !errors.Is(failure, net.ErrClosed) && !errors.Is(failure, os.ErrClosed) {
		return failure
	}
	return nil
}
