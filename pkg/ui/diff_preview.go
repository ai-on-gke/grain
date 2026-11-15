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

package ui

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/redact"
)

const (
	// DefaultDiffPreviewHunks is how many hunks GET
	// /api/tasks/{id}/diff-preview answers when ?max= is omitted.
	DefaultDiffPreviewHunks = 6
	// MaxDiffPreviewHunks bounds ?max=: a preview beside a decision, not
	// the pull request's Files tab.
	MaxDiffPreviewHunks = 30
	// diffPreviewHunkLines bounds the lines one previewed hunk carries.
	diffPreviewHunkLines = 40
	// diffPreviewLineRunes bounds one previewed line (a minified file's
	// single line would otherwise be the whole preview).
	diffPreviewLineRunes = 240
)

// DiffPreview is GET /api/tasks/{id}/diff-preview: the first hunks of the
// change a task's branch brings to its base, file by file, for the
// Inbox's inspector to show beside the decision. It is the same
// comparison GET /api/repos/{owner}/{name}/compare draws (RepoComparer),
// cut short, with every line passed through the log redactor first.
type DiffPreview struct {
	Repo      string `json:"repo"`
	Base      string `json:"base"`
	Head      string `json:"head"`
	MergeBase string `json:"mergeBase,omitempty"`
	Merged    bool   `json:"merged,omitempty"`
	// Files are the files previewed, in diff order; a file past the
	// hunk budget is not listed.
	Files []DiffPreviewFile `json:"files"`
	// TotalFiles and TotalHunks are the whole change's counts, so a
	// reader can say "3 of 41 files".
	TotalFiles int `json:"totalFiles"`
	TotalHunks int `json:"totalHunks"`
	// Truncated is whether anything at all was left out: a file, a hunk,
	// or lines of one.
	Truncated bool `json:"truncated,omitempty"`
}

// DiffPreviewFile is one file of a DiffPreview.
type DiffPreviewFile struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	// Status is added, deleted, renamed, or modified.
	Status    string            `json:"status"`
	Binary    bool              `json:"binary,omitempty"`
	Additions int               `json:"additions"`
	Deletions int               `json:"deletions"`
	Hunks     []DiffPreviewHunk `json:"hunks"`
	// HunksOmitted is how many of this file's hunks the budget left out.
	HunksOmitted int `json:"hunksOmitted,omitempty"`
}

// DiffPreviewHunk is one hunk: its @@ header and its lines.
type DiffPreviewHunk struct {
	Header string            `json:"header"`
	Lines  []DiffPreviewLine `json:"lines"`
	// LinesOmitted is how many of the hunk's lines were cut.
	LinesOmitted int `json:"linesOmitted,omitempty"`
}

// DiffPreviewLine is one line of a hunk. Kind is add, del, or ctx.
type DiffPreviewLine struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

func (s *Server) handleTaskDiffPreview(w http.ResponseWriter, r *http.Request) {
	maxHunks := DefaultDiffPreviewHunks
	if raw := strings.TrimSpace(r.URL.Query().Get("max")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeClientError(w, validationErrorf("max must be a positive whole number of hunks"))
			return
		}
		maxHunks = min(n, MaxDiffPreviewHunks)
	}
	id := r.PathValue("id")
	task, err := s.tasks.Store.GetTask(r.Context(), id)
	if err != nil {
		writeClientError(w, err)
		return
	}
	if task == nil {
		writeClientError(w, &NotFoundError{ID: id})
		return
	}
	if task.Target == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("task %s has no repository branch to preview", id))
		return
	}
	if s.tasks.Config.RepoComparer == nil {
		writeError(w, http.StatusNotFound, errors.New("repository comparison is not available on this UI server"))
		return
	}
	repo, head := *task.Target, task.BranchName()
	comparison, err := s.tasks.Config.RepoComparer.Compare(r.Context(), repo, task.Base, head)
	if github.IsNotFound(err) {
		// Most often a task that has pushed nothing yet: its branch does
		// not exist. Said in grain's words, as handleCompareBranches does.
		writeError(w, http.StatusNotFound, fmt.Errorf("task %s's branch %s has nothing to compare in %s yet", id, head, repo))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	var redactor *redact.Redactor
	if s.tasks.Config.LogRedactor != nil {
		if redactor, err = s.tasks.Config.LogRedactor(); err != nil {
			// Refuse rather than show a diff the deployment's own secret
			// values were not checked against.
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("checking the diff for secrets: %w", err))
			return
		}
	}
	diff, _ := redactor.Text(comparison.Diff)
	preview := buildDiffPreview(diff, maxHunks)
	preview.Repo, preview.Base, preview.Head = repo.String(), comparison.Base, comparison.Head
	preview.MergeBase, preview.Merged = comparison.MergeBase, comparison.Merged
	writeJSON(w, http.StatusOK, preview)
}

// buildDiffPreview parses a unified git diff and keeps its first maxHunks
// hunks, each cut to diffPreviewHunkLines lines of at most
// diffPreviewLineRunes runes. Counts cover the whole diff.
func buildDiffPreview(diff string, maxHunks int) DiffPreview {
	out := DiffPreview{Files: []DiffPreviewFile{}}
	var file *DiffPreviewFile
	var hunk *DiffPreviewHunk
	inHeader := false
	kept := 0
	keepFile := func() {
		switch {
		case file == nil:
		case len(file.Hunks) > 0, file.HunksOmitted == 0 && kept < maxHunks:
			out.Files = append(out.Files, *file)
		default:
			out.Truncated = true
		}
	}
	closeHunk := func() {
		if hunk != nil {
			file.Hunks = append(file.Hunks, *hunk)
		}
		hunk = nil
	}
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			closeHunk()
			keepFile()
			out.TotalFiles++
			oldPath, newPath := diffGitPaths(line)
			file = &DiffPreviewFile{Path: newPath, OldPath: oldPath, Status: "modified", Hunks: []DiffPreviewHunk{}}
			inHeader = true
			continue
		}
		if file == nil {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			closeHunk()
			inHeader = false
			out.TotalHunks++
			if kept >= maxHunks {
				file.HunksOmitted++
				out.Truncated = true
				continue
			}
			kept++
			hunk = &DiffPreviewHunk{Header: clampRunes(line, diffPreviewLineRunes), Lines: []DiffPreviewLine{}}
			continue
		}
		if inHeader {
			switch {
			case strings.HasPrefix(line, "new file mode"):
				file.Status = "added"
			case strings.HasPrefix(line, "deleted file mode"):
				file.Status = "deleted"
			case strings.HasPrefix(line, "rename from "):
				file.Status = "renamed"
				file.OldPath = strings.TrimPrefix(line, "rename from ")
			case strings.HasPrefix(line, "rename to "):
				file.Path = strings.TrimPrefix(line, "rename to ")
			case strings.HasPrefix(line, "Binary files "), line == "GIT binary patch":
				file.Binary = true
			}
			continue
		}
		var kind string
		switch {
		case strings.HasPrefix(line, "+"):
			kind = "add"
			file.Additions++
		case strings.HasPrefix(line, "-"):
			kind = "del"
			file.Deletions++
		case strings.HasPrefix(line, " "):
			kind = "ctx"
		default:
			continue // "\ No newline at end of file", and the trailing "".
		}
		if hunk == nil {
			continue // a hunk past the budget: counted, not kept.
		}
		if len(hunk.Lines) >= diffPreviewHunkLines {
			hunk.LinesOmitted++
			out.Truncated = true
			continue
		}
		hunk.Lines = append(hunk.Lines, DiffPreviewLine{Kind: kind, Text: clampRunes(line[1:], diffPreviewLineRunes)})
	}
	closeHunk()
	keepFile()
	for i := range out.Files {
		if out.Files[i].OldPath == out.Files[i].Path {
			out.Files[i].OldPath = ""
		}
	}
	return out
}

// diffGitPaths reads the two paths of a "diff --git a/x b/y" line. A path
// with a space in it is ambiguous there; the rename and ---/+++ lines
// are not, and a rename's own lines correct it.
func diffGitPaths(line string) (oldPath, newPath string) {
	rest := strings.TrimPrefix(line, "diff --git ")
	if i := strings.Index(rest, " b/"); strings.HasPrefix(rest, "a/") && i >= 0 {
		return rest[2:i], rest[i+3:]
	}
	return rest, rest
}

// clampRunes cuts s to at most n runes, marking a cut with "…".
func clampRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
