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

package daemon_test

import (
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
)

func TestCheckUIAllowedPrincipalsRefusesGCPKeyPoolAccounts(t *testing.T) {
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")

	baseCfg := daemon.Config{
		GcpProject:             "my-proj",
		GcpServiceAccountEmail: "grain-agent@my-proj.iam.gserviceaccount.com",
		GcpServiceAccountCount: 2,
	}

	for _, bad := range []string{
		"serviceAccount:grain-agent@my-proj.iam.gserviceaccount.com",
		"serviceAccount:grain-agent-0@my-proj.iam.gserviceaccount.com",
		"serviceAccount:GRAIN-AGENT-1@my-proj.iam.gserviceaccount.com",
	} {
		cfg := baseCfg
		cfg.UiAllowedPrincipals = []string{"user:owner@example.com", bad}
		err := daemon.CheckUIAllowedPrincipals(cfg)
		if err == nil || !strings.Contains(err.Error(), "gcp-key") {
			t.Errorf("CheckUIAllowedPrincipals(%q) = %v, want error naming gcp-key", bad, err)
		}
	}

	// Same refusal when supplied through $GRAIN_ALLOWED_PRINCIPALS.
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "user:owner@example.com,serviceAccount:grain-agent-0@my-proj.iam.gserviceaccount.com")
	if err := daemon.CheckUIAllowedPrincipals(baseCfg); err == nil || !strings.Contains(err.Error(), "gcp-key") {
		t.Fatalf("CheckUIAllowedPrincipals via env = %v, want error naming gcp-key", err)
	}

	// A dedicated operator/hostd service account outside the gcp-key pool is accepted.
	t.Setenv("GRAIN_ALLOWED_PRINCIPALS", "")
	okCfg := baseCfg
	okCfg.UiAllowedPrincipals = []string{
		"user:owner@example.com",
		"serviceAccount:hostd-runner@my-proj.iam.gserviceaccount.com",
	}
	if err := daemon.CheckUIAllowedPrincipals(okCfg); err != nil {
		t.Fatalf("CheckUIAllowedPrincipals(non-pool SA) = %v, want nil", err)
	}
}
