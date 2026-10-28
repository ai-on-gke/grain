//go:build linux

package protocol

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

// startTestHostd serves a real grain hostd whose allowlist offers this
// test binary as the capability entry "testcap", taking the two variables
// the helper server reads.
func startTestHostd(t *testing.T) hostd.Client {
	t.Helper()
	_, client := startTestHostdServer(t)
	return client
}

// startTestHostdServer is startTestHostd, handing back the server too.
func startTestHostdServer(t *testing.T) (*hostd.Server, hostd.Client) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	// Not t.TempDir: a unix socket's path has to fit in 108 bytes.
	dir, err := os.MkdirTemp("", "hostd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	allow := filepath.Join(dir, "hostd.yaml")
	body := fmt.Sprintf("capabilities:\n  testcap:\n    command: %s\n    env: [%s, TEST_MINTER_KEY]\n", exe, helperEnv)
	if err := os.WriteFile(allow, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &hostd.Server{
		Socket:          filepath.Join(dir, "hostd.sock"),
		Allowlist:       allow,
		AllowlistOwners: []uint32{uint32(os.Getuid())},
		StateDir:        filepath.Join(dir, "state"),
		Logf:            func(string, ...any) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	for i := 0; ; i++ {
		if _, err := os.Stat(srv.Socket); err == nil {
			break
		}
		if i > 500 {
			t.Fatal("hostd never listened")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return srv, hostd.Client{Socket: srv.Socket}
}

// A run: host server is started by grain hostd, with its stdio carried
// over hostd's socket, and behaves exactly as a spawned one does.
func TestHostCapabilityServerThroughHostdEndToEnd(t *testing.T) {
	client := startTestHostd(t)
	id := "https://github.com/bwsalmon/grain/capabilities/test"
	cfg := ServerConfig{Command: "testcap", Run: RunHost, Env: map[string]string{helperEnv: "1", "TEST_MINTER_KEY": "$secret:minter"}}
	if err := cfg.Validate(id); err != nil {
		t.Fatal(err)
	}
	secrets := func(name string) (string, bool) {
		return map[string]string{"minter": "root-key"}[name], name == "minter"
	}
	srv, err := Attach(context.Background(), id, cfg, secrets, DialOptions{Hostd: &client, Stderr: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	exerciseServer(t, srv)
	// hostd reports the pid it started, which the server's ports must
	// belong to (§5.3.2): a run: host server can own ports.
	if pid, _ := srv.ServiceProcess(); pid <= 0 || pid == os.Getpid() || syscall.Kill(pid, 0) != nil {
		t.Errorf("ServiceProcess() pid = %d, want the live server hostd started", pid)
	}
	closed := make(chan struct{})
	go func() { srv.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(20 * time.Second):
		t.Fatal("closing a server hostd started never returned")
	}
}

// hostd's drain for its own upgrade ends a run: host server once no run is
// left, and the session says so (Ended), which is how the capability
// registry knows to start it again on the new hostd rather than keep
// handing out a session every call over fails.
func TestAHostdDrainEndsAHostCapabilityServersSession(t *testing.T) {
	hd, client := startTestHostdServer(t)
	id := "https://github.com/bwsalmon/grain/capabilities/test"
	cfg := ServerConfig{Command: "testcap", Run: RunHost, Env: map[string]string{helperEnv: "1", "TEST_MINTER_KEY": "$secret:minter"}}
	secrets := func(name string) (string, bool) {
		return map[string]string{"minter": "root-key"}[name], name == "minter"
	}
	srv, err := Attach(context.Background(), id, cfg, secrets, DialOptions{Hostd: &client, Stderr: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if srv.Ended() {
		t.Fatal("a server hostd has just started reports ended")
	}
	hd.Drain()
	deadline := time.Now().Add(20 * time.Second)
	for !srv.Ended() {
		if time.Now().After(deadline) {
			t.Fatal("hostd drained, but the capability server's session never reported ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := srv.Refresh(context.Background()); err == nil {
		t.Error("tools/list over a session hostd's drain ended succeeded")
	}
}

func TestHostCapabilityServerIsRefusedUnlessHostdOffersIt(t *testing.T) {
	client := startTestHostd(t)
	id := "https://github.com/bwsalmon/grain/capabilities/test"
	ctx := context.Background()
	none := func(string) (string, bool) { return "", false }

	cfg := ServerConfig{Command: "othercap", Run: RunHost}
	if _, err := Attach(ctx, id, cfg, none, DialOptions{Hostd: &client}); err == nil ||
		!strings.Contains(err.Error(), `offers no capability entry "othercap"`) {
		t.Errorf("an entry hostd does not offer: err = %v", err)
	}
	gone := hostd.Client{Socket: filepath.Join(t.TempDir(), "gone.sock")}
	cfg.Command = "testcap"
	if _, err := Attach(ctx, id, cfg, none, DialOptions{Hostd: &gone}); err == nil ||
		!strings.Contains(err.Error(), "not reachable") {
		t.Errorf("no hostd: err = %v", err)
	}
	if _, err := Attach(ctx, id, cfg, none, DialOptions{}); err == nil ||
		!strings.Contains(err.Error(), "no hostd") {
		t.Errorf("a daemon with no hostd at all: err = %v", err)
	}
}

func TestValidateRunHost(t *testing.T) {
	id := "https://github.com/bwsalmon/grain/capabilities/test"
	for _, bad := range []ServerConfig{
		{Command: "/usr/local/bin/corp-search", Run: RunHost},
		{ServerURL: "http://127.0.0.1:9802/mcp", Run: RunHost},
		{Command: "corp-search", Run: "sideways"},
	} {
		if err := bad.Validate(id); err == nil {
			t.Errorf("%+v validated", bad)
		}
	}
	if err := (ServerConfig{Command: "corp-search", Run: RunHost}).Validate(id); err != nil {
		t.Error(err)
	}
}

// Exec'ing a file-capability binary (Dockerfile's setcap
// cap_net_bind_service on /usr/local/bin/grain) leaves the process
// non-dumpable, which makes /proc/<pid>/fd root-owned 0500 and unreadable
// even by a same-uid process. NewCapabilityServer calls MakeDumpable so a
// same-uid relay or grain hostd can inspect the server's fds without
// CAP_SYS_PTRACE and CAP_DAC_READ_SEARCH.
func TestMakeDumpableRestoresProcFDAccess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root owns /proc/<pid>/fd either way")
	}
	defer MakeDumpable()
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0); errno != 0 {
		t.Fatalf("clearing dumpable: %v", errno)
	}
	fdDir := fmt.Sprintf("/proc/%d/fd", os.Getpid())
	info, err := os.Stat(fdDir)
	if err != nil {
		t.Fatal(err)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 {
		t.Fatalf("with dumpable=0, %s uid = %d, want 0 (root)", fdDir, st.Uid)
	}
	if _, err := NewCapabilityServer("dumpable-test"); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(fdDir)
	if err != nil {
		t.Fatal(err)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
		t.Fatalf("after NewCapabilityServer, %s uid = %d, want %d", fdDir, st.Uid, os.Geteuid())
	}
}
