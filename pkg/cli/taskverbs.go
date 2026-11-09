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
	"strings"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func cmdClose(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain close", flag.ContinueOnError)
	// The same choice the UI puts in a checkbox beside its own Close
	// button, and the same default: off. A task's pull request is real
	// work, so closing it happens because somebody typed this and not
	// because they closed a task (ui.CloseOptions).
	closePR := fs.Bool("close-pull-request", false,
		"close the task's pull request on GitHub too, without merging it "+
			"(the branch and its commits are left untouched)")
	reason := fs.String("reason", "", "why, kept with the close in the task's history")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := atMostArgs(fs, rest, 1); err != nil {
		return err
	}
	id, err := taskID(first(rest))
	if err != nil {
		return err
	}
	if err := c.Close(ctx, id, ui.CloseOptions{ClosePullRequest: *closePR, Reason: *reason}); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

func cmdReopen(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain reopen", flag.ContinueOnError)
	reason := fs.String("reason", "", "why, kept with the reopen in the task's history")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := atMostArgs(fs, rest, 1); err != nil {
		return err
	}
	id, err := taskID(first(rest))
	if err != nil {
		return err
	}
	if err := c.ReopenBecause(ctx, id, *reason); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

// cmdRetry is `grain retry <id>`: the only way to clear a task's own
// failure streak once it has reached model.MaxConsecutiveFailures and
// state has stopped calling it "queued" at all (bwsalmon/agents#403).
func cmdRetry(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain retry", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	if err := c.Retry(ctx, id); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

// cmdRetryMerge is `grain retry-merge <id>`: the shell's half of the
// UI's own "Retry merge" button, and the only way to ask the merge queue
// for another attempt at a pull request it has already given up on (see
// ui.Client.RetryMerge).
func cmdRetryMerge(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain retry-merge", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	if err := c.RetryMerge(ctx, id); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

// cmdForceMerge is `grain force-merge <id> <reason>`: the shell's half
// of the UI's "Force merge" button, and the one sanctioned exception to
// the merge predicate -- grain lands the change itself, on an operator's
// say-so, with the reason recorded on the task and on the pull request
// (see ui.Client.ForceMerge).
//
// The reason is an argument rather than a flag because it is not
// optional: a force merge without one is refused at the other end, and a
// required flag reads like something that could be left out.
//
// -head names the commit that was reviewed, and the override is for that
// commit only: grain refuses it if the branch is somewhere else, and
// withdraws it if anything is pushed before the merge. Without it the
// override is for the pull request's head as it is when grain receives
// the request.
func cmdForceMerge(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain force-merge", flag.ContinueOnError)
	head := fs.String("head", "", "the head commit you reviewed; the override is for it alone")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 2); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	reason := strings.TrimSpace(strings.Join(fs.Args()[1:], " "))
	if reason == "" {
		return fmt.Errorf("say why: grain force-merge <id> \"the release is broken and this is the fix\"")
	}
	if err := c.ForceMerge(ctx, id, reason, *head); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

// cmdRunNow is `grain run-now <id>`: the shell's half of the UI's "Run
// now" button. It jumps the queue and ignores the state machine -- a
// blocked, deferred, failed or already-finished task is started all the
// same -- which is the only way to hand a task whose work is over, its
// pull request sitting on the merge queue, back to an agent for more of
// it. See ui.Client.RunNow.
func cmdRunNow(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain run-now", flag.ContinueOnError)
	reason := fs.String("reason", "", "why: sent as X-Grain-Acting-Reason, which an external agent credential (gma_) must give when run-now approves an unapproved task")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	c.ActingReason = *reason
	if err := c.RunNow(ctx, id); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}
