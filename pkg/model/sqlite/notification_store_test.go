package sqlite_test

// Operator notifications against a real database (model.Notification,
// docs/design/durable-tasks.md DT-5): raise is an upsert on the open
// subject, severity only rises, acknowledging is not clearing, and a
// cleared subject raised again is a new instance.

import (
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestRaisingAnOpenSubjectBumpsItAndSeverityOnlyRises(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	first, err := store.RaiseNotification(ctx, model.Notification{
		Subject: "pipeline.base-red:acme/x@main", Severity: model.NotificationCritical,
		Title: "main is red", Body: "two jobs", RaisedBy: "automation:test"})
	if err != nil {
		t.Fatal(err)
	}
	if !first.New || first.Notification.Family != "pipeline.base-red" {
		t.Fatalf("first raise = %+v, want a new instance in family pipeline.base-red", first)
	}
	again, err := store.RaiseNotification(ctx, model.Notification{
		Subject: "pipeline.base-red:acme/x@main", Severity: model.NotificationInfo,
		Title: "main is still red", Body: "three jobs", RaisedBy: "automation:test"})
	if err != nil {
		t.Fatal(err)
	}
	n := again.Notification
	if again.New || n.ID != first.Notification.ID {
		t.Fatalf("second raise made a new instance: %+v", again)
	}
	if n.RaiseCount != 2 || n.Severity != model.NotificationCritical || n.Title != "main is still red" {
		t.Fatalf("bumped = %+v, want count 2, still critical, the newer title", n)
	}
	open, err := store.ListNotifications(ctx, model.NotificationFilter{Open: true})
	if err != nil || len(open) != 1 {
		t.Fatalf("open = %v (%v), want exactly one", open, err)
	}
}

func TestAckIsNotClearAndARiseResurfacesIt(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	raise := func(sev model.NotificationSeverity) model.NotificationRaise {
		t.Helper()
		r, err := store.RaiseNotification(ctx, model.Notification{Subject: "agent.7:stuck", Severity: sev,
			Title: "stuck", RaisedBy: "agent:7"})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := raise(model.NotificationInfo)
	if err := store.AckNotification(ctx, r.Notification.ID, "human:alice", time.Now()); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.GetNotification(ctx, r.Notification.ID); n.AckedAt == nil || !n.Open() {
		t.Fatalf("after ack = %+v, want acknowledged and still open", n)
	}
	if r := raise(model.NotificationInfo); r.Notification.AckedAt == nil {
		t.Fatalf("a raise at the same severity re-surfaced an acknowledged notification")
	}
	if r := raise(model.NotificationWarning); !r.Rose || r.Notification.AckedAt != nil {
		t.Fatalf("a raise that rose = %+v, want its ack dropped", r)
	}
	if ok, err := store.ClearNotification(ctx, "agent.7:stuck", "human:alice"); err != nil || !ok {
		t.Fatalf("ClearNotification = %v, %v", ok, err)
	}
	if r := raise(model.NotificationInfo); !r.New || r.Notification.ID == 0 {
		t.Fatalf("raising a cleared subject = %+v, want a new instance", r)
	}
	all, _ := store.ListNotifications(ctx, model.NotificationFilter{})
	if len(all) != 2 {
		t.Fatalf("history = %d rows, want the cleared instance kept beside the new one", len(all))
	}
}

func TestClearsWhenIsValidatedAndTTLBecomesATime(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if _, err := store.RaiseNotification(ctx, model.Notification{Subject: "x:y", Title: "t", RaisedBy: "p:q",
		ClearsWhen: &model.NotificationClear{Kind: "never"}}); err == nil {
		t.Fatal("an unknown clears_when kind was accepted")
	}
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	r, err := store.RaiseNotification(ctx, model.Notification{Subject: "x:y", Title: "t", RaisedBy: "p:q",
		RaisedAt: now, ClearsWhen: &model.NotificationClear{Kind: model.ClearsOnTTL, TTL: "6h"}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetNotification(ctx, r.Notification.ID)
	if got.ClearsWhen == nil || got.ClearsWhen.At == nil || !got.ClearsWhen.At.Equal(now.Add(6*time.Hour)) {
		t.Fatalf("clears_when = %+v, want at %s", got.ClearsWhen, now.Add(6*time.Hour))
	}
}

func TestSubscriptionsMatchFamiliesAndPatterns(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.SetTaskNotificationSubscriptions(ctx, "12", []string{"pipeline.*", "task.no-progress"}); err != nil {
		t.Fatal(err)
	}
	for family, want := range map[string]int{"pipeline.base-red": 1, "task.no-progress": 1, "agent.4": 0} {
		got, err := store.NotificationSubscribers(ctx, family)
		if err != nil || len(got) != want {
			t.Errorf("subscribers of %s = %v (%v), want %d", family, got, err, want)
		}
	}
}
