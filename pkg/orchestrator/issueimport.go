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

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The periodic GitHub issue importer (model.RepoConfig.IssueImport): every
// so often, each repository that has it switched on has the open issues of
// its source repository read, and a task filed for each one no task stands
// for yet (model.Store.TaskForIssue) -- so an issue is imported once, and
// never again however its task ends.
//
// What it files lands unapproved unless the repository's settings say
// otherwise (IssueImport.Approved), and reads as "imported" while it
// waits: the words in it were written by whoever could open an issue on
// the source, so the default is that a person reads them before an agent
// is dispatched on them.

// IssueImportPrincipal is the automation actor an imported task is filed,
// and when the repository says so approved, by.
var IssueImportPrincipal = model.Principal{Kind: model.PrincipalAutomation, ID: "issue-import"}

// Bounds on what an imported task's body carries of the issue.
const (
	issueImportMaxBody     = 32 << 10
	issueImportMaxComments = 5
	issueImportMaxComment  = 4 << 10
)

// issueImportPolled is when each repository's source was last read, keyed
// by the store and the repository (issueImportKey). Process memory for
// pathTriggerPolled's reason: forgetting it on a restart costs one read,
// and what decides whether an issue is imported is in the store.
var issueImportPolled sync.Map

func issueImportKey(store *model.Store, repo model.RepoRef) string {
	return fmt.Sprintf("%p|%s", store, strings.ToLower(repo.String()))
}

// reconcileIssueImports runs ImportIssues for every repository whose
// importer is on and whose interval has passed since it last ran.
func reconcileIssueImports(ctx context.Context, deps Deps, now time.Time) error {
	if deps.Store == nil || deps.Client == nil {
		return nil
	}
	configs, err := deps.Store.ListRepoConfigs(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: listing repositories for issue import: %w", err)
	}
	var errs []error
	for _, cfg := range configs {
		if !cfg.IssueImport.Enabled {
			continue
		}
		key := issueImportKey(deps.Store, cfg.Repo)
		if last, ok := issueImportPolled.Load(key); ok && now.Sub(last.(time.Time)) < cfg.IssueImport.Interval() {
			continue
		}
		// Recorded before the read rather than after it, so a source
		// that fails is retried at the interval rather than every tick.
		issueImportPolled.Store(key, now)
		if _, err := importIssues(ctx, deps.Store, deps.Client, cfg, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ImportIssues imports repo's source repository's open issues now, whether
// or not its interval has passed: the manual trigger beside the periodic
// one (POST /api/repos/{owner}/{name}/import-issues). It refuses a
// repository whose importer is not switched on, since what it would file
// and how is that configuration.
func ImportIssues(ctx context.Context, store *model.Store, client github.Client, repo model.RepoRef,
	now time.Time) (model.IssueImportResult, error) {
	if client == nil {
		return model.IssueImportResult{}, errors.New("this deployment has no GitHub client to read issues with")
	}
	cfg, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		return model.IssueImportResult{}, err
	}
	if cfg == nil || !cfg.IssueImport.Enabled {
		return model.IssueImportResult{}, fmt.Errorf("issue import is not switched on for %s", repo)
	}
	issueImportPolled.Store(issueImportKey(store, repo), now)
	return importIssues(ctx, store, client, *cfg, now)
}

// importIssues is one pass for one repository.
func importIssues(ctx context.Context, store *model.Store, client github.Client, cfg model.RepoConfig,
	now time.Time) (model.IssueImportResult, error) {
	repo := cfg.Repo
	out := model.IssueImportResult{Repo: repo.String(), Imported: []string{}}
	source, err := cfg.IssueImport.Source(repo)
	if err != nil {
		return out, fmt.Errorf("orchestrator: issue import for %s: source repository %q: %w", repo, cfg.IssueImport.Repo, err)
	}
	out.Source = source.String()
	issues, err := client.ListIssues(source.Owner, source.Name, strings.TrimSpace(cfg.IssueImport.Label))
	if err != nil {
		return out, fmt.Errorf("orchestrator: issue import for %s: listing open issues on %s: %w",
			repo, source, github.Readable(err))
	}
	out.Open = len(issues)
	// Oldest first, so the backlog takes them in the order they were
	// opened and task ids follow issue numbers.
	slices.SortFunc(issues, func(a, b github.Issue) int { return a.Number - b.Number })
	var errs []error
	for _, issue := range issues {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		existing, err := store.TaskForIssue(ctx, source, issue.Number, repo)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if existing != "" {
			out.Skipped++
			continue
		}
		id, err := fileImportedIssue(ctx, store, client, cfg, source, issue, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("orchestrator: importing %s#%d into %s: %w", source, issue.Number, repo, err))
			continue
		}
		log.Printf("orchestrator: imported %s#%d into %s as task %s (approved=%v)",
			source, issue.Number, repo, id, cfg.IssueImport.Approved)
		out.Imported = append(out.Imported, id)
	}
	return out, errors.Join(errs...)
}

// fileImportedIssue files the task for one issue and returns its id.
func fileImportedIssue(ctx context.Context, store *model.Store, client github.Client, cfg model.RepoConfig,
	source model.RepoRef, issue github.Issue, now time.Time) (string, error) {
	repo := cfg.Repo
	id, err := store.NewTaskID(ctx)
	if err != nil {
		return "", fmt.Errorf("allocating task id: %w", err)
	}
	orderKey, err := store.OrderKeyForNewTask(ctx, false)
	if err != nil {
		return "", fmt.Errorf("allocating order key: %w", err)
	}
	grants, err := defaultRepoGrants(ctx, store, repo)
	if err != nil {
		return "", err
	}
	// A read failure leaves the comments out rather than the issue: the
	// task links the issue, and its agent can read the rest there.
	comments, err := client.ListComments(source.Owner, source.Name, issue.Number)
	if err != nil {
		log.Printf("orchestrator: issue import: reading comments on %s#%d: %v", source, issue.Number, github.Readable(err))
		comments = nil
	}
	task := model.Task{
		ID:     id,
		Intent: model.IntentImplement,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: IssueImportPrincipal},
			Reason:      model.ReasonImported,
		},
		Title:      importedIssueTitle(source, repo, issue),
		Body:       importedIssueBody(source, issue, comments),
		Target:     &repo,
		Binding:    model.BindingDirective,
		WorkOrigin: model.GitHubOrigin(source, issue.Number),
		Grants:     grants,
		CreatedAt:  &now,
		OrderKey:   orderKey,
	}
	// Task.Issue is a number in the task's own target -- its pull request
	// says "Fixes #N" there -- so it is set only when the issue is one.
	if source.Same(repo) {
		task.Issue = issue.Number
	}
	if cfg.IssueImport.Approved {
		task.Approval = &model.Attribution{Actor: IssueImportPrincipal}
		task.ApprovedAt = &now
	}
	if err := store.PutTask(ctx, task); err != nil {
		return "", fmt.Errorf("filing task %s: %w", id, err)
	}
	return id, nil
}

// importedIssueTitle is "[#N] title", or "[owner/name#N] title" for an
// issue on another repository than the one the task targets.
func importedIssueTitle(source, repo model.RepoRef, issue github.Issue) string {
	ref := fmt.Sprintf("#%d", issue.Number)
	if !source.Same(repo) {
		ref = fmt.Sprintf("%s#%d", source, issue.Number)
	}
	title := strings.TrimSpace(issue.Title)
	if title == "" {
		title = "Untitled issue"
	}
	return "[" + ref + "] " + title
}

// importedIssueBody is the issue as the task's agent reads it: where it is,
// who opened it, its labels, what it says and its first few comments.
func importedIssueBody(source model.RepoRef, issue github.Issue, comments []github.Comment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Imported from GitHub issue %s#%d", source, issue.Number)
	if issue.HTMLURL != "" {
		fmt.Fprintf(&b, ": %s", issue.HTMLURL)
	}
	b.WriteString("\n\n")
	if issue.Author != "" {
		fmt.Fprintf(&b, "Author: @%s\n", issue.Author)
	}
	if len(issue.Labels) > 0 {
		labels := make([]string, 0, len(issue.Labels))
		for l := range issue.Labels {
			labels = append(labels, l)
		}
		slices.Sort(labels)
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(labels, ", "))
	}
	b.WriteString("\n## Description\n\n")
	if desc := strings.TrimSpace(issue.Body); desc != "" {
		b.WriteString(clipText(desc, issueImportMaxBody))
	} else {
		b.WriteString("(no description)")
	}
	b.WriteString("\n")
	if len(comments) > 0 {
		shown := comments
		if len(shown) > issueImportMaxComments {
			shown = shown[:issueImportMaxComments]
		}
		fmt.Fprintf(&b, "\n## Comments (%d of %d)\n", len(shown), len(comments))
		for _, c := range shown {
			fmt.Fprintf(&b, "\n**@%s:**\n\n%s\n", c.User, clipText(strings.TrimSpace(c.Body), issueImportMaxComment))
		}
	}
	return b.String()
}

// clipText cuts s to at most max bytes on a rune boundary, saying so.
func clipText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n\n…(truncated)"
}
