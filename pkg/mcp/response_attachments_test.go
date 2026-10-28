package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResponseAttachmentsStripsDirectoryComponents(t *testing.T) {
	t.Parallel()
	content := base64.StdEncoding.EncodeToString([]byte("x"))
	for _, tc := range []struct{ name, want string }{
		{"plain.txt", "plain.txt"},
		{"dir/file.txt", "file.txt"},
		{`dir\file.txt`, "file.txt"},
		{`C:\Users\me\file.txt`, "file.txt"},
		{"../../x", "x"},
		{"..", "attachment"},
		{"", "attachment"},
	} {
		got, err := ResponseAttachments(context.Background(), map[string]any{"attachments": []any{
			map[string]any{"filename": tc.name, "content": content},
		}}, AttachmentFiles{})
		if err != nil {
			t.Fatalf("ResponseAttachments(%q): %v", tc.name, err)
		}
		if len(got) != 1 || got[0].Filename != tc.want {
			t.Errorf("ResponseAttachments(%q) filename = %+v, want %q", tc.name, got, tc.want)
		}
	}
}

func TestResponseAttachmentsTolerantKeepsWhatDecodesAndNamesWhatDoesNot(t *testing.T) {
	args := map[string]any{"attachments": []any{
		map[string]any{"filename": "good.png", "content_type": "image/png", "content": "cG5n"},
		map[string]any{"filename": "shots/bad.png", "content": "not base64!"},
		"not an object",
	}}
	if _, err := ResponseAttachments(context.Background(), args, AttachmentFiles{}); err == nil {
		t.Fatal("ResponseAttachments accepted an invalid attachment")
	}
	got, dropped := ResponseAttachmentsTolerant(args, nil)
	if len(got) != 1 || got[0].Filename != "good.png" || string(got[0].Content) != "png" {
		t.Fatalf("kept = %+v, want good.png alone", got)
	}
	if len(dropped) != 2 || dropped[0].Filename != "bad.png" ||
		!strings.HasPrefix(dropped[0].Reason, "not valid base64") ||
		dropped[1].Reason != "attachment 3 must be an object" {
		t.Fatalf("dropped = %+v", dropped)
	}
}

// pngBytes is a PNG signature followed by n bytes of filler: enough for
// http.DetectContentType to call it image/png, and as big as a test needs
// it to be -- past the ~16 KB a tool answer is cut at being the point.
func pngBytes(n int) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0xab}, n)...)
}

// stagedCalls records what AttachmentFiles.Stage was handed.
type stagedCalls map[string]ResponseAttachment

func (s stagedCalls) stage(_ context.Context, path string, a ResponseAttachment) error {
	s[path] = a
	return nil
}

// hostFiles is the AttachmentFiles a host-directory sandbox at root
// reads through, staging into staged.
func hostFiles(root string, staged stagedCalls) AttachmentFiles {
	return AttachmentFiles{Read: SandboxFileReaderOf(NewSandboxTools(root)), Stage: staged.stage}
}

func TestResponseAttachmentsReadsAFileByPathAndStagesItUnderThatPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sheet := pngBytes(150 << 10)
	if err := os.MkdirAll(filepath.Join(root, "screenshots", "after"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "screenshots", "after", "contact-sheet.png"), sheet, 0o644); err != nil {
		t.Fatal(err)
	}
	staged := stagedCalls{}
	got, err := ResponseAttachments(context.Background(), map[string]any{"attachments": []any{
		map[string]any{"path": "screenshots/after/contact-sheet.png"},
		map[string]any{"filename": "notes.txt", "content": base64.StdEncoding.EncodeToString([]byte("plain words"))},
	}}, hostFiles(root, staged))
	if err != nil {
		t.Fatalf("ResponseAttachments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d attachments, want 2: %+v", len(got), got)
	}
	if got[0].Filename != "contact-sheet.png" || got[0].ContentType != "image/png" || !bytes.Equal(got[0].Content, sheet) {
		t.Errorf("by path = %q %q (%d bytes), want contact-sheet.png image/png with the file's %d bytes",
			got[0].Filename, got[0].ContentType, len(got[0].Content), len(sheet))
	}
	if got[1].Filename != "notes.txt" || got[1].ContentType != "text/plain; charset=utf-8" || string(got[1].Content) != "plain words" {
		t.Errorf("by content = %+v", got[1])
	}
	held, ok := staged["screenshots/after/contact-sheet.png"]
	if len(staged) != 1 || !ok || !bytes.Equal(held.Content, sheet) {
		t.Fatalf("staged = %v, want the contact sheet alone, under the path the call named", staged)
	}

	// And the finish path, which has the call's arguments back and
	// nothing else, finds it there again.
	kept, dropped := ResponseAttachmentsTolerant(map[string]any{"attachments": []any{
		map[string]any{"path": "screenshots/after/contact-sheet.png", "filename": "after.png"},
		map[string]any{"path": "screenshots/before/contact-sheet.png"},
	}}, staged)
	if len(kept) != 1 || kept[0].Filename != "after.png" || !bytes.Equal(kept[0].Content, sheet) {
		t.Errorf("kept = %+v, want the staged sheet as after.png", kept)
	}
	if len(dropped) != 1 || dropped[0].Filename != "contact-sheet.png" ||
		!strings.Contains(dropped[0].Reason, "was not read from screenshots/before/contact-sheet.png") {
		t.Errorf("dropped = %+v, want the one never staged named", dropped)
	}
}

func TestResponseAttachmentsWorksOutAContentTypeNobodyGave(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "shot"), pngBytes(16), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResponseAttachments(context.Background(), map[string]any{"attachments": []any{
		map[string]any{"path": "shot"},
		map[string]any{"path": filepath.Join(root, "shot"), "filename": "given.bin", "content_type": "image/x-mine"},
		map[string]any{"filename": "c", "content": base64.StdEncoding.EncodeToString(pngBytes(16))},
	}}, hostFiles(root, stagedCalls{}))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"image/png", "image/x-mine", "image/png"} {
		if got[i].ContentType != want {
			t.Errorf("attachment %d content type = %q, want %q", i+1, got[i].ContentType, want)
		}
	}
}

func TestResponseAttachmentsRefusesAPathItCannotAttach(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, pngBytes(16), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "both.png"), pngBytes(16), 0o644); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(root, "big.bin")
	if err := os.WriteFile(big, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, maxResponseAttachmentSize+1); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		item  map[string]any
		files AttachmentFiles
		want  string
	}{
		{"missing", map[string]any{"path": "screenshots/nope.png"}, AttachmentFiles{}, `"nope.png" could not be read from screenshots/nope.png`},
		{"outside the workspace", map[string]any{"path": outside}, AttachmentFiles{}, "outside this sandbox's workspace"},
		{"through a symlink out of it", map[string]any{"path": "link.png"}, AttachmentFiles{}, "outside this sandbox's workspace"},
		{"a directory", map[string]any{"path": "."}, AttachmentFiles{}, "not a regular file"},
		{"too big", map[string]any{"path": "big.bin"}, AttachmentFiles{}, `"big.bin" is over the 10485760 byte limit`},
		{"both path and content", map[string]any{"path": "both.png", "content": "cG5n"}, AttachmentFiles{}, `"both.png" gives both path and content`},
		{"no way to read it", map[string]any{"path": "both.png"}, AttachmentFiles{Read: nil}, "cannot be attached by path on this run"},
		{"neither", map[string]any{"filename": "x"}, AttachmentFiles{}, `"x" needs a path or base64 content`},
	} {
		files := tc.files
		if tc.name != "no way to read it" {
			files = hostFiles(root, stagedCalls{})
		}
		staged := stagedCalls{}
		if files.Read != nil {
			files.Stage = staged.stage
		}
		_, err := ResponseAttachments(context.Background(), map[string]any{"attachments": []any{tc.item}}, files)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to say %q", tc.name, err, tc.want)
		}
		if len(staged) != 0 {
			t.Errorf("%s: staged %v for an attachment that was refused", tc.name, staged)
		}
	}
}

func TestSSHSandboxReadsAnAttachmentWholeFromTheWorkspace(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	workspace := filepath.Join(home, "work")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	// Every byte value, and a run of them far past what a tool answer
	// carries: nothing between the guest and here may treat it as text.
	var sheet []byte
	for i := 0; i < 200<<10; i++ {
		sheet = append(sheet, byte(i))
	}
	if err := os.WriteFile(filepath.Join(workspace, "sheet.png"), sheet, 0o644); err != nil {
		t.Fatal(err)
	}
	read := SandboxFileReaderOf(NewSSHSandboxTools(localExecRunner{dir: home}, workspace))
	if read == nil {
		t.Fatal("NewSSHSandboxTools' read_file has no ReadBytes")
	}
	got, err := read(context.Background(), "sheet.png", maxResponseAttachmentSize)
	if err != nil || !bytes.Equal(got, sheet) {
		t.Fatalf("read(sheet.png) = %d bytes, %v; want the file's %d bytes exactly", len(got), err, len(sheet))
	}
	if _, err := read(context.Background(), "sheet.png", 1024); err == nil || !strings.Contains(err.Error(), "over the 1024 byte limit") {
		t.Errorf("read past the limit = %v, want it refused", err)
	}
	if _, err := read(context.Background(), "missing.png", 1024); err == nil || !strings.Contains(err.Error(), "No such file") {
		t.Errorf("read of a missing file = %v, want head's own words", err)
	}
}
