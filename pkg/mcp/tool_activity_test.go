package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// narratingClient is a server whose calls are narrated onto a task's row,
// driven the way a real client drives one: over the protocol, by tool
// name, so what is under test is the registry's own path rather than a
// handler called directly.
func narratingClient(t *testing.T, reporter StatusReporter, tools ...Tool) *Client {
	t.Helper()
	registry := NewRegistry()
	registry.Register(tools...)
	registry.AnnounceActivity(reporter)
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })
	return client
}

// sandboxClient is narratingClient over the local sandbox roster --
// run_command, read_file and the rest, against root.
func sandboxClient(t *testing.T, reporter StatusReporter, root string) *Client {
	t.Helper()
	return narratingClient(t, reporter, NewSandboxTools(root)...)
}

// callTool calls name and fails the test if the call itself did not get
// through; what the tool answered is returned for the caller to judge.
func callTool(t *testing.T, client *Client, name string, args map[string]any) *CallResult {
	t.Helper()
	res, err := client.CallTool(context.Background(), name, args)
	if err != nil {
		t.Fatalf("calling %s: %v", name, err)
	}
	return res
}

// The whole point of the change: a call that is not run_command says what
// it is doing too. A run that spends twenty minutes reading files used to
// leave the row showing whatever it last said about something else.
func TestACallOtherThanRunCommandNarratesItself(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reporter := &fakeReporter{report: StatusReport{Live: true}}
	client := sandboxClient(t, reporter, root)

	if got := callTool(t, client, "read_file", map[string]any{"file_path": "notes.txt"}).Text(); !strings.Contains(got, "hello") {
		t.Fatalf("read_file answered %q, want the file's own contents", got)
	}
	if len(reporter.notes) != 1 || reporter.notes[0] != "reading a file in the sandbox" {
		t.Fatalf("reporter got %q, want the phrase for read_file", reporter.notes)
	}
}

// Every tool, not only the sandbox's: the ones a run can sit inside for
// twenty minutes are exactly the ones with no command to narrate them.
func TestEachToolNarratesItsOwnPhrase(t *testing.T) {
	for _, tt := range []struct {
		tool string
		want string
	}{
		{"read_grain_task", "reading a grain task"},
		{"wait_for_checks", "waiting for CI"},
		{"chat", "waiting for a reply"},
		{"propose_task", "filing a follow-up task"},
	} {
		t.Run(tt.tool, func(t *testing.T) {
			reporter := &fakeReporter{report: StatusReport{Live: true}}
			client := narratingClient(t, reporter, Tool{
				Name:        tt.tool,
				InputSchema: map[string]any{"type": "object"},
				Handler:     func(context.Context, map[string]any) Result { return Result{Text: "done"} },
			})

			callTool(t, client, tt.tool, nil)
			if len(reporter.notes) != 1 || reporter.notes[0] != tt.want {
				t.Fatalf("reporter got %q, want %q", reporter.notes, tt.want)
			}
		})
	}
}

// A tool this package has never heard of -- one proxied from elsewhere,
// or one added without a phrase -- is named rather than passed over. A
// clumsy live line beats a graceful stale one, which is the bug.
func TestAnUnknownToolIsStillNarrated(t *testing.T) {
	reporter := &fakeReporter{report: StatusReport{Live: true}}
	client := narratingClient(t, reporter, Tool{
		Name:        "consult_the_oracle",
		InputSchema: map[string]any{"type": "object"},
		Handler:     func(context.Context, map[string]any) Result { return Result{Text: "mu"} },
	})

	callTool(t, client, "consult_the_oracle", nil)
	if len(reporter.notes) != 1 || reporter.notes[0] != "calling consult_the_oracle" {
		t.Fatalf("reporter got %q, want the tool named", reporter.notes)
	}
}

// update_status writes the row from its own handler, in the agent's own
// words. The registry stays out of its way rather than overwriting a
// considered phrase with a generic one a moment before it lands.
func TestUpdateStatusIsNotNarratedOverItsOwnNote(t *testing.T) {
	reporter := &fakeReporter{report: StatusReport{Live: true}}
	client := narratingClient(t, reporter, NewStatusTools(reporter)...)

	callTool(t, client, "update_status", map[string]any{"status": "working out why the merge conflicts"})
	if len(reporter.notes) != 1 || reporter.notes[0] != "working out why the merge conflicts" {
		t.Fatalf("reporter got %q, want only the agent's own phrase", reporter.notes)
	}
}

// Every tool this package serves has a phrase of its own: the fallback
// above is for tools grain did not write, and a new one here should read
// as a sentence rather than as its own name.
func TestEveryToolThisPackageServesHasAPhrase(t *testing.T) {
	var rosters []Tool
	rosters = append(rosters, NewSandboxTools(t.TempDir())...)
	rosters = append(rosters, NewSSHSandboxTools(localExecRunner{dir: t.TempDir()}, t.TempDir())...)
	rosters = append(rosters, NewMockTools(&MockSink{}, nil)...)
	rosters = append(rosters, NewStatusTools(nil)...)
	rosters = append(rosters, NewOpenPullRequestTools(nil)...)
	rosters = append(rosters, NewRecreateSandboxTools(nil)...)
	rosters = append(rosters, NewChatTools(nil, nil)...)
	rosters = append(rosters, NewMemoryTools(nil)...)
	rosters = append(rosters, NewTaskTools(nil)...)
	rosters = append(rosters, NewGrainCIWaitTools(nil, CIRepoScope{})...)
	rosters = append(rosters, NewRunCIJobTools(nil, CIRepoScope{})...)
	rosters = append(rosters, NewPullRequestTools(nil, nil, PullRequestScope{})...)
	rosters = append(rosters, NewWaitForChecksTools(nil, nil, PullRequestScope{})...)

	for _, tool := range rosters {
		if _, ok := toolActivityNotes[tool.Name]; !ok {
			t.Errorf("%s has no phrase in toolActivityNotes: a run inside it would say %q on the row",
				tool.Name, toolActivityNote(tool.Name, nil))
		}
	}
}

func TestNarratedCommandReportsItsDescription(t *testing.T) {
	reporter := &fakeReporter{report: StatusReport{Live: true}}
	client := sandboxClient(t, reporter, t.TempDir())

	got := callTool(t, client, "run_command", map[string]any{
		"command":     "echo hello",
		"description": "Say hello",
	}).Text()
	if !strings.Contains(got, "hello") {
		t.Errorf("text = %q, want the command's own output", got)
	}
	if len(reporter.notes) != 1 || reporter.notes[0] != "Say hello" {
		t.Fatalf("reporter got %q, want the description as written", reporter.notes)
	}
}

// watchingReporter answers like a live daemon and, while it does, looks
// for a file the command it is being told about creates -- so the
// ordering below is observed rather than assumed.
type watchingReporter struct {
	marker    string
	sawMarker bool
}

func (r *watchingReporter) ReportStatus(_ context.Context, _ string) (StatusReport, error) {
	if _, err := os.Stat(r.marker); err == nil {
		r.sawMarker = true
	}
	return StatusReport{Live: true}, nil
}

// The line has to be recorded before the call runs, not after: a row that
// only catches up once a ten-minute build has finished is describing the
// one stretch nobody needed it for.
func TestNarratedCallReportsBeforeItRuns(t *testing.T) {
	root := t.TempDir()
	reporter := &watchingReporter{marker: filepath.Join(root, "ran")}
	client := sandboxClient(t, reporter, root)

	callTool(t, client, "run_command", map[string]any{
		"command":     "touch ran",
		"description": "Run a command",
	})
	if _, err := os.Stat(reporter.marker); err != nil {
		t.Fatalf("the command never ran: %v", err)
	}
	if reporter.sawMarker {
		t.Error("the line was recorded after the command had already run, want it before")
	}
}

// A command with no description of its own still says something: the
// tool's own phrase, which is the whole of what the row can honestly
// claim about a call that declined to describe itself.
func TestNarratedCommandWithoutADescriptionFallsBackToItsPhrase(t *testing.T) {
	reporter := &fakeReporter{report: StatusReport{Live: true}}
	client := sandboxClient(t, reporter, t.TempDir())

	for _, args := range []map[string]any{
		{"command": "echo hello"},
		{"command": "echo hello", "description": "   "},
	} {
		callTool(t, client, "run_command", args)
	}
	if len(reporter.notes) != 1 || reporter.notes[0] != "running a command in the sandbox" {
		t.Errorf("reporter got %q, want run_command's own phrase once", reporter.notes)
	}
}

// Repeating a phrase repeats no hop -- an agent reading ten files in a
// row, or re-running the same command with the same description, is not
// new information for the row.
func TestNarrationSkipsARepeatedPhrase(t *testing.T) {
	reporter := &fakeReporter{report: StatusReport{Live: true}}
	client := sandboxClient(t, reporter, t.TempDir())

	for _, note := range []string{"Run the tests", "Run the tests", "Push the branch", "Run the tests"} {
		callTool(t, client, "run_command", map[string]any{"command": "true", "description": note})
	}
	want := []string{"Run the tests", "Push the branch", "Run the tests"}
	if strings.Join(reporter.notes, "|") != strings.Join(want, "|") {
		t.Errorf("reporter got %q, want %q", reporter.notes, want)
	}
}

// The row has another writer -- the run's capability servers, narrated
// from the daemon (capability_activity.go) -- so a repeat is only trusted
// to still be standing for toolActivityRepeatWindow: a test run repeated
// after a workspace edit must say so rather than leave the edit's phrase up.
func TestARepeatedPhraseIsWrittenAgainOnceTheWindowHasPassed(t *testing.T) {
	reporter := &fakeReporter{report: StatusReport{Live: true}}
	registry := NewRegistry()
	registry.Register(NewSandboxTools(t.TempDir())...)
	registry.AnnounceActivity(reporter)
	now := time.Unix(1_800_000_000, 0)
	registry.activity.now = func() time.Time { return now }
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })

	run := map[string]any{"command": "true", "description": "Run the tests"}
	callTool(t, client, "run_command", run)
	now = now.Add(toolActivityRepeatWindow / 2)
	callTool(t, client, "run_command", run)
	now = now.Add(toolActivityRepeatWindow)
	callTool(t, client, "run_command", run)
	if len(reporter.notes) != 2 {
		t.Fatalf("reporter got %q, want the repeat inside the window skipped and the one after it written", reporter.notes)
	}
}

// A declared wait always stamps its start, even when the phrase is the same as
// the last call's (a targeted wait_for_checks followed by a full-suite one),
// so the idle watchdog sees the second wait's timestamp past the first wait's
// transcript output.
func TestRepeatedDeclaredWaitRefreshesItsStamp(t *testing.T) {
	reporter := &fakeReporter{report: StatusReport{Live: true}}
	client := narratingClient(t, reporter, Tool{
		Name:        "wait_for_checks",
		InputSchema: map[string]any{"type": "object"},
		Handler:     func(context.Context, map[string]any) Result { return Result{Text: "green"} },
	})

	callTool(t, client, "wait_for_checks", nil)
	callTool(t, client, "wait_for_checks", nil)
	if len(reporter.notes) != 2 || reporter.notes[0] != "waiting for CI" || reporter.notes[1] != "waiting for CI" {
		t.Fatalf("reporter got %q, want both wait_for_checks calls stamped", reporter.notes)
	}
}

// The row is one line, so a description written as several is flattened
// onto one -- exactly what update_status does with its own note.
func TestNarratedCommandFlattensWhitespace(t *testing.T) {
	reporter := &fakeReporter{report: StatusReport{Live: true}}
	client := sandboxClient(t, reporter, t.TempDir())

	callTool(t, client, "run_command", map[string]any{
		"command": "true", "description": "  running the tests\nfor pkg/orchestrator  ",
	})
	if got := reporter.notes[0]; got != "running the tests for pkg/orchestrator" {
		t.Errorf("reporter got %q, want it flattened onto one line", got)
	}
}

// An over-long description is cut rather than refused: the agent is not
// asking for anything here, and the daemon enforces the same limit on the
// write (ui.MaxActivityLength), so sending it whole would lose the phrase
// entirely instead of its tail.
func TestNarratedCommandTruncatesALongDescription(t *testing.T) {
	reporter := &fakeReporter{report: StatusReport{Live: true}}
	client := sandboxClient(t, reporter, t.TempDir())

	callTool(t, client, "run_command", map[string]any{
		"command": "true", "description": strings.Repeat("x", MaxStatusLength+50),
	})
	got := []rune(reporter.notes[0])
	if len(got) != MaxStatusLength {
		t.Errorf("reporter got %d characters, want it cut to %d", len(got), MaxStatusLength)
	}
	if got[len(got)-1] != '…' {
		t.Errorf("reporter got %q, want the cut marked", string(got))
	}
}

// The call is the work and the line is a courtesy: a daemon that refuses
// the note, or a task nobody is watching any more, changes nothing about
// the call or what the agent is told about it.
func TestNarratedCallRunsAnywayWhenTheLineFails(t *testing.T) {
	for _, tt := range []struct {
		name     string
		reporter *fakeReporter
	}{
		{"daemon unreachable", &fakeReporter{err: errors.New("connection refused")}},
		{"run no longer live", &fakeReporter{report: StatusReport{Live: false}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := sandboxClient(t, tt.reporter, t.TempDir())
			got := callTool(t, client, "run_command", map[string]any{
				"command": "echo hello", "description": "Say hello",
			}).Text()
			if !strings.Contains(got, "hello") {
				t.Errorf("text = %q, want the command's own output", got)
			}
			// Nothing about the daemon reaches the agent: it asked to run
			// a command, and the answer is the command's.
			if strings.Contains(got, "connection refused") {
				t.Errorf("text = %q, want the line's failure kept out of it", got)
			}
		})
	}
}

// A dispatch with no daemon to tell -- a bare `grain mcpserver
// -sandbox-root`, or an agent package reading names off a roster -- works
// exactly as it did before, narrating nothing.
func TestWithoutAReporterNothingIsNarrated(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewSandboxTools(t.TempDir())...)
	registry.AnnounceActivity(nil)
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })

	got := callTool(t, client, "run_command", map[string]any{
		"command": "echo hello", "description": "Say hello",
	}).Text()
	if !strings.Contains(got, "hello") {
		t.Errorf("text = %q, want the command's own output", got)
	}
}

// Both transports advertise run_command under the same name and schema,
// and narration is by name, so a kontur-hosted run narrates itself too --
// it is exactly the one whose commands take long enough for the row to
// matter.
func TestNarrationCoversTheSSHTransportToo(t *testing.T) {
	reporter := &fakeReporter{report: StatusReport{Live: true}}
	client := narratingClient(t, reporter,
		NewSSHSandboxTools(localExecRunner{dir: t.TempDir()}, t.TempDir())...)

	callTool(t, client, "run_command", map[string]any{"command": "true", "description": "Check the guest"})
	if len(reporter.notes) != 1 || reporter.notes[0] != "Check the guest" {
		t.Fatalf("reporter got %q, want the description as written", reporter.notes)
	}
}

// The names and schemas the roster advertises are unchanged by narration:
// which dispatch a run got, and whether anybody is watching it, is not
// something the model should be able to see.
func TestNarrationLeavesTheAdvertisedShapeAlone(t *testing.T) {
	root := t.TempDir()
	plain := NewSandboxTools(root)
	client := sandboxClient(t, &fakeReporter{report: StatusReport{Live: true}}, root)

	listed, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(plain) {
		t.Fatalf("tools/list returned %d tools, want the %d registered", len(listed), len(plain))
	}
	for i := range plain {
		if listed[i].Name != plain[i].Name || listed[i].Description != plain[i].Description {
			t.Errorf("tool %d = %q/%q, want %q/%q", i,
				listed[i].Name, listed[i].Description, plain[i].Name, plain[i].Description)
		}
	}
}
