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

package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the image's real entrypoint with an orphan-producing stand-in
// for grain. The installer shares the host PID namespace for the Top pane;
// Tini must adopt and reap descendants there as well as when it is PID 1.
func TestTheEntrypointReapsOrphansInBothPIDNamespaces(t *testing.T) {
	requireImage(t)
	probe, err := filepath.Abs("testdata/reaper.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(probe); err != nil {
		t.Fatal(err)
	}
	for _, namespace := range []string{"private", "host"} {
		t.Run(namespace, func(t *testing.T) {
			name := "grain-reaper-" + randomSuffix(t, 10)
			t.Cleanup(func() { tryDocker("rm", "-f", name) })
			args := []string{"run", "--rm", "--name", name,
				"--network", "none", "--user", "1234:1234",
				"--mount", "type=bind,src=" + probe + ",dst=/usr/local/bin/grain,readonly"}
			if namespace == "host" {
				args = append(args, "--pid", "host")
			}
			// No --entrypoint override: use precisely what the image ships.
			out, err := tryDocker(append(args, image)...)
			if err != nil || !strings.Contains(out, "orphan-reaped") {
				t.Fatalf("entrypoint did not adopt and reap an orphan: %v\n%s", err, out)
			}
		})
	}
}
