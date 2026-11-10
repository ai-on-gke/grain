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
	"io"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// cmdPR is `grain pr adopt|review|status <pr>` (external-cli spec #XC-28,
// #XC-29): coordinating grain's CI, reviews and merge queue on a pull
// request somebody else opened and pushes to with their own git login.
// The in-sandbox `grain pr` (pkg/sandboxcli) is a different command about
// the task's own branch.
func cmdPR(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageErrorf("usage: grain pr adopt|review|status|submit <pr-url|owner/name#n> [flags]")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "adopt":
		return cmdPRAdopt(ctx, c, out, args)
	case "review":
		return cmdPRReview(ctx, c, out, args)
	case "status":
		return cmdPRStatus(ctx, c, out, args)
	case "submit":
		return cmdPRSubmit(ctx, c, out, args)
	}
	return usageErrorf("unknown \"grain pr\" subcommand %q: one of adopt, review, status, submit", sub)
}

// prArg is the one pull request a `grain pr` verb names, checked here so
// a typo is a usage error rather than a round trip.
func prArg(fs *flag.FlagSet, pos []string) (string, error) {
	if len(pos) != 1 {
		return "", usageErrorf("%s takes one pull request (a URL or owner/name#n), got %d arguments", fs.Name(), len(pos))
	}
	if _, err := model.ParsePullRequestArg(pos[0]); err != nil {
		return "", usageErrorf("%s: %v", fs.Name(), err)
	}
	return pos[0], nil
}

func cmdPRAdopt(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain pr adopt", flag.ContinueOnError)
	mode := fs.String("mode", string(model.TakeoverReviewOnly),
		"review-only (grain reviews and gates the pull request without pushing) or finish (it also pushes fixes)")
	spec := fs.String("spec", "", "the specification the change implements, as a repository path under design/spec/")
	design := fs.String("design", "", "the design the change implements, as a repository path under design/design/")
	asJSON := fs.Bool("json", false, "print the shepherd task as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	pr, err := prArg(fs, pos)
	if err != nil {
		return err
	}
	if _, err := model.ParseTakeoverMode(*mode); err != nil {
		return usageErrorf("grain pr adopt -mode: %v", err)
	}
	got, err := c.AdoptPullRequest(ctx, ui.AdoptPullRequestRequest{PR: pr, Mode: *mode, Spec: *spec, Design: *design})
	if err != nil {
		return err
	}
	if *asJSON || out.json {
		out.encode(got)
		return nil
	}
	printAdoption(os.Stdout, got)
	return nil
}

func printAdoption(w io.Writer, a model.PullRequestAdoption) {
	verb := "adopted"
	switch {
	case a.Upgraded:
		verb = "upgraded"
	case a.Existing:
		verb = "already holds"
	}
	fmt.Fprintf(w, "task %s %s %s\nstate: %s\nmode:  %s\n", a.TaskID, verb, a.PR, a.State, a.Mode)
	if a.Spec != "" {
		fmt.Fprintf(w, "spec:   %s\n", a.Spec)
	}
	if a.Design != "" {
		fmt.Fprintf(w, "design: %s\n", a.Design)
	}
	if a.State == string(model.StateProposed) {
		fmt.Fprintf(w, "It waits for a person to approve it (/tasks/%s) before grain files anything on the pull request.\n", a.TaskID)
	}
}

func cmdPRReview(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain pr review", flag.ContinueOnError)
	design := fs.Bool("design", false,
		"review the design: design-folder conventions (contributions.spec.md) and overlapping designs under design/")
	spec := fs.String("spec", "", "a specification to check the change's conformance against, as a repository path under design/")
	asJSON := fs.Bool("json", false, "print the review task as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	pr, err := prArg(fs, pos)
	if err != nil {
		return err
	}
	got, err := c.ReviewPullRequest(ctx, ui.ReviewPullRequestRequest{PR: pr, Design: *design, Spec: *spec})
	if err != nil {
		return err
	}
	if *asJSON || out.json {
		out.encode(got)
		return nil
	}
	kind := "review"
	if got.Design {
		kind = "design review"
	}
	fmt.Fprintf(os.Stdout, "task %s: %s of %s (%s)\n", got.TaskID, kind, got.PR, got.State)
	return nil
}

func cmdPRStatus(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain pr status", flag.ContinueOnError)
	threads := fs.Bool("threads", false, "list every inline review thread's file, line and body")
	asJSON := fs.Bool("json", false, "print the status as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	pr, err := prArg(fs, pos)
	if err != nil {
		return err
	}
	got, err := c.PullRequestStatus(ctx, pr, *threads)
	if err != nil {
		return err
	}
	if *asJSON || out.json {
		out.encode(got)
		return nil
	}
	printPRStatus(os.Stdout, got)
	return nil
}

func printPRStatus(w io.Writer, r model.PullRequestReport) {
	fmt.Fprintf(w, "%s", r.PR)
	if r.State != "" {
		fmt.Fprintf(w, " (%s)", r.State)
	}
	fmt.Fprintf(w, "\nhead:        %s\n", orDash(r.Head))
	if r.Shepherd != nil {
		fmt.Fprintf(w, "shepherd:    task %s, %s, %s\n", r.Shepherd.TaskID, r.Shepherd.Mode, r.Shepherd.State)
	} else {
		fmt.Fprintf(w, "shepherd:    none (grain pr adopt %s)\n", r.PR)
	}
	if r.CI != nil {
		verdict := r.CI.Conclusion
		if verdict == "" {
			verdict = r.CI.Status
		}
		fmt.Fprintf(w, "ci:          %s %s", r.CI.RunID, verdict)
		if len(r.CI.FailingJobs) > 0 {
			fmt.Fprintf(w, " (failing: %s)", strings.Join(r.CI.FailingJobs, ", "))
		}
		fmt.Fprintln(w)
	} else {
		fmt.Fprintln(w, "ci:          no grain CI run")
	}
	fmt.Fprintf(w, "merge queue: %s\n", r.MergeQueue)
	if len(r.Gates) > 0 {
		fmt.Fprintln(w, "gates:")
		for _, g := range r.Gates {
			detail := g.Detail
			if detail == "" {
				detail = string(g.State)
			}
			fmt.Fprintf(w, "  %-16s  %-10s  %s\n", g.ID, g.State, detail)
		}
	}
	if len(r.Reviews) == 0 {
		fmt.Fprintln(w, "reviews:     none")
	} else {
		fmt.Fprintln(w, "reviews:")
		for _, rv := range r.Reviews {
			fmt.Fprintf(w, "  task %s  %-8s  %-8s  %d findings, %d unresolved threads\n",
				rv.TaskID, rv.Kind, rv.State, rv.Findings, rv.UnresolvedThreads)
		}
	}
	if len(r.Threads) > 0 {
		fmt.Fprintln(w, "threads:")
		for _, th := range r.Threads {
			state := "open"
			if th.Resolved {
				state = "resolved"
			}
			loc := th.Path
			if th.Line > 0 {
				loc = fmt.Sprintf("%s:%d", th.Path, th.Line)
			}
			fmt.Fprintf(w, "  %s [%s]\n", loc, state)
			for _, line := range strings.Split(strings.TrimSpace(th.Body), "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func cmdPRSubmit(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain pr submit", flag.ContinueOnError)
	overrideGates := fs.String("override-gates", "",
		"comma-separated unsatisfied gates to override under merge:force (e.g. security-review, review, ci); never implied")
	reason := fs.String("reason", "", "required reason when -override-gates is set, recorded on the task and pull request")
	head := fs.String("head", "", "the head commit SHA reviewed when overriding gates")
	asJSON := fs.Bool("json", false, "print the submit result as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	pr, err := prArg(fs, pos)
	if err != nil {
		return err
	}
	var gates []string
	if strings.TrimSpace(*overrideGates) != "" {
		gates, err = model.NormalizeOverrideGates([]string{*overrideGates})
		if err != nil {
			return usageErrorf("grain pr submit -override-gates: %v", err)
		}
		if strings.TrimSpace(*reason) == "" {
			return usageErrorf("grain pr submit: -override-gates requires -reason explaining why the gate is skipped")
		}
	} else if strings.TrimSpace(*reason) != "" {
		return usageErrorf("grain pr submit: -reason requires -override-gates naming each skipped gate explicitly")
	}
	got, err := c.SubmitPullRequest(ctx, ui.SubmitPullRequestRequest{
		PR: pr, OverrideGates: gates, Reason: *reason, Head: *head,
	})
	if err != nil {
		return err
	}
	if *asJSON || out.json {
		out.encode(got)
		return nil
	}
	printPRSubmit(os.Stdout, got)
	return nil
}

func printPRSubmit(w io.Writer, r ui.SubmitPullRequestResult) {
	if len(r.Overridden) > 0 {
		fmt.Fprintf(w, "%s (task %s): overrode gate(s) %s\n", r.PR, r.TaskID, strings.Join(r.Overridden, ", "))
	} else {
		fmt.Fprintf(w, "%s (task %s): approval recorded (merges once all required gates pass)\n", r.PR, r.TaskID)
	}
	fmt.Fprintf(w, "state: %s\n", r.State)
	if len(r.Gates) > 0 {
		fmt.Fprintln(w, "gates:")
		for _, g := range r.Gates {
			detail := g.Detail
			if detail == "" {
				detail = string(g.State)
			}
			fmt.Fprintf(w, "  %-16s  %-10s  %s\n", g.ID, g.State, detail)
		}
	}
}
