package namespace

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/redact"
	"github.com/bwsalmon/grain/pkg/safepath"
)

const (
	// DefaultMaxResponseBytes is the default per-response ceiling on /fs/
	// responses, sized from the CI artifact ceiling so large raw gotest.log
	// files stream cleanly while protecting the daemon.
	DefaultMaxResponseBytes int64 = 64 << 20 // 64 MiB

	// DefaultGrepMaxMatches is the default number of matching lines ?grep=
	// returns when the caller does not pass ?max=N.
	DefaultGrepMaxMatches = 100

	// MaxGrepMaxMatches is the upper bound a caller may request via ?max=N.
	MaxGrepMaxMatches = 10000

	// MaxGrepContextLines bounds ?context=N.
	MaxGrepContextLines = 50

	// DefaultFollowPollInterval is how often ?follow=1 polls a growing CI log.
	DefaultFollowPollInterval = 250 * time.Millisecond

	// DefaultFollowMaxDuration bounds a single ?follow=1 request so a dropped
	// client cannot hold a goroutine forever.
	DefaultFollowMaxDuration = 15 * time.Minute

	// maxWalkFiles bounds how many files a single directory ?glob= or ?grep=
	// inspects per request to keep store and filesystem work bounded.
	maxWalkFiles = 2000

	// maxCIRunsListed bounds how many runs a ci://<owner>/<repo> directory
	// listing fetches from the CI run store in one request.
	maxCIRunsListed = 200
)

var (
	ErrNotFound          = errors.New("not found")
	ErrForbidden         = errors.New("forbidden")
	ErrBadRequest        = errors.New("bad request")
	ErrUnsupportedScheme = errors.New("unsupported namespace scheme")
)

// Scope is the caller's authorization scope for reading namespace roots.
type Scope struct {
	TaskID            int64
	TaskIDStr         string
	RunID             string
	TaskRepo          string
	TaskBranch        string
	TaskBase          string
	ConfigRepo        string
	MayReadCI         bool
	MayReadAllCI      bool
	AllowedRepos      map[string]bool
	MayReadSrc        bool
	MayReadAllTasks   bool
	MayReadFullConfig bool
	MayReadAllMemory  bool
	MayReadMetrics    bool
	MayReadPipeline   bool
	MayReadHealth     bool
	MayReadGrainLog   bool
	MayReadPlaybooks  bool
	MayReadIssues     bool
	MayReadDB         bool
}

// PermitsCI reports whether this scope may read ci://<repo>/...
func (s Scope) PermitsCI(repo string) bool {
	if !s.MayReadCI && !s.MayReadAllCI {
		return false
	}
	if repo == "" {
		return true
	}
	if s.MayReadAllCI {
		return true
	}
	if s.TaskRepo != "" && strings.EqualFold(s.TaskRepo, repo) {
		return true
	}
	if s.AllowedRepos != nil && (s.AllowedRepos[repo] || s.AllowedRepos[strings.ToLower(repo)]) {
		return true
	}
	return false
}

// PermitsSrc reports whether this scope may read src://...
func (s Scope) PermitsSrc() bool {
	return s.MayReadSrc
}

// PermitsSrcPath reports whether this scope may read src://<relPath>.
func (s Scope) PermitsSrcPath(relPath string) bool {
	if s.MayReadSrc {
		return true
	}
	relPath = strings.TrimPrefix(strings.TrimSuffix(relPath, "/"), "/")
	if s.MayReadPlaybooks && (relPath == "playbooks" || strings.HasPrefix(relPath, "playbooks/")) {
		return true
	}
	return false
}

// Entry is one entry in a namespace directory listing.
type Entry struct {
	Name string `json:"name"`
	Type string `json:"type"` // "file" or "dir"
	Size int64  `json:"size,omitempty"`
}

// ResolvedNode is the result of resolving a namespace path.
type ResolvedNode struct {
	Scheme  string
	Path    string
	IsDir   bool
	Entries []Entry
	Size    int64
	Growing bool
	Open    func() (io.ReadCloser, error)
}

// Server resolves and serves paths in the grain namespace (ci://, src://, task://, config://, memory://, deploy://).
type Server struct {
	CIStore     ci.RunStore
	ArtifactDir string
	SrcDir      string
	Tasks       TaskProvider
	ConfigDir   string
	// ConfigAllow, when set, is the whole of what config:// serves out of
	// ConfigDir: a slash path relative to it is listed and read only if
	// this says so. ConfigDir is a working tree a deployment keeps more in
	// than its exported configuration (the state repository: .git, and
	// on older builds an encrypted secrets file), so the daemon names the
	// paths grain itself writes there rather than the ones it withholds
	// (staterepo.BrowsablePath). The virtual repos/ entries a
	// ConfigProvider serves are not files in ConfigDir and are not asked.
	ConfigAllow func(relPath string) bool
	Config      ConfigProvider
	MemoryDir   string
	Memory      MemoryProvider
	Deploy      DeployProvider
	Redactor    *redact.Redactor
	// NewRedactor, when set, builds the Redactor afresh for each
	// ServeHTTP request in place of Redactor, so that a secret set since
	// the daemon started is redacted by the next request rather than by
	// the next restart.
	NewRedactor        func() *redact.Redactor
	MaxResponseBytes   int64
	FollowPollInterval time.Duration
	FollowMaxDuration  time.Duration
}

func (s *Server) maxBytes() int64 {
	if s != nil && s.MaxResponseBytes > 0 {
		return s.MaxResponseBytes
	}
	return DefaultMaxResponseBytes
}

func (s *Server) pollInterval() time.Duration {
	if s != nil && s.FollowPollInterval > 0 {
		return s.FollowPollInterval
	}
	return DefaultFollowPollInterval
}

func (s *Server) followMaxDuration() time.Duration {
	if s != nil && s.FollowMaxDuration > 0 {
		return s.FollowMaxDuration
	}
	return DefaultFollowMaxDuration
}

// ParseURI parses a namespace URI ("ci://owner/repo/...", "src://pkg/...", or
// "/fs/ci/owner/repo/...") into its scheme and normalized slash-separated path.
func ParseURI(raw string) (scheme, relPath string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("%w: empty namespace path", ErrBadRequest)
	}
	if rest, ok := strings.CutPrefix(raw, "/fs/"); ok {
		raw = rest
	} else if raw == "/fs" {
		return "", "", fmt.Errorf("%w: missing namespace scheme", ErrBadRequest)
	}
	if idx := strings.Index(raw, "://"); idx > 0 {
		scheme = strings.ToLower(raw[:idx])
		relPath = raw[idx+3:]
	} else {
		raw = strings.TrimPrefix(raw, "/")
		slash := strings.IndexByte(raw, '/')
		if slash < 0 {
			scheme = strings.ToLower(raw)
			relPath = ""
		} else {
			scheme = strings.ToLower(raw[:slash])
			relPath = raw[slash+1:]
		}
	}
	for _, seg := range strings.Split(relPath, "/") {
		if seg == ".." {
			return "", "", fmt.Errorf("%w: path traversal %q is not allowed", ErrBadRequest, raw)
		}
	}
	relPath = strings.TrimPrefix(relPath, "/")
	relPath = strings.TrimSuffix(relPath, "/")
	switch scheme {
	case "ci", "src", "task", "config", "memory", "deploy":
		return scheme, relPath, nil
	default:
		return "", "", fmt.Errorf("%w: unknown scheme %q", ErrBadRequest, scheme)
	}
}

// RESTURL returns the sandbox proxy URL for a scheme and path.
func RESTURL(scheme, relPath string) string {
	relPath = strings.TrimPrefix(relPath, "/")
	if relPath == "" {
		return fmt.Sprintf("http://127.0.0.1:9728/fs/%s/", scheme)
	}
	return fmt.Sprintf("http://127.0.0.1:9728/fs/%s/%s", scheme, relPath)
}

// Resolve resolves a scheme and relative path under scope.
func (s *Server) Resolve(ctx context.Context, scope Scope, scheme, relPath string) (*ResolvedNode, error) {
	switch scheme {
	case "ci":
		return s.resolveCI(ctx, scope, relPath)
	case "src":
		return s.resolveSrc(ctx, scope, relPath)
	case "task":
		return s.resolveTask(ctx, scope, relPath)
	case "config":
		return s.resolveConfig(ctx, scope, relPath)
	case "memory":
		return s.resolveMemory(ctx, scope, relPath)
	case "deploy":
		return s.resolveDeploy(ctx, scope, relPath)
	default:
		return nil, fmt.Errorf("%w: unknown scheme %q", ErrBadRequest, scheme)
	}
}

func (s *Server) resolveSrc(ctx context.Context, scope Scope, relPath string) (*ResolvedNode, error) {
	if !scope.PermitsSrcPath(relPath) {
		trimmed := strings.TrimPrefix(strings.TrimSuffix(relPath, "/"), "/")
		if trimmed == "playbooks" || strings.HasPrefix(trimmed, "playbooks/") {
			return nil, RefuseCapability("src://"+relPath, "bootstrap-playbooks")
		}
		return nil, RefuseCapability("src://"+relPath, "self-debug")
	}
	if s == nil || strings.TrimSpace(s.SrcDir) == "" {
		return nil, fmt.Errorf("%w: src:// is not configured on this daemon", ErrNotFound)
	}
	for _, seg := range strings.Split(relPath, "/") {
		if seg == ".git" {
			return nil, fmt.Errorf("%w: .git is not accessible via src://", ErrForbidden)
		}
	}
	var target string
	var err error
	if relPath == "" {
		target = s.SrcDir
	} else {
		target, err = safepath.Within(s.SrcDir, filepath.FromSlash(relPath))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadRequest, err)
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: src://%s", ErrNotFound, relPath)
		}
		return nil, err
	}
	if info.IsDir() {
		dirEntries, err := os.ReadDir(target)
		if err != nil {
			return nil, err
		}
		entries := make([]Entry, 0, len(dirEntries))
		for _, de := range dirEntries {
			if de.Name() == ".git" {
				continue
			}
			typ := "file"
			var sz int64
			if de.IsDir() {
				typ = "dir"
			} else if fi, err := de.Info(); err == nil {
				sz = fi.Size()
			}
			entries = append(entries, Entry{
				Name: de.Name(),
				Type: typ,
				Size: sz,
			})
		}
		sortEntries(entries)
		return &ResolvedNode{
			Scheme:  "src",
			Path:    relPath,
			IsDir:   true,
			Entries: entries,
		}, nil
	}
	return &ResolvedNode{
		Scheme:  "src",
		Path:    relPath,
		IsDir:   false,
		Size:    info.Size(),
		Growing: false,
		Open: func() (io.ReadCloser, error) {
			return os.Open(target)
		},
	}, nil
}

func (s *Server) resolveCI(ctx context.Context, scope Scope, relPath string) (*ResolvedNode, error) {
	if !scope.PermitsCI("") {
		return nil, RefuseCapability("ci://"+relPath, "grain-ci")
	}
	if s == nil || s.CIStore == nil {
		return nil, fmt.Errorf("%w: CI store is unavailable", ErrNotFound)
	}
	if relPath == "" {
		repos := s.permittedCIRepos(scope)
		entries := make([]Entry, 0, len(repos))
		for _, r := range repos {
			entries = append(entries, Entry{Name: r, Type: "dir"})
		}
		return &ResolvedNode{
			Scheme:  "ci",
			Path:    "",
			IsDir:   true,
			Entries: entries,
		}, nil
	}
	parts := strings.Split(relPath, "/")
	if len(parts) == 1 {
		owner := parts[0]
		repos := s.permittedCIRepos(scope)
		var entries []Entry
		prefix := owner + "/"
		for _, r := range repos {
			if rest, ok := strings.CutPrefix(r, prefix); ok && rest != "" {
				entries = append(entries, Entry{Name: rest, Type: "dir"})
			}
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("%w: ci://%s", ErrNotFound, relPath)
		}
		sortEntries(entries)
		return &ResolvedNode{
			Scheme:  "ci",
			Path:    relPath,
			IsDir:   true,
			Entries: entries,
		}, nil
	}

	repo := parts[0] + "/" + parts[1]
	if !scope.PermitsCI(repo) {
		return nil, fmt.Errorf("%w: ci://%s is outside this sandbox's CI read scope", ErrForbidden, repo)
	}
	if len(parts) == 2 {
		page := s.CIStore.Find(ci.RunFilter{Repo: repo, Descending: true}, maxCIRunsListed)
		entries := make([]Entry, 0, len(page.Runs))
		for _, run := range page.Runs {
			if run.Repo != repo {
				continue
			}
			entries = append(entries, Entry{Name: run.ID, Type: "dir"})
		}
		return &ResolvedNode{
			Scheme:  "ci",
			Path:    relPath,
			IsDir:   true,
			Entries: entries,
		}, nil
	}

	runID := parts[2]
	run, ok := s.CIStore.Get(runID)
	if !ok || run.Repo != repo {
		return nil, fmt.Errorf("%w: ci://%s/%s", ErrNotFound, repo, runID)
	}
	if len(parts) == 3 {
		entries := make([]Entry, 0, len(run.Jobs)+1)
		seen := make(map[string]bool, len(run.Jobs))
		hasArtifacts := false
		for _, job := range ci.SortJobRuns(run.Jobs) {
			key := jobKey(job)
			if key != "" && !seen[key] {
				seen[key] = true
				entries = append(entries, Entry{Name: key, Type: "dir"})
			}
			for _, a := range job.Artifacts {
				if a.Stored {
					hasArtifacts = true
				}
			}
		}
		if hasArtifacts {
			entries = append(entries, Entry{Name: "artifacts", Type: "dir"})
		}
		return &ResolvedNode{
			Scheme:  "ci",
			Path:    relPath,
			IsDir:   true,
			Entries: entries,
		}, nil
	}

	if parts[3] == "artifacts" {
		return s.resolveCIArtifacts(run, relPath, parts[4:])
	}

	job, ok := findCIJob(run, parts[3])
	if !ok {
		return nil, fmt.Errorf("%w: job %q in ci://%s/%s", ErrNotFound, parts[3], repo, runID)
	}

	rem := parts[4:]
	if len(rem) == 0 {
		att, _ := job.LatestAttempt()
		entries := attemptEntries(att, true)
		return &ResolvedNode{
			Scheme:  "ci",
			Path:    relPath,
			IsDir:   true,
			Entries: entries,
		}, nil
	}

	if rem[0] == "attempts" {
		attempts := job.EffectiveAttempts()
		if len(rem) == 1 {
			entries := make([]Entry, 0, len(attempts))
			for _, att := range attempts {
				entries = append(entries, Entry{Name: strconv.Itoa(att.Attempt), Type: "dir"})
			}
			return &ResolvedNode{
				Scheme:  "ci",
				Path:    relPath,
				IsDir:   true,
				Entries: entries,
			}, nil
		}
		attemptNum, err := strconv.Atoi(rem[1])
		if err != nil || attemptNum < 1 {
			return nil, fmt.Errorf("%w: invalid attempt number %q", ErrNotFound, rem[1])
		}
		att, ok := job.GetAttempt(attemptNum)
		if !ok {
			return nil, fmt.Errorf("%w: attempt %d in job %q", ErrNotFound, attemptNum, parts[3])
		}
		isLatest := attemptNum == len(attempts)
		growingAttempt := isLatest && !run.Conclusion.Done() && !job.Conclusion.Done() && !att.Conclusion.Done()
		return s.resolveCIAttempt(relPath, att, rem[2:], false, growingAttempt)
	}

	att, _ := job.LatestAttempt()
	growingAttempt := !run.Conclusion.Done() && !job.Conclusion.Done() && !att.Conclusion.Done()
	return s.resolveCIAttempt(relPath, att, rem, true, growingAttempt)
}

func (s *Server) resolveCIArtifacts(run ci.Run, relPath string, rem []string) (*ResolvedNode, error) {
	if len(rem) == 0 {
		seen := make(map[string]bool)
		var entries []Entry
		for _, job := range run.Jobs {
			for _, a := range job.Artifacts {
				if !a.Stored {
					continue
				}
				jk := a.Job
				if jk == "" {
					jk = jobKey(job)
				}
				if jk != "" && !seen[jk] {
					seen[jk] = true
					entries = append(entries, Entry{Name: jk, Type: "dir"})
				}
			}
		}
		sortEntries(entries)
		return &ResolvedNode{
			Scheme:  "ci",
			Path:    relPath,
			IsDir:   true,
			Entries: entries,
		}, nil
	}
	jobName := rem[0]
	var matching []ci.Artifact
	for _, job := range run.Jobs {
		jk := jobKey(job)
		if jk != jobName && job.Name != jobName && job.JobID != jobName {
			continue
		}
		for _, a := range job.Artifacts {
			if a.Stored {
				matching = append(matching, a)
			}
		}
	}
	if len(rem) == 1 {
		if len(matching) == 0 {
			return nil, fmt.Errorf("%w: no artifacts for job %q", ErrNotFound, jobName)
		}
		entries := make([]Entry, 0, len(matching))
		for _, a := range matching {
			entries = append(entries, Entry{Name: a.Name, Type: "file", Size: a.Bytes})
		}
		sortEntries(entries)
		return &ResolvedNode{
			Scheme:  "ci",
			Path:    relPath,
			IsDir:   true,
			Entries: entries,
		}, nil
	}
	artName := strings.Join(rem[1:], "/")
	for _, a := range matching {
		if a.Name == artName {
			if s.ArtifactDir == "" {
				return nil, fmt.Errorf("%w: artifact storage directory is not configured", ErrNotFound)
			}
			artJob := a.Job
			if artJob == "" {
				artJob = jobName
			}
			target, err := safepath.Within(s.ArtifactDir, filepath.Join(run.ID, artJob, filepath.FromSlash(a.Name)))
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrBadRequest, err)
			}
			info, err := os.Stat(target)
			if err != nil {
				return nil, fmt.Errorf("%w: artifact %s", ErrNotFound, artName)
			}
			return &ResolvedNode{
				Scheme:  "ci",
				Path:    relPath,
				IsDir:   false,
				Size:    info.Size(),
				Growing: false,
				Open: func() (io.ReadCloser, error) {
					return os.Open(target)
				},
			}, nil
		}
	}
	return nil, fmt.Errorf("%w: artifact %q", ErrNotFound, artName)
}

func (s *Server) resolveCIAttempt(relPath string, att ci.JobAttempt, rem []string, includeAttemptsDir bool, growingAttempt bool) (*ResolvedNode, error) {
	if len(rem) == 0 {
		return &ResolvedNode{
			Scheme:  "ci",
			Path:    relPath,
			IsDir:   true,
			Entries: attemptEntries(att, includeAttemptsDir),
		}, nil
	}

	if rem[0] == "tests" {
		return s.resolveCIAttemptTests(relPath, att, rem[1:])
	}

	if len(rem) == 1 {
		name := rem[0]
		if name == "gotest.log" {
			content := attemptGoTestRawLog(att)
			if content == "" {
				return nil, fmt.Errorf("%w: gotest.log is not present for this attempt", ErrNotFound)
			}
			return stringNode("ci", relPath, content, false), nil
		}
		if name == "job.log" {
			content := attemptCombinedLog(att)
			return stringNode("ci", relPath, content, growingAttempt), nil
		}
		if step, ok := findAttemptStep(att, name); ok {
			content := stepLogContent(step)
			growing := growingAttempt && !step.Conclusion.Done()
			return stringNode("ci", relPath, content, growing), nil
		}
	}

	return nil, fmt.Errorf("%w: ci://%s", ErrNotFound, relPath)
}

func (s *Server) resolveCIAttemptTests(relPath string, att ci.JobAttempt, rem []string) (*ResolvedNode, error) {
	byPkg := ci.ParseAttemptGoTests(att)
	if len(byPkg) == 0 {
		return nil, fmt.Errorf("%w: no per-test output recorded for this attempt", ErrNotFound)
	}
	if len(rem) == 0 {
		pkgs := ci.SortedAttemptTestPackages(byPkg)
		entries := make([]Entry, 0, len(pkgs))
		for _, p := range pkgs {
			entries = append(entries, Entry{Name: p, Type: "dir"})
		}
		return &ResolvedNode{
			Scheme:  "ci",
			Path:    relPath,
			IsDir:   true,
			Entries: entries,
		}, nil
	}

	joined := strings.Join(rem, "/")
	if strings.HasSuffix(joined, ".log") {
		withoutExt := strings.TrimSuffix(joined, ".log")
		// Try splitting withoutExt into <pkg>/<Test> from longest package prefix to shortest.
		for i := len(withoutExt) - 1; i > 0; i-- {
			if withoutExt[i] != '/' {
				continue
			}
			pkgPart := withoutExt[:i]
			testName := withoutExt[i+1:]
			if out, ok := ci.LookupAttemptGoTest(att, pkgPart, testName); ok {
				return stringNode("ci", relPath, out, false), nil
			}
		}
		if out, ok := ci.LookupAttemptGoTest(att, "_default", withoutExt); ok {
			return stringNode("ci", relPath, out, false), nil
		}
		return nil, fmt.Errorf("%w: test log %q", ErrNotFound, joined)
	}

	norm := ci.NormalizeGoTestPackage(joined)
	if testsMap, ok := byPkg[norm]; ok {
		names := ci.SortedPackageTestNames(testsMap)
		entries := make([]Entry, 0, len(names))
		for _, name := range names {
			entries = append(entries, Entry{
				Name: name + ".log",
				Type: "file",
				Size: int64(len(testsMap[name])),
			})
		}
		return &ResolvedNode{
			Scheme:  "ci",
			Path:    relPath,
			IsDir:   true,
			Entries: entries,
		}, nil
	}

	// Check if norm is a parent directory prefix of one or more packages (e.g. "pkg" for "pkg/ci").
	prefix := norm + "/"
	seenSub := make(map[string]bool)
	var subEntries []Entry
	for pkg := range byPkg {
		if rest, ok := strings.CutPrefix(pkg, prefix); ok && rest != "" {
			nextSeg := rest
			if slash := strings.IndexByte(rest, '/'); slash >= 0 {
				nextSeg = rest[:slash]
			}
			if !seenSub[nextSeg] {
				seenSub[nextSeg] = true
				subEntries = append(subEntries, Entry{Name: nextSeg, Type: "dir"})
			}
		}
	}
	if len(subEntries) > 0 {
		sortEntries(subEntries)
		return &ResolvedNode{
			Scheme:  "ci",
			Path:    relPath,
			IsDir:   true,
			Entries: subEntries,
		}, nil
	}

	return nil, fmt.Errorf("%w: test package %q", ErrNotFound, joined)
}

func stringNode(scheme, relPath, content string, growing bool) *ResolvedNode {
	return &ResolvedNode{
		Scheme:  scheme,
		Path:    relPath,
		IsDir:   false,
		Size:    int64(len(content)),
		Growing: growing,
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(content)), nil
		},
	}
}

func (s *Server) permittedCIRepos(scope Scope) []string {
	seen := make(map[string]bool)
	if scope.TaskRepo != "" && scope.PermitsCI(scope.TaskRepo) {
		seen[scope.TaskRepo] = true
	}
	for r := range scope.AllowedRepos {
		if scope.PermitsCI(r) {
			seen[r] = true
		}
	}
	if s.CIStore != nil {
		page := s.CIStore.Find(ci.RunFilter{Descending: true}, maxCIRunsListed)
		for _, run := range page.Runs {
			if run.Repo != "" && scope.PermitsCI(run.Repo) {
				seen[run.Repo] = true
			}
		}
	}
	repos := make([]string, 0, len(seen))
	for r := range seen {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	return repos
}

func jobKey(job ci.JobRun) string {
	if job.JobID != "" {
		return job.JobID
	}
	return job.Name
}

func findCIJob(run ci.Run, name string) (ci.JobRun, bool) {
	for _, job := range run.Jobs {
		if job.JobID == name || job.Name == name || sanitizeStepName(job.Name) == name {
			return job, true
		}
	}
	return ci.JobRun{}, false
}

func sanitizeStepName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	res := strings.Trim(b.String(), "_")
	if res == "" {
		return "step"
	}
	return res
}

func stepFileName(step ci.StepRun) string {
	name := strings.TrimSpace(step.Name)
	if name == "" {
		name = "step"
	}
	name = strings.ReplaceAll(name, "/", "_")
	if !strings.HasSuffix(name, ".log") {
		name += ".log"
	}
	return name
}

func findAttemptStep(att ci.JobAttempt, fileName string) (ci.StepRun, bool) {
	trimmed := strings.TrimSuffix(fileName, ".log")
	for _, step := range att.Steps {
		if stepFileName(step) == fileName ||
			step.Name == trimmed ||
			sanitizeStepName(step.Name) == trimmed {
			return step, true
		}
	}
	return ci.StepRun{}, false
}

func stepLogContent(step ci.StepRun) string {
	var b strings.Builder
	if step.Output != "" {
		b.WriteString(step.Output)
		// A running step's last line may be half written; leaving it
		// without its newline is what tells ?follow=1 to wait for the
		// rest of it (redactWindow's holdPartial).
		if !strings.HasSuffix(step.Output, "\n") && step.Conclusion.Done() {
			b.WriteByte('\n')
		}
	}
	if step.Error != "" && !strings.Contains(step.Output, step.Error) {
		fmt.Fprintf(&b, "Error: %s\n", step.Error)
	}
	return b.String()
}

func attemptCombinedLog(att ci.JobAttempt) string {
	var b strings.Builder
	for _, step := range att.Steps {
		sc := stepLogContent(step)
		if sc == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "=== Step: %s ===\n%s", step.Name, sc)
	}
	if att.Error != "" {
		fmt.Fprintf(&b, "Job error: %s\n", att.Error)
	}
	return b.String()
}

func attemptGoTestRawLog(att ci.JobAttempt) string {
	if att.GoTestLog != "" {
		return att.GoTestLog
	}
	var b strings.Builder
	for _, step := range att.Steps {
		if strings.Contains(step.Output, "=== RUN") || strings.Contains(step.Output, `"Action":`) || strings.Contains(step.Output, "--- FAIL:") {
			b.WriteString(step.Output)
			if !strings.HasSuffix(step.Output, "\n") {
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

func attemptEntries(att ci.JobAttempt, includeAttemptsDir bool) []Entry {
	var entries []Entry
	if includeAttemptsDir {
		entries = append(entries, Entry{Name: "attempts", Type: "dir"})
	}
	seen := make(map[string]bool)
	for _, step := range att.Steps {
		fn := stepFileName(step)
		if !seen[fn] {
			seen[fn] = true
			entries = append(entries, Entry{
				Name: fn,
				Type: "file",
				Size: int64(len(stepLogContent(step))),
			})
		}
	}
	if raw := attemptGoTestRawLog(att); raw != "" {
		entries = append(entries, Entry{
			Name: "gotest.log",
			Type: "file",
			Size: int64(len(raw)),
		})
	}
	if len(ci.ParseAttemptGoTests(att)) > 0 {
		entries = append(entries, Entry{
			Name: "tests",
			Type: "dir",
		})
	}
	return entries
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
}

// ServeHTTP serves GET and HEAD requests under /fs/<scheme>/<path>.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request, scope Scope) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.NewRedactor != nil {
		fresh := *s
		fresh.Redactor = s.NewRedactor()
		fresh.NewRedactor = nil
		fresh.ServeHTTP(w, r, scope)
		return
	}
	scheme, relPath, err := ParseURI(r.URL.Path)
	if err != nil {
		writeNamespaceError(w, err)
		return
	}
	q := r.URL.Query()
	if grepExpr := q.Get("grep"); grepExpr != "" {
		s.serveGrep(w, r, scope, scheme, relPath, grepExpr)
		return
	}
	if globPat := q.Get("glob"); globPat != "" {
		s.serveGlob(w, r, scope, scheme, relPath, globPat)
		return
	}

	node, err := s.Resolve(r.Context(), scope, scheme, relPath)
	if err != nil {
		writeNamespaceError(w, err)
		return
	}

	_, wantList := q["list"]
	if wantList {
		if !node.IsDir && len(node.Entries) == 0 {
			http.Error(w, fmt.Sprintf("%s://%s is a file, not a directory", scheme, relPath), http.StatusBadRequest)
			return
		}
		s.serveList(w, r, node)
		return
	}
	if node.IsDir && node.Open == nil {
		s.serveList(w, r, node)
		return
	}

	if q.Get("follow") == "1" || q.Get("follow") == "true" {
		s.serveFollow(w, r, scope, scheme, relPath, node)
		return
	}

	s.serveFile(w, r, node)
}

func writeNamespaceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrUnsupportedScheme):
		http.Error(w, err.Error(), http.StatusNotImplemented)
	case errors.Is(err, ErrBadRequest):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) serveList(w http.ResponseWriter, r *http.Request, node *ResolvedNode) {
	w.Header().Set("X-Grain-Growing", "false")
	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"scheme":  node.Scheme,
			"path":    node.Path,
			"entries": node.Entries,
		})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	var b strings.Builder
	for _, e := range node.Entries {
		name := e.Name
		if e.Type == "dir" && !strings.HasSuffix(name, "/") {
			name += "/"
		}
		b.WriteString(redactLine(s.Redactor, name))
		b.WriteByte('\n')
	}
	_, _ = io.WriteString(w, b.String())
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, node *ResolvedNode) {
	growingStr := "false"
	if node.Growing {
		growingStr = "true"
	}
	w.Header().Set("X-Grain-Growing", growingStr)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Grain-Total-Size", strconv.FormatInt(node.Size, 10))

	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", strconv.FormatInt(node.Size, 10))
		w.WriteHeader(http.StatusOK)
		return
	}

	q := r.URL.Query()
	var startOffset int64
	var lengthLimit int64 = -1
	statusCode := http.StatusOK

	if tailStr := q.Get("tail"); tailStr != "" {
		tailBytes, err := strconv.ParseInt(tailStr, 10, 64)
		if err != nil || tailBytes < 0 {
			http.Error(w, fmt.Sprintf("invalid tail parameter %q", tailStr), http.StatusBadRequest)
			return
		}
		if tailBytes < node.Size {
			startOffset = node.Size - tailBytes
		}
		w.Header().Set("X-Grain-Offset", strconv.FormatInt(startOffset, 10))
	} else if rangeHdr := r.Header.Get("Range"); rangeHdr != "" {
		start, end, ok, satisfiable := parseByteRange(rangeHdr, node.Size, node.Growing)
		if !ok {
			http.Error(w, fmt.Sprintf("invalid Range header %q", rangeHdr), http.StatusBadRequest)
			return
		}
		if !satisfiable {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", node.Size))
			http.Error(w, "requested range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		startOffset = start
		if end >= start {
			lengthLimit = (end - start) + 1
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, node.Size))
			statusCode = http.StatusPartialContent
		} else {
			// Growing file where start == node.Size (caught up to tip).
			lengthLimit = 0
		}
		w.Header().Set("X-Grain-Offset", strconv.FormatInt(startOffset, 10))
	} else if q.Get("offset") != "" || q.Get("limit") != "" {
		if offStr := q.Get("offset"); offStr != "" {
			off, err := strconv.ParseInt(offStr, 10, 64)
			if err != nil || off < 0 {
				http.Error(w, fmt.Sprintf("invalid offset parameter %q", offStr), http.StatusBadRequest)
				return
			}
			startOffset = off
		}
		if limStr := q.Get("limit"); limStr != "" {
			lim, err := strconv.ParseInt(limStr, 10, 64)
			if err != nil || lim < 0 {
				http.Error(w, fmt.Sprintf("invalid limit parameter %q", limStr), http.StatusBadRequest)
				return
			}
			lengthLimit = lim
		}
		w.Header().Set("X-Grain-Offset", strconv.FormatInt(startOffset, 10))
	}

	maxCeiling := s.maxBytes()
	if lengthLimit < 0 || lengthLimit > maxCeiling {
		lengthLimit = maxCeiling
	}
	// Opened once up front so that a node that cannot be read is an
	// error status rather than an empty 200.
	rc, err := node.Open()
	if err != nil {
		writeNamespaceError(w, err)
		return
	}
	rc.Close()

	w.WriteHeader(statusCode)
	_, _ = s.redactWindow(w, node, startOffset, startOffset+lengthLimit, false)
}

func (s *Server) serveFollow(w http.ResponseWriter, r *http.Request, scope Scope, scheme, relPath string, initial *ResolvedNode) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Grain-Growing", strconv.FormatBool(initial.Growing))
	w.Header().Set("Cache-Control", "no-cache")

	var offset int64
	if tailStr := r.URL.Query().Get("tail"); tailStr != "" {
		if tailBytes, err := strconv.ParseInt(tailStr, 10, 64); err == nil && tailBytes >= 0 && tailBytes < initial.Size {
			offset = initial.Size - tailBytes
		}
	} else if rangeHdr := r.Header.Get("Range"); rangeHdr != "" {
		if start, _, ok, satisfiable := parseByteRange(rangeHdr, initial.Size, true); ok && satisfiable {
			offset = start
		}
	}

	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	ctx, cancel := context.WithTimeout(r.Context(), s.followMaxDuration())
	defer cancel()

	var totalSent int64
	maxCeiling := s.maxBytes()

	sendFrom := func(node *ResolvedNode) (bool, error) {
		if node.Size > offset {
			toRead := node.Size - offset
			if totalSent+toRead > maxCeiling {
				toRead = maxCeiling - totalSent
			}
			if toRead > 0 {
				// Only whole lines while the log is still growing: a
				// poll that lands mid-line waits for the rest of it.
				next, err := s.redactWindow(w, node, offset, offset+toRead, node.Growing)
				if err != nil {
					return false, err
				}
				totalSent += next - offset
				offset = next
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
		if totalSent >= maxCeiling {
			return false, nil
		}
		return node.Growing, nil
	}

	stillGrowing, err := sendFrom(initial)
	if err != nil || !stillGrowing {
		return
	}

	ticker := time.NewTicker(s.pollInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			node, err := s.Resolve(ctx, scope, scheme, relPath)
			if err != nil {
				return
			}
			stillGrowing, err = sendFrom(node)
			if err != nil || !stillGrowing {
				return
			}
		}
	}
}

func (s *Server) serveGlob(w http.ResponseWriter, r *http.Request, scope Scope, scheme, relPath, pattern string) {
	matches, err := s.Glob(r.Context(), scope, scheme, relPath, pattern)
	if err != nil {
		writeNamespaceError(w, err)
		return
	}
	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"scheme":  scheme,
			"path":    relPath,
			"glob":    pattern,
			"matches": matches,
		})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	var b strings.Builder
	for _, m := range matches {
		b.WriteString(redactLine(s.Redactor, m))
		b.WriteByte('\n')
	}
	_, _ = io.WriteString(w, b.String())
}

// Glob returns relative paths under scheme://relPath matching pattern.
func (s *Server) Glob(ctx context.Context, scope Scope, scheme, relPath, pattern string) ([]string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("%w: empty glob pattern", ErrBadRequest)
	}
	node, err := s.Resolve(ctx, scope, scheme, relPath)
	if err != nil {
		return nil, err
	}
	if !node.IsDir {
		if matchGlob(pattern, path.Base(relPath)) {
			return []string{path.Base(relPath)}, nil
		}
		return nil, nil
	}
	var matches []string
	visited := 0
	var walk func(curRel string) error
	walk = func(curRel string) error {
		if visited >= maxWalkFiles {
			return nil
		}
		fullRel := relPath
		if curRel != "" {
			if fullRel != "" {
				fullRel = fullRel + "/" + curRel
			} else {
				fullRel = curRel
			}
		}
		n, err := s.Resolve(ctx, scope, scheme, fullRel)
		if err != nil || !n.IsDir {
			return nil
		}
		for _, e := range n.Entries {
			visited++
			if visited > maxWalkFiles {
				break
			}
			childRel := e.Name
			if curRel != "" {
				childRel = curRel + "/" + e.Name
			}
			if matchGlob(pattern, childRel) || matchGlob(pattern, e.Name) {
				entryName := childRel
				if e.Type == "dir" && !strings.HasSuffix(entryName, "/") {
					entryName += "/"
				}
				matches = append(matches, entryName)
			}
			if e.Type == "dir" {
				// Avoid descending into both unnumbered job files and attempts/<n> unless pattern asks for attempts.
				if e.Name == "attempts" && !strings.Contains(pattern, "attempts") && !strings.Contains(pattern, "**") {
					continue
				}
				_ = walk(childRel)
			}
		}
		return nil
	}
	if err := walk(""); err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

// GrepOptions configures a namespace grep operation.
type GrepOptions struct {
	Pattern      string
	IgnoreCase   bool
	LineNumbers  bool
	ContextLines int
	MaxMatches   int
	GlobFilter   string
}

// GrepResult holds formatted grep output and metadata.
type GrepResult struct {
	Output    string
	Matches   int
	Truncated bool
	MaxLimit  int
}

func (s *Server) serveGrep(w http.ResponseWriter, r *http.Request, scope Scope, scheme, relPath, pattern string) {
	q := r.URL.Query()
	_, hasI := q["i"]
	ignoreCase := hasI && q.Get("i") != "0" && q.Get("i") != "false"
	_, hasN := q["n"]
	lineNumbers := hasN && q.Get("n") != "0" && q.Get("n") != "false"
	contextLines := 0
	if cStr := q.Get("context"); cStr != "" {
		c, err := strconv.Atoi(cStr)
		if err != nil || c < 0 {
			http.Error(w, fmt.Sprintf("invalid context parameter %q", cStr), http.StatusBadRequest)
			return
		}
		contextLines = c
	}
	maxMatches := DefaultGrepMaxMatches
	if mStr := q.Get("max"); mStr != "" {
		m, err := strconv.Atoi(mStr)
		if err != nil || m < 1 {
			http.Error(w, fmt.Sprintf("invalid max parameter %q", mStr), http.StatusBadRequest)
			return
		}
		maxMatches = m
	}

	res, err := s.Grep(r.Context(), scope, scheme, relPath, GrepOptions{
		Pattern:      pattern,
		IgnoreCase:   ignoreCase,
		LineNumbers:  lineNumbers,
		ContextLines: contextLines,
		MaxMatches:   maxMatches,
		GlobFilter:   q.Get("glob"),
	})
	if err != nil {
		writeNamespaceError(w, err)
		return
	}
	if res.Truncated {
		w.Header().Set("X-Grain-Truncated", "true")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = io.WriteString(w, res.Output)
}

// Grep searches a file or directory subtree in the namespace for lines matching opts.Pattern.
func (s *Server) Grep(ctx context.Context, scope Scope, scheme, relPath string, opts GrepOptions) (GrepResult, error) {
	expr := opts.Pattern
	if expr == "" {
		return GrepResult{}, fmt.Errorf("%w: grep pattern is required", ErrBadRequest)
	}
	if opts.IgnoreCase && !strings.HasPrefix(expr, "(?i)") {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return GrepResult{}, fmt.Errorf("%w: invalid grep regex %q: %v", ErrBadRequest, opts.Pattern, err)
	}
	maxMatches := opts.MaxMatches
	if maxMatches <= 0 {
		maxMatches = DefaultGrepMaxMatches
	}
	if maxMatches > MaxGrepMaxMatches {
		maxMatches = MaxGrepMaxMatches
	}
	ctxLines := opts.ContextLines
	if ctxLines < 0 {
		ctxLines = 0
	}
	if ctxLines > MaxGrepContextLines {
		ctxLines = MaxGrepContextLines
	}

	node, err := s.Resolve(ctx, scope, scheme, relPath)
	if err != nil {
		return GrepResult{}, err
	}

	type targetFile struct {
		label string
		node  *ResolvedNode
	}
	var files []targetFile
	if !node.IsDir {
		files = append(files, targetFile{label: "", node: node})
	} else {
		visited := 0
		var collect func(curRel string)
		collect = func(curRel string) {
			if visited >= maxWalkFiles {
				return
			}
			fullRel := relPath
			if curRel != "" {
				if fullRel != "" {
					fullRel = fullRel + "/" + curRel
				} else {
					fullRel = curRel
				}
			}
			n, err := s.Resolve(ctx, scope, scheme, fullRel)
			if err != nil || !n.IsDir {
				return
			}
			for _, e := range n.Entries {
				visited++
				if visited > maxWalkFiles {
					break
				}
				childRel := e.Name
				if curRel != "" {
					childRel = curRel + "/" + e.Name
				}
				if e.Type == "dir" {
					// When grepping a job directory, skip attempts/ unless asked so we don't duplicate latest attempt matches.
					if e.Name == "attempts" && curRel == "" {
						continue
					}
					collect(childRel)
					continue
				}
				if opts.GlobFilter != "" && !matchGlob(opts.GlobFilter, childRel) && !matchGlob(opts.GlobFilter, e.Name) {
					continue
				}
				childFull := relPath
				if childFull != "" {
					childFull += "/" + childRel
				} else {
					childFull = childRel
				}
				fn, err := s.Resolve(ctx, scope, scheme, childFull)
				if err == nil && !fn.IsDir {
					files = append(files, targetFile{label: childRel, node: fn})
				}
			}
		}
		collect("")
	}

	showLineNum := opts.LineNumbers || len(files) > 1 || ctxLines > 0
	var out strings.Builder
	totalMatches := 0
	truncated := false

	for _, tf := range files {
		if totalMatches >= maxMatches {
			truncated = true
			break
		}
		rc, err := tf.node.Open()
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(rc)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

		type numberedLine struct {
			num  int
			text string
		}
		var beforeBuf []numberedLine
		afterRemaining := 0
		lastPrintedLine := 0
		lineNum := 0

		for scanner.Scan() {
			lineNum++
			line := redactLine(s.Redactor, scanner.Text())
			if re.MatchString(line) {
				if totalMatches >= maxMatches {
					truncated = true
					break
				}
				totalMatches++
				for _, bLine := range beforeBuf {
					if bLine.num > lastPrintedLine {
						writeGrepLine(&out, tf.label, bLine.num, '-', bLine.text, showLineNum)
						lastPrintedLine = bLine.num
					}
				}
				beforeBuf = beforeBuf[:0]
				writeGrepLine(&out, tf.label, lineNum, ':', line, showLineNum)
				lastPrintedLine = lineNum
				afterRemaining = ctxLines
			} else if afterRemaining > 0 {
				writeGrepLine(&out, tf.label, lineNum, '-', line, showLineNum)
				lastPrintedLine = lineNum
				afterRemaining--
			} else if ctxLines > 0 {
				beforeBuf = append(beforeBuf, numberedLine{num: lineNum, text: line})
				if len(beforeBuf) > ctxLines {
					beforeBuf = beforeBuf[1:]
				}
			}
		}
		rc.Close()
		if truncated {
			break
		}
	}

	if truncated {
		fmt.Fprintf(&out, "[truncated at match limit (%d matches); pass ?max=%d to raise the limit]\n", maxMatches, maxMatches*2)
	}

	return GrepResult{
		Output:    out.String(),
		Matches:   totalMatches,
		Truncated: truncated,
		MaxLimit:  maxMatches,
	}, nil
}

func writeGrepLine(out *strings.Builder, fileLabel string, lineNum int, sep byte, text string, showLineNum bool) {
	if fileLabel != "" {
		out.WriteString(fileLabel)
		out.WriteByte(sep)
	}
	if showLineNum {
		out.WriteString(strconv.Itoa(lineNum))
		out.WriteByte(sep)
	}
	out.WriteString(text)
	out.WriteByte('\n')
}

func redactLine(r *redact.Redactor, s string) string {
	out, _ := r.Line(s)
	return out
}

// ReadRedacted reads at most limit bytes of node's content through s's
// Redactor, the same pass serveFile streams a REST read through. Anything
// that moves namespace content out of the daemon other than over ServeHTTP
// (grain_copy) reads it here, so no path hands a sandbox unredacted bytes.
func (s *Server) ReadRedacted(node *ResolvedNode, limit int64) (string, error) {
	if node == nil || node.IsDir || node.Open == nil {
		return "", fmt.Errorf("%w: not a file", ErrBadRequest)
	}
	if s == nil {
		s = &Server{}
	}
	var b strings.Builder
	if _, err := s.redactWindow(&b, node, 0, limit, false); err != nil {
		return "", err
	}
	return b.String(), nil
}

// lineStartLookback is how far before a window's start redactWindow looks
// for the newline that begins the line the window starts in, before it
// falls back to scanning from the top of the file.
const lineStartLookback = 64 << 10

// redactWindow writes the raw bytes [start, end) of node to w through
// redactor, and returns the raw offset just past what it wrote. end < 0 is
// "to the end of the file".
//
// Redaction is a line at a time, so the window is not cut until after it:
// a window cut first could start or end inside a secret, and the part of
// it that was served would match nothing (a Range of bytes=N-N+4 across a
// token would return its characters four at a time). Each line the window
// touches is read whole and redacted; a line redaction left unchanged is
// then cut to the window exactly, and a line it changed is served whole,
// redacted, by every window that touches it -- so adjacent windows may
// repeat that line, and neither of them shows any of it in the clear.
//
// holdPartial leaves a final line with no newline yet unwritten, and the
// returned offset at its start, so that ?follow=1 on a growing log never
// serves the first half of a line whose second half (and secret) is still
// being written.
func (s *Server) redactWindow(w io.Writer, node *ResolvedNode, start, end int64, holdPartial bool) (int64, error) {
	lineStart, err := lineStartAt(node, start)
	if err != nil {
		return start, err
	}
	rc, err := node.Open()
	if err != nil {
		return start, err
	}
	defer rc.Close()
	if err := skipTo(rc, lineStart); err != nil {
		return start, err
	}
	br := bufio.NewReaderSize(rc, 32*1024)
	pos := lineStart
	next := start
	for end < 0 || pos < end {
		line, rerr := br.ReadString('\n')
		if line == "" {
			if rerr != nil && !errors.Is(rerr, io.EOF) {
				return next, rerr
			}
			break
		}
		if holdPartial && !strings.HasSuffix(line, "\n") {
			break
		}
		lineEnd := pos + int64(len(line))
		if lineEnd <= start {
			// Only a window starting past the end of the file gets here.
			pos = lineEnd
			if rerr != nil {
				break
			}
			continue
		}
		out := redactLine(s.Redactor, line)
		if out == line {
			from, to := max(start, pos), lineEnd
			if end >= 0 {
				to = min(to, end)
			}
			out = line[from-pos : to-pos]
			next = to
		} else {
			next = max(next, lineEnd)
		}
		if _, werr := io.WriteString(w, out); werr != nil {
			return next, werr
		}
		pos = lineEnd
		if rerr != nil {
			break
		}
	}
	return next, nil
}

// lineStartAt is the raw offset of the start of the line holding offset.
func lineStartAt(node *ResolvedNode, offset int64) (int64, error) {
	if offset <= 0 {
		return 0, nil
	}
	from := max(0, offset-lineStartLookback)
	for {
		rc, err := node.Open()
		if err != nil {
			return 0, err
		}
		if err := skipTo(rc, from); err != nil {
			rc.Close()
			return 0, err
		}
		buf := make([]byte, 32*1024)
		found := int64(-1)
		pos := from
		for pos < offset {
			n, rerr := rc.Read(buf[:min(int64(len(buf)), offset-pos)])
			if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
				found = pos + int64(i) + 1
			}
			pos += int64(n)
			if rerr != nil {
				break
			}
		}
		rc.Close()
		if found >= 0 || from == 0 {
			return max(found, 0), nil
		}
		// The line is longer than the lookback: find its start the slow
		// way rather than redact half of it.
		from = 0
	}
}

// skipTo advances rc to offset, seeking where it can.
func skipTo(rc io.Reader, offset int64) error {
	if offset <= 0 {
		return nil
	}
	if seeker, ok := rc.(io.Seeker); ok {
		_, err := seeker.Seek(offset, io.SeekStart)
		return err
	}
	if _, err := io.CopyN(io.Discard, rc, offset); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// streamRedacted streams r to w line-by-line (or in bounded chunks for lines
// without newlines), applying Redactor.Line to each chunk without buffering the
// whole stream in memory.
func streamRedacted(w io.Writer, r io.Reader, redactor *redact.Redactor) error {
	br := bufio.NewReaderSize(r, 32*1024)
	for {
		chunk, err := br.ReadString('\n')
		if len(chunk) > 0 {
			redacted := redactLine(redactor, chunk)
			if _, werr := io.WriteString(w, redacted); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func parseByteRange(rangeHeader string, size int64, growing bool) (start, end int64, valid, satisfiable bool) {
	rangeHeader = strings.TrimSpace(rangeHeader)
	rest, ok := strings.CutPrefix(rangeHeader, "bytes=")
	if !ok || strings.Contains(rest, ",") {
		return 0, 0, false, false
	}
	before, after, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, 0, false, false
	}
	before = strings.TrimSpace(before)
	after = strings.TrimSpace(after)
	if before == "" {
		// Suffix range: bytes=-N
		suffix, err := strconv.ParseInt(after, 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, false, false
		}
		if size == 0 {
			if growing {
				return 0, -1, true, true
			}
			return 0, 0, true, false
		}
		if suffix > size {
			suffix = size
		}
		return size - suffix, size - 1, true, true
	}
	s, err := strconv.ParseInt(before, 10, 64)
	if err != nil || s < 0 {
		return 0, 0, false, false
	}
	if s >= size {
		if growing && s == size {
			return s, s - 1, true, true
		}
		return 0, 0, true, false
	}
	if after == "" {
		return s, size - 1, true, true
	}
	e, err := strconv.ParseInt(after, 10, 64)
	if err != nil || e < s {
		return 0, 0, false, false
	}
	if e >= size {
		e = size - 1
	}
	return s, e, true, true
}

func wantsJSON(r *http.Request) bool {
	if r.URL.Query().Get("format") == "json" {
		return true
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/json")
}

// matchGlob matches candidate against a glob pattern supporting *, ?, and **.
func matchGlob(pattern, candidate string) bool {
	pattern = strings.TrimPrefix(pattern, "/")
	candidate = strings.TrimSuffix(strings.TrimPrefix(candidate, "/"), "/")
	if !strings.Contains(pattern, "**") && !strings.Contains(pattern, "/") {
		if ok, err := path.Match(pattern, path.Base(candidate)); err == nil && ok {
			return true
		}
	}
	reStr := globToRegex(pattern)
	re, err := regexp.Compile(reStr)
	if err != nil {
		return false
	}
	return re.MatchString(candidate)
}

func globToRegex(glob string) string {
	var b bytes.Buffer
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '[', ']', '{', '}', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString("$")
	return b.String()
}

// WalkFiles returns all file nodes under scheme://relPath (including relPath itself if it is a file).
func (s *Server) WalkFiles(ctx context.Context, scope Scope, scheme, relPath string) ([]struct {
	RelPath string
	Node    *ResolvedNode
}, error) {
	node, err := s.Resolve(ctx, scope, scheme, relPath)
	if err != nil {
		return nil, err
	}
	var out []struct {
		RelPath string
		Node    *ResolvedNode
	}
	if !node.IsDir {
		out = append(out, struct {
			RelPath string
			Node    *ResolvedNode
		}{RelPath: path.Base(relPath), Node: node})
		return out, nil
	}
	visited := 0
	var walk func(curRel string) error
	walk = func(curRel string) error {
		if visited >= maxWalkFiles {
			return nil
		}
		fullRel := relPath
		if curRel != "" {
			if fullRel != "" {
				fullRel = fullRel + "/" + curRel
			} else {
				fullRel = curRel
			}
		}
		n, err := s.Resolve(ctx, scope, scheme, fullRel)
		if err != nil || !n.IsDir {
			return nil
		}
		for _, e := range n.Entries {
			visited++
			if visited > maxWalkFiles {
				break
			}
			childRel := e.Name
			if curRel != "" {
				childRel = curRel + "/" + e.Name
			}
			if e.Type == "dir" {
				if err := walk(childRel); err != nil {
					return err
				}
				continue
			}
			childFull := relPath
			if childFull != "" {
				childFull += "/" + childRel
			} else {
				childFull = childRel
			}
			fn, err := s.Resolve(ctx, scope, scheme, childFull)
			if err == nil && !fn.IsDir {
				out = append(out, struct {
					RelPath string
					Node    *ResolvedNode
				}{RelPath: childRel, Node: fn})
			}
		}
		return nil
	}
	if err := walk(""); err != nil {
		return nil, err
	}
	return out, nil
}

var _ fs.FileMode
