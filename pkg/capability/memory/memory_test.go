package memory_test

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/memory"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestSpecIsAGrantThatMintsNothing(t *testing.T) {
	spec := memory.New().Spec()
	if spec.Name != memory.CapabilityName {
		t.Errorf("name = %q", spec.Name)
	}
	if spec.Provision != model.ProvisionGrant {
		t.Errorf("provision = %q, want a grant: there is nothing here to mint", spec.Provision)
	}
	if spec.MaxLease != 0 {
		t.Errorf("MaxLease = %s, want none: nothing is minted, so nothing expires", spec.MaxLease)
	}
	if len(spec.Requires) != 0 {
		t.Errorf("Requires = %v, want none: this capability resolves no credential", spec.Requires)
	}
	if spec.Description == "" {
		t.Error("a capability with no description is a picker row nobody can read")
	}
}

func TestMaterializeMovesNothingIntoASandbox(t *testing.T) {
	p := memory.New()
	ctx := context.Background()
	res, err := p.Resolve(ctx, model.CapabilityContext{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Refused {
		t.Errorf("resolve refused: %s", res.Reason)
	}
	m, err := p.Materialize(ctx, model.CapabilityContext{})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if len(m.Placements) != 0 || m.Lease != nil || m.CredentialOverride != nil {
		t.Errorf("memory must place nothing and mint nothing: %+v", m)
	}
}
