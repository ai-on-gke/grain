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

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func findDBDir(customDir string, port int) (string, error) {
	if customDir != "" {
		return customDir, nil
	}

	if port > 0 {
		out, err := exec.Command("ss", "-tlpn", fmt.Sprintf("sport = :%d", port)).Output()
		if err == nil {
			re := regexp.MustCompile(`pid=(\d+)`)
			match := re.FindStringSubmatch(string(out))
			if len(match) > 1 {
				pid := match[1]
				envBytes, err := os.ReadFile(fmt.Sprintf("/proc/%s/environ", pid))
				if err == nil {
					for _, entry := range bytes.Split(envBytes, []byte{0}) {
						if bytes.HasPrefix(entry, []byte("TMPDIR=")) {
							tmpDir := string(bytes.TrimPrefix(entry, []byte("TMPDIR=")))
							matches, _ := filepath.Glob(filepath.Join(tmpDir, "grain-demo-*"))
							if len(matches) > 0 {
								return matches[0], nil
							}
						}
					}
				}
			}
		}
	}

	// Search in /tmp for grain-e2e-run-*/grain-demo-*
	matches, _ := filepath.Glob("/tmp/grain-e2e-run-*/grain-demo-*")
	if len(matches) > 0 {
		return matches[len(matches)-1], nil
	}
	matches, _ = filepath.Glob("/tmp/grain-demo-*")
	if len(matches) > 0 {
		return matches[len(matches)-1], nil
	}
	return "", fmt.Errorf("no grain demo database directory found in /tmp")
}

func main() {
	dbDirFlag := flag.String("dir", "", "path to directory holding grain.db")
	portFlag := flag.Int("port", 0, "port number of running grain demo instance")
	// -release names the release a command acts on. Without it a command
	// acts on the oldest unmerged release, which is all a walkthrough with
	// one release in flight needs; one with two has to say which.
	releaseFlag := flag.String("release", "", "release name to act on (default: the oldest one)")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		log.Fatal("usage: releasehelper [-dir <dir>] [-port <port>] [-release <name>] <command>\ncommands: link-port, complete-suite-pass, sync-suites, sync-releases, provision-legacy, branch-sha, mark-candidate-error, mark-release-provisioned, mark-candidate-cut, mark-candidate-promoted, mark-release-merged, mark-release-error, sync-qualifications")
	}
	cmd := args[0]

	dbDir, err := findDBDir(*dbDirFlag, *portFlag)
	if err != nil {
		log.Fatalf("find DB: %v", err)
	}

	sqlDB, err := sqlite.Open(sqlite.DefaultConfig(dbDir))
	if err != nil {
		log.Fatalf("open DB at %s: %v", dbDir, err)
	}
	defer sqlDB.Close()

	store := model.New(sqlDB)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// release is the one the command acts on: the one -release names, or
	// the oldest (ListReleases is newest first).
	release := func() model.Release {
		releases, err := store.ListReleases(ctx, repo)
		if err != nil {
			log.Fatalf("list releases: %v", err)
		}
		if len(releases) == 0 {
			log.Fatalf("no releases found for %s", repo)
		}
		if *releaseFlag == "" {
			return releases[len(releases)-1]
		}
		for _, r := range releases {
			if r.Name == *releaseFlag {
				return r
			}
		}
		log.Fatalf("no release %q on %s", *releaseFlag, repo)
		return model.Release{}
	}
	// candidate is that release's newest candidate (ListCandidates is
	// newest first), which is the one a reconciler would act on next.
	candidate := func(r model.Release) model.Candidate {
		candidates, err := store.ListCandidates(ctx, repo, r.Name)
		if err != nil {
			log.Fatalf("list candidates: %v", err)
		}
		if len(candidates) == 0 {
			log.Fatalf("no candidates found for %s / %s", repo, r.Name)
		}
		return candidates[0]
	}

	switch cmd {
	case "mark-candidate-error":
		c := candidate(release())
		if err := store.MarkCandidateError(ctx, c.ID, "Simulated remote git branch failure during candidate cut"); err != nil {
			log.Fatalf("update candidate status: %v", err)
		}
		fmt.Printf("Updated candidate %s to error status\n", c.Branch)

	case "mark-release-provisioned":
		r := release()
		if err := store.MarkReleaseProvisioned(ctx, r.ID); err != nil {
			log.Fatalf("mark release provisioned: %v", err)
		}
		fmt.Printf("Updated release %s to provisioned\n", r.Name)

	case "mark-candidate-cut":
		c := candidate(release())
		if err := store.MarkCandidateCut(ctx, c.ID); err != nil {
			log.Fatalf("mark candidate cut: %v", err)
		}
		fmt.Printf("Updated candidate %s to active/cut\n", c.Branch)

	case "mark-candidate-promoted":
		c := candidate(release())
		if err := store.MarkCandidatePromoted(ctx, c.ID, time.Now()); err != nil {
			log.Fatalf("mark candidate promoted: %v", err)
		}
		fmt.Printf("Updated candidate %s to promoted\n", c.Branch)

	case "mark-release-merged":
		r := release()
		url := fmt.Sprintf("https://github.com/%s/%s/pull/4242", repo.Owner, repo.Name)
		if err := store.MarkReleaseMerged(ctx, r.ID, url, time.Now()); err != nil {
			log.Fatalf("mark release merged: %v", err)
		}
		fmt.Printf("Updated release %s to merged\n", r.Name)

	case "mark-release-error":
		r := release()
		if err := store.MarkReleaseError(ctx, r.ID, "Simulated GitHub failure creating the release branches"); err != nil {
			log.Fatalf("mark release error: %v", err)
		}
		fmt.Printf("Updated release %s to error status\n", r.Name)

	case "sync-qualifications":
		err = orchestrator.SyncQualifications(ctx, store, time.Now())
		if err != nil {
			log.Fatalf("sync qualifications: %v", err)
		}
		fmt.Printf("Synced qualifications\n")

	case "link-port":
		// link-port <source> <port>: the two links the merge queue writes
		// when it files a port (orchestrator.filePorts), which the demo,
		// running no merge queue, cannot produce on its own.
		if len(args) != 3 {
			log.Fatal("usage: releasehelper link-port <source task> <port task>")
		}
		source, port := args[1], args[2]
		link := func(id string, l model.Link) {
			if err := store.UpdateTask(ctx, id, func(t *model.Task) error {
				t.Links = append(t.Links, l)
				return nil
			}); err != nil {
				log.Fatalf("linking %s: %v", id, err)
			}
		}
		link(port, model.Link{Kind: model.LinkPortOf, Target: source})
		link(source, model.Link{Kind: model.LinkPortedBy, Target: port})
		fmt.Printf("Linked %s as a port of %s\n", port, source)

	case "complete-suite-pass":
		// complete-suite-pass: every task in the newest active suite run's
		// current pass finishes, as an agent's run ending in a comment
		// would have it -- the demo runs no agents.
		runs, err := store.ActiveSuiteRuns(ctx)
		if err != nil {
			log.Fatalf("list active suite runs: %v", err)
		}
		if len(runs) == 0 {
			log.Fatal("no active suite run")
		}
		run := runs[len(runs)-1]
		now := time.Now().UTC()
		for _, t := range run.PassTasks(run.CurrentPass()) {
			if err := store.ObserveField(ctx, t.TaskID, now, func(o *model.Observation) { o.CompletedAt = &now }); err != nil {
				log.Fatalf("completing %s: %v", t.TaskID, err)
			}
			fmt.Printf("Completed %s (suite run %d, pass %d)\n", t.TaskID, run.ID, run.CurrentPass())
		}

	case "sync-suites":
		// sync-suites: one cycle of the reconcile loop's own suite step
		// (orchestrator.SyncSuites), logged the way the daemon logs a
		// reconciler's error, which is what a walkthrough greps.
		if err := orchestrator.SyncSuites(ctx, store, time.Now().UTC()); err != nil {
			log.Printf("reconcile: suites: %v", err)
		}
		fmt.Println("Synced suites")

	case "sync-releases":
		// sync-releases: one cycle of the reconcile loop's own release
		// step (orchestrator.SyncReleases) against the -github-sim demo's
		// own simulated GitHub, over the same bare repo its pane lists
		// branches from.
		client := github.NewClient(githubsim.New(repo.Owner, repo.Name, simRepo(dbDir, repo), "main"), nil)
		if err := orchestrator.SyncReleases(ctx, store, client, time.Now().UTC()); err != nil {
			log.Printf("reconcile: releases: %v", err)
		}
		fmt.Println("Synced releases")

	case "provision-legacy":
		// provision-legacy: what provisioning did before it refused a
		// release named after the default branch -- the release's latest
		// branch and its first candidate created, at a commit the default
		// branch does not have, so a promotion that force-moved the prod
		// branch would visibly move it.
		r := release()
		c := candidate(r)
		bare := simRepo(dbDir, repo)
		tree := simGit(bare, "rev-parse", "main^{tree}")
		commit := simGit(bare, "commit-tree", "-p", "main", "-m", "Work on "+r.LatestBranch(), tree)
		for _, branch := range []string{r.LatestBranch(), c.Branch} {
			simGit(bare, "update-ref", "refs/heads/"+branch, commit)
		}
		if err := store.MarkReleaseProvisioned(ctx, r.ID); err != nil {
			log.Fatalf("mark release provisioned: %v", err)
		}
		if err := store.MarkCandidateCut(ctx, c.ID); err != nil {
			log.Fatalf("mark candidate cut: %v", err)
		}
		fmt.Printf("Provisioned %s and cut %s at %s\n", r.Name, c.Branch, commit)

	case "branch-sha":
		// branch-sha <branch>: the simulated GitHub's tip of branch.
		if len(args) != 2 {
			log.Fatal("usage: releasehelper branch-sha <branch>")
		}
		fmt.Println(simGit(simRepo(dbDir, repo), "rev-parse", "--verify", "refs/heads/"+args[1]))

	default:
		log.Fatalf("unknown command: %s", cmd)
	}
}

// simRepo is the bare repo `grain demo -github-sim` keeps the simulated
// GitHub's branches in (pkg/demo's configureGitHubSimDemo).
func simRepo(dbDir string, repo model.RepoRef) string {
	return filepath.Join(dbDir, "githubsim", repo.Owner, repo.Name+".git")
}

// simGit runs git against the simulated GitHub's bare repo.
func simGit(bare string, args ...string) string {
	cmd := exec.Command("git", append([]string{"--git-dir", bare}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=releasehelper", "GIT_AUTHOR_EMAIL=releasehelper@grain.invalid",
		"GIT_COMMITTER_NAME=releasehelper", "GIT_COMMITTER_EMAIL=releasehelper@grain.invalid")
	out, err := cmd.Output()
	if err != nil {
		log.Fatalf("git %v: %v", args, err)
	}
	return string(bytes.TrimSpace(out))
}
