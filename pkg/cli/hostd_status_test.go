//go:build linux

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

// grain hostd -status is what setup.sh's readiness report says about a
// running hostd: that it answers, its build, and every entry it offers
// with the host path it runs.
func TestHostdStatusLineNamesWhatAnsweringHostdOffers(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("hostd refuses to run as root")
	}
	// Not t.TempDir: a unix socket's path has to fit in 108 bytes.
	dir, err := os.MkdirTemp("", "hostd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	allow := filepath.Join(dir, "hostd.yaml")
	body := "clis:\n  agy:\n    path: /opt/fakeagy/agy\n    cwd_under: [" + dir + "]\n" +
		"capabilities:\n  corp-search:\n    command: /usr/local/bin/corp-search-mcp\n"
	if err := os.WriteFile(allow, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "hostd.sock")
	ctx := context.Background()

	// Nothing listening: an error, which -status exits 1 on.
	if _, err := hostdStatusLine(ctx, hostd.Client{Socket: socket}); err == nil ||
		!strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("no hostd: err = %v, want not reachable", err)
	}

	srv := &hostd.Server{
		Socket:          socket,
		Allowlist:       allow,
		AllowlistOwners: []uint32{uint32(os.Getuid())},
		StateDir:        filepath.Join(dir, "state"),
		Build:           "v9.9.9",
		Logf:            func(string, ...any) {},
	}
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { srv.Serve(sctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	for i := 0; ; i++ {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if i > 500 {
			t.Fatal("hostd never listened")
		}
		time.Sleep(10 * time.Millisecond)
	}

	line, err := hostdStatusLine(ctx, hostd.Client{Socket: socket})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"answering on " + socket + " (v9.9.9, protocol ",
		"cli agy (/opt/fakeagy/agy)",
		"capability corp-search (/usr/local/bin/corp-search-mcp)",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("status = %q, want it to contain %q", line, want)
		}
	}
}

func TestFormatHostdStatus(t *testing.T) {
	got := formatHostdStatus("/s", hostd.Hello{Protocol: 2, Draining: true}, nil)
	if want := "answering on /s (an unknown build, protocol 2), draining for an upgrade, offering nothing"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
