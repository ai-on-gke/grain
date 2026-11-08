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
	"fmt"
	"strings"
	"testing"
)

// Each line a stdio capability server writes to its stderr is the
// daemon's log line, attributed to the server and without the server's
// own timestamp in front of it: the gcp server's "gcp: audit: ..." lines,
// the only record of each grant and token it mints, were discarded
// (2026-11-08 gcp token-mode walk).
func TestACapabilityServersStderrIsLoggedAsItsOwn(t *testing.T) {
	var lines []string
	w := capabilityServerStderr("https://grain.dev/capabilities/gcp").(capabilityServerLog)
	w.logf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)+"\n") }
	w.Write([]byte("2026/11/08 02:18:16 gcp: audit: attempt grain-3-1-0: permission set ext-deploy: granted access tokens\n"))
	w.Write([]byte("no stamp here\n"))
	want := "grain daemon: capability server https://grain.dev/capabilities/gcp: gcp: audit: attempt grain-3-1-0: permission set ext-deploy: granted access tokens\n" +
		"grain daemon: capability server https://grain.dev/capabilities/gcp: no stamp here\n"
	got := strings.Join(lines, "")
	if got != want {
		t.Errorf("logged:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "2026/11/08") {
		t.Error("the server's own timestamp was kept")
	}
}
