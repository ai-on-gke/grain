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

package localexec

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The obvious argument-to-code holes of design §12.4 (call 23). These
// are best-effort, line by line, and only warnings: grain cannot see what
// a script really does with its arguments, and the user wrote it.
var (
	evalRE = regexp.MustCompile(`(^|[\s;&|(` + "`" + `])eval(\s|$)`)
	// shellC is a shell given a command string.
	shellC = regexp.MustCompile(`(^|[\s;&|(/` + "`" + `])(sh|bash|dash|zsh|ksh|mksh|ash|fish)\s+(-[a-zA-Z]*\s+)*-[a-zA-Z]*c\b`)
	// positional is any positional parameter.
	positional = regexp.MustCompile(`\$([0-9@*]|\{[0-9@*][^}]*\})`)
	xargsRE    = regexp.MustCompile(`(^|[\s;&|(/` + "`" + `])xargs(\s|$)`)
	// interpreterName is the file name of a shell or interpreter.
	interpreterName = regexp.MustCompile(`^(sh|bash|dash|zsh|ksh|mksh|ash|fish|csh|tcsh|busybox|env|python[0-9.]*|perl[0-9.]*|ruby[0-9.]*|node|nodejs|deno|bun|php[0-9.]*|lua[0-9.]*|luajit|tclsh[0-9.]*|wish[0-9.]*|osascript|pwsh|powershell|awk|gawk|mawk|nawk|expect)$`)
	// execInterpreter is a wrapper that hands its arguments straight to
	// a shell or interpreter: exec python3 "$@".
	execInterpreter = regexp.MustCompile(`(^|[\s;&|])(exec\s+)?(\S*/)?(sh|bash|dash|zsh|ksh|mksh|ash|fish|python[0-9.]*|perl[0-9.]*|ruby[0-9.]*|node|nodejs|php[0-9.]*|lua[0-9.]*|awk|gawk)\s+"?\$(\{)?[@*]`)
)

// Warnings returns design §12.4's warnings for the script at path, or
// nothing when it shows none of the obvious holes.
func Warnings(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if !bytes.HasPrefix(b, []byte("#!")) {
		// Not a script, so maybe an interpreter itself: a copy of
		// python3, or a link to one.
		for _, n := range names(path) {
			if interpreterName.MatchString(n) {
				return []string{fmt.Sprintf("it is itself a shell or interpreter (%s): any argument can be code, so the agent can run anything as you", n)}
			}
		}
	}
	if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
		// A binary: there are no lines to read.
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(line int, format string, args ...any) {
		key := fmt.Sprintf(format, args...)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, fmt.Sprintf("line %d: %s", line, key))
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		code := stripComment(sc.Text())
		if strings.TrimSpace(code) == "" {
			continue
		}
		if evalRE.MatchString(code) {
			add(n, "eval runs its arguments as code: if they come from the agent's arguments, the agent can run anything as you")
		}
		if shellC.MatchString(code) && positional.MatchString(code) {
			add(n, "a shell's -c with a positional parameter runs the agent's argument as a command")
		}
		if m := execInterpreter.FindStringSubmatch(code); m != nil {
			add(n, "it hands its arguments to %s, which can treat them as code", m[4])
		}
		if unquotedAll(code) {
			add(n, "an unquoted $@ or $* splits and globs the agent's arguments: quote it as \"$@\"")
		}
		if xargsRE.MatchString(code) {
			add(n, "xargs turns input into command lines: if the agent's arguments reach it, they can choose what runs")
		}
	}
	return out
}

// names is the file name of path and, if it is a symlink, of each link
// on the way to its target.
func names(path string) []string {
	out := []string{filepath.Base(path)}
	if real, err := filepath.EvalSymlinks(path); err == nil && filepath.Base(real) != out[0] {
		out = append(out, filepath.Base(real))
	}
	return out
}

// stripComment removes a shell comment: a # that starts a word outside
// quotes, and everything after it.
func stripComment(line string) string {
	var sq, dq bool
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '\\' && !sq:
			i++
		case c == '\'' && !dq:
			sq = !sq
		case c == '"' && !sq:
			dq = !dq
		case c == '#' && !sq && !dq && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t' || line[i-1] == ';'):
			return line[:i]
		}
	}
	return line
}

// unquotedAll reports whether line has $@, $*, ${@} or ${*} outside
// double quotes (inside single quotes it is not expanded at all).
func unquotedAll(line string) bool {
	var sq, dq bool
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && !sq:
			i++
		case c == '\'' && !dq:
			sq = !sq
		case c == '"' && !sq:
			dq = !dq
		case c == '$' && !sq && !dq:
			rest := line[i+1:]
			if strings.HasPrefix(rest, "@") || strings.HasPrefix(rest, "*") ||
				strings.HasPrefix(rest, "{@") || strings.HasPrefix(rest, "{*") {
				return true
			}
		}
	}
	return false
}
