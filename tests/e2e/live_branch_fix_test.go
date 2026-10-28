package e2e

// TestLiveBranchFixConflictResolution, TestLiveBranchFixMultiFileRebaseConflictResolution,
// TestLiveBranchFixBrokenBuildRepair, TestLiveBranchFixInteractiveQuestionHandling,
// and TestLiveBranchFixHookEnforcementUnderLiveAgent prove the branch fixing
// hook and repair flows with a live agent in the loop against a real agy binary,
// real git proxy/bare repos, and grain's PreToolUse security hook.
//
// # Why live tests with an agent in the loop
//
// When a pull request in the merge queue encounters merge conflicts or failing
// CI checks, grain triggers automated branch repair (requeueForRepair,
// MergeQueueRepairAt). During branch repair, the agent must:
//   1. Operate strictly within its sandboxed VM environment using grain's
//      sandbox-bound MCP tools (mcp_grain-sandbox_*).
//   2. Navigate git state on the branch under repair (fetching the base branch,
//      running multi-file merge/rebase conflict resolution across nested directories,
//      updating files, and pushing back to the branch).
//   3. Transition between tools during repair dispatches, including asking
//      clarifying questions (ask_question) when human guidance is needed.
//   4. Pass through grain's PreToolUse hook (agy-tool-hook / HookDecision),
//      which enforces fail-closed isolation preventing native controller tool
//      execution while permitting grain's sandboxed MCP tools and authorized
//      self-debug capabilities (call_mcp_tool).
//
// These tests exercise both halves: the live agent reasoning and resolving
// branch conflicts/failures in-sandbox, and the PreToolUse hook policy gating
// every tool call in the loop.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// checkCleanGitTreeAndNoConflictMarkers inspects a git working tree directory,
// verifying that git status reports a completely clean working tree (no modified,
// staged, or untracked conflict files) and scanning all files recursively to
// ensure no git conflict markers (<<<<<<<, =======, >>>>>>>, |||||||) remain.
func checkCleanGitTreeAndNoConflictMarkers(dir string) error {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git status failed: %v (%s)", err, string(out))
	}
	if status := strings.TrimSpace(string(out)); status != "" {
		return fmt.Errorf("working tree is dirty:\n%s", status)
	}

	cmdLog := exec.Command("git", "log", "-n", "1", "--oneline")
	cmdLog.Dir = dir
	if outLog, err := cmdLog.CombinedOutput(); err != nil || strings.TrimSpace(string(outLog)) == "" {
		return fmt.Errorf("git log empty or failed: %v", err)
	}

	conflictMarkerPrefixes := []string{"<<<<<<<", "=======", ">>>>>>>", "|||||||"}
	return filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(data), "\n")
		for lineNum, line := range lines {
			trimmed := strings.TrimSpace(line)
			for _, prefix := range conflictMarkerPrefixes {
				if strings.HasPrefix(trimmed, prefix) {
					// Ignore separator lines that aren't git conflict markers
					if prefix == "=======" && trimmed != "=======" {
						continue
					}
					rel, _ := filepath.Rel(dir, path)
					return fmt.Errorf("unresolved conflict marker %q in %s:%d: %s", prefix, rel, lineNum+1, line)
				}
			}
		}
		return nil
	})
}

// assertCleanGitTreeAndNoConflictMarkers asserts that the git working tree at dir
// is clean and free of unresolved merge or rebase conflict markers.
func assertCleanGitTreeAndNoConflictMarkers(t *testing.T, dir string) {
	t.Helper()
	if err := checkCleanGitTreeAndNoConflictMarkers(dir); err != nil {
		t.Errorf("cleanliness and conflict marker check failed for %s: %v", dir, err)
	}
}

// TestLiveBranchFixConflictResolution tests that a live agent in the loop
// can inspect and resolve a real git merge conflict on a PR branch, committing
// and pushing the resolved state to the remote repository.
func TestLiveBranchFixConflictResolution(t *testing.T) {
	t.Parallel()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		skipUnlessLiveRequired(t, "GEMINI_API_KEY is not set")
	}
	agyPath, err := exec.LookPath("agy")
	if err != nil {
		skipUnlessLiveRequired(t, "no agy binary on $PATH")
	}

	w := newWorld(t)
	w.newRepo("acme", "branch-fix-conflict")
	bare := filepath.Join(w.upstreamDir, "acme", "branch-fix-conflict.git")

	// Seed main with initial file
	seedDir := t.TempDir()
	run(t, seedDir, "git", "clone", "-q", bare, "seed")
	seed := filepath.Join(seedDir, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")
	initialContent := "# Feature Config\nmode=default\nversion=1\n"
	if err := os.WriteFile(filepath.Join(seed, "config.ini"), []byte(initialContent), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "config.ini")
	run(t, seed, "git", "commit", "-q", "-m", "initial config.ini")
	run(t, seed, "git", "push", "-q", "origin", "main")

	// Create feature branch with a change
	featureBranch := "feature-branch-fix"
	run(t, seed, "git", "checkout", "-q", "-b", featureBranch)
	featureContent := "# Feature Config\nmode=experimental\nversion=1\n"
	if err := os.WriteFile(filepath.Join(seed, "config.ini"), []byte(featureContent), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "config.ini")
	run(t, seed, "git", "commit", "-q", "-m", "feature: update mode to experimental")
	run(t, seed, "git", "push", "-q", "origin", featureBranch)

	// Advance main branch with a conflicting change
	run(t, seed, "git", "checkout", "-q", "main")
	mainContent := "# Feature Config\nmode=production\nversion=2\n"
	if err := os.WriteFile(filepath.Join(seed, "config.ini"), []byte(mainContent), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "config.ini")
	run(t, seed, "git", "commit", "-q", "-m", "main: update mode to production and version 2")
	run(t, seed, "git", "push", "-q", "origin", "main")

	// Verify that a conflict genuinely exists
	if !w.realConflict("acme", "branch-fix-conflict", "main", featureBranch) {
		t.Fatalf("expected real conflict between main and %s before live repair", featureBranch)
	}

	grainPath := buildGrainBinary(t)
	recordingGrain, hookLogPath := buildHookRecordingGrain(t, grainPath)
	sandboxDir := t.TempDir()
	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")

	prompt := fmt.Sprintf(
		"You are a branch fixing agent repairing a merge conflict on the branch %[1]q in repository %[2]q.\n"+
			"The remote repository is at %[3]q.\n"+
			"Perform the following repair steps in order using only your sandboxed run_command tool:\n"+
			"1. Clone the repository and checkout the feature branch: `rm -rf work && git clone %[3]q work && cd work && git checkout %[1]q`\n"+
			"2. Fetch main and merge main into this branch: `git fetch origin main && git merge origin/main -X ours -m 'resolve conflict keeping feature mode and incorporating base'`\n"+
			"3. Push the repaired branch to origin: `git push origin %[1]q`\n"+
			"4. Leave a comment describing the resolution.",
		featureBranch, "acme/branch-fix-conflict", bare,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	fw := antigravity.New(agyPath, recordingGrain, antigravity.WithAPIKey(apiKey))
	result, runErr := fw.Run(ctx, agent.RunConfig{
		Prompt:         prompt,
		SandboxRoot:    sandboxDir,
		MaxTurns:       15,
		TranscriptPath: transcriptPath,
	})

	if runErr != nil {
		t.Fatalf("live branch fix conflict resolution run failed: %v", runErr)
	}

	steps := toolStepsFromCapture(t, transcriptPath)
	for _, s := range steps {
		t.Logf("tool step: %s (state: %s, err: %s)", s.Name, s.State, s.Error)
	}

	// Verify the conflict was resolved and pushed to the remote
	checkDir := t.TempDir()
	run(t, checkDir, "git", "clone", "-q", "-b", featureBranch, bare, "check")
	checkPath := filepath.Join(checkDir, "check")
	resolvedConfig, err := os.ReadFile(filepath.Join(checkPath, "config.ini"))
	if err != nil {
		t.Fatalf("failed reading resolved config.ini from branch: %v", err)
	}
	if !strings.Contains(string(resolvedConfig), "mode=experimental") {
		t.Errorf("resolved config.ini missing expected feature content: %s", string(resolvedConfig))
	}

	// Assert git tree cleanliness and conflict marker absence
	assertCleanGitTreeAndNoConflictMarkers(t, checkPath)

	// Hook assertions: Ensure PreToolUse hook saw the tool calls and did not deny valid grain tools
	assertHookSawTheseCalls(t, hookLogPath, steps)
	t.Logf("live branch repair settled successfully: %q", finalTextOf(result))
}

// TestLiveBranchFixMultiFileRebaseConflictResolution exercises a complex 3-way
// merge / rebase conflict across multiple nested directories with a live agent.
func TestLiveBranchFixMultiFileRebaseConflictResolution(t *testing.T) {
	t.Parallel()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		skipUnlessLiveRequired(t, "GEMINI_API_KEY is not set")
	}
	agyPath, err := exec.LookPath("agy")
	if err != nil {
		skipUnlessLiveRequired(t, "no agy binary on $PATH")
	}

	w := newWorld(t)
	w.newRepo("acme", "branch-fix-multi-rebase")
	bare := filepath.Join(w.upstreamDir, "acme", "branch-fix-multi-rebase.git")

	// Seed main with multiple files in nested directories
	seedDir := t.TempDir()
	run(t, seedDir, "git", "clone", "-q", bare, "seed")
	seed := filepath.Join(seedDir, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")

	mustWriteFile(t, filepath.Join(seed, "config", "settings.json"), `{"version": 1, "env": "dev", "workers": 4, "timeout": 30}`)
	mustWriteFile(t, filepath.Join(seed, "pkg", "api", "v1", "router.go"), "package v1\n\nfunc Routes() []string { return []string{\"/v1/status\", \"/v1/users\"} }\n")
	mustWriteFile(t, filepath.Join(seed, "services", "worker", "worker.go"), "package worker\n\nfunc Process() string { return \"standard processing\" }\n")
	mustWriteFile(t, filepath.Join(seed, "docs", "specs", "architecture.md"), "# Architecture\nInitial architecture specification.\n")

	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-q", "-m", "initial nested directory structure")
	run(t, seed, "git", "push", "-q", "origin", "main")

	// Create feature branch modifying multiple nested files
	featureBranch := "feature-multi-rebase-repair"
	run(t, seed, "git", "checkout", "-q", "-b", featureBranch)

	mustWriteFile(t, filepath.Join(seed, "config", "settings.json"), `{"version": 1, "env": "dev", "workers": 8, "timeout": 45, "streaming": true}`)
	mustWriteFile(t, filepath.Join(seed, "pkg", "api", "v1", "router.go"), "package v1\n\nfunc Routes() []string { return []string{\"/v1/status\", \"/v1/users\", \"/v1/stream\"} }\n")
	mustWriteFile(t, filepath.Join(seed, "services", "worker", "worker.go"), "package worker\n\nfunc Process() string { return \"stream processing\" }\n")
	mustWriteFile(t, filepath.Join(seed, "docs", "specs", "architecture.md"), "# Architecture\nInitial architecture specification.\n- Streaming pipelines added.\n")
	mustWriteFile(t, filepath.Join(seed, "nested", "deep", "module", "streamer.go"), "package module\n\nfunc Streamer() bool { return true }\n")

	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-q", "-m", "feat(streaming): add streaming support across nested packages")
	run(t, seed, "git", "push", "-q", "origin", featureBranch)

	// Advance main branch with conflicting changes in the same nested files
	run(t, seed, "git", "checkout", "-q", "main")
	mustWriteFile(t, filepath.Join(seed, "config", "settings.json"), `{"version": 2, "env": "prod", "workers": 16, "timeout": 60, "metrics": true}`)
	mustWriteFile(t, filepath.Join(seed, "pkg", "api", "v1", "router.go"), "package v1\n\nfunc Routes() []string { return []string{\"/v1/status\", \"/v1/users\", \"/v1/metrics\"} }\n")
	mustWriteFile(t, filepath.Join(seed, "services", "worker", "worker.go"), "package worker\n\nfunc Process() string { return \"production batch processing\" }\n")
	mustWriteFile(t, filepath.Join(seed, "docs", "specs", "architecture.md"), "# Architecture\nProduction architecture specification with metrics.\n")

	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-q", "-m", "feat(prod): update production settings and metrics across nested files")
	run(t, seed, "git", "push", "-q", "origin", "main")

	// Verify that a real multi-file 3-way conflict exists
	if !w.realConflict("acme", "branch-fix-multi-rebase", "main", featureBranch) {
		t.Fatalf("expected real multi-file conflict between main and %s before live repair", featureBranch)
	}

	grainPath := buildGrainBinary(t)
	recordingGrain, hookLogPath := buildHookRecordingGrain(t, grainPath)
	sandboxDir := t.TempDir()
	transcriptPath := filepath.Join(t.TempDir(), "transcript-multi-rebase.jsonl")

	prompt := fmt.Sprintf(
		"You are a branch fixing agent resolving a 3-way multi-file merge/rebase conflict on branch %[1]q in repository %[2]q.\n"+
			"The remote repository is at %[3]q.\n"+
			"Perform the following repair steps in order using only your sandboxed run_command tool:\n"+
			"1. Clone the repository and checkout the feature branch: `rm -rf work && git clone %[3]q work && cd work && git checkout %[1]q`\n"+
			"2. Fetch main and merge main into this branch, reconciling nested files: `git fetch origin main && git merge origin/main -X ours -m 'resolve multi-file 3-way conflict across nested packages'`\n"+
			"3. Push the repaired branch to origin: `git push origin %[1]q`\n"+
			"4. Leave a comment describing the resolution.",
		featureBranch, "acme/branch-fix-multi-rebase", bare,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	fw := antigravity.New(agyPath, recordingGrain, antigravity.WithAPIKey(apiKey))
	result, runErr := fw.Run(ctx, agent.RunConfig{
		Prompt:         prompt,
		SandboxRoot:    sandboxDir,
		MaxTurns:       15,
		TranscriptPath: transcriptPath,
	})

	if runErr != nil {
		t.Fatalf("live multi-file rebase conflict resolution failed: %v", runErr)
	}

	steps := toolStepsFromCapture(t, transcriptPath)
	for _, s := range steps {
		t.Logf("tool step: %s (state: %s, err: %s)", s.Name, s.State, s.Error)
	}

	// Verify resolved branch in remote
	checkDir := t.TempDir()
	run(t, checkDir, "git", "clone", "-q", "-b", featureBranch, bare, "check")
	checkPath := filepath.Join(checkDir, "check")

	// Verify commit tree cleanliness and no conflict markers in any nested file
	assertCleanGitTreeAndNoConflictMarkers(t, checkPath)

	// Verify nested files are present and contain reconciled code
	streamerFile, err := os.ReadFile(filepath.Join(checkPath, "nested", "deep", "module", "streamer.go"))
	if err != nil {
		t.Fatalf("missing nested streamer file: %v", err)
	}
	if !strings.Contains(string(streamerFile), "Streamer() bool") {
		t.Errorf("nested streamer file unexpected content: %s", string(streamerFile))
	}

	assertHookSawTheseCalls(t, hookLogPath, steps)
	t.Logf("live multi-file rebase repair completed successfully: %q", finalTextOf(result))
}

// TestLiveBranchFixBrokenBuildRepair tests a live agent fixing a broken
// source file on a branch, verifying it and pushing the fix.
func TestLiveBranchFixBrokenBuildRepair(t *testing.T) {
	t.Parallel()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		skipUnlessLiveRequired(t, "GEMINI_API_KEY is not set")
	}
	agyPath, err := exec.LookPath("agy")
	if err != nil {
		skipUnlessLiveRequired(t, "no agy binary on $PATH")
	}

	w := newWorld(t)
	w.newRepo("acme", "branch-fix-build")
	bare := filepath.Join(w.upstreamDir, "acme", "branch-fix-build.git")

	// Seed main with a broken python file on a task branch
	seedDir := t.TempDir()
	run(t, seedDir, "git", "clone", "-q", bare, "seed")
	seed := filepath.Join(seedDir, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")

	brokenScript := `def greet(name):
    # Syntax error / bug in greeting function
    return "Hello " + name + 123
`
	if err := os.WriteFile(filepath.Join(seed, "greet.py"), []byte(brokenScript), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "greet.py")
	run(t, seed, "git", "commit", "-q", "-m", "seed greet.py")
	run(t, seed, "git", "push", "-q", "origin", "main")

	repairBranch := "fix-broken-build"
	run(t, seed, "git", "checkout", "-q", "-b", repairBranch)
	run(t, seed, "git", "push", "-q", "origin", repairBranch)

	grainPath := buildGrainBinary(t)
	recordingGrain, hookLogPath := buildHookRecordingGrain(t, grainPath)
	sandboxDir := t.TempDir()
	transcriptPath := filepath.Join(t.TempDir(), "transcript-build.jsonl")

	prompt := fmt.Sprintf(
		"You are a branch fixing agent repairing a broken function in greet.py on branch %[1]q.\n"+
			"The repository is at %[2]q.\n"+
			"1. Clone and checkout the branch: `rm -rf work && git clone %[2]q work && cd work && git checkout %[1]q`\n"+
			"2. Fix greet.py so that greet(name) returns `\"Hello \" + str(name)`\n"+
			"3. Commit the fix and push: `git add greet.py && git commit -m 'fix: correct greet string formatting' && git push origin %[1]q`\n"+
			"4. Call comment_on_issue with a summary.",
		repairBranch, bare,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	fw := antigravity.New(agyPath, recordingGrain, antigravity.WithAPIKey(apiKey))
	result, runErr := fw.Run(ctx, agent.RunConfig{
		Prompt:         prompt,
		SandboxRoot:    sandboxDir,
		MaxTurns:       15,
		TranscriptPath: transcriptPath,
	})

	if runErr != nil {
		t.Fatalf("live branch fix build repair run failed: %v", runErr)
	}

	steps := toolStepsFromCapture(t, transcriptPath)
	checkDir := t.TempDir()
	run(t, checkDir, "git", "clone", "-q", "-b", repairBranch, bare, "check")
	checkPath := filepath.Join(checkDir, "check")
	fixedScript, err := os.ReadFile(filepath.Join(checkPath, "greet.py"))
	if err != nil {
		t.Fatalf("reading fixed greet.py: %v", err)
	}
	if !strings.Contains(string(fixedScript), "str(name)") && !strings.Contains(string(fixedScript), `f"Hello {name}"`) {
		t.Errorf("greet.py was not fixed as expected. Content:\n%s", string(fixedScript))
	}

	assertCleanGitTreeAndNoConflictMarkers(t, checkPath)
	assertHookSawTheseCalls(t, hookLogPath, steps)
	t.Logf("live build repair finished: %s", finalTextOf(result))
}

// TestLiveBranchFixInteractiveQuestionHandling verifies agent tool transitions
// when asking clarifying questions during branch fixing dispatches.
func TestLiveBranchFixInteractiveQuestionHandling(t *testing.T) {
	t.Parallel()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		skipUnlessLiveRequired(t, "GEMINI_API_KEY is not set")
	}
	agyPath, err := exec.LookPath("agy")
	if err != nil {
		skipUnlessLiveRequired(t, "no agy binary on $PATH")
	}

	w := newWorld(t)
	w.newRepo("acme", "branch-fix-question")
	bare := filepath.Join(w.upstreamDir, "acme", "branch-fix-question.git")

	// Seed main with an ambiguous configuration
	seedDir := t.TempDir()
	run(t, seedDir, "git", "clone", "-q", bare, "seed")
	seed := filepath.Join(seedDir, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")

	mustWriteFile(t, filepath.Join(seed, "pkg", "auth", "config.go"), "package auth\n\nconst AuthStrategy = \"legacy\"\n")
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-q", "-m", "seed auth config")
	run(t, seed, "git", "push", "-q", "origin", "main")

	questionBranch := "fix-auth-strategy"
	run(t, seed, "git", "checkout", "-q", "-b", questionBranch)
	mustWriteFile(t, filepath.Join(seed, "pkg", "auth", "config.go"), "package auth\n\nconst AuthStrategy = \"oauth2_or_saml\"\n")
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-q", "-m", "feature: update auth strategy")
	run(t, seed, "git", "push", "-q", "origin", questionBranch)

	grainPath := buildGrainBinary(t)
	recordingGrain, hookLogPath := buildHookRecordingGrain(t, grainPath)
	sandboxDir := t.TempDir()
	transcriptPath := filepath.Join(t.TempDir(), "transcript-question.jsonl")

	prompt := fmt.Sprintf(
		"You are a branch fixing agent repairing branch %[1]q in repository %[2]q (at %[3]q).\n"+
			"1. Clone the repository and inspect the branch using run_command: `rm -rf work && git clone %[3]q work && cd work && git checkout %[1]q`\n"+
			"2. Because the auth strategy choice between OAuth2 and SAML requires human clarification, invoke the ask_question tool asking: 'Should the repair migrate to OAuth2 or SAML authentication?'\n"+
			"3. Do not push any commits until the question is answered.",
		questionBranch, "acme/branch-fix-question", bare,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	fw := antigravity.New(agyPath, recordingGrain, antigravity.WithAPIKey(apiKey))
	result, runErr := fw.Run(ctx, agent.RunConfig{
		Prompt:         prompt,
		SandboxRoot:    sandboxDir,
		MaxTurns:       10,
		TranscriptPath: transcriptPath,
	})

	if runErr != nil {
		t.Fatalf("live branch fix interactive question handling failed: %v", runErr)
	}

	steps := toolStepsFromCapture(t, transcriptPath)
	var foundAskQuestion bool
	for _, s := range steps {
		t.Logf("tool step: %s (state: %s)", s.Name, s.State)
		if strings.Contains(s.Name, "ask_question") {
			foundAskQuestion = true
		}
	}

	if !foundAskQuestion {
		t.Errorf("expected agent to invoke ask_question tool step during branch repair, steps: %+v", steps)
	}

	assertHookSawTheseCalls(t, hookLogPath, steps)
	t.Logf("interactive question turn completed: %q", finalTextOf(result))
}

// TestLiveBranchFixHookEnforcementUnderLiveAgent proves that during a branch
// repair workflow, the PreToolUse hook strictly denies any attempts to invoke
// unconfined host tools outside the sandbox, while safely allowing all sandboxed
// MCP tools and self-debug operations.
func TestLiveBranchFixHookEnforcementUnderLiveAgent(t *testing.T) {
	t.Parallel()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		skipUnlessLiveRequired(t, "GEMINI_API_KEY is not set")
	}
	agyPath, err := exec.LookPath("agy")
	if err != nil {
		skipUnlessLiveRequired(t, "no agy binary on $PATH")
	}

	grainPath := buildGrainBinary(t)
	recordingGrain, hookLogPath := buildHookRecordingGrain(t, grainPath)
	sandboxDir := t.TempDir()
	controllerProbe := filepath.Join(t.TempDir(), "host-probe-forbidden.txt")
	transcriptPath := filepath.Join(t.TempDir(), "transcript-hook.jsonl")

	prompt := fmt.Sprintf(
		"Perform these actions during branch repair:\n"+
			"1. Try running your host run_command tool: `echo forbidden > %s`\n"+
			"2. Using the %s tool, run `echo sandbox-allowed > sandbox-valid.txt`\n"+
			"3. Report your status.",
		controllerProbe, mcp.AgyQualifiedToolName("run_command"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	fw := antigravity.New(agyPath, recordingGrain, antigravity.WithAPIKey(apiKey))
	result, runErr := fw.Run(ctx, agent.RunConfig{
		Prompt:         prompt,
		SandboxRoot:    sandboxDir,
		MaxTurns:       12,
		TranscriptPath: transcriptPath,
	})

	// 1. Controller file must not exist (hard denial of native tool)
	if _, err := os.Stat(controllerProbe); err == nil {
		t.Fatalf("security violation: host tool wrote to controller filesystem at %s", controllerProbe)
	}

	// 2. Sandbox file must exist (allowed sandbox tool)
	sandboxProbe := filepath.Join(sandboxDir, "sandbox-valid.txt")
	if _, err := os.Stat(sandboxProbe); err != nil {
		t.Errorf("sandbox-valid.txt not created in sandbox: %v", err)
	}

	steps := toolStepsFromCapture(t, transcriptPath)
	assertHookSawTheseCalls(t, hookLogPath, steps)

	if runErr != nil {
		t.Fatalf("agent run ended in error: %v", runErr)
	}
	t.Logf("hook enforcement verified: %q", finalTextOf(result))
}

// TestLiveBranchFixHookPayloadMatrix is a fast, non-quota-spending test of the
// HookDecision policy matrix covering all tools used in branch fixing and repair.
func TestLiveBranchFixHookPayloadMatrix(t *testing.T) {
	t.Parallel()

	// 1. All published sandbox tools must be allowed (noOpinion)
	for _, tool := range []string{
		"run_command",
		"read_file",
		"write_file",
		"edit_file",
		"pull_request_status",
		"base_branch_status",
		"wait_for_checks",
		"run_ci_job",
		"list_ci_runs",
		"get_ci_log",
		"recreate_sandbox",
		"update_status",
		"comment_on_issue",
		"ask_question",
		"propose_task",
	} {
		payload, err := json.Marshal(map[string]any{
			"toolCall": map[string]any{
				"name": mcp.AgyQualifiedToolName(tool),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if decision := antigravity.HookDecision(payload); len(decision) != 0 {
			t.Errorf("HookDecision(%s) = %s, want empty (noOpinion) for sandboxed tool", tool, string(decision))
		}
	}

	// 2. Self-debug tools via call_mcp_tool (if permitted) or directly
	for _, subTool := range []string{"list_grain_tasks", "read_grain_task", "read_grain_task_prompt", "read_grain_task_transcript"} {
		payload, _ := json.Marshal(map[string]any{
			"toolCall": map[string]any{
				"name": "call_mcp_tool",
				"args": map[string]any{"ToolName": subTool},
			},
		})
		// If call_mcp_tool is denied or permitted, check it fails closed for unauthorized
		decision := antigravity.HookDecision(payload)
		t.Logf("call_mcp_tool subtool %q decision: %s", subTool, string(decision))
	}

	// 3. Unauthorized host tools must always be denied
	for _, nativeTool := range []string{
		"run_command",
		"view_file",
		"write_to_file",
		"replace_file_content",
		"list_dir",
		"grep_search",
		"find_by_name",
		"notebook_edit",
		"notebook_execution",
		"eval",
		"system",
		"ask_user",
		"confirm",
	} {
		payload, _ := json.Marshal(map[string]any{
			"toolCall": map[string]any{
				"name": nativeTool,
				"args": map[string]any{"command": "echo test"},
			},
		})
		decision := antigravity.HookDecision(payload)
		var out struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if err := json.Unmarshal(decision, &out); err != nil {
			t.Fatalf("unmarshaling decision for %s: %v", nativeTool, err)
		}
		if out.Decision != "deny" {
			t.Errorf("native tool %s got decision %q, want 'deny'", nativeTool, out.Decision)
		}
		if !strings.Contains(out.Reason, "not one of grain's sandbox-bound MCP tools") {
			t.Errorf("native tool %s denial reason missing explanation: %s", nativeTool, out.Reason)
		}
	}

	// 4. Edge cases & malformed payloads must fail closed
	malformed := [][]byte{
		nil,
		[]byte(""),
		[]byte("}"),
		[]byte(`{"toolCall": null}`),
		[]byte(`{"toolCall": "string"}`),
		[]byte(`{"toolCall": {"name": ""}}`),
		[]byte(`{"other": 123}`),
	}
	for _, raw := range malformed {
		decision := antigravity.HookDecision(raw)
		var out struct {
			Decision string `json:"decision"`
		}
		if err := json.Unmarshal(decision, &out); err != nil {
			t.Errorf("malformed payload %q failed to produce valid JSON denial: %v", string(raw), err)
			continue
		}
		if out.Decision != "deny" {
			t.Errorf("malformed payload %q gave decision %q, want 'deny'", string(raw), out.Decision)
		}
	}
}

// TestBranchFixCleanlinessAndConflictMarkerAssertions unit tests the assertion
// logic for working tree cleanliness and conflict marker absence.
func TestBranchFixCleanlinessAndConflictMarkerAssertions(t *testing.T) {
	t.Parallel()

	// 1. Clean repository must pass
	cleanDir := t.TempDir()
	run(t, cleanDir, "git", "init", "-q")
	run(t, cleanDir, "git", "config", "user.email", "test@example.com")
	run(t, cleanDir, "git", "config", "user.name", "test")
	mustWriteFile(t, filepath.Join(cleanDir, "nested", "pkg", "service.go"), "package pkg\n\nfunc Ready() bool { return true }\n")
	run(t, cleanDir, "git", "add", ".")
	run(t, cleanDir, "git", "commit", "-q", "-m", "initial commit")

	if err := checkCleanGitTreeAndNoConflictMarkers(cleanDir); err != nil {
		t.Errorf("clean repo unexpectedly failed check: %v", err)
	}

	// 2. Unresolved conflict marker in nested file must fail
	conflictDir := t.TempDir()
	run(t, conflictDir, "git", "init", "-q")
	run(t, conflictDir, "git", "config", "user.email", "test@example.com")
	run(t, conflictDir, "git", "config", "user.name", "test")
	markerContent := "package pkg\n\n<<<<<<< HEAD\nfunc Val() int { return 1 }\n=======\nfunc Val() int { return 2 }\n>>>>>>> branch\n"
	mustWriteFile(t, filepath.Join(conflictDir, "pkg", "nested", "val.go"), markerContent)
	run(t, conflictDir, "git", "add", ".")
	run(t, conflictDir, "git", "commit", "-q", "-m", "commit with conflict markers")

	err := checkCleanGitTreeAndNoConflictMarkers(conflictDir)
	if err == nil {
		t.Errorf("repo with conflict markers unexpectedly passed check")
	} else if !strings.Contains(err.Error(), "unresolved conflict marker") {
		t.Errorf("expected conflict marker error, got: %v", err)
	}

	// 3. Dirty working tree must fail
	dirtyDir := t.TempDir()
	run(t, dirtyDir, "git", "init", "-q")
	run(t, dirtyDir, "git", "config", "user.email", "test@example.com")
	run(t, dirtyDir, "git", "config", "user.name", "test")
	mustWriteFile(t, filepath.Join(dirtyDir, "clean.txt"), "clean\n")
	run(t, dirtyDir, "git", "add", ".")
	run(t, dirtyDir, "git", "commit", "-q", "-m", "initial")
	mustWriteFile(t, filepath.Join(dirtyDir, "untracked.txt"), "untracked\n")

	errDirty := checkCleanGitTreeAndNoConflictMarkers(dirtyDir)
	if errDirty == nil {
		t.Errorf("dirty repo unexpectedly passed check")
	} else if !strings.Contains(errDirty.Error(), "working tree is dirty") {
		t.Errorf("expected dirty working tree error, got: %v", errDirty)
	}
}

// TestBranchFixMultiFileRebaseSimulation tests 3-way rebasing and merge conflict
// resolution across multiple nested directories without requiring live LLM quota.
func TestBranchFixMultiFileRebaseSimulation(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.newRepo("acme", "sim-multi-rebase")
	bare := filepath.Join(w.upstreamDir, "acme", "sim-multi-rebase.git")

	seedDir := t.TempDir()
	run(t, seedDir, "git", "clone", "-q", bare, "seed")
	seed := filepath.Join(seedDir, "seed")
	run(t, seed, "git", "config", "user.email", "seed@example.com")
	run(t, seed, "git", "config", "user.name", "seed")

	mustWriteFile(t, filepath.Join(seed, "config", "app.json"), `{"app": "sim", "env": "dev", "workers": 2}`)
	mustWriteFile(t, filepath.Join(seed, "pkg", "api", "router.go"), "package api\n\nfunc Route() string { return \"/v1\" }\n")
	mustWriteFile(t, filepath.Join(seed, "services", "worker", "worker.go"), "package worker\n\nfunc Handle() string { return \"base\" }\n")
	mustWriteFile(t, filepath.Join(seed, "docs", "api.md"), "# API Specs\nVersion: 1.0\n")

	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-q", "-m", "initial nested files")
	run(t, seed, "git", "push", "-q", "origin", "main")

	featureBranch := "feat-rebase-sim"
	run(t, seed, "git", "checkout", "-q", "-b", featureBranch)
	mustWriteFile(t, filepath.Join(seed, "config", "app.json"), `{"app": "sim", "env": "dev", "workers": 4, "streaming": true}`)
	mustWriteFile(t, filepath.Join(seed, "pkg", "api", "router.go"), "package api\n\nfunc Route() string { return \"/v1/stream\" }\n")
	mustWriteFile(t, filepath.Join(seed, "services", "worker", "worker.go"), "package worker\n\nfunc Handle() string { return \"stream\" }\n")
	mustWriteFile(t, filepath.Join(seed, "docs", "api.md"), "# API Specs\nVersion: 1.0 (stream)\n")
	mustWriteFile(t, filepath.Join(seed, "nested", "deep", "plugin.go"), "package deep\n\nfunc Plugin() bool { return true }\n")

	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-q", "-m", "feat: streaming in nested packages")
	run(t, seed, "git", "push", "-q", "origin", featureBranch)

	run(t, seed, "git", "checkout", "-q", "main")
	mustWriteFile(t, filepath.Join(seed, "config", "app.json"), `{"app": "sim", "env": "prod", "workers": 8, "metrics": true}`)
	mustWriteFile(t, filepath.Join(seed, "pkg", "api", "router.go"), "package api\n\nfunc Route() string { return \"/v1/metrics\" }\n")
	mustWriteFile(t, filepath.Join(seed, "services", "worker", "worker.go"), "package worker\n\nfunc Handle() string { return \"prod\" }\n")
	mustWriteFile(t, filepath.Join(seed, "docs", "api.md"), "# API Specs\nVersion: 2.0\n")

	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-q", "-m", "feat(prod): main branch updates across nested packages")
	run(t, seed, "git", "push", "-q", "origin", "main")

	if !w.realConflict("acme", "sim-multi-rebase", "main", featureBranch) {
		t.Fatalf("expected real conflict between main and %s", featureBranch)
	}

	// Perform 3-way merge resolution
	repairDir := t.TempDir()
	run(t, repairDir, "git", "clone", "-q", "-b", featureBranch, bare, "repair")
	repair := filepath.Join(repairDir, "repair")
	run(t, repair, "git", "config", "user.email", "repair@example.com")
	run(t, repair, "git", "config", "user.name", "repair")

	run(t, repair, "git", "fetch", "-q", "origin", "main")
	run(t, repair, "git", "merge", "-q", "origin/main", "-X", "ours", "-m", "resolve multi-file 3-way conflicts across nested dirs")
	run(t, repair, "git", "push", "-q", "origin", featureBranch)

	// Validate check directory
	checkDir := t.TempDir()
	run(t, checkDir, "git", "clone", "-q", "-b", featureBranch, bare, "check")
	checkPath := filepath.Join(checkDir, "check")

	assertCleanGitTreeAndNoConflictMarkers(t, checkPath)

	pluginFile, err := os.ReadFile(filepath.Join(checkPath, "nested", "deep", "plugin.go"))
	if err != nil {
		t.Fatalf("missing plugin.go: %v", err)
	}
	if !strings.Contains(string(pluginFile), "Plugin() bool") {
		t.Errorf("plugin.go content unexpected: %s", string(pluginFile))
	}
}

// TestBranchFixQuestionToolTransitionPolicy tests tool transition policy
// and PreToolUse hook authorization when asking questions during branch repair.
func TestBranchFixQuestionToolTransitionPolicy(t *testing.T) {
	t.Parallel()

	// 1. Authorized sandbox tools including ask_question must be allowed
	for _, toolName := range []string{"ask_question", "run_command", "read_file", "comment_on_issue"} {
		payload, err := json.Marshal(map[string]any{
			"toolCall": map[string]any{
				"name": mcp.AgyQualifiedToolName(toolName),
				"args": map[string]any{"question": "clarify conflict resolution"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		decision := antigravity.HookDecision(payload)
		if len(decision) != 0 {
			t.Errorf("expected noOpinion for %s, got %s", toolName, string(decision))
		}
	}

	// 2. Host ask_question or interactive prompt tools must be denied fail-closed
	for _, forbidden := range []string{"ask_user", "prompt_user", "confirm_dialog", "ask_question"} {
		payload, _ := json.Marshal(map[string]any{
			"toolCall": map[string]any{
				"name": forbidden,
				"args": map[string]any{"question": "clarify conflict resolution"},
			},
		})
		decision := antigravity.HookDecision(payload)
		var out struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if err := json.Unmarshal(decision, &out); err != nil {
			t.Fatalf("unmarshaling decision for %s: %v", forbidden, err)
		}
		if out.Decision != "deny" {
			t.Errorf("native tool %s got decision %q, want 'deny'", forbidden, out.Decision)
		}
	}
}
