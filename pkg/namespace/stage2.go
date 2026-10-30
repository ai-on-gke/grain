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

package namespace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/safepath"
)

// RefuseCapability returns an ErrForbidden error with the actionable refusal
// message matching pkg/mcp/toolnames.go's unknownToolMessage when a run lacks
// a required capability grant.
func RefuseCapability(subject, capability string) error {
	return fmt.Errorf(
		"%w: %s: that tool belongs to the %q capability, and this run does not hold it (this run does not hold the %q capability). "+
			"Nothing you can do from inside this run will add it -- a human attaches it to the task.",
		ErrForbidden, subject, capability, capability)
}

// TaskAttemptView is one attempt's addressable prompt and transcript under
// task://<id>/attempts/<n>/.
type TaskAttemptView struct {
	Number     int
	RunID      string
	Prompt     string
	Transcript string
	Growing    bool
}

// TaskView is the addressable files of task://<id>/.
type TaskView struct {
	ID           string
	Record       string
	Summary      string
	Conversation string
	Handover     string
	Prompt       string
	Transcript   string
	Attempts     []TaskAttemptView
	// Repo is the repository the task works in ("" for none), which is
	// what decides whether a scope without MayReadAllTasks may read it
	// (Scope.PermitsTaskRepo).
	Repo string
}

// TaskProvider backs the task:// namespace root.
type TaskProvider interface {
	ListTaskIDs(ctx context.Context) ([]string, error)
	ReadTaskView(ctx context.Context, id string) (*TaskView, error)
}

// TaskAttemptReader is what a TaskProvider also implements when it would
// rather not read every attempt's prompt and transcript to answer a read
// of one file: its ReadTaskView then names the attempts (Number, RunID,
// Growing) and leaves their Prompt and Transcript empty, and a path under
// task://<id>/attempts/<n>/ -- or task://<id>/transcript.md, which is the
// latest attempt's -- is read through ReadTaskAttempt instead.
type TaskAttemptReader interface {
	ReadTaskAttempt(ctx context.Context, id string, attempt TaskAttemptView) (*TaskAttemptView, error)
}

// loadAttempt fills in one attempt's prompt and transcript where the
// provider reads them lazily (TaskAttemptReader), and is att itself
// otherwise.
func (s *Server) loadAttempt(ctx context.Context, id string, att *TaskAttemptView) (*TaskAttemptView, error) {
	lazy, ok := s.Tasks.(TaskAttemptReader)
	if !ok {
		return att, nil
	}
	full, err := lazy.ReadTaskAttempt(ctx, id, *att)
	if err != nil {
		return nil, fmt.Errorf("%w: task://%s/attempts/%d: %v", ErrNotFound, id, att.Number, err)
	}
	if full == nil {
		return att, nil
	}
	return full, nil
}

// ConfigProvider backs virtual entries under the config:// namespace root when
// not backed directly by files in ConfigDir.
type ConfigProvider interface {
	ListConfigRepos(ctx context.Context) ([]string, error)
	ReadRepoConfig(ctx context.Context, repo string) (string, error)
}

// MemoryDocView is one addressable memory document under memory://.
type MemoryDocView struct {
	Slug    string
	Summary string
	Text    string
}

// MemoryLayerView is one standing memory layer and its documents under
// memory://global/ or memory://repos/<owner>/<repo>/.
type MemoryLayerView struct {
	Scope string
	Text  string
	Docs  []MemoryDocView
}

// MemoryProvider backs the memory:// namespace root.
type MemoryProvider interface {
	ListMemoryRepos(ctx context.Context) ([]string, error)
	ReadMemoryLayer(ctx context.Context, scope string) (*MemoryLayerView, error)
}

// DeployProvider backs the deploy:// namespace root.
type DeployProvider interface {
	ReadMergeQueue(ctx context.Context) (string, error)
	ReadMetrics(ctx context.Context, section string, q MetricsQuery) (string, error)
	ReadPipelineChanges(ctx context.Context) (string, error)
	ReadHealth(ctx context.Context) (string, error)
	ReadState(ctx context.Context, table string) (string, error)
	ListStateTables(ctx context.Context) ([]string, error)
	ReadGrainLog(ctx context.Context, source string, q GrainLogQuery) (string, error)
	ListGrainLogSources(ctx context.Context) ([]string, error)
}

// MetricsSectionLister is what a DeployProvider also implements to name
// the sections deploy://metrics/ lists, where they are not the default
// set.
type MetricsSectionLister interface {
	ListMetricsSections() []string
}

func (s Scope) ownTaskID() string {
	if id := strings.TrimSpace(s.TaskIDStr); id != "" {
		return id
	}
	if s.TaskID > 0 {
		return strconv.FormatInt(s.TaskID, 10)
	}
	return ""
}

// PermitsTask reports whether this scope may read task://<taskID>/...
func (s Scope) PermitsTask(taskID string) bool {
	if s.MayReadAllTasks {
		return true
	}
	taskID = strings.TrimSpace(strings.TrimPrefix(taskID, "#"))
	own := s.ownTaskID()
	return own != "" && taskID != "" && strings.EqualFold(own, taskID)
}

// PermitsTaskRepo reports whether this scope may read the task:// record
// of a task working in repo: one in the scope's own repository, or one
// it reads or writes, or one an api:tasks:read@repo grant names. A task
// with no repository is only MayReadAllTasks'.
func (s Scope) PermitsTaskRepo(repo string) bool {
	if s.MayReadAllTasks {
		return true
	}
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return false
	}
	if s.TaskRepo != "" && strings.EqualFold(s.TaskRepo, repo) {
		return true
	}
	return s.TaskReadRepos[repo] || s.TaskReadRepos[strings.ToLower(repo)]
}

// mayReadRepoTasks reports whether this scope may read some other tasks'
// records, by their repository.
func (s Scope) mayReadRepoTasks() bool {
	return s.TaskRepo != "" || len(s.TaskReadRepos) > 0
}

// PermitsConfig reports whether this scope may read config://<relPath>.
func (s Scope) PermitsConfig(relPath string) bool {
	if s.MayReadFullConfig {
		return true
	}
	relPath = strings.TrimPrefix(strings.TrimSuffix(relPath, "/"), "/")
	if relPath == "" || relPath == "repos" || relPath == "workflows" || relPath == ".grain" || relPath == ".grain/workflows" {
		return true
	}
	if strings.HasPrefix(relPath, "workflows/") || strings.HasPrefix(relPath, ".grain/workflows/") {
		return true
	}
	if s.TaskRepo != "" {
		repoPrefix := "repos/" + strings.ToLower(s.TaskRepo)
		lower := strings.ToLower(relPath)
		if strings.HasPrefix(repoPrefix, lower+"/") ||
			lower == repoPrefix ||
			strings.HasPrefix(lower, repoPrefix+"/") ||
			lower == repoPrefix+".json" ||
			lower == repoPrefix+".yml" ||
			lower == repoPrefix+".yaml" {
			return true
		}
	}
	return false
}

// PermitsMemory reports whether this scope may read memory://<relPath>.
func (s Scope) PermitsMemory(relPath string) bool {
	if s.MayReadAllMemory {
		return true
	}
	relPath = strings.TrimPrefix(strings.TrimSuffix(relPath, "/"), "/")
	if relPath == "" || relPath == "global" || relPath == "global.md" || strings.HasPrefix(relPath, "global/") || relPath == "repos" {
		return true
	}
	if s.TaskRepo != "" {
		repoPrefix := "repos/" + strings.ToLower(s.TaskRepo)
		lower := strings.ToLower(relPath)
		if strings.HasPrefix(repoPrefix, lower+"/") ||
			lower == repoPrefix ||
			lower == repoPrefix+".md" ||
			strings.HasPrefix(lower, repoPrefix+"/") {
			return true
		}
	}
	return false
}

func (s *Server) resolveTask(ctx context.Context, scope Scope, relPath string) (*ResolvedNode, error) {
	relPath = strings.TrimPrefix(strings.TrimSuffix(relPath, "/"), "/")
	if relPath == "" {
		if !scope.MayReadAllTasks {
			own := scope.ownTaskID()
			if own == "" {
				return nil, RefuseCapability("task://", "self-debug")
			}
			return &ResolvedNode{
				Scheme:  "task",
				Path:    "",
				IsDir:   true,
				Entries: []Entry{{Name: own, Type: "dir"}},
			}, nil
		}
		if s == nil || s.Tasks == nil {
			return nil, fmt.Errorf("%w: task:// is not configured on this daemon", ErrNotFound)
		}
		ids, err := s.Tasks.ListTaskIDs(ctx)
		if err != nil {
			return nil, err
		}
		entries := make([]Entry, 0, len(ids))
		for _, id := range ids {
			entries = append(entries, Entry{Name: id, Type: "dir"})
		}
		sortEntries(entries)
		return &ResolvedNode{
			Scheme:  "task",
			Path:    "",
			IsDir:   true,
			Entries: entries,
		}, nil
	}

	parts := strings.Split(relPath, "/")
	taskID := strings.TrimPrefix(parts[0], "#")
	own := scope.PermitsTask(taskID)
	if !own && !scope.mayReadRepoTasks() {
		return nil, RefuseCapability("task://"+relPath, "self-debug")
	}
	if s == nil || s.Tasks == nil {
		return nil, fmt.Errorf("%w: task:// is not configured on this daemon", ErrNotFound)
	}
	tv, err := s.Tasks.ReadTaskView(ctx, taskID)
	if !own && (err != nil || tv == nil || !scope.PermitsTaskRepo(tv.Repo)) {
		// Another task is readable where it works in a repository this
		// scope reaches; anything else -- including a task that does not
		// exist -- is refused alike, so the refusal says nothing about it.
		return nil, RefuseCapability("task://"+relPath, "self-debug")
	}
	if err != nil || tv == nil {
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: task://%s: %v", ErrNotFound, relPath, err)
		}
		return nil, fmt.Errorf("%w: task://%s", ErrNotFound, relPath)
	}

	if len(parts) == 1 {
		entries := []Entry{
			{Name: "conversation.md", Type: "file", Size: int64(len(tv.Conversation))},
			{Name: "handover.md", Type: "file", Size: int64(len(tv.Handover))},
			{Name: "prompt.md", Type: "file", Size: int64(len(tv.Prompt))},
			{Name: "record.md", Type: "file", Size: int64(len(tv.Record))},
		}
		if tv.Summary != "" {
			entries = append(entries, Entry{Name: "summary.md", Type: "file", Size: int64(len(tv.Summary))})
		}
		if len(tv.Attempts) > 0 {
			entries = append(entries, Entry{Name: "attempts", Type: "dir"})
		}
		sortEntries(entries)
		node := stringNode("task", relPath, tv.Record, false)
		node.IsDir = true
		node.Entries = entries
		return node, nil
	}

	switch parts[1] {
	case "record.md", "record", "task.md":
		if len(parts) == 2 {
			return stringNode("task", relPath, tv.Record, false), nil
		}
	case "summary.md", "summary":
		if len(parts) == 2 {
			text := tv.Summary
			if text == "" {
				text = tv.Record
			}
			return stringNode("task", relPath, text, false), nil
		}
	case "conversation.md", "conversation", "comments.md", "comments":
		if len(parts) == 2 {
			return stringNode("task", relPath, tv.Conversation, false), nil
		}
	case "handover.md", "handover":
		if len(parts) == 2 {
			return stringNode("task", relPath, tv.Handover, false), nil
		}
	case "prompt.md", "prompt":
		if len(parts) == 2 {
			return stringNode("task", relPath, tv.Prompt, false), nil
		}
	case "transcript.md", "transcript":
		if len(parts) == 2 {
			if tv.Transcript != "" {
				return stringNode("task", relPath, tv.Transcript, false), nil
			}
			if len(tv.Attempts) > 0 {
				att, err := s.loadAttempt(ctx, taskID, &tv.Attempts[len(tv.Attempts)-1])
				if err != nil {
					return nil, err
				}
				return stringNode("task", relPath, att.Transcript, att.Growing), nil
			}
			return stringNode("task", relPath, "", false), nil
		}
	case "attempts":
		if len(parts) == 2 {
			entries := make([]Entry, 0, len(tv.Attempts))
			for _, att := range tv.Attempts {
				entries = append(entries, Entry{Name: strconv.Itoa(att.Number), Type: "dir"})
			}
			return &ResolvedNode{
				Scheme:  "task",
				Path:    relPath,
				IsDir:   true,
				Entries: entries,
			}, nil
		}
		attNum, err := strconv.Atoi(parts[2])
		if err != nil {
			return nil, fmt.Errorf("%w: invalid attempt %q in task://%s", ErrNotFound, parts[2], relPath)
		}
		var found *TaskAttemptView
		for i := range tv.Attempts {
			if tv.Attempts[i].Number == attNum {
				found = &tv.Attempts[i]
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("%w: task://%s", ErrNotFound, relPath)
		}
		if len(parts) > 4 {
			return nil, fmt.Errorf("%w: task://%s", ErrNotFound, relPath)
		}
		if found, err = s.loadAttempt(ctx, taskID, found); err != nil {
			return nil, err
		}
		if len(parts) == 3 {
			entries := []Entry{
				{Name: "prompt.md", Type: "file", Size: int64(len(found.Prompt))},
				{Name: "transcript.md", Type: "file", Size: int64(len(found.Transcript))},
			}
			return &ResolvedNode{
				Scheme:  "task",
				Path:    relPath,
				IsDir:   true,
				Entries: entries,
			}, nil
		}
		if len(parts) == 4 {
			switch parts[3] {
			case "transcript.md", "transcript":
				return stringNode("task", relPath, found.Transcript, found.Growing), nil
			case "prompt.md", "prompt":
				return stringNode("task", relPath, found.Prompt, false), nil
			}
		}
	}
	return nil, fmt.Errorf("%w: task://%s", ErrNotFound, relPath)
}

func (s *Server) resolveConfig(ctx context.Context, scope Scope, relPath string) (*ResolvedNode, error) {
	relPath = strings.TrimPrefix(strings.TrimSuffix(relPath, "/"), "/")
	if !scope.PermitsConfig(relPath) {
		return nil, RefuseCapability("config://"+relPath, "self-debug")
	}
	for _, seg := range strings.Split(relPath, "/") {
		if seg == ".git" || seg == "secrets.enc" {
			return nil, fmt.Errorf("%w: %s is not accessible via config://", ErrForbidden, seg)
		}
	}

	// First try virtual ConfigProvider for repos/... if configured.
	if s != nil && s.Config != nil {
		if node, ok, err := s.resolveVirtualConfig(ctx, scope, relPath); ok || err != nil {
			return node, err
		}
	}

	if s == nil || strings.TrimSpace(s.ConfigDir) == "" {
		return nil, fmt.Errorf("%w: config:// is not configured on this daemon", ErrNotFound)
	}
	if relPath != "" && !s.configAllowed(relPath) {
		return nil, fmt.Errorf("%w: config://%s is not part of grain's exported configuration", ErrForbidden, relPath)
	}
	var target string
	var err error
	if relPath == "" {
		target = s.ConfigDir
	} else {
		target, err = safepath.Within(s.ConfigDir, filepath.FromSlash(relPath))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadRequest, err)
		}
		// The test that counts is on where the path lands, not on how
		// it was spelled: a symlink at tables/x.json pointing at
		// ../.git/config or at a file outside the tree altogether is
		// refused by the same rules a direct request for it is.
		if err := s.configResolvesInside(target); err != nil {
			return nil, fmt.Errorf("%w: config://%s: %v", ErrForbidden, relPath, err)
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: config://%s", ErrNotFound, relPath)
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
			name := de.Name()
			if name == ".git" || name == "secrets.enc" {
				continue
			}
			childPath := name
			if relPath != "" {
				childPath = relPath + "/" + name
			}
			if !scope.PermitsConfig(childPath) || !s.configAllowed(childPath) {
				continue
			}
			// grain writes no symlinks into its configuration, so one
			// there is something else's and is neither listed nor
			// followed.
			if !de.IsDir() && !de.Type().IsRegular() {
				continue
			}
			typ := "file"
			var sz int64
			if de.IsDir() {
				typ = "dir"
			} else if fi, err := de.Info(); err == nil {
				sz = fi.Size()
			}
			entries = append(entries, Entry{Name: name, Type: typ, Size: sz})
		}
		if relPath == "" && s.Config != nil && !slices.ContainsFunc(entries, func(e Entry) bool { return e.Name == "repos" }) {
			// The ConfigProvider's per-repository view, beside the files.
			entries = append(entries, Entry{Name: "repos", Type: "dir"})
		}
		sortEntries(entries)
		return &ResolvedNode{
			Scheme:  "config",
			Path:    relPath,
			IsDir:   true,
			Entries: entries,
		}, nil
	}
	return &ResolvedNode{
		Scheme:  "config",
		Path:    relPath,
		IsDir:   false,
		Size:    info.Size(),
		Growing: false,
		Open: func() (io.ReadCloser, error) {
			return os.Open(target)
		},
	}, nil
}

// configAllowed reports whether relPath under ConfigDir may be listed or
// read (ConfigAllow). .git and secrets.enc are refused whatever it says.
func (s *Server) configAllowed(relPath string) bool {
	for _, seg := range strings.Split(relPath, "/") {
		if seg == ".git" || seg == "secrets.enc" {
			return false
		}
	}
	return s.ConfigAllow == nil || s.ConfigAllow(relPath)
}

// configResolvesInside refuses a path under ConfigDir whose symlinks lead
// out of it, or to somewhere inside it configAllowed refuses.
func (s *Server) configResolvesInside(target string) error {
	root, err := filepath.EvalSymlinks(s.ConfigDir)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Nothing there to follow: the Stat that comes next says so.
			return nil
		}
		return err
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("leads outside the configuration")
	}
	if rel != "." && !s.configAllowed(filepath.ToSlash(rel)) {
		return errors.New("leads outside grain's exported configuration")
	}
	return nil
}

func equalFoldParts(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

func trimRepoConfigExt(s string) string {
	for _, ext := range []string{".json", ".yml", ".yaml"} {
		if strings.HasSuffix(s, ext) {
			return strings.TrimSuffix(s, ext)
		}
	}
	return s
}

func (s *Server) resolveVirtualConfig(ctx context.Context, scope Scope, relPath string) (*ResolvedNode, bool, error) {
	if relPath == "" && strings.TrimSpace(s.ConfigDir) == "" {
		entries := []Entry{
			{Name: "repos", Type: "dir"},
			{Name: "workflows", Type: "dir"},
		}
		return &ResolvedNode{Scheme: "config", Path: "", IsDir: true, Entries: entries}, true, nil
	}
	if relPath == "repos" {
		if strings.TrimSpace(s.ConfigDir) != "" {
			if fi, err := os.Stat(filepath.Join(s.ConfigDir, "repos")); err == nil && fi.IsDir() {
				return nil, false, nil
			}
		}
		repos, err := s.Config.ListConfigRepos(ctx)
		if err != nil {
			return nil, true, err
		}
		seenOwners := make(map[string]bool)
		var entries []Entry
		for _, r := range repos {
			if !scope.PermitsConfig("repos/" + r) {
				continue
			}
			owner, _, ok := strings.Cut(r, "/")
			if ok && !seenOwners[owner] {
				seenOwners[owner] = true
				entries = append(entries, Entry{Name: owner, Type: "dir"})
			}
		}
		sortEntries(entries)
		return &ResolvedNode{Scheme: "config", Path: relPath, IsDir: true, Entries: entries}, true, nil
	}
	if strings.HasPrefix(relPath, "repos/") {
		if strings.TrimSpace(s.ConfigDir) != "" {
			if _, err := os.Stat(filepath.Join(s.ConfigDir, filepath.FromSlash(relPath))); err == nil {
				return nil, false, nil
			}
		}
		rest := strings.TrimPrefix(relPath, "repos/")
		parts := strings.Split(rest, "/")
		repoSegs := repoSegmentCount(parts[0])
		if len(parts) < repoSegs {
			repos, err := s.Config.ListConfigRepos(ctx)
			if err != nil {
				return nil, true, err
			}
			childType := "file"
			if len(parts)+1 < repoSegs {
				childType = "dir"
			}
			seen := make(map[string]bool)
			var entries []Entry
			for _, r := range repos {
				if !scope.PermitsConfig("repos/" + r) {
					continue
				}
				rParts := strings.Split(r, "/")
				if len(rParts) != repoSegs || !equalFoldParts(rParts[:len(parts)], parts) {
					continue
				}
				name := rParts[len(parts)]
				if !seen[name] {
					seen[name] = true
					entries = append(entries, Entry{Name: name, Type: childType})
				}
			}
			if len(entries) == 0 {
				return nil, true, fmt.Errorf("%w: config://%s", ErrNotFound, relPath)
			}
			sortEntries(entries)
			return &ResolvedNode{Scheme: "config", Path: relPath, IsDir: true, Entries: entries}, true, nil
		}
		if len(parts) == repoSegs {
			last := trimRepoConfigExt(parts[repoSegs-1])
			repo := strings.Join(append(parts[:repoSegs-1:repoSegs-1], last), "/")
			text, err := s.Config.ReadRepoConfig(ctx, repo)
			if err != nil {
				return nil, true, fmt.Errorf("%w: config://%s: %v", ErrNotFound, relPath, err)
			}
			return stringNode("config", relPath, text, false), true, nil
		}
	}
	return nil, false, nil
}

func (s *Server) resolveMemory(ctx context.Context, scope Scope, relPath string) (*ResolvedNode, error) {
	relPath = strings.TrimPrefix(strings.TrimSuffix(relPath, "/"), "/")
	if !scope.PermitsMemory(relPath) {
		return nil, RefuseCapability("memory://"+relPath, "memory")
	}

	if s != nil && strings.TrimSpace(s.MemoryDir) != "" {
		var target string
		var err error
		if relPath == "" {
			target = s.MemoryDir
		} else {
			target, err = safepath.Within(s.MemoryDir, filepath.FromSlash(relPath))
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrBadRequest, err)
			}
		}
		if info, err := os.Stat(target); err == nil {
			if info.IsDir() {
				dirEntries, err := os.ReadDir(target)
				if err != nil {
					return nil, err
				}
				var entries []Entry
				for _, de := range dirEntries {
					name := de.Name()
					childPath := name
					if relPath != "" {
						childPath = relPath + "/" + name
					}
					if !scope.PermitsMemory(childPath) {
						continue
					}
					typ := "file"
					var sz int64
					if de.IsDir() {
						typ = "dir"
					} else if fi, err := de.Info(); err == nil {
						sz = fi.Size()
					}
					entries = append(entries, Entry{Name: name, Type: typ, Size: sz})
				}
				sortEntries(entries)
				return &ResolvedNode{Scheme: "memory", Path: relPath, IsDir: true, Entries: entries}, nil
			}
			return &ResolvedNode{
				Scheme: "memory",
				Path:   relPath,
				IsDir:  false,
				Size:   info.Size(),
				Open: func() (io.ReadCloser, error) {
					return os.Open(target)
				},
			}, nil
		}
	}

	if s == nil || s.Memory == nil {
		return nil, fmt.Errorf("%w: memory:// is not configured on this daemon", ErrNotFound)
	}

	if relPath == "" {
		entries := []Entry{
			{Name: "global", Type: "dir"},
			{Name: "repos", Type: "dir"},
		}
		return &ResolvedNode{Scheme: "memory", Path: "", IsDir: true, Entries: entries}, nil
	}

	if relPath == "global.md" {
		layer, err := s.Memory.ReadMemoryLayer(ctx, "global")
		if err != nil || layer == nil {
			return nil, fmt.Errorf("%w: memory://%s", ErrNotFound, relPath)
		}
		return stringNode("memory", relPath, layer.Text, false), nil
	}

	if relPath == "global" || strings.HasPrefix(relPath, "global/") {
		layer, err := s.Memory.ReadMemoryLayer(ctx, "global")
		if err != nil || layer == nil {
			return nil, fmt.Errorf("%w: memory://%s", ErrNotFound, relPath)
		}
		rest := strings.TrimPrefix(strings.TrimPrefix(relPath, "global"), "/")
		return resolveMemoryLayerNode("global", relPath, rest, layer)
	}

	if relPath == "repos" {
		repos, err := s.Memory.ListMemoryRepos(ctx)
		if err != nil {
			return nil, err
		}
		if scope.TaskRepo != "" {
			repos = append(repos, scope.TaskRepo)
		}
		seenOwners := make(map[string]bool)
		var entries []Entry
		for _, r := range repos {
			if !scope.PermitsMemory("repos/" + r) {
				continue
			}
			owner, _, ok := strings.Cut(r, "/")
			if ok && !seenOwners[owner] {
				seenOwners[owner] = true
				entries = append(entries, Entry{Name: owner, Type: "dir"})
			}
		}
		sortEntries(entries)
		return &ResolvedNode{Scheme: "memory", Path: relPath, IsDir: true, Entries: entries}, nil
	}

	if strings.HasPrefix(relPath, "repos/") {
		rest := strings.TrimPrefix(relPath, "repos/")
		parts := strings.Split(rest, "/")
		repoSegs := repoSegmentCount(parts[0])
		if len(parts) < repoSegs {
			repos, err := s.Memory.ListMemoryRepos(ctx)
			if err != nil {
				return nil, err
			}
			if scope.TaskRepo != "" {
				repos = append(repos, scope.TaskRepo)
			}
			seen := make(map[string]bool)
			var entries []Entry
			for _, r := range repos {
				if !scope.PermitsMemory("repos/" + r) {
					continue
				}
				rParts := strings.Split(r, "/")
				if len(rParts) != repoSegs || !equalFoldParts(rParts[:len(parts)], parts) {
					continue
				}
				name := rParts[len(parts)]
				if !seen[name] {
					seen[name] = true
					entries = append(entries, Entry{Name: name, Type: "dir"})
				}
			}
			if len(entries) == 0 {
				return nil, fmt.Errorf("%w: memory://%s", ErrNotFound, relPath)
			}
			sortEntries(entries)
			return &ResolvedNode{Scheme: "memory", Path: relPath, IsDir: true, Entries: entries}, nil
		}
		repoName := strings.TrimSuffix(parts[repoSegs-1], ".md")
		repo := strings.Join(append(parts[:repoSegs-1:repoSegs-1], repoName), "/")
		layer, err := s.Memory.ReadMemoryLayer(ctx, repo)
		if err != nil || layer == nil {
			return nil, fmt.Errorf("%w: memory://%s", ErrNotFound, relPath)
		}
		if len(parts) == repoSegs && strings.HasSuffix(parts[repoSegs-1], ".md") {
			return stringNode("memory", relPath, layer.Text, false), nil
		}
		subPath := strings.Join(parts[repoSegs:], "/")
		return resolveMemoryLayerNode("repos/"+repo, relPath, subPath, layer)
	}

	return nil, fmt.Errorf("%w: memory://%s", ErrNotFound, relPath)
}

func resolveMemoryLayerNode(basePath, relPath, subPath string, layer *MemoryLayerView) (*ResolvedNode, error) {
	if subPath == "" {
		entries := []Entry{
			{Name: "MEMORY.md", Type: "file", Size: int64(len(layer.Text))},
		}
		for _, doc := range layer.Docs {
			name := doc.Slug
			if !strings.HasSuffix(name, ".md") {
				name += ".md"
			}
			entries = append(entries, Entry{Name: name, Type: "file", Size: int64(len(doc.Text))})
		}
		sortEntries(entries)
		node := stringNode("memory", relPath, layer.Text, false)
		node.IsDir = true
		node.Entries = entries
		return node, nil
	}
	if subPath == "MEMORY.md" || subPath == "memory.md" || subPath == "index.md" {
		return stringNode("memory", relPath, layer.Text, false), nil
	}
	slug := strings.TrimPrefix(subPath, "docs/")
	slugTrimmed := strings.TrimSuffix(slug, ".md")
	for _, doc := range layer.Docs {
		if doc.Slug == slug || doc.Slug == slugTrimmed {
			return stringNode("memory", relPath, doc.Text, false), nil
		}
	}
	return nil, fmt.Errorf("%w: memory://%s (%s)", ErrNotFound, relPath, basePath)
}

var urlUserinfoPattern = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/@\s]+)@`)

// RedactGrainLogText scrubs URL userinfo credentials as well as any registered
// Redactor secrets, matching pkg/capability/grainlog.Redactor.
func (s *Server) RedactGrainLogText(raw string) string {
	lines := strings.Split(raw, "\n")
	redactor := s.redactor()
	for i, line := range lines {
		line = redactLine(redactor, line)
		if strings.Contains(line, "://") && strings.Contains(line, "@") {
			line = urlUserinfoPattern.ReplaceAllStringFunc(line, func(m string) string {
				u, err := url.Parse(m)
				if err == nil && u.User != nil {
					if _, hasPass := u.User.Password(); hasPass {
						user := u.User.Username()
						return u.Scheme + "://" + user + ":[REDACTED]@"
					}
					return u.Scheme + "://[REDACTED]@"
				}
				return urlUserinfoPattern.ReplaceAllString(m, "${1}[REDACTED]@")
			})
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func (s *Server) resolveDeploy(ctx context.Context, scope Scope, relPath string) (*ResolvedNode, error) {
	relPath = strings.TrimPrefix(strings.TrimSuffix(relPath, "/"), "/")
	if relPath == "" {
		entries := []Entry{
			{Name: "merge-queue", Type: "file"},
		}
		if scope.MayReadGrainLog {
			entries = append(entries, Entry{Name: "grain.log", Type: "file"})
		}
		if scope.MayReadHealth {
			entries = append(entries,
				Entry{Name: "health", Type: "file"},
				Entry{Name: "state", Type: "dir"},
			)
		}
		if scope.MayReadMetrics {
			entries = append(entries, Entry{Name: "metrics", Type: "dir"})
		}
		if scope.MayReadPipeline {
			entries = append(entries, Entry{Name: "pipeline-changes", Type: "file"})
		}
		sortEntries(entries)
		return &ResolvedNode{Scheme: "deploy", Path: "", IsDir: true, Entries: entries}, nil
	}

	parts := strings.Split(relPath, "/")
	head := parts[0]

	switch head {
	case "merge-queue", "merge-queue.md":
		if s == nil || s.Deploy == nil {
			return nil, fmt.Errorf("%w: deploy://merge-queue is not configured on this daemon", ErrNotFound)
		}
		if len(parts) > 1 {
			return nil, fmt.Errorf("%w: deploy://%s", ErrNotFound, relPath)
		}
		text, err := s.Deploy.ReadMergeQueue(ctx)
		if err != nil {
			return nil, err
		}
		return stringNode("deploy", relPath, text, false), nil

	case "grain.log":
		if !scope.MayReadGrainLog {
			return nil, RefuseCapability("deploy://"+relPath, "grain-log")
		}
		if s == nil || s.Deploy == nil {
			return nil, fmt.Errorf("%w: deploy://grain.log is not configured on this daemon", ErrNotFound)
		}
		var source string
		if len(parts) == 2 {
			source = strings.TrimSuffix(parts[1], ".log")
		} else if len(parts) > 2 {
			return nil, fmt.Errorf("%w: deploy://%s", ErrNotFound, relPath)
		}
		q, err := grainLogQueryFrom(ctx)
		if err != nil {
			return nil, err
		}
		text, err := s.Deploy.ReadGrainLog(ctx, source, q)
		if err != nil {
			return nil, fmt.Errorf("%w: deploy://%s: %v", ErrNotFound, relPath, err)
		}
		return stringNode("deploy", relPath, s.RedactGrainLogText(text), false), nil

	case "health", "health.json":
		if !scope.MayReadHealth {
			return nil, RefuseCapability("deploy://"+relPath, "deployment-health")
		}
		if s == nil || s.Deploy == nil {
			return nil, fmt.Errorf("%w: deploy://health is not configured on this daemon", ErrNotFound)
		}
		if len(parts) > 1 {
			return nil, fmt.Errorf("%w: deploy://%s", ErrNotFound, relPath)
		}
		text, err := s.Deploy.ReadHealth(ctx)
		if err != nil {
			return nil, err
		}
		return stringNode("deploy", relPath, text, false), nil

	case "state", "state.json":
		if !scope.MayReadHealth {
			return nil, RefuseCapability("deploy://"+relPath, "deployment-health")
		}
		if s == nil || s.Deploy == nil {
			return nil, fmt.Errorf("%w: deploy://state is not configured on this daemon", ErrNotFound)
		}
		if len(parts) == 1 {
			text, err := s.Deploy.ReadState(ctx, "")
			if err != nil {
				return nil, err
			}
			node := stringNode("deploy", relPath, text, false)
			node.Entries = []Entry{{Name: "tables", Type: "dir"}}
			return node, nil
		}
		table := parts[len(parts)-1]
		if len(parts) == 2 && parts[1] == "tables" {
			tables, err := s.Deploy.ListStateTables(ctx)
			if err != nil {
				return nil, err
			}
			entries := make([]Entry, 0, len(tables))
			for _, tbl := range tables {
				name := tbl
				if !strings.HasSuffix(name, ".json") {
					name += ".json"
				}
				entries = append(entries, Entry{Name: name, Type: "file"})
			}
			sortEntries(entries)
			return &ResolvedNode{Scheme: "deploy", Path: relPath, IsDir: true, Entries: entries}, nil
		}
		table = strings.TrimSuffix(table, ".json")
		text, err := s.Deploy.ReadState(ctx, table)
		if err != nil {
			return nil, fmt.Errorf("%w: deploy://%s: %v", ErrNotFound, relPath, err)
		}
		return stringNode("deploy", relPath, text, false), nil

	case "metrics", "metrics.txt":
		if !scope.MayReadMetrics {
			return nil, RefuseCapability("deploy://"+relPath, "self-debug")
		}
		if s == nil || s.Deploy == nil {
			return nil, fmt.Errorf("%w: deploy://metrics is not configured on this daemon", ErrNotFound)
		}
		if len(parts) == 1 {
			text, err := s.Deploy.ReadMetrics(ctx, "all", metricsQueryFrom(ctx))
			if err != nil {
				return nil, err
			}
			node := stringNode("deploy", relPath, text, false)
			sections := []string{"checks", "cost", "pipeline", "quality", "speed", "subscription", "summary", "tools"}
			if lister, ok := s.Deploy.(MetricsSectionLister); ok {
				sections = lister.ListMetricsSections()
			}
			for _, sec := range sections {
				node.Entries = append(node.Entries, Entry{Name: sec + ".txt", Type: "file"})
			}
			sortEntries(node.Entries)
			return node, nil
		}
		if len(parts) == 2 {
			sec := strings.TrimSuffix(strings.TrimSuffix(parts[1], ".txt"), ".md")
			text, err := s.Deploy.ReadMetrics(ctx, sec, metricsQueryFrom(ctx))
			if err != nil {
				return nil, fmt.Errorf("%w: deploy://%s: %v", ErrNotFound, relPath, err)
			}
			return stringNode("deploy", relPath, text, false), nil
		}
		return nil, fmt.Errorf("%w: deploy://%s", ErrNotFound, relPath)

	case "pipeline-changes", "pipeline-changes.md":
		if !scope.MayReadPipeline {
			return nil, RefuseCapability("deploy://"+relPath, "self-debug")
		}
		if s == nil || s.Deploy == nil {
			return nil, fmt.Errorf("%w: deploy://pipeline-changes is not configured on this daemon", ErrNotFound)
		}
		if len(parts) > 1 {
			return nil, fmt.Errorf("%w: deploy://%s", ErrNotFound, relPath)
		}
		text, err := s.Deploy.ReadPipelineChanges(ctx)
		if err != nil {
			return nil, err
		}
		return stringNode("deploy", relPath, text, false), nil
	}

	return nil, fmt.Errorf("%w: deploy://%s", ErrNotFound, relPath)
}
