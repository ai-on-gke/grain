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

package staterepo_test

// What a reader with no shell on the controller may see of this
// repository, and -- the half that matters -- what they may not. Every
// refusal below is one way a file browser over a directory that holds
// .git, and that has held an encrypted secrets file, would have handed
// back something nobody asked it to serve.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// browsableDir is a working tree laid out the way an export leaves one,
// with the things that live beside it that must never be served.
func browsableDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# grain state\n")
	write("schema-version", "16\n")
	write("tables/grain_config.json", `[{"id":1}]`)
	write("tables/memory.json", `[{"file":"memory/repos/bwsalmon/grain.md"}]`)
	write("memory/deployment.md", "what grain has learned\n")
	write("memory/repos/bwsalmon/grain.md", "about this repository\n")
	write("scripts/guest-customization.sh", "#!/bin/sh\n")
	// And the neighbours.
	write(".git/config", "[remote \"origin\"]\n\turl = http://127.0.0.1/grain/config\n")
	write(".gitignore", staterepo.SecretsFile+"\n")
	write(staterepo.SecretsFile, "ciphertext")
	write("secrets/secrets.key", "AGE-SECRET-KEY-NOBODY-SHOULD-SEE")
	return dir
}

func TestBrowseTreeListsTheExportedContentAndNothingBeside(t *testing.T) {
	t.Parallel()
	tree, err := staterepo.BrowseTree(browsableDir(t))
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if tree.Truncated {
		t.Error("a listing of eight files reported itself truncated")
	}
	paths := map[string]staterepo.Entry{}
	for _, e := range tree.Entries {
		paths[e.Path] = e
	}
	for _, want := range []string{
		"README.md", "schema-version",
		"tables", "tables/grain_config.json", "tables/memory.json",
		"memory", "memory/deployment.md", "memory/repos/bwsalmon/grain.md",
		"scripts", "scripts/guest-customization.sh",
	} {
		if _, ok := paths[want]; !ok {
			t.Errorf("%s is not in the listing: %v", want, tree.Entries)
		}
	}
	for path := range paths {
		for _, forbidden := range []string{".git", ".gitignore", staterepo.SecretsFile, "secrets"} {
			if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
				t.Errorf("the listing offers %s", path)
			}
		}
	}
	if entry := paths["tables/grain_config.json"]; entry.Dir || entry.Size != int64(len(`[{"id":1}]`)) {
		t.Errorf("tables/grain_config.json listed as %+v", entry)
	}
	if entry := paths["memory"]; !entry.Dir {
		t.Errorf("memory listed as %+v, not as a directory", entry)
	}
	// Sorted, so that a reader who comes back to the pane finds things
	// where they were.
	for i := 1; i < len(tree.Entries); i++ {
		if tree.Entries[i-1].Path >= tree.Entries[i].Path {
			t.Fatalf("listing is not sorted: %s before %s", tree.Entries[i-1].Path, tree.Entries[i].Path)
		}
	}
}

func TestBrowseTreeOnADirectoryWithNoExportYet(t *testing.T) {
	t.Parallel()
	tree, err := staterepo.BrowseTree(t.TempDir())
	if err != nil {
		t.Fatalf("listing an empty working tree: %v", err)
	}
	if len(tree.Entries) != 0 {
		t.Errorf("an empty working tree listed %v", tree.Entries)
	}
}

func TestBrowseFileReadsTheExportedContent(t *testing.T) {
	t.Parallel()
	dir := browsableDir(t)
	for _, name := range []string{"README.md", "schema-version", "tables/memory.json", "memory/repos/bwsalmon/grain.md"} {
		got, err := staterepo.BrowseFile(dir, name, 0)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if got.Path != name || got.Truncated || got.Binary || got.Text == "" {
			t.Errorf("reading %s gave %+v", name, got)
		}
		if got.Size != int64(len(got.Text)) {
			t.Errorf("reading %s reported size %d for %d bytes", name, got.Size, len(got.Text))
		}
	}
}

// The refusals. Each names something a browser over the working tree
// would have served, and the refusal has to say what is browsable
// instead -- a bare 404 leaves a reader unable to tell a path they may
// not have from one that is simply not there.
func TestBrowseFileRefusesWhatIsNotExportedConfiguration(t *testing.T) {
	t.Parallel()
	dir := browsableDir(t)
	for _, name := range []string{
		".git/config",
		".git",
		".gitignore",
		staterepo.SecretsFile,
		"secrets/secrets.key",
		"../secrets/secrets.key",
		"..",
		"tables/../.git/config",
		"tables/../../etc/passwd",
		"/etc/passwd",
		"/home/debian/work/tables/memory.json",
		"",
		".",
		"./tables/memory.json",
		"tables//memory.json",
		"tables/",
		`tables\..\.git\config`,
		"tables/memory.json\x00",
	} {
		got, err := staterepo.BrowseFile(dir, name, 0)
		if !errors.Is(err, staterepo.ErrNotBrowsable) {
			t.Errorf("reading %q: got %+v, %v -- wanted a refusal", name, got, err)
			continue
		}
		if !strings.Contains(err.Error(), "README.md") || !strings.Contains(err.Error(), "tables/") {
			t.Errorf("refusing %q does not say what is browsable: %v", name, err)
		}
	}
}

func TestBrowseFileRefusesASymlinkOutOfTheTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	t.Parallel()
	dir := browsableDir(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.md")
	if err := os.WriteFile(outside, []byte("not this deployment's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		// Out of the repository altogether.
		"tables/elsewhere.json": outside,
		// And back into it, at something not browsable -- the case only
		// a check on where the path landed can catch, since this one has
		// no ".." in the request at all.
		"tables/git-config.json":    filepath.Join(dir, ".git", "config"),
		"memory/secrets.md":         filepath.Join(dir, staterepo.SecretsFile),
		"memory/repos/elsewhere.md": filepath.Join(dir, "secrets", "secrets.key"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	for name := range links {
		got, err := staterepo.BrowseFile(dir, name, 0)
		if !errors.Is(err, staterepo.ErrNotBrowsable) {
			t.Errorf("reading the symlink %s: got %+v, %v -- wanted a refusal", name, got, err)
		}
	}
	// Nor are they listed, which is the other half: a listing that names
	// a symlink is a listing inviting somebody to follow it.
	tree, err := staterepo.BrowseTree(dir)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	for _, entry := range tree.Entries {
		if _, linked := links[entry.Path]; linked {
			t.Errorf("the listing offers the symlink %s", entry.Path)
		}
	}
}

// A whole directory of symlinks does not turn into a way in either: the
// walk neither follows nor lists one, whatever it points at.
func TestBrowseTreeDoesNotFollowASymlinkedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	t.Parallel()
	dir := browsableDir(t)
	if err := os.Symlink(filepath.Join(dir, ".git"), filepath.Join(dir, "tables", "history")); err != nil {
		t.Fatal(err)
	}
	tree, err := staterepo.BrowseTree(dir)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	for _, entry := range tree.Entries {
		if strings.HasPrefix(entry.Path, "tables/history") {
			t.Errorf("the listing followed a symlinked directory: %s", entry.Path)
		}
	}
}

// A path inside the browsable set that is not there is a different fact
// from one that was refused, and says so: a table this deployment has
// never written is nobody's security question.
func TestBrowseFileSaysWhenAnAllowedPathIsSimplyNotThere(t *testing.T) {
	t.Parallel()
	_, err := staterepo.BrowseFile(browsableDir(t), "tables/no_such_table.json", 0)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("reading a missing table: %v", err)
	}
	if errors.Is(err, staterepo.ErrNotBrowsable) {
		t.Errorf("a missing table was reported as forbidden: %v", err)
	}
}

func TestBrowseFileSaysADirectoryIsADirectory(t *testing.T) {
	t.Parallel()
	_, err := staterepo.BrowseFile(browsableDir(t), "memory/repos", 0)
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("reading a directory: %v", err)
	}
}

// The bound, and the thing that makes a bound honest: a cut file says it
// was cut rather than coming back looking like the whole of it.
func TestBrowseFileTruncatesAndSaysSo(t *testing.T) {
	t.Parallel()
	dir := browsableDir(t)
	body := strings.Repeat("a", 4096)
	if err := os.WriteFile(filepath.Join(dir, "tables", "task.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := staterepo.BrowseFile(dir, "tables/task.json", 100)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if !got.Truncated {
		t.Error("a file cut at 100 bytes did not say it was truncated")
	}
	if len(got.Text) != 100 {
		t.Errorf("returned %d bytes for a limit of 100", len(got.Text))
	}
	if got.Size != 4096 {
		t.Errorf("reported size %d, not the whole file's 4096", got.Size)
	}
	// A file that exactly fits is not truncated, which is what the read
	// of one byte past the limit is for.
	exact, err := staterepo.BrowseFile(dir, "tables/task.json", 4096)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if exact.Truncated || len(exact.Text) != 4096 {
		t.Errorf("a file that exactly fits came back %+v", exact)
	}
}

func TestBrowseFileReportsBinaryRatherThanMangleIt(t *testing.T) {
	t.Parallel()
	dir := browsableDir(t)
	if err := os.WriteFile(filepath.Join(dir, "scripts", "blob"), []byte{0x00, 0xff, 0xfe}, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := staterepo.BrowseFile(dir, "scripts/blob", 0)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if !got.Binary || got.Text != "" {
		t.Errorf("a file of bytes came back as %+v", got)
	}
}
