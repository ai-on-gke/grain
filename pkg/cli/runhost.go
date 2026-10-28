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

package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/runhost"
)

// runHostCmd is "grain runhost exec": a run host's entrypoint
// (pkg/runhost). It runs the invocation the daemon spooled, appends its
// output to the spool and records how it ended, and nothing else -- it
// never opens the store and never calls the daemon.
func runHostCmd(args []string) {
	if len(args) == 0 || args[0] != "exec" {
		fmt.Fprintln(os.Stderr, "usage: grain runhost exec -spool DIR [-deadline RFC3339]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("runhost exec", flag.ExitOnError)
	spool := fs.String("spool", "", "the run host's spool directory")
	deadline := fs.String("deadline", "", "the hard ceiling this run host enforces on itself (RFC 3339)")
	_ = fs.Parse(args[1:])
	if *spool == "" {
		fmt.Fprintln(os.Stderr, "grain runhost exec: -spool is required")
		os.Exit(2)
	}
	var until time.Time
	if *deadline != "" {
		t, err := time.Parse(time.RFC3339, *deadline)
		if err != nil {
			fmt.Fprintf(os.Stderr, "grain runhost exec: -deadline: %v\n", err)
			os.Exit(2)
		}
		until = t
	}
	// docker stop and docker rm -f signal the run host; the CLI's process
	// group goes with it, and exit.json still says so.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := runhost.Exec(ctx, *spool, until); err != nil {
		fmt.Fprintln(os.Stderr, "grain runhost exec: "+err.Error())
		os.Exit(1)
	}
}
