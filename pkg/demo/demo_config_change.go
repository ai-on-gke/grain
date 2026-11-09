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

package demo

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// configChangeDemo is `grain demo -config-change`: the demo serves a real,
// throwaway grain/config -- the controller-local configuration repository
// a daemon keeps beside its store (daemon.OpenStateRepo), exported from
// the demo's own store and managed by the daemon's own StateManager -- and
// seeds tasks parked on real proposal branches of it, each pushed the way
// a session's propose_config_change pushes one and diffed and validated by
// the same code (ui.Client.ProposeConfigChange).
//
// Without it the config-change card (ui/src/components/detail/
// PendingConfigChange.jsx) rendered but neither of its buttons could work:
// the demo had no config repository to merge into or delete a branch
// from, so Approve & Apply and Reject both answered 501
// (docs/test-runs/2026-11-08-task-intake-inbox-lifecycle-task-3611.md,
// step 36).
//
// Two tasks, so both decisions can be made in one demo: one proposing a
// longer poll interval (approve it, and grain/config's main gets exactly
// that commit and the live configuration polls every 30s), and one
// proposing a prompt extension (reject it, and its branch is deleted and
// the reason goes back to the session). They edit lines far enough apart
// that approving both merges cleanly too.
type configChangeDemoProposal struct {
	title, description string
	name, summary      string
	edit               func(grainConfig string) (string, error)
}

// ConfigChangeDemoPollTitle and ConfigChangeDemoPromptTitle are the two
// seeded tasks' titles, for the tests that find them.
const (
	ConfigChangeDemoPollTitle   = "Ease off polling between reconcile cycles"
	ConfigChangeDemoPromptTitle = "Remind every run to run the unit tests"
)

var pollIntervalLine = regexp.MustCompile(`"poll_interval_ms": (\d+)`)

func configChangeDemoProposals() []configChangeDemoProposal {
	return []configChangeDemoProposal{
		{
			title: ConfigChangeDemoPollTitle,
			description: "The reconcile loop polls far more often than this deployment needs. " +
				"Propose a longer poll interval in grain/config.",
			name:    "poll-interval",
			summary: "Raise the default poll interval to 30s",
			edit: func(grainConfig string) (string, error) {
				m := pollIntervalLine.FindStringSubmatch(grainConfig)
				if m == nil {
					return "", fmt.Errorf("no poll_interval_ms in grain_config.json")
				}
				if ms, err := strconv.Atoi(m[1]); err != nil || ms == 30000 {
					return "", fmt.Errorf("poll_interval_ms is %s already", m[1])
				}
				return strings.Replace(grainConfig, m[0], `"poll_interval_ms": 30000`, 1), nil
			},
		},
		{
			title: ConfigChangeDemoPromptTitle,
			description: "Runs keep pushing without running the unit tests first. " +
				"Propose a prompt extension in grain/config that reminds them.",
			name:    "prompt-extension",
			summary: "Ask every run to run the unit tests before pushing",
			edit: func(grainConfig string) (string, error) {
				const empty = `"prompt_extension": ""`
				if !strings.Contains(grainConfig, empty) {
					return "", fmt.Errorf("prompt_extension in grain_config.json is not empty")
				}
				return strings.Replace(grainConfig, empty,
					`"prompt_extension": "Run the unit tests before you push."`, 1), nil
			},
		},
	}
}

// configureConfigChangeDemo opens the throwaway grain/config under dir,
// exports the demo's store into it, hands its StateManager to cfg as the
// daemon does (ui.Config.StateRepo and StateRepoRef), and seeds the
// parked tasks. It runs after every other seed, so what grain/config
// holds is the demo's finished configuration.
func configureConfigChangeDemo(ctx context.Context, dir string, db *sql.DB, store *model.Store, cfg *ui.Config) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("-config-change needs git on PATH: %v", err)
	}
	// -git-proxy's namespace may have put a placeholder settings file
	// where grain/config goes (newDemoNamespace); the real repository
	// replaces it.
	if !demoStateRepoIsReal(dir) {
		if err := os.RemoveAll(daemon.StateRepoDir(dir)); err != nil {
			return err
		}
	}
	repo, err := daemon.OpenStateRepo(ctx, dir)
	if err != nil {
		return fmt.Errorf("opening grain/config: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		return fmt.Errorf("exporting the demo's configuration to grain/config: %v", err)
	}
	manager := daemon.NewStateManager(dir, db, repo, daemon.OpenSecrets(dir), nil, nil)
	if _, err := manager.Sync(ctx); err != nil {
		return fmt.Errorf("syncing grain/config: %v", err)
	}
	cfg.StateRepo = manager
	cfg.StateRepoRef = func() model.RepoRef { return model.ConfigRepo() }
	// The daemon lists grain/config's branches from this same checkout
	// (daemon.repoBranchGate); without it every page about a config task
	// asked for them, got "branch listing is not available for the
	// internal config repository" and logged the 404 (2026-11-09 intake
	// walkthrough).
	cfg.RepoBranches = configRepoBranches{local: manager, next: cfg.RepoBranches}

	client := ui.NewClient(*cfg, store)
	for _, p := range configChangeDemoProposals() {
		if err := seedConfigChangeProposal(ctx, client, store, cfg.Actor.ID, repo.Dir(), repo.Branch(), p); err != nil {
			return fmt.Errorf("seeding %q: %v", p.title, err)
		}
	}
	return nil
}

// configRepoBranches is ui.Config.RepoBranches for -config-change: the
// throwaway grain/config's branches from its own checkout, as the
// daemon's repoBranchGate reads them, and every other repository left to
// whatever source was there before.
type configRepoBranches struct {
	local interface {
		Branches(ctx context.Context) ([]string, error)
	}
	next ui.RepoBranchSource
}

func (b configRepoBranches) RepoBranches(ctx context.Context, repo model.RepoRef) ([]string, bool, error) {
	if model.IsConfigRepo(repo) {
		names, err := b.local.Branches(ctx)
		return names, true, err
	}
	if b.next == nil {
		return nil, false, nil
	}
	return b.next.RepoBranches(ctx, repo)
}

// seedConfigChangeProposal files p's task, pushes its proposal branch
// into grain/config, records the proposal through the UI's own code, and
// parks the task on it as the orchestrator does when the run ends
// (orchestrator.finish's relayQuestion).
func seedConfigChangeProposal(ctx context.Context, client *ui.Client, store *model.Store, actor, repoDir, base string, p configChangeDemoProposal) error {
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:       p.title,
		Description: p.description,
		Repo:        model.ConfigRepo().String(),
		Approved:    true,
	})
	if err != nil {
		return err
	}
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Writes = append(t.Writes, model.WriteTarget{Repo: model.ConfigRepo()})
		return nil
	}); err != nil {
		return fmt.Errorf("giving it grain/config to write: %w", err)
	}
	branch := model.ConfigProposalBranchPrefix(task.ID) + p.name
	head, err := pushConfigProposal(repoDir, base, branch, p.summary, p.edit)
	if err != nil {
		return fmt.Errorf("pushing %s: %w", branch, err)
	}
	pending, err := client.ProposeConfigChange(ctx, task.ID, ui.ConfigChangeProposal{
		Summary: p.summary, Branch: branch, HeadSHA: head,
	})
	if err != nil {
		return fmt.Errorf("proposing %s: %w", branch, err)
	}
	if !pending.Validation.OK {
		return fmt.Errorf("%s does not validate: %s", branch, pending.Validation.Error)
	}
	now := time.Now().UTC()
	agent := model.Principal{Kind: model.PrincipalAgent, ID: "demo-run-" + task.ID}
	body := fmt.Sprintf("**This session proposed a change to %s:** %s\n\nBranch `%s` at `%s` touches %s. "+
		"It passed config validation. Approve or reject it in the card on this task; nothing takes effect until it is approved.",
		model.ConfigRepo(), pending.Summary, pending.Branch, shortDemoSHA(pending.HeadSHA), strings.Join(pending.Files, ", "))
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: actor}, OnBehalfOf: &agent},
		Body:      body,
		Summary:   "Approve config change: " + pending.Summary,
		CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("posting the proposal: %w", err)
	}
	return store.ObserveField(ctx, task.ID, now, func(o *model.Observation) {
		o.CompletedAt = &now
		o.PendingQuestionCommentID = &commentID
	})
}

// pushConfigProposal clones grain/config, commits edit's change to
// tables/grain_config.json on branch, and pushes it back -- what a
// session's propose_config_change does through the git proxy -- and is
// the commit pushed.
func pushConfigProposal(repoDir, base, branch, summary string, edit func(string) (string, error)) (string, error) {
	work, err := os.MkdirTemp("", "grain-demo-config-proposal-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", work}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := git("clone", "--quiet", "--branch", base, "--end-of-options", repoDir, "."); err != nil {
		return "", err
	}
	if _, err := git("checkout", "--quiet", "-b", branch); err != nil {
		return "", err
	}
	file := filepath.Join(work, "tables", "grain_config.json")
	before, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	after, err := edit(string(before))
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(file, []byte(after), 0o644); err != nil {
		return "", err
	}
	if _, err := git("-c", "user.name=grain demo session", "-c", "user.email=session@grain.invalid",
		"commit", "--quiet", "-am", summary); err != nil {
		return "", err
	}
	if _, err := git("push", "--quiet", "origin", "HEAD:refs/heads/"+branch); err != nil {
		return "", err
	}
	return git("rev-parse", "HEAD")
}

// demoStateRepoIsReal reports whether dir's grain/config is a real
// repository (-config-change's) rather than nothing or a placeholder.
func demoStateRepoIsReal(dir string) bool {
	_, err := os.Stat(filepath.Join(daemon.StateRepoDir(dir), ".git"))
	return err == nil
}

func shortDemoSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
