package antigravity

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/agent"
)

type mockRemoteHost struct {
	agent.LocalHost
	dirs        []string
	runs        []agent.Command
	mkdirAllErr error
}

func (m *mockRemoteHost) IsLocal() bool    { return false }
func (m *mockRemoteHost) Unconfined() bool { return true }

func (m *mockRemoteHost) MkdirAll(ctx context.Context, path string, perm fs.FileMode) error {
	m.dirs = append(m.dirs, path)
	if m.mkdirAllErr != nil && strings.Contains(path, "sessions/antigravity") {
		return m.mkdirAllErr
	}
	return nil
}

func (m *mockRemoteHost) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	return nil
}

func (m *mockRemoteHost) RemoveAll(ctx context.Context, path string) error {
	return nil
}

func (m *mockRemoteHost) Run(ctx context.Context, cmd agent.Command, tee io.Writer) (string, error) {
	m.runs = append(m.runs, cmd)
	return "", nil
}

func TestRemoteHostUsesUserScopedSessionDir(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-1"), successResult("done"))},
	}}
	rh := &mockRemoteHost{}
	storeDir := t.TempDir()
	f := newFramework(r, "/usr/local/bin/grain", WithHost(rh), withSessionsIn(storeDir))

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do something", SandboxRoot: t.TempDir(), TaskID: "101",
	})
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if result.ResumableSession != "conv-1" {
		t.Errorf("ResumableSession = %q, want conv-1", result.ResumableSession)
	}

	// Verify that HOME was constructed under ~/.grain/sessions/antigravity/101, not storeDir
	home := homeOf(r.calls[0].env)
	wantPrefix := filepath.Join("~/.grain", "sessions", "antigravity", "101")
	if home != wantPrefix && home != "~/.grain/sessions/antigravity/101" {
		t.Errorf("HOME = %q, want remote user-scoped path %q", home, wantPrefix)
	}
	if strings.Contains(home, storeDir) {
		t.Errorf("HOME %q contains controller storeDir %q", home, storeDir)
	}

	// Verify that pruning was executed on remote host
	if len(rh.runs) == 0 {
		t.Fatalf("no pruning command was executed on remote host")
	}
	pruneCmd := rh.runs[0]
	if pruneCmd.Path != "/bin/sh" {
		t.Errorf("pruning command path = %q, want /bin/sh", pruneCmd.Path)
	}
	if len(pruneCmd.Args) < 2 || !strings.Contains(pruneCmd.Args[1], "sessions/antigravity") {
		t.Errorf("pruning command args = %v, want script targeting remote session base", pruneCmd.Args)
	}
}

func TestRemoteHostWithCustomRemoteSessionRoot(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-2"), successResult("done"))},
	}}
	rh := &mockRemoteHost{}
	customRoot := "/opt/remote/sessions"
	f := newFramework(r, "/usr/local/bin/grain", WithHost(rh), withSessionsIn(t.TempDir()), WithRemoteSessionRoot(customRoot))

	_, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do something", SandboxRoot: t.TempDir(), TaskID: "202",
	})
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}

	home := homeOf(r.calls[0].env)
	wantHome := filepath.Join(customRoot, "antigravity", "202")
	if home != wantHome {
		t.Errorf("HOME = %q, want custom remote path %q", home, wantHome)
	}
}

func TestRemoteHostCrossHostResumptionFallback(t *testing.T) {
	// A task was previously run on Host A and produced "conv-old".
	// It is now resumed on RemoteHost B where "conv-old" is not present in agy's store.
	// agy opens "conv-new" and proceeds with the task.
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-new"), successResult("resumed afresh"))},
	}}
	rh := &mockRemoteHost{}
	f := newFramework(r, "/usr/local/bin/grain", WithHost(rh), withSessionsIn(t.TempDir()))

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do something", SandboxRoot: t.TempDir(), TaskID: "303", ResumeSession: "conv-old",
	})
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if result.ResumableSession != "conv-new" {
		t.Errorf("ResumableSession = %q, want conv-new", result.ResumableSession)
	}
	// Verify that suspendedResumeLostNote was appended
	if !strings.Contains(result.Transcript, "the conversation a previous run of this task left suspended was no longer in agy's store") {
		t.Errorf("transcript does not note that suspended session was lost/unavailable on host:\n%s", result.Transcript)
	}
}

func TestRemoteHostMkdirAllFailureFallsBackToTempHome(t *testing.T) {
	r := &attemptRunner{replies: []attemptReply{
		{stdout: stream(conversationInit("conv-tmp"), successResult("done"))},
	}}
	rh := &mockRemoteHost{mkdirAllErr: errors.New("permission denied")}
	f := newFramework(r, "/usr/local/bin/grain", WithHost(rh), withSessionsIn(t.TempDir()))

	result, err := f.Run(context.Background(), agent.RunConfig{
		Prompt: "do something", SandboxRoot: t.TempDir(), TaskID: "404", ResumeSession: "conv-old",
	})
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	// Fallback to temp home means persistent = false, so no ResumableSession
	if result.ResumableSession != "" {
		t.Errorf("ResumableSession = %q, want empty (temp home fallback)", result.ResumableSession)
	}
	// And first attempt should not attempt to resume conv-old
	if got := flagValue(r.calls[0].args, "--conversation"); got != "" {
		t.Errorf("attempt tried to resume %q with temp home", got)
	}
}
