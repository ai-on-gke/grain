package ui_test

// Operator notifications and message_task through the client
// (docs/design/durable-tasks.md DT-5, DT-10): an agent's raise is
// namespaced and bounded, it never parks the sender, and a note reaches
// another task's run without changing that task's state.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

func grantTask(t *testing.T, store *model.Store, id string, edit func(*model.Task)) {
	t.Helper()
	if err := store.UpdateTask(t.Context(), id, func(task *model.Task) error {
		edit(task)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentNotifyNamespacesItsSubjectAndNeverParks(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)

	resp, err := client.AgentNotify(ctx, task.ID, ui.RaiseNotificationRequest{
		Subject: "pipeline.base-red:acme/widgets@main", Title: "main looks red"})
	if err != nil {
		t.Fatalf("AgentNotify: %v", err)
	}
	if want := ui.AgentNamespace(task.ID) + "pipeline.base-red:acme/widgets@main"; resp.Notification.Subject != want {
		t.Errorf("subject = %q, want %q: a run may not raise a reconciler's subject", resp.Notification.Subject, want)
	}
	if resp.Notification.TaskID != task.ID || resp.Notification.RaisedBy != "agent:"+task.ID {
		t.Errorf("notification = %+v, want it about and raised by task %s", resp.Notification, task.ID)
	}
	if obs, _ := store.GetObservation(ctx, task.ID); obs != nil && obs.PendingQuestionCommentID != nil {
		t.Errorf("raising a notification parked its sender")
	}
	if _, err := client.AgentNotify(ctx, task.ID, ui.RaiseNotificationRequest{
		Subject: "x", Title: "on fire", Severity: "critical"}); err == nil {
		t.Errorf("a run without api:task-admin raised a critical notification")
	}
}

func TestAgentNotifyIsBoundedPerRun(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	running(t, store, ctx, task.ID)
	for i := 0; i < ui.MaxAgentRaisesPerRun; i++ {
		if _, err := client.AgentNotify(ctx, task.ID, ui.RaiseNotificationRequest{Subject: "same", Title: "again"}); err != nil {
			t.Fatalf("raise %d: %v", i+1, err)
		}
	}
	_, err := client.AgentNotify(ctx, task.ID, ui.RaiseNotificationRequest{Subject: "same", Title: "again"})
	if err == nil || !strings.Contains(err.Error(), "already raised") {
		t.Fatalf("raise %d = %v, want a refusal saying why", ui.MaxAgentRaisesPerRun+1, err)
	}
}

func TestTaskAdminRaisesUnnamespacedAndCritical(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	grantTask(t, store, task.ID, func(m *model.Task) {
		m.Grants = append(m.Grants, model.Grant{Capability: "api:task-admin"})
	})
	resp, err := client.AgentNotify(ctx, task.ID, ui.RaiseNotificationRequest{
		Subject: "request.force-merge:12", Title: "please force-merge 12", Severity: "critical"})
	if err != nil {
		t.Fatalf("AgentNotify: %v", err)
	}
	if resp.Notification.Subject != "request.force-merge:12" || resp.Notification.Severity != model.NotificationCritical {
		t.Errorf("notification = %+v, want the subject as given, critical", resp.Notification)
	}
}

func TestAckNeedsAPersonAndClearsNothing(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	resp, err := client.RaiseNotification(ctx, ui.RaiseNotificationRequest{Subject: "ops:disk", Title: "disk at 90%"})
	if err != nil {
		t.Fatal(err)
	}
	n, err := client.AckNotification(ctx, resp.Notification.ID)
	if err != nil {
		t.Fatalf("AckNotification: %v", err)
	}
	if n.AckedAt == nil || !n.Open() || n.AckedBy != "human:alice" {
		t.Errorf("acked = %+v, want acknowledged by alice and still open", n)
	}
	agentCtx := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: "9"})
	if _, err := client.AckNotification(agentCtx, resp.Notification.ID); err == nil {
		t.Errorf("a run acknowledged a notification")
	}
	cleared, err := client.ClearNotification(ctx, resp.Notification.ID)
	if err != nil || cleared.Open() {
		t.Fatalf("ClearNotification = %+v, %v", cleared, err)
	}
}

func TestInvestigateFilesOneApprovedTaskPerInstance(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	resp, err := client.RaiseNotification(ctx, ui.RaiseNotificationRequest{Subject: "ops:disk", Title: "disk at 90%"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.InvestigateNotification(ctx, resp.Notification.ID)
	if err != nil {
		t.Fatalf("InvestigateNotification: %v", err)
	}
	second, err := client.InvestigateNotification(ctx, resp.Notification.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Filed || second.Filed || first.TaskID != second.TaskID {
		t.Fatalf("investigate twice = %+v then %+v, want one task", first, second)
	}
	task, err := store.GetTask(ctx, first.TaskID)
	if err != nil || task == nil {
		t.Fatalf("investigation task: %v", err)
	}
	if task.Approval == nil || !strings.Contains(task.Body, "ops:disk") {
		t.Errorf("investigation = approval %v, body %q; want approved and quoting the notification", task.Approval, task.Body)
	}
}

func TestMessageTaskNeedsKinshipAndReachesTheRun(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	parent := create(t, client, ctx)
	child := create(t, client, ctx)
	stranger := create(t, client, ctx)
	grantTask(t, store, child.ID, func(m *model.Task) {
		m.Links = append(m.Links, model.Link{Kind: model.LinkProposedBy, Target: parent.ID})
	})
	running(t, store, ctx, child.ID)

	if _, err := client.PostTaskNote(ctx, child.ID, stranger.ID, "do it my way"); err == nil ||
		!strings.Contains(err.Error(), "task-admin") {
		t.Fatalf("a stranger's note = %v, want a refusal naming api:task-admin", err)
	}
	note, err := client.PostTaskNote(ctx, child.ID, parent.ID, "the base is green again")
	if err != nil {
		t.Fatalf("PostTaskNote: %v", err)
	}
	if obs, _ := store.GetObservation(ctx, child.ID); obs != nil && obs.PendingQuestionCommentID != nil {
		t.Errorf("a note parked its target")
	}
	messages, err := client.AddendaSince(ctx, child.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 || messages[len(messages)-1].ID != note.ID {
		t.Fatalf("messages = %+v, want the note handed to the live run", messages)
	}
}

// message_task's target and notify_operator's task are ids, never paths:
// the daemon's hop carries no principal, so a target of "42/messages?"
// that reached POST /api/tasks/42/messages would park task 42 on a
// question as the trusted hop. Each is escaped into its one segment.
func TestNotifyHopsEscapeTheirTaskIDs(t *testing.T) {
	t.Parallel()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath()+"|"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()
	c := ui.NewHTTPClient(srv.URL)
	if _, err := c.PostTaskNote(t.Context(), "7", "42/messages?x=", "hi"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Notify(t.Context(), "7/../42", ui.RaiseNotificationRequest{Subject: "s", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"/api/tasks/42%2Fmessages%3Fx=/notes|", "/api/tasks/7%2F..%2F42/notify|"}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("paths = %q, want %q", paths, want)
	}
}
