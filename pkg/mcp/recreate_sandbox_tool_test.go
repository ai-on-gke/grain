package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeRecreator struct {
	report SandboxRecreationReport
	err    error
	calls  int
}

func (f *fakeRecreator) RecreateSandbox(context.Context) (SandboxRecreationReport, error) {
	f.calls++
	return f.report, f.err
}

// recreateSandbox calls the tool the way a client would: by name, off the
// registry, with whatever arguments the model sent.
func recreateSandbox(t *testing.T, recreator SandboxRecreator, args map[string]any) Result {
	t.Helper()
	tools := NewRecreateSandboxTools(recreator)
	if len(tools) != 1 || tools[0].Name != "recreate_sandbox" {
		t.Fatalf("NewRecreateSandboxTools returned %+v, want one recreate_sandbox tool", tools)
	}
	return tools[0].Handler(context.Background(), args)
}

func TestRecreateSandboxReportsWhatCameBack(t *testing.T) {
	recreator := &fakeRecreator{report: SandboxRecreationReport{
		Sandbox:     "12-1",
		CheckoutDir: "work",
		Restored: []string{
			"its git credentials for grain's git proxy",
			"a fresh clone of acme/widgets at ./work, with grain/task-12 checked out",
		},
	}}

	res := recreateSandbox(t, recreator, nil)
	if res.IsError {
		t.Fatalf("recreate_sandbox reported an error: %s", res.Text)
	}
	if recreator.calls != 1 {
		t.Errorf("recreator called %d times, want 1", recreator.calls)
	}
	for _, want := range []string{
		"12-1",
		"destroyed and rebuilt",
		"its git credentials for grain's git proxy",
		"a fresh clone of acme/widgets",
		"./work",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text = %q, want it to contain %q", res.Text, want)
		}
	}
}

// The point of the answer is that the agent knows what it is now sitting
// in front of. A run whose credentials did not come back cannot push and
// a run whose repo did not clone has an empty directory, so both have to
// be legible as failures rather than folded in with what did work.
func TestRecreateSandboxSaysWhatCouldNotBePutBack(t *testing.T) {
	res := recreateSandbox(t, &fakeRecreator{report: SandboxRecreationReport{
		Sandbox:  "12-1",
		Restored: []string{"its git credentials for grain's git proxy"},
		Warnings: []string{"this task's repo could not be cloned again: the proxy refused"},
	}}, nil)
	if res.IsError {
		t.Fatalf("a rebuilt sandbox is not a failed call: %s", res.Text)
	}
	if !strings.Contains(res.Text, "could not put back") {
		t.Errorf("text = %q, want the warnings called out as their own section", res.Text)
	}
	if !strings.Contains(res.Text, "the proxy refused") {
		t.Errorf("text = %q, want the reason the clone is missing", res.Text)
	}
}

// A deployment with nothing to put back (a task with no repo, no
// attachments and no proxy) still gets a sandbox, and saying so plainly
// is better than an answer that lists nothing and reads like a failure.
func TestRecreateSandboxWithNothingToRestore(t *testing.T) {
	res := recreateSandbox(t, &fakeRecreator{report: SandboxRecreationReport{Sandbox: "12-1"}}, nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "bare, empty sandbox") {
		t.Errorf("text = %q, want it to say the sandbox is bare", res.Text)
	}
}

func TestRecreateSandboxReportsAFailureToRebuild(t *testing.T) {
	res := recreateSandbox(t, &fakeRecreator{
		err: errors.New("orchestrator: rebuilding run 12-1's sandbox: granule vm create failed"),
	}, nil)
	if !res.IsError {
		t.Fatal("expected an error result")
	}
	if !strings.Contains(res.Text, "granule vm create failed") {
		t.Errorf("text = %q, want the reason the sandbox was not rebuilt", res.Text)
	}
}

// A nil recreator is how a framework's allowedTools enumerates this
// tool's name without holding a live one (agent/claude's own
// allowedTools), so building the tool must work -- and calling it must
// refuse rather than panic.
func TestRecreateSandboxWithNoRecreatorRefuses(t *testing.T) {
	res := recreateSandbox(t, nil, nil)
	if !res.IsError {
		t.Fatal("expected a nil recreator to refuse the call")
	}
	if !strings.Contains(res.Text, "Work with the sandbox you have") {
		t.Errorf("text = %q, want it to say what to do instead", res.Text)
	}
}

// The tool takes no arguments at all: a run can only ever rebuild its
// own sandbox, and a schema that admitted a name would invite an agent
// to try naming somebody else's.
func TestRecreateSandboxTakesNoArguments(t *testing.T) {
	tool := NewRecreateSandboxTools(nil)[0]
	props, ok := tool.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("InputSchema = %+v, want a properties object", tool.InputSchema)
	}
	if len(props) != 0 {
		t.Errorf("properties = %+v, want none", props)
	}
	if tool.InputSchema["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false", tool.InputSchema["additionalProperties"])
	}
}

// The description is the only place an agent learns that calling this
// throws its uncommitted work away, and it reads that before it decides
// to call -- so the warning has to be in there.
func TestRecreateSandboxDescriptionWarnsAboutLosingWork(t *testing.T) {
	tool := NewRecreateSandboxTools(nil)[0]
	for _, want := range []string{"DESTROYED", "uncommitted", "push"} {
		if !strings.Contains(tool.Description, want) {
			t.Errorf("description = %q, want it to mention %q", tool.Description, want)
		}
	}
}

// Registered alongside the rest, it is reachable by name over the same
// JSON-RPC surface every other tool is -- which is all a real client
// (claude's --mcp-config fork) ever gets.
func TestRecreateSandboxIsCallableThroughTheRegistry(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewRecreateSandboxTools(&fakeRecreator{report: SandboxRecreationReport{
		Sandbox: "12-1", CheckoutDir: "work",
	}})...)
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "recreate_sandbox" {
		t.Fatalf("ListTools = %+v, want just recreate_sandbox", tools)
	}
	res, err := client.CallTool(context.Background(), "recreate_sandbox", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("recreate_sandbox reported an error: %s", res.Text())
	}
	if !strings.Contains(res.Text(), "12-1") {
		t.Errorf("text = %q, want the sandbox it rebuilt", res.Text())
	}
}
