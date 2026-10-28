package orchestrator

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// recordingDNSEgress is a DNSEgressPolicy that counts what it was told.
type recordingDNSEgress struct {
	restricted map[string]int
}

func (r *recordingDNSEgress) RestrictClient(ip net.IP)   { r.restricted[ip.String()]++ }
func (r *recordingDNSEgress) UnrestrictClient(ip net.IP) { r.restricted[ip.String()]-- }

// TestNetworkNoneGuestIsRestrictedAtTheResolver: a VM with NetworkNone is
// named to the sandbox resolver under every container address (IPv6
// included), released
// exactly once when its isolation is cleaned up, and an address that
// cannot be found fails the enforcement rather than handing over a
// sandbox whose DNS still forwards anywhere.
func TestNetworkNoneGuestIsRestrictedAtTheResolver(t *testing.T) {
	policy := &recordingDNSEgress{restricted: map[string]int{}}
	k := NewGranuleSandboxes(GranuleConfig{DNSEgress: policy})
	k.guestIPs = func(ctx context.Context, vmName string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("172.18.0.7"), net.ParseIP("fd00:18::7")}, nil
	}
	ctx := context.Background()

	if err := k.restrictGuestDNS(ctx, "grain-vm-1"); err != nil {
		t.Fatalf("restrictGuestDNS: %v", err)
	}
	// Re-applied to the same VM (Reapply): still one restriction, not two.
	if err := k.restrictGuestDNS(ctx, "grain-vm-1"); err != nil {
		t.Fatalf("restrictGuestDNS again: %v", err)
	}
	for _, addr := range []string{"172.18.0.7", "fd00:18::7"} {
		if got := policy.restricted[addr]; got != 1 {
			t.Fatalf("restrictions held for %s = %d, want 1", addr, got)
		}
	}
	k.cleanupGranuleHostIsolation(ctx, "grain-vm-1")
	k.cleanupGranuleHostIsolation(ctx, "grain-vm-1")
	for _, addr := range []string{"172.18.0.7", "fd00:18::7"} {
		if got := policy.restricted[addr]; got != 0 {
			t.Fatalf("restrictions held for %s after cleanup = %d, want 0", addr, got)
		}
	}

	k.guestIPs = func(ctx context.Context, vmName string) ([]net.IP, error) {
		return nil, errors.New("no such container")
	}
	err := k.enforceGuestNetwork(ctx, "grain-vm-2", nil, Shape{NetworkAccess: model.NetworkNone}, netip.Addr{})
	if err == nil || !strings.Contains(err.Error(), "resolver") {
		t.Fatalf("enforceGuestNetwork with no findable address = %v, want a refusal naming the resolver", err)
	}
}
