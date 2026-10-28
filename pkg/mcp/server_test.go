package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, root string) *Client {
	t.Helper()
	registry := NewRegistry()
	registry.Register(NewSandboxTools(root)...)
	sink := &MockSink{}
	registry.Register(NewMockTools(sink, nil)...)
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })
	return client
}

func TestListToolsAdvertisesAllTenInOrder(t *testing.T) {
	client := newTestClient(t, t.TempDir())
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"run_command", "read_file", "edit_file", "write_file",
		"ask_question", "request_secret", "comment_on_issue", "nothing_needed", "write_handover",
		"propose_task", "add_review_comment",
	}
	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d: %+v", len(tools), len(want), tools)
	}
	for i, name := range want {
		if tools[i].Name != name {
			t.Errorf("tool %d: got %q, want %q", i, tools[i].Name, name)
		}
		if tools[i].Description == "" {
			t.Errorf("tool %q: empty description", name)
		}
	}
}

func TestWriteThenReadFileRoundTrips(t *testing.T) {
	root := t.TempDir()
	client := newTestClient(t, root)
	ctx := context.Background()

	res, err := client.CallTool(ctx, "write_file", map[string]any{
		"file_path": "sub/dir/hello.txt",
		"content":   "line one\nline two\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("write_file reported error: %s", res.Text())
	}

	on := filepath.Join(root, "sub", "dir", "hello.txt")
	if _, err := os.Stat(on); err != nil {
		t.Fatalf("file was not created at expected path: %v", err)
	}

	res, err = client.CallTool(ctx, "read_file", map[string]any{"file_path": "sub/dir/hello.txt"})
	if err != nil {
		t.Fatal(err)
	}
	want := "     1\tline one\n     2\tline two"
	if res.Text() != want {
		t.Errorf("read_file text = %q, want %q", res.Text(), want)
	}
}

func TestReadFileRespectsOffsetAndLimit(t *testing.T) {
	root := t.TempDir()
	client := newTestClient(t, root)
	ctx := context.Background()

	if _, err := client.CallTool(ctx, "write_file", map[string]any{
		"file_path": "f.txt", "content": "a\nb\nc\nd\n",
	}); err != nil {
		t.Fatal(err)
	}
	res, err := client.CallTool(ctx, "read_file", map[string]any{
		"file_path": "f.txt", "offset": float64(1), "limit": float64(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "     2\tb\n     3\tc"
	if res.Text() != want {
		t.Errorf("got %q, want %q", res.Text(), want)
	}
}

func TestEditFileRequiresUniqueMatchUnlessReplaceAll(t *testing.T) {
	root := t.TempDir()
	client := newTestClient(t, root)
	ctx := context.Background()

	if _, err := client.CallTool(ctx, "write_file", map[string]any{
		"file_path": "f.txt", "content": "x x x",
	}); err != nil {
		t.Fatal(err)
	}

	res, err := client.CallTool(ctx, "edit_file", map[string]any{
		"file_path": "f.txt", "old_string": "x", "new_string": "y",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("expected ambiguous edit to be an error, got: %s", res.Text())
	}

	res, err = client.CallTool(ctx, "edit_file", map[string]any{
		"file_path": "f.txt", "old_string": "x", "new_string": "y", "replace_all": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("replace_all edit failed: %s", res.Text())
	}

	data, err := os.ReadFile(filepath.Join(root, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "y y y" {
		t.Errorf("file content = %q, want %q", string(data), "y y y")
	}
}

func TestSandboxToolsRejectEscapingPaths(t *testing.T) {
	root := t.TempDir()
	client := newTestClient(t, root)
	ctx := context.Background()

	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []string{outside, "../../etc/passwd", "../escape.txt"}
	for _, fp := range cases {
		res, err := client.CallTool(ctx, "read_file", map[string]any{"file_path": fp})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Errorf("read_file(%q): expected escape to be rejected, got: %s", fp, res.Text())
		}
	}
}

func TestRunCommandRunsInsideSandboxRoot(t *testing.T) {
	root := t.TempDir()
	client := newTestClient(t, root)
	ctx := context.Background()

	res, err := client.CallTool(ctx, "run_command", map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("run_command failed: %s", res.Text())
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	want := "exit=0\nstdout:\n" + realRoot + "\n\nstderr:\n"
	if res.Text() != want {
		t.Errorf("run_command pwd output = %q, want %q", res.Text(), want)
	}
}

// TestRunCommandIsKilledWhenItsCallersCtxIsCancelled is bwsalmon/agents#346's
// own regression test for the wiring gap it closed: every Tool.Handler
// used to build its own context.Background() internally, so cancelling
// the ctx driving an agent's run had no way to reach a run_command call
// already in flight. NewInProcess now binds Serve to the ctx it was
// constructed with, and run_command's Handler now runs its
// exec.CommandContext against the ctx it's actually called with instead
// of a fresh one -- this proves both links of that chain by cancelling a
// real, currently-sleeping `sleep 30` from outside the call that started
// it and checking it dies promptly rather than running to completion.
func TestRunCommandIsKilledWhenItsCallersCtxIsCancelled(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not installed")
	}
	root := t.TempDir()
	registry := NewRegistry()
	registry.Register(NewSandboxTools(root)...)

	ctx, cancel := context.WithCancel(context.Background())
	client := NewInProcess(ctx, registry)
	defer client.Close()

	callDone := make(chan struct{})
	go func() {
		defer close(callDone)
		client.CallTool(ctx, "run_command", map[string]any{"command": "touch running; sleep 30"})
	}()

	// Wait for the command to be running before cancelling it, because
	// cancelling one that had not started yet would prove nothing about
	// killing one that had. The command says so itself, which is the point:
	// giving it 200ms and hoping was a guess about how quickly this machine
	// forks a bash, and a loaded CI host is not quick (grain/task-431).
	awaitFile(t, filepath.Join(root, "running"))
	cancel()

	// The command it started sleeps for 30 seconds, so a call that comes
	// back at all came back because the sleep was killed rather than
	// waited out. The bound is a hang-guard and nothing more: it says the
	// call returned, not that the kill was quick.
	select {
	case <-callDone:
	case <-time.After(20 * time.Second):
		t.Fatal("run_command never returned after its ctx was cancelled -- the sleep it started was never killed")
	}
}

// awaitFile blocks until path exists, which is how a test waits for a
// command it started to have got somewhere -- a signal the command itself
// raises. The bound is a hang-guard, not a budget: the wait is over when
// the file appears, however long the machine took over it.
func awaitFile(t *testing.T, path string) {
	t.Helper()
	const limit = 30 * time.Second
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s never appeared within %s: the command never ran", path, limit)
}

// TestRunCommandAppliesDefaultTimeoutWhenNoneGiven is bwsalmon/agents#575's
// own regression test: a run_command call that omits "timeout" entirely
// used to run with no server-side bound at all, so a caller that forgot
// to pass one (or an unbounded command like a `grep -r` from $HOME) could
// wedge the tool call, and the sandbox slot behind it, indefinitely.
// defaultRunCommandTimeout is shrunk for this test only, so it can prove
// the bound applies without actually waiting out the real 5-minute
// default.
func TestRunCommandAppliesDefaultTimeoutWhenNoneGiven(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not installed")
	}
	old := defaultRunCommandTimeout
	defaultRunCommandTimeout = 200 * time.Millisecond
	t.Cleanup(func() { defaultRunCommandTimeout = old })

	client := newTestClient(t, t.TempDir())
	start := time.Now()
	res, err := client.CallTool(context.Background(), "run_command", map[string]any{"command": "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("run_command with no timeout took %s, want near-instant given the shrunk default, not close to the full 30s sleep", elapsed)
	}
	if !res.IsError {
		t.Error("run_command killed by the default timeout: want IsError true")
	}
}

func TestRunCommandSeesGitCredentialsConfiguredForItsSandboxRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	if err := ConfigureGitCredentials(root, "http://proxy.example:8080/owner/repo.git", "sandbox-token", GitIdentity{}); err != nil {
		t.Fatal(err)
	}
	client := newTestClient(t, root)
	ctx := context.Background()

	// run_command sets HOME=root (sandbox_tools.go), so git here reads
	// root/.gitconfig and root/.git-credentials exactly as it would in a
	// real sandbox reading its own home directory -- proving the two
	// pieces (ConfigureGitCredentials' files, run_command's HOME scoping)
	// actually agree with each other rather than merely compiling.
	res, err := client.CallTool(ctx, "run_command", map[string]any{
		"command": "git config --get credential.helper && " +
			"git credential fill <<<'protocol=http\nhost=proxy.example:8080\n' | grep ^password=",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("run_command failed: %s", res.Text())
	}
	if !strings.Contains(res.Text(), "password=sandbox-token") {
		t.Errorf("git did not resolve the configured credential: %s", res.Text())
	}
}

func TestMockToolsRecordWithoutAnyNetworkEffect(t *testing.T) {
	root := t.TempDir()
	registry := NewRegistry()
	registry.Register(NewSandboxTools(root)...)
	sink := &MockSink{}
	registry.Register(NewMockTools(sink, nil)...)
	ctx := context.Background()
	client := NewInProcess(ctx, registry)
	defer client.Close()

	if _, err := client.CallTool(ctx, "ask_question", map[string]any{"question": "which branch?"}); err != nil {
		t.Fatal(err)
	}
	if got := sink.Question(); got != "which branch?" {
		t.Errorf("Question() = %q", got)
	}

	if _, err := client.CallTool(ctx, "comment_on_issue", map[string]any{"comment": "done"}); err != nil {
		t.Fatal(err)
	}
	if got := sink.Comment(); got != "done" {
		t.Errorf("Comment() = %q", got)
	}

	if _, err := client.CallTool(ctx, "propose_task", map[string]any{
		"title": "follow up", "body": "do the other thing", "id": "a",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(ctx, "propose_task", map[string]any{
		"title":      "second",
		"body":       "depends on first",
		"depends_on": []any{"a"},
		"auto_merge": false,
		// One field's value is a string and another's is an array, the
		// two shapes a declaration can promise (grain/task-105, and
		// model.CustomField.Multiple for the second). Which of them is
		// allowed is decided against the declarations later and
		// elsewhere; what is recorded here is what the call said.
		"custom_fields": map[string]any{
			"customer":   "acme",
			"components": []any{"store", "prompt"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	tasks := sink.ProposedTasks()
	if len(tasks) != 2 {
		t.Fatalf("got %d proposed tasks, want 2", len(tasks))
	}
	if tasks[1].DependsOn[0] != "a" {
		t.Errorf("second task depends_on = %v, want [a]", tasks[1].DependsOn)
	}
	if !slices.Equal(tasks[1].CustomFields["customer"], []string{"acme"}) ||
		!slices.Equal(tasks[1].CustomFields["components"], []string{"store", "prompt"}) {
		t.Errorf("second task custom_fields = %v, want a string read as a set of one "+
			"and an array read as itself", tasks[1].CustomFields)
	}
	if tasks[0].CustomFields != nil {
		t.Errorf("first task custom_fields = %v, want none -- it said nothing", tasks[0].CustomFields)
	}
	// Unset and false are different answers -- see ProposedTask.
	if tasks[0].AutoMerge != nil {
		t.Errorf("first task auto_merge = %v, want unset -- it asked for nothing", *tasks[0].AutoMerge)
	}
	if tasks[1].AutoMerge == nil || *tasks[1].AutoMerge {
		t.Errorf("second task auto_merge = %v, want an explicit false", tasks[1].AutoMerge)
	}

	if _, err := client.CallTool(ctx, "propose_task", map[string]any{
		"title":           "third",
		"body":            "specialized model task",
		"model":           "gemini-3.1-pro",
		"agent_framework": "antigravity",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(ctx, "propose_task", map[string]any{
		"title":    "fourth",
		"body":     "investigation task",
		"no_repo":  true,
		"at_front": true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(ctx, "propose_task", map[string]any{
		"title":           "fifth",
		"body":            "template task",
		"template_id":     "tmpl-1",
		"template_fields": map[string]any{"env": "staging"},
	}); err != nil {
		t.Fatal(err)
	}
	allTasks := sink.ProposedTasks()
	if len(allTasks) != 5 {
		t.Fatalf("got %d proposed tasks, want 5", len(allTasks))
	}
	if allTasks[2].Model != "gemini-3.1-pro" || allTasks[2].AgentFramework != "antigravity" {
		t.Errorf("third task model/framework = %q/%q, want gemini-3.1-pro/antigravity",
			allTasks[2].Model, allTasks[2].AgentFramework)
	}
	if !allTasks[3].NoRepo {
		t.Errorf("fourth task no_repo = %v, want true", allTasks[3].NoRepo)
	}
	if allTasks[3].AtFront == nil || !*allTasks[3].AtFront {
		t.Errorf("fourth task at_front = %v, want true", allTasks[3].AtFront)
	}
	if allTasks[4].TemplateID != "tmpl-1" {
		t.Errorf("fifth task template_id = %q, want tmpl-1", allTasks[4].TemplateID)
	}
	if allTasks[4].TemplateFields["env"] != "staging" {
		t.Errorf("fifth task template_fields = %v, want env: staging", allTasks[4].TemplateFields)
	}

	res, err := client.CallTool(ctx, "add_review_comment", map[string]any{"body": "nit", "path": "x.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("expected path-without-line to be rejected, got: %s", res.Text())
	}
	for _, bad := range []map[string]any{
		{"body": "   ", "path": "x.go", "line": float64(1)},
		{"body": "nit", "path": "  ", "line": float64(12)},
		{"body": "nit", "path": "x.go", "line": float64(0)},
		{"body": "nit", "path": "x.go", "line": float64(-3)},
	} {
		res, err := client.CallTool(ctx, "add_review_comment", bad)
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatalf("expected invalid comment args %v to be rejected, got: %s", bad, res.Text())
		}
	}
	if _, err := client.CallTool(ctx, "add_review_comment", map[string]any{
		"body": "nit", "path": "x.go", "line": float64(12),
	}); err != nil {
		t.Fatal(err)
	}
	comments := sink.ReviewComments()
	if len(comments) != 1 || comments[0].Line != 12 || comments[0].Path != "x.go" {
		t.Errorf("ReviewComments() = %+v", comments)
	}
}

func TestUnknownToolIsAnRPCError(t *testing.T) {
	client := newTestClient(t, t.TempDir())
	_, err := client.CallTool(context.Background(), "does_not_exist", nil)
	if err == nil {
		t.Fatal("expected an error calling an unregistered tool")
	}
}

// An empty old_string used to be taken literally: strings.Count finds a
// match between every character, so replace_all interleaved new_string
// through the whole file and a plain edit refused with a count of
// phantom matches. Found by hand-driving this server the way a framework
// does (task 244) -- the file it silently rewrote had asked for one
// change.
func TestEditFileRefusesAnEmptyOldString(t *testing.T) {
	root := t.TempDir()
	client := newTestClient(t, root)
	ctx := context.Background()

	if _, err := client.CallTool(ctx, "write_file", map[string]any{
		"file_path": "f.txt", "content": "abc",
	}); err != nil {
		t.Fatal(err)
	}

	for _, replaceAll := range []bool{false, true} {
		res, err := client.CallTool(ctx, "edit_file", map[string]any{
			"file_path": "f.txt", "old_string": "", "new_string": "X", "replace_all": replaceAll,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatalf("replace_all=%v: empty old_string was accepted: %s", replaceAll, res.Text())
		}
		if !strings.Contains(res.Text(), "old_string") {
			t.Errorf("replace_all=%v: error %q does not say which argument is wrong", replaceAll, res.Text())
		}
	}

	data, err := os.ReadFile(filepath.Join(root, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abc" {
		t.Errorf("file content = %q, want it untouched", string(data))
	}
}
