package orchestrator_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent/antigravity"
)

// liveFramework builds the real agent/antigravity Framework the live
// tests in this package drive, or skips the calling test when this
// machine cannot run one.
//
// Three things have to be present, and none of them is in CI: a Gemini
// credential, the Antigravity CLI itself, and a grain binary for agy to
// fork as its MCP server. The last is new with agent/antigravity -- the
// home-grown Gemini runtime this replaced looped tool calls in-process
// and so needed no second binary at all -- and is why these tests build
// one rather than just resolving a path.
func liveFramework(t *testing.T, opts ...antigravity.Option) *antigravity.Framework {
	t.Helper()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set; skipping live agent integration test")
	}
	agyPath, err := exec.LookPath("agy")
	if err != nil {
		t.Skip("agy binary not found on PATH; skipping live agent integration test")
	}
	return antigravity.New(agyPath, buildGrainBinary(t), append([]antigravity.Option{
		antigravity.WithAPIKey(apiKey),
	}, opts...)...)
}

// buildGrainBinary compiles cmd/grain into t.TempDir() so a live run's
// agy can fork its "mcpserver" subcommand -- the same binary a real
// deployment builds once and reuses across every dispatch.
func buildGrainBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "grain")
	cmd := exec.Command("go", "build", "-o", out, "github.com/bwsalmon/grain/cmd/grain")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the grain binary for agy's MCP server: %v\n%s", err, output)
	}
	return out
}
