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

package selfdebug

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestSpecIsAGrantCapabilityNamedSelfDebug(t *testing.T) {
	spec := New().Spec()
	if CapabilityName != "self-debug" || spec.Name != CapabilityName {
		t.Fatalf("Name = %q, want %q", spec.Name, "self-debug")
	}
	if spec.Provision != model.ProvisionGrant {
		t.Fatalf("Provision = %q, want %q", spec.Provision, model.ProvisionGrant)
	}
}

func TestResolveAlwaysHonours(t *testing.T) {
	res, err := New().Resolve(context.Background(), model.CapabilityContext{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Refused {
		t.Fatalf("Resolve refused: %+v", res)
	}
}

func setupTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "nested.go"), []byte("package sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// requireUnprivileged skips a test whose whole subject is a permission
// this process might not be subject to: root ignores the mode bits these
// tests set, so asserting on them as root asserts nothing.
func requireUnprivileged(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root, which ignores the modes this test sets")
	}
}

// A tree that lists at its root and denies everything below it is what
// grain/task-160 and grain/task-250 both report, and what a presence
// check -- os.Stat, `test -d` -- cannot tell from a healthy one.
func TestCheckSourceReadableSeesATreeThatOnlyListsAtItsRoot(t *testing.T) {
	requireUnprivileged(t)
	dir := setupTree(t)
	if err := CheckSourceReadable(dir); err != nil {
		t.Fatalf("a readable checkout was reported as unreadable: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "sub"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "sub"), 0o755) })

	err := CheckSourceReadable(dir)
	if err == nil {
		t.Fatal("a checkout whose only readable path is its root was reported as fine")
	}
	if !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "lists at its root") {
		t.Errorf("the report does not say what is wrong: %v", err)
	}
}

// A deployment with no checkout at all is not this check's business:
// there is nothing for an operator to restage.
func TestCheckSourceReadableSaysNothingAboutADeploymentWithNoCheckout(t *testing.T) {
	if err := CheckSourceReadable(""); err != nil {
		t.Errorf("CheckSourceReadable(\"\") = %v, want nil", err)
	}
}
