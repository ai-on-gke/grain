package daemon

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Capability servers are sent the deployment id the branch names carry
// (Store.DeploymentID): the default derived from the installation id
// until a system admin sets one, then theirs, with the installation id
// beside it either way.
func TestCapabilityServersAreSentTheBranchDeploymentID(t *testing.T) {
	store, ctx := openStuckTestStore(t)
	instance, err := store.InstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	identity := servers.StoreIdentity(store, t.Logf)
	want := protocol.Identity{DeploymentID: model.EffectiveDeploymentID("", instance), InstanceID: instance}
	if got := identity(); got != want || len(got.DeploymentID) != 8 {
		t.Fatalf("default identity = %+v, want %+v", got, want)
	}
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.DeploymentID = "bsalmon"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	branchID, err := store.DeploymentID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := identity(); got.DeploymentID != "bsalmon" || got.DeploymentID != branchID || got.InstanceID != instance {
		t.Errorf("configured identity = %+v, branch names use %s", got, branchID)
	}
}
