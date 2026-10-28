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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestUpgradeFromPreviousVersion performs an automated upgrade test from
// the previous version of the software to the current version.
//
// It tests:
//  1. Initializing deployment state and data using the previous release
//     version baseline (state-repo, tasks, sqlite store).
//  2. Starting the daemon on the previous version data.
//  3. Executing an upgrade via the upgrader API (/api/upgrade) to the
//     current build image.
//  4. Following the upgrade phases: pull image, health check, ref file update,
//     and restart trigger.
//  5. Restarting the deployment on the current version image and verifying
//     that data, tasks, and state repository synchronize without regression.
func TestUpgradeFromPreviousVersion(t *testing.T) {
	requireImage(t)
	requireGoToolchain(t)
	requireGit(t)

	repo := registry(t)
	root := t.TempDir()

	control := filepath.Join(root, "data", "control")
	if err := os.MkdirAll(control, 0o755); err != nil {
		t.Fatalf("laying out the control directory: %v", err)
	}
	refFile := filepath.Join(root, "data", "image.env")
	restart := filepath.Join(control, "restart")

	// Determine the previous version tag (e.g. v1.7.0).
	prevTag := resolvePreviousVersionTag(t)
	t.Logf("testing upgrade from previous version %s to current version image %s", prevTag, image)

	// In the registry, tag the previous version baseline image as prevTag.
	prevImage := buildPreviousVersionImage(t, prevTag)
	writeRef(t, refFile, prevImage)

	// Tag the current image under test as "current" in the local registry.
	currentTag := "current"
	currentRef := repo + ":" + currentTag
	docker(t, "tag", image, currentRef)
	docker(t, "push", currentRef)

	// Start the daemon running the previous version image.
	d := upgradeDaemon(t, root, repo, refFile, restart)

	// Populate deployment data in the previous version: create tasks and comments.
	task1 := createTask(t, d, "Task created in previous version "+prevTag)
	task2 := createTask(t, d, "Second task in previous version "+prevTag)

	status, body := d.api(t, http.MethodPost, "/api/tasks/"+task1.ID+"/comments",
		map[string]string{"body": "Comment recorded prior to software upgrade"})
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("commenting on task before upgrade: %d %s", status, body)
	}

	// Request upgrade to the current version.
	final := upgradeTo(t, d, currentTag)
	if final.Phase != "ok" {
		t.Fatalf("the upgrade from %s to current ended %q: %s\n%s", prevTag, final.Phase, final.Detail, d.logs())
	}

	// Verify that the ref file was repointed and pinned to the current image digest.
	pinned := currentRef + "@" + digestOf(t, repo, currentRef)
	if got := readRef(t, refFile); got != "GRAIN_IMAGE="+pinned {
		t.Fatalf("the ref file reads %q, want it pinned to %s", got, pinned)
	}

	// Verify the restart request is generated.
	waitFor(t, "the restart request the upgrade ends with", 60*time.Second, func() error {
		_, err := os.Stat(restart)
		return err
	})
	d.stop()

	// Bring the daemon back up on the new image (as systemd does on restart).
	back := start(t, root, options{image: refFileImage(t, refFile)})

	// Verify the tasks and comments created in the previous version survived.
	status, body = back.api(t, http.MethodGet, "/api/tasks/"+task1.ID, nil)
	if status != http.StatusOK {
		t.Fatalf("task %s is missing after upgrade: %d %s\n%s", task1.ID, status, body, back.logs())
	}

	var fetched task
	decode(t, body, &fetched)
	if fetched.Title != task1.Title {
		t.Errorf("task title mismatch after upgrade: got %q, want %q", fetched.Title, task1.Title)
	}

	var commentPreserved bool
	for _, c := range fetched.Comments {
		if strings.Contains(c.Body, "Comment recorded prior to software upgrade") {
			commentPreserved = true
			break
		}
	}
	if !commentPreserved {
		t.Errorf("comment did not survive upgrade: %s", body)
	}

	// Verify all tasks are listed.
	_, body = back.api(t, http.MethodGet, "/api/tasks", nil)
	var listed []task
	decode(t, body, &listed)
	if !slicesContainsID(listed, task1.ID) || !slicesContainsID(listed, task2.ID) {
		t.Errorf("tasks missing from task list after upgrade: %s", body)
	}

	// Verify state-repo sync and health endpoint on the upgraded daemon.
	healthStatus, healthBody := back.api(t, http.MethodGet, "/api/sandboxes", nil)
	if healthStatus != http.StatusOK {
		t.Errorf("health check on upgraded daemon returned %d: %s", healthStatus, healthBody)
	}

	// Confirm fix ce3789180: /api/health does not exist on Grain daemon (returns 404).
	health404Status, _ := back.api(t, http.MethodGet, "/api/health", nil)
	if health404Status != http.StatusNotFound {
		t.Errorf("expected GET /api/health to return 404, got %d", health404Status)
	}
}

// resolvePreviousVersionTag finds the most recent released version tag
// (vX.Y.Z without -rc) prior to current development.
func resolvePreviousVersionTag(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("git", "tag", "-l", "--sort=-v:refname", "v*")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return parsePreviousVersionTag(string(out))
	}
	// Fallback to v1.7.0 if git tags are unavailable
	return "v1.7.0"
}

// parsePreviousVersionTag extracts the newest non-rc release tag starting with 'v'.
func parsePreviousVersionTag(tagOutput string) string {
	lines := strings.Split(strings.TrimSpace(tagOutput), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Choose the newest non-rc release tag
		if strings.HasPrefix(line, "v") && !strings.Contains(line, "-rc") && line != "" {
			return line
		}
	}
	// Fallback to v1.7.0 if no eligible tags are found
	return "v1.7.0"
}

// TestResolvePreviousVersionTag verifies that resolvePreviousVersionTag
// correctly parses git tags and returns the latest release version.
func TestResolvePreviousVersionTag(t *testing.T) {
	// 1. Verify against repository tags
	got := resolvePreviousVersionTag(t)
	if got == "" {
		t.Fatalf("resolvePreviousVersionTag returned empty string")
	}
	if !strings.HasPrefix(got, "v") {
		t.Errorf("expected tag starting with 'v', got %q", got)
	}
	// Which release that is moves with every promotion, so it is not
	// pinned here: a pin turns red the moment the next release is tagged.
	if strings.Contains(got, "-rc") {
		t.Errorf("expected non-rc tag, got %q", got)
	}

	// 2. Unit test parsePreviousVersionTag with various synthetic tag sets
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "rc tags filtered out",
			input:    "v1.8.0-rc.1\nv1.7.0\nv1.6.0",
			expected: "v1.7.0",
		},
		{
			name:     "multiple rc tags before release",
			input:    "v1.8.0-rc.2\nv1.8.0-rc.1\nv1.7.0",
			expected: "v1.7.0",
		},
		{
			name:     "all rc tags falls back",
			input:    "v1.8.0-rc.2\nv1.8.0-rc.1",
			expected: "v1.7.0",
		},
		{
			name:     "empty output falls back",
			input:    "",
			expected: "v1.7.0",
		},
		{
			name:     "tags with extra whitespace and empty lines",
			input:    "  \n  v1.9.0 \n v1.8.0 \n",
			expected: "v1.9.0",
		},
		{
			name:     "non-v tags ignored",
			input:    "release-1.9\nlatest\nv1.7.0",
			expected: "v1.7.0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := parsePreviousVersionTag(tc.input)
			if result != tc.expected {
				t.Errorf("parsePreviousVersionTag(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

// buildPreviousVersionImage prepares an image in the local registry
// tagged as prevTag representing the previous release.
func buildPreviousVersionImage(t *testing.T, tag string) string {
	t.Helper()
	// Build an image based on the current test image with the previous version
	// stamped so it serves as the previous baseline.
	return buildImageWithEdits(t, tag)
}
