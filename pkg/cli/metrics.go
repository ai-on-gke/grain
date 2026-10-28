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

// metrics.go is "grain metrics": the throughput and latency report
// (pkg/metrics, served as GET /api/metrics) printed at a terminal --
// including the daemon's own reconcile tick, which is what says whether
// a queue wait in that report was contention or scheduling.
//
// It is a task verb like every other one in main.go rather than a mode of
// its own -- it asks a running daemon over REST, holds no store, and
// takes -server and -json from the same global flags -- so `grain metrics
// -json | jq` is how a number here gets into anything else.
//
// It asks the same question the endpoint takes, and no smaller one: a
// span said either as a length ending now (-window) or as actual dates
// (-since/-until), and a scope (-repo, -author) narrowing which tasks
// the numbers are about. The CLI is how a report reaches a pull request
// comment or a weekly note, and "how did acme/widgets go last month" is
// a question asked there more often than anywhere else.
package cli

import (
	"context"
	"flag"
	"os"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// cmdMetrics asks for a report over a span and a scope: -window (or
// -since/-until) is which measurements, and -repo/-author is which tasks
// they are about.
//
// The two scope flags are repeatable rather than comma-separated, the
// same choice -capability and -read make (stringList in main.go): the
// endpoint takes them that way already, one ?repo= per value, and a
// repeated flag needs no rule about what a value containing a comma
// means. Each is an "any of", matching what a report board's own query
// does with the same attributes.
func cmdMetrics(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain metrics", flag.ContinueOnError)
	window := fs.String("window", "", "how far back to measure: a duration like 36h, or a count of days or weeks like 7d (default 7d)")
	since := fs.String("since", "", "measure from this moment instead of a window: a date like 2026-01-31, or an RFC 3339 timestamp (a bare date is midnight UTC)")
	until := fs.String("until", "", "measure up to this moment rather than up to now")
	var repos, authors stringList
	fs.Var(&repos, "repo", "only tasks whose work lands in this repo, owner/name (repeatable -- any of them)")
	fs.Var(&authors, "author", "only tasks attributed to this author (repeatable -- any of them)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	req, err := daemon.MetricsRequest(*window, *since, *until, repos, authors)
	if err != nil {
		return err
	}
	report, err := c.Metrics(ctx, req)
	if err != nil {
		return err
	}
	out.metrics(report)
	return nil
}

func (p *printer) metrics(rep ui.MetricsReport) {
	if p.json {
		p.encode(rep)
		return
	}
	daemon.RenderMetrics(os.Stdout, rep, "")
}
