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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A "v2" path segment, as its own string or inside one.
var (
	quotedV2 = regexp.MustCompile(`(["'])v2(["'])`)
	pathV2   = regexp.MustCompile(`v2/[A-Za-z_]`)
)

// TestNoSourceFileStillRefersToTheV2Subdirectory is the generalized form
// of TestEveryBuildPathAgreesTheModuleRootIsTheRepositoryRoot.
//
// v1's removal moved the Go tree from v2/ to the repository root, and a
// `v2` path segment left anywhere behind is a file-not-found at run time,
// never a compile error. Three separate ones shipped before this check
// existed -- in the Dockerfile, in pkg/orchestrator's real-VM suite, and
// in the installer suite -- and each was invisible to every other test:
// the first is not read by Go, the second gates on /dev/kvm, the third on
// GRAIN_INSTALLER_E2E. A suite that skips is indistinguishable from one
// that passes.
//
// So this reads the tree directly rather than trusting any of them to
// run. Two spellings are legitimate and excluded: the Docker registry
// HTTP API (`/v2/`, which the container suite polls) and Go module paths
// whose major version really is 2 (`gax-go/v2`).
func TestNoSourceFileStillRefersToTheV2Subdirectory(t *testing.T) {
	root := repoRoot(t)
	suffixes := map[string]bool{
		".go": true, ".py": true, ".sh": true,
		".js": true, ".jsx": true, ".mjs": true,
	}
	skip := map[string]bool{
		".git": true, "node_modules": true, "third_party": true, "static": true,
	}

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if skip[name] {
				return fs.SkipDir
			}
			return nil
		}
		if !suffixes[filepath.Ext(name)] {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for number, line := range strings.Split(string(content), "\n") {
			stripped := strings.TrimSpace(line)
			// Prose keeps its historical references; only code is checked.
			if strings.HasPrefix(stripped, "#") || strings.HasPrefix(stripped, "//") ||
				strings.HasPrefix(stripped, "*") {
				continue
			}
			// The registry API endpoint, not a directory.
			if strings.Contains(line, "/v2/") || strings.Contains(line, "127.0.0.1") || strings.Contains(line, "localhost") {
				continue
			}
			if quotedV2.MatchString(line) || pathV2.MatchString(line) {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel, number+1, stripped))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the checkout: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("v2 path segments survive the promotion:\n%s", strings.Join(offenders, "\n"))
	}
}
