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

package daemon

import (
	"strings"
	"testing"
)

// -k8s-granule-access=dialout is refused off-cluster and without the
// acceptor; there is no fallback to kubectl exec
// (docs/design/grain-on-gke.md#GKE-3).
func TestValidateK8sGranuleAccess(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cfg       Config
		inCluster bool
		wantErr   string
	}{
		{name: "default", cfg: Config{}, inCluster: false},
		{name: "kubectl off-cluster", cfg: Config{K8sGranuleAccess: "kubectl"}, inCluster: false},
		{name: "dialout in-cluster", cfg: Config{K8sGranuleAccess: "dialout", SandboxAccessAddr: ":4244"}, inCluster: true},
		{name: "dialout off-cluster", cfg: Config{K8sGranuleAccess: "dialout", SandboxAccessAddr: ":4244"},
			wantErr: "needs grain running in-cluster"},
		{name: "dialout without acceptor", cfg: Config{K8sGranuleAccess: "dialout"}, inCluster: true,
			wantErr: "needs -sandbox-access-addr"},
		{name: "unknown mode", cfg: Config{K8sGranuleAccess: "ssh"}, inCluster: true, wantErr: `want "kubectl" or "dialout"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateK8sGranuleAccess(tc.cfg, tc.inCluster)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("validateK8sGranuleAccess: %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("validateK8sGranuleAccess: %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}
