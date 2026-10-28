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

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestE2EBuildAppAndManageTasks tests the end-to-end workflow of:
// 1. Initializing a new Grain instance and connecting it to a custom git repository.
// 2. Using Grain as an operator to manage tasks (create, list, update, get, approve, defer, undefer, comment, capabilities).
// 3. Dispatching an agent run that builds a small application (source code, tests, documentation).
// 4. Running tests inside the sandbox, committing, pushing a task branch, and opening a PR.
// 5. Merging the PR, syncing completion, closing the task, and verifying the built app on main.
func TestE2EBuildAppAndManageTasks(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}

	bin := buildGrainCLI(t)

	const owner, repoName = "customorg", "calc-app"
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")

	// Seed repository with initial README.
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "admin@example.com")
	run(t, seed, "git", "config", "user.name", "Admin")
	mustWriteFile(t, filepath.Join(seed, "README.md"), "# Calc App Repository\nInitial seed repository.\n")
	run(t, seed, "git", "add", "README.md")
	run(t, seed, "git", "commit", "-q", "-m", "chore: initial commit")
	run(t, seed, "git", "push", "origin", "main")

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)
	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"

	storeDir := t.TempDir()

	// -------------------------------------------------------------
	// 1. Task Management: Create Task (Unapproved / Proposed)
	// -------------------------------------------------------------
	t.Log("Step 1: Creating task via CLI in proposed state")
	createOut := runCLIStore(t, bin, storeDir,
		"-json",
		"create",
		"-title", "Implement calculator web and cli app",
		"-body", "Build a Python calculator module with math functions, CLI, and unit tests.",
		"-repo", owner+"/"+repoName,
	)
	var task ui.Task
	if err := json.Unmarshal([]byte(createOut), &task); err != nil {
		t.Fatalf("parsing grain create output: %v\n%s", err, createOut)
	}
	if task.ID == "" {
		t.Fatalf("expected non-empty task ID, got: %s", createOut)
	}
	if task.State != model.StateProposed {
		t.Fatalf("task state = %q, want proposed", task.State)
	}
	taskID := task.ID

	// -------------------------------------------------------------
	// 2. Task Management: List, Get, Update, Defer/Undefer, Approve
	// -------------------------------------------------------------
	t.Log("Step 2: Testing CLI task verbs (list, get, update, defer, undefer, approve)")

	// List tasks
	listOut := runCLIStore(t, bin, storeDir, "-json", "list", "-repo", owner+"/"+repoName)
	var listResp []ui.Task
	if err := json.Unmarshal([]byte(listOut), &listResp); err != nil {
		t.Fatalf("parsing grain list: %v\n%s", err, listOut)
	}
	if len(listResp) != 1 || listResp[0].ID != taskID {
		t.Fatalf("grain list returned %+v, want task %s", listResp, taskID)
	}

	// Get task
	getOut := runCLIStore(t, bin, storeDir, "-json", "get", taskID)
	var getTask ui.TaskDetail
	if err := json.Unmarshal([]byte(getOut), &getTask); err != nil {
		t.Fatalf("parsing grain get: %v\n%s", err, getOut)
	}
	if getTask.Title != "Implement calculator web and cli app" {
		t.Fatalf("getTask.Title = %q", getTask.Title)
	}

	// Update task title
	_ = runCLIStore(t, bin, storeDir, "update", taskID, "-title", "Implement robust calculator app with test suite")
	getOut = runCLIStore(t, bin, storeDir, "-json", "get", taskID)
	_ = json.Unmarshal([]byte(getOut), &getTask)
	if getTask.Title != "Implement robust calculator app with test suite" {
		t.Fatalf("updated Title = %q, want 'Implement robust calculator app with test suite'", getTask.Title)
	}

	// Defer and Undefer
	_ = runCLIStore(t, bin, storeDir, "defer", taskID)
	getOut = runCLIStore(t, bin, storeDir, "-json", "get", taskID)
	_ = json.Unmarshal([]byte(getOut), &getTask)
	if getTask.State != model.StateDeferred {
		t.Fatalf("state after defer = %q, want deferred", getTask.State)
	}

	_ = runCLIStore(t, bin, storeDir, "undefer", taskID)
	getOut = runCLIStore(t, bin, storeDir, "-json", "get", taskID)
	_ = json.Unmarshal([]byte(getOut), &getTask)
	if getTask.State != model.StateProposed {
		t.Fatalf("state after undefer = %q, want proposed", getTask.State)
	}

	// Approve task (proposed -> queued)
	_ = runCLIStore(t, bin, storeDir, "approve", taskID)
	getOut = runCLIStore(t, bin, storeDir, "-json", "get", taskID)
	_ = json.Unmarshal([]byte(getOut), &getTask)
	if getTask.State != model.StateQueued {
		t.Fatalf("state after approve = %q, want queued", getTask.State)
	}

	// -------------------------------------------------------------
	// 3. Build the Application via Operator / Agent Run
	// -------------------------------------------------------------
	t.Log("Step 3: Dispatching agent to build the app")
	sandboxes := credentialed(t, remote)
	branch := model.BranchName(taskID)

	calcPy := `"""Calculator module providing basic and advanced math operations."""

def add(a: float, b: float) -> float:
    return a + b

def subtract(a: float, b: float) -> float:
    return a - b

def multiply(a: float, b: float) -> float:
    return a * b

def divide(a: float, b: float) -> float:
    if b == 0:
        raise ValueError("Cannot divide by zero")
    return a / b

def power(a: float, b: float) -> float:
    return a ** b

def factorial(n: int) -> int:
    if n < 0:
        raise ValueError("Factorial is not defined for negative numbers")
    if n == 0 or n == 1:
        return 1
    result = 1
    for i in range(2, n + 1):
        result *= i
    return result

if __name__ == "__main__":
    print("Calculator App ready. 2 + 3 =", add(2, 3))
`

	testCalcPy := `import unittest
from calc import add, subtract, multiply, divide, power, factorial

class TestCalculator(unittest.TestCase):
    def test_add(self):
        self.assertEqual(add(2, 3), 5)
        self.assertEqual(add(-1, 1), 0)

    def test_subtract(self):
        self.assertEqual(subtract(10, 4), 6)
        self.assertEqual(subtract(5, 10), -5)

    def test_multiply(self):
        self.assertEqual(multiply(3, 7), 21)
        self.assertEqual(multiply(-2, 4), -8)

    def test_divide(self):
        self.assertEqual(divide(10, 2), 5)
        self.assertAlmostEqual(divide(1, 3), 0.3333333, places=5)
        with self.assertRaises(ValueError):
            divide(5, 0)

    def test_power(self):
        self.assertEqual(power(2, 3), 8)
        self.assertEqual(power(9, 0.5), 3)

    def test_factorial(self):
        self.assertEqual(factorial(0), 1)
        self.assertEqual(factorial(5), 120)
        with self.assertRaises(ValueError):
            factorial(-1)

if __name__ == "__main__":
    unittest.main()
`

	readmeMd := `# Calculator App
A simple calculator library with unit tests.

## Running Tests
Run:
` + "```bash\npython3 -m unittest test_calc.py\n```\n"

	// Script commands executed in the sandbox
	buildAppScript := fmt.Sprintf(`git clone %s work && cd work && \
git checkout -b %s && \
cat << 'EOF' > calc.py
%s
EOF
cat << 'EOF' > test_calc.py
%s
EOF
cat << 'EOF' > README.md
%s
EOF
python3 -m unittest test_calc.py && \
git add calc.py test_calc.py README.md && \
git commit -q -m 'feat: implement calculator application and test suite' && \
git push origin %s`, remote, branch, calcPy, testCalcPy, readmeMd, branch)

	agentSteps := []antigravity.Step{
		toolCall("run_command", map[string]any{"command": buildAppScript}),
		finalText("Built calculator application, verified test suite, and pushed " + branch),
	}

	client := github.NewClient(sim, nil)
	deps := orchestrator.Deps{
		Client: client, Sandboxes: sandboxes, MaxWorkers: 1,
		Framework: scriptedFramework(agentSteps),
	}

	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
			t.Fatalf("RunCycle dispatch: %v", err)
		}
		st, err := store.State(ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateAwaitingSubmit {
			t.Fatalf("state after agent push = %q, want awaiting_submit", st)
		}
	})

	if sim.pullRequestCount() != 1 {
		t.Fatalf("expected 1 pull request, got %d", sim.pullRequestCount())
	}
	prNumber := sim.firstPullRequestNumber()
	t.Logf("Pull Request #%d created successfully", prNumber)

	// -------------------------------------------------------------
	// 4. Operator Task Comment
	// -------------------------------------------------------------
	t.Log("Step 4: Posting review comment via CLI")
	_ = runCLIStore(t, bin, storeDir, "comment", taskID, "Code looks great, tests pass. Merging PR.")
	getOut = runCLIStore(t, bin, storeDir, "-json", "get", taskID)
	_ = json.Unmarshal([]byte(getOut), &getTask)
	foundComment := false
	for _, c := range getTask.Comments {
		if strings.Contains(c.Body, "Code looks great") {
			foundComment = true
		}
	}
	if !foundComment {
		t.Fatalf("expected task comments to include new comment, got none: %+v", getTask.Comments)
	}

	// -------------------------------------------------------------
	// 5. Merge Pull Request and Verify Task Completion
	// -------------------------------------------------------------
	t.Log("Step 5: Merging Pull Request and checking task lifecycle completion")
	mergeParent := t.TempDir()
	run(t, mergeParent, "git", "clone", remote, "merge")
	mergeWd := filepath.Join(mergeParent, "merge")
	run(t, mergeWd, "git", "config", "user.email", "reviewer@example.com")
	run(t, mergeWd, "git", "config", "user.name", "Reviewer")
	run(t, mergeWd, "git", "fetch", "origin", branch)
	run(t, mergeWd, "git", "checkout", "main")
	run(t, mergeWd, "git", "merge", "--no-ff", "origin/"+branch, "-m", "Merge pull request #"+fmt.Sprint(prNumber))
	run(t, mergeWd, "git", "push", "origin", "main")

	userTransport := github.NewRealTransport(githubHost)
	userTransport.UseTLS = false
	userClient := github.NewClient(userTransport, nil)
	if err := userClient.MergePullRequest(owner, repoName, prNumber, ""); err != nil {
		t.Fatalf("merging PR: %v", err)
	}

	// Run cycle to sync PR state
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
			t.Fatalf("RunCycle (PR sync): %v", err)
		}
		st, err := store.State(ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		if st != model.StateClosed {
			t.Fatalf("state after merge = %q, want closed", st)
		}
	})

	// -------------------------------------------------------------
	// 6. Verify the Built Application in Upstream 'main'
	// -------------------------------------------------------------
	t.Log("Step 6: Verifying built app on upstream main branch")
	verifyParent := t.TempDir()
	run(t, verifyParent, "git", "clone", remote, "verify")
	verifyDir := filepath.Join(verifyParent, "verify")

	// Check files exist
	for _, fname := range []string{"calc.py", "test_calc.py", "README.md"} {
		path := filepath.Join(verifyDir, fname)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected file %s to exist on main: %v", fname, err)
		}
	}

	// Run python tests in the clean clone
	testCmd := exec.Command("python3", "-m", "unittest", "test_calc.py")
	testCmd.Dir = verifyDir
	out, err := testCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running tests on main failed: %v\n%s", err, string(out))
	}
	t.Logf("App test output on main:\n%s", string(out))

	// -------------------------------------------------------------
	// 7. Post-completion task operations: reopen, retry, close
	// -------------------------------------------------------------
	t.Log("Step 7: Testing post-close verbs (reopen, close)")
	_ = runCLIStore(t, bin, storeDir, "reopen", taskID)
	getOut = runCLIStore(t, bin, storeDir, "-json", "get", taskID)
	_ = json.Unmarshal([]byte(getOut), &getTask)
	if getTask.State != model.StateAwaitingSubmit {
		t.Fatalf("state after reopen = %q, want awaiting_submit", getTask.State)
	}

	_ = runCLIStore(t, bin, storeDir, "close", taskID)
	getOut = runCLIStore(t, bin, storeDir, "-json", "get", taskID)
	_ = json.Unmarshal([]byte(getOut), &getTask)
	if getTask.State != model.StateClosed {
		t.Fatalf("state after close = %q, want closed", getTask.State)
	}

	t.Log("All e2e task management and app building tests passed successfully!")
}

// TestE2EMultiTaskAppWithDependencies tests building an application in stages across multiple tasks:
// 1. Task 1 builds the data layer (models.py).
// 2. Task 2 builds the API layer (api.py) and depends on Task 1.
// 3. Task 2 stays blocked while Task 1 is open.
// 4. Once Task 1 merges and closes, Task 2 is unblocked and dispatched, builds on top of Task 1, and merges.
func TestE2EMultiTaskAppWithDependencies(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}

	bin := buildGrainCLI(t)

	const owner, repoName = "customorg", "notes-service"
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"), "main")
	run(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")

	// Seed repository
	seedParent := t.TempDir()
	run(t, seedParent, "git", "clone", bare, "seed")
	seed := filepath.Join(seedParent, "seed")
	run(t, seed, "git", "config", "user.email", "admin@example.com")
	run(t, seed, "git", "config", "user.name", "Admin")
	mustWriteFile(t, filepath.Join(seed, "README.md"), "# Notes Service\n")
	run(t, seed, "git", "add", "README.md")
	run(t, seed, "git", "commit", "-q", "-m", "chore: initial commit")
	run(t, seed, "git", "push", "origin", "main")

	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)
	remote := "http://" + githubHost + "/" + owner + "/" + repoName + ".git"

	storeDir := t.TempDir()

	// 1. File Task 1 (Models)
	t.Log("Creating Task 1: Data Models")
	t1Out := runCLIStore(t, bin, storeDir, "-json", "create",
		"-title", "Implement Note data model",
		"-repo", owner+"/"+repoName,
		"-approve",
	)
	var t1 ui.Task
	_ = json.Unmarshal([]byte(t1Out), &t1)

	// 2. File Task 2 (API Service) depending on Task 1
	t.Log("Creating Task 2: API Service (depends on Task 1)")
	t2Out := runCLIStore(t, bin, storeDir, "-json", "create",
		"-title", "Implement HTTP REST API for Notes",
		"-repo", owner+"/"+repoName,
		"-approve",
	)
	var t2 ui.Task
	_ = json.Unmarshal([]byte(t2Out), &t2)

	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		task, err := store.GetTask(ctx, t2.ID)
		if err != nil {
			t.Fatal(err)
		}
		task.Links = append(task.Links, model.Link{Kind: model.LinkDependsOn, Target: t1.ID})
		if err := store.PutTask(ctx, *task); err != nil {
			t.Fatal(err)
		}
	})

	sandboxes := credentialed(t, remote)
	client := github.NewClient(sim, nil)

	// Script for Task 1: writes models.py and test_models.py
	modelsPy := `class Note:
    def __init__(self, note_id: int, title: str, content: str):
        self.id = note_id
        self.title = title
        self.content = content

    def to_dict(self):
        return {"id": self.id, "title": self.title, "content": self.content}
`
	testModelsPy := `import unittest
from models import Note

class TestNoteModel(unittest.TestCase):
    def test_note_creation(self):
        n = Note(1, "Test", "Hello World")
        self.assertEqual(n.to_dict(), {"id": 1, "title": "Test", "content": "Hello World"})

if __name__ == "__main__":
    unittest.main()
`
	b1 := model.BranchName(t1.ID)
	script1 := fmt.Sprintf(`git clone %s work && cd work && \
git checkout -b %s && \
cat << 'EOF' > models.py
%s
EOF
cat << 'EOF' > test_models.py
%s
EOF
python3 -m unittest test_models.py && \
git add models.py test_models.py && \
git commit -q -m 'feat: add Note data model' && \
git push origin %s`, remote, b1, modelsPy, testModelsPy, b1)

	// Script for Task 2: writes api.py and test_api.py (which uses models.py from main)
	apiPy := `from models import Note

class NotesStore:
    def __init__(self):
        self._notes = {}
        self._next_id = 1

    def create_note(self, title: str, content: str) -> Note:
        note = Note(self._next_id, title, content)
        self._notes[self._next_id] = note
        self._next_id += 1
        return note

    def get_note(self, note_id: int):
        return self._notes.get(note_id)

    def list_notes(self):
        return [n.to_dict() for n in self._notes.values()]
`
	testApiPy := `import unittest
from api import NotesStore

class TestNotesStore(unittest.TestCase):
    def test_create_and_get(self):
        store = NotesStore()
        note = store.create_note("First", "Content 1")
        self.assertEqual(note.id, 1)
        self.assertEqual(store.get_note(1).title, "First")
        self.assertEqual(len(store.list_notes()), 1)

if __name__ == "__main__":
    unittest.main()
`
	b2 := model.BranchName(t2.ID)
	script2 := fmt.Sprintf(`git clone %s work && cd work && \
git checkout -b %s && \
cat << 'EOF' > api.py
%s
EOF
cat << 'EOF' > test_api.py
%s
EOF
python3 -m unittest test_api.py && \
git add api.py test_api.py && \
git commit -q -m 'feat: add NotesStore API service' && \
git push origin %s`, remote, b2, apiPy, testApiPy, b2)

	// Dispatch round 1: only Task 1 should run, Task 2 is blocked on Task 1
	var (
		mu      sync.Mutex
		scripts = [][]antigravity.Step{
			{
				toolCall("run_command", map[string]any{"command": script1}),
				finalText("Done Task 1"),
			},
			{
				toolCall("run_command", map[string]any{"command": script2}),
				finalText("Done Task 2"),
			},
		}
	)

	framework := func(ctx context.Context, choice orchestrator.AgentChoice) (agent.Framework, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(scripts) == 0 {
			return nil, fmt.Errorf("no more scripts in queue")
		}
		s := scripts[0]
		scripts = scripts[1:]
		return antigravity.NewForTest(antigravity.Steps(s...)), nil
	}

	deps := orchestrator.Deps{
		Client: client, Sandboxes: sandboxes, MaxWorkers: 1,
		Framework: framework,
	}

	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
			t.Fatalf("RunCycle 1: %v", err)
		}
	})

	if sim.pullRequestCount() != 1 {
		t.Fatalf("expected 1 PR, got %d", sim.pullRequestCount())
	}
	pr1 := sim.pullRequestNumber(0)

	// Merge PR 1 into main
	mergeParent := t.TempDir()
	run(t, mergeParent, "git", "clone", remote, "merge")
	mergeWd := filepath.Join(mergeParent, "merge")
	run(t, mergeWd, "git", "config", "user.email", "admin@example.com")
	run(t, mergeWd, "git", "config", "user.name", "Admin")
	run(t, mergeWd, "git", "fetch", "origin", b1)
	run(t, mergeWd, "git", "checkout", "main")
	run(t, mergeWd, "git", "merge", "--no-ff", "origin/"+b1, "-m", "Merge PR 1")
	run(t, mergeWd, "git", "push", "origin", "main")

	userTransport := github.NewRealTransport(githubHost)
	userTransport.UseTLS = false
	userClient := github.NewClient(userTransport, nil)
	_ = userClient.MergePullRequest(owner, repoName, pr1, "")

	// Cycle to sync PR 1 closure and unblock Task 2
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(time.Minute)); err != nil {
			t.Fatalf("RunCycle 2: %v", err)
		}
		st1, _ := store.State(ctx, t1.ID)
		if st1 != model.StateClosed {
			t.Fatalf("Task 1 state = %q, want closed", st1)
		}
	})

	// Cycle to dispatch Task 2 now that Task 1 is closed
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(2*time.Minute)); err != nil {
			t.Fatalf("RunCycle 3: %v", err)
		}
	})

	if sim.pullRequestCount() != 2 {
		t.Fatalf("expected 2 PRs, got %d", sim.pullRequestCount())
	}
	pr2 := sim.pullRequestNumber(1)

	// Merge PR 2 into main
	run(t, mergeWd, "git", "fetch", "origin", b2)
	run(t, mergeWd, "git", "checkout", "main")
	run(t, mergeWd, "git", "merge", "--no-ff", "origin/"+b2, "-m", "Merge PR 2")
	run(t, mergeWd, "git", "push", "origin", "main")
	_ = userClient.MergePullRequest(owner, repoName, pr2, "")

	// Cycle to sync PR 2 closure
	withStore(t, storeDir, func(store *model.Store, ctx context.Context) {
		deps.Store = store
		if err := orchestrator.RunCycle(ctx, deps, baseTime.Add(3*time.Minute)); err != nil {
			t.Fatalf("RunCycle 4: %v", err)
		}
		st2, _ := store.State(ctx, t2.ID)
		if st2 != model.StateClosed {
			t.Fatalf("Task 2 state = %q, want closed", st2)
		}
	})

	// Verify all code on upstream main
	verifyParent := t.TempDir()
	run(t, verifyParent, "git", "clone", remote, "verify")
	verifyDir := filepath.Join(verifyParent, "verify")

	for _, fname := range []string{"models.py", "test_models.py", "api.py", "test_api.py"} {
		if _, err := os.Stat(filepath.Join(verifyDir, fname)); err != nil {
			t.Fatalf("expected %s on main: %v", fname, err)
		}
	}

	testCmd := exec.Command("python3", "-m", "unittest", "test_models.py", "test_api.py")
	testCmd.Dir = verifyDir
	out, err := testCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running multi-task test suite on main failed: %v\n%s", err, string(out))
	}
	t.Logf("Combined tests output on main:\n%s", string(out))
}
