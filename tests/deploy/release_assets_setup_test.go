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

package deploy

import (
	"strings"
	"testing"
)

// The release setup.sh downloads from is a path segment of a github.com
// URL, and curl normalises "..": a GRAIN_IMAGE_TAG such as
// sha-x/../../<owner>/<repo> would otherwise fetch images.json -- and every
// tarball it names -- from a repository other than GRAIN_RELEASE_REPO
// (task 3039's review of the release-asset installer). Every name
// release_for_tag prints is held to an exact pattern; anything else fails.
func TestReleaseForTagNamesOnlyAReleaseOfTheRepository(t *testing.T) {
	fn := shellFunction(t, "release_for_tag")
	for tag, want := range map[string]string{
		"latest":           "latest",
		"main":             "channel-main",
		"candidate":        "channel-candidate",
		"rc":               "channel-candidate",
		"release/2.1":      "channel-release-2.1",
		"release..2.1":     "channel-release-2.1",
		"channel-main":     "channel-main",
		"sha-0123456789ab": "sha-0123456789ab",
		"v3.4.0":           "v3.4.0",
		"v3.5.0-rc.1":      "v3.5.0-rc.1",
	} {
		got := strings.TrimSpace(runBash(t, fn+`release_for_tag "$T"`, "T="+tag))
		if got != want {
			t.Errorf("release_for_tag %q = %q, want %q", tag, got, want)
		}
	}
	for _, tag := range []string{
		"sha-x/../../evil/repo", "sha-../../evil", "v1/../../evil", "v3.4.0/../../x",
		"channel-../x", "channel-a/b", "a b", "main?x=1", "main#frag",
	} {
		got := strings.TrimSpace(runBash(t, fn+`release_for_tag "$T" || echo REFUSED`, "T="+tag))
		if got != "REFUSED" {
			t.Errorf("release_for_tag %q = %q; a tag that is no release name must be refused", tag, got)
		}
	}

	// And install_release_assets holds the repository and every manifest
	// field to the contract before it is used: GRAIN_IMAGE_REF lands in the
	// unit's root-owned EnvironmentFile, and only a grain/ ref is never
	// pulled (release_asset_ref), so a ref with a registry host, another
	// namespace, a space or a newline must never get that far.
	install := body(t, setupText(t), "install_release_assets() {")
	for _, want := range []string{
		`[[ "$GRAIN_RELEASE_REPO" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]]`,
		`release="$(release_for_tag "$GRAIN_IMAGE_TAG")" || {`,
		`[[ "$asset" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*\.tar\.gz$ ]]`,
		`[[ "$sum" =~ ^[0-9a-f]{64}$ ]]`,
		`[[ "$id" =~ ^sha256:[0-9a-f]{64}$ ]]`,
		`[[ "$ref" =~ ^grain/[a-z0-9][a-z0-9._-]*:[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$ ]]`,
	} {
		contains(t, install, want)
	}
	// install-grain.sh holds -repo and the same fields to the same rules.
	installer := read(t, "scripts", "install-grain.sh")
	for _, want := range []string{
		`[[ "$repo" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]]`,
		`[[ "$asset" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*\.tar\.gz$ ]]`,
		`[[ "$sum" =~ ^[0-9a-f]{64}$ ]]`,
		`[[ "$id" =~ ^sha256:[0-9a-f]{64}$ ]]`,
		`[[ "$ref" =~ ^grain/[a-z0-9][a-z0-9._-]*:[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$ ]]`,
	} {
		contains(t, installer, want)
	}
}

// A release-asset install records the local grain/grain ref it loaded,
// tagged with the name it was asked for as well as sha-<short>. A re-run
// keeps that channel, as it keeps a registry install's: before, grain/grain
// was no repository resolve_image_tag would keep a tag from, so every
// re-run moved a main or candidate host onto latest.
func TestSetupReRunKeepsAReleaseAssetHostsChannel(t *testing.T) {
	for _, tc := range []struct{ ref, tag, channel string }{
		{"grain/grain:main", "main", "main"},
		{"grain/grain:candidate", "candidate", "candidate"},
		{"grain/grain:sha-0123456789ab", "sha-0123456789ab", ""},
	} {
		f := refFile(t, tc.ref)
		out, tag, channel, ok := resolveTag(t, nil, f, f)
		if !ok || tag != tc.tag || channel != tc.channel {
			t.Errorf("re-run over %s settled on tag=%q channel=%q (ok=%v), want %q %q:\n%s",
				tc.ref, tag, channel, ok, tc.tag, tc.channel, out)
		}
	}
	// grain/grain is only ever a repository a re-run keeps a tag from,
	// never one it moves onto: it would be pulled from Docker Hub.
	f := refFile(t, "docker.io/grainai/grain:main")
	out, tag, _, _ := resolveTag(t, []string{"GRAIN_IMAGE=grain/grain"}, f, f)
	if tag != "latest" {
		t.Errorf("a re-run onto GRAIN_IMAGE=grain/grain kept tag %q; want latest (no shift onto grain/grain):\n%s", tag, out)
	}

	install := body(t, setupText(t), "install_release_assets() {")
	contains(t, install, `docker tag "$ref" "${ref%:*}:$GRAIN_IMAGE_TAG"`)
}
