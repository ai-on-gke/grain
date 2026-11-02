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

package granule

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContainerOOMKillsReadsMemoryEventsUnderEitherDriver(t *testing.T) {
	root := t.TempDir()
	was := cgroupRoot
	cgroupRoot = root
	t.Cleanup(func() { cgroupRoot = was })

	write := func(dir, events string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "memory.events"), []byte(events), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("system.slice/docker-abc.scope", "low 0\nhigh 0\nmax 912\noom 3\noom_kill 1\noom_group_kill 0\n")
	write("docker/def", "low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\n")

	if n, err := ContainerOOMKills("abc"); err != nil || n != 1 {
		t.Errorf("systemd-driver cgroup: ContainerOOMKills = %d, %v; want 1", n, err)
	}
	if n, err := ContainerOOMKills("def"); err != nil || n != 0 {
		t.Errorf("cgroupfs-driver cgroup: ContainerOOMKills = %d, %v; want 0", n, err)
	}
	if _, err := ContainerOOMKills("gone"); err == nil {
		t.Errorf("a container with no cgroup read as no kills rather than unreadable")
	}
	if _, err := ContainerOOMKills("../etc"); err == nil {
		t.Errorf("a path-like container id was read")
	}
}
