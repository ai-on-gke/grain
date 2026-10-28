package postgres_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// TestTeamGitConnectionsCRUD exercises the git_connections column on PostgreSQL:
// putting a team with multiple git connections across different hosts and scopes,
// reading it back via GetTeam, and testing TeamGitCredentialOn resolution.
func TestTeamGitConnectionsCRUD(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	// The deployment's connections, grain_config's since schema 22.
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitConnections = []model.TeamGitConnection{
			{Credential: "dotcom-app"},
			{Credential: "dotcom-acme", Scope: "acme"},
			{Credential: "ghe-a-pat", Host: "https://GHE-A.example.com/"},
		}
		return nil
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got == nil {
		t.Fatal("GetConfig returned nil")
	}
	if len(got.GitConnections) != 3 {
		t.Fatalf("got %d git connections, want 3: %+v", len(got.GitConnections), got.GitConnections)
	}
	if got.GitConnections[0].Credential != "dotcom-app" ||
		got.GitConnections[1].Credential != "dotcom-acme" ||
		got.GitConnections[2].Credential != "ghe-a-pat" {
		t.Fatalf("unexpected GitConnections: %+v", got.GitConnections)
	}

	// Verify resolution via TeamGitCredentialOn
	cred, err := store.GitConnectionOn(ctx, "", "acme", "widgets")
	if err != nil || cred != "dotcom-acme" {
		t.Errorf("TeamGitCredentialOn(acme/widgets) = %q, %v; want dotcom-acme", cred, err)
	}

	credGHE, err := store.GitConnectionOn(ctx, "ghe-a.example.com", "acme", "widgets")
	if err != nil || credGHE != "ghe-a-pat" {
		t.Errorf("TeamGitCredentialOn(ghe-a.example.com) = %q, %v; want ghe-a-pat", credGHE, err)
	}

	_, err = store.GitConnectionOn(ctx, "ghe-other.example.com", "acme", "widgets")
	if !errors.Is(err, model.ErrNoTeamGitCredential) || !strings.Contains(err.Error(), "ghe-other.example.com") {
		t.Errorf("expected ErrNoTeamGitCredential for unconfigured host, got: %v", err)
	}
}
