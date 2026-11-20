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
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// ErrFileNotFound indicates a requested relative file path does not exist at the queried revision.
var ErrFileNotFound = errors.New("qa/checks: file not found")

// ErrBaseResolutionFailed indicates that S3 could not determine a verified merge-base
// against the target base branch. Callers must never fall back to the repository's
// root/initial commit or empty tree.
var ErrBaseResolutionFailed = errors.New("qa/checks: failed to resolve base commit")

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
	if baseHint != "" {
		if err := validateMirrorBranchName(baseHint); err != nil {
			return "", fmt.Errorf("%w: %v", ErrBaseResolutionFailed, err)
		}
	}
	var script strings.Builder
	fmt.Fprintf(&script, "cd %s && ", shellSingleQuote(w.workDir()))
	script.WriteString(`try_mb() { if git rev-parse --verify --quiet --end-of-options "$1" >/dev/null 2>&1; then mb=$(git merge-base -- "$1" HEAD 2>/dev/null) && [ -n "$mb" ] && printf '%s\n' "$mb" && return 0; fi; return 1; }; `)
	if baseHint != "" {
		qOrigin := shellSingleQuote("origin/" + baseHint)
		qLocal := shellSingleQuote(baseHint)
		qRefspec := shellSingleQuote("+refs/heads/" + baseHint + ":refs/remotes/origin/" + baseHint)
		fmt.Fprintf(&script, "try_mb %s && exit 0; ", qOrigin)
		fmt.Fprintf(&script, "try_mb %s && exit 0; ", qLocal)
		// Fetch the target base branch from origin if missing locally or shallow.
		fmt.Fprintf(&script, "git fetch --quiet --depth=64 origin %s >/dev/null 2>&1 || true; ", qRefspec)
		fmt.Fprintf(&script, "try_mb %s && exit 0; ", qOrigin)
		fmt.Fprintf(&script, "(git fetch --quiet --deepen=256 origin %s >/dev/null 2>&1 || true); (git fetch --quiet --deepen=256 origin HEAD >/dev/null 2>&1 || true); ", qRefspec)
		fmt.Fprintf(&script, "try_mb %s && exit 0; ", qOrigin)
		fmt.Fprintf(&script, "(git fetch --quiet --unshallow origin %s >/dev/null 2>&1 || git fetch --quiet origin %s >/dev/null 2>&1 || true); (git fetch --quiet --unshallow origin >/dev/null 2>&1 || true); ",
			qRefspec, qRefspec)
		fmt.Fprintf(&script, "try_mb %s && exit 0; ", qOrigin)
		script.WriteString("echo 'could not resolve merge-base against target base branch' >&2; exit 1")
	} else {
		script.WriteString("try_mb origin/HEAD && exit 0; ")
		script.WriteString("try_mb origin/main && exit 0; ")
		script.WriteString("try_mb origin/master && exit 0; ")
		// Resolve the remote default branch via git without relying on REST or root-commit fallbacks.
		script.WriteString(`defref=$(git ls-remote --symref origin HEAD 2>/dev/null | awk '$1 == "ref:" && $3 == "HEAD" { sub("^refs/heads/", "", $2); print $2; exit }'); `)
		script.WriteString(`if [ -n "$defref" ]; then `)
		script.WriteString(`git fetch --quiet --depth=64 origin "+refs/heads/$defref:refs/remotes/origin/$defref" >/dev/null 2>&1 || true; `)
		script.WriteString(`try_mb "refs/remotes/origin/$defref" && exit 0; `)
		script.WriteString(`(git fetch --quiet --deepen=256 origin "+refs/heads/$defref:refs/remotes/origin/$defref" >/dev/null 2>&1 || true); (git fetch --quiet --deepen=256 origin HEAD >/dev/null 2>&1 || true); `)
		script.WriteString(`try_mb "refs/remotes/origin/$defref" && exit 0; `)
		script.WriteString(`(git fetch --quiet --unshallow origin "+refs/heads/$defref:refs/remotes/origin/$defref" >/dev/null 2>&1 || git fetch --quiet origin "+refs/heads/$defref:refs/remotes/origin/$defref" >/dev/null 2>&1 || true); (git fetch --quiet --unshallow origin >/dev/null 2>&1 || true); `)
		script.WriteString(`try_mb "refs/remotes/origin/$defref" && exit 0; `)
		script.WriteString(`fi; `)
		script.WriteString(`git fetch --quiet --depth=64 origin "+HEAD:refs/remotes/origin/__qa_default_head__" >/dev/null 2>&1 || true; `)
		script.WriteString(`try_mb refs/remotes/origin/__qa_default_head__ && exit 0; `)
		script.WriteString(`(git fetch --quiet --deepen=256 origin "+HEAD:refs/remotes/origin/__qa_default_head__" >/dev/null 2>&1 || true); (git fetch --quiet --deepen=256 origin HEAD >/dev/null 2>&1 || true); `)
		script.WriteString(`try_mb refs/remotes/origin/__qa_default_head__ && exit 0; `)
		script.WriteString(`(git fetch --quiet --unshallow origin "+HEAD:refs/remotes/origin/__qa_default_head__" >/dev/null 2>&1 || git fetch --quiet origin "+HEAD:refs/remotes/origin/__qa_default_head__" >/dev/null 2>&1 || true); (git fetch --quiet --unshallow origin >/dev/null 2>&1 || true); `)
		script.WriteString(`try_mb refs/remotes/origin/__qa_default_head__ && exit 0; `)
		script.WriteString("echo 'could not resolve merge-base against remote default branch' >&2; exit 1")
	}

	out, isErr, err := w.RunCommand(ctx, script.String())
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrBaseResolutionFailed, err)
	}
	ref := strings.TrimSpace(firstNonEmptyLine(out))
	if isErr || ref == "" || !isLikelyGitSHA(ref) {
		return "", fmt.Errorf("%w: resolving base commit in sandbox: %s", ErrBaseResolutionFailed, strings.TrimSpace(out))
	}
	w.mu.Lock()
	w.resolvedRef = ref
	w.mu.Unlock()
	return ref, nil
}

// BaseSHA resolves and returns the verified merge-base commit SHA for the sandbox checkout.
func (w *SandboxWorkspace) BaseSHA(ctx context.Context) (string, error) {
	return w.resolveBaseRef(ctx)
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

// resolveRemoteDefaultBranch discovers remoteURL's default branch via
// `git ls-remote --symref <cleanURL> HEAD` without relying on forge REST APIs.
func resolveRemoteDefaultBranch(ctx context.Context, bareDir, basicAuth, cleanURL, bearerToken, urlPassword string) (branchName, headSHA string, err error) {
	out, lsErr := runBareGit(ctx, bareDir, basicAuth, "ls-remote", "--symref", "--end-of-options", cleanURL, "HEAD")
	if lsErr != nil {
		return "", "", fmt.Errorf("%w: git ls-remote HEAD failed: %v (%s)", ErrBaseResolutionFailed, lsErr, redactMirrorOutput(out, bearerToken, urlPassword))
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "ref:") {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[2] == "HEAD" && strings.HasPrefix(fields[1], "refs/heads/") {
				branchName = strings.TrimPrefix(fields[1], "refs/heads/")
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "HEAD" && isLikelyGitSHA(fields[0]) {
			headSHA = fields[0]
		}
	}
	if branchName != "" && headSHA == "" {
		return "", "", fmt.Errorf("%w: remote HEAD points to unborn branch %q", ErrBaseResolutionFailed, branchName)
	}
	if branchName == "" && headSHA == "" {
		return "", "", fmt.Errorf("%w: remote %s did not advertise HEAD", ErrBaseResolutionFailed, mirrorStableURL(cleanURL))
	}
	return branchName, headSHA, nil
}

func isLikelyGitSHA(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func validateMirrorBranchName(branch string) error {
	if branch == "" {
		return fmt.Errorf("empty branch name")
	}
	if strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	if strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.ContainsAny(branch, ":~^?*[\\ \t\r\n\x00") {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	for _, r := range branch {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid control character in branch name %q", branch)
		}
	}
	return nil
}

// OpenMirrorWorkspace fetches baseBranch and headBranch into a reusable bare git mirror under
// cacheRoot and returns a MirrorWorkspace pinned to merge-base(baseBranch, headBranch)..headSHA.
// When baseBranch is empty, it resolves the remote's default branch via git (never REST and never
// falling back to the repository's initial/root commit).
func OpenMirrorWorkspace(ctx context.Context, cacheRoot, remoteURL, bearerToken, baseBranch, headBranch string) (*MirrorWorkspace, error) {
	headBranch = strings.TrimSpace(headBranch)
	baseBranch = strings.TrimSpace(baseBranch)
	if strings.TrimSpace(cacheRoot) == "" || strings.TrimSpace(remoteURL) == "" || headBranch == "" {
		return nil, fmt.Errorf("qa/checks: cacheRoot, remoteURL, and headBranch are required for mirror workspace")
	}
	if err := validateMirrorBranchName(headBranch); err != nil {
		return nil, fmt.Errorf("qa/checks: %w", err)
	}
	if baseBranch != "" {
		if err := validateMirrorBranchName(baseBranch); err != nil {
			return nil, fmt.Errorf("qa/checks: %w", err)
		}
	}
	cleanURL, basicAuth, urlPassword := stripURLCredentials(remoteURL, bearerToken)
	mirrorKey := sanitizePathKey(mirrorStableURL(cleanURL))
	bareDir := filepath.Join(cacheRoot, mirrorKey+".git")

	mirrorMu.Lock()
	defer mirrorMu.Unlock()

	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		return nil, fmt.Errorf("qa/checks: creating mirror cache root: %w", err)
	}
	if _, err := os.Stat(filepath.Join(bareDir, "HEAD")); err != nil {
		if initOut, initErr := runBareGit(ctx, "", "", "init", "--bare", "--", bareDir); initErr != nil {
			return nil, fmt.Errorf("qa/checks: git init --bare: %v (%s)", initErr, redactMirrorOutput(initOut, bearerToken, urlPassword))
		}
	}

	var baseRefspec, baseLocalRef, baseDesc string
	if baseBranch == "" {
		defBranch, defSHA, defErr := resolveRemoteDefaultBranch(ctx, bareDir, basicAuth, cleanURL, bearerToken, urlPassword)
		if defErr != nil {
			return nil, defErr
		}
		if defBranch != "" {
			if err := validateMirrorBranchName(defBranch); err != nil {
				return nil, fmt.Errorf("%w: remote advertised invalid default branch: %v", ErrBaseResolutionFailed, err)
			}
			baseBranch = defBranch
			baseRefspec = fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", baseBranch, baseBranch)
			baseLocalRef = "refs/remotes/origin/" + baseBranch
			baseDesc = "origin/" + baseBranch
		} else if defSHA != "" {
			baseRefspec = "+HEAD:refs/remotes/origin/__qa_default_head__"
			baseLocalRef = "refs/remotes/origin/__qa_default_head__"
			baseDesc = "origin/HEAD"
		}
	} else {
		baseRefspec = fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", baseBranch, baseBranch)
		baseLocalRef = "refs/remotes/origin/" + baseBranch
		baseDesc = "origin/" + baseBranch
	}

	headRefspec := fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", headBranch, headBranch)
	refspecs := []string{headRefspec}
	if baseRefspec != "" && baseRefspec != headRefspec {
		refspecs = append(refspecs, baseRefspec)
	}

	fetchArgs := append([]string{"fetch", "--quiet", "--depth=64", "--end-of-options", cleanURL}, refspecs...)
	if fetchOut, fetchErr := runBareGit(ctx, bareDir, basicAuth, fetchArgs...); fetchErr != nil {
		redacted := redactMirrorOutput(fetchOut, bearerToken, urlPassword)
		if baseBranch != "" && strings.Contains(redacted, "refs/heads/"+baseBranch) {
			return nil, fmt.Errorf("%w: git fetch base %s failed: %v (%s)", ErrBaseResolutionFailed, baseDesc, fetchErr, redacted)
		}
		return nil, fmt.Errorf("qa/checks: git fetch mirror: %v (%s)", fetchErr, redacted)
	}

	headOut, err := runBareGit(ctx, bareDir, "", "rev-parse", "--verify", "--end-of-options", "refs/remotes/origin/"+headBranch)
	if err != nil {
		return nil, fmt.Errorf("qa/checks: resolving mirror head %s: %w", headBranch, err)
	}
	headSHA := strings.TrimSpace(headOut)

	if _, err := runBareGit(ctx, bareDir, "", "rev-parse", "--verify", "--end-of-options", baseLocalRef); err != nil {
		return nil, fmt.Errorf("%w: base ref %s missing after fetch: %v", ErrBaseResolutionFailed, baseDesc, err)
	}

	baseSHA := ""
	mbOut, mbErr := runBareGit(ctx, bareDir, "", "merge-base", "--end-of-options", baseLocalRef, headSHA)
	if mbErr == nil {
		baseSHA = strings.TrimSpace(mbOut)
	} else {
		// Shallow 64-commit horizon missed the fork point: deepen and retry merge-base.
		deepArgs := append([]string{"fetch", "--quiet", "--deepen=256", "--end-of-options", cleanURL}, refspecs...)
		_, _ = runBareGit(ctx, bareDir, basicAuth, deepArgs...)
		if mbOut2, mbErr2 := runBareGit(ctx, bareDir, "", "merge-base", "--end-of-options", baseLocalRef, headSHA); mbErr2 == nil {
			baseSHA = strings.TrimSpace(mbOut2)
		} else {
			unshallowArgs := append([]string{"fetch", "--quiet", "--unshallow", "--end-of-options", cleanURL}, refspecs...)
			if _, uErr := runBareGit(ctx, bareDir, basicAuth, unshallowArgs...); uErr != nil {
				// Mirror may already be complete (not shallow); run a full fetch instead.
				fullArgs := append([]string{"fetch", "--quiet", "--end-of-options", cleanURL}, refspecs...)
				_, _ = runBareGit(ctx, bareDir, basicAuth, fullArgs...)
			}
			if mbOut3, mbErr3 := runBareGit(ctx, bareDir, "", "merge-base", "--end-of-options", baseLocalRef, headSHA); mbErr3 == nil {
				baseSHA = strings.TrimSpace(mbOut3)
			} else {
				return nil, fmt.Errorf("%w: could not compute merge-base between %s and %s: %v", ErrBaseResolutionFailed, baseDesc, headBranch, mbErr3)
			}
		}
	}
	if !isLikelyGitSHA(baseSHA) {
		return nil, fmt.Errorf("%w: invalid merge-base %q between %s and %s", ErrBaseResolutionFailed, baseSHA, baseDesc, headBranch)
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

func runBareGit(ctx context.Context, bareDir, basicAuth string, args ...string) (string, error) {
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
	if basicAuth != "" && strings.HasPrefix(extractURLArg(args), "http") {
		env = append(env,
			"GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=safe.bareRepository",
			"GIT_CONFIG_VALUE_0=all",
			"GIT_CONFIG_KEY_1=http.extraHeader",
			"GIT_CONFIG_VALUE_1=Authorization: Basic "+basicAuth,
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

var urlUserinfoRe = regexp.MustCompile(`(https?://)[^/\s@]+@`)

func stripURLCredentials(remoteURL, bearerToken string) (cleanURL, basicAuth, urlPassword string) {
	trimmed := strings.TrimSpace(remoteURL)
	cleanURL = trimmed
	if u, err := url.Parse(trimmed); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User != nil {
		user := u.User.Username()
		pass, hasPass := u.User.Password()
		urlPassword = pass
		cred := user
		if hasPass {
			cred += ":" + pass
		}
		if bearerToken == "" && cred != "" {
			basicAuth = base64.StdEncoding.EncodeToString([]byte(cred))
		}
		u.User = nil
		cleanURL = u.String()
	}
	if bearerToken != "" {
		basicAuth = base64.StdEncoding.EncodeToString([]byte("x-access-token:" + bearerToken))
	}
	return cleanURL, basicAuth, urlPassword
}

func mirrorStableURL(remoteURL string) string {
	trimmed := strings.TrimSpace(remoteURL)
	if u, err := url.Parse(trimmed); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		u.User = nil
		hostPort := u.Host
		repoPath := strings.TrimPrefix(u.Path, "/")
		if repoPath != "" && (strings.HasPrefix(hostPort, "127.0.0.1:") || strings.HasPrefix(hostPort, "localhost:") || strings.HasPrefix(hostPort, "[::1]:")) {
			return "gitproxy/" + repoPath
		}
		return u.String()
	}
	return trimmed
}

func redactMirrorOutput(out string, secrets ...string) string {
	if out == "" {
		return out
	}
	redacted := urlUserinfoRe.ReplaceAllString(out, "${1}[redacted]@")
	for _, s := range secrets {
		if s == "" {
			continue
		}
		redacted = strings.ReplaceAll(redacted, s, "[redacted]")
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + s))
		redacted = strings.ReplaceAll(redacted, basic, "[redacted]")
	}
	return redacted
}
