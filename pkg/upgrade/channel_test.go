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

package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestChannelForRef(t *testing.T) {
	for ref, want := range map[string]string{
		"ghcr.io/bwsalmon/grain/grain:latest":                 "latest",
		"ghcr.io/bwsalmon/grain/grain:latest@sha256:0123abcd": "latest",
		"ghcr.io/bwsalmon/grain/grain:candidate":              "candidate",
		"ghcr.io/bwsalmon/grain/grain:rc":                     "candidate",
		"ghcr.io/bwsalmon/grain/grain:rc@sha256:0123abcd":     "candidate",
		"ghcr.io/bwsalmon/grain/grain:main@sha256:0123abcd":   "main",
		"ghcr.io/bwsalmon/grain/grain:v1.9.0":                 "latest",
		"ghcr.io/bwsalmon/grain/grain:v2.0.0-google":          "latest",
		"ghcr.io/bwsalmon/grain/grain:v2.0.0-oss":             "latest",
		"ghcr.io/bwsalmon/grain/grain:v1.10.0-rc.1":           "candidate",
		"ghcr.io/bwsalmon/grain/grain:v1.10.0-rc.1-discarded": "",
		"ghcr.io/bwsalmon/grain/grain:v2.0.0-rc.1-google":     "candidate",
		"ghcr.io/bwsalmon/grain/grain:v2.0.0-rc.1-oss":        "candidate",
		"ghcr.io/bwsalmon/grain/grain:v2.0.0-google-rc.1":     "candidate",
		"ghcr.io/bwsalmon/grain/grain:release..2.1":           "release/2.1",
		"ghcr.io/bwsalmon/grain/grain:sha-1234567":            "",
		"ghcr.io/bwsalmon/grain/grain@sha256:0123abcd":        "",
		"localhost:5000/grain":                                "",
		"localhost:5000/grain:candidate":                      "candidate",
		"localhost:5000/grain:rc":                             "candidate",
		"":                                                    "",
	} {
		if got := ChannelForRef(ref); got != want {
			t.Errorf("ChannelForRef(%q) = %q, want %q", ref, got, want)
		}
	}
}

// TestTheOfferedUpgradeFollowsTheChannel: what the pane offers by
// default is the channel the deployment already runs, read off the ref
// file scripts/setup.sh (or the last upgrade) wrote -- so a deployment
// installed on latest is offered the next release, not main's head, and
// one moved onto candidate follows candidate from then on.
func TestTheOfferedUpgradeFollowsTheChannel(t *testing.T) {
	refFile := filepath.Join(t.TempDir(), "image.env")
	u := New(Config{
		Image:    &ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain", RefFile: refFile},
		ListTags: func(context.Context, string) ([]string, error) { return []string{"latest", "v1.9.0"}, nil },
	})

	// Nothing installed yet: no channel to follow, and nothing guessed.
	if got := u.Targets(context.Background()).Default; got != "" {
		t.Errorf("with no ref file, Default = %q, want none", got)
	}

	for _, tc := range []struct{ ref, want string }{
		{"ghcr.io/bwsalmon/grain/grain:latest@sha256:0123abcd", "latest"},
		{"ghcr.io/bwsalmon/grain/grain:v1.9.0", "latest"},
		{"ghcr.io/bwsalmon/grain/grain:v2.0.0-google", "latest"},
		{"ghcr.io/bwsalmon/grain/grain:v2.0.0-rc.1-google", "candidate"},
		{"ghcr.io/bwsalmon/grain/grain:candidate", "candidate"},
		{"ghcr.io/bwsalmon/grain/grain:rc", "candidate"},
		{"ghcr.io/bwsalmon/grain/grain:main", "main"},
		{"ghcr.io/bwsalmon/grain/grain:sha-1234567", ""},
	} {
		if err := os.WriteFile(refFile, []byte("GRAIN_IMAGE="+tc.ref+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := u.Channel(); got != tc.want {
			t.Errorf("running %s, Channel() = %q, want %q", tc.ref, got, tc.want)
		}
		targets := u.Targets(context.Background())
		if targets.Default != tc.want {
			t.Errorf("running %s, Targets().Default = %q, want %q", tc.ref, targets.Default, tc.want)
		}
		if tc.want != "" && checkAllowed(targets, tc.want) != nil {
			t.Errorf("running %s, the default %q is not offered as an allowed target: %+v", tc.ref, tc.want, targets.Targets)
		}
	}

	// The source pipeline runs a binary it built, which follows no tag.
	if got := New(Config{SrcDir: t.TempDir()}).Channel(); got != "" {
		t.Errorf("source pipeline Channel() = %q, want none", got)
	}
}

// checkAllowed is nil when name is listed in targets and allowed.
func checkAllowed(targets Targets, name string) error {
	for _, target := range targets.Targets {
		if target.Name == name && target.Allowed {
			return nil
		}
	}
	return os.ErrNotExist
}
