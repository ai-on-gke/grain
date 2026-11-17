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

package checks

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// ErrFileNotFound indicates a requested relative file path does not exist at the queried revision.
var ErrFileNotFound = errors.New("qa/checks: file not found")

// CallSite records a 1-based line match for an exported symbol across the workspace.
type CallSite struct {
	File    string
	Line    int
	Snippet string
}

// WorkspaceReader abstracts fast, rate-limit-free git diff/file/grep access across both
// in-sandbox MCP calls and daemon-side shallow bare mirrors.
type WorkspaceReader interface {
	HeadSHA(ctx context.Context) (string, error)
	BaseHeadDiff(ctx context.Context) (string, error)
	ReadFileAtHead(ctx context.Context, relPath string) ([]byte, error)
	ReadFileAtBase(ctx context.Context, relPath string) ([]byte, error)
	GrepSymbolSites(ctx context.Context, symbol string) ([]CallSite, error)
}

// InMemoryWorkspace implements WorkspaceReader over maps for deterministic unit testing.
type InMemoryWorkspace struct {
	Head      string
	Diff      string
	HeadFiles map[string]string
	BaseFiles map[string]string
}

func (w *InMemoryWorkspace) HeadSHA(context.Context) (string, error) {
	if w.Head != "" {
		return w.Head, nil
	}
	return "deadbeef00000000000000000000000000000001", nil
}

func (w *InMemoryWorkspace) BaseHeadDiff(context.Context) (string, error) {
	return w.Diff, nil
}

func (w *InMemoryWorkspace) ReadFileAtHead(_ context.Context, relPath string) ([]byte, error) {
	relPath = cleanRelPath(relPath)
	if v, ok := w.HeadFiles[relPath]; ok {
		return []byte(v), nil
	}
	return nil, ErrFileNotFound
}

func (w *InMemoryWorkspace) ReadFileAtBase(_ context.Context, relPath string) ([]byte, error) {
	relPath = cleanRelPath(relPath)
	if v, ok := w.BaseFiles[relPath]; ok {
		return []byte(v), nil
	}
	return nil, ErrFileNotFound
}

func (w *InMemoryWorkspace) GrepSymbolSites(_ context.Context, symbol string) ([]CallSite, error) {
	if strings.TrimSpace(symbol) == "" {
		return nil, nil
	}
	var out []CallSite
	for file, content := range w.HeadFiles {
		lines := strings.Split(content, "\n")
		for idx, line := range lines {
			if containsIdentifierToken(line, symbol) {
				out = append(out, CallSite{
					File:    cleanRelPath(file),
					Line:    idx + 1,
					Snippet: strings.TrimSpace(line),
				})
			}
		}
	}
	return out, nil
}

// CommandRunner executes a shell command inside a sandbox workspace and returns combined output.
type CommandRunner func(ctx context.Context, command string) (output string, isErr bool, err error)

// FileReaderFunc reads a file inside a sandbox workspace up to limit bytes.
type FileReaderFunc func(ctx context.Context, relOrWorkPath string, limit int) ([]byte, error)

// SandboxWorkspace implements WorkspaceReader inside `grain mcpserver` over a live checkout.
type SandboxWorkspace struct {
	CheckoutDir string
	BaseBranch  string
	ReadFile    FileReaderFunc
	RunCommand  CommandRunner

	mu          sync.Mutex
	resolvedRef string
}

const maxQAFileReadBytes = 2 << 20 // 2 MiB

func (w *SandboxWorkspace) workDir() string {
	d := strings.TrimSpace(w.CheckoutDir)
	if d == "" {
		return "work"
	}
	return d
}

func (w *SandboxWorkspace) resolveBaseRef(ctx context.Context) (string, error) {
	w.mu.Lock()
	if w.resolvedRef != "" {
		ref := w.resolvedRef
		w.mu.Unlock()
		return ref, nil
	}
	w.mu.Unlock()

	if w.RunCommand == nil {
		return "", fmt.Errorf("qa/checks: sandbox workspace has no command runner")
	}
	baseHint := strings.TrimSpace(w.BaseBranch)
	var script strings.Builder
	fmt.Fprintf(&script, "cd %s && ", shellSingleQuote(w.workDir()))
	if baseHint != "" {
		fmt.Fprintf(&script, "if git rev-parse --verify --quiet %s >/dev/null 2>&1; then git merge-base %s HEAD; exit 0; fi; ",
			shellSingleQuote("origin/"+baseHint), shellSingleQuote("origin/"+baseHint))
		fmt.Fprintf(&script, "if git rev-parse --verify --quiet %s >/dev/null 2>&1; then git merge-base %s HEAD; exit 0; fi; ",
			shellSingleQuote(baseHint), shellSingleQuote(baseHint))
	}
	script.WriteString("if git rev-parse --verify --quiet origin/HEAD >/dev/null 2>&1; then git merge-base origin/HEAD HEAD; exit 0; fi; ")
	script.WriteString("if git rev-parse --verify --quiet origin/main >/dev/null 2>&1; then git merge-base origin/main HEAD; exit 0; fi; ")
	script.WriteString("if git rev-parse --verify --quiet origin/master >/dev/null 2>&1; then git merge-base origin/master HEAD; exit 0; fi; ")
	script.WriteString("git rev-list --max-parents=0 HEAD | head -n 1")

	out, isErr, err := w.RunCommand(ctx, script.String())
	if err != nil {
		return "", err
	}
	ref := strings.TrimSpace(firstNonEmptyLine(out))
	if isErr || ref == "" {
		return "", fmt.Errorf("qa/checks: resolving base commit in sandbox: %s", strings.TrimSpace(out))
	}
	w.mu.Lock()
	w.resolvedRef = ref
	w.mu.Unlock()
	return ref, nil
}

func (w *SandboxWorkspace) HeadSHA(ctx context.Context) (string, error) {
	if w.RunCommand == nil {
		return "", fmt.Errorf("qa/checks: sandbox workspace has no command runner")
	}
	cmd := fmt.Sprintf("cd %s && git rev-parse HEAD", shellSingleQuote(w.workDir()))
	out, isErr, err := w.RunCommand(ctx, cmd)
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(firstNonEmptyLine(out))
	if isErr || sha == "" {
		return "", fmt.Errorf("qa/checks: reading HEAD SHA in sandbox: %s", strings.TrimSpace(out))
	}
	return sha, nil
}

func (w *SandboxWorkspace) BaseHeadDiff(ctx context.Context) (string, error) {
	baseRef, err := w.resolveBaseRef(ctx)
	if err != nil {
		return "", err
	}
	cmd := fmt.Sprintf("cd %s && git diff --find-renames --unified=3 --end-of-options %s..HEAD",
		shellSingleQuote(w.workDir()), shellSingleQuote(baseRef))
	out, isErr, err := w.RunCommand(ctx, cmd)
	if err != nil {
		return "", err
	}
	if isErr {
		return "", fmt.Errorf("qa/checks: running git diff in sandbox: %s", strings.TrimSpace(out))
	}
	return out, nil
}

func (w *SandboxWorkspace) ReadFileAtHead(ctx context.Context, relPath string) ([]byte, error) {
	relPath = cleanRelPath(relPath)
	if relPath == "" {
		return nil, ErrFileNotFound
	}
	if w.RunCommand != nil {
		cmd := fmt.Sprintf("cd %s && git show --end-of-options %s", shellSingleQuote(w.workDir()), shellSingleQuote("HEAD:"+relPath))
		out, isErr, err := w.RunCommand(ctx, cmd)
		if err != nil {
			return nil, err
		}
		if isErr {
			return nil, ErrFileNotFound
		}
		if len(out) > maxQAFileReadBytes {
			return []byte(out[:maxQAFileReadBytes]), nil
		}
		return []byte(out), nil
	}
	if w.ReadFile != nil {
		joined := path.Join(w.workDir(), relPath)
		b, err := w.ReadFile(ctx, joined, maxQAFileReadBytes)
		if err == nil {
			return b, nil
		}
	}
	return nil, ErrFileNotFound
}

func (w *SandboxWorkspace) ReadFileAtBase(ctx context.Context, relPath string) ([]byte, error) {
	relPath = cleanRelPath(relPath)
	if relPath == "" {
		return nil, ErrFileNotFound
	}
	baseRef, err := w.resolveBaseRef(ctx)
	if err != nil {
		return nil, err
	}
	cmd := fmt.Sprintf("cd %s && git show --end-of-options %s", shellSingleQuote(w.workDir()), shellSingleQuote(baseRef+":"+relPath))
	out, isErr, err := w.RunCommand(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if isErr {
		return nil, ErrFileNotFound
	}
	return []byte(out), nil
}

func (w *SandboxWorkspace) GrepSymbolSites(ctx context.Context, symbol string) ([]CallSite, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" || w.RunCommand == nil {
		return nil, nil
	}
	cmd := fmt.Sprintf("cd %s && (git grep -n -F -w -e %s HEAD -- '*.go' '*.ts' || true)",
		shellSingleQuote(w.workDir()), shellSingleQuote(symbol))
	out, _, err := w.RunCommand(ctx, cmd)
	if err != nil {
		return nil, err
	}
	return parseGitGrepOutput(out, "HEAD:"), nil
}

// goModuleLocator is an optional WorkspaceReader capability: return the directory and contents of
// the nearest go.mod at HEAD for dir in one round trip (modDir "" means the repository root).
// ok is false when no go.mod exists on the path. Readers without it fall back to walking up with
// ReadFileAtHead, one call per directory level.
type goModuleLocator interface {
	NearestGoModAtHead(ctx context.Context, dir string) (modDir string, content []byte, ok bool, err error)
}

// goModCandidates lists dir/go.mod, parent/go.mod, ..., go.mod (deepest first).
func goModCandidates(dir string) []string {
	dir = cleanDir(dir)
	var out []string
	for {
		if dir == "" {
			return append(out, "go.mod")
		}
		out = append(out, dir+"/go.mod")
		dir = parentDir(dir)
	}
}

// NearestGoModAtHead implements goModuleLocator with a single sandbox command.
func (w *SandboxWorkspace) NearestGoModAtHead(ctx context.Context, dir string) (string, []byte, bool, error) {
	if w.RunCommand == nil {
		return "", nil, false, fmt.Errorf("qa/checks: sandbox workspace has no command runner")
	}
	var quoted []string
	for _, c := range goModCandidates(dir) {
		quoted = append(quoted, shellSingleQuote(c))
	}
	cmd := fmt.Sprintf("cd %s && for p in %s; do if git cat-file -e \"HEAD:$p\" 2>/dev/null; then printf '%%s\\n' \"$p\"; git show \"HEAD:$p\"; exit 0; fi; done; exit 1",
		shellSingleQuote(w.workDir()), strings.Join(quoted, " "))
	out, isErr, err := w.RunCommand(ctx, cmd)
	if err != nil {
		return "", nil, false, err
	}
	if isErr {
		return "", nil, false, nil
	}
	first, rest, _ := strings.Cut(out, "\n")
	modDir := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(first), "go.mod"), "/")
	return modDir, []byte(rest), true, nil
}

// MirrorWorkspace implements WorkspaceReader over a shallow bare git mirror on the controller
// (`<cacheRoot>/<host>/<owner>/<repo>.git`), avoiding per-file GitHub REST calls during salvage
// and control-arm shadow checks.
type MirrorWorkspace struct {
	BareDir    string
	BaseSHA    string
	Head       string
	HeadBranch string
}

var mirrorMu sync.Mutex

// OpenMirrorWorkspace fetches baseBranch and headBranch into a reusable bare git mirror under
// cacheRoot and returns a MirrorWorkspace pinned to merge-base(baseBranch, headBranch)..headSHA.
func OpenMirrorWorkspace(ctx context.Context, cacheRoot, remoteURL, bearerToken, baseBranch, headBranch string) (*MirrorWorkspace, error) {
	headBranch = strings.TrimSpace(headBranch)
	baseBranch = strings.TrimSpace(baseBranch)
	if strings.TrimSpace(cacheRoot) == "" || strings.TrimSpace(remoteURL) == "" || headBranch == "" {
		return nil, fmt.Errorf("qa/checks: cacheRoot, remoteURL, and headBranch are required for mirror workspace")
	}
	if strings.HasPrefix(headBranch, "-") || strings.HasPrefix(baseBranch, "-") {
		return nil, fmt.Errorf("qa/checks: invalid branch name starting with '-'")
	}
	mirrorKey := sanitizePathKey(remoteURL)
	bareDir := filepath.Join(cacheRoot, mirrorKey+".git")

	mirrorMu.Lock()
	defer mirrorMu.Unlock()

	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		return nil, fmt.Errorf("qa/checks: creating mirror cache root: %w", err)
	}
	if _, err := os.Stat(filepath.Join(bareDir, "HEAD")); err != nil {
		if initOut, initErr := runBareGit(ctx, "", "", "init", "--bare", "--", bareDir); initErr != nil {
			return nil, fmt.Errorf("qa/checks: git init --bare: %v (%s)", initErr, initOut)
		}
	}

	refspecs := []string{
		fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", headBranch, headBranch),
	}
	if baseBranch != "" {
		refspecs = append(refspecs, fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", baseBranch, baseBranch))
	}
	fetchArgs := append([]string{"fetch", "--quiet", "--depth=64", "--end-of-options", remoteURL}, refspecs...)
	if fetchOut, fetchErr := runBareGit(ctx, bareDir, bearerToken, fetchArgs...); fetchErr != nil {
		return nil, fmt.Errorf("qa/checks: git fetch mirror: %v (%s)", fetchErr, fetchOut)
	}

	headOut, err := runBareGit(ctx, bareDir, "", "rev-parse", "--verify", "refs/remotes/origin/"+headBranch)
	if err != nil {
		return nil, fmt.Errorf("qa/checks: resolving mirror head %s: %w", headBranch, err)
	}
	headSHA := strings.TrimSpace(headOut)

	baseSHA := ""
	if baseBranch != "" {
		if mbOut, mbErr := runBareGit(ctx, bareDir, "", "merge-base", "--end-of-options", "refs/remotes/origin/"+baseBranch, headSHA); mbErr == nil {
			baseSHA = strings.TrimSpace(mbOut)
		} else {
			// Shallow 64-commit horizon missed the fork point: deepen and retry merge-base before falling back.
			deepArgs := append([]string{"fetch", "--quiet", "--deepen=256", "--end-of-options", remoteURL}, refspecs...)
			_, _ = runBareGit(ctx, bareDir, bearerToken, deepArgs...)
			if mbOut2, mbErr2 := runBareGit(ctx, bareDir, "", "merge-base", "--end-of-options", "refs/remotes/origin/"+baseBranch, headSHA); mbErr2 == nil {
				baseSHA = strings.TrimSpace(mbOut2)
			} else {
				unshallowArgs := append([]string{"fetch", "--quiet", "--unshallow", "--end-of-options", remoteURL}, refspecs...)
				_, _ = runBareGit(ctx, bareDir, bearerToken, unshallowArgs...)
				if mbOut3, mbErr3 := runBareGit(ctx, bareDir, "", "merge-base", "--end-of-options", "refs/remotes/origin/"+baseBranch, headSHA); mbErr3 == nil {
					baseSHA = strings.TrimSpace(mbOut3)
				} else if bOut, bErr := runBareGit(ctx, bareDir, "", "rev-parse", "--verify", "refs/remotes/origin/"+baseBranch); bErr == nil {
					baseSHA = strings.TrimSpace(bOut)
				}
			}
		}
	}
	if baseSHA == "" {
		if rootOut, rErr := runBareGit(ctx, bareDir, "", "rev-list", "--max-parents=0", "--end-of-options", headSHA); rErr == nil {
			baseSHA = strings.TrimSpace(firstNonEmptyLine(rootOut))
		}
	}
	if baseSHA == "" {
		baseSHA = headSHA
	}
	return &MirrorWorkspace{
		BareDir:    bareDir,
		BaseSHA:    baseSHA,
		Head:       headSHA,
		HeadBranch: headBranch,
	}, nil
}

func (m *MirrorWorkspace) HeadSHA(context.Context) (string, error) {
	return m.Head, nil
}

func (m *MirrorWorkspace) BaseHeadDiff(ctx context.Context) (string, error) {
	return runBareGit(ctx, m.BareDir, "", "diff", "--find-renames", "--unified=3", "--end-of-options", m.BaseSHA, m.Head)
}

func (m *MirrorWorkspace) ReadFileAtHead(ctx context.Context, relPath string) ([]byte, error) {
	relPath = cleanRelPath(relPath)
	if relPath == "" {
		return nil, ErrFileNotFound
	}
	out, err := runBareGit(ctx, m.BareDir, "", "show", "--end-of-options", m.Head+":"+relPath)
	if err != nil {
		return nil, ErrFileNotFound
	}
	return []byte(out), nil
}

func (m *MirrorWorkspace) ReadFileAtBase(ctx context.Context, relPath string) ([]byte, error) {
	relPath = cleanRelPath(relPath)
	if relPath == "" {
		return nil, ErrFileNotFound
	}
	out, err := runBareGit(ctx, m.BareDir, "", "show", "--end-of-options", m.BaseSHA+":"+relPath)
	if err != nil {
		return nil, ErrFileNotFound
	}
	return []byte(out), nil
}

func (m *MirrorWorkspace) GrepSymbolSites(ctx context.Context, symbol string) ([]CallSite, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" || m.HeadBranch == "" {
		return nil, nil
	}
	out, err := runBareGit(ctx, m.BareDir, "", "grep", "-n", "-F", "-w", "-e"+symbol, "refs/remotes/origin/"+m.HeadBranch, "--", "*.go", "*.ts")
	if err != nil {
		// git grep exits 1 when 0 lines match.
		return nil, nil
	}
	return parseGitGrepOutput(out, "refs/remotes/origin/"+m.HeadBranch+":"), nil
}

// NearestGoModAtHead implements goModuleLocator with one ls-tree plus one show.
func (m *MirrorWorkspace) NearestGoModAtHead(ctx context.Context, dir string) (string, []byte, bool, error) {
	cands := goModCandidates(dir)
	out, err := runBareGit(ctx, m.BareDir, "", append([]string{"ls-tree", "--name-only", "--end-of-options", m.Head, "--"}, cands...)...)
	if err != nil {
		return "", nil, false, nil
	}
	present := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		present[strings.TrimSpace(l)] = true
	}
	for _, c := range cands {
		if !present[c] {
			continue
		}
		content, sErr := runBareGit(ctx, m.BareDir, "", "show", "--end-of-options", m.Head+":"+c)
		if sErr != nil {
			return "", nil, false, nil
		}
		return strings.TrimSuffix(strings.TrimSuffix(c, "go.mod"), "/"), []byte(content), true, nil
	}
	return "", nil, false, nil
}

func runBareGit(ctx context.Context, bareDir, bearerToken string, args ...string) (string, error) {
	var gitArgs []string
	if bareDir != "" {
		gitArgs = append(gitArgs, "--git-dir="+bareDir)
	}
	gitArgs = append(gitArgs, args...)

	env := make([]string, 0, len(os.Environ())+8)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_CONFIG_COUNT=") ||
			strings.HasPrefix(kv, "GIT_CONFIG_KEY_") ||
			strings.HasPrefix(kv, "GIT_CONFIG_VALUE_") ||
			strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0")
	if bearerToken != "" && strings.HasPrefix(extractURLArg(args), "http") {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + bearerToken))
		env = append(env,
			"GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=safe.bareRepository",
			"GIT_CONFIG_VALUE_0=all",
			"GIT_CONFIG_KEY_1=http.extraHeader",
			"GIT_CONFIG_VALUE_1=Authorization: Basic "+basic,
		)
	} else {
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=safe.bareRepository",
			"GIT_CONFIG_VALUE_0=all",
		)
	}

	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func extractURLArg(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://") || strings.HasPrefix(a, "file://") {
			return a
		}
	}
	return ""
}

func parseGitGrepOutput(raw, trimRevPrefix string) []CallSite {
	var out []CallSite
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if trimRevPrefix != "" && strings.HasPrefix(line, trimRevPrefix) {
			line = strings.TrimPrefix(line, trimRevPrefix)
		}
		c1 := strings.IndexByte(line, ':')
		if c1 <= 0 {
			continue
		}
		rest := line[c1+1:]
		c2 := strings.IndexByte(rest, ':')
		if c2 <= 0 {
			continue
		}
		ln, err := strconv.Atoi(rest[:c2])
		if err != nil || ln <= 0 {
			continue
		}
		out = append(out, CallSite{
			File:    cleanRelPath(line[:c1]),
			Line:    ln,
			Snippet: strings.TrimSpace(rest[c2+1:]),
		})
	}
	return out
}

func containsIdentifierToken(line, ident string) bool {
	idx := 0
	for {
		pos := strings.Index(line[idx:], ident)
		if pos < 0 {
			return false
		}
		start := idx + pos
		end := start + len(ident)
		leftOK := start == 0 || !isIdentRune(line[start-1])
		rightOK := end == len(line) || !isIdentRune(line[end])
		if leftOK && rightOK {
			return true
		}
		idx = start + 1
	}
}

func isIdentRune(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func firstNonEmptyLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return ""
}

func sanitizePathKey(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if len(out) > 120 {
		out = out[len(out)-120:]
	}
	return out
}
