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

// drainprobe: §11 item 2. Run as the host's user while daemon runs are in
// flight on its grain-agentd. It starts one more CLI attached over the
// local socket (the third run; the daemon's are detached), lists what
// the host holds, asks for a plain drain (OpDrain without Idle, what
// `grain-agentd -drain` sends a host it cannot cut over), tries a start
// straight after the answer, then prints the attached run's Exit frame
// when it arrives and how long the host lingered after it.
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	t0 := time.Now()
	stamp := func() string { return fmt.Sprintf("[+%5.1fs]", time.Since(t0).Seconds()) }

	type result struct {
		exit hostd.Exit
		err  error
	}
	attached := make(chan result, 1)
	go func() {
		var out, errb bytes.Buffer
		ex, err := c.Run(ctx, hostd.Request{Op: hostd.OpStart, Kind: hostd.KindCLI, Name: "claude",
			Args: []string{"attached-run"}}, nil, &out, &errb)
		attached <- result{ex, err}
	}()
	time.Sleep(time.Second)
	ps, err := c.Processes(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "processes:", err)
		os.Exit(1)
	}
	for _, p := range ps {
		fmt.Printf("%s in flight: %s %s pid %d run %q detached=%v\n", stamp(), p.Kind, p.Name, p.PID, p.Run, p.Detached)
	}
	if err := c.Drain(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "drain:", err)
		os.Exit(1)
	}
	fmt.Printf("%s drain answered\n", stamp())
	var out, errb bytes.Buffer
	_, err = c.Run(ctx, hostd.Request{Op: hostd.OpStart, Kind: hostd.KindCLI, Name: "claude",
		Args: []string{"after-drain"}}, nil, &out, &errb)
	fmt.Printf("%s start right after the drain: draining=%v err=%v\n", stamp(), hostd.IsDraining(err), err)
	if h, err := c.Hello(ctx); err == nil {
		fmt.Printf("%s hello while draining: draining=%v\n", stamp(), h.Draining)
	}
	r := <-attached
	fmt.Printf("%s attached run's Exit: code=%d signal=%q draining=%v error=%q err=%v\n",
		stamp(), r.exit.Code, r.exit.Signal, r.exit.Draining, r.exit.Error, r.err)
	for {
		hctx, hcancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := c.Hello(hctx)
		hcancel()
		if err != nil {
			fmt.Printf("%s grain-agentd no longer answers on its socket\n", stamp())
			return
		}
		select {
		case <-ctx.Done():
			fmt.Println("timed out waiting for grain-agentd to exit")
			os.Exit(1)
		case <-time.After(500 * time.Millisecond):
		}
	}
}
