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

package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/procgroup"
	"github.com/ai-on-gke/grain/pkg/redact"
	"github.com/ai-on-gke/grain/pkg/safepath"
)

// NewSandboxTools returns the four tools v1 runs over SSH against a real
// assigned sandbox VM (run_command, read_file, edit_file, write_file),
// ported field-for-field: same names, same input schemas, same output
// shapes. Most callers here still have no host adapter to give these a
// real remote VM to run against, so root stands in for the sandbox --
// every tool here is confined to it. That confinement is v1's own boundary
// substitute, not something v1 needed: its actual isolation is the
// sandbox VM itself. NewSSHSandboxTools (ssh_tools.go) is the same four
// tools with that boundary lifted, run against a real remote host instead
// -- what a kontur-managed sandbox VM needs (bwsalmon/agents#256).
//
// extra is NAME=VALUE entries ValidateSandboxEnv accepts, which
// run_command sets on top of the sandboxPassthrough allowlist. It is how
// an operator opts a -host-sandboxes run into ambient workstation
// credentials -- an SSH_AUTH_SOCK, a CLOUDSDK_CONFIG pointing at their
// real gcloud login, an HTTPS_PROXY -- by name (the daemon's
// -host-sandbox-env), without lifting the allowlist for every run or
// unpinning HOME. Every other caller passes none.
//
// Because those entries carry the operator's own identity, run_command's
// output is redacted whenever there are any (SandboxEnvRedactor): every
// value passed through, and every recognizable token shape, is cut before
// the result reaches the agent -- and so before it reaches the transcript
// grain stores, a commit, or anything streamed off this host (GSA1-12).
func NewSandboxTools(root string, extra ...string) []Tool {
	return NewHostSandboxTools(root, nil, extra...)
}

// NewHostSandboxTools is NewSandboxTools for a host-directory sandbox
// that must not reach the paths in hide: run_command runs each command
// with them hidden (host_confine.go), and refuses to run one at all
// where that cannot be done. read_file, edit_file and write_file need
// no such list, since they already reach nothing outside root. A nil
// hide is NewSandboxTools exactly.
func NewHostSandboxTools(root string, hide []string, extra ...string) []Tool {
	return []Tool{
		runCommandTool(root, hide, extra),
		readFileTool(root),
		editFileTool(root),
		writeFileTool(root),
	}
}

// resolvePath maps a tool-supplied path onto one inside root, rejecting
// anything -- absolute, relative-with-.., or symlinks -- that would land
// outside it. It returns two spellings of the same file: named, the path
// cleaned and joined onto root but otherwise as the caller wrote it, for
// saying which file was used; and real, the one safepath.Within resolved
// through every symlink and checked, which is the only one to open. A
// dangling symlink is refused even when its target would be inside root:
// what writing through it creates is wherever the link points, and
// Within will not vouch for a place that is not there yet.
func resolvePath(root, p string) (named, real string, err error) {
	if p == "" {
		return "", "", errors.New("file_path is required")
	}
	if filepath.IsAbs(p) {
		named = filepath.Clean(p)
	} else {
		named = filepath.Clean(filepath.Join(root, p))
	}
	real, err = safepath.Within(root, named)
	if err != nil {
		// The workspace is named in the refusal, not just the path that
		// missed it: the caller that gets here is usually one that has
		// been told the wrong thing about where it is working
		// (workspace_paths.go), and "escapes the sandbox root" on its
		// own gives it nothing to correct that with.
		return "", "", fmt.Errorf("path %q is outside this sandbox's workspace (%s) -- "+
			"name files by their path inside it", p, filepath.Clean(root))
	}
	return named, real, nil
}

// resolveForRead resolves a tool-supplied path the way resolvePath does
// and, when that fails for an absolute path pointing outside the
// sandbox, tries the same path once more under root -- returning the
// path to read, the path it names (the one the caller asked for, or the
// retry's), and the notice saying which path was actually used.
//
// Only a path that is really there is substituted: a run that named a
// file that exists nowhere gets its own path back in the error, since
// the alternative is answering "no such file" with a different "no such
// file" and a paragraph of explanation for it. See workspace_paths.go
// for the guess this recovers and how often runs make it.
func resolveForRead(root, fp string) (real, named, notice string, err error) {
	named, real, err = resolvePath(root, fp)
	if err == nil {
		return real, named, "", nil
	}
	retry, ok := workspaceRetry(root, fp)
	if !ok {
		return "", "", "", err
	}
	retryNamed, retryReal, retryErr := resolvePath(root, retry)
	if retryErr != nil {
		return "", "", "", err
	}
	if _, statErr := os.Stat(retryReal); statErr != nil {
		return "", "", "", err
	}
	return retryReal, retryNamed, workspaceRetryNotice(fp, retryNamed, root), nil
}

// resolveForWrite is resolveForRead for a file that need not exist yet,
// and gates the retry on the parent directory existing instead: a write
// to a path outside the sandbox is worth redirecting to a directory
// that is actually there, and never worth guessing a whole tree into
// existence under -- "/etc/hosts" must not quietly become a new
// <workspace>/etc/hosts.
func resolveForWrite(root, fp string) (real, named, notice string, err error) {
	named, real, err = resolvePath(root, fp)
	if err == nil {
		return real, named, "", nil
	}
	retry, ok := workspaceRetry(root, fp)
	if !ok {
		return "", "", "", err
	}
	retryNamed, retryReal, retryErr := resolvePath(root, retry)
	if retryErr != nil {
		return "", "", "", err
	}
	if info, statErr := os.Stat(filepath.Dir(retryReal)); statErr != nil || !info.IsDir() {
		return "", "", "", err
	}
	return retryReal, retryNamed, workspaceRetryNotice(fp, retryNamed, root), nil
}

// The four schemas below are shared with ssh_tools.go: NewSSHSandboxTools'
// tools run their work over SSH instead of locally, but advertise the
// exact same name/description/inputSchema shape a caller of this package
// already sees from NewSandboxTools, so which one a given mcpserver
// process was started with is invisible to the model calling them.
var runCommandInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"command": map[string]any{"type": "string"},
		"timeout": map[string]any{
			"type":        "number",
			"description": "Timeout in milliseconds, max 600000. Defaults to 300000 (5 minutes) if omitted.",
		},
		"description": map[string]any{
			"type": "string",
			// Not decoration, and not only for the transcript: where the
			// run has a daemon to tell, this is shown on its task's row
			// as what the run is doing while the command runs
			// (tool_activity.go), in place of the phrase the tool's own
			// name would otherwise get, which is why it asks for a phrase
			// written for somebody who has not read the command.
			"description": "Short description of what this command does, in one line -- " +
				"\"running the test suite\", \"pushing the branch\". It is shown to the " +
				"human watching this task as what your run is doing while the command " +
				"runs, so write it for somebody who cannot see the command itself, and " +
				"write one for every command rather than saving them for the " +
				"interesting ones.",
		},
	},
	"required": []string{"command"},
}

// defaultRunCommandTimeout is the bound run_command applies when its
// caller omits "timeout" entirely, so an unbounded command (e.g. an
// unbounded `grep -r /`) can never wedge its sandbox slot indefinitely
// with no automatic recovery (bwsalmon/agents#575) -- relying on the
// model to remember to pass "timeout" was the actual gap: a run only
// advances to its next turn once the current tool call returns, so an
// omitted timeout left a stuck run_command with no server-side bound at
// all. maxRunCommandTimeout is the ceiling runCommandInputSchema already
// advertises for a caller-supplied value.
//
// defaultRunCommandTimeout is a var, not a const, solely so a test can
// shrink it for the duration of a single test rather than actually
// waiting out the real default.
var defaultRunCommandTimeout = 5 * time.Minute

const maxRunCommandTimeout = 600 * time.Second

// runCommandTimeout resolves run_command's "timeout" argument
// (milliseconds, matching native Bash's own unit) into a duration
// clamped to [1s, maxRunCommandTimeout], or defaultRunCommandTimeout when
// the caller supplies none -- shared by both this package's run_command
// (a local subprocess) and ssh_tools.go's (a remote `timeout` coreutil
// invocation) so the two apply the identical bound.
func runCommandTimeout(args map[string]any) time.Duration {
	ms, ok := argFloat(args, "timeout")
	if !ok {
		return defaultRunCommandTimeout
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		d = time.Second
	}
	if d > maxRunCommandTimeout {
		d = maxRunCommandTimeout
	}
	return d
}

var readFileInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"file_path": map[string]any{"type": "string"},
		"offset":    map[string]any{"type": "integer", "description": "Line number to start from"},
		"limit":     map[string]any{"type": "integer", "description": "Number of lines to read"},
	},
	"required": []string{"file_path"},
}

var writeFileInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"file_path": map[string]any{"type": "string"},
		"content":   map[string]any{"type": "string"},
	},
	"required": []string{"file_path", "content"},
}

var editFileInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"file_path":   map[string]any{"type": "string"},
		"old_string":  map[string]any{"type": "string"},
		"new_string":  map[string]any{"type": "string"},
		"replace_all": map[string]any{"type": "boolean", "default": false},
	},
	"required": []string{"file_path", "old_string", "new_string"},
}

// sandboxPassthrough is the allowlist of environment variables that
// local sandbox subprocesses are permitted to inherit from the host daemon.
// All host tokens, API keys, GCP credentials, database URLs, and daemon
// internals are dropped by omitting them from this allowlist.
var sandboxPassthrough = []string{
	"PATH",
	"USER",
	"LOGNAME",
	"SHELL",
	"TERM",
	"LANG",
	"LC_ALL",
	"LC_CTYPE",
	"TMPDIR",
	"GOMODCACHE",
	"GOCACHE",
	"npm_config_cache",
}

const sandboxFallbackPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// ValidateSandboxEnv checks extra entries for NewSandboxTools:
// each must be NAME=VALUE with NAME a plain environment variable name,
// and none may name HOME. HOME stays pinned to the sandbox root whatever
// an operator passes through, because that is what keeps a run's
// ~/.gitconfig and ~/.git-credentials its own (ConfigureGitCredentials)
// rather than the operator's; a tool whose login lives under the real
// HOME is pointed at it by its own variable instead (CLOUDSDK_CONFIG for
// gcloud, and so on).
func ValidateSandboxEnv(extra []string) error {
	for _, kv := range extra {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("sandbox environment entry %q is not NAME=VALUE", kv)
		}
		if !validEnvName(name) {
			return fmt.Errorf("sandbox environment entry %q: %q is not an environment variable name", kv, name)
		}
		if name == "HOME" {
			return errors.New("sandbox environment cannot set HOME: it is pinned to each run's own directory " +
				"so that run's git credentials stay its own -- point the tool at its real config directory " +
				"with its own variable instead (e.g. CLOUDSDK_CONFIG)")
		}
	}
	return nil
}

// validEnvName is the POSIX portable spelling of an environment variable
// name: letters, digits and underscores, not starting with a digit.
func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// sandboxEnv constructs a sanitized environment for local sandbox subprocesses:
// HOME is pinned to root (so ~/.gitconfig, ~/.git-credentials, etc. remain confined),
// and only safe, allowlisted host variables are passed through, followed by
// extra (NewSandboxTools' operator-chosen entries, which win over an
// allowlisted variable of the same name). An entry naming HOME is dropped
// here as well as refused by ValidateSandboxEnv, so the pin holds even for a
// caller that skipped validation.
func sandboxEnv(root string, extra ...string) []string {
	env := []string{"HOME=" + root}
	havePath := false
	for _, name := range sandboxPassthrough {
		value, ok := os.LookupEnv(name)
		if !ok {
			continue
		}
		if name == "PATH" {
			havePath = value != ""
			if !havePath {
				continue
			}
		}
		env = append(env, name+"="+value)
	}
	if !havePath {
		env = append(env, "PATH="+sandboxFallbackPath)
	}
	for _, kv := range extra {
		if name, _, ok := strings.Cut(kv, "="); !ok || name == "HOME" {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// SandboxEnvRedactor is what run_command cuts out of its output for a
// sandbox carrying extra entries: each entry's value, named by its
// variable, and redact's token shapes. Nil when there are none, which is
// every sandbox but an operator-env host one, and whose output is
// returned as the command printed it. pkg/orchestrator redacts a run's
// stored transcript with the same one (a nil Redactor still cuts the
// shapes), and so should anything that streams a run's events off this
// host.
func SandboxEnvRedactor(extra []string) *redact.Redactor {
	if len(extra) == 0 {
		return nil
	}
	known := make([]redact.KnownValue, 0, len(extra))
	for _, kv := range extra {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		known = append(known, redact.KnownValue{Label: "sandbox env " + name, Value: value})
	}
	return redact.New(known)
}

func runCommandTool(root string, hide, extra []string) Tool {
	redactor := SandboxEnvRedactor(extra)
	return Tool{
		Name:        "run_command",
		Description: "Run a shell command in your assigned sandbox workspace.",
		InputSchema: runCommandInputSchema,
		Handler: func(ctx context.Context, args map[string]any) Result {
			command, ok := argString(args, "command")
			if !ok || command == "" {
				return Result{Text: "command is required", IsError: true}
			}

			bound := resolveRunCommandBound(args)
			started := time.Now()
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, bound.d)
			defer cancel()

			argv, err := commandArgv(root, hide, command)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			procgroup.Prepare(cmd)
			cmd.Dir = root
			// HOME=root, not whatever this process's own HOME is, so that
			// anything a command reads or writes there -- notably
			// ~/.gitconfig and ~/.git-credentials (see
			// ConfigureGitCredentials) -- is confined to this sandbox
			// stand-in the same way its working directory already is.
			//
			// Only safe allowlisted environment variables (sandboxPassthrough)
			// are passed through to prevent sandbox commands from inspecting
			// host environment variables to extract daemon credentials, GCP
			// service account keys, DB paths, or API tokens -- plus only
			// what the operator opted this sandbox into by name
			// (NewSandboxTools' extra).
			cmd.Env = sandboxEnv(root, extra...)
			stdout, stderr := newHeadTailBuffer(), newHeadTailBuffer()
			cmd.Stdout = stdout
			cmd.Stderr = stderr
			err = cmd.Start()
			if err == nil {
				_ = procgroup.SacrificeOnOOM(cmd.Process.Pid)
				err = cmd.Wait()
			}

			exitCode := 0
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = -1
					stderr.Write([]byte(err.Error()))
				}
			}
			// A command the deadline killed comes back as exit=-1 (an
			// *exec.ExitError for a process killed by a signal has no
			// exit status of its own), which is also what "could not
			// start at all" looks like -- so the deadline has to say
			// so itself. ctx here is the derived one, but its Err can
			// still be a *parent* deadline (the run's own wall clock)
			// that fired first, which is not this bound and would name
			// the wrong number; the elapsed check tells the two apart,
			// since a parent that fired first fired before bound.d
			// was up.
			notice := ""
			if errors.Is(ctx.Err(), context.DeadlineExceeded) && time.Since(started) >= bound.d {
				notice = bound.timedOutNotice()
			}
			text := formatRunCommandResult(exitCode, stdout.String(), stderr.String(), notice)
			if redactor != nil {
				text, _ = redactor.Text(text)
			}
			return Result{Text: text, IsError: exitCode != 0}
		},
	}
}

// linesFromContent splits content the way Python's splitlines() does: a
// trailing newline never produces a trailing empty "line", which the
// cat -n-style numbering numberedRange does depends on to avoid an
// off-by-one phantom last line. Shared with ssh_tools.go's read_file,
// which has no os.ReadFile of its own to call -- content there is
// whatever `cat` on the remote host returned.
func linesFromContent(content string) []string {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" && strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// numberedRange renders lines[offset:offset+limit] (args' "offset"/"limit",
// clamped into range) in the same cat -n-style numbering read_file's model-
// facing output always uses, capped at maxToolResultBytes: a read_file
// call with no "limit" against a 250 KB file used to spend a large part
// of a run's context in one turn. The numbering is what makes the cut
// recoverable -- the line numbers either side of the elision notice are
// exactly the "offset" and "limit" needed to read the missing part.
func numberedRange(lines []string, args map[string]any) string {
	start := 0
	if v, ok := argFloat(args, "offset"); ok {
		start = int(v)
	}
	if start < 0 {
		start = 0
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := len(lines)
	if v, ok := argFloat(args, "limit"); ok {
		end = start + int(v)
	}
	if end > len(lines) {
		end = len(lines)
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		if i > start {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%6d\t%s", i+1, lines[i])
	}
	return elideMiddle(b.String(), int(maxToolResultBytes.Load()), elisionAdviceFileLines)
}

// maxReadFileBytes is the largest file read_file and edit_file will read,
// locally or on a remote sandbox. It has to stay below execOutputLimit, so
// that a remote read under it always comes back through the runner whole:
// edit_file writes what it read back out, and a file with its middle
// elided must never be what it writes.
var maxReadFileBytes = 32 << 20

// errFileTooLarge is why a read past maxReadFileBytes was refused, and
// what to do instead.
var errFileTooLarge = fmt.Errorf("file is larger than %d bytes, more than read_file and edit_file read; "+
	"use run_command with head, tail, sed -n or grep to read part of it", maxReadFileBytes)

// readRegularFile is os.ReadFile for a path a run named, which can be
// anything: a FIFO blocks os.ReadFile forever, and /dev/zero fills memory
// until the process dies. So only a regular file is read, and only up to
// maxReadFileBytes of it.
func readRegularFile(full string) ([]byte, error) {
	data, err := readRegularFileUpTo(full, maxReadFileBytes)
	if errors.Is(err, errOverLimit) {
		return nil, errFileTooLarge
	}
	return data, err
}

// errOverLimit is readRegularFileUpTo's refusal of a file past its limit,
// which each caller words for itself.
var errOverLimit = errors.New("file is over the limit")

// readRegularFileUpTo is readRegularFile with the limit the caller's own:
// at most limit bytes, or errOverLimit.
func readRegularFileUpTo(full string, limit int) ([]byte, error) {
	info, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", full)
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errOverLimit
	}
	return data, nil
}

func readFileTool(root string) Tool {
	return Tool{
		Name:        "read_file",
		Description: "Read a file from your assigned sandbox workspace.",
		InputSchema: readFileInputSchema,
		Handler: func(_ context.Context, args map[string]any) Result {
			fp, _ := argString(args, "file_path")
			full, _, notice, err := resolveForRead(root, fp)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			data, err := readRegularFile(full)
			if err != nil {
				return Result{Text: fmt.Sprintf("Error reading %s: %v", fp, err), IsError: true}
			}
			lines := linesFromContent(string(data))
			return Result{Text: noticed(notice, numberedRange(lines, args))}
		},
		ReadBytes: hostReadBytes(root),
	}
}

func writeFileTool(root string) Tool {
	return Tool{
		Name:        "write_file",
		Description: "Write (create or overwrite) a file in your assigned sandbox workspace.",
		InputSchema: writeFileInputSchema,
		Handler: func(_ context.Context, args map[string]any) Result {
			fp, _ := argString(args, "file_path")
			content, ok := argString(args, "content")
			if !ok {
				return Result{Text: "content is required", IsError: true}
			}
			full, named, notice, err := resolveForWrite(root, fp)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return Result{Text: fmt.Sprintf("Error creating %s: %v", filepath.Dir(fp), err), IsError: true}
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				return Result{Text: fmt.Sprintf("Error writing %s: %v", fp, err), IsError: true}
			}
			return Result{Text: noticed(notice, "Wrote "+wroteName(fp, named, notice))}
		},
	}
}

func editFileTool(root string) Tool {
	return Tool{
		Name:        "edit_file",
		Description: "Replace an exact string in a file in your assigned sandbox workspace.",
		InputSchema: editFileInputSchema,
		Handler: func(_ context.Context, args map[string]any) Result {
			fp, _ := argString(args, "file_path")
			oldStr, hasOld := argString(args, "old_string")
			newStr, hasNew := argString(args, "new_string")
			if !hasOld || !hasNew {
				return Result{Text: "old_string and new_string are required", IsError: true}
			}
			// An empty old_string matches between every character in
			// the file: with replace_all it interleaves new_string
			// through the whole file ("abc" becomes "XaXbXcX"), and
			// without it the refusal counts those phantom matches and
			// reads as nonsense ("String appears 19 times in the
			// file"). Neither is what a caller that meant "create this
			// file" or "add this to it" was asking for -- found by hand
			// driving this server the way a framework does, task 244.
			if oldStr == "" {
				return Result{
					Text: "old_string must not be empty: it matches between every character in the file. " +
						"Use write_file to create or replace a file, or name the exact text this edit replaces.",
					IsError: true,
				}
			}
			replaceAll, _ := argBool(args, "replace_all")

			full, named, notice, err := resolveForRead(root, fp)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			data, err := readRegularFile(full)
			if err != nil {
				return Result{Text: fmt.Sprintf("Error reading %s: %v", fp, err), IsError: true}
			}
			content := string(data)
			count := strings.Count(content, oldStr)
			if count == 0 {
				return Result{Text: fmt.Sprintf("String not found in file: %q", echoedArgument(oldStr)), IsError: true}
			}
			if count > 1 && !replaceAll {
				return Result{
					Text: fmt.Sprintf(
						"String appears %d times in the file. Use replace_all: true to replace "+
							"every occurrence, or provide more surrounding context to uniquely "+
							"identify the instance you mean.", count),
					IsError: true,
				}
			}
			n := 1
			if replaceAll {
				n = -1
			}
			newContent := strings.Replace(content, oldStr, newStr, n)
			if err := os.WriteFile(full, []byte(newContent), 0o644); err != nil {
				return Result{Text: fmt.Sprintf("Error writing %s: %v", fp, err), IsError: true}
			}
			return Result{Text: noticed(notice, "Wrote "+wroteName(fp, named, notice))}
		},
	}
}
