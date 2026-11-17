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

package checks

import (
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/qa"
)

// NumberedLine pairs a 1-based line number with its raw content (without leading +/-).
type NumberedLine struct {
	Line int
	Text string
}

// DiffFile captures the structural metadata and changed lines for one file in a unified diff.
type DiffFile struct {
	OldPath      string
	NewPath      string
	IsNew        bool
	IsDeleted    bool
	IsRename     bool
	Hunks        []qa.LineRange
	AddedLines   []NumberedLine
	DeletedLines []NumberedLine
}

// EffectivePath returns NewPath when present, or OldPath for deleted files.
func (f DiffFile) EffectivePath() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}

// ParsedDiff holds all modified files and aggregate non-test churn from a unified git diff.
type ParsedDiff struct {
	Raw            string
	Files          []DiffFile
	NonTestAdded   int
	NonTestDeleted int
	TestAdded      int
}

// AddedLinesMap returns a map from file path to the lines added in the diff.
func (d ParsedDiff) AddedLinesMap() map[string][]string {
	out := make(map[string][]string, len(d.Files))
	for _, f := range d.Files {
		p := f.EffectivePath()
		if p == "" {
			continue
		}
		var lines []string
		for _, al := range f.AddedLines {
			lines = append(lines, al.Text)
		}
		if len(lines) > 0 {
			out[p] = lines
		}
	}
	return out
}

// AllPaths returns normalized unique file paths touched by the diff.
func (d ParsedDiff) AllPaths() []string {
	seen := make(map[string]bool, len(d.Files))
	out := make([]string, 0, len(d.Files))
	for _, f := range d.Files {
		p := f.EffectivePath()
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// HunkMap returns modified 1-based head line ranges keyed by normalized relative path.
func (d ParsedDiff) HunkMap() map[string][]qa.LineRange {
	out := make(map[string][]qa.LineRange, len(d.Files))
	for _, f := range d.Files {
		if f.NewPath != "" && len(f.Hunks) > 0 {
			out[f.NewPath] = append(out[f.NewPath], f.Hunks...)
		}
	}
	return out
}

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// ParseUnifiedDiff parses standard `git diff` output into per-file hunks and line deltas.
func ParseUnifiedDiff(raw string) ParsedDiff {
	out := ParsedDiff{Raw: raw}
	lines := strings.Split(raw, "\n")

	var cur *DiffFile
	inHunk := false
	oldLine := 0
	newLine := 0

	flush := func() {
		if cur == nil {
			return
		}
		p := cur.EffectivePath()
		if p != "" && !isDocOrTestPath(p) {
			out.NonTestAdded += len(cur.AddedLines)
			out.NonTestDeleted += len(cur.DeletedLines)
		}
		out.Files = append(out.Files, *cur)
		cur = nil
		inHunk = false
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			oldP, newP := parseDiffGitPaths(line)
			cur = &DiffFile{OldPath: oldP, NewPath: newP}
			continue
		}
		if cur == nil {
			continue
		}

		if strings.HasPrefix(line, "new file mode ") {
			cur.IsNew = true
			inHunk = false
			continue
		}
		if strings.HasPrefix(line, "deleted file mode ") {
			cur.IsDeleted = true
			cur.NewPath = ""
			inHunk = false
			continue
		}
		if strings.HasPrefix(line, "rename from ") {
			cur.IsRename = true
			cur.OldPath = cleanRelPath(strings.TrimPrefix(line, "rename from "))
			inHunk = false
			continue
		}
		if strings.HasPrefix(line, "rename to ") {
			cur.IsRename = true
			cur.NewPath = cleanRelPath(strings.TrimPrefix(line, "rename to "))
			inHunk = false
			continue
		}
		if strings.HasPrefix(line, "Binary files ") {
			inHunk = false
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			p := strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			if p == "/dev/null" {
				cur.IsNew = true
				cur.OldPath = ""
			} else if strings.HasPrefix(p, "a/") {
				cur.OldPath = cleanRelPath(p[2:])
			}
			inHunk = false
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			p := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if p == "/dev/null" {
				cur.IsDeleted = true
				cur.NewPath = ""
			} else if strings.HasPrefix(p, "b/") {
				cur.NewPath = cleanRelPath(p[2:])
			}
			inHunk = false
			continue
		}
		if m := hunkHeaderRe.FindStringSubmatch(line); m != nil {
			oStart, _ := strconv.Atoi(m[1])
			nStart, _ := strconv.Atoi(m[3])
			nLen := 1
			if m[4] != "" {
				nLen, _ = strconv.Atoi(m[4])
			}
			oldLine = oStart
			newLine = nStart
			endLine := nStart
			if nLen > 1 {
				endLine = nStart + nLen - 1
			}
			if nStart > 0 {
				cur.Hunks = append(cur.Hunks, qa.LineRange{Start: nStart, End: endLine})
			}
			inHunk = true
			continue
		}
		if !inHunk {
			continue
		}
		const maxDiffLinesPerFile = 20000
		switch {
		case strings.HasPrefix(line, "+"):
			if len(cur.AddedLines) < maxDiffLinesPerFile {
				cur.AddedLines = append(cur.AddedLines, NumberedLine{Line: newLine, Text: line[1:]})
			}
			newLine++
		case strings.HasPrefix(line, "-"):
			if len(cur.DeletedLines) < maxDiffLinesPerFile {
				cur.DeletedLines = append(cur.DeletedLines, NumberedLine{Line: oldLine, Text: line[1:]})
			}
			oldLine++
		case strings.HasPrefix(line, " "):
			oldLine++
			newLine++
		case strings.HasPrefix(line, `\`):
			// "\ No newline at end of file"
		}
	}
	flush()
	return out
}

func parseDiffGitPaths(header string) (string, string) {
	rest := strings.TrimSpace(strings.TrimPrefix(header, "diff --git "))
	// Standard format: a/<path> b/<path>
	if idx := strings.Index(rest, " b/"); idx > 0 && strings.HasPrefix(rest, "a/") {
		return cleanRelPath(rest[2:idx]), cleanRelPath(rest[idx+3:])
	}
	parts := strings.Fields(rest)
	if len(parts) == 2 {
		return cleanRelPath(strings.TrimPrefix(parts[0], "a/")), cleanRelPath(strings.TrimPrefix(parts[1], "b/"))
	}
	return "", ""
}

func cleanRelPath(p string) string {
	p = strings.TrimSpace(strings.Trim(p, `"`))
	if p == "" || strings.HasPrefix(p, "/") {
		return ""
	}
	c := strings.TrimPrefix(path.Clean(p), "./")
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return ""
	}
	return c
}

func isTestPath(p string) bool {
	return strings.HasSuffix(p, "_test.go") ||
		strings.HasSuffix(p, ".spec.ts") ||
		strings.HasSuffix(p, ".spec.js") ||
		strings.HasSuffix(p, ".test.ts") ||
		strings.HasSuffix(p, ".test.js")
}

func isDocOrTestPath(p string) bool {
	if isTestPath(p) {
		return true
	}
	lower := strings.ToLower(p)
	return strings.HasSuffix(lower, ".md") ||
		strings.HasPrefix(lower, "docs/") ||
		strings.HasPrefix(lower, "design/")
}
