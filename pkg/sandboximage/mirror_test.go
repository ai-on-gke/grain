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

package sandboximage

import "testing"

func TestDerivedMirror(t *testing.T) {
	const stamped = "ghcr.io/example/grain/guest"
	for _, tc := range []struct {
		name, k8sGranule, flag string
		want                   MirrorHint
		ok                     bool
	}{
		{name: "nothing"},
		{
			name:       "sha guest pin in another project",
			k8sGranule: "us-docker.pkg.dev/proj/grain/guest:sha-abc@sha256:00",
			want:       MirrorHint{Field: "k8s_granule_image", Image: "us-docker.pkg.dev/proj/grain/guest:sha-abc@sha256:00", Repository: "us-docker.pkg.dev/proj/grain/guest"},
			ok:         true,
		},
		{
			name: "bare repository from the flag",
			flag: "us-docker.pkg.dev/proj/grain/guest",
			want: MirrorHint{Field: "-sandbox-image", Image: "us-docker.pkg.dev/proj/grain/guest", Repository: "us-docker.pkg.dev/proj/grain/guest"},
			ok:   true,
		},
		{name: "a real image of its own", k8sGranule: "us-docker.pkg.dev/proj/team/custom-guest:v1"},
		{name: "not a GCP registry", k8sGranule: "registry.example/grain/guest:sha-abc"},
		{name: "the stamped repository", k8sGranule: stamped + ":sha-abc"},
	} {
		got, ok := DerivedMirror(tc.k8sGranule, tc.flag, stamped)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: DerivedMirror = %+v, %v; want %+v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIsMirroredGuestRef(t *testing.T) {
	const repo = "us-docker.pkg.dev/proj/grain/guest"
	for ref, want := range map[string]bool{
		repo:                            true,
		repo + ":sha-abc@sha256:00":     true,
		repo + ":v3":                    true,
		repo + ":custom":                false,
		"us-docker.pkg.dev/proj/x:sha-": false,
		"":                              false,
	} {
		if got := IsMirroredGuestRef(ref, repo, "v3"); got != want {
			t.Errorf("IsMirroredGuestRef(%q) = %v, want %v", ref, got, want)
		}
	}
}
