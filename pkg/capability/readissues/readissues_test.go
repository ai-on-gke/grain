package readissues_test

import (
	"context"
	"testing"

	"github.com/bwsalmon/grain/pkg/capability/readissues"
	"github.com/bwsalmon/grain/pkg/model"
)

func TestSpecIsAGrantThatMintsNothing(t *testing.T) {
	spec := readissues.New().Spec()
	if spec.Name != readissues.CapabilityName {
		t.Errorf("name = %q, want %q", spec.Name, readissues.CapabilityName)
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
	p := readissues.New()
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
		t.Errorf("the read-issues capability must place nothing and mint nothing: %+v", m)
	}
}
