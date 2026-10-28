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

package orchestrator_test

import (
	"context"
	"os"
	"testing"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A host sandbox's directory holds a checkout that may be private, so
// only this account may read it -- when it is made and when a rebuild
// makes it again (GSA1-18).
func TestHostSandboxDirectoryIsPrivate(t *testing.T) {
	ctx := context.Background()
	sb, err := orchestrator.NewHostSandboxes(t.TempDir()).Acquire(ctx, "1-1", orchestrator.Shape{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sb.Release(ctx) })
	root, err := sb.(interface{ Root() (string, error) }).Root()
	if err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		info, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("sandbox directory %s: mode %o, want no access for group or others", when, perm)
		}
	}
	check("as acquired")
	if err := sb.(interface{ Rebuild(context.Context) error }).Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	check("after a rebuild")
}
