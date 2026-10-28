package cli

// grain/task-365: the adapter behind Settings' Gemini model picker.
// These drive it through a stub `agy` on disk rather than a fake seam,
// because what is being asserted is the wiring either side of
// antigravity.Catalog -- that the binary this deployment dispatches
// through is the one asked, that a listing is not re-fetched on every
// visit to the pane, and that a deployment with no binary gets a sentence
// an operator can act on rather than a pane that will not load.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/daemon"
)

// stubAgy writes an executable that answers `agy models` and `agy --help`
// from canned output, appending a line to a counter file per invocation.
// It returns the path to the binary and to that counter.
func stubAgy(t *testing.T) (agyPath, calls string) {
	t.Helper()
	dir := t.TempDir()
	agyPath = filepath.Join(dir, "agy")
	calls = filepath.Join(dir, "calls")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + calls + "\n" +
		"case \"$1\" in\n" +
		"--help) echo '  --effort  Reasoning effort for the current CLI session (low|medium|high)' ;;\n" +
		"models) printf 'Fetching available models...\\ngemini-3.1-pro-high\\tGemini 3.1 Pro (High)\\n' ;;\n" +
		"esac\n"
	if err := os.WriteFile(agyPath, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stub agy: %v", err)
	}
	return agyPath, calls
}

func countCalls(t *testing.T, calls, arg string) int {
	t.Helper()
	b, err := os.ReadFile(calls)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, arg) {
			n++
		}
	}
	return n
}

func TestAgyModelListerReadsTheConfiguredBinary(t *testing.T) {
	t.Parallel()
	agyPath, calls := stubAgy(t)
	lister := &daemon.AgyModelLister{Live: testLiveConfig(daemon.Config{AgyPath: agyPath})}

	got, err := lister.ListModels(context.Background(), "")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "gemini-3.1-pro-high" {
		t.Fatalf("Models = %+v, want the stub's own listing", got.Models)
	}
	if got.Models[0].Effort != "high" || got.Models[0].Family != "gemini-3.1-pro" {
		t.Errorf("Models[0] = %+v, want the effort split off the name", got.Models[0])
	}
	if len(got.Efforts) != 3 {
		t.Errorf("Efforts = %v, want the vocabulary agy's --help documents", got.Efforts)
	}
	if n := countCalls(t, calls, "models"); n != 1 {
		t.Errorf("`agy models` ran %d times, want 1", n)
	}
}

// The pane is opened repeatedly while a deployment is configured, and
// each open would otherwise be a subprocess and a network fetch.
func TestAgyModelListerCachesAnAnswer(t *testing.T) {
	t.Parallel()
	agyPath, calls := stubAgy(t)
	lister := &daemon.AgyModelLister{Live: testLiveConfig(daemon.Config{AgyPath: agyPath})}

	for i := 0; i < 3; i++ {
		if _, err := lister.ListModels(context.Background(), ""); err != nil {
			t.Fatalf("ListModels: %v", err)
		}
	}
	if n := countCalls(t, calls, "models"); n != 1 {
		t.Errorf("`agy models` ran %d times, want 1: a fresh catalog is reused", n)
	}
}

// A failure is not cached: it is usually something being fixed right now
// (a key just pasted, an agy just installed), and the next reload has to
// be allowed to prove it.
func TestAgyModelListerRetriesAfterAFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	agyPath := filepath.Join(dir, "agy")
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + calls + "\nexit 1\n"
	if err := os.WriteFile(agyPath, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stub agy: %v", err)
	}
	lister := &daemon.AgyModelLister{Live: testLiveConfig(daemon.Config{AgyPath: agyPath})}

	for i := 0; i < 2; i++ {
		if _, err := lister.ListModels(context.Background(), ""); err == nil {
			t.Fatal("ListModels: want an error from an agy that refuses")
		}
	}
	if n := countCalls(t, calls, "models"); n != 2 {
		t.Errorf("`agy models` ran %d times, want 2: a failure is not cached", n)
	}
}

// No binary at all is the one failure worth wording carefully: it reaches
// an operator on the model field itself, and it is the same condition
// buildAntigravityFramework refuses a dispatch for.
func TestAgyModelListerNamesAMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	lister := &daemon.AgyModelLister{Live: testLiveConfig(daemon.Config{})}

	_, err := lister.ListModels(context.Background(), "")
	if err == nil {
		t.Fatal("ListModels: want an error with no agy anywhere")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("err = %v, want it to name the missing install", err)
	}
}
