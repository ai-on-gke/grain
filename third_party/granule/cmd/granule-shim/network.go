package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/guestimage"
	"github.com/bwsalmon/granule/pkg/netshim"
)

// networkMode preserves old split-container manifests (NETSHIM_VM without
// a mode) and manually supplied CHV_NET. A new bare container defaults to
// inline setup. Explicit none leaves CHV_NET/CHV_CMDLINE untouched, including
// an empty CHV_NET for a networkless VM. Never silently downgrade a failed
// inline setup to a guest with no network.
func networkMode() (string, error) {
	mode := strings.TrimSpace(os.Getenv("CHV_NET_MODE"))
	if mode == "" {
		if netshim.Enabled() {
			return "external", nil
		}
		if _, manual := os.LookupEnv("CHV_NET"); manual {
			return "none", nil
		}
		return "inline", nil
	}
	switch mode {
	case "inline", "external", "none":
		return mode, nil
	default:
		return "", fmt.Errorf("CHV_NET_MODE: unknown mode %q (want inline, external or none)", mode)
	}
}

// configureNetwork runs after the live-VM socket guard and before the VMM
// starts, on both fresh boots and restores. Setup is idempotent, so container
// restarts inside a surviving Kubernetes pod namespace reuse the same taps.
// Dependencies are parameters so the ordering and failure paths are testable
// without changing the test runner's network namespace.
func configureNetwork(cfg *config.Config, setup func(netshim.Config) error, discover func(netshim.Config) (netshim.Identity, error)) error {
	mode, err := networkMode()
	if err != nil || mode == "none" {
		return err
	}
	if mode == "inline" && len(cfg.Nets) != 0 {
		return fmt.Errorf("CHV_NET conflicts with inline networking; use CHV_NET_MODE=none for a manually configured network")
	}
	ncfg, err := netshim.FromEnv()
	if err != nil {
		return err
	}
	if mode == "inline" {
		if err := setup(ncfg); err != nil {
			return fmt.Errorf("setting up inline networking (requires NET_ADMIN and /dev/net/tun; use CHV_NET_MODE=none for a networkless guest): %w", err)
		}
	}
	id, err := discover(ncfg)
	if err != nil {
		return err
	}
	// "auto" DNS is read off this namespace's own resolv.conf, which is
	// only the sandbox's in here, and kept to the families the guest
	// takes over.
	ncfg = ncfg.ResolveDNS(id)
	applyNetshimNet(cfg, ncfg, id)
	return nil
}

// ipv6ContractWarning is what to say about booting a guest image that
// predates IPv6 (guest contract < 3) into a namespace whose identity has
// an IPv6 half, or "" when there is nothing to say. Such an image ignores
// granule.ip6= and granule.routes6= -- which is why they are parameters
// of their own -- so its guest boots, and simply has no IPv6. For a
// v6-only identity that is no network at all, and the warning says so.
func ipv6ContractWarning(contract guestimage.BootContract, id netshim.Identity) string {
	if !id.HasIPv6() || contract.Version >= guestimage.ContractIPv6 {
		return ""
	}
	if !id.HasIPv4() {
		return fmt.Sprintf("warning: %s, below version %d, the first to configure IPv6, and %s's identity is IPv6 only (%s): this guest will have no network at all; repack or rebase it with this granule",
			contract.Describe(), guestimage.ContractIPv6, id.Iface, id.Addresses())
	}
	return fmt.Sprintf("warning: %s, below version %d, the first to configure IPv6: this guest image does not configure IPv6, so it will have none, though %s has %s/%d",
		contract.Describe(), guestimage.ContractIPv6, id.Iface, id.IP6, id.Prefix6)
}
