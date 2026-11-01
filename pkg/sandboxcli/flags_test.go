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

package sandboxcli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeFileProxy answers every /fs/ read with body, as the namespace does
// (X-Grain-Total-Size and all), and records what it was asked.
func fakeFileProxy(t *testing.T, body string) (env sandboxEnv, asked *[]string) {
	t.Helper()
	asked = &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*asked = append(*asked, r.URL.RequestURI())
		w.Header().Set("X-Grain-Total-Size", fmt.Sprint(len(body)))
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return fakeSandboxEnv(map[string]string{"GRAIN_SANDBOX_PROXY": srv.URL, "GRAIN_SANDBOX_TOKEN": "tok"}, ""), asked
}

func runIn(t *testing.T, env sandboxEnv, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code, handled := maybeRunSandboxCLI(args, env, &out, &errOut)
	if !handled {
		t.Fatalf("%q not handled in sandbox mode", args)
	}
	return out.String(), errOut.String(), code
}

// Every command a sandbox answers names the flags it reads, and has help.
func TestEverySandboxCommandNamesItsFlags(t *testing.T) {
	for cmd, subs := range sandboxCommands {
		if isHelpFlag(cmd) || cmd == "cap" {
			continue
		}
		names := []string{cmd}
		if subs != nil {
			names = nil
			for _, s := range subs {
				if cmd+" "+s == "memory doc" {
					s = "doc-write"
				}
				names = append(names, cmd+" "+s)
			}
		}
		for _, name := range names {
			if _, ok := sandboxCommandFlags[name]; !ok {
				t.Errorf("sandboxCommandFlags has no entry for %q", name)
			}
			if _, ok := sandboxCommandHelp[name]; !ok {
				t.Errorf("sandboxCommandHelp has no entry for %q", name)
			}
		}
	}
}

// A flag a command does not read, or a count that is not one, is refused
// before anything is asked of the proxy -- not dropped, so that the answer
// is to the question asked.
func TestSandboxCommandsRefuseFlagsTheyDoNotRead(t *testing.T) {
	env, asked := fakeFileProxy(t, "whole file\n")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"task", "list", "--bogus", "x"}, "grain task list: unknown flag --bogus (flags: --changed-since, --json, "},
		{[]string{"task", "read", "1933", "--tail", "2000"}, "grain task read: unknown flag --tail (it takes none)"},
		{[]string{"pr", "base-status", "--repo", "acme/widgets"}, "grain pr base-status: unknown flag --repo"},
		{[]string{"ls", "ci://acme/widgets", "-z"}, "grain ls: unknown flag -z (flags: --glob)"},
		{[]string{"grep", "x", "src://cmd", "--lines", "3"}, "grain grep: unknown flag --lines (flags: -C, --glob, -i, --max)"},
		{[]string{"ci", "list", "--limit", "abc"}, `grain ci list: --limit "abc" is not a count`},
		{[]string{"read", "config://README.md", "--tail", "-3"}, `grain read: --tail "-3" is not a count`},
		{[]string{"read", "config://README.md", "-n", "x"}, `grain read: -n "x" is not a count`},
		{[]string{"grep", "x", "src://cmd", "-C", "two"}, `grain grep: -C "two" is not a count`},
		{[]string{"task", "propose", "--title", "t", "--body", "b", "--model", "m"}, "grain task propose: unknown flag --model"},
		{[]string{"read", "config://README.md", "--tail"}, "grain read: flag --tail needs a value"},
	} {
		_, errOut, code := runIn(t, env, tc.args...)
		if code != 2 || !strings.HasPrefix(errOut, tc.want) {
			t.Errorf("%q: exit %d %q, want exit 2 and %q", tc.args, code, errOut, tc.want)
		}
	}
	if len(*asked) != 0 {
		t.Errorf("refused commands still asked the proxy %q", *asked)
	}
}

// A typo in a sandbox command's subcommand is answered with that command's
// own help and exit 2 -- never by falling through to the host CLI, whose
// grain ci has another vocabulary altogether.
func TestUnknownSubcommandGetsTheSandboxHelp(t *testing.T) {
	env, asked := fakeFileProxy(t, "")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"task", "lsit"}, "grain task: unknown command \"lsit\"\n\ngrain task (list | read | prompt | transcript | wait | message | propose)"},
		{[]string{"ci", "foo"}, "grain ci: unknown command \"foo\"\n\ngrain ci (list | log | test-history | artifact | wait | run-job | retrigger)"},
		{[]string{"db", "upgrade-postgres"}, "grain db: unknown command \"upgrade-postgres\""},
	} {
		out, errOut, code := runIn(t, env, tc.args...)
		if code != 2 || out != "" || !strings.HasPrefix(errOut, tc.want) {
			t.Errorf("%q: exit %d %q %q, want exit 2 and %q", tc.args, code, out, errOut, tc.want)
		}
	}
	if len(*asked) != 0 {
		t.Errorf("unknown subcommands asked the proxy %q", *asked)
	}
}

// A top-level command neither CLI has is answered, inside a sandbox, with
// the sandbox CLI's help rather than the host CLI's daemon-facing usage.
func TestUnknownCommandInASandboxGetsTheSandboxHelp(t *testing.T) {
	env, _ := fakeFileProxy(t, "")
	var errOut bytes.Buffer
	code, handled := unknownCommand([]string{"frobnicate"}, env, &errOut)
	if !handled || code != 2 || !strings.HasPrefix(errOut.String(), "grain: unknown command \"frobnicate\"\n\ngrain -- sandbox CLI") ||
		strings.Contains(errOut.String(), "runhost") {
		t.Errorf("frobnicate: handled %v, exit %d %q", handled, code, errOut.String())
	}
	if _, handled := unknownCommand([]string{"-server", "http://x", "list"}, env, &errOut); handled {
		t.Error("a leading global flag is the host CLI's, and was taken")
	}
	if _, handled := unknownCommand([]string{"frobnicate"}, fakeSandboxEnv(nil, ""), &errOut); handled {
		t.Error("handled outside a sandbox")
	}
}

// --tail, --offset and --limit are the namespace's byte range, passed on;
// --lines (-n) is the last lines of whatever that answers.
func TestReadTakesBytesAndLines(t *testing.T) {
	env, asked := fakeFileProxy(t, "one\ntwo\nthree\nfour")
	for _, tc := range []struct {
		args      []string
		out, path string
	}{
		{[]string{"read", "config://README.md"}, "one\ntwo\nthree\nfour", "/fs/config/README.md"},
		{[]string{"read", "config://README.md", "--tail", "5"}, "one\ntwo\nthree\nfour", "/fs/config/README.md?tail=5"},
		{[]string{"read", "config://README.md", "--lines", "2"}, "three\nfour", "/fs/config/README.md"},
		{[]string{"read", "config://README.md", "-n", "3", "--offset", "0", "--limit", "9"}, "two\nthree\nfour", "/fs/config/README.md?limit=9&offset=0"},
		{[]string{"read", "config://README.md", "-n", "9"}, "one\ntwo\nthree\nfour", "/fs/config/README.md"},
		{[]string{"task", "prompt", "12", "-n", "1"}, "four", "/fs/task/12/prompt.md"},
		{[]string{"task", "prompt", "12", "--attempt", "2", "--tail", "4"}, "one\ntwo\nthree\nfour", "/fs/task/12/attempts/2/prompt.md?tail=4"},
	} {
		*asked = nil
		out, errOut, code := runIn(t, env, tc.args...)
		if code != 0 || out != tc.out || len(*asked) != 1 || (*asked)[0] != tc.path {
			t.Errorf("%q: exit %d %q %s, asked %q; want %q of %s", tc.args, code, out, errOut, *asked, tc.out, tc.path)
		}
	}
}

// grain read deploy://pipeline-changes pages with --cursor (or --since)
// and --page, which every other file refuses.
func TestReadPagesPipelineChanges(t *testing.T) {
	env, asked := fakeFileProxy(t, "a change\nnext: grain read deploy://pipeline-changes --cursor c~2\n")
	for _, tc := range []struct {
		args []string
		path string
	}{
		{[]string{"read", "deploy://pipeline-changes"}, "/fs/deploy/pipeline-changes"},
		{[]string{"read", "deploy://pipeline-changes", "--cursor", "2026-10-29T11:50:43.079363246Z~1"},
			"/fs/deploy/pipeline-changes?cursor=2026-10-29T11%3A50%3A43.079363246Z~1"},
		{[]string{"read", "deploy://pipeline-changes", "--since", "2026-10-29T11:50:43Z", "--page", "20"},
			"/fs/deploy/pipeline-changes?page=20&since=2026-10-29T11%3A50%3A43Z"},
	} {
		*asked = nil
		_, errOut, code := runIn(t, env, tc.args...)
		if code != 0 || len(*asked) != 1 || (*asked)[0] != tc.path {
			t.Errorf("%q: exit %d %s, asked %q; want %s", tc.args, code, errOut, *asked, tc.path)
		}
	}
	for _, args := range [][]string{
		{"read", "config://README.md", "--cursor", "c~1"},
		{"read", "deploy://metrics", "--page", "5"},
	} {
		*asked = nil
		_, errOut, code := runIn(t, env, args...)
		if code == 0 || len(*asked) != 0 || !strings.Contains(errOut, "pages deploy://pipeline-changes") {
			t.Errorf("%q: exit %d, asked %q, stderr %q; want a refusal before any read", args, code, *asked, errOut)
		}
	}
	if _, errOut, code := runIn(t, env, "read", "deploy://pipeline-changes", "--page", "lots"); code == 0 || !strings.Contains(errOut, "not a count") {
		t.Errorf("--page lots: exit %d, stderr %q; want a usage error", code, errOut)
	}
}

// grain task transcript says on stderr how big the transcript is, so a run
// knows to read part of it, and takes the same range flags as read.
func TestTaskTranscriptSaysHowBigItIs(t *testing.T) {
	env, asked := fakeFileProxy(t, "a\nb\nc\n")
	out, errOut, code := runIn(t, env, "task", "transcript", "1933", "--lines", "1")
	if code != 0 || out != "c\n" || (*asked)[0] != "/fs/task/1933/transcript.md" ||
		!strings.Contains(errOut, "task://1933/transcript.md is 6 bytes") {
		t.Errorf("transcript --lines 1: exit %d %q %q, asked %q", code, out, errOut, *asked)
	}
	*asked = nil
	if _, _, code := runIn(t, env, "task", "transcript", "1933", "--tail", "2000"); code != 0 || (*asked)[0] != "/fs/task/1933/transcript.md?tail=2000" {
		t.Errorf("transcript --tail 2000: exit %d, asked %q", code, *asked)
	}
	// A prompt, which nobody reads in parts, says nothing about its size.
	if _, errOut, _ := runIn(t, env, "task", "prompt", "1933"); errOut != "" {
		t.Errorf("task prompt stderr = %q", errOut)
	}
}

func TestTailWriterKeepsTheLastLinesAcrossWrites(t *testing.T) {
	for _, tc := range []struct {
		chunks []string
		n      int
		want   string
	}{
		{[]string{"a\nb", "\nc\n"}, 2, "b\nc\n"},
		{[]string{"a\n", "b\n", "c"}, 2, "b\nc"},
		{[]string{"abc"}, 1, "abc"},
		{[]string{"a\nb\n"}, 0, ""},
		{nil, 3, ""},
	} {
		tw := &tailWriter{n: tc.n}
		for _, c := range tc.chunks {
			tw.Write([]byte(c))
		}
		if got := string(tw.bytes()); got != tc.want {
			t.Errorf("%q, last %d = %q, want %q", tc.chunks, tc.n, got, tc.want)
		}
	}
}

// The help says the range flags count bytes, offers --lines, and its
// examples name no retired repository and no path one edition lacks.
func TestSandboxHelpSaysBytesAndHasLiveExamples(t *testing.T) {
	for _, cmd := range []string{"read", "ci log", "task prompt", "task transcript", "log"} {
		help := sandboxCommandHelp[cmd]
		if !strings.Contains(help, "BYTES") || !strings.Contains(help, "--lines N") {
			t.Errorf("grain %s --help does not say --tail is bytes and --lines is lines:\n%s", cmd, help)
		}
	}
	if !strings.Contains(sandboxCommandHelp["grep"], "--max N") {
		t.Errorf("grain grep --help does not list --max:\n%s", sandboxCommandHelp["grep"])
	}
	all := sandboxHelpText
	for _, help := range sandboxCommandHelp {
		all += help
	}
	for _, stale := range []string{"bwsalmon/grain", "src://pkg"} {
		if strings.Contains(all, stale) {
			t.Errorf("the sandbox help still uses %s in an example", stale)
		}
	}
	// Every flag a command's table names that is not one of grain cap's is
	// spelled the way its help spells it.
	for cmd, flags := range sandboxCommandFlags {
		help := sandboxCommandHelp[cmd]
		if cmd == "task propose" || cmd == "api" || cmd == "notify" || cmd == "task list" || cmd == "task wait" || cmd == "status" || cmd == "handover write" {
			continue
		}
		for _, f := range flags {
			if f == "n" || f == "min-priority" || f == "max-lines" || f == "json" || f == "period" && cmd == "release-notes publish" {
				continue
			}
			if !strings.Contains(help, flagName(f)) {
				t.Errorf("grain %s takes %s, which its --help does not mention", cmd, flagName(f))
			}
		}
	}
}

// The help names the proxy it answers over, never the token it presents.
func TestSandboxHelpLeavesOutTheProxyToken(t *testing.T) {
	if got := redactProxy("http://sandbox:s3cret@192.168.9.1:9728"); got != "http://192.168.9.1:9728" {
		t.Errorf("redactProxy = %q", got)
	}
	if got := redactProxy("http://127.0.0.1:9728"); got != "http://127.0.0.1:9728" {
		t.Errorf("redactProxy without a token = %q", got)
	}
}
