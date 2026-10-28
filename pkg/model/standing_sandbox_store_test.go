package model_test

import (
	"context"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
)

func openTestStore(t *testing.T) *model.Store {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := model.New(db)
	if err := st.Init(context.Background()); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return st
}

func TestStandingSandboxCRUD(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTestStore(t)

	// List on empty store
	list, err := st.ListStandingSandboxes(ctx)
	if err != nil {
		t.Fatalf("ListStandingSandboxes: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("got %d sandboxes, want 0", len(list))
	}

	// Create a standing sandbox
	sb1 := model.StandingSandbox{
		Name:          "staging-app",
		Image:         "docker.io/repo/granule-app:v1.2",
		Command:       "/bin/serve --port 8080",
		Dir:           "/app",
		CPUs:          2,
		MemoryMB:      4096,
		DiskGB:        20,
		NetworkAccess: model.NetworkOpen,
		Enabled:       true,
		Env: map[string]string{
			"APP_ENV": "staging",
			"DEBUG":   "false",
		},
		Secrets: []model.StandingSandboxSecret{
			{
				SandboxName: "staging-app",
				SecretName:  "gcp/sa-key",
				FilePath:    "/etc/secrets/gcp.json",
			},
			{
				SandboxName: "staging-app",
				SecretName:  "db/password",
				EnvVar:      "DB_PASSWORD",
			},
		},
	}

	if err := st.PutStandingSandbox(ctx, sb1); err != nil {
		t.Fatalf("PutStandingSandbox: %v", err)
	}

	// Get standing sandbox
	got, err := st.GetStandingSandbox(ctx, "staging-app")
	if err != nil {
		t.Fatalf("GetStandingSandbox: %v", err)
	}
	if got == nil {
		t.Fatalf("GetStandingSandbox returned nil")
	}
	if got.Name != sb1.Name || got.Image != sb1.Image || got.Command != sb1.Command || got.Dir != sb1.Dir {
		t.Errorf("got %+v, want %+v", got, sb1)
	}
	if got.CPUs != 2 || got.MemoryMB != 4096 || got.DiskGB != 20 || !got.Enabled {
		t.Errorf("shape mismatch: %+v", got)
	}
	if got.Env["APP_ENV"] != "staging" || got.Env["DEBUG"] != "false" {
		t.Errorf("env mismatch: %+v", got.Env)
	}
	if len(got.Secrets) != 2 {
		t.Fatalf("got %d secrets, want 2", len(got.Secrets))
	}

	// List standing sandboxes
	list, err = st.ListStandingSandboxes(ctx)
	if err != nil {
		t.Fatalf("ListStandingSandboxes: %v", err)
	}
	if len(list) != 1 || list[0].Name != "staging-app" {
		t.Fatalf("ListStandingSandboxes returned %+v", list)
	}

	// Update standing sandbox
	sb1.Image = "docker.io/repo/granule-app:v1.3"
	sb1.Env["NEW_VAR"] = "val"
	if err := st.PutStandingSandbox(ctx, sb1); err != nil {
		t.Fatalf("PutStandingSandbox update: %v", err)
	}
	got, err = st.GetStandingSandbox(ctx, "staging-app")
	if err != nil {
		t.Fatalf("GetStandingSandbox after update: %v", err)
	}
	if got.Image != "docker.io/repo/granule-app:v1.3" || got.Env["NEW_VAR"] != "val" {
		t.Errorf("updated fields mismatch: %+v", got)
	}

	// Delete standing sandbox
	if err := st.DeleteStandingSandbox(ctx, "staging-app"); err != nil {
		t.Fatalf("DeleteStandingSandbox: %v", err)
	}
	got, err = st.GetStandingSandbox(ctx, "staging-app")
	if err != nil {
		t.Fatalf("GetStandingSandbox after delete: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}

func TestStandingSandboxValidation(t *testing.T) {
	t.Parallel()

	// Invalid name
	err := model.ValidateStandingSandbox(model.StandingSandbox{
		Name:  "invalid name with spaces",
		Image: "docker.io/repo/image:v1",
	})
	if err == nil {
		t.Errorf("expected validation error for invalid name")
	}

	// Empty image
	err = model.ValidateStandingSandbox(model.StandingSandbox{
		Name:  "valid-name",
		Image: "",
	})
	if err == nil {
		t.Errorf("expected validation error for empty image")
	}

	// Empty secret name
	err = model.ValidateStandingSandbox(model.StandingSandbox{
		Name:  "valid-name",
		Image: "docker.io/repo/image:v1",
		Secrets: []model.StandingSandboxSecret{
			{SecretName: ""},
		},
	})
	if err == nil {
		t.Errorf("expected validation error for empty secret name")
	}

	// Secret with neither env_var nor file_path
	err = model.ValidateStandingSandbox(model.StandingSandbox{
		Name:  "valid-name",
		Image: "docker.io/repo/image:v1",
		Secrets: []model.StandingSandboxSecret{
			{SecretName: "secret-key"},
		},
	})
	if err == nil {
		t.Errorf("expected validation error for secret without destination")
	}
}
