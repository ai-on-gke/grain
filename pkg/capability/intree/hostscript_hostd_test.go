//go:build linux

package intree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/portforward"
	"github.com/bwsalmon/grain/pkg/capability/protocol"
	"github.com/bwsalmon/grain/pkg/hostd"
)

// startHostScriptHostd serves a real grain hostd whose allowlist offers
// this test binary as the capability entry "host-script".
func startHostScriptHostd(t *testing.T, data string) hostd.Client {
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
	body := fmt.Sprintf("capabilities:\n  host-script:\n    command: %s\n    env: [%s, %s]\n    paths_under: [%s]\n", exe, hostScriptHelperEnv, hostScriptHelperData, data)
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
	return hostd.Client{Socket: srv.Socket}
}

// Under run: host the server is started by grain hostd and its scripts
// run as the user hostd runs as, with the user's ambient identity; its
// pid is known, so its port can be forwarded.
func TestHostScriptRunsOnTheHostThroughHostd(t *testing.T) {
	data := t.TempDir()
	client := startHostScriptHostd(t, data)
	id := "https://github.com/bwsalmon/grain/capabilities/host-script"
	cfg := protocol.ServerConfig{
		Command: "host-script", Run: protocol.RunHost,
		Env: map[string]string{hostScriptHelperEnv: "1", hostScriptHelperData: data},
		Config: map[string]any{"scripts": []any{
			map[string]any{"name": "whoami", "description": "reports who it runs as", "script": `id -u; printf '%s\n' "$1"`, "timeout": 10},
		}},
	}
	if err := cfg.Validate(id); err != nil {
		t.Fatal(err)
	}
	ps, err := protocol.Attach(context.Background(), id, cfg, func(string) (string, bool) { return "", false }, protocol.DialOptions{Hostd: &client, Stderr: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()
	if pid, _ := ps.ServiceProcess(); pid <= 0 || syscall.Kill(pid, 0) != nil {
		t.Errorf("ServiceProcess() pid = %d: a run: host server's ports could not be forwarded", pid)
	}
	f := &hostScriptFixture{t: t, ps: ps, data: data}
	cmd, resp := f.grant("e1-42-1-0", "whoami")
	if len(resp.Ports) != 1 {
		t.Fatalf("grant ports = %+v, want 1", resp.Ports)
	}
	svc, err := portforward.ServiceFor(ps.ServiceProcess())
	if err != nil {
		t.Fatal(err)
	}
	inode, err := portforward.ListenerInode(resp.Ports[0].HostPort)
	if err != nil {
		t.Fatalf("ListenerInode(%d): %v", resp.Ports[0].HostPort, err)
	}
	if owner, err := client.SocketOwner(context.Background(), inode); err != nil || !svc.Contains(owner) {
		t.Errorf("hostd SocketOwner(%d) = %d, %v; want in service %+v", inode, owner, err, svc)
	}
	relay := portforward.NewRelay()
	relay.InodeOwner = client.SocketOwner
	fwd, err := relay.Open(context.Background(), resp.Ports[0].HostPort, svc)
	if err != nil {
		t.Fatalf("relay.Open for run: host server port: %v", err)
	}
	defer fwd.Close()
	got := call(t, cmd, "$(id -u) literal")
	want := strconv.Itoa(os.Getuid()) + "\n$(id -u) literal\n"
	if got.code != 0 || got.stdout != want {
		t.Errorf("got %+v, want stdout %q: the script runs as the user, with its argument literal", got, want)
	}
	if _, err := ps.Release(context.Background(), "host-script", "e1-42-1-0"); err != nil {
		t.Fatal(err)
	}
	if got := call(t, cmd); got.code != 125 {
		t.Errorf("after release: %+v", got)
	}
}
