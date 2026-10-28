package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// workstation is the configuration -host-sandboxes is meant for: a
// daemon whose UI only its own host's user can reach.
func workstation() Config {
	return Config{HostSandboxes: true, SandboxDir: "/home/me/.grain/sandboxes", UiAddr: "127.0.0.1:4242"}
}

func TestCheckHostSandboxesAllowsASingleUserWorkstation(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{"127.0.0.1:4242", "localhost:4242", "[::1]:4242", ""} {
		cfg := workstation()
		cfg.UiAddr = addr
		if err := CheckSandboxBackend(cfg, false); err != nil {
			t.Errorf("-host-sandboxes with -ui-addr %q = %v, want it to start", addr, err)
		}
	}
}

func TestCheckHostSandboxesRefusesADaemonOthersCanReach(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		mutate func(*Config)
		names  string
	}{
		"default ui-addr":  {func(c *Config) { c.UiAddr = "0.0.0.0:4242" }, "-ui-addr 0.0.0.0:4242"},
		"any interface":    {func(c *Config) { c.UiAddr = ":4242" }, "-ui-addr :4242"},
		"lan address":      {func(c *Config) { c.UiAddr = "10.1.2.3:4242" }, "-ui-addr 10.1.2.3:4242"},
		"remote executors": {func(c *Config) { c.ExecutorAddr = ":8422" }, "-executor-addr"},
		"iap":              {func(c *Config) { c.IapBackendService = "grain-backend" }, "IAP"},
		"run hosts":        {func(c *Config) { c.RunHostImage = "example/grain" }, "-run-host-image"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := workstation()
			tc.mutate(&cfg)
			err := CheckSandboxBackend(cfg, false)
			if err == nil || !strings.Contains(err.Error(), tc.names) || !strings.Contains(err.Error(), "-allow-unisolated-host-sandboxes") {
				t.Fatalf("CheckSandboxBackend = %v, want a refusal naming %q and the override", err, tc.names)
			}

			cfg.AllowUnisolatedHostSandboxes = true
			if err := CheckSandboxBackend(cfg, false); err != nil {
				t.Fatalf("with -allow-unisolated-host-sandboxes: %v, want it to start", err)
			}
		})
	}
}

func TestCheckHostSandboxesNeedsASandboxDir(t *testing.T) {
	t.Parallel()
	cfg := workstation()
	cfg.SandboxDir = ""
	cfg.AllowUnisolatedHostSandboxes = true
	if err := CheckSandboxBackend(cfg, false); err == nil || !strings.Contains(err.Error(), "-sandbox-dir") {
		t.Fatalf("CheckSandboxBackend = %v, want -sandbox-dir required", err)
	}
}

func TestHostSandboxesAreTheSelectedBackend(t *testing.T) {
	t.Parallel()
	cfg := workstation()
	if got := cfg.SandboxBackend(); got != model.SandboxBackendHost {
		t.Fatalf("SandboxBackend = %q, want host", got)
	}
	if got := SelectedSandboxBackends(cfg); !reflect.DeepEqual(got, []string{"-host-sandboxes"}) {
		t.Fatalf("SelectedSandboxBackends = %v", got)
	}
	wantBackends := []model.SandboxBackend{
		model.SandboxBackendKubernetes,
		model.SandboxBackendKubernetesGranule,
		model.SandboxBackendHost,
	}
	if got := cfg.availableSandboxBackends(); !reflect.DeepEqual(got, wantBackends) {
		t.Fatalf("availableSandboxBackends = %v, want %v", got, wantBackends)
	}
	if got := SandboxAdvertiseHost(cfg); got != "" {
		t.Fatalf("SandboxAdvertiseHost = %q, want loopback (empty): a host sandbox shares this network namespace", got)
	}
	runnable, notes := cfg.RunnableStoredConfig(model.Config{SandboxBackend: model.SandboxBackendHost})
	for _, note := range notes {
		if strings.Contains(note, "sandbox backend") {
			t.Fatalf("stored host on a -host-sandboxes daemon was rejected: %v", notes)
		}
	}
	if runnable.SandboxBackend != model.SandboxBackendHost {
		t.Fatalf("stored host on a -host-sandboxes daemon: backend %q notes %v", runnable.SandboxBackend, notes)
	}
	if !cfg.WithModelConfig(runnable).HostSandboxes {
		t.Fatal("stored host backend did not keep host sandboxes selected")
	}
}

// A Settings choice cannot move a deployment onto host sandboxes: the
// flag is where the workstation guard is asked, so a daemon started
// without it neither offers nor builds "host".
func TestStoredHostBackendNeedsTheFlag(t *testing.T) {
	t.Parallel()
	cfg := Config{
		SandboxDir:            "/sandboxes",
		ContainerSandboxes:    true,
		ContainerImage:        "example/guest",
		ContainerGitProxyHost: "172.17.0.1",
	}
	for _, backend := range cfg.availableSandboxBackends() {
		if backend == model.SandboxBackendHost {
			t.Fatal("host offered on a daemon started without -host-sandboxes")
		}
	}
	runnable, _ := cfg.RunnableStoredConfig(model.Config{SandboxBackend: model.SandboxBackendHost})
	if started := cfg.WithModelConfig(runnable); started.HostSandboxes || !started.ContainerSandboxes {
		t.Fatalf("stored host selected host=%v container=%v, want the flags' container backend", started.HostSandboxes, started.ContainerSandboxes)
	}
}

// What the daemon builds for -host-sandboxes gives each run its own
// directory under -sandbox-dir, removes it on release, and reaps what a
// killed daemon left behind at the next start -- and only that: the
// caches that live beside the runs under -sandbox-dir survive the reap.
func TestHostSandboxesIsolateRunsUnderTheSandboxDir(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := workstation()
	cfg.SandboxDir = t.TempDir()
	dir := HostSandboxRunsDir(cfg)
	if err := os.MkdirAll(filepath.Join(dir, "run-killed-mid-flight", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cache := range []string{packageCacheDir(cfg), actionsCacheDir(cfg)} {
		if err := os.MkdirAll(cache, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := os.Stat(cache); err != nil {
				t.Errorf("cache %s did not survive the host sandbox reap: %v", cache, err)
			}
		})
	}
	h := orchestrator.NewHostSandboxes(dir)
	a, err := h.Acquire(ctx, "run-a", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.Acquire(ctx, "run-b", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	reaped, err := h.ReapOrphans(ctx)
	if err != nil || reaped != 1 {
		t.Fatalf("ReapOrphans = %d, %v; want the one orphan reaped", reaped, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "run-killed-mid-flight")); !os.IsNotExist(err) {
		t.Fatalf("orphaned run directory survived the reap: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "run-a", "secret"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "run-b", "secret")); !os.IsNotExist(err) {
		t.Fatal("one run's file is visible in another run's directory")
	}
	for _, sb := range []orchestrator.Sandbox{a, b} {
		if _, err := os.Stat(filepath.Join(dir, sb.Name())); err != nil {
			t.Fatalf("live run %s was reaped: %v", sb.Name(), err)
		}
		if err := sb.Release(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, sb.Name())); !os.IsNotExist(err) {
			t.Fatalf("run %s's directory survived its release", sb.Name())
		}
	}
}
