package demo

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

var _ ui.StateRepoManager = (*externalConfigRepo)(nil)

// The stub reports the repository the way a daemon's state repo does in
// external mode -- through ExternalConfigRepo, which the refusal reads
// first, and through Status, which Settings → Config Repo reads -- and
// refuses everything that would need a working tree.
func TestExternalConfigRepo(t *testing.T) {
	ctx := context.Background()
	r := newExternalConfigRepo(" https://github.com/acme/grain-config.git ", "")
	if remote, branch := r.ExternalConfigRepo(); remote != "https://github.com/acme/grain-config.git" || branch != "main" {
		t.Errorf("ExternalConfigRepo() = %q, %q; want the trimmed remote on main", remote, branch)
	}
	st, err := r.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Available || st.Mode != "external" || st.Remote != "https://github.com/acme/grain-config.git" || st.Branch != "main" {
		t.Errorf("Status() = %+v, want an available external repository on main", st)
	}
	if _, err := r.Sync(ctx); err == nil {
		t.Error("Sync succeeded with no working tree behind it")
	}
	if _, err := r.SetRemote(ctx, ui.StateRepoRemoteRequest{}); err == nil {
		t.Error("SetRemote succeeded with no working tree behind it")
	}
	if _, err := r.Tree(ctx); err == nil {
		t.Error("Tree succeeded with no working tree behind it")
	}
}
