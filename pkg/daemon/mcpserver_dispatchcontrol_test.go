package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/capability/dispatchcontrol"
	"github.com/bwsalmon/grain/pkg/capability/pipelinecontrol"
	"github.com/bwsalmon/grain/pkg/ui"
)

// A run holding both pipeline-control and dispatch-control is served
// each tool name once -- dispatch-control's, which do what
// pipeline-control's do and more (grain/task-399) -- and a run holding
// dispatch-control alone gets run_task_now and defer_task beside them.
func TestDispatchControlToolsAreServedOnce(t *testing.T) {
	count := func(grants GrantNames) map[string]int {
		seen := map[string]int{}
		for _, tool := range GrantedDaemonTools(grants, "", "", "", "7", nil) {
			seen[tool.Name]++
		}
		return seen
	}
	both := count(GrantNames{pipelinecontrol.CapabilityName, dispatchcontrol.CapabilityName})
	for _, name := range []string{"pause_pipeline", "resume_pipeline", "run_task_now", "defer_task"} {
		if both[name] != 1 {
			t.Errorf("with both grants %s is served %d times, want once", name, both[name])
		}
	}
	if only := count(GrantNames{pipelinecontrol.CapabilityName}); only["run_task_now"] != 0 || only["pause_pipeline"] != 1 {
		t.Errorf("pipeline-control alone serves %v, want its own two tools and no run_task_now", only)
	}
}

// dispatchDaemon answers the reads and the resume daemonDispatchControl
// makes, holding pause as the deployment's pause and counting resumes.
func dispatchDaemon(t *testing.T, pause ui.PipelinePauseStatus, resumes *int) *ui.HTTPClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		switch {
		case r.URL.Path == "/api/tasks/7":
			body = ui.TaskDetail{Task: ui.Task{ID: "7", Capabilities: []string{dispatchcontrol.CapabilityName}}}
		case r.URL.Path == "/api/pipeline-pause" && r.Method == http.MethodGet:
			body = map[string]any{"pause": pause}
		case r.URL.Path == "/api/pipeline-pause" && r.Method == http.MethodDelete:
			*resumes++
			body = map[string]any{"pause": ui.PipelinePauseStatus{}, "resumed": true}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return ui.NewHTTPClient(srv.URL)
}

// resume_pipeline under dispatch-control never lifts an upgrade's hold:
// that would pass it with the whole queue, and for a scheduled upgrade
// call the upgrade off -- self-upgrade's to do, not this grant's. Any
// other pause, the agent's own included, it lifts.
func TestDispatchControlResumeRefusesUpgradeHold(t *testing.T) {
	var resumes int
	held := daemonDispatchControl{taskID: "7", client: dispatchDaemon(t,
		ui.PipelinePauseStatus{Paused: true, Reason: "waiting to upgrade", RunNowPasses: false}, &resumes)}
	if _, err := held.ResumePipeline(context.Background()); err == nil || !strings.Contains(err.Error(), "upgrade holds the queue") {
		t.Fatalf("resume under an upgrade's hold: err %v, want a refusal", err)
	}
	if resumes != 0 {
		t.Fatalf("resume under an upgrade's hold reached the daemon %d times", resumes)
	}

	manual := daemonDispatchControl{taskID: "7", client: dispatchDaemon(t,
		ui.PipelinePauseStatus{Paused: true, Reason: "hand-picking", RunNowPasses: true}, &resumes)}
	resumed, err := manual.ResumePipeline(context.Background())
	if err != nil || !resumed || resumes != 1 {
		t.Fatalf("resume of an operator's pause: resumed %v, err %v, %d resumes", resumed, err, resumes)
	}
}

// A task id is the agent's own input, and the daemon makes these requests
// with this deployment's credentials: an id that reads as more path --
// "5/approve?" -- must stay one id rather than reaching another route,
// or defer_task would be an approval nothing checked.
func TestDispatchControlTaskIDStaysOnePathSegment(t *testing.T) {
	var reached []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tasks/7" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ui.TaskDetail{Task: ui.Task{ID: "7", Capabilities: []string{dispatchcontrol.CapabilityName}}})
			return
		}
		reached = append(reached, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	d := daemonDispatchControl{taskID: "7", client: ui.NewHTTPClient(srv.URL)}

	_ = d.DeferTask(context.Background(), "5/approve?", nil)
	_, _, _ = d.RunTaskNow(context.Background(), "../5/approve?")
	want := []string{
		"POST /api/tasks/5%2Fapprove%3F/defer?",
		"POST /api/tasks/..%2F5%2Fapprove%3F/run-now?",
	}
	if strings.Join(reached, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests reached\n%s\nwant\n%s", strings.Join(reached, "\n"), strings.Join(want, "\n"))
	}
}
