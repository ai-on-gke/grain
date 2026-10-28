package daemon

// grain CI's verdict is the one thing in pull_request_status' answer that
// does not come from GitHub: it is a row on the task, so this process
// asks the daemon for it. These pin the two halves of that -- who gets a
// reader at all, and what the hop makes of what the daemon answers.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// grainCIDaemon is a grain daemon as far as daemonGrainCI is concerned:
// GET /api/tasks/{id}, answering with the CI status this test wants.
type grainCIDaemon struct {
	detail ui.TaskDetail
	reads  int
}

func (d *grainCIDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.reads++
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d.detail)
}

func grainCIAgainst(t *testing.T, daemon *grainCIDaemon) DaemonGrainCI {
	t.Helper()
	srv := httptest.NewServer(daemon)
	t.Cleanup(srv.Close)
	return DaemonGrainCI{Client: ui.NewHTTPClient(srv.URL), TaskID: "t-1"}
}

// The hop itself: what grain CI recorded on the task, in the vocabulary
// pkg/mcp reports it in.
func TestDaemonGrainCIReadsTheVerdictOffTheTask(t *testing.T) {
	t.Parallel()
	daemon := &grainCIDaemon{detail: ui.TaskDetail{
		CI: &ui.TaskCIStatus{
			RunID:      "ci-7",
			Commit:     "abcdef0123456789",
			Conclusion: "failure",
			Detail:     `job "unit" failed`,
		},
	}}

	verdict, err := grainCIAgainst(t, daemon).GrainCI(context.Background())
	if err != nil {
		t.Fatalf("GrainCI: %v", err)
	}
	if verdict == nil {
		t.Fatal("GrainCI returned no verdict for a task carrying one")
	}
	if verdict.Commit != "abcdef0123456789" || verdict.Conclusion != "failure" {
		t.Errorf("GrainCI = %+v, want the task's own commit and conclusion", verdict)
	}
	if verdict.RunID != "ci-7" || verdict.Detail != `job "unit" failed` {
		t.Errorf("GrainCI = %+v, want the run id and detail carried through", verdict)
	}
}

// A task with nothing recorded is the ordinary case, not an error: a
// branch that has not been through CI yet holds nothing, and absence is
// never failure (orchestrator.withGrainCI).
func TestDaemonGrainCIReportsNoVerdictAsNoVerdict(t *testing.T) {
	t.Parallel()
	verdict, err := grainCIAgainst(t, &grainCIDaemon{}).GrainCI(context.Background())
	if err != nil {
		t.Fatalf("GrainCI: %v", err)
	}
	if verdict != nil {
		t.Errorf("GrainCI = %+v, want nothing at all for a task with no CI status", verdict)
	}
}

// Who is told about grain CI: a run whose task holds the capability that
// puts its branch through it, and only then a run with a daemon to ask.
// The capability is the switch and the only one (pkg/capability/grainci),
// which is the rule the merge queue reads the same verdict under.
func TestGrainCIReaderNeedsBothTheGrantAndADaemon(t *testing.T) {
	t.Parallel()
	client := ui.NewHTTPClient("http://daemon.invalid")

	if r := grainCIReader(client, "t-1", GrantNames{grainci.CapabilityName}); r == nil {
		t.Error("a run holding grain-ci with a daemon to ask got no reader")
	}
	if r := grainCIReader(client, "t-1", GrantNames{"self-debug"}); r != nil {
		t.Errorf("a run without the grain-ci grant got a reader: %+v", r)
	}
	if r := grainCIReader(nil, "", GrantNames{grainci.CapabilityName}); r != nil {
		t.Errorf("a run with no daemon to ask got a reader: %+v", r)
	}
}
