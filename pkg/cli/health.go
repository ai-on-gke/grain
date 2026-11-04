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

// health.go is "grain health": the counters a layer built on grain keeps
// in the daemon (ui.RegisterHealthCounters), read off GET /api/sandboxes
// -- the same response System → Health and an agent's `grain health`
// read. -json prints that whole response.

package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func cmdHealth(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain health", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	health, err := c.SandboxHealth(ctx)
	if err != nil {
		return err
	}
	out.health(health)
	return nil
}

func (p *printer) health(h ui.SandboxHealthResponse) {
	if p.json {
		p.encode(h)
		return
	}
	if len(h.Counters) == 0 {
		fmt.Println("no counters: nothing on this daemon registers any")
		return
	}
	fmt.Println("counters since the daemon started:")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, section := range h.Counters {
		fmt.Fprintf(w, "\n%s\n", section.Name)
		if len(section.Counters) == 0 {
			fmt.Fprintln(w, "  (none yet)")
			continue
		}
		keys := make([]string, 0, len(section.Counters))
		for k := range section.Counters {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "  %s\t%d\n", k, section.Counters[k])
		}
	}
	_ = w.Flush()
}
