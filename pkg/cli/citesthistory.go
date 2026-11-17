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

// cicmd_print.go is how `grain ci` (cicmd.go) prints what the daemon
// answered: a run's jobs and steps, a listing of runs, the job cache and a
// run's artifacts, as text or, under -json, as the route's own body.
package cli

// `grain ci test-history` and `grain ci flake-report`: the flags of each;
// the runs, their job attempts and the verdicts are pkg/ci/cli's and
// pkg/ci's, and the printing cicmd_print.go's.

import (
	"context"
	"flag"
	"path"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	cicli "github.com/ai-on-gke/grain/pkg/ci/cli"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// cmdCITestHistory is `grain ci test-history <test>`: how one test has
// fared across a repository's recent runs -- passes, failures, flakes --
// read and rendered by the same ci.QueryTestHistory the in-sandbox
// command's answer comes from. With -workflow, a test that is one of the
// workflow's jobs is judged from every attempt of it, per commit.
//
// The runs, and which CI's they are, are cicli.ProviderTestHistory's.
func cmdCITestHistory(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain ci test-history", flag.ContinueOnError)
	repo := fs.String("repo", "", "only this repository's runs, as owner/name (default: every repository's)")
	limit := fs.Int("limit", 20, "how many of the newest runs to look through (at most 100; with -since or a -ref glob, every run in the window, up to 500)")
	provider := fs.String("provider", "", "only this CI's runs: grain or github-actions (default: github-actions for a -repo whose CI is GitHub Actions, grain otherwise)")
	workflow := fs.String("workflow", "", "only this workflow's runs (nightly.yml); a <test> that is one of its jobs is judged from every attempt of it, per commit")
	ref := fs.String("ref", "", "only runs on this branch (a substring) or, as a glob, on matching branches and tags (v*-rc.*)")
	since := fs.String("since", "", "only runs started since then: a span back from now (24h, 7d) or a date, in place of the newest -limit runs")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := atMostArgs(fs, pos, 1); err != nil {
		return err
	}
	test := strings.TrimSpace(first(pos))
	if test == "" {
		return usageErrorf("a test name is required: %q", "grain ci test-history <test-name>")
	}
	if *limit <= 0 {
		return usageErrorf("-limit must be a positive number of runs, not %d", *limit)
	}
	if p := strings.ToLower(strings.TrimSpace(*provider)); p != "" && p != ci.ProviderGrain && p != ci.ProviderGitHubActions {
		return usageErrorf("-provider must be %s or %s, not %q", ci.ProviderGrain, ci.ProviderGitHubActions, *provider)
	}
	f := ci.RunFilter{Repo: strings.TrimSpace(*repo), Provider: strings.ToLower(strings.TrimSpace(*provider)), Descending: true}
	refGlob, n, err := historyWindow(&f, *workflow, *ref, *since, fs, min(*limit, 100))
	if err != nil {
		return err
	}
	report, err := cicli.ProviderTestHistory(ctx, c, f, n, refGlob, test)
	if err != nil {
		return err
	}
	out.ciTestHistory(report)
	return nil
}

// historyWindow puts -workflow, -ref and -since on f and says how many
// runs to list: limit, or with -since or a -ref glob every run in the
// window up to ci.MaxListedRuns (unless -limit was given). A -ref glob
// is returned apart, since the listing's own Ref is a substring.
func historyWindow(f *ci.RunFilter, workflow, ref, since string, fs *flag.FlagSet, limit int) (string, int, error) {
	f.Workflow = strings.TrimSpace(workflow)
	refGlob := ""
	if ref = strings.TrimSpace(ref); ci.IsRefGlob(ref) {
		if _, err := path.Match(ref, ""); err != nil {
			return "", 0, usageErrorf("-ref %q is not a glob: %v", ref, err)
		}
		refGlob = ref
	} else {
		f.Ref = ref
	}
	if since = strings.TrimSpace(since); since != "" {
		at, err := ci.ParseSince(since, time.Now())
		if err != nil {
			return "", 0, usageErrorf("-since: %v", err)
		}
		f.Since = at
	}
	if (since != "" || refGlob != "") && !flagSet(fs, "limit") {
		limit = ci.MaxListedRuns
	}
	return refGlob, limit, nil
}

// flagSet is whether name was given on the command line.
func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// cmdCIFlakeReport is `grain ci flake-report -workflow <file>`: every job
// of one GitHub Actions workflow judged per commit from every attempt --
// passed, passed on retry, confirmed red -- against the flake budget, the
// verdict release qualification reaches on a candidate's Stage 1.
func cmdCIFlakeReport(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain ci flake-report", flag.ContinueOnError)
	repo := fs.String("repo", "", "the repository, as owner/name (required)")
	workflow := fs.String("workflow", "", "the workflow whose jobs to judge, e.g. nightly.yml (required)")
	ref := fs.String("ref", "", "only runs on this branch (a substring) or, as a glob, on matching branches and tags (v*-rc.*)")
	since := fs.String("since", "7d", "only runs started since then: a span back from now (24h, 7d) or a date")
	limit := fs.Int("limit", ci.MaxListedRuns, "at most this many of the newest runs")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := atMostArgs(fs, pos, 0); err != nil {
		return err
	}
	if strings.TrimSpace(*repo) == "" {
		return usageErrorf("-repo is required: %q", "grain ci flake-report -repo owner/name -workflow nightly.yml")
	}
	if strings.TrimSpace(*workflow) == "" {
		return usageErrorf("-workflow is required: %q", "grain ci flake-report -repo owner/name -workflow nightly.yml")
	}
	if *limit <= 0 {
		return usageErrorf("-limit must be a positive number of runs, not %d", *limit)
	}
	f := ci.RunFilter{Repo: strings.TrimSpace(*repo), Provider: ci.ProviderGitHubActions, Descending: true}
	refGlob, _, err := historyWindow(&f, *workflow, *ref, *since, fs, *limit)
	if err != nil {
		return err
	}
	report, err := cicli.ProviderFlakeReport(ctx, c, f, min(*limit, ci.MaxListedRuns), refGlob)
	if err != nil {
		return err
	}
	report.Ref = strings.TrimSpace(*ref)
	out.ciFlakeReport(report)
	return nil
}
