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

package pkgcache

import (
	"strings"
	"testing"
)

// TestOnlyTheGoCacheWriterCommandCarriesAToken: the GOCACHEPROG every
// sandbox is given names the cache and nothing else, and the writer's
// carries its job's token as basic auth, which the cache reads.
func TestOnlyTheGoCacheWriterCommandCarriesAToken(t *testing.T) {
	cmds, err := SandboxCommands("http://10.0.0.1:4100")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cmds {
		for _, arg := range c.Argv {
			if strings.Contains(arg, "@") {
				t.Errorf("every sandbox's %s carries user information: %q", c.What, arg)
			}
		}
	}

	w, err := GoCacheWriterCommand("http://10.0.0.1:4100", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	want := "GOCACHEPROG=" + GoCacheProgPath + " http://job:s3cret@10.0.0.1:4100"
	if got := w.Argv[len(w.Argv)-1]; got != want {
		t.Errorf("writer command = %q, want %q", got, want)
	}
	if tok := parseAuthHeaderToken("Basic " + "am9iOnMzY3JldA=="); tok != "s3cret" {
		t.Errorf("the cache reads job:s3cret as token %q", tok)
	}
	if _, err := GoCacheWriterCommand("http://10.0.0.1:4100", ""); err == nil {
		t.Error("a writer command with no token was built")
	}
}
