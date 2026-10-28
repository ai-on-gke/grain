package actions

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunnerImagesResolve(t *testing.T) {
	m := RunnerImages{DefaultRunner: "granule:github-ubuntu-latest"}
	if got, err := m.Resolve(DefaultRunner); err != nil || got != "granule:github-ubuntu-latest" {
		t.Fatalf("Resolve = %q, %v", got, err)
	}
	if _, err := m.Resolve("windows-latest"); err == nil {
		t.Fatal("unmapped runner accepted")
	}
}

func TestActScript(t *testing.T) {
	got := ActScript(".github/workflows/ci.yml", "test", "pull_request")
	for _, want := range []string{"act 'pull_request'", "--job 'test'", "ubuntu-latest=-self-hosted", "/tmp/grit-actions-artifacts"} {
		if !strings.Contains(got, want) {
			t.Errorf("script missing %q:\n%s", want, got)
		}
	}
}

func TestActScriptEmptyEvent(t *testing.T) {
	got := ActScript(".github/workflows/ci.yml", "test", "")
	if !strings.Contains(got, `act "$GRAIN_CI_EVENT"`) {
		t.Errorf("script with empty event missing $GRAIN_CI_EVENT:\n%s", got)
	}
}

// Where the sandbox was told about this deployment's Actions cache, the
// job is pointed at it and act runs no cache server of its own -- which
// is the whole of what makes a `type=gha` build warm across runs.
func TestActScriptUsesTheDeploymentsCache(t *testing.T) {
	got := ActScript(".github/workflows/ci.yml", "image", "push")
	for _, want := range []string{
		CacheURLFile,
		"--no-cache-server",
		`--env "ACTIONS_CACHE_URL=$grain_actions_cache"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("script missing %q:\n%s", want, got)
		}
	}
	// The URL itself is never in the script: it carries a per-job token,
	// and the script's text is part of the job's cache key (pkg/ci's
	// jobFingerprint), so a token in here would give every Actions job a
	// key that never repeats.
	if strings.Contains(got, "http://") {
		t.Errorf("the generated script names an address:\n%s", got)
	}
}

// A job whose workflow grain had to edit -- one job's `needs:` taken
// out, so act runs that job alone (pkg/ci/actions_needs.go) -- is given
// the text rather than the path, and act reads what grain wrote.
func TestActScriptForTextRunsTheWorkflowItWasGiven(t *testing.T) {
	text := "name: CI\non: push\njobs:\n  image:\n#    needs: [test]\n    runs-on: ubuntu-latest\n    steps:\n      - run: 'it'\\''s fine'\n"
	got := ActScriptForText(".github/workflows/ci.yml", "image", "push", text)
	for _, want := range []string{
		"mkdir -p '" + WorkflowDir + "'",
		"base64 -d > '" + WorkflowDir + "/ci.yml'",
		"--workflows '" + WorkflowDir + "/ci.yml'",
		"--job 'image'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("script missing %q:\n%s", want, got)
		}
	}
	// The workflow travels as base64 and never as itself, so no line of
	// somebody's file can end the heredoc or be read as shell.
	if strings.Contains(got, "runs-on") || strings.Contains(got, "it'\\''s fine") {
		t.Errorf("the workflow is in the script verbatim:\n%s", got)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	if !strings.Contains(strings.ReplaceAll(got, "\n", ""), encoded) {
		t.Errorf("script does not carry the workflow it was given:\n%s", got)
	}
	// Wrapped, and with no blank line to end the heredoc early.
	_, rest, _ := strings.Cut(got, "<<'GRAIN_CI_WORKFLOW'\n")
	block, _, _ := strings.Cut(rest, "GRAIN_CI_WORKFLOW\n")
	for _, line := range strings.Split(strings.TrimSuffix(block, "\n"), "\n") {
		if line == "" || len(line) > 76 {
			t.Errorf("encoded line is %d characters: %q", len(line), line)
		}
	}
}

// The script is what a job's key is computed over, so two jobs of the
// same workflow and event must produce the same text every time.
func TestActScriptIsStable(t *testing.T) {
	first := ActScript(".github/workflows/ci.yml", "image", "push")
	if second := ActScript(".github/workflows/ci.yml", "image", "push"); first != second {
		t.Errorf("the same job generated two different scripts:\n%s\n---\n%s", first, second)
	}
}

func TestRunValidation(t *testing.T) {
	ctx := context.Background()
	var out, errOut bytes.Buffer

	// Unmapped runner
	err := Run(ctx, RunRequest{
		Repo:   ".",
		Runner: "macos-latest",
		Images: RunnerImages{"ubuntu-latest": "grain-sandbox:dev"},
	}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "no Kontur image is mapped") {
		t.Fatalf("Run with unmapped runner: got %v, want unmapped runner error", err)
	}

	// Missing workflow and job
	err = Run(ctx, RunRequest{
		Repo:   ".",
		Runner: "ubuntu-latest",
		Images: RunnerImages{"ubuntu-latest": "grain-sandbox:dev"},
	}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "workflow and job are required") {
		t.Fatalf("Run with missing workflow/job: got %v, want required error", err)
	}
}

// A `granule cp` that dies before reading the repository must fail Run
// promptly -- not leave tar blocked on a full pipe until ctx ends -- and
// must still be waited on, so it is not left behind as a zombie.
func TestRunFailsWhenTheCopyDiesEarly(t *testing.T) {
	repo := t.TempDir()
	big := make([]byte, 4<<20)
	for i := range big {
		big[i] = byte(i*7 + i/251)
	}
	if err := os.WriteFile(filepath.Join(repo, "big.bin"), big, 0o600); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	granule, docker := filepath.Join(bin, "granule"), filepath.Join(bin, "docker")
	if err := os.WriteFile(granule, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Exits without reading stdin, and with a status only cp.Wait can
	// report -- tar's own failure is a broken pipe.
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	originalGranule, originalDocker := granuleCommand, dockerCommand
	t.Cleanup(func() { granuleCommand, dockerCommand = originalGranule, originalDocker })
	granuleCommand, dockerCommand = granule, docker

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// io.Discard rather than a buffer: tar and cp share stderr, and only
	// a writer that is safe for concurrent use can be handed to both.
	err := Run(ctx, RunRequest{
		Repo: repo, Workflow: "ci.yml", Job: "test", Name: "test",
		Images: RunnerImages{DefaultRunner: "image"},
	}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "copy repository") {
		t.Fatalf("Run with a failing copy: got %v, want a copy repository error", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("Run only returned once its context ran out: %v", err)
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("Run with a failing copy: got %v, want the copy's own exit status, which only waiting on it reports", err)
	}
}
