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

// taskcmd.go is the task verbs of the CLI main.go dispatches to -- get,
// create, update, approve, defer, comment, close, retry and the rest --
// and how a task is printed.
package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// respond re-reads number and prints it -- what every mutating command
// below does once its own call to Client succeeds, the CLI's analogue of
// pkg/ui's own respondWithTask.
func respond(ctx context.Context, c *ui.HTTPClient, out *printer, id string) error {
	task, err := c.Task(ctx, id)
	if err != nil {
		return err
	}
	out.task(task)
	return nil
}

func cmdGet(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain get", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	detail, err := c.GetTask(ctx, id)
	if err != nil {
		return err
	}
	out.detail(detail)
	return nil
}

// writeTargets reads the -write flag's own spelling of one of a task's
// extra writeable repos: owner/name, optionally with the branch work for
// that repo goes on after an "@" (ui.WriteTarget, model.WriteTarget).
//
// One flag rather than two paired ones because the two halves are one
// declaration and a CLI has no way to pair repeated flags -- `-write
// acme/state -base deploy -write acme/docs` would be an ordering rule
// nobody could read off the usage line. The "@" is unambiguous: a repo
// segment may not contain one at all (model.validRepoSegment), so the
// first "@" is always the separator, whatever the branch name after it
// contains.
//
// Nothing is validated here beyond the split. The repo goes through
// model.ParseRepo on the server like every other repo this CLI sends,
// which is the one place that decides what a repo is.
func writeTargets(flags []string) ([]ui.WriteTarget, error) {
	out := make([]ui.WriteTarget, 0, len(flags))
	for _, raw := range flags {
		text := strings.TrimSpace(raw)
		if text == "" {
			continue
		}
		repo, branch, hasBranch := strings.Cut(text, "@")
		if hasBranch && strings.TrimSpace(branch) == "" {
			return nil, fmt.Errorf("-write %s names no branch after its @ -- leave the @ off for the repo's default branch", text)
		}
		out = append(out, ui.WriteTarget{Repo: strings.TrimSpace(repo), Base: strings.TrimSpace(branch)})
	}
	return out, nil
}

func cmdCreate(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain create", flag.ContinueOnError)
	title := fs.String("title", "", "task title (required)")
	body := fs.String("body", "", "task description")
	repo := fs.String("repo", "", "owner/name of the repo this task's work targets")
	// The requester (Task.Creator) of a task filed from this
	// workstation's own CLI (ui.CreateTaskRequest.Author). Metadata
	// only: the daemon still checks the filing against its own actor,
	// and refuses it from a signed-in caller naming somebody else.
	author := fs.String("author", "", "who asked for this task, recorded as its requester (defaults to the daemon's own actor)")
	noRepo := fs.Bool("no-repo", false, "file this task with no repo at all, rather than falling back to the deployment default")
	resolveChangeTarget := bindChangeTargetFlags(fs)
	base := fs.String("base", "", "base branch, if not the target repo's default")
	issue := fs.Int("issue", 0, "GitHub issue number associated with this task")
	var autoMerge bool
	fs.BoolVar(&autoMerge, "auto-merge", false, "merge this task's pull request automatically once it reads clean")
	var capabilities stringList
	fs.Var(&capabilities, "capability", "capability to attach (repeatable): a capability ID, a capability reference "+
		"(https://...[#method]), or a reference with its arguments as ref={json}")
	var reads stringList
	fs.Var(&reads, "read", "owner/name of a repo this task's run may read but never push to (repeatable)")
	var writes stringList
	fs.Var(&writes, "write", "owner/name[@branch] of another repo this task may land work in, by proposing a task against it (repeatable)")
	reviewTemplate := fs.String("review-template", "",
		"ID of the template to file a review from once this task's work is done -- a second agent on the same branch, before the merge")
	// The per-task network override (model.Task.NetworkAccess,
	// grain/task-269). Unset means what it means everywhere else: no
	// override, so the task's sandbox gets whatever the deployment is
	// set to. Validated by ui.CreateTask rather than here, one copy of
	// the vocabulary, at the end that stores it -- and refused there,
	// with a sentence saying why, on a deployment whose sandboxes cannot
	// honour it.
	networkAccess := fs.String("network-access", "",
		"how much of the internet this task's sandbox may reach: "+model.NetworkAccessNames()+
			" -- unset takes the deployment's own default")
	// The per-task model pin (model.Task.AgentBackendID, grain/task-110).
	// Unset is the rotation, which is what nearly every task wants.
	// Validated by ui.CreateTask against the pool as it stands, for the
	// reason -network-access is validated there: the vocabulary is the
	// deployment's own and this end has no copy of it -- and a model
	// disabled in Settings is deliberately still accepted, since naming
	// one by hand is the only way work reaches it.
	agentBackend := fs.String("model", "",
		"ID of the model this task runs on (Settings -> Models), disabled ones included"+
			" -- unset takes whichever model the rotation reaches")
	approve := fs.Bool("approve", false, "queue the task immediately instead of filing it as a proposal awaiting approval")
	position := fs.String("position", "", `which end of the backlog to file it at: "front" to run ahead of everything already queued, "end" to queue behind it. Unset files it wherever the last task added chose, and leaves that choice alone`)
	priority := fs.Int("priority", model.DefaultTaskPriority, "task priority (default 5; higher numbers mean higher priority)")
	interactive := fs.Bool("interactive", false, "file this as a live chat rather than a change to run unattended (bwsalmon/agents#539); implies -approve and dispatches ahead of the backlog")
	var attach stringList
	fs.Var(&attach, "attach", "path to a local file to attach to the task (repeatable)")
	var fields stringList
	fs.Var(&fields, "field", customFieldFlagUsage)
	var dependsOn stringList
	fs.Var(&dependsOn, "depends-on", dependsOnFlagUsage)
	if err := fs.Parse(args); err != nil {
		// -h prints the flags this build has; the fields a value can be
		// given for are the deployment's own, so they are asked for and
		// printed under a heading of their own -- list.go's declaredHelp
		// on why help that fails is worse than help that is short.
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(os.Stderr, declaredFieldHelp(ctx, c))
		}
		return err
	}
	// Every field of a new task is a flag, so a bare word here is a
	// title somebody meant to pass to -title: worth saying so, rather
	// than sending the request without it.
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}

	customFields, _, err := customFieldValues(ctx, c, fields)
	if err != nil {
		return err
	}
	attachments, err := loadAttachments(attach)
	if err != nil {
		return err
	}
	atFront, err := backlogPosition(*position)
	if err != nil {
		return err
	}
	writeTargets, err := writeTargets(writes)
	if err != nil {
		return err
	}
	changeTarget, err := resolveChangeTarget()
	if err != nil {
		return err
	}
	var priorityOpt *int
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "priority" {
			v := *priority
			priorityOpt = &v
		}
	})
	req := ui.CreateTaskRequest{
		ChangeTarget: changeTarget,
		Title:        *title, Description: *body, Repo: *repo, NoRepo: *noRepo, Base: *base,
		Issue:     *issue,
		AutoMerge: autoMerge, Reads: reads, Writes: writeTargets, Approved: *approve, AtFront: atFront,
		Priority:    priorityOpt,
		Interactive: *interactive, Attachments: attachments,
		ReviewTemplateID: *reviewTemplate, CustomFields: customFields,
		NetworkAccess: *networkAccess, AgentBackendID: *agentBackend,
		DependsOn: dependsOn, Author: strings.TrimSpace(*author),
	}
	// Naming any -capability names the whole set (ui.CreateTaskRequest.
	// Capabilities); naming none leaves the field unset, so the task is
	// filed with whatever this deployment attaches by default -- the same
	// answer the UI's own new-task form starts from. To file one with
	// nothing at all on a deployment that defaults something, detach it
	// afterwards (`grain capability <task> <cap> detach`).
	if len(capabilities) > 0 {
		ids := []string(capabilities)
		// A reference's ref={json} form is checked here, so a mistyped
		// argument object is refused before anything is filed; its
		// server checks the arguments themselves when the task is.
		for _, id := range ids {
			if model.IsCapabilityRef(id) {
				if err := model.CheckCapabilityGrantSyntax(id); err != nil {
					return fmt.Errorf("-capability: %w", err)
				}
			}
		}
		req.Capabilities = &ids
	}

	task, err := c.CreateTask(ctx, req)
	if err != nil {
		return err
	}
	out.task(task)
	if note := unapprovedNote(*approve, task); note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	return nil
}

// unapprovedNote is what `grain create -approve` says when the task came
// back unapproved anyway -- the daemon files some tasks as proposals,
// whatever the request asked for. "state: proposed" alone read like the flag had been dropped. It
// goes to stderr, so -json output stays one object.
func unapprovedNote(asked bool, t ui.Task) string {
	if !asked || t.Approved || t.State != model.StateProposed {
		return ""
	}
	return fmt.Sprintf("grain: task %s was filed as a proposal, not approved: it waits for a human to approve it", t.ID)
}

// backlogPosition turns `grain create -position` into
// ui.CreateTaskRequest.AtFront: the two ends a new task can join are
// "front" (ahead of everything already queued, dispatched next) and
// "end" (behind it, the FIFO backlog grain defaults to).
//
// An unset flag is nil rather than false -- "no opinion", which files
// the task at whichever end the last one filed with an opinion chose and
// leaves that memory as it is (that field's own doc comment). Naming
// either end both files this task there and makes it the default the
// next one starts from, which is the same thing the UI's own new-task
// form does with its picker.
func backlogPosition(v string) (*bool, error) {
	switch v {
	case "":
		return nil, nil
	case "front":
		front := true
		return &front, nil
	case "end":
		end := false
		return &end, nil
	}
	return nil, fmt.Errorf(`-position must be "front" or "end", not %q`, v)
}

func cmdUpdate(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain update", flag.ContinueOnError)
	title := fs.String("title", "", "new task title")
	body := fs.String("body", "", "new task description")
	repo := fs.String("repo", "", "new target repo (owner/name)")
	base := fs.String("base", "", "new base branch")
	issue := fs.Int("issue", 0, "new associated GitHub issue number")
	var autoMerge bool
	fs.BoolVar(&autoMerge, "auto-merge", false, "whether the task auto-merges once clean")
	var reads stringList
	fs.Var(&reads, "read", "owner/name of a repo this task's run may read but never push to (repeatable) -- replaces the whole set")
	var writes stringList
	fs.Var(&writes, "write", "owner/name[@branch] of another repo this task may land work in (repeatable) -- replaces the whole set")
	// Empty is a real value, not an unset one: it detaches whatever
	// review the task already had (ui.UpdateTaskRequest.ReviewTemplateID),
	// which is why this is read through fs.Visit like every other field
	// here rather than by comparing against "".
	reviewTemplate := fs.String("review-template", "",
		"ID of the template to file a review from once this task's work is done -- empty detaches the review it has")
	// The model and the connection pin, the same pair the task pane's Edit
	// form saves (ui.UpdateTaskRequest.Model and .AgentBackendID). This is
	// how a task is moved onto another model after it was filed -- most
	// often because the one it was on met its usage limit -- without
	// having to file it again. Empty is a real edit for both, read through
	// fs.Visit like -review-template: -model "" hands the task back to the
	// deployment's default, -connection "" takes the pin off so the rotation
	// picks again. Either reaches the task's next run only; one already
	// dispatched keeps what it started on.
	agentModel := fs.String("model", "",
		"model this task's next run asks for (Settings -> Models) -- empty takes the deployment's default")
	connection := fs.String("connection", "",
		"ID of the one connection this task's next run must use -- empty takes the pin off; "+
			"pass it with -model when the pinned connection does not serve the new model")
	priority := fs.Int("priority", model.DefaultTaskPriority, "new task priority (default 5; higher numbers mean higher priority)")
	var fields stringList
	fs.Var(&fields, "field", customFieldFlagUsage)
	// Unlike -read and -write these add to and take from the set the
	// task already has rather than replacing it -- the UI picker's own
	// add/remove buttons, one POST /api/tasks/{id}/depends-on each.
	var dependsOn, noDependsOn stringList
	fs.Var(&dependsOn, "depends-on", dependsOnFlagUsage)
	fs.Var(&noDependsOn, "no-depends-on", "task this one should no longer wait on, in any form -depends-on takes (repeatable)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(os.Stderr, declaredFieldHelp(ctx, c))
		}
		return err
	}
	if err := atMostArgs(fs, rest, 1); err != nil {
		return err
	}
	id, err := taskID(first(rest))
	if err != nil {
		return err
	}

	// Parsed before fs.Visit so a malformed -write fails the command
	// rather than being reported from inside a callback that cannot
	// return an error.
	writeTargets, err := writeTargets(writes)
	if err != nil {
		return err
	}
	// Likewise the fields, and for a second reason: what an edit sends
	// for them is the task's own values with these written over the top,
	// which takes a request of its own (mergedCustomFields).
	customFields, err := updatedCustomFields(ctx, c, id, fields)
	if err != nil {
		return err
	}

	var req ui.UpdateTaskRequest
	edits := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "depends-on", "no-depends-on", "field":
		default:
			edits = true
		}
		switch f.Name {
		case "title":
			v := *title
			req.Title = &v
		case "body":
			v := *body
			req.Description = &v
		case "repo":
			v := *repo
			req.Repo = &v
		case "base":
			v := *base
			req.Base = &v
		case "issue":
			v := *issue
			req.Issue = &v
		case "auto-merge":
			v := autoMerge
			req.AutoMerge = &v
		case "read":
			v := []string(reads)
			req.Reads = &v
		case "write":
			v := writeTargets
			req.Writes = &v
		case "review-template":
			v := *reviewTemplate
			req.ReviewTemplateID = &v
		case "model":
			v := strings.TrimSpace(*agentModel)
			req.Model = &v
		case "connection":
			v := strings.TrimSpace(*connection)
			req.AgentBackendID = &v
		case "priority":
			v := *priority
			req.Priority = &v
		}
	})
	// Not read through fs.Visit like the rest: a -field is only ever
	// written with a value, so the flags themselves say whether this
	// edit has an opinion about any field, and the merged map is
	// already the whole answer when it does.
	if customFields != nil {
		req.CustomFields = &customFields
		edits = true
	}

	// Detaches first, so `-no-depends-on 3 -depends-on 4` swaps one for
	// the other; each is refused (or not) by the server on its own, and a
	// refusal stops the command before the rest of the edit is sent.
	for _, dep := range noDependsOn {
		if err := c.SetDependency(ctx, id, dep, false); err != nil {
			return fmt.Errorf("-no-depends-on %s: %w", dep, err)
		}
	}
	for _, dep := range dependsOn {
		if err := c.SetDependency(ctx, id, dep, true); err != nil {
			return fmt.Errorf("-depends-on %s: %w", dep, err)
		}
	}
	if !edits && (len(dependsOn) > 0 || len(noDependsOn) > 0) {
		return respond(ctx, c, out, id)
	}

	task, err := c.UpdateTask(ctx, id, req)
	if err != nil {
		return err
	}
	out.task(task)
	return nil
}

// dependsOnFlagUsage is -depends-on's help on both create and update.
// The forms are normalized by the server (model.NormalizeTaskID), not
// here, so the CLI accepts exactly what the UI picker and the API do.
const dependsOnFlagUsage = "task this one must wait on before it is dispatched -- 12, #12, task-12, grain/task-12 " +
	"or the task's page URL (repeatable)"

func cmdApprove(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain approve", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	if err := c.Approve(ctx, id); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

// cmdDefer and cmdUndefer put a task aside and pick it back up --
// ui.Client.Defer's own doc comment for which states it applies to, and
// for why deferring is not the same act as withdrawing approval (the
// approval survives, so a task deferred off the queue comes back onto
// it).
func cmdDefer(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain defer", flag.ContinueOnError)
	until := fs.String("until", "", "wake time: pick this task back up on its own at or after this instant "+
		"(a date, 2026-09-14, or an RFC 3339 time). Omitted, the task stays aside until you undefer it.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	opts, err := deferOptions(*until)
	if err != nil {
		return err
	}
	if err := c.Defer(ctx, id, opts); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

// deferOptions turns -until into ui.DeferOptions, accepting a bare date
// as well as a full RFC 3339 time: "not before 2026-09-14" is what
// somebody actually has in mind when they defer a task, and making them
// spell out a midnight and an offset to say it would be a worse command
// for the common case.
//
// A bare date means midnight local -- the start of that day where the
// person typing it is -- which is the reading that matches the words. A
// full timestamp is taken exactly as given, offset included, for the
// caller who does mean a particular instant.
func deferOptions(until string) (ui.DeferOptions, error) {
	if until == "" {
		return ui.DeferOptions{}, nil
	}
	if t, err := time.ParseInLocation(time.DateOnly, until, time.Local); err == nil {
		return ui.DeferOptions{Until: &t}, nil
	}
	t, err := time.Parse(time.RFC3339, until)
	if err != nil {
		return ui.DeferOptions{}, fmt.Errorf("-until %q: want a date (2026-09-14) or an RFC 3339 time (2026-09-14T09:00:00Z)", until)
	}
	return ui.DeferOptions{Until: &t}, nil
}

func cmdUndefer(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain undefer", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	if err := c.Undefer(ctx, id); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

// cmdWithdrawApproval is cmdApprove's undo -- Client.WithdrawApproval's
// own doc comment for what it does and what it refuses.
func cmdWithdrawApproval(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain withdraw", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	if err := c.WithdrawApproval(ctx, id); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

func cmdCapability(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain capability", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 3 {
		return errors.New("usage: grain capability <id> <capability-id> attach|detach")
	}
	if err := atMostArgs(fs, fs.Args(), 3); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	capability := fs.Arg(1)
	var attach bool
	switch fs.Arg(2) {
	case "attach":
		attach = true
	case "detach":
		attach = false
	default:
		return fmt.Errorf("third argument must be attach or detach, got %q", fs.Arg(2))
	}
	if err := c.SetCapability(ctx, id, capability, attach); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

func cmdComment(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain comment", flag.ContinueOnError)
	var attach stringList
	fs.Var(&attach, "attach", "path to a local file to attach to the comment (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: grain comment [-attach path]... <id> [body...]")
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	// Go's flag package stops at the first non-flag argument, so
	// `grain comment 3 -attach shot.png "look at this"` parses no flags
	// at all: it files a comment whose body is the literal words
	// "-attach shot.png look at this", and the file is silently not
	// attached (found by hand, task 244). A bare body word that names
	// one of this command's own flags is that mistake far more often
	// than it is prose -- and prose survives it, since a quoted body is
	// one argument and never looks like a flag.
	if name, ok := misplacedFlag(fs, fs.Args()[1:]); ok {
		return fmt.Errorf("-%s has to come before the task id: grain comment -%s <path> %s <body>", name, name, fs.Arg(0))
	}
	body := strings.Join(fs.Args()[1:], " ")
	attachments, err := loadAttachments(attach)
	if err != nil {
		return err
	}
	if body == "" && len(attachments) == 0 {
		return errors.New("usage: grain comment [-attach path]... <id> [body...] -- a body, an attachment, or both is required")
	}
	if err := c.AddComment(ctx, id, body, attachments); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

// loadAttachments reads each path in paths off local disk and returns it
// as the base64 upload CreateTaskRequest/addCommentRequest send over the
// wire -- grain create/comment's own -attach flag's encoding step, so a
// human filing a task from the CLI can hand the agent a screenshot or a
// repro zip the same way the web UI's file picker does.
func loadAttachments(paths []string) ([]ui.AttachmentUpload, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	out := make([]ui.AttachmentUpload, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("reading attachment %s: %w", p, err)
		}
		contentType := mime.TypeByExtension(filepath.Ext(p))
		if contentType == "" {
			contentType = http.DetectContentType(data)
		}
		out = append(out, ui.AttachmentUpload{
			Filename:    filepath.Base(p),
			ContentType: contentType,
			Content:     base64.StdEncoding.EncodeToString(data),
		})
	}
	return out, nil
}

func cmdClose(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain close", flag.ContinueOnError)
	// The same choice the UI puts in a checkbox beside its own Close
	// button, and the same default: off. A task's pull request is real
	// work, so closing it happens because somebody typed this and not
	// because they closed a task (ui.CloseOptions).
	closePR := fs.Bool("close-pull-request", false,
		"close the task's pull request on GitHub too, without merging it "+
			"(the branch and its commits are left untouched)")
	reason := fs.String("reason", "", "why, kept with the close in the task's history")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := atMostArgs(fs, rest, 1); err != nil {
		return err
	}
	id, err := taskID(first(rest))
	if err != nil {
		return err
	}
	if err := c.Close(ctx, id, ui.CloseOptions{ClosePullRequest: *closePR, Reason: *reason}); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

func cmdReopen(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain reopen", flag.ContinueOnError)
	reason := fs.String("reason", "", "why, kept with the reopen in the task's history")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := atMostArgs(fs, rest, 1); err != nil {
		return err
	}
	id, err := taskID(first(rest))
	if err != nil {
		return err
	}
	if err := c.ReopenBecause(ctx, id, *reason); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

// cmdRetry is `grain retry <id>`: the only way to clear a task's own
// failure streak once it has reached model.MaxConsecutiveFailures and
// state has stopped calling it "queued" at all (bwsalmon/agents#403).
func cmdRetry(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain retry", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	if err := c.Retry(ctx, id); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

// cmdRetryMerge is `grain retry-merge <id>`: the shell's half of the
// UI's own "Retry merge" button, and the only way to ask the merge queue
// for another attempt at a pull request it has already given up on (see
// ui.Client.RetryMerge).
func cmdRetryMerge(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain retry-merge", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	if err := c.RetryMerge(ctx, id); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

// cmdForceMerge is `grain force-merge <id> <reason>`: the shell's half
// of the UI's "Force merge" button, and the one sanctioned exception to
// the merge predicate -- grain lands the change itself, on an operator's
// say-so, with the reason recorded on the task and on the pull request
// (see ui.Client.ForceMerge).
//
// The reason is an argument rather than a flag because it is not
// optional: a force merge without one is refused at the other end, and a
// required flag reads like something that could be left out.
//
// -head names the commit that was reviewed, and the override is for that
// commit only: grain refuses it if the branch is somewhere else, and
// withdraws it if anything is pushed before the merge. Without it the
// override is for the pull request's head as it is when grain receives
// the request.
func cmdForceMerge(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain force-merge", flag.ContinueOnError)
	head := fs.String("head", "", "the head commit you reviewed; the override is for it alone")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 2); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	reason := strings.TrimSpace(strings.Join(fs.Args()[1:], " "))
	if reason == "" {
		return fmt.Errorf("say why: grain force-merge <id> \"the release is broken and this is the fix\"")
	}
	if err := c.ForceMerge(ctx, id, reason, *head); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}

// cmdRunNow is `grain run-now <id>`: the shell's half of the UI's "Run
// now" button. It jumps the queue and ignores the state machine -- a
// blocked, deferred, failed or already-finished task is started all the
// same -- which is the only way to hand a task whose work is over, its
// pull request sitting on the merge queue, back to an agent for more of
// it. See ui.Client.RunNow.
func cmdRunNow(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain run-now", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id, err := taskID(fs.Arg(0))
	if err != nil {
		return err
	}
	if err := c.RunNow(ctx, id); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}
