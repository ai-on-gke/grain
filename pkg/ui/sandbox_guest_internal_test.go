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

package ui

import "testing"

// A release stamps its guest by digest as well as by tag
// (scripts/release-images.sh), and the tag in front of the digest still
// says which commit it was built from.
func TestGuestImageCommitReadsThroughADigest(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for image, want := range map[string]string{
		"ghcr.io/bwsalmon/grain/guest:sha-7746d62":           "7746d62",
		"ghcr.io/bwsalmon/grain/guest:sha-7746d62@" + digest: "7746d62",
		"ghcr.io/bwsalmon/grain/guest@" + digest:             "",
		"ghcr.io/bwsalmon/grain/guest:main@" + digest:        "",
		"localhost:5000/bwsalmon/grain/guest:sha-abc1234":    "abc1234",
		"localhost:5000/bwsalmon/grain/guest@" + digest:      "",
	} {
		if got := guestImageCommit(image); got != want {
			t.Errorf("guestImageCommit(%q) = %q, want %q", image, got, want)
		}
	}
}
