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

func TestDaemonObjectStoreFlagAndValidation(t *testing.T) {
	t.Run("FlagSetsObjectStoreURI", func(t *testing.T) {
		t.Setenv("GRAIN_OBJECT_STORE", "")
		cfg, _, _, err := parseDaemonArgs([]string{
			"-data-dir", t.TempDir(),
			"-disable-native-sandboxes",
			"-object-store", "gs://grain-instances-prod-state/instances/calm-otter-7f3a/",
		})
		if err != nil {
			t.Fatalf("parseDaemonArgs: %v", err)
		}
		if cfg.ObjectStoreURI != "gs://grain-instances-prod-state/instances/calm-otter-7f3a/" {
			t.Fatalf("ObjectStoreURI = %q", cfg.ObjectStoreURI)
		}
		if err := checkConfig(cfg); err != nil {
			t.Fatalf("checkConfig: %v", err)
		}
	})

	t.Run("EnvFallbackAndFlagOverride", func(t *testing.T) {
		t.Setenv("GRAIN_OBJECT_STORE", "gs://env-bucket/instances/env-vm/")
		cfg, _, _, err := parseDaemonArgs([]string{
			"-data-dir", t.TempDir(),
			"-disable-native-sandboxes",
		})
		if err != nil {
			t.Fatalf("parseDaemonArgs: %v", err)
		}
		if cfg.ObjectStoreURI != "gs://env-bucket/instances/env-vm/" {
			t.Fatalf("ObjectStoreURI from env = %q", cfg.ObjectStoreURI)
		}
		if err := checkConfig(cfg); err != nil {
			t.Fatalf("checkConfig: %v", err)
		}

		cfgOverride, _, _, err := parseDaemonArgs([]string{
			"-data-dir", t.TempDir(),
			"-disable-native-sandboxes",
			"-object-store", "gs://flag-bucket/instances/flag-vm/",
		})
		if err != nil {
			t.Fatalf("parseDaemonArgs override: %v", err)
		}
		if cfgOverride.ObjectStoreURI != "gs://flag-bucket/instances/flag-vm/" {
			t.Fatalf("ObjectStoreURI override = %q", cfgOverride.ObjectStoreURI)
		}
	})

	t.Run("InvalidURIRejectedByCheckConfig", func(t *testing.T) {
		for _, bad := range []string{
			"s3://wrong-scheme/prefix",
			"gs://",
			"gs://bucket/instances/../escape",
			"file://relative/dir",
		} {
			cfg, _, _, err := parseDaemonArgs([]string{
				"-data-dir", t.TempDir(),
				"-disable-native-sandboxes",
				"-object-store", bad,
			})
			if err != nil {
				t.Fatalf("parseDaemonArgs(%q): %v", bad, err)
			}
			err = checkConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), "-object-store") {
				t.Errorf("checkConfig(%q) = %v, want error mentioning -object-store", bad, err)
			}
		}
	})
}
