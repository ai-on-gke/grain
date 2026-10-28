package workflows

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// The files a job reads, and what knowing them buys.
//
// # The problem
//
// A job's key was the commit (jobFingerprint), so a commit that touched
// only `docs/` produced a fresh key for every job in the file and every
// job ran again. The cache worked perfectly for asking about the *same*
// commit twice and did nothing at all across commits -- which is the only
// direction a branch ever moves in. On grain's own suite that is eight
// jobs and sixteen to fifty minutes of wall clock, paid in full for a
// commit that changed a comment.
//
// # The declaration
//
// A job may say which files it depends on, in GitHub's own two keys and
// GitHub's own glob syntax:
//
//	jobs:
//	  terraform:
//	    paths:
//	      - terraform/**
//	      - "!terraform/**/*.md"
//	  go:
//	    paths-ignore:
//	      - docs/**
//
// Both keys take GitHub's `!` negation and GitHub's evaluation order: a
// pattern that matches decides, and a later one that also matches decides
// again, so "everything under terraform except its markdown" and
// "everything except docs, but docs/claude-surface.md after all" are both
// sayable. `paths-ignore:` is `paths: ["**", "!…"]` said the short way,
// and declaring both in one job is refused, exactly as GitHub refuses it.
//
// GitHub spells those on a workflow's trigger, where they decide whether
// the workflow runs at all. Here they are on the *job*, which is the unit
// that gets a sandbox and the unit that is cached, and they are used for
// something strictly stronger than deciding whether to run: they decide
// what the job's cache key is made of.
//
// A job that declares `paths:` is keyed on the *content of the files that
// match*, rather than on the commit. So the same `go` job across two
// commits that touched only `ui/` is a cache *hit* -- the job does not
// run, and its earlier result stands. That is better than a filter in
// three ways a filter cannot be made to match: it needs no diff and so no
// base commit to diff against; it works across branches, so a job answered
// on main is answered on every branch cut from it; and it works across a
// rebase or a merge of the base branch that changed nothing the job reads,
// which is exactly the case that costs a whole extra suite today.
//
// It is also why there is no separate "not affected, skipped" conclusion
// here, and why the merge gate needed no change to accept this. A job that
// nothing it reads has touched comes back from the cache as the success it
// was, recorded as a success against this commit, with JobRun.Cached set
// so a reader can see where the green came from. "Skipped" remains what it
// has always been -- a job whose `needs` did not pass, which judged
// nothing -- and the gate goes on refusing it (pkg/mcp/grain_ci.go).
//
// # What defends the declaration
//
// The declaration is load-bearing: a job that reads a file it did not
// declare gets a stale green, which is the one failure mode a cache must
// not have (fingerprintVersion's own comment). Four things defend it:
//
//   - Declaring nothing is the default and means the whole tree. A job
//     says nothing, it keys on the commit, and it behaves exactly as every
//     job did before this existed. Nothing is opted in by accident.
//   - A declaration that matches no file in the tree keys on the commit
//     too (Tree.Match reports no match). A `paths:` with a typo in it
//     would otherwise produce the same empty content for every commit
//     forever -- one key, one entry, a permanent green -- and a typo is a
//     far likelier reading of "this job depends on no file at all" than
//     the alternative.
//   - Everything about the job that is not a file is still in the key: its
//     whole declaration, steps, `sandbox:` shape, `env:` and timeouts
//     included (jobFingerprint.Job), the workflow-level `env:` it
//     inherits, the capabilities and release secrets the run carries, and
//     the environment grain hands the steps (runEnv) apart from the two
//     values below. Editing a job's `paths:` is editing its declaration,
//     so it misses that job's own entries and no other job's.
//   - What is dropped from the key is dropped deliberately and is named in
//     one place, contentEnv: GRAIN_CI_COMMIT and GRAIN_CI_REF. Keeping
//     them would make a content key a commit key again, since they change
//     on every push. Declaring `paths:` is therefore also a promise that
//     the job's answer does not depend on which commit or which branch it
//     is run against -- true of a build or a test suite, false of a job
//     that stamps a version into something it publishes, which is what
//     `cache: false` is for.
//
// What is *not* in the key, here as before this change, is the sandbox
// image a job runs in. A rebuilt guest image is a different machine under
// the same key, for a job that declares paths and for one that does not
// alike; fingerprintVersion is the lever for that, and it is unchanged by
// this.

// Tree is what a commit contains, as much of it as keying a job on the
// files it reads needs: every file, and a hash of its content.
//
// A value rather than an fs.FS because nothing here wants to *read* the
// tree -- the files are already hashed by whatever produced this (git's
// own blob ids, out of `git ls-tree`), and a fingerprint over those ids is
// a fingerprint over the content without this package opening a file.
//
// A nil *Tree is a source that cannot say what the commit holds, which
// every Source but the one that clones is. It is not an error and not an
// empty tree: it means every job keys on the commit, which is what they
// all did before this existed.
type Tree struct {
	// Files is every file of the commit, in any order. Match sorts what
	// it returns rather than trusting this, so a source is free to hand
	// over whatever order its own tooling printed.
	Files []TreeFile
}

// TreeFile is one file of a Tree.
type TreeFile struct {
	// Path is slash-separated and relative to the repository root, with
	// no leading "./" -- "pkg/ci/paths.go", the spelling `git ls-tree -r`
	// prints and the spelling a `paths:` pattern is written in.
	Path string
	// Blob is a hash of the file's content: git's blob id where the tree
	// came from a clone. What it is derived from does not matter to
	// anything here -- only that two files with the same content have the
	// same Blob and two with different content do not.
	//
	// The file's *mode* is part of it where the source can supply one
	// (see ciTree in cmd/grain), because a script losing its execute bit
	// changes what a job does without changing a byte of its content.
	Blob string
}

// Match is the files of this tree a job's declaration selects, sorted by
// path.
//
// ok is false for a declaration this cannot honour -- no declaration at
// all, no tree, or a declaration that matched nothing -- and a caller that
// gets it keys on the commit instead. See this file's doc comment on why
// an empty match is treated as a declaration nobody can trust rather than
// as an empty content key.
func (t *Tree) Match(paths, ignore []string) (files []TreeFile, ok bool) {
	patterns := pathPatterns(paths, ignore)
	if t == nil || len(patterns) == 0 {
		return nil, false
	}
	for _, f := range t.Files {
		if !selects(patterns, f.Path) {
			continue
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, false
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, true
}

// pathPatterns is a job's two keys as the one ordered list everything
// downstream works from. `paths-ignore:` is the short way of saying
// "everything, then not these", which is what it expands to here; the
// parse refuses a job that declares both, so at most one of these
// branches ever has anything in it.
func pathPatterns(paths, ignore []string) []string {
	if len(paths) > 0 {
		return paths
	}
	if len(ignore) == 0 {
		return nil
	}
	patterns := make([]string, 0, len(ignore)+1)
	patterns = append(patterns, "**")
	for _, pattern := range ignore {
		// A `!` in paths-ignore is a re-inclusion, since the list is
		// already inverted: "not docs, except this one file of it".
		if strings.HasPrefix(pattern, "!") {
			patterns = append(patterns, strings.TrimPrefix(pattern, "!"))
			continue
		}
		patterns = append(patterns, "!"+pattern)
	}
	return patterns
}

// selects is GitHub's own evaluation of an ordered filter list: a pattern
// that matches decides, and a later one that also matches decides again,
// so a `!` after a `**` takes files back out and a plain pattern after
// that puts some of them back.
//
// Order rather than "any positive and no negative", because order is what
// makes the two-sided case sayable at all -- and because it is what GitHub
// does, which is the rule this format follows wherever GitHub has one.
func selects(patterns []string, file string) bool {
	selected := false
	for _, pattern := range patterns {
		if negated, ok := strings.CutPrefix(pattern, "!"); ok {
			if matchPath(negated, file) {
				selected = false
			}
			continue
		}
		if matchPath(pattern, file) {
			selected = true
		}
	}
	return selected
}

// matchPath is GitHub's `paths:` globbing over one file: matchGlob, plus
// the one convenience GitHub does not have and everybody writes anyway.
//
// A pattern with no wildcard in it that names a directory matches
// everything under that directory, so `paths: [docs]` and `paths:
// [docs/]` mean what `docs/**` means. Erring towards matching *more* is
// the safe direction by construction: a pattern that matches more files
// puts more content in the key, which costs a cache miss and can never
// produce a stale hit. The reverse -- quietly matching less than the
// writer meant -- is exactly the failure this whole mechanism must not
// have.
func matchPath(pattern, file string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if matchGlob(pattern, file) {
		return true
	}
	if strings.ContainsAny(pattern, "*?[") {
		return false
	}
	return strings.HasPrefix(file, strings.TrimSuffix(pattern, "/")+"/")
}

// matchGlob is the globbing a branch filter and a path filter share, cut
// down to the two wildcards anybody writes: `*`, which matches within one
// path segment, and `**`, which matches across them ("grain/**" against
// "grain/task-185", "pkg/**/*.go" against "pkg/ci/paths.go").
// path.Match gives the first for free and refuses the second, so `**` is
// expanded here before handing the rest over.
//
// Not regexp, and not full fnmatch: these patterns are read far more often
// than they are written, and the two wildcards above cover every filter
// this repository's own workflows have ever wanted.
func matchGlob(pattern, s string) bool {
	if strings.Contains(pattern, "**") {
		// Turn the pattern into a regexp rather than into a path.Match
		// call, since `**` is exactly what path.Match cannot express.
		var b strings.Builder
		b.WriteString("^")
		for i := 0; i < len(pattern); i++ {
			switch {
			case strings.HasPrefix(pattern[i:], "**/"):
				// Zero or more whole directories, as on GitHub, where
				// "**/README.md" matches the root README.md and
				// "docs/**/*.md" matches docs/README.md. Requiring a
				// directory here would match *fewer* files than the
				// writer meant, and a `paths:` key missing files is a
				// stale cache hit.
				b.WriteString("(?:.*/)?")
				i += 2
			case strings.HasPrefix(pattern[i:], "**"):
				b.WriteString(".*")
				i++
			case pattern[i] == '*':
				b.WriteString("[^/]*")
			case pattern[i] == '?':
				// What path.Match makes of it below, so a pattern means
				// the same with or without a `**` elsewhere in it.
				b.WriteString("[^/]")
			default:
				b.WriteString(regexp.QuoteMeta(string(pattern[i])))
			}
		}
		b.WriteString("$")
		re, err := regexp.Compile(b.String())
		if err != nil {
			return false
		}
		return re.MatchString(s)
	}
	ok, err := path.Match(pattern, s)
	return err == nil && ok
}

// validPathFilter refuses what a `paths:` entry may not be. It is
// deliberately strict about the two shapes that would silently match
// nothing -- an absolute path and one that climbs out of the tree -- since
// a declaration that matches nothing is a job that keys on the commit and
// so a filter that quietly did not work.
func validPathFilter(pattern string) error {
	pattern = strings.TrimPrefix(strings.TrimSpace(pattern), "!")
	switch {
	case pattern == "":
		return fmt.Errorf("an empty path filter selects nothing -- leave the key out instead")
	case strings.HasPrefix(pattern, "/"):
		return fmt.Errorf("%q is absolute, and a path filter is relative to the repository root", pattern)
	case pattern == ".." || strings.HasPrefix(pattern, "../") ||
		strings.Contains(pattern, "/../") || strings.HasSuffix(pattern, "/.."):
		return fmt.Errorf("%q climbs out of the repository, and there is nothing out there to depend on", pattern)
	}
	return nil
}
