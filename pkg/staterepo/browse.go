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

package staterepo

// browse.go is the read side of this repository for somebody who has no
// shell on the controller.
//
// The repository is controller-local: there is no forge page for it and
// no remote a browser can be pointed at, so until now the only ways to
// read grain's exported configuration -- or the memory prose every run
// in a scope is told as fact before it starts -- were to be an agent in
// a sandbox that had it checked out, or to have a terminal on the host.
// Neither is available to the person running the deployment.
//
// What this is deliberately not is a file server for Repo.Dir(). That
// directory holds .git, it has held the encrypted secrets file on older
// builds (SecretsFile's own comment: a sandbox that may clone may read
// every object, and history does not forget), and it holds whatever else
// a deployment has put beside it. A browser over the directory is a
// browser that eventually serves one of those.
//
// So what is served is an allowlist of the paths grain itself writes --
// dump.go's own constants, above -- and the check is made on the path
// the filesystem resolves to rather than on the one that was asked for,
// so a symlink out of the tree is refused by the same test that refuses
// "..". Anything outside that set is refused with a sentence naming what
// is browsable, which is more use to a reader than a bare 404 and gives
// away nothing: the set is the same on every deployment.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// DefaultBrowseLimit is how much of one file a read returns when the
// caller does not say. Bounded rather than whole: tables/task.json on a
// deployment with a long history is megabytes, and a reader wants the
// beginning of it far more than a browser wants all of it. What is cut
// is reported as cut (FileContents.Truncated) rather than handed back as
// if it were the file.
const DefaultBrowseLimit = 256 << 10

// maxTreeEntries bounds a listing the same way, for the same reason:
// memory/documents on a busy deployment is not a small directory.
const maxTreeEntries = 5000

// browsableDirs and browsableFiles are exactly what a reader may see:
// the exported configuration and the sidecar text it points at, and
// nothing else in the working tree. Named from the layout constants
// rather than spelled again, so a directory that moves moves here too.
var (
	browsableDirs  = []string{MemoryDir, ReleaseNotesDir, ScriptsDir, SettingsDir, TablesDir, WorkflowsDir}
	browsableFiles = []string{ReadmeFile, SchemaVersionFile}
)

// ErrNotBrowsable is every refusal of a path outside that set -- "..",
// .git, the secrets file, a symlink leading out of the tree. One error
// for all of them on purpose: telling a caller which of its tricks was
// spotted is telling it something about the host, and the answer a
// reader needs is the same in every case.
var ErrNotBrowsable = errors.New("not part of grain's exported configuration")

// Browsable is the sentence a refusal ends with, so that whoever asked
// learns what they may ask for instead.
func Browsable() string {
	names := make([]string, 0, len(browsableDirs))
	for _, dir := range browsableDirs {
		names = append(names, dir+"/")
	}
	return fmt.Sprintf("this repository is browsable at %s and under %s",
		strings.Join(browsableFiles, ", "), strings.Join(names, ", "))
}

// Entry is one file or directory of the exported configuration, at a
// slash-separated path relative to the repository root.
type Entry struct {
	Path string
	Dir  bool
	// Size is the file's length in bytes, and 0 for a directory.
	Size int64
}

// Tree is the whole listing, with Truncated set when a repository holds
// more files than one listing carries -- said out loud for the same
// reason a cut file says so.
type Tree struct {
	Entries   []Entry
	Truncated bool
}

// BrowseTree lists the exported configuration under dir.
//
// Sorted by path, directories and files together, so the listing is
// stable between calls and a reader who comes back to it finds things
// where they were. A root a deployment has never written -- workflows/
// on one with no repo workflows -- is simply absent rather than an
// error: what is listed is what is there.
//
// Symlinks are never listed. grain writes none, so one in the tree is
// something else's, and a listing that names it is a listing inviting
// somebody to follow it.
func BrowseTree(dir string) (Tree, error) {
	var out Tree
	for _, name := range browsableFiles {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out.Entries = append(out.Entries, Entry{Path: name, Size: info.Size()})
	}
	for _, root := range browsableDirs {
		if err := walkBrowsable(dir, root, &out); err != nil {
			return Tree{}, err
		}
	}
	sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Path < out.Entries[j].Path })
	return out, nil
}

// walkBrowsable adds one allowed directory to the listing. WalkDir does
// not follow symlinks, and the entries it reports as symlinks are
// dropped here, so nothing outside dir can be reached by walking.
func walkBrowsable(dir, root string, out *Tree) error {
	base := filepath.Join(dir, root)
	if info, err := os.Lstat(base); err != nil || !info.IsDir() {
		return nil
	}
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory that vanished under an export running at the
			// same moment is not worth failing a listing over.
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		// A symlink -- to a file, to a directory, to anywhere -- is
		// neither walked nor listed.
		if !d.IsDir() && !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}
		if len(out.Entries) >= maxTreeEntries {
			out.Truncated = true
			return filepath.SkipAll
		}
		entry := Entry{Path: filepath.ToSlash(rel), Dir: d.IsDir()}
		if !d.IsDir() {
			info, infoErr := d.Info()
			if infoErr != nil {
				if errors.Is(infoErr, fs.ErrNotExist) {
					return nil
				}
				return infoErr
			}
			entry.Size = info.Size()
		}
		out.Entries = append(out.Entries, entry)
		return nil
	})
	if err != nil {
		return fmt.Errorf("staterepo: listing %s: %w", root, err)
	}
	return nil
}

// FileContents is one file as a reader sees it.
type FileContents struct {
	// Path is the resolved slash-separated path inside the repository,
	// which is what was read -- not necessarily the spelling asked for.
	Path string
	// Size is the file's whole length, whether or not all of it is here.
	Size int64
	// Text is the file's content, cut to the caller's limit. Empty for a
	// file that is not text (Binary), since a browser has nothing to do
	// with the bytes and a JSON body has no way to carry them.
	Text string
	// Truncated says Text is the beginning of the file rather than the
	// file. Said rather than left for a reader to notice, which is the
	// whole difference between a bound and a silent cut.
	Truncated bool
	Binary    bool
}

// BrowseFile reads one file of the exported configuration under dir.
//
// limit is how many bytes of it to return; DefaultBrowseLimit when not
// positive. A path outside the browsable set is refused with
// ErrNotBrowsable and a sentence naming what is browsable; one inside it
// that is not there is fs.ErrNotExist, which is a different fact with a
// different answer -- a table this deployment has never written is not a
// path anybody was refused.
func BrowseFile(dir, name string, limit int64) (FileContents, error) {
	if limit <= 0 {
		limit = DefaultBrowseLimit
	}
	real, rel, err := resolveBrowsable(dir, name)
	if err != nil {
		return FileContents{}, err
	}
	info, err := os.Stat(real)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return FileContents{}, fmt.Errorf("%s: %w", rel, fs.ErrNotExist)
		}
		return FileContents{}, fmt.Errorf("staterepo: reading %s: %w", rel, err)
	}
	if info.IsDir() {
		return FileContents{}, fmt.Errorf("%s is a directory, not a file", rel)
	}
	if !info.Mode().IsRegular() {
		return FileContents{}, fmt.Errorf("%q is %w; %s", name, ErrNotBrowsable, Browsable())
	}
	f, err := os.Open(real)
	if err != nil {
		return FileContents{}, fmt.Errorf("staterepo: reading %s: %w", rel, err)
	}
	defer f.Close()
	// One byte past the limit, which is what tells a file that exactly
	// fits from one that was cut.
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return FileContents{}, fmt.Errorf("staterepo: reading %s: %w", rel, err)
	}
	out := FileContents{Path: rel, Size: info.Size()}
	if int64(len(data)) > limit {
		out.Truncated, data = true, data[:limit]
	}
	// A file with a NUL in it, or one whose bytes are not UTF-8, is not
	// something this pane can render and not something grain writes --
	// it is reported as what it is rather than turned into replacement
	// characters that would read as corruption of the file itself.
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		out.Binary = true
		return out, nil
	}
	out.Text = string(data)
	return out, nil
}

// resolveBrowsable turns a requested path into the file on disk to read,
// or refuses it. It returns the real path and the repository-relative
// slash path that was actually resolved.
//
// The order matters. The request is checked lexically first, so that a
// path with ".." or an absolute path never reaches the filesystem at
// all; then it is resolved through symlinks, and the *result* is checked
// against the same allowlist. That second check is the one that counts:
// a symlink at tables/x.json pointing at ../.git/config resolves to a
// path still inside the repository, and only a test on where it landed
// catches it.
func resolveBrowsable(dir, name string) (real, rel string, err error) {
	refused := func() (string, string, error) {
		return "", "", fmt.Errorf("%q is %w; %s", name, ErrNotBrowsable, Browsable())
	}
	if !browsablePath(name) {
		return refused()
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", "", fmt.Errorf("staterepo: resolving %s: %w", dir, err)
	}
	full := filepath.Join(root, filepath.FromSlash(path.Clean(name)))
	real, err = filepath.EvalSymlinks(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", fmt.Errorf("%s: %w", path.Clean(name), fs.ErrNotExist)
		}
		return "", "", fmt.Errorf("staterepo: resolving %s: %w", name, err)
	}
	inside, err := filepath.Rel(root, real)
	if err != nil {
		return refused()
	}
	rel = filepath.ToSlash(inside)
	if !browsablePath(rel) {
		return refused()
	}
	return real, rel, nil
}

// BrowsablePath reports whether name -- a clean, slash-separated path
// relative to the repository root -- is part of the exported
// configuration a reader may see: the lexical half of BrowseFile's test,
// for a caller that serves the files itself (the sandbox's config://,
// pkg/namespace) and resolves symlinks against it on its own.
func BrowsablePath(name string) bool { return browsablePath(name) }

// browsablePath is the lexical half: a relative, clean, slash-separated
// path whose first component is one of the allowed roots -- and, for the
// two roots that are files rather than directories, exactly that file.
func browsablePath(name string) bool {
	if name == "" || strings.ContainsAny(name, "\x00\\") {
		return false
	}
	clean := path.Clean(name)
	if clean != name {
		// Any spelling cleaning would change -- "tables/../README.md",
		// "./tables/x", "tables//x", a trailing slash -- is refused
		// rather than quietly rewritten, so that what is checked here
		// and what is opened later cannot come apart.
		return false
	}
	if path.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	for _, name := range browsableFiles {
		if clean == name {
			return true
		}
	}
	// The directory roots themselves are allowed as well as what is
	// under them, so that asking for one is answered with "that is a
	// directory" rather than with a refusal reading as though it were
	// forbidden.
	first, _, _ := strings.Cut(clean, "/")
	for _, name := range browsableDirs {
		if first == name {
			return true
		}
	}
	return false
}
