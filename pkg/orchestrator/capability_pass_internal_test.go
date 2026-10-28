package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
)

const passTestCap = "https://github.com/bwsalmon/grain/capabilities/gcp"

// mintCaller is a capability server with one releasable method, gcp-key,
// answering every mint with reply (an ok by default) and recording each
// call.
type mintCaller struct {
	mu    sync.Mutex
	reply func() ([]byte, error)
	// releaseReply answers every release_; empty is a bare ok.
	releaseReply string
	calls        []string // tool name and attempt_id
	args         []map[string]any
}

func (c *mintCaller) ListTools(context.Context) ([]protocol.Tool, error) {
	return []protocol.Tool{
		{Name: "mint_gcp-key", InputSchema: map[string]any{
			"type": "object", "required": []any{"attempt_id", "deadline", "args"},
			"properties": map[string]any{
				"attempt_id": map[string]any{"type": "string"},
				"deadline":   map[string]any{"type": "string"},
				"args": map[string]any{
					"type": "object", "additionalProperties": false,
					"properties": map[string]any{"account": map[string]any{"type": "string", "default": "staging"}},
				},
			},
		}},
		{Name: "release_gcp-key", InputSchema: map[string]any{
			"type": "object", "required": []any{"attempt_id"},
			"properties": map[string]any{"attempt_id": map[string]any{"type": "string"}},
		}},
	}, nil
}

func (c *mintCaller) CallTool(_ context.Context, name string, args map[string]any) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, name+" "+args["attempt_id"].(string))
	if strings.HasPrefix(name, "release_") {
		if c.releaseReply != "" {
			return []byte(c.releaseReply), nil
		}
		return []byte(`{"status":"ok"}`), nil
	}
	a, _ := args["args"].(map[string]any)
	c.args = append(c.args, a)
	if c.reply != nil {
		return c.reply()
	}
	return []byte(`{"status":"ok"}`), nil
}

func (c *mintCaller) Close() error { return nil }

func (c *mintCaller) called() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

type oneServer struct{ srv *protocol.Server }

func (o oneServer) Lookup(id string) (*protocol.Server, error) {
	if id != o.srv.ID {
		return nil, &protocol.RefusedError{Reason: id + " is not configured"}
	}
	return o.srv, nil
}

func passHarness(t *testing.T, caller *mintCaller) (Deps, *model.Store, context.Context, *time.Time) {
	t.Helper()
	store, ctx := openTreeStore(t)
	srv, err := protocol.NewServer(ctx, passTestCap, caller)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	deps := Deps{Store: store, CapabilityServers: oneServer{srv}, CapabilityPasses: NewCapabilityPasses()}
	deps.Config.Now = func() time.Time { return now }
	if err := store.PutTask(ctx, model.Task{ID: "7", Title: "needs a key", Target: &widgets,
		Grants: []model.Grant{{Capability: "self-debug"}, {Capability: passTestCap, Via: model.GrantByLabel}}}); err != nil {
		t.Fatal(err)
	}
	return deps, store, ctx, &now
}

func admitAll(context.Context, dispatch.Candidate) (dispatch.Admission, error) {
	return dispatch.Admission{Admitted: true}, nil
}

func TestAdmissionMintsRecordsAndReleasesAPass(t *testing.T) {
	caller := &mintCaller{}
	deps, store, ctx, _ := passHarness(t, caller)
	released := false
	admit := capabilityAdmitter(deps, func(context.Context, dispatch.Candidate) (dispatch.Admission, error) {
		return dispatch.Admission{Admitted: true, Release: func() { released = true }}, nil
	})
	cand := dispatch.Candidate{TaskID: "7", RunID: dispatch.RunID("7", 2), Attempt: 2}
	adm, err := admit(ctx, cand)
	if err != nil || !adm.Admitted {
		t.Fatalf("admit = %+v, %v; want admitted", adm, err)
	}
	if got := caller.called(); len(got) != 1 || got[0] != "mint_gcp-key grain-7-2-0" {
		t.Fatalf("calls = %q, want one mint for attempt grain-7-2-0", got)
	}
	if caller.args[0]["account"] != "staging" {
		t.Errorf("minted with args %v, want the schema's default applied", caller.args[0])
	}
	task, err := store.GetTask(ctx, "7")
	if err != nil {
		t.Fatal(err)
	}
	recorded := false
	for _, g := range task.Grants {
		recorded = recorded || g.Capability == passTestCap+`#gcp-key={"account":"staging"}`
	}
	if !recorded {
		t.Errorf("recorded grants = %+v, want the normalized reference", task.Grants)
	}
	if deps.CapabilityPasses.Pass(cand.RunID) == nil {
		t.Fatal("no pass held for the run")
	}
	// A dispatch that fails to start hands the admission back, pass and
	// all.
	adm.Release()
	if !released {
		t.Error("the inner admission was not released")
	}
	if got := caller.called(); len(got) != 2 || got[1] != "release_gcp-key grain-7-2-0" {
		t.Errorf("calls = %q, want the pass released", got)
	}
	if deps.CapabilityPasses.Pass(cand.RunID) != nil {
		t.Error("the released pass is still held")
	}
}

func TestAdmissionRefusalParksTheTaskWithTheServersWordsQuoted(t *testing.T) {
	caller := &mintCaller{reply: func() ([]byte, error) {
		return []byte(`{"status":"refused","reason":"account staging is closed"}`), nil
	}}
	deps, store, ctx, _ := passHarness(t, caller)
	admit := capabilityAdmitter(deps, admitAll)
	adm, err := admit(ctx, dispatch.Candidate{TaskID: "7", RunID: "7-1", Attempt: 1})
	if err != nil || adm.Admitted {
		t.Fatalf("admit = %+v, %v; want declined", adm, err)
	}
	if got := caller.called(); len(got) != 2 || !strings.HasPrefix(got[1], "release_") {
		t.Errorf("calls = %q, want the refused grant rolled back", got)
	}
	comments, err := store.Comments(ctx, "7")
	if err != nil || len(comments) != 1 {
		t.Fatalf("comments = %v, %v; want one", comments, err)
	}
	c := comments[0]
	if !strings.Contains(c.Body, "account staging is closed") || !strings.Contains(c.Body, "untrusted text") {
		t.Errorf("comment body = %q, want the reason quoted as untrusted text", c.Body)
	}
	if c.Author.OnBehalfOf == nil || !strings.Contains(c.Author.OnBehalfOf.ID, passTestCap) {
		t.Errorf("comment author = %+v, want it attributed to the capability server", c.Author)
	}
	obs, err := store.GetObservation(ctx, "7")
	if err != nil || obs == nil || obs.PendingQuestionCommentID == nil {
		t.Errorf("observation = %+v, %v; want the task parked on the comment", obs, err)
	}
}

func TestAdmissionBacksOffTransientFailuresAndRefusesTheThird(t *testing.T) {
	caller := &mintCaller{reply: func() ([]byte, error) { return []byte(`not json`), nil }}
	deps, store, ctx, now := passHarness(t, caller)
	admit := capabilityAdmitter(deps, admitAll)
	cand := dispatch.Candidate{TaskID: "7", RunID: "7-1", Attempt: 1}
	mints := func() int {
		n := 0
		for _, c := range caller.called() {
			if strings.HasPrefix(c, "mint_") {
				n++
			}
		}
		return n
	}
	for i := 1; i <= 3; i++ {
		if adm, _ := admit(ctx, cand); adm.Admitted {
			t.Fatalf("attempt %d was admitted", i)
		}
		if mints() != i {
			t.Fatalf("after attempt %d, %d mints", i, mints())
		}
		if i == 3 {
			break // refused, and parked on the comment below
		}
		// Within the backoff nothing is minted.
		if adm, _ := admit(ctx, cand); adm.Admitted || mints() != i {
			t.Fatalf("a retry inside the backoff minted (%d mints)", mints())
		}
		*now = now.Add(passBackoffBase << i)
	}
	comments, _ := store.Comments(ctx, "7")
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "mint response") {
		t.Errorf("comments = %+v, want one refusal carrying the last error", comments)
	}
}

func TestAdmissionKeepsAnExhaustedGrantQueuedUntilRetryAfter(t *testing.T) {
	caller := &mintCaller{}
	deps, store, ctx, now := passHarness(t, caller)
	retry := now.Add(10 * time.Minute)
	caller.reply = func() ([]byte, error) {
		return json.Marshal(map[string]any{"status": "resource_exhausted", "retry_after": retry.Format(time.RFC3339)})
	}
	admit := capabilityAdmitter(deps, admitAll)
	cand := dispatch.Candidate{TaskID: "7", RunID: "7-1", Attempt: 1}
	if adm, _ := admit(ctx, cand); adm.Admitted {
		t.Fatal("an exhausted grant was admitted")
	}
	*now = now.Add(5 * time.Minute)
	if adm, _ := admit(ctx, cand); adm.Admitted || len(caller.called()) != 2 {
		t.Fatalf("calls before retry_after = %q, want only the first mint and its rollback", caller.called())
	}
	caller.reply = nil
	*now = retry.Add(time.Second)
	if adm, err := admit(ctx, cand); err != nil || !adm.Admitted {
		t.Fatalf("after retry_after: admit = %+v, %v", adm, err)
	}
	if comments, _ := store.Comments(ctx, "7"); len(comments) != 0 {
		t.Errorf("an exhausted grant posted %d comments; it only waits", len(comments))
	}
}

func TestAdmissionRefusesWhatTheCapabilityPolicyDoesNotAdmit(t *testing.T) {
	caller := &mintCaller{}
	deps, store, ctx, _ := passHarness(t, caller)
	if err := store.PutConfig(ctx, model.Config{CapabilityPolicy: model.CapabilityPolicy{Rules: []model.CapabilityPolicyRule{
		{Capability: passTestCap + "#gcp-key", Repos: []string{"elsewhere/*"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	if adm, _ := capabilityAdmitter(deps, admitAll)(ctx, dispatch.Candidate{TaskID: "7", RunID: "7-1", Attempt: 1}); adm.Admitted {
		t.Fatal("a grant the policy does not admit was minted")
	}
	if got := caller.called(); len(got) != 0 {
		t.Errorf("calls = %q, want nothing minted", got)
	}
	if comments, _ := store.Comments(ctx, "7"); len(comments) != 1 || !strings.Contains(comments[0].Body, "capability policy") {
		t.Errorf("comments = %+v, want the policy refusal", comments)
	}
}

func TestExtendRenewsAPassOnlyPastItsDeadline(t *testing.T) {
	caller := &mintCaller{}
	deps, _, ctx, now := passHarness(t, caller)
	if adm, err := capabilityAdmitter(deps, admitAll)(ctx, dispatch.Candidate{TaskID: "7", RunID: "7-1", Attempt: 1}); err != nil || !adm.Admitted {
		t.Fatalf("admit = %+v, %v", adm, err)
	}
	passes := deps.CapabilityPasses
	deadline := capabilityDeadline(*now, deps.Config.maxRunRuntime())
	if err := passes.Extend(ctx, "7-1", deadline); err != nil || len(caller.called()) != 1 {
		t.Fatalf("an unextended budget renewed: %q, %v", caller.called(), err)
	}
	if err := passes.Extend(ctx, "7-1", deadline.Add(time.Hour)); err != nil || len(caller.called()) != 2 {
		t.Fatalf("an extended budget did not renew: %q, %v", caller.called(), err)
	}
	passes.Release(ctx, "7-1")
	if got := caller.called(); got[len(got)-1] != "release_gcp-key grain-7-1-0" {
		t.Errorf("calls = %q, want the pass released at the end", got)
	}
}

func TestAdmissionRefusesGrantsOnHostDirectorySandboxes(t *testing.T) {
	caller := &mintCaller{}
	deps, store, ctx, _ := passHarness(t, caller)
	deps.Sandboxes = NewHostSandboxes(t.TempDir())
	if adm, _ := capabilityAdmitter(deps, admitAll)(ctx, dispatch.Candidate{TaskID: "7", RunID: "7-1", Attempt: 1}); adm.Admitted {
		t.Fatal("a grant was minted for a sandbox that shares grain's own host")
	}
	if got := caller.called(); len(got) != 0 {
		t.Errorf("calls = %q, want nothing minted", got)
	}
	if comments, _ := store.Comments(ctx, "7"); len(comments) != 1 || !strings.Contains(comments[0].Body, "grain's own host") {
		t.Errorf("comments = %+v, want the host-sandbox refusal", comments)
	}
}

// What a run's grants did for it reaches the run record when the run's
// pass is released: each record under the grant and attempt_id it is
// about, then grain's note on any the server left out.
func TestReleasingARunRecordsItsCapabilityAudit(t *testing.T) {
	caller := &mintCaller{releaseReply: `{"status":"ok","audit_omitted":2,"audit":[` +
		`{"time":"2026-10-22T12:05:00Z","attempt_id":"grain-7-1-0","script":"deploy","argv":["--dry-run"],"exit_code":0}]}`}
	deps, store, ctx, now := passHarness(t, caller)
	if adm, err := capabilityAdmitter(deps, admitAll)(ctx, dispatch.Candidate{TaskID: "7", RunID: "7-1", Attempt: 1}); err != nil || !adm.Admitted {
		t.Fatalf("admit = %+v, %v", adm, err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "7-1", TaskID: "7", Attempt: 1, StartedAt: *now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	releaseRunCapabilities(ctx, deps, "7", "7-1")
	recs, ok, err := store.RunCapabilityAudit(ctx, "7", 1)
	if err != nil || !ok || len(recs) != 2 {
		t.Fatalf("audit = %+v, %v, %v; want the record and a note", recs, ok, err)
	}
	if recs[0].AttemptID != "grain-7-1-0" || recs[0].Grant != passTestCap+"#gcp-key" ||
		!recs[0].At.Equal(time.Date(2026, 10, 22, 12, 5, 0, 0, time.UTC)) || !strings.Contains(recs[0].Record, `"--dry-run"`) {
		t.Errorf("record = %+v", recs[0])
	}
	if recs[1].Record != "" || !strings.Contains(recs[1].Note, "2 further") {
		t.Errorf("note = %+v", recs[1])
	}
	// Released once: a second end of the same run finds nothing held
	// and records nothing more.
	releaseRunCapabilities(ctx, deps, "7", "7-1")
	if recs, _, _ := store.RunCapabilityAudit(ctx, "7", 1); len(recs) != 2 {
		t.Errorf("a second release recorded again: %d records", len(recs))
	}
}

// A CL a grant's audit says is the task's own is linked to the task,
// once however many records name it; one it only refused is not.
func TestReleasingARunLinksTheTaskToItsCLs(t *testing.T) {
	caller := &mintCaller{releaseReply: `{"status":"ok","audit":[` +
		`{"time":"2026-10-22T12:05:00Z","attempt_id":"grain-7-1-0","tool":"citc_cl_create","changelist":"cl/123","task_changelist":"cl/123"},` +
		`{"time":"2026-10-22T12:06:00Z","attempt_id":"grain-7-1-0","tool":"citc_cl_mail","changelist":"cl/123","task_changelist":"cl/123"},` +
		`{"time":"2026-10-22T12:07:00Z","attempt_id":"grain-7-1-0","tool":"citc_cl_mail","changelist":"cl/99","result":"refused: not ours"},` +
		`{"time":"2026-10-22T12:08:00Z","attempt_id":"grain-7-1-0","tool":"x","task_changelist":"cl/12; rm -rf"}]}`}
	deps, store, ctx, now := passHarness(t, caller)
	if adm, err := capabilityAdmitter(deps, admitAll)(ctx, dispatch.Candidate{TaskID: "7", RunID: "7-1", Attempt: 1}); err != nil || !adm.Admitted {
		t.Fatalf("admit = %+v, %v", adm, err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "7-1", TaskID: "7", Attempt: 1, StartedAt: *now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	releaseRunCapabilities(ctx, deps, "7", "7-1")
	task, err := store.GetTask(ctx, "7")
	if err != nil || task == nil {
		t.Fatalf("GetTask = %v, %v", task, err)
	}
	var cls []string
	for _, l := range task.Links {
		if l.Kind == model.LinkChangelist {
			cls = append(cls, l.Target)
		}
	}
	if len(cls) != 1 || cls[0] != "cl/123" {
		t.Errorf("changelist links = %q, want only cl/123", cls)
	}
}

func TestCapabilityAuditRecordsNoteARefusedAudit(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	got := capabilityAuditRecords([]protocol.GrantAudit{{Grant: "x#m", AttemptID: "a-1-1-0", Refused: "audit record 0 has no time"}}, now)
	if len(got) != 1 || got[0].Record != "" || !strings.Contains(got[0].Note, "refused") || !got[0].At.Equal(now) {
		t.Errorf("records = %+v", got)
	}
}
