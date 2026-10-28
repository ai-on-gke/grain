package mcp

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// shrinkMaxToolResultBytes caps results at something a test can produce
// in a line or two, rather than generating 16 KB to prove the cap works.
func shrinkMaxToolResultBytes(t *testing.T, n int) {
	t.Helper()
	old := maxToolResultBytes.Swap(int64(n))
	t.Cleanup(func() { maxToolResultBytes.Store(old) })
}

func TestElideMiddleKeepsHeadAndTailAndSaysWhatWentMissing(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 400; i++ {
		b.WriteString("line of output\n")
	}
	text := b.String()

	// The limit bounds the output kept, not the notice grain adds on
	// top of it -- which is a few hundred bytes, once.
	got := elideMiddle(text, 1000, elisionAdviceCommandOutput)
	if len(got) > 1000+len(elisionAdviceCommandOutput)+200 {
		t.Errorf("elided text is %d bytes, want near the 1000-byte limit", len(got))
	}
	if !strings.HasPrefix(got, "line of output\n") {
		t.Error("elided text lost the head of the output")
	}
	if !strings.HasSuffix(got, "line of output\n") {
		t.Error("elided text lost the tail of the output")
	}
	if !strings.Contains(got, "elided here by grain, not by the command") {
		t.Errorf("elided text does not say grain cut it:\n%s", got)
	}
	if !strings.Contains(got, "read that back with read_file's") {
		t.Errorf("elided text does not say how to get the rest:\n%s", got)
	}
	// Neither half may end mid-line: a half-written line reads as
	// output the command produced rather than as a cut.
	head, _, _ := strings.Cut(got, "[grain]")
	if !strings.HasSuffix(head, "\n") {
		t.Errorf("head does not end at a line boundary: %q", head[max(0, len(head)-40):])
	}
}

func TestElideMiddleLeavesTextThatFitsAlone(t *testing.T) {
	text := "small enough\n"
	if got := elideMiddle(text, 1000, elisionAdviceCommandOutput); got != text {
		t.Errorf("got %q, want it untouched", got)
	}
}

// TestElideMiddleHandlesOneOverlongLine is the degenerate case: minified
// JSON, a base64 blob, a `curl` of a whole page -- there is no line
// boundary to snap a tail to, and the notice still has to arrive rather
// than the whole line coming through uncut.
func TestElideMiddleHandlesOneOverlongLine(t *testing.T) {
	text := strings.Repeat("x", 5000)
	got := elideMiddle(text, 1000, elisionAdviceCommandOutput)
	if len(got) > 1400 {
		t.Errorf("one long line elided to %d bytes, want near the 1000-byte limit", len(got))
	}
	if !strings.Contains(got, "[grain]") {
		t.Errorf("no elision notice for a single overlong line:\n%s", got)
	}
}

func TestSplitResultBudgetLendsTheUnusedHalfToTheOtherStream(t *testing.T) {
	// Both fit: neither is touched.
	if a, b := splitResultBudget(10, 20, 100); a != 10 || b != 20 {
		t.Errorf("got (%d, %d), want (10, 20)", a, b)
	}
	// A small stderr next to a huge stdout leaves stdout the rest,
	// rather than halving stdout and wasting the other half.
	if a, b := splitResultBudget(1000, 10, 100); a != 90 || b != 10 {
		t.Errorf("got (%d, %d), want (90, 10)", a, b)
	}
	if a, b := splitResultBudget(10, 1000, 100); a != 10 || b != 90 {
		t.Errorf("got (%d, %d), want (10, 90)", a, b)
	}
	// Both oversized: half each, and never more than the budget
	// between them.
	a, b := splitResultBudget(1000, 1000, 100)
	if a+b != 100 {
		t.Errorf("got (%d, %d), want them to sum to the 100-byte budget", a, b)
	}
}

// callOversizedTool registers one tool answering with text and calls it,
// returning what a client actually receives -- the whole of handleCall,
// which is where the backstop lives.
func callOversizedTool(t *testing.T, tool Tool) string {
	t.Helper()
	registry := NewRegistry()
	registry.Register(tool)
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })
	res, err := client.CallTool(context.Background(), tool.Name, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	return res.Text()
}

// TestEveryToolsAnswerIsBounded is the backstop: a tool that bounds
// nothing -- one grain did not write, one whose upstream had a bad day --
// still cannot spend a run's whole context in one turn.
func TestEveryToolsAnswerIsBounded(t *testing.T) {
	shrinkMaxToolResultBytes(t, 2000)

	text := callOversizedTool(t, Tool{
		Name: "unbounded",
		Handler: func(context.Context, map[string]any) Result {
			return Result{Text: strings.Repeat("a line of it\n", 4000)}
		},
	})
	if len(text) > maxResultTextBytes()+1000 {
		t.Errorf("unbounded tool answered %d bytes against a %d-byte backstop", len(text), maxResultTextBytes())
	}
	if !strings.Contains(text, "elided here by grain") {
		t.Errorf("the cut answer does not say it was cut:\n%s", text)
	}
	if !strings.Contains(text, "say in what you report") {
		t.Errorf("a tool with no advice of its own should get the generic advice:\n%s", text)
	}
}

// TestBoundedAnswerCarriesTheToolsOwnAdvice: the notice is only worth
// anything if it names the next call, and only the tool knows what that
// is.
func TestBoundedAnswerCarriesTheToolsOwnAdvice(t *testing.T) {
	shrinkMaxToolResultBytes(t, 2000)

	text := callOversizedTool(t, Tool{
		Name:          "chatty",
		ElisionAdvice: "ask for one page of it at a time",
		Handler: func(context.Context, map[string]any) Result {
			return Result{Text: strings.Repeat("a line of it\n", 4000)}
		},
	})
	if !strings.Contains(text, "ask for one page of it at a time") {
		t.Errorf("the cut answer does not carry the tool's own advice:\n%s", text)
	}
}

// TestAToolThatBoundsItselfIsNotCutTwice: run_command and read_file cap
// their own output and then write an exit line, a notice or two of their
// own around it. The backstop sits far enough above their cap for that
// framing, so an answer they have already bounded arrives with one
// notice in it rather than two.
func TestAToolThatBoundsItselfIsNotCutTwice(t *testing.T) {
	shrinkMaxToolResultBytes(t, 2000)

	client := newTestClient(t, t.TempDir())
	res, err := client.CallTool(context.Background(), "run_command", map[string]any{
		"command": "printf 'x%.0s' $(seq 1 100000)",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(res.Text(), "elided here by grain"); got != 1 {
		t.Errorf("got %d elision notices, want 1:\n%s", got, res.Text())
	}
}

// TestRunCommandCapsItsOutput is docs/agent-ergonomics.md's finding 4 for
// run_command: one `go test ./...` on a failing suite, one `git log -p`
// or one over-broad `grep -r` used to come back whole and spend a large
// part of the run's context in a single turn.
func TestRunCommandCapsItsOutput(t *testing.T) {
	if _, err := exec.LookPath("seq"); err != nil {
		t.Skip("seq not installed")
	}
	shrinkMaxToolResultBytes(t, 2000)

	client := newTestClient(t, t.TempDir())
	res, err := client.CallTool(context.Background(), "run_command", map[string]any{
		"command": "seq 1 20000",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Text()
	if len(text) > 3000 {
		t.Errorf("run_command answer is %d bytes against a 2000-byte cap", len(text))
	}
	if !strings.Contains(text, "elided here by grain") {
		t.Errorf("capped run_command answer does not say it was cut:\n%s", text)
	}
	// The head and the tail are what a run reads: the command it ran
	// and how it ended.
	if !strings.Contains(text, "\n1\n") {
		t.Error("capped run_command answer lost the head of stdout")
	}
	if !strings.Contains(text, "\n20000\n") {
		t.Error("capped run_command answer lost the tail of stdout")
	}
}

// TestReadFileCapsAWholeFileAndPointsAtOffsetAndLimit is finding 4 for
// read_file, where the way back to the missing part is already in the
// answer: the cat -n numbering either side of the cut is the "offset" the
// next call needs.
func TestReadFileCapsAWholeFileAndPointsAtOffsetAndLimit(t *testing.T) {
	shrinkMaxToolResultBytes(t, 2000)

	root := t.TempDir()
	client := newTestClient(t, root)
	ctx := context.Background()

	var content strings.Builder
	for i := 1; i <= 2000; i++ {
		content.WriteString("a line of a rather long file\n")
	}
	if _, err := client.CallTool(ctx, "write_file", map[string]any{
		"file_path": "big.txt", "content": content.String(),
	}); err != nil {
		t.Fatal(err)
	}

	res, err := client.CallTool(ctx, "read_file", map[string]any{"file_path": "big.txt"})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Text()
	if len(text) > 3000 {
		t.Errorf("read_file answer is %d bytes against a 2000-byte cap", len(text))
	}
	if !strings.Contains(text, "read the missing range with read_file's \"offset\"") {
		t.Errorf("capped read_file answer does not say how to read the rest:\n%s", text)
	}
	if !strings.Contains(text, "     1\ta line") {
		t.Error("capped read_file answer lost the first line")
	}
	if !strings.Contains(text, "  2000\ta line") {
		t.Error("capped read_file answer lost the last line")
	}
}

// TestEditFileDoesNotReadBackTheWholeStringYouSent: the "String not
// found" loop is one of the measured ones (metrics/tools.go), and an
// agent generating an edit out of a large block can send an old_string
// the size of a file. Naming which edit failed does not take the whole
// of it, and the run has it already.
func TestEditFileDoesNotReadBackTheWholeStringYouSent(t *testing.T) {
	client := newTestClient(t, t.TempDir())
	ctx := context.Background()
	if _, err := client.CallTool(ctx, "write_file", map[string]any{
		"file_path": "f.txt", "content": "a\nb\nc\n",
	}); err != nil {
		t.Fatal(err)
	}
	res, err := client.CallTool(ctx, "edit_file", map[string]any{
		"file_path": "f.txt",
		"old_string": "the first line of what I sent\n" +
			strings.Repeat("a line that is not in that file\n", 500),
		"new_string": "z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("edit_file claimed to have replaced a string that is not there")
	}
	text := res.Text()
	if len(text) > 2000 {
		t.Errorf("edit_file read back %d bytes of the caller's own argument", len(text))
	}
	if !strings.Contains(text, "String not found in file") {
		t.Errorf("the error no longer says what went wrong:\n%s", text)
	}
	if !strings.Contains(text, "the first line of what I sent") {
		t.Errorf("the error does not say which edit failed:\n%s", text)
	}
	// Cut without saying so would read as a shorter string than the one
	// actually searched for, and send the run hunting a difference that
	// is not there.
	if !strings.Contains(text, "not this head") {
		t.Errorf("the error does not say the quoted string is only its head:\n%s", text)
	}
}

// TestReadFileLeavesASmallFileExactlyAsItWas: the cap is a backstop for
// the answers that were eating a run's context, not a change to what
// read_file returns for the files it is normally pointed at.
func TestReadFileLeavesASmallFileExactlyAsItWas(t *testing.T) {
	client := newTestClient(t, t.TempDir())
	ctx := context.Background()
	if _, err := client.CallTool(ctx, "write_file", map[string]any{
		"file_path": "f.txt", "content": "a\nb\nc\n",
	}); err != nil {
		t.Fatal(err)
	}
	res, err := client.CallTool(ctx, "read_file", map[string]any{"file_path": "f.txt"})
	if err != nil {
		t.Fatal(err)
	}
	want := "     1\ta\n     2\tb\n     3\tc"
	if res.Text() != want {
		t.Errorf("got %q, want %q", res.Text(), want)
	}
}
