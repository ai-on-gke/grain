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

// Command reviewed-merge is the release build's gate: it refuses a commit
// that did not reach a release branch through a reviewed merge, and
// otherwise prints the record of how it did (pkg/reviewedmerge), which
// scripts/release-images.sh attests onto the images it publishes.
//
//	reviewed-merge -repo owner/name -commit <sha> [-since <sha>] \
//	  [-branches main,release/v*] [-mergers grain-login,...]
//
// -since is the commit already checked -- what the branch's published
// image was built from -- and every first-parent commit after it up to
// -commit is checked too. The forge is GitHub's REST API at
// $GITHUB_API_URL (https://api.github.com by default), read with
// $GITHUB_TOKEN when it is set.
//
// Exit 0 with the record on stdout; 1, with the reason on stderr, for a
// commit that is refused; 3, with the forge's error on stderr, for one
// whose records could not be read even after retrying, which decides
// nothing about it; 2 for a usage error.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/reviewedmerge"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv, nil))
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string, forge reviewedmerge.Forge) int {
	fs := flag.NewFlagSet("reviewed-merge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "the repository, owner/name")
	commit := fs.String("commit", "", "the commit to be released (a full sha)")
	since := fs.String("since", "", "the commit already checked; every first-parent commit after it is checked too")
	branches := fs.String("branches", "main,release/v*", "the release branches, comma-separated path patterns")
	mergers := fs.String("mergers", "", "the forge logins grain merges as, comma-separated")
	max := fs.Int("max", 1000, "the most first-parent commits to walk")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *repo == "" || *commit == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "reviewed-merge: -repo and -commit are required")
		return 2
	}
	if forge == nil {
		forge = reviewedmerge.REST{BaseURL: getenv("GITHUB_API_URL"), Token: getenv("GITHUB_TOKEN")}
	}
	policy := reviewedmerge.Policy{Repo: *repo, Branches: splitList(*branches), Mergers: splitList(*mergers)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	rec, err := policy.CheckChain(ctx, forge, *commit, *since, *max)
	if reviewedmerge.IsForgeError(err) {
		fmt.Fprintf(stderr, "reviewed-merge: forge error, not a policy refusal: %v\n", err)
		return 3
	}
	if err != nil {
		fmt.Fprintf(stderr, "reviewed-merge: refused: %v\n", err)
		if len(policy.Mergers) == 0 {
			fmt.Fprintln(stderr, "reviewed-merge: no -mergers were given, so no merge grain made counts -- only a forge approval does (vars.GRAIN_MERGE_LOGINS)")
		}
		return 1
	}
	out, _ := json.Marshal(rec)
	fmt.Fprintln(stdout, string(out))
	return 0
}
