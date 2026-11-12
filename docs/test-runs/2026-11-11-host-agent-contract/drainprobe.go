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

//go:build ignore

// drainprobe: §9 item 5. Run as the host's user while a run is in flight
// on its grain-agentd: it lists the running processes, asks for a plain
// drain over the local socket, starts a CLI straight after the answer
// (which must be refused as draining), and then waits for grain-agentd to
// exit, reporting how long the drain took.
//
//	go build -o /var/tmp/hac/drainprobe docs/test-runs/2026-11-11-host-agent-contract/drainprobe.go
//	sudo -u walkhost /var/tmp/hac/drainprobe -socket /run/user-walkhost/grain-agentd/agentd.sock
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

func main() {
	socket := flag.String("socket", "", "grain-agentd's local socket")
	flag.Parse()
	c := hostd.Client{Socket: *socket, Build: "drainprobe"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ps, err := c.Processes(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "processes:", err)
		os.Exit(1)
	}
	for _, p := range ps {
		fmt.Printf("in flight: %s %s pid %d run %s detached=%v\n", p.Kind, p.Name, p.PID, p.Run, p.Detached)
	}
	t0 := time.Now()
	if err := c.Drain(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "drain:", err)
		os.Exit(1)
	}
	fmt.Printf("drain answered after %s\n", time.Since(t0).Round(time.Millisecond))
	var out, errb bytes.Buffer
	_, err = c.Run(ctx, hostd.Request{Op: hostd.OpStart, Kind: hostd.KindCLI, Name: "claude",
		Args: []string{"--version"}}, nil, &out, &errb)
	fmt.Printf("start right after the drain: err=%v draining=%v\n", err, hostd.IsDraining(err))
	if h, err := c.Hello(ctx); err == nil {
		fmt.Printf("hello while draining: draining=%v\n", h.Draining)
	}
	for {
		if _, err := c.Hello(ctx); err != nil {
			fmt.Printf("grain-agentd gone after %s (%v)\n", time.Since(t0).Round(time.Millisecond), err)
			return
		}
		select {
		case <-ctx.Done():
			fmt.Println("timed out waiting for grain-agentd to exit")
			os.Exit(1)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
