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

//go:build linux

package hostd

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestListenLeavesTheProcessUmaskAlone: listen used to bind its socket
// under syscall.Umask(0o177), which is the whole process's umask, so a
// directory anything else in the process made in that window came out
// 0600 -- untraversable, and the next mkdir inside it failed with
// "permission denied" (pkg/daemon's TestBuildCodexFrameworkDetached...
// beside an in-process hostd under -race). Directories made while
// sockets are bound keep their 0700, and the socket itself is 0600 with
// nothing staged left beside it.
func TestListenLeavesTheProcessUmaskAlone(t *testing.T) {
	base := t.TempDir()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var bad []string
	var mu sync.Mutex
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			dir, err := os.MkdirTemp(base, "d")
			if err != nil {
				t.Error(err)
				return
			}
			if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
				mu.Lock()
				bad = append(bad, dir)
				mu.Unlock()
			}
			os.Remove(dir)
		}
	}()
	sockDir := t.TempDir()
	for i := 0; i < 300; i++ {
		s := &Server{Socket: filepath.Join(sockDir, "hostd.sock")}
		ln, err := s.listen()
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("listen: %v", err)
		}
		info, err := os.Lstat(s.Socket)
		if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
			close(stop)
			wg.Wait()
			t.Fatalf("socket %s: %v, %v; want a 0600 socket", s.Socket, info, err)
		}
		ln.Close()
	}
	close(stop)
	wg.Wait()
	if len(bad) > 0 {
		t.Fatalf("%d directories made while hostd was binding did not come out 0700 (first %s)", len(bad), bad[0])
	}
	entries, err := os.ReadDir(sockDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "hostd.sock" {
			t.Errorf("listen left %s behind in %s", e.Name(), sockDir)
		}
	}
}
