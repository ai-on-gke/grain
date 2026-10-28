// flag_placement_test.go covers parseArgs (main.go): a flag written
// after the argument it belongs to still counts.
//
// It is here because the alternative was silent. `grain update 5 -title
// x` -- the form this CLI's own usage line prints, `update <id>
// [flags]` -- parsed as "the id, and three words nobody asked about":
// flag.Parse stops at the first non-flag word, fs.Visit then saw no flag
// set at all, and the command sent an empty request, got the unchanged
// task back and printed it. Exit status 0, output that looks like a task
// somebody just edited, and nothing edited. `grain repo capabilities
// acme/widgets -set self-debug` was the same shape: it printed the
// repo's capabilities instead of replacing them.
package cli

import (
	"flag"
	"strings"
	"testing"
)

func TestParseArgsTakesFlagsOnEitherSideOfTheArguments(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		wantTitle  string
		wantAll    bool
		positional []string
	}{
		{
			name:       "flags first, as Go's own Parse requires",
			args:       []string{"-title", "new", "5"},
			wantTitle:  "new",
			positional: []string{"5"},
		},
		{
			name:       "flags after the id, as the usage line prints it",
			args:       []string{"5", "-title", "new"},
			wantTitle:  "new",
			positional: []string{"5"},
		},
		{
			name:       "a boolean flag after the id",
			args:       []string{"5", "-all"},
			wantAll:    true,
			positional: []string{"5"},
		},
		{
			name:       "flags on both sides of it",
			args:       []string{"-all", "5", "-title", "new"},
			wantTitle:  "new",
			wantAll:    true,
			positional: []string{"5"},
		},
		{
			name:       "several arguments keep the order they were written",
			args:       []string{"acme/widgets", "-title", "new", "second"},
			wantTitle:  "new",
			positional: []string{"acme/widgets", "second"},
		},
		{
			name:       "nothing at all",
			args:       nil,
			positional: nil,
		},
		// A bare "--" still ends the flags, which is how a value that
		// starts with a dash reaches a command that takes one.
		{
			name:       "everything after -- is an argument",
			args:       []string{"5", "--", "-title", "new"},
			positional: []string{"5", "-title", "new"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(&strings.Builder{})
			title := fs.String("title", "", "")
			all := fs.Bool("all", false, "")
			rest, err := parseArgs(fs, tc.args)
			if err != nil {
				t.Fatalf("parseArgs(%v) = %v", tc.args, err)
			}
			if *title != tc.wantTitle {
				t.Errorf("-title = %q, want %q", *title, tc.wantTitle)
			}
			if *all != tc.wantAll {
				t.Errorf("-all = %v, want %v", *all, tc.wantAll)
			}
			if strings.Join(rest, " ") != strings.Join(tc.positional, " ") {
				t.Errorf("arguments = %v, want %v", rest, tc.positional)
			}
		})
	}
}

// A flag nobody defined is still an error wherever it is written, rather
// than being quietly taken for an argument -- which is what a plain
// Parse would do with it once a positional word had ended the flags.
func TestParseArgsStillRefusesAFlagNobodyDefined(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&strings.Builder{})
	fs.String("title", "", "")
	if _, err := parseArgs(fs, []string{"5", "-colour", "blue"}); err == nil {
		t.Fatal("parseArgs = nil, want an error naming the flag nobody defined")
	}
}

// The command this was found through, end to end: the title actually
// changes.
func TestUpdateTakesItsFlagsAfterTheTaskID(t *testing.T) {
	server := listTestServer(t)
	if err := runCLIWithAccessCode([]string{"-server", server, "update", "1", "-title", "Renamed work"}); err != nil {
		t.Fatalf("grain update 1 -title: %v", err)
	}
	out := captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "get", "1"}); err != nil {
			t.Fatalf("grain get 1: %v", err)
		}
	})
	if !strings.Contains(out, "Renamed work") {
		t.Errorf("grain get 1 printed %q, want the title the update asked for", out)
	}
}

// And its repo-side twin, which is a parse function rather than a
// request, so it is checked where the rest of that family is.
func TestRepoCapabilitiesTakesItsSetAfterTheRepo(t *testing.T) {
	t.Parallel()
	repo, set, err := parseRepoCapabilities([]string{"acme/widgets", "-set", "self-debug"})
	if err != nil {
		t.Fatalf("parseRepoCapabilities: %v", err)
	}
	if repo != "acme/widgets" {
		t.Errorf("repo = %q, want acme/widgets", repo)
	}
	if set == nil || strings.Join(*set, ",") != "self-debug" {
		t.Errorf("set = %v, want the capabilities the flag named", set)
	}
}
