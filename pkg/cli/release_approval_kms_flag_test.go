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

func TestDaemonReleaseApprovalKMSKeyFlag(t *testing.T) {
	for _, env := range []string{
		dataDirEnvVar, "GRAIN_DB_DRIVER", "GRAIN_DB_DSN", "GRAIN_POSTGRES_URL", "DATABASE_URL",
		"GRAIN_SECRETS_BACKEND", "GRAIN_SECRETS_SM_PREFIX", "GRAIN_RELEASE_APPROVAL_KMS_KEY",
	} {
		t.Setenv(env, "")
	}
	base := []string{"-data-dir", t.TempDir(), "-sandbox-dir", t.TempDir(), "-host-sandboxes", "-allow-unisolated-host-sandboxes"}
	const version = "projects/example-project/locations/global/keyRings/grain/cryptoKeys/release-approval/cryptoKeyVersions/1"
	parse := func(args ...string) (string, error) {
		t.Helper()
		cfg, _, _, err := parseDaemonArgs(append(append([]string{}, base...), args...))
		if err != nil {
			t.Fatalf("parseDaemonArgs: %v", err)
		}
		return cfg.ReleaseApprovalKMSKey, checkConfig(cfg)
	}

	if got, err := parse(); got != "" || err != nil {
		t.Fatalf("no flag: key %q, check %v; want the store-held key", got, err)
	}
	if got, err := parse("-release-approval-kms-key=" + version); got != version || err != nil {
		t.Fatalf("the flag: key %q, check %v; want %s", got, err, version)
	}
	t.Setenv("GRAIN_RELEASE_APPROVAL_KMS_KEY", version)
	if got, _ := parse(); got != version {
		t.Fatalf("$GRAIN_RELEASE_APPROVAL_KMS_KEY: key %q, want %s", got, version)
	}
	// A CryptoKey without its version names no one key pair.
	_, err := parse("-release-approval-kms-key=" + strings.TrimSuffix(version, "/cryptoKeyVersions/1"))
	if err == nil || !strings.Contains(err.Error(), "-release-approval-kms-key") {
		t.Fatalf("a CryptoKey name: check %v, want a -release-approval-kms-key refusal", err)
	}
}
