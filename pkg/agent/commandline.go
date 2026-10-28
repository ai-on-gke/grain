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

package agent

// What an agent CLI invocation looks like in the daemon's own log.
//
// Every run here ends up as one exec of somebody else's CLI, and until
// now nothing said which one. An operator reading the journal after a run
// that produced nothing could see the orchestrator dispatch it and see
// whatever the CLI itself printed, but not the line in between: which
// binary, which model, which MCP config, which working directory, which
// variables were set. That line is the first thing anybody reaches for
// when a framework upgrade changes a flag, when two deployments disagree
// about a model name, or when a run fails before the CLI has written a
// single byte of its own output.
//
// It is also the line most likely to carry a credential, since a
// credential is exactly what a CLI is handed to log in with. So nothing
// here prints a value unexamined:
//
//   - Every argument and every environment value goes through
//     grainlog.Redactor's shape patterns, which is the same redaction
//     this repository already applies to journal text a run reads back
//     (pkg/capability/grainlog/redact.go). A token that matches a known
//     shape is cut whatever it is attached to.
//   - A flag or a variable *named* like a credential has its value cut
//     whether or not the value looks like one -- the shapeless secrets
//     (a webhook secret, an operator's pasted string) are precisely the
//     ones no pattern can recognize. This over-redacts: a path passed as
//     --exec-key is cut too. That is the right trade, because the cost
//     of over-redacting is a debugging session that has to read one
//     value out of the configuration instead of the log, and the cost of
//     under-redacting is a credential in a journal that is shipped
//     off-host and readable by every run holding the grain-log grant.
//
// Stdin is deliberately not rendered. It carries the run's whole prompt
// -- untrusted issue content, kilobytes of it -- which is not what this
// line is for, and which the attempt transcript already keeps.

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/ai-on-gke/grain/pkg/capability/grainlog"
)

// maxCommandLine is how much of a rendered invocation is logged.
//
// One of these lines is genuinely long: claude's --allowedTools is every
// tool on the roster, comma-separated, and that alone runs to a couple of
// kilobytes. Cutting it costs a reader the tail of a list they can read
// off the roster anyway; letting it run costs every reader of the journal
// a screenful per run. Whatever is cut is announced with a byte count, so
// nobody reads a truncated command as the whole of one.
const maxCommandLine = 4096

// CommandLine renders one Command the way an operator would have typed
// it: the binary, its arguments, the directory it starts in and the
// variables added to its environment, with credential material taken out
// (see this file's own comment).
//
// The result is a single line -- a newline inside an argument is escaped
// by the quoting below -- so that one invocation is one journal entry and
// `journalctl | grep` finds the whole of it.
func CommandLine(c Command) string {
	r := grainlog.NewRedactor(nil)
	var b strings.Builder
	b.WriteString(render(r, c.Path))
	var pending string
	for _, arg := range c.Args {
		b.WriteByte(' ')
		b.WriteString(renderArg(r, arg, pending))
		pending = arg
	}
	if c.Dir != "" {
		fmt.Fprintf(&b, " (in %s)", c.Dir)
	}
	if len(c.Env) > 0 {
		rendered := make([]string, 0, len(c.Env))
		for _, kv := range c.Env {
			rendered = append(rendered, renderEnv(r, kv))
		}
		fmt.Fprintf(&b, " (env: %s)", strings.Join(rendered, " "))
	}
	return truncate(b.String())
}

// renderArg renders one argument, given the argument before it: a value
// that follows a flag named like a credential is the credential, and has
// no shape of its own to recognize it by.
func renderArg(r *grainlog.Redactor, arg, previous string) string {
	if isSensitiveName(previous) && !strings.HasPrefix(arg, "-") {
		return redactedFor(previous)
	}
	// --flag=value, where the flag is the one naming what the value is.
	if name, value, ok := strings.Cut(arg, "="); ok && strings.HasPrefix(name, "-") && isSensitiveName(name) {
		if value == "" {
			return arg
		}
		return name + "=" + redactedFor(name)
	}
	return render(r, arg)
}

// renderEnv renders one "NAME=value" entry. An empty value is left as it
// is: "GOOGLE_API_KEY=" is a framework deliberately unsetting a variable
// (agent/antigravity), and hiding the nothing it set would hide the fact.
func renderEnv(r *grainlog.Redactor, kv string) string {
	name, value, ok := strings.Cut(kv, "=")
	if !ok || value == "" {
		return kv
	}
	if isSensitiveName(name) {
		return name + "=" + redactedFor(name)
	}
	return name + "=" + render(r, value)
}

// render is one value as it appears on the line: quoted for the shell
// first, then run through the shape patterns -- what catches a credential
// nobody named, in an argument nobody expected to hold one.
//
// That order is what keeps a redaction marker readable. Quoting decided
// afterwards would look at "[redacted: api key]", see the space and the
// brackets it introduced, and wrap grain's own marker in quotes as though
// the run had passed it.
func render(r *grainlog.Redactor, value string) string {
	redacted, _ := r.Line(quoteArg(value))
	return redacted
}

func redactedFor(name string) string {
	return fmt.Sprintf("[redacted: %s]", strings.TrimLeft(name, "-"))
}

// sensitiveNames are the words that make a flag or a variable one whose
// value never reaches the log. They are matched as substrings of the
// upper-cased name, so CLAUDE_CODE_OAUTH_TOKEN, --api-key and
// GH_APP_PRIVATE_KEY_PATH are all caught by one entry each.
// A word here is worth its false positives and no more: "AUTHORIZATION"
// and "OAUTH" rather than a bare "AUTH", which would have cut
// GIT_AUTHOR_NAME, and no "SESSION", because a CLI's session id is the
// thing an operator most wants to read back out of this line when a run
// was resumed.
var sensitiveNames = []string{
	"TOKEN",
	"SECRET",
	"PASSWORD",
	"PASSWD",
	"CREDENTIAL",
	"APIKEY",
	"API_KEY",
	"AUTHORIZATION",
	"OAUTH",
	"KEY",
	"COOKIE",
}

// isSensitiveName reports whether a flag or variable of this name is one
// whose value is assumed to be a credential.
func isSensitiveName(name string) bool {
	if name == "" {
		return false
	}
	upper := strings.ToUpper(strings.ReplaceAll(strings.TrimLeft(name, "-"), "-", "_"))
	for _, word := range sensitiveNames {
		if strings.Contains(upper, word) {
			return true
		}
	}
	return false
}

// quoteArg renders one argument so that the line reads back as a command
// somebody could paste: an argument holding a space or a shell
// metacharacter is single-quoted, and an ordinary one is left bare rather
// than dressed in quotes nobody typed. An empty argument is rendered as
// ” -- claude's own `--tools ”`, which would otherwise vanish from the
// line entirely.
//
// An argument holding a newline or any other unprintable byte is escaped
// Go-style instead, which is the one form that keeps a single invocation
// to a single journal entry.
func quoteArg(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return strconv.Quote(s)
		}
	}
	if !strings.ContainsAny(s, " '\"\\$`|&;<>()*?[]{}#~!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// truncate caps the whole line, saying how much it cut. See
// maxCommandLine.
func truncate(line string) string {
	if len(line) <= maxCommandLine {
		return line
	}
	head := headAtRune(line, maxCommandLine)
	return fmt.Sprintf("%s... (%d more bytes)", head, len(line)-len(head))
}
