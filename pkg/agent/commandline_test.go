package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf8"
)

// The line is meant to be pasteable and readable: the binary, the flags
// in the order they were passed, the empty argument still visible.
func TestCommandLineReadsBackAsTheCommand(t *testing.T) {
	got := CommandLine(Command{
		Path: "/usr/local/bin/claude",
		Args: []string{"-p", "--tools", "", "--model", "claude-sonnet-5", "--mcp-config", "/tmp/mcp 1.json"},
		Dir:  "/srv/sandbox/run-7",
	})
	want := `/usr/local/bin/claude -p --tools '' --model claude-sonnet-5 --mcp-config '/tmp/mcp 1.json' (in /srv/sandbox/run-7)`
	if got != want {
		t.Fatalf("CommandLine =\n%s\nwant\n%s", got, want)
	}
}

// The whole reason this line is redacted rather than printed: the
// credential a framework hands its CLI is in the environment, and the
// journal it lands in leaves this host.
func TestCommandLineCutsCredentialsOutOfTheEnvironment(t *testing.T) {
	const token = "sk-ant-oat01-B7dQ2xMerelyAnExampleValue0000"
	got := CommandLine(Command{
		Path: "/usr/local/bin/claude",
		Env: []string{
			"MCP_TOOL_TIMEOUT=600000",
			"CLAUDE_CODE_OAUTH_TOKEN=" + token,
			"GOOGLE_API_KEY=",
			"HOME=/tmp/agy-home",
		},
	})
	if strings.Contains(got, token) {
		t.Fatalf("the token reached the log line: %s", got)
	}
	for _, want := range []string{
		"MCP_TOOL_TIMEOUT=600000",
		"CLAUDE_CODE_OAUTH_TOKEN=[redacted: CLAUDE_CODE_OAUTH_TOKEN]",
		// Deliberately unsetting a variable is a fact worth keeping:
		// there is no value here to hide.
		"GOOGLE_API_KEY=",
		"HOME=/tmp/agy-home",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CommandLine = %s, want it to contain %q", got, want)
		}
	}
}

// A credential that arrives somewhere nobody expected one is caught by
// its shape rather than by the name it was passed under -- the same
// patterns the grain-log capability redacts a journal with.
func TestCommandLineCutsCredentialShapesAnywhere(t *testing.T) {
	const key = "ghp_0123456789abcdefghijABCDEFGHIJ"
	got := CommandLine(Command{
		Path: "/usr/local/bin/codex",
		Args: []string{"exec", "--remote", "https://grain:" + key + "@github.com/bwsalmon/grain"},
		Env:  []string{"GIT_ASKPASS_VALUE=" + key},
	})
	if strings.Contains(got, key) {
		t.Fatalf("the token reached the log line: %s", got)
	}
}

// A flag named like a credential has its value cut whether or not the
// value has a shape anything could recognize -- both spellings of it.
func TestCommandLineCutsValuesOfCredentialFlags(t *testing.T) {
	got := CommandLine(Command{
		Path: "/usr/local/bin/agy",
		Args: []string{"--api-key", "an-entirely-shapeless-secret", "--session-token=another-one", "--model", "gemini-3-pro"},
	})
	for _, unwanted := range []string{"an-entirely-shapeless-secret", "another-one"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("CommandLine = %s, want it not to contain %q", got, unwanted)
		}
	}
	// And nothing else is cut with them: what the run was actually doing
	// still has to be readable.
	for _, want := range []string{"--api-key [redacted: api-key]", "--session-token=[redacted: session-token]", "--model gemini-3-pro"} {
		if !strings.Contains(got, want) {
			t.Errorf("CommandLine = %s, want it to contain %q", got, want)
		}
	}
}

// A name that merely looks like one of the credential words is not one.
// GIT_AUTHOR_NAME and a resumed session's id are exactly the values an
// operator reads this line for.
func TestCommandLineKeepsValuesThatOnlyLookSensitive(t *testing.T) {
	got := CommandLine(Command{
		Path: "/usr/local/bin/claude",
		Args: []string{"--resume", "6f1c0a2e-session"},
		Env:  []string{"GIT_AUTHOR_NAME=grain"},
	})
	for _, want := range []string{"--resume 6f1c0a2e-session", "GIT_AUTHOR_NAME=grain"} {
		if !strings.Contains(got, want) {
			t.Errorf("CommandLine = %s, want it to contain %q", got, want)
		}
	}
}

// One invocation is one journal entry, whatever an argument holds.
func TestCommandLineStaysOnOneLine(t *testing.T) {
	got := CommandLine(Command{Path: "/bin/echo", Args: []string{"first\nsecond"}})
	if strings.Contains(got, "\n") {
		t.Fatalf("CommandLine spans more than one line: %q", got)
	}
}

// The prompt travels on stdin and is not this line's business.
func TestCommandLineOmitsStdin(t *testing.T) {
	got := CommandLine(Command{Path: "/bin/cat", Stdin: "the whole untrusted issue body"})
	if strings.Contains(got, "untrusted") {
		t.Fatalf("stdin reached the log line: %s", got)
	}
}

// A roster long enough to bury the journal is cut, and says so.
func TestCommandLineTruncatesAndSaysSo(t *testing.T) {
	got := CommandLine(Command{Path: "/usr/local/bin/claude", Args: []string{strings.Repeat("a", maxCommandLine*2)}})
	if len(got) > maxCommandLine+64 {
		t.Errorf("CommandLine is %d bytes, want it capped near %d", len(got), maxCommandLine)
	}
	if !strings.Contains(got, "more bytes)") {
		t.Errorf("CommandLine = ...%q, want it to announce what it cut", got[max(0, len(got)-64):])
	}
}

// And the point of all of it: running a CLI writes that line to the
// daemon's log, before the process is started rather than after it ends.
func TestLocalHostRunLogsTheInvocation(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no shell to run: %v", err)
	}
	var lines []string
	host := LocalHost{Logf: func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}}
	const token = "sk-ant-oat01-B7dQ2xMerelyAnExampleValue0000"
	out, err := host.Run(context.Background(), Command{
		Path:  sh,
		Args:  []string{"-c", "echo ran"},
		Env:   []string{"CLAUDE_CODE_OAUTH_TOKEN=" + token},
		Dir:   os.TempDir(),
		Stdin: "",
	}, io.Discard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(out) != "ran" {
		t.Errorf("stdout = %q, want %q", out, "ran")
	}
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want 1: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "agent: running: "+sh+" -c 'echo ran'") {
		t.Errorf("logged %q, want the invocation", lines[0])
	}
	if strings.Contains(lines[0], token) {
		t.Errorf("the token reached the daemon log: %s", lines[0])
	}
}

// A cut taken at a byte offset would leave half a multi-byte character
// in the log line; the count of what was dropped must still add up.
func TestCommandLineTruncatesOnARuneBoundary(t *testing.T) {
	for pad := 0; pad < 3; pad++ {
		line := strings.Repeat("a", pad) + strings.Repeat("€", maxCommandLine)
		got := truncate(line)
		if !utf8.ValidString(got) {
			t.Fatalf("pad %d: truncated line is not valid UTF-8", pad)
		}
		head, _, _ := strings.Cut(got, "... (")
		if want := fmt.Sprintf("... (%d more bytes)", len(line)-len(head)); !strings.HasSuffix(got, want) {
			t.Errorf("pad %d: truncated line ends %q, want %q", pad, got[len(got)-30:], want)
		}
	}
}
