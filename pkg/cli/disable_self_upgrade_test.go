// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli

import (
	"strings"
	"testing"
)

func TestDaemonDisableSelfUpgradeFlagsAndEnv(t *testing.T) {
	t.Run("flags", func(t *testing.T) {
		t.Setenv("GRAIN_DISABLE_SELF_UPGRADE", "")
		t.Setenv("GRAIN_MANAGEMENT_URL", "")

		cfg, _, _, err := parseDaemonArgs([]string{
			"-data-dir", t.TempDir(),
			"-disable-native-sandboxes",
			"-disable-self-upgrade",
			"-management-url", "https://grain.example.com/entries/alice",
			// An unreferenced -upgrade-image or -upgrade-src-dir left in a unit
			// file is inert when -disable-self-upgrade is set, so checkConfig
			// must not demand -upgrade-image-ref-file or -upgrade-install-path.
			"-upgrade-image", "docker.io/grainai/grain",
			"-upgrade-src-dir", "/srv/grain-src",
		})
		if err != nil {
			t.Fatalf("parseDaemonArgs: %v", err)
		}
		if !cfg.DisableSelfUpgrade {
			t.Fatalf("DisableSelfUpgrade = false, want true")
		}
		if cfg.ManagementURL != "https://grain.example.com/entries/alice" {
			t.Fatalf("ManagementURL = %q, want https://grain.example.com/entries/alice", cfg.ManagementURL)
		}
		if err := checkConfig(cfg); err != nil {
			t.Fatalf("checkConfig: %v", err)
		}
	})

	t.Run("env fallback", func(t *testing.T) {
		t.Setenv("GRAIN_DISABLE_SELF_UPGRADE", "true")
		t.Setenv("GRAIN_MANAGEMENT_URL", "http://127.0.0.1:8080/entries/dev")

		cfg, _, _, err := parseDaemonArgs([]string{
			"-data-dir", t.TempDir(),
			"-disable-native-sandboxes",
		})
		if err != nil {
			t.Fatalf("parseDaemonArgs: %v", err)
		}
		if !cfg.DisableSelfUpgrade {
			t.Fatalf("DisableSelfUpgrade = false, want true from GRAIN_DISABLE_SELF_UPGRADE")
		}
		if cfg.ManagementURL != "http://127.0.0.1:8080/entries/dev" {
			t.Fatalf("ManagementURL = %q, want http://127.0.0.1:8080/entries/dev", cfg.ManagementURL)
		}
		if err := checkConfig(cfg); err != nil {
			t.Fatalf("checkConfig: %v", err)
		}
	})

	t.Run("invalid env bool refused", func(t *testing.T) {
		t.Setenv("GRAIN_DISABLE_SELF_UPGRADE", "not-a-bool")
		_, _, _, err := parseDaemonArgs([]string{
			"-data-dir", t.TempDir(),
			"-disable-native-sandboxes",
		})
		if err == nil || !strings.Contains(err.Error(), "GRAIN_DISABLE_SELF_UPGRADE") {
			t.Fatalf("parseDaemonArgs err = %v, want GRAIN_DISABLE_SELF_UPGRADE error", err)
		}
	})

	t.Run("invalid management url refused", func(t *testing.T) {
		t.Setenv("GRAIN_DISABLE_SELF_UPGRADE", "")
		t.Setenv("GRAIN_MANAGEMENT_URL", "")
		for _, bad := range []string{"javascript:alert(1)", "/entries/alice", "https://"} {
			cfg, _, _, err := parseDaemonArgs([]string{
				"-data-dir", t.TempDir(),
				"-disable-native-sandboxes",
				"-management-url", bad,
			})
			if err != nil {
				t.Fatalf("parseDaemonArgs(%q): %v", bad, err)
			}
			if err := checkConfig(cfg); err == nil || !strings.Contains(err.Error(), "-management-url") {
				t.Fatalf("checkConfig(%q) = %v, want -management-url error", bad, err)
			}
		}
	})
}

func TestParseDemoFlagsDisableSelfUpgrade(t *testing.T) {
	opts, err := parseDemoFlags([]string{
		"-disable-self-upgrade",
		"-management-url", "https://grain.example.com/entries/demo",
	})
	if err != nil {
		t.Fatalf("parseDemoFlags: %v", err)
	}
	if !opts.DisableSelfUpgrade {
		t.Fatalf("DisableSelfUpgrade = false, want true")
	}
	if opts.ManagementURL != "https://grain.example.com/entries/demo" {
		t.Fatalf("ManagementURL = %q, want https://grain.example.com/entries/demo", opts.ManagementURL)
	}

	if _, err := parseDemoFlags([]string{"-management-url", "javascript:alert(1)"}); err == nil || !strings.Contains(err.Error(), "-management-url") {
		t.Fatalf("parseDemoFlags(bad -management-url) = %v, want -management-url error", err)
	}
}
