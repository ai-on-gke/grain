package model_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestRedactSecretMaterialRemovesCredentialShapes(t *testing.T) {
	for _, material := range []string{
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"github_pat_11ABCDEFG0123456789_abcdefghijklmnop",
		"ya29.a0AfH6SMBexampleaccesstoken",
		"AIzaSyA1234567890abcdefghijklmnopqrstuv",
		"sk-proj-abcdefghijklmnopqrstuvwx",
		"-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\n-----END PRIVATE KEY-----",
	} {
		for _, wrap := range []string{
			"error: forwarding failed: %s",
			"Authorization: Bearer %s",
			"https://x-access-token:%s@github.com/o/r.git",
			`{"private_key": "%s"}`,
		} {
			in := strings.ReplaceAll(wrap, "%s", material)
			out := model.RedactSecretMaterial(in)
			if strings.Contains(out, material) {
				t.Errorf("RedactSecretMaterial(%q) = %q, still carries the material", in, out)
			}
		}
	}
	// A value with no recognisable shape is still caught where it sits in
	// a URL's userinfo or behind an Authorization scheme.
	for _, in := range []string{
		"https://user:hunter2secretpass@git.example.com/o/r.git",
		"Authorization: Basic hunter2secretpass",
	} {
		if out := model.RedactSecretMaterial(in); strings.Contains(out, "hunter2secretpass") {
			t.Errorf("RedactSecretMaterial(%q) = %q, still carries the password", in, out)
		}
	}
	// Ordinary outcomes pass through untouched.
	for _, plain := range []string{"forwarded: 200", "denied: not in scope", "served locally: 200"} {
		if got := model.RedactSecretMaterial(plain); got != plain {
			t.Errorf("RedactSecretMaterial(%q) = %q, want it unchanged", plain, got)
		}
	}
}

func TestSandboxAuditAttributionNamesTheLiveRunsTaskAndTeam(t *testing.T) {
	store, ctx := open(t)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	if err := store.PutTask(ctx, model.Task{
		ID: "a1b2", Intent: model.IntentImplement, Title: "t",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: human},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: "a1b2", Sandbox: "s1", Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := store.SandboxAuditAttribution(ctx, "s1")
	if err != nil || !ok {
		t.Fatalf("SandboxAuditAttribution(s1) = %v, %v, %v; want the live run", got, ok, err)
	}
	if want := (model.AuditAttribution{TaskID: "a1b2", RunID: "r1"}); got != want {
		t.Fatalf("SandboxAuditAttribution(s1) = %+v, want %+v", got, want)
	}
	if _, ok, err := store.SandboxAuditAttribution(ctx, "nobody"); err != nil || ok {
		t.Fatalf("SandboxAuditAttribution(nobody) = %v, %v; want nothing", ok, err)
	}

	if err := store.FinishRun(ctx, "r1", now.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.SandboxAuditAttribution(ctx, "s1"); err != nil || ok {
		t.Fatalf("SandboxAuditAttribution(s1) after finish = %v, %v; want nothing", ok, err)
	}
}

func TestTeamAuditEventsMergeBothTrails(t *testing.T) {
	store, ctx := open(t)
	base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	if err := store.RecordSecretAuditEntry(ctx, model.SecretAuditEntry{
		Timestamp: base, TaskID: "a1b2", RunID: "r1",
		SecretName: "gcp-sa", Key: "key.json",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCredentialAccess(ctx, model.CredentialAccessEntry{
		Timestamp: base.Add(time.Minute), TaskID: "a1b2", RunID: "r1", Sandbox: "s1",
		Resource: "acme/widgets", Action: "git-upload-pack", Credential: "eng-github",
		Outcome: "error: forwarding failed: https://x-access-token:ghp_abcdefghijklmnopqrstuvwxyz0123@github.com",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCredentialAccess(ctx, model.CredentialAccessEntry{
		Timestamp: base.Add(2 * time.Minute), Resource: "acme/infra",
		Action: "git-receive-pack", Credential: "ops-github", Outcome: "forwarded: 200",
	}); err != nil {
		t.Fatal(err)
	}

	events, err := store.ListTeamAuditEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v, want all three", events)
	}
	push, git, secret := events[0], events[1], events[2]
	if push.Resource != "acme/infra" {
		t.Fatalf("newest event = %+v, want the push", push)
	}
	if git.Kind != model.CredentialAccessGit || git.Resource != "acme/widgets" || git.Credential != "eng-github" ||
		git.TaskID != "a1b2" || git.RunID != "r1" || git.Sandbox != "s1" {
		t.Fatalf("second event = %+v, want the git access", git)
	}
	if strings.Contains(git.Outcome, "ghp_") {
		t.Fatalf("git outcome %q carries the token", git.Outcome)
	}
	if secret.Kind != model.TeamAuditKindSecret || secret.Resource != "gcp-sa/key.json" || secret.Action != "resolve" {
		t.Fatalf("oldest event = %+v, want the secret resolution", secret)
	}

	limited, err := store.ListTeamAuditEvents(ctx, 1)
	if err != nil || len(limited) != 1 || limited[0].Resource != "acme/infra" {
		t.Fatalf("events limited to 1 = %+v (%v), want the newest", limited, err)
	}
}
