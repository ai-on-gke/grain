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

package model

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// WorkOrigin is where a tree of tasks ultimately came from: the GitHub
// pull request or issue that started it, or the grain task (a chat, or a
// task a human filed) at its root. Every task carries one (Task.WorkOrigin,
// the task.origin_ref column), and a task filed with propose_task inherits
// its parent's, at any depth -- which is what lets somebody follow all the
// work that grew out of a design PR, an issue or a chat from GitHub and git
// alone: the PR descriptions, the `Origin:` commit trailers and the
// progress comment on the origin all name it (docs/work-trees.md).
//
// It is not Task.Origin, which says who filed this one task and why.
//
// The stored form is one string: `owner/repo#12` for a GitHub pull request
// or issue (GitHub numbers both in one sequence, and the comments API
// treats them alike, so the two need not be told apart), and `task:1766`
// for a grain task. "" is a task with none -- one filed before origins
// existed whose parent had none either.
type WorkOrigin string

var workOriginGitHub = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)#([1-9][0-9]*)$`)

// GitHubOrigin is the origin for pull request or issue n of repo.
func GitHubOrigin(repo RepoRef, n int) WorkOrigin {
	return WorkOrigin(fmt.Sprintf("%s#%d", repo, n))
}

// TaskOrigin is the origin for a tree rooted at grain task id.
func TaskOrigin(id string) WorkOrigin {
	return WorkOrigin("task:" + strings.TrimSpace(id))
}

// ParseWorkOrigin reads an origin as a person or an agent might write
// one: the stored forms, `task 12`, `#12` against defaultRepo, or a
// github.com pull request or issue URL.
func ParseWorkOrigin(s string, defaultRepo *RepoRef) (WorkOrigin, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if rest, ok := strings.CutPrefix(s, "https://github.com/"); ok {
		parts := strings.Split(strings.Trim(rest, "/"), "/")
		if len(parts) >= 4 && (parts[2] == "pull" || parts[2] == "issues") {
			if n, err := strconv.Atoi(parts[3]); err == nil && n > 0 {
				return GitHubOrigin(RepoRef{Owner: parts[0], Name: parts[1]}, n), nil
			}
		}
		return "", fmt.Errorf("origin %q is not a pull request or issue URL", s)
	}
	for _, prefix := range []string{"task:", "task "} {
		if id, ok := strings.CutPrefix(s, prefix); ok && taskIDPattern.MatchString(strings.TrimSpace(id)) {
			return TaskOrigin(id), nil
		}
	}
	if n, ok := strings.CutPrefix(s, "#"); ok && defaultRepo != nil {
		if v, err := strconv.Atoi(n); err == nil && v > 0 {
			return GitHubOrigin(*defaultRepo, v), nil
		}
	}
	if workOriginGitHub.MatchString(s) {
		return WorkOrigin(s), nil
	}
	return "", fmt.Errorf("origin %q is not owner/repo#N, a GitHub URL or task:<id>", s)
}

var taskIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

// GitHub is the pull request or issue this origin names, ok false for a
// task origin or none.
func (o WorkOrigin) GitHub() (repo RepoRef, n int, ok bool) {
	m := workOriginGitHub.FindStringSubmatch(string(o))
	if m == nil {
		return RepoRef{}, 0, false
	}
	n, _ = strconv.Atoi(m[3])
	return RepoRef{Owner: m[1], Name: m[2]}, n, true
}

// Task is the grain task this origin names, "" for a GitHub origin.
func (o WorkOrigin) Task() string {
	id, ok := strings.CutPrefix(string(o), "task:")
	if !ok {
		return ""
	}
	return id
}

// Trailer is the value of an `Origin:` commit trailer for o:
// `owner/repo#12`, or `task 1766`.
func (o WorkOrigin) Trailer() string {
	if id := o.Task(); id != "" {
		return "task " + id
	}
	return string(o)
}

// Label is the GitHub label a pull request in this tree carries when the
// origin is a pull request or issue -- `origin/#12`, or `origin/owner/repo#12`
// when the origin lives in another repository than the pull request -- and
// "" for a task origin.
func (o WorkOrigin) Label(prRepo RepoRef) string {
	repo, n, ok := o.GitHub()
	if !ok {
		return ""
	}
	if repo.Same(prRepo) {
		return fmt.Sprintf("origin/#%d", n)
	}
	return fmt.Sprintf("origin/%s#%d", repo, n)
}

// Parent is the task that filed t with propose_task (LinkProposedBy), ""
// for a task nobody's run filed.
func (t Task) Parent() string {
	for _, l := range t.Links {
		if l.Kind == LinkProposedBy {
			return l.Target
		}
	}
	return ""
}

// ExplicitOrigin reads the origin a propose_task call (or a person filing
// a task) named, for the task being filed against target by parent (nil
// for nobody's). It is honoured only where it adds something: a task
// whose parent already has a GitHub origin stays in that tree, since an
// origin is inherited at any depth. "" means the task inherits
// (putTask's originForNewTask).
func ExplicitOrigin(named string, target *RepoRef, parent *Task) (WorkOrigin, error) {
	origin, err := ParseWorkOrigin(named, target)
	if err != nil || origin == "" {
		return "", err
	}
	if parent != nil {
		if _, _, ok := parent.WorkOrigin.GitHub(); ok {
			return "", nil
		}
	}
	return origin, nil
}

// OriginKeptNote is the line a proposal's body carries when it named a
// GitHub origin ExplicitOrigin did not honour -- its parent's tree already
// has a different one, which it stays in -- so the run that named it, and
// whoever reads the task, learn why it was not re-rooted. "" when there is
// nothing to say: no origin named, a malformed one (ExplicitOrigin's own
// error), or the one the tree already has.
func OriginKeptNote(named string, target *RepoRef, parent *Task) string {
	origin, err := ParseWorkOrigin(named, target)
	if err != nil || origin == "" || parent == nil {
		return ""
	}
	if _, _, ok := parent.WorkOrigin.GitHub(); !ok || parent.WorkOrigin == origin {
		return ""
	}
	return fmt.Sprintf("\n\n_grain kept this proposal in %s's tree, where task %s's work already belongs, "+
		"rather than the origin it named (%s): a task whose tree has a GitHub origin keeps it._",
		parent.WorkOrigin, parent.ID, origin)
}
