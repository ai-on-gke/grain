package orchestrator

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/tunnel/hostport"
)

// persistentTestRepo makes a git repository holding files, for a
// persistent sandbox to clone, and returns its path.
func persistentTestRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "."},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

func fastPersistentBackoff(t *testing.T) {
	base, stable := persistentBackoffBase, persistentStableAfter
	persistentBackoffBase, persistentStableAfter = 20*time.Millisecond, time.Hour
	t.Cleanup(func() { persistentBackoffBase, persistentStableAfter = base, stable })
}

func newTestPersistentSupervisor(repo string) *PersistentSandboxSupervisor {
	s := NewPersistentSandboxSupervisor()
	s.RepoURL = func(string) string { return repo }
	return s
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func persistentStatus(s *PersistentSandboxSupervisor, def model.PersistentSandbox) model.PersistentSandboxStatus {
	return s.List([]model.PersistentSandbox{def})[0]
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestPersistentSandboxRestartsWhenCommandExits(t *testing.T) {
	fastPersistentBackoff(t)
	repo := persistentTestRepo(t, map[string]string{"run.sh": "#!/bin/sh\necho started from checkout\nexit 3\n"})
	s := newTestPersistentSupervisor(repo)
	t.Cleanup(s.Close)
	def := model.PersistentSandbox{Name: "crashy", Repo: "acme/crashy", Ref: "main", Command: "./run.sh", Enabled: true}

	s.Reconcile(context.Background(), Deps{Sandboxes: NewHostSandboxes(t.TempDir())}, []model.PersistentSandbox{def})

	waitFor(t, "three restarts", func() bool { return persistentStatus(s, def).Restarts >= 3 })
	st := persistentStatus(s, def)
	if st.LastError != "exited with status 3" {
		t.Errorf("LastError = %q, want the exit status", st.LastError)
	}
	var exits int
	for _, ev := range st.Events {
		if ev.Kind == "exit" {
			exits++
		}
	}
	if exits == 0 {
		t.Errorf("no exit events recorded: %+v", st.Events)
	}
	logs, ok := s.Logs("crashy")
	if !ok || !strings.Contains(logs, "started from checkout") {
		t.Errorf("Logs = %q, %v; want the command's output", logs, ok)
	}
	if got := PersistentBackoff(4); got != 8*persistentBackoffBase {
		t.Errorf("PersistentBackoff(4) = %s, want 8x base", got)
	}
}

func TestPersistentSandboxStartsAgainAfterDaemonRestart(t *testing.T) {
	ctx := context.Background()
	st := openTestStoreForOrchestrator(t)
	repo := persistentTestRepo(t, map[string]string{"serve.sh": "#!/bin/sh\necho up\nexec sleep 600\n"})
	def := model.PersistentSandbox{Name: "staging", Repo: "acme/app", Command: "./serve.sh", Enabled: true}
	if err := st.PutPersistentSandbox(ctx, def); err != nil {
		t.Fatal(err)
	}
	sandboxes := NewHostSandboxes(t.TempDir())

	for boot := 1; boot <= 2; boot++ {
		// Each boot is a fresh daemon: a new supervisor, reconciled from
		// the store with nothing carried over but the definition.
		s := newTestPersistentSupervisor(repo)
		deps := Deps{Store: st, Sandboxes: sandboxes, PersistentSandboxes: s}
		if err := reconcilePersistentSandboxes(ctx, deps, time.Now()); err != nil {
			t.Fatal(err)
		}
		waitFor(t, fmt.Sprintf("boot %d to run the command", boot), func() bool {
			logs, _ := s.Logs("staging")
			return persistentStatus(s, def).State == PersistentRunning && strings.Contains(logs, "up")
		})
		if got := persistentStatus(s, def).Restarts; got != 0 {
			t.Errorf("boot %d: Restarts = %d, want 0 for a command that is still running", boot, got)
		}
		s.Close()
		if got := persistentStatus(s, def).State; got != PersistentStopped {
			t.Errorf("boot %d: after Close, State = %q", boot, got)
		}
	}
}

func TestPersistentSandboxStopStartAndRestart(t *testing.T) {
	fastPersistentBackoff(t)
	repo := persistentTestRepo(t, map[string]string{"serve.sh": "#!/bin/sh\necho up\nexec sleep 600\n"})
	s := newTestPersistentSupervisor(repo)
	t.Cleanup(s.Close)
	def := model.PersistentSandbox{Name: "ctl", Repo: "acme/app", Command: "./serve.sh", Enabled: true}
	deps := Deps{Sandboxes: NewHostSandboxes(t.TempDir())}
	s.Reconcile(context.Background(), deps, []model.PersistentSandbox{def})
	waitFor(t, "running", func() bool { return persistentStatus(s, def).State == PersistentRunning })

	if err := s.Restart("ctl"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a restart", func() bool {
		st := persistentStatus(s, def)
		return st.Restarts == 1 && st.State == PersistentRunning
	})

	s.Stop("ctl")
	s.Reconcile(context.Background(), deps, []model.PersistentSandbox{def})
	if got := persistentStatus(s, def).State; got != PersistentStopped {
		t.Fatalf("a stopped sandbox was started again by reconcile: %q", got)
	}
	s.Start("ctl")
	s.Reconcile(context.Background(), deps, []model.PersistentSandbox{def})
	waitFor(t, "running after Start", func() bool { return persistentStatus(s, def).State == PersistentRunning })

	if err := s.Rebuild("ctl"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a rebuild", func() bool {
		for _, ev := range persistentStatus(s, def).Events {
			if ev.Kind == "rebuild" {
				return persistentStatus(s, def).State == PersistentRunning
			}
		}
		return false
	})
}

func TestPersistentSandboxHostPortForwarding(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is needed for a listening service")
	}
	repo := persistentTestRepo(t, map[string]string{"hello.txt": "hello from staging\n"})
	sandboxPort, hostPort := freePort(t), freePort(t)
	s := newTestPersistentSupervisor(repo)
	ports := hostport.New("127.0.0.1")
	s.Ports = ports
	t.Cleanup(ports.Close)
	t.Cleanup(s.Close)
	def := model.PersistentSandbox{
		Name: "web", Repo: "acme/web", Enabled: true,
		Command:     fmt.Sprintf("exec python3 -m http.server %d --bind 127.0.0.1", sandboxPort),
		SandboxPort: sandboxPort, HostPort: hostPort,
	}
	s.Reconcile(context.Background(), Deps{Sandboxes: NewHostSandboxes(t.TempDir())}, []model.PersistentSandbox{def})

	var body string
	waitFor(t, "the host port to answer", func() bool {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/hello.txt", hostPort))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		body = string(b)
		return resp.StatusCode == http.StatusOK
	})
	if body != "hello from staging\n" {
		t.Errorf("body = %q", body)
	}
}

func TestPersistentSandboxRefusesGrainsOwnPort(t *testing.T) {
	s := NewPersistentSandboxSupervisor()
	ports := hostport.New("127.0.0.1")
	s.Ports = ports
	t.Cleanup(ports.Close)
	s.ReservedPorts = []int{freePort(t)}
	def := model.PersistentSandbox{Name: "clash", Repo: "a/b", Command: "true", SandboxPort: 80, HostPort: s.ReservedPorts[0]}
	s.Reconcile(context.Background(), Deps{}, []model.PersistentSandbox{def})
	def.Enabled = true
	s.syncPorts([]model.PersistentSandbox{def})
	if st := persistentStatus(s, def); !strings.Contains(st.LastError, "grain itself") {
		t.Errorf("LastError = %q, want the reserved-port refusal", st.LastError)
	}
	if ports.Addr(def.HostPort) != nil {
		t.Error("a reserved port was bound")
	}
}

func TestValidatePersistentSandboxes(t *testing.T) {
	ok := model.PersistentSandbox{Name: "a", Repo: "o/r", Command: "x", SandboxPort: 80, HostPort: 9000, UISubpath: "/staging/a", Enabled: true}
	if err := model.ValidatePersistentSandboxes([]model.PersistentSandbox{ok}, []int{8080}); err != nil {
		t.Fatalf("valid definition refused: %v", err)
	}
	for name, list := range map[string][]model.PersistentSandbox{
		"bad repo":       {{Name: "a", Repo: "nope", Command: "x"}},
		"no command":     {{Name: "a", Repo: "o/r"}},
		"no target port": {{Name: "a", Repo: "o/r", Command: "x", HostPort: 9000}},
		"wrong subpath":  {{Name: "a", Repo: "o/r", Command: "x", SandboxPort: 80, UISubpath: "/elsewhere"}},
		"port collision": {ok, {Name: "b", Repo: "o/r", Command: "x", SandboxPort: 80, HostPort: 9000, Enabled: true}},
		"grain's port":   {{Name: "a", Repo: "o/r", Command: "x", SandboxPort: 80, HostPort: 8080, Enabled: true}},
		"escaping dir":   {{Name: "a", Repo: "o/r", Command: "x", Dir: "../up"}},
		// The refresh policy (model.PersistentSandbox.Refresh).
		"unknown refresh":           {{Name: "a", Repo: "o/r", Command: "x", AutoUpdate: true, Refresh: "hourly"}},
		"bad interval":              {{Name: "a", Repo: "o/r", Command: "x", AutoUpdate: true, Refresh: "interval:soon"}},
		"too short an interval":     {{Name: "a", Repo: "o/r", Command: "x", AutoUpdate: true, Refresh: "interval:10s"}},
		"on_push without auto":      {{Name: "a", Repo: "o/r", Command: "x", Refresh: "on_push"}},
		"interval without auto":     {{Name: "a", Repo: "o/r", Command: "x", Refresh: "interval:1h"}},
		"after_green without auto":  {{Name: "a", Repo: "o/r", Ref: "main", Command: "x", Refresh: "after_green"}},
		"after_green without a ref": {{Name: "a", Repo: "o/r", Command: "x", AutoUpdate: true, Refresh: "after_green"}},
		"rollback without green":    {{Name: "a", Repo: "o/r", Command: "x", AutoUpdate: true, Refresh: "on_push", RollbackOnCrash: true}},
		"rollback on a manual one":  {{Name: "a", Repo: "o/r", Command: "x", AutoUpdate: true, RollbackOnCrash: true}},
	} {
		if err := model.ValidatePersistentSandboxes(list, []int{8080}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPersistentSandboxStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openTestStoreForOrchestrator(t)
	def := model.PersistentSandbox{
		Name: "rt", Repo: "o/r", Ref: "v1", AutoUpdate: true, Command: "make serve", Dir: "web",
		Env: map[string]string{"A": "1"}, Capabilities: []string{"gcp-key"}, CPUs: 2, MemoryMB: 2048, DiskGB: 20,
		NetworkAccess: model.NetworkOpen, SandboxPort: 8080, HostPort: 4280, UISubpath: "/staging/rt", StripPrefix: true, Enabled: true,
		Refresh: "after_green", RollbackOnCrash: true,
	}
	if err := st.PutPersistentSandbox(ctx, def); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetPersistentSandbox(ctx, "rt")
	if err != nil || got == nil {
		t.Fatalf("get: %v, %v", got, err)
	}
	if !reflect.DeepEqual(*got, def) {
		t.Errorf("round trip:\n got %+v\nwant %+v", *got, def)
	}
	if err := st.DeletePersistentSandbox(ctx, "rt"); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListPersistentSandboxes(ctx); len(list) != 0 {
		t.Errorf("after delete: %+v", list)
	}
}

type fakeDialRunner struct {
	fakeShellRunner
	dialArgv []string
	dialConn net.Conn
}

func (r *fakeDialRunner) DialStream(_ context.Context, argv []string) (net.Conn, error) {
	r.dialArgv = argv
	return r.dialConn, nil
}

func TestK8sAndContainerSandboxesImplementSandboxPortDialer(t *testing.T) {
	wantArgv := []string{"sh", "-c", guestDialScript, "sh", "4280"}
	cases := []struct {
		name  string
		build func(runner sandboxRunner) Sandbox
	}{
		{
			name: "granuleSandbox",
			build: func(runner sandboxRunner) Sandbox {
				return &granuleSandbox{name: "p12345678", vmName: "granule-vm-p12345678", runner: runner}
			},
		},
		{
			name: "k8sGranuleSandbox",
			build: func(runner sandboxRunner) Sandbox {
				return &k8sGranuleSandbox{name: "p12345678", podName: "grain-granule-p12345678", runner: runner}
			},
		},
		{
			name: "k8sSandbox",
			build: func(runner sandboxRunner) Sandbox {
				return &k8sSandbox{name: "p12345678", podName: "grain-sandbox-p12345678", runner: runner}
			},
		},
		{
			name: "containerSandbox",
			build: func(runner sandboxRunner) Sandbox {
				return &containerSandbox{name: "p12345678", container: "grain-sandbox-p12345678", runner: runner}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			defer serverConn.Close()

			runner := &fakeDialRunner{dialConn: clientConn}
			sb := tc.build(runner)
			dialer, ok := sb.(SandboxPortDialer)
			if !ok {
				t.Fatalf("%s does not implement SandboxPortDialer", tc.name)
			}

			conn, err := dialer.DialPort(context.Background(), 4280)
			if err != nil {
				t.Fatalf("DialPort: %v", err)
			}
			if !reflect.DeepEqual(runner.dialArgv, wantArgv) {
				t.Fatalf("dialArgv = %q, want %q", runner.dialArgv, wantArgv)
			}
			go func() {
				_, _ = io.WriteString(serverConn, "ok")
			}()
			buf := make([]byte, 2)
			if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ok" {
				t.Fatalf("read from DialPort conn = (%q, %v), want (\"ok\", nil)", string(buf), err)
			}

			noStream := tc.build(&fakeShellRunner{})
			if _, err := noStream.(SandboxPortDialer).DialPort(context.Background(), 4280); err == nil || !strings.Contains(err.Error(), "transport cannot open a connection") {
				t.Fatalf("DialPort without streamRunner = %v, want transport error", err)
			}
		})
	}
}

func writeExecutableForPersistentTest(t *testing.T, dst, script string) {
	t.Helper()
	src := dst + ".src"
	if err := os.WriteFile(src, []byte(script), 0o644); err != nil {
		t.Fatalf("writing %s: %v", src, err)
	}
	if out, err := exec.Command("install", "-m", "0755", src, dst).CombinedOutput(); err != nil {
		t.Fatalf("installing %s: %v: %s", dst, err, out)
	}
}

func TestPersistentSandboxSupervisorRunsAndDialsOnK8sGranuleAndK8sSandboxes(t *testing.T) {
	repo := persistentTestRepo(t, map[string]string{
		"serve.sh": "#!/bin/sh\necho persistent-ready\nexec sleep 600\n",
	})
	binDir := t.TempDir()
	grainConnect := filepath.Join(binDir, "grain-connect")
	writeExecutableForPersistentTest(t, grainConnect, "#!/bin/sh\necho \"dialed:$1\"\n")
	kubectl := filepath.Join(binDir, "kubectl")
	kubectlScript := `#!/bin/sh
export PATH="` + binDir + `:$PATH"
is_exec=0
for arg in "$@"; do
	case "$arg" in
		apply|delete)
			exit 0
			;;
		exec)
			is_exec=1
			;;
	esac
done
if [ "$is_exec" = "0" ]; then
	printf 'Running|True|docker-pullable://example/granule@sha256:abc|\n'
	exit 0
fi
while [ $# -gt 0 ]; do
	if [ "$1" = "--" ]; then
		shift
		break
	fi
	shift
done
if [ "$1" = "granule" ] && [ "$2" = "exec" ] && [ "$3" = "--" ]; then
	shift 3
fi
exec "$@"
`
	writeExecutableForPersistentTest(t, kubectl, kubectlScript)

	backends := []struct {
		name      string
		sandboxes func(workspace string) Sandboxes
	}{
		{
			name: "K8sGranuleSandboxes",
			sandboxes: func(workspace string) Sandboxes {
				return NewK8sGranuleSandboxes(K8sGranuleConfig{
					Namespace:         "gke-sandboxes",
					Kubectl:           kubectl,
					Image:             "example/granule:latest",
					Workspace:         workspace,
					ReadyPollInterval: 5 * time.Millisecond,
				})
			},
		},
		{
			name: "K8sSandboxes",
			sandboxes: func(workspace string) Sandboxes {
				return NewK8sSandboxes(K8sConfig{
					Namespace:         "gke-sandboxes",
					Kubectl:           kubectl,
					Image:             "example/sandbox:latest",
					Workspace:         workspace,
					ReadyPollInterval: 5 * time.Millisecond,
				})
			},
		},
	}

	for _, tc := range backends {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestPersistentSupervisor(repo)
			t.Cleanup(s.Close)
			def := model.PersistentSandbox{
				Name:        "grain-candidate",
				Repo:        "bwsalmon/grain",
				Ref:         "main",
				Command:     "./serve.sh",
				SandboxPort: 4280,
				Enabled:     true,
			}
			s.Reconcile(context.Background(), Deps{Sandboxes: tc.sandboxes(t.TempDir())}, []model.PersistentSandbox{def})

			waitFor(t, tc.name+" persistent sandbox running and streaming logs", func() bool {
				st := persistentStatus(s, def)
				logs, _ := s.Logs(def.Name)
				return st.State == PersistentRunning && strings.Contains(logs, "persistent-ready")
			})

			st := persistentStatus(s, def)
			if st.LastError != "" {
				t.Fatalf("LastError = %q, want empty", st.LastError)
			}

			conn, err := s.Dial(context.Background(), def.Name)
			if err != nil {
				t.Fatalf("Dial(%q): %v", def.Name, err)
			}
			defer conn.Close()
			if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatalf("SetReadDeadline: %v", err)
			}
			out, err := io.ReadAll(conn)
			if err != nil {
				t.Fatalf("ReadAll from dialed port: %v", err)
			}
			if got := strings.TrimSpace(string(out)); got != "dialed:127.0.0.1:4280" {
				t.Fatalf("Dial output = %q, want %q", got, "dialed:127.0.0.1:4280")
			}
		})
	}
}
