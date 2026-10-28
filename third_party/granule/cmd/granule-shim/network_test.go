package main

import (
	"errors"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/config"
	"github.com/bwsalmon/granule/pkg/netshim"
)

func networkEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"CHV_NET_MODE", "CHV_NET", "NETSHIM_VM", "NETSHIM_BRIDGE", "NETSHIM_EXTERNAL_IFACE", "NETSHIM_CONTROL_CIDR", "NETSHIM_DNS"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func TestNetworkMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"bare container", nil, "inline"},
		{"old split deployment", map[string]string{"NETSHIM_VM": "web"}, "external"},
		{"custom net", map[string]string{"CHV_NET": "tap=mytap"}, "none"},
		{"empty custom net", map[string]string{"CHV_NET": ""}, "none"},
		{"explicit inline", map[string]string{"NETSHIM_VM": "web", "CHV_NET_MODE": "inline"}, "inline"},
		{"explicit external", map[string]string{"CHV_NET_MODE": "external"}, "external"},
		{"disabled", map[string]string{"NETSHIM_VM": "web", "CHV_NET_MODE": "none"}, "none"},
		{"invalid", map[string]string{"CHV_NET_MODE": "typo"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			networkEnv(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			mode, err := networkMode()
			if mode != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("mode = %q, err = %v, want %q", mode, err, tc.want)
			}
		})
	}
}

func networkIdentity() netshim.Identity {
	return netshim.Identity{
		Iface: "eth0", IP: net.IPv4(10, 2, 0, 7).To4(),
		MAC: net.HardwareAddr{2, 0, 0, 0, 0, 7}, MTU: 1450,
		Mask: net.CIDRMask(24, 32), Gateway: net.IPv4(10, 2, 0, 1).To4(),
		Routes: []netshim.Route{{Dst: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}, Gw: net.IPv4(10, 2, 0, 1)}},
	}
}

func TestConfigureNetwork_InlineBeforeGuestConfig(t *testing.T) {
	networkEnv(t)
	t.Setenv("CHV_NET_MODE", "inline")
	t.Setenv("NETSHIM_VM", "web")
	t.Setenv("NETSHIM_DNS", "10.2.0.53")
	t.Setenv("NETSHIM_EXTERNAL_IFACE", "net1")
	var order []string
	cfg := config.Config{Cmdline: "console=ttyS0 root=/dev/vda rw"}
	setup := func(ncfg netshim.Config) error {
		order = append(order, "setup")
		if ncfg.VM != "web" || ncfg.DNS[0].String() != "10.2.0.53" {
			t.Fatalf("wrong config: %+v", ncfg)
		}
		if len(cfg.Nets) != 0 {
			t.Fatal("guest config applied before setup")
		}
		return nil
	}
	discover := func(ncfg netshim.Config) (netshim.Identity, error) {
		order = append(order, "discover")
		if ncfg.ExternalIface != "net1" {
			t.Fatalf("iface = %q", ncfg.ExternalIface)
		}
		return networkIdentity(), nil
	}
	// Fresh boot and a second boot/restore in a surviving namespace take
	// the same path. No persisted "already set up" flag may skip setup.
	for i := 0; i < 2; i++ {
		cfg.Nets = nil
		if err := configureNetwork(&cfg, setup, discover); err != nil {
			t.Fatal(err)
		}
		if len(cfg.Nets) != 2 || !strings.Contains(cfg.Nets[0], "tap=tap-web") || !strings.Contains(cfg.Nets[0], "mtu=1450") {
			t.Fatalf("guest nets = %v", cfg.Nets)
		}
		for _, want := range []string{"ip=10.2.0.7", "10.2.0.53", "granule.routes="} {
			if !strings.Contains(cfg.Cmdline, want) {
				t.Fatalf("cmdline %q missing %q", cfg.Cmdline, want)
			}
		}
	}
	if !reflect.DeepEqual(order, []string{"setup", "discover", "setup", "discover"}) {
		t.Fatal(order)
	}
}

func TestConfigureNetwork_ExternalDoesNotNeedSetupPrivileges(t *testing.T) {
	networkEnv(t)
	t.Setenv("NETSHIM_VM", "web") // unchanged old init-container manifest
	cfg := config.Config{}
	err := configureNetwork(&cfg, func(netshim.Config) error {
		t.Fatal("legacy deployment tried to configure the kernel")
		return nil
	}, func(netshim.Config) (netshim.Identity, error) { return networkIdentity(), nil })
	if err != nil || len(cfg.Nets) == 0 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestConfigureNetwork_NoneLeavesManualConfigAlone(t *testing.T) {
	networkEnv(t)
	t.Setenv("CHV_NET_MODE", "none")
	cfg := config.Config{Nets: []string{"tap=custom"}, Cmdline: "ip=custom"}
	original := cfg
	err := configureNetwork(&cfg, func(netshim.Config) error {
		t.Fatal("none called setup")
		return nil
	}, func(netshim.Config) (netshim.Identity, error) {
		t.Fatal("none read the namespace")
		return netshim.Identity{}, nil
	})
	if err != nil || !reflect.DeepEqual(cfg, original) {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestConfigureNetwork_FailsClosed(t *testing.T) {
	for _, kind := range []string{"setup", "discover", "config", "mode", "conflicting net"} {
		t.Run(kind, func(t *testing.T) {
			networkEnv(t)
			t.Setenv("CHV_NET_MODE", "inline")
			cfg := config.Config{}
			if kind == "config" {
				t.Setenv("NETSHIM_DNS", "not-an-ip")
			}
			if kind == "mode" {
				t.Setenv("CHV_NET_MODE", "typo")
			}
			if kind == "conflicting net" {
				cfg.Nets = []string{"tap=custom"}
			}
			setupCalls, discoverCalls := 0, 0
			err := configureNetwork(&cfg, func(netshim.Config) error {
				setupCalls++
				if kind == "setup" {
					return errors.New("permission denied")
				}
				return nil
			}, func(netshim.Config) (netshim.Identity, error) {
				discoverCalls++
				return netshim.Identity{}, errors.New("no interface")
			})
			if err == nil {
				t.Fatal("expected failure")
			}
			if kind != "discover" && discoverCalls != 0 {
				t.Fatal("discovery after failure")
			}
			if (kind == "config" || kind == "mode" || kind == "conflicting net") && setupCalls != 0 {
				t.Fatal("setup after invalid config")
			}
		})
	}
}
