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

package hostd

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCurrentLinkForIsTheDirectoryOnlyWhenItIsCurrent(t *testing.T) {
	for argv0, want := range map[string]string{
		"/usr/local/lib/grain/hostd/current/grain": "/usr/local/lib/grain/hostd/current",
		"/usr/local/bin/grain":                     "",
		"current/grain":                            "",
		"grain":                                    "",
	} {
		if got := CurrentLinkFor(argv0); got != want {
			t.Errorf("CurrentLinkFor(%q) = %q, want %q", argv0, got, want)
		}
	}
}

func TestCopyOfNamesTheImageIDDirectory(t *testing.T) {
	for bin, want := range map[string]string{
		"/usr/local/lib/grain/hostd/0123abcd/grain": "0123abcd",
		"/usr/local/lib/grain/hostd/current/grain":  "",
		"/usr/local/bin/grain":                      "",
		"/usr/local/lib/grain/hostd/0123abcd/other": "",
	} {
		if got := copyOf(bin); got != want {
			t.Errorf("copyOf(%q) = %q, want %q", bin, got, want)
		}
	}
}

// A hostd still running the old copy, whose current link the installer
// has just repointed: Hello names both, and the new copy's build.
func TestDescribeCopiesNamesTheCopyCurrentPointsAt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script copies")
	}
	copies := filepath.Join(t.TempDir(), "hostd")
	for _, id := range []string{"old111", "new222"} {
		if err := os.MkdirAll(filepath.Join(copies, id), 0o755); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\n[ \"$1 $2\" = 'hostd -build' ] && echo v9." + id + "\n"
		if err := os.WriteFile(filepath.Join(copies, id, "grain"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(copies, CurrentName)
	if err := os.Symlink("new222", link); err != nil {
		t.Fatal(err)
	}
	h := ThisHello("v9.old")
	describeCopies(context.Background(), &h, link)
	if h.Current != "new222" || h.CurrentBuild != "v9.new222" {
		t.Errorf("current = %q (%q), want new222 (v9.new222)", h.Current, h.CurrentBuild)
	}

	none := ThisHello("v9.old")
	describeCopies(context.Background(), &none, "")
	if none.Current != "" || none.CurrentBuild != "" {
		t.Errorf("a hostd not started through a link reports current %q (%q)", none.Current, none.CurrentBuild)
	}
}
