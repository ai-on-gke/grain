package quarantine

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestQuarantineProviderSpec(t *testing.T) {
	p := New()
	spec := p.Spec()
	if spec.Name != CapabilityName {
		t.Fatalf("spec.Name = %q, want %q", spec.Name, CapabilityName)
	}
	if spec.Source != model.GrantByLabel {
		t.Fatalf("spec.Source = %v, want %v", spec.Source, model.GrantByLabel)
	}
	if spec.Provision != model.ProvisionGrant {
		t.Fatalf("spec.Provision = %v, want %v", spec.Provision, model.ProvisionGrant)
	}
	if spec.Description == "" {
		t.Fatalf("spec.Description should not be empty")
	}
}
