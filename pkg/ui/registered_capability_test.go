package ui

import (
	"slices"
	"testing"

	"github.com/bwsalmon/grain/pkg/capability/capabilitytest"
)

// A capability registered from outside core is offered in the picker,
// after grain-log, under the name it was registered with, and Settings
// names it the same way.
func TestOfferedCapabilitiesIncludeRegisteredProviders(t *testing.T) {
	ids := func() []string {
		var ids []string
		for _, c := range OfferedCapabilities() {
			ids = append(ids, c.ID)
		}
		return ids
	}
	if slices.Contains(ids(), capabilitytest.ID) {
		t.Fatalf("%s is offered before it is registered", capabilitytest.ID)
	}
	capabilitytest.Register(t)

	offered := OfferedCapabilities()
	i := slices.IndexFunc(offered, func(c Capability) bool { return c.ID == capabilitytest.ID })
	if i < 0 {
		t.Fatalf("OfferedCapabilities() = %v, want %s among them", ids(), capabilitytest.ID)
	}
	if c := offered[i]; c.Name != capabilitytest.Name || c.Description != capabilitytest.Description {
		t.Errorf("offered %+v, want the registered name and description", c)
	}
	if i == 0 || offered[i-1].ID != "grain-log" {
		t.Errorf("%s offered at %d, want it straight after grain-log: %v", capabilitytest.ID, i, ids())
	}
	if got := capabilityDisplayName(capabilitytest.ID); got != capabilitytest.Name {
		t.Errorf("capabilityDisplayName = %q, want %q", got, capabilitytest.Name)
	}
	found := false
	for _, p := range capabilityProviderCatalog() {
		found = found || p.Spec().Name == capabilitytest.ID
	}
	if !found {
		t.Errorf("the provider catalog has no %s, so a task could not be filed holding it", capabilitytest.ID)
	}
}
