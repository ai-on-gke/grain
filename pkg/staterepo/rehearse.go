package staterepo

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// FileChange is one file of the exported configuration that a write
// changes: what a deployment whose configuration lives in an external
// config repository tells the caller to commit there instead of making
// the write itself (pkg/ui's checkSettingsWritable).
//
// It is computed by exporting the configuration on both sides of the
// write (Rehearse), so it is in the exact format the export writes and
// the sync reads back -- there is no second serializer to drift from it.
type FileChange struct {
	// Path is slash-separated and relative to the repository's root:
	// "settings/schedules.yml", "tables/grain_config.json".
	Path string `json:"path"`
	// Op is ChangeCreate, ChangeModify or ChangeDelete.
	Op string `json:"op"`
	// Patch is a unified diff of the file with one hunk, applicable with
	// `git apply` (or ApplyChanges) to the file as the export wrote it.
	Patch string `json:"patch"`
	// Fragment is the text the change writes in: the lines Patch adds,
	// without their markers. Empty for a deletion.
	Fragment string `json:"fragment,omitempty"`
}

// The kinds of FileChange.
const (
	ChangeCreate = "create"
	ChangeModify = "modify"
	ChangeDelete = "delete"
)

// Rehearse runs write on tx between two exports of the configuration tx
// can see, and returns the files that differ. It does not commit or roll
// back tx: that is its caller's, who is expected to roll it back.
//
// The write's own error comes back as writeErr, apart from err: a write
// that refused its own request (a validation failure, a missing row) is
// that refusal and nothing else, while err is this function failing to
// describe a write that would have succeeded.
func Rehearse(ctx context.Context, tx *sql.Tx, write func(*sql.Tx) error) (changes []FileChange, writeErr, err error) {
	root, err := os.MkdirTemp("", "grain-rehearsal-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(root)
	before, after := filepath.Join(root, "before"), filepath.Join(root, "after")
	if err := exportConfig(ctx, tx, before); err != nil {
		return nil, nil, err
	}
	if err := write(tx); err != nil {
		return nil, err, nil
	}
	if err := exportConfig(ctx, tx, after); err != nil {
		return nil, nil, err
	}
	changes, err = DiffTrees(before, after)
	return changes, nil, err
}

// DiffTrees compares two exported configurations file by file.
func DiffTrees(before, after string) ([]FileChange, error) {
	old, err := treeFiles(before)
	if err != nil {
		return nil, err
	}
	cur, err := treeFiles(after)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(old)+len(cur))
	for p := range old {
		paths = append(paths, p)
	}
	for p := range cur {
		if _, ok := old[p]; !ok {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	var out []FileChange
	for _, p := range paths {
		a, inOld := old[p]
		b, inCur := cur[p]
		if inOld && inCur && bytes.Equal(a, b) {
			continue
		}
		op := ChangeModify
		switch {
		case !inOld:
			op = ChangeCreate
		case !inCur:
			op = ChangeDelete
		}
		out = append(out, filePatch(p, op, string(a), string(b)))
	}
	return out, nil
}

func treeFiles(dir string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == dir {
				return filepath.SkipDir
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	return out, err
}

// splitLines splits s into lines that keep their "\n"; the last one has
// none when s does not end in one.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// patchContext is how many unchanged lines the hunk carries on each side,
// the number `git diff` uses by default.
const patchContext = 3

// filePatch writes the one-hunk patch taking a to b. One hunk is enough
// for what it is used for -- a single write changes one row, or one
// entry of a settings document, and the files are ordered so that row
// sits in one place -- and where a write did change two places apart,
// the hunk spans both and is still exact.
func filePatch(path, op, a, b string) FileChange {
	al, bl := splitLines(a), splitLines(b)
	prefix := 0
	for prefix < len(al) && prefix < len(bl) && al[prefix] == bl[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(al)-prefix && suffix < len(bl)-prefix && al[len(al)-1-suffix] == bl[len(bl)-1-suffix] {
		suffix++
	}
	start := max(0, prefix-patchContext)
	trail := min(suffix, patchContext)
	oldEnd, newEnd := len(al)-suffix, len(bl)-suffix

	var p strings.Builder
	oldName, newName := "a/"+path, "b/"+path
	fmt.Fprintf(&p, "diff --git a/%s b/%s\n", path, path)
	switch op {
	case ChangeCreate:
		p.WriteString("new file mode 100644\n")
		oldName = "/dev/null"
	case ChangeDelete:
		p.WriteString("deleted file mode 100644\n")
		newName = "/dev/null"
	}
	fmt.Fprintf(&p, "--- %s\n+++ %s\n", oldName, newName)
	oldLen := oldEnd + trail - start
	newLen := newEnd + trail - start
	if oldLen == 0 && newLen == 0 {
		return FileChange{Path: path, Op: op, Patch: p.String()}
	}
	fmt.Fprintf(&p, "@@ -%s +%s @@\n", hunkRange(start, oldLen), hunkRange(start, newLen))
	emit := func(mark byte, line string) {
		p.WriteByte(mark)
		p.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			p.WriteString("\n\\ No newline at end of file\n")
		}
	}
	for _, l := range al[start:prefix] {
		emit(' ', l)
	}
	for _, l := range al[prefix:oldEnd] {
		emit('-', l)
	}
	var frag strings.Builder
	for _, l := range bl[prefix:newEnd] {
		emit('+', l)
		frag.WriteString(l)
	}
	for _, l := range al[oldEnd : oldEnd+trail] {
		emit(' ', l)
	}
	return FileChange{Path: path, Op: op, Patch: p.String(), Fragment: frag.String()}
}

// hunkRange is one side of a hunk header: "start,len", where a side with
// no lines names the line it would follow, as unified diff has it.
func hunkRange(start, n int) string {
	if n == 0 {
		return strconv.Itoa(start) + ",0"
	}
	return strconv.Itoa(start+1) + "," + strconv.Itoa(n)
}

// ApplyChanges applies changes to the configuration exported at dir,
// refusing any whose patch does not match the file it is applied to.
// It is the reading of FileChange.Patch that `git apply` would make, here
// so that a test (and anything else holding a checkout) can follow the
// instruction without a git binary.
func ApplyChanges(dir string, changes []FileChange) error {
	for _, c := range changes {
		if err := applyChange(dir, c); err != nil {
			return fmt.Errorf("staterepo: applying the change to %s: %w", c.Path, err)
		}
	}
	return nil
}

func applyChange(dir string, c FileChange) error {
	path := filepath.Clean(filepath.FromSlash(c.Path))
	if path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return fmt.Errorf("the path is not inside the repository")
	}
	full := filepath.Join(dir, path)
	var current string
	if c.Op != ChangeCreate {
		data, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		current = string(data)
	}
	start, oldSeg, newSeg, err := parseHunk(c.Patch)
	if err != nil {
		return err
	}
	lines := splitLines(current)
	if start+len(oldSeg) > len(lines) {
		return fmt.Errorf("the patch does not match the file")
	}
	for i, l := range oldSeg {
		if lines[start+i] != l {
			return fmt.Errorf("the patch does not match the file at line %d", start+i+1)
		}
	}
	if c.Op == ChangeDelete {
		if start != 0 || len(oldSeg) != len(lines) {
			return fmt.Errorf("the patch deletes a file it does not describe whole")
		}
		return os.Remove(full)
	}
	next := append(append(append([]string{}, lines[:start]...), newSeg...), lines[start+len(oldSeg):]...)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(strings.Join(next, "")), 0o644)
}

// parseHunk reads the one hunk filePatch writes: the index of its first
// line in the old file, and the lines it expects there and puts there.
func parseHunk(patch string) (start int, oldSeg, newSeg []string, err error) {
	lines := splitLines(patch)
	i := 0
	for i < len(lines) && !strings.HasPrefix(lines[i], "@@ ") {
		i++
	}
	if i == len(lines) {
		return 0, nil, nil, nil
	}
	var oldStart, oldLen, newStart, newLen int
	if _, err := fmt.Sscanf(lines[i], "@@ -%d,%d +%d,%d @@", &oldStart, &oldLen, &newStart, &newLen); err != nil {
		return 0, nil, nil, fmt.Errorf("unreadable hunk header %q", strings.TrimSpace(lines[i]))
	}
	start = oldStart - 1
	if oldLen == 0 {
		start = oldStart
	}
	var last byte
	for _, l := range lines[i+1:] {
		if l == "" {
			continue
		}
		body := l[1:]
		switch l[0] {
		case ' ':
			oldSeg, newSeg = append(oldSeg, body), append(newSeg, body)
		case '-':
			oldSeg = append(oldSeg, body)
		case '+':
			newSeg = append(newSeg, body)
		case '\\':
			trim := func(seg []string) {
				if n := len(seg); n > 0 {
					seg[n-1] = strings.TrimSuffix(seg[n-1], "\n")
				}
			}
			if last == ' ' || last == '-' {
				trim(oldSeg)
			}
			if last == ' ' || last == '+' {
				trim(newSeg)
			}
			continue
		default:
			return 0, nil, nil, fmt.Errorf("unreadable patch line %q", strings.TrimSpace(l))
		}
		last = l[0]
	}
	if len(oldSeg) != oldLen || len(newSeg) != newLen {
		return 0, nil, nil, fmt.Errorf("the hunk's lines do not add up to its header")
	}
	return start, oldSeg, newSeg, nil
}
