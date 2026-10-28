package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// seedGitHubCIDemo records a few days of GitHub CI on acme/widgets
// (model.GitHubCIJob), so the Metrics pane's GitHub CI section has
// something to show beside grain's own CI: a pull request whose test job
// flaked and went green on a re-run, one still red, a skipped deploy and
// a neutral coverage check, and main going red and back by its own
// results. Main's red and green time is measured only where main is a
// watched base branch, which a -fake-ci demo records.
//
// These rows are GitHub's results, not grain CI's: nothing here writes
// ci_run, and every grain-CI number the demo shows is the same with or
// without them.
func seedGitHubCIDemo(ctx context.Context, store *model.Store, ago func(time.Duration) time.Time) error {
	const repo = "acme/widgets"
	var id int64 = 9100
	put := func(ref, commit, workflow, job string, attempt int, conclusion string, at time.Duration) error {
		id++
		done := ago(at)
		started := done.Add(-7 * time.Minute)
		j := model.GitHubCIJob{
			Repo: repo, CheckRunID: id, App: model.GitHubActionsApp, Ref: ref, Commit: commit,
			Workflow: workflow, JobName: job, RunAttempt: attempt, Conclusion: conclusion,
			StartedAt: &started, CompletedAt: &done, Via: "webhook", UpdatedAt: done,
		}
		if workflow == "" {
			j.App = "codecov"
		} else {
			j.RunID = id * 10
		}
		if err := store.PutGitHubCIJob(ctx, j); err != nil {
			return fmt.Errorf("recording GitHub CI job %s on %s: %w", job, ref, err)
		}
		return nil
	}
	h := time.Hour
	for _, j := range []struct {
		ref, commit, workflow, job string
		attempt                    int
		conclusion                 string
		at                         time.Duration
	}{
		// Pull request #101: test flakes, the re-run passes.
		{"feature/pagination", "a1b2c3d4e5f6", "CI", "build", 1, "success", 50 * h},
		{"feature/pagination", "a1b2c3d4e5f6", "CI", "test", 1, "failure", 50 * h},
		{"feature/pagination", "a1b2c3d4e5f6", "CI", "lint", 1, "success", 50 * h},
		{"feature/pagination", "a1b2c3d4e5f6", "", "codecov/patch", 0, "neutral", 50 * h},
		{"feature/pagination", "a1b2c3d4e5f6", "CI", "test", 2, "success", 49 * h},
		// Pull request #102: a real failure nobody has fixed yet.
		{"feature/dark-mode", "b2c3d4e5f6a1", "CI", "build", 1, "success", 20 * h},
		{"feature/dark-mode", "b2c3d4e5f6a1", "CI", "test", 1, "failure", 20 * h},
		{"feature/dark-mode", "b2c3d4e5f6a1", "CI", "lint", 1, "timed_out", 20 * h},
		{"feature/dark-mode", "b2c3d4e5f6a1", "Deploy", "preview", 1, "cancelled", 20 * h},
		// main: green, red for a few hours, green again.
		{"main", "c3d4e5f6a1b2", "CI", "build", 1, "success", 60 * h},
		{"main", "c3d4e5f6a1b2", "CI", "test", 1, "success", 60 * h},
		{"main", "c3d4e5f6a1b2", "Deploy", "release", 1, "skipped", 60 * h},
		{"main", "d4e5f6a1b2c3", "CI", "build", 1, "success", 30 * h},
		{"main", "d4e5f6a1b2c3", "CI", "test", 1, "failure", 30 * h},
		{"main", "e5f6a1b2c3d4", "CI", "build", 1, "success", 26 * h},
		{"main", "e5f6a1b2c3d4", "CI", "test", 1, "success", 26 * h},
	} {
		if err := put(j.ref, j.commit, j.workflow, j.job, j.attempt, j.conclusion, j.at); err != nil {
			return err
		}
	}

	// Three unrelated branches failing the same GitHub Actions step within the
	// shared-fault window, with Actions timestamps and runner paths that
	// normalizeFailure strips to one digest.
	for i, b := range []struct {
		ref, commit, logLine string
		at                   time.Duration
	}{
		{
			ref:    "feature/dark-mode",
			commit: "b2c3d4e5f6a1",
			logLine: "2026-10-27T08:15:01.1234567Z ##[group]Run go test ./...\n" +
				"2026-10-27T08:15:02.0000000Z --- FAIL: TestSessionStore (0.14s)\n" +
				"2026-10-27T08:15:02.0000000Z     /home/runner/work/widgets/widgets/pkg/auth/session.go:142: token signature mismatch\n" +
				"2026-10-27T08:15:02.0000000Z ##[error]Process completed with exit code 1.\n",
			at: 45 * time.Minute,
		},
		{
			ref:    "feature/webhook-retry",
			commit: "f6a1b2c3d4e5",
			logLine: "2026-10-27T08:30:11.9876543Z ##[group]Run go test ./...\n" +
				"2026-10-27T08:30:12.0000000Z --- FAIL: TestSessionStore (0.29s)\n" +
				"2026-10-27T08:30:12.0000000Z     D:\\a\\widgets\\widgets\\pkg\\auth\\session.go:142: token signature mismatch\n" +
				"2026-10-27T08:30:12.0000000Z ##[error]Process completed with exit code 1.\n",
			at: 30 * time.Minute,
		},
		{
			ref:    "feature/audit-log",
			commit: "1a2b3c4d5e6f",
			logLine: "2026-10-27T08:45:44.0000001Z ##[group]Run go test ./...\n" +
				"2026-10-27T08:45:45.0000000Z --- FAIL: TestSessionStore (0.08s)\n" +
				"2026-10-27T08:45:45.0000000Z     /home/runner/work/widgets/widgets/pkg/auth/session.go:142: token signature mismatch\n" +
				"2026-10-27T08:45:45.0000000Z ##[error]Process completed with exit code 1.\n",
			at: 15 * time.Minute,
		},
	} {
		id++
		done := ago(b.at)
		started := done.Add(-4 * time.Minute)
		j := model.GitHubCIJob{
			Repo:        repo,
			CheckRunID:  id,
			App:         model.GitHubActionsApp,
			Ref:         b.ref,
			Commit:      b.commit,
			Workflow:    "CI",
			RunID:       id * 10,
			RunAttempt:  1,
			JobName:     "e2e",
			Conclusion:  "failure",
			StartedAt:   &started,
			CompletedAt: &done,
			FailingStep: "Run go test ./...",
			FailureTail: b.logLine,
			TailFetched: true,
			TailSource:  "log",
			Via:         "webhook",
			UpdatedAt:   done.Add(time.Duration(i) * time.Second),
		}
		if err := store.PutGitHubCIJob(ctx, j); err != nil {
			return fmt.Errorf("recording demo GitHub shared fault on %s: %w", b.ref, err)
		}
	}
	return nil
}
