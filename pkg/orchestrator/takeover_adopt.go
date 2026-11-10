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
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Adopting a pull request over the API: POST /api/pull-requests/adopt
// (`grain pr adopt`), POST /api/pull-requests/review (`grain pr review`)
// and GET /api/pull-requests/status (`grain pr status`) -- external-cli
// spec #XC-28 and #XC-29. An adopted pull request gets the same shepherd
// the "@<slug> take over" command files (takeover.go), with two things
// the command never had: a mode, and the documents the change says it
// implements.
//
// A review-only shepherd (model.TakeoverReviewOnly) never pushes. grain
// reviews the pull request's head, holds model.LinkTakeoverCleared until
// those reviews have found nothing and CI is green at that head, and the
// merge queue takes it from there; findings are the author's to fix. A
// finish shepherd is what the command files: it fixes findings and red CI
// by pushing onto the branch. Asking for finish on a review-only shepherd
// upgrades it in place.

// takeoverModeOf is task's mode; a shepherd with none recorded was filed
// by the GitHub command, which always finished.
func takeoverModeOf(task model.Task) model.TakeoverMode {
	if model.TakeoverMode(linkTarget(task, model.LinkTakeoverMode)) == model.TakeoverReviewOnly {
		return model.TakeoverReviewOnly
	}
	return model.TakeoverFinish
}

// designReviewInstructions is what `grain pr review --design` adds to the
// reviewer's prompt.
const designReviewInstructions = "This is a design review. Check the change against the design folder's " +
	"conventions in `design/spec/contributions/contributions.spec.md` (where designs and specs live, how they " +
	"are named, their status lines and requirement anchors), and look through `design/` for existing designs " +
	"or specs this one overlaps or contradicts: name each one you find, and say whether this change should " +
	"extend it instead.\n\n"

// conformanceInstructions asks a review to check the change against the
// spec and design it says it implements; "" when it names neither.
func conformanceInstructions(spec, design string) string {
	var docs []string
	if spec != "" {
		docs = append(docs, "the specification `"+spec+"`")
	}
	if design != "" {
		docs = append(docs, "the design `"+design+"`")
	}
	if len(docs) == 0 {
		return ""
	}
	return fmt.Sprintf("This change implements %s. Read it in the repository and verify conformance: every "+
		"requirement it states that this change touches is met, nothing here contradicts it, and the tests it "+
		"requires exist and pass. Record each gap as a finding that names the requirement.\n\n",
		strings.Join(docs, " and "))
}

// docsFromPRBody reads the "Implements: <path>" and "Design: <path>"
// lines of a pull request's description. Anything that is not a clean
// path under design/ is ignored: the description is external content
// and may only name a document, never anything else.
func docsFromPRBody(body string) (spec, design string) {
	for _, line := range strings.Split(body, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "`")
		clean, err := model.CleanDesignPath(value)
		if err != nil || clean == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "implements":
			if spec == "" {
				spec = clean
			}
		case "design":
			if design == "" {
				design = clean
			}
		}
	}
	return spec, design
}

func conflictf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", model.ErrPullRequestConflict, fmt.Sprintf(format, a...))
}

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", model.ErrPullRequestInvalid, fmt.Sprintf(format, a...))
}

// readPullRequestErr says why reading ref from GitHub failed. A 404 is
// the caller naming a pull request that does not exist (or that this
// deployment's credential cannot see) -- model.ErrPullRequestNotFound,
// which the routes answer 404 -- not grain failing with a 500.
func readPullRequestErr(ref model.PullRequestRef, err error) error {
	if github.IsNotFound(err) {
		return fmt.Errorf("%w: GitHub has no %s that this deployment's credential can see: %w", model.ErrPullRequestNotFound, ref, err)
	}
	return fmt.Errorf("reading %s: %w", ref, err)
}

// openPullRequestDetail reads ref, refusing one that is no longer open.
func openPullRequestDetail(deps Deps, ref model.PullRequestRef) (github.PullRequestDetail, error) {
	if deps.Client == nil {
		return github.PullRequestDetail{}, errors.New("this deployment has no GitHub client to read pull requests through")
	}
	detail, err := deps.Client.GetPullRequest(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		return github.PullRequestDetail{}, readPullRequestErr(ref, err)
	}
	if detail.Merged || !strings.EqualFold(detail.State, "open") {
		return detail, conflictf("%s is no longer open", ref)
	}
	return detail, nil
}

// AdoptPullRequest files a shepherd for in.PR -- or, when one already
// holds it, updates that one in place -- and reports it.
func AdoptPullRequest(ctx context.Context, deps Deps, in model.AdoptPullRequestInput, now time.Time) (model.PullRequestAdoption, error) {
	mode := in.Mode
	if mode == "" {
		mode = model.TakeoverReviewOnly
	}
	if _, err := model.ParseTakeoverMode(string(mode)); err != nil {
		return model.PullRequestAdoption{}, invalidf("%v", err)
	}
	spec, err := model.CleanDesignPath(in.Spec)
	if err != nil {
		return model.PullRequestAdoption{}, invalidf("spec: %v", err)
	}
	design, err := model.CleanDesignPath(in.Design)
	if err != nil {
		return model.PullRequestAdoption{}, invalidf("design: %v", err)
	}
	ref := in.PR
	owner, err := takeoverOwner(ctx, deps.Store, ref)
	if err != nil {
		return model.PullRequestAdoption{}, err
	}
	if owner != nil {
		if !isTakeoverTask(*owner) {
			return model.PullRequestAdoption{}, conflictf("%s is grain task %s's own pull request; there is nothing to adopt", ref, owner.ID)
		}
		return updateAdoptedShepherd(ctx, deps, *owner, in, mode, spec, design, now)
	}
	detail, err := openPullRequestDetail(deps, ref)
	if err != nil {
		return model.PullRequestAdoption{}, err
	}
	bodySpec, bodyDesign := docsFromPRBody(detail.Body)
	if spec == "" {
		spec = bodySpec
	}
	if design == "" {
		design = bodyDesign
	}
	trustCache := map[string]bool{}
	authorTrusted, err := senderTrusted(deps.Client, trustCache, ref.Repo, detail.AuthorLogin)
	if err != nil {
		return model.PullRequestAdoption{}, err
	}
	task, err := fileAdoptedShepherd(ctx, deps, in, detail, mode, spec, design, authorTrusted, now)
	if err != nil {
		return model.PullRequestAdoption{}, err
	}
	if in.Approved {
		if err := settleTakeover(ctx, deps, task.ID, trustCache, now); err != nil {
			return model.PullRequestAdoption{}, err
		}
	}
	return adoptionOf(ctx, deps.Store, task.ID, ref, false, false)
}

func adoptionOf(ctx context.Context, store *model.Store, id string, ref model.PullRequestRef,
	existing, upgraded bool) (model.PullRequestAdoption, error) {

	task, err := store.GetTask(ctx, id)
	if err != nil || task == nil {
		return model.PullRequestAdoption{}, err
	}
	state, err := store.State(ctx, id)
	if err != nil {
		return model.PullRequestAdoption{}, err
	}
	return model.PullRequestAdoption{
		TaskID: id, State: string(state), Mode: string(takeoverModeOf(*task)), PR: ref.String(),
		Existing: existing, Upgraded: upgraded, AutoMerge: task.AutoMerge,
		Spec: linkTarget(*task, model.LinkImplementsSpec), Design: linkTarget(*task, model.LinkDesignDoc),
	}, nil
}

func fileAdoptedShepherd(ctx context.Context, deps Deps, in model.AdoptPullRequestInput, detail github.PullRequestDetail,
	mode model.TakeoverMode, spec, design string, authorTrusted bool, now time.Time) (model.Task, error) {

	ref := in.PR
	repo := ref.Repo
	id, err := deps.Store.NewTaskID(ctx)
	if err != nil {
		return model.Task{}, fmt.Errorf("allocating takeover task id: %w", err)
	}
	orderKey, err := deps.Store.OrderKeyForNewTask(ctx, false)
	if err != nil {
		return model.Task{}, fmt.Errorf("allocating order key: %w", err)
	}
	grants, err := defaultRepoGrants(ctx, deps.Store, repo)
	if err != nil {
		return model.Task{}, err
	}

	own, branch := ref, detail.HeadRef
	replaced := false
	if mode == model.TakeoverReviewOnly {
		// Nothing is pushed, so nothing needs copying: the shepherd holds
		// the pull request itself and works on no branch. Never the
		// author's head, even where grain could push it: the git proxy
		// lets a sandbox push its task's Branch, and review-only's promise
		// not to push must not rest on the prompt alone. An upgrade to
		// finish sets the branch (updateAdoptedShepherd).
		branch = ""
	} else if !headPushable(deps.Client, repo, detail) {
		switch {
		case !in.Approved:
			// As for the command: the replacement is made on approval.
			branch = ""
		default:
			if own, branch, err = openReplacementPR(deps.Client, ref, detail, id); err != nil {
				return model.Task{}, err
			}
			replaced = true
		}
	}

	origin := model.Origin{Attribution: in.Requester, Reason: model.ReasonDirect}
	var approval *model.Attribution
	var approvedAt *time.Time
	if in.Approved {
		a := in.Requester
		if in.Approval != nil {
			a = *in.Approval
		}
		approval, approvedAt = &a, &now
	} else {
		origin.Reason = model.ReasonProposal
	}
	links := []model.Link{
		{Kind: model.LinkTakeover, Target: ref.String()},
		{Kind: model.LinkTakeoverMode, Target: string(mode)},
	}
	if branch != "" || mode == model.TakeoverReviewOnly {
		links = append(links, model.Link{Kind: model.LinkFixes, Target: own.String()})
	}
	if replaced {
		links = append(links, model.Link{Kind: model.LinkExternalPR, Target: ref.String()})
	}
	if spec != "" {
		links = append(links, model.Link{Kind: model.LinkImplementsSpec, Target: spec})
	}
	if design != "" {
		links = append(links, model.Link{Kind: model.LinkDesignDoc, Target: design})
	}
	verb := "Adopt"
	if mode == model.TakeoverFinish {
		verb = "Take over"
	}
	title := fmt.Sprintf("%s %s", verb, ref)
	if t := strings.TrimSpace(detail.Title); t != "" {
		title = fmt.Sprintf("%s %s: %s", verb, ref, t)
	}
	task := model.Task{
		ID:         id,
		Branch:     branch,
		Intent:     model.IntentImplement,
		Origin:     origin,
		Approval:   approval,
		ApprovedAt: approvedAt,
		Title:      title,
		Body:       adoptTaskBody(ref, own, detail, requesterName(in.Requester), branch, replaced, mode, spec, design),
		Target:     &repo,
		Binding:    model.BindingDirective,
		Base:       detail.BaseRef,
		WorkOrigin: model.GitHubOrigin(repo, ref.Number),
		Grants:     grants,
		Links:      links,
		CreatedAt:  &now,
		OrderKey:   orderKey,
		// takeover's rule, with an unlimited human principal in the
		// maintainer's place: an approved request by an unlimited human, of a
		// pull request whose author is a collaborator. Limited roles, agents,
		// and external credentials never turn auto-merge on. The
		// trust-boundary and path-trigger holds still withdraw it where a
		// person must Submit.
		AutoMerge: in.Approved && in.AllowAutoMerge && in.Requester.Actor.Kind == model.PrincipalHuman && authorTrusted,
	}
	if err := deps.Store.PutTask(ctx, task); err != nil {
		return model.Task{}, fmt.Errorf("filing takeover task %s: %w", task.ID, err)
	}
	if err := deps.Store.SetTaskGoal(ctx, task.ID, model.GoalDecl{
		Kind: model.GoalPRMerged, Spec: model.GoalSpec{Task: task.ID},
	}, "automation:pull-request-adopt", now); err != nil {
		log.Printf("orchestrator: setting adopted task %s's pr-merged goal: %v", task.ID, err)
	}
	log.Printf("orchestrator: adopted %s as task %s (mode=%s, approved=%v)", ref, task.ID, mode, in.Approved)
	return task, nil
}

func requesterName(a model.Attribution) string {
	if a.OnBehalfOf != nil && a.OnBehalfOf.ID != "" {
		return a.OnBehalfOf.ID
	}
	return a.Actor.ID
}

// updateAdoptedShepherd is AdoptPullRequest on a pull request a shepherd
// already holds: it records any newly named spec or design, upgrades a
// review-only shepherd asked for finish, and approves a proposed one when
// the request itself is approved. A finish shepherd is never downgraded.
func updateAdoptedShepherd(ctx context.Context, deps Deps, task model.Task, in model.AdoptPullRequestInput,
	mode model.TakeoverMode, spec, design string, now time.Time) (model.PullRequestAdoption, error) {

	ref := in.PR
	upgrade := takeoverModeOf(task) == model.TakeoverReviewOnly && mode == model.TakeoverFinish
	if upgrade && task.Approval != nil && !in.Approved {
		// Finish lets the shepherd push onto the author's branch, which
		// whoever approved the review-only shepherd never agreed to: a
		// request that would itself land proposed may not grant it.
		return model.PullRequestAdoption{}, fmt.Errorf("%w: upgrading approved shepherd %s of %s to finish "+
			"needs an approved request (a person, or tasks:admin on %s)", model.ErrPullRequestForbidden, task.ID, ref, ref.Repo)
	}
	if upgrade && task.AutoMerge && (!in.AllowAutoMerge || in.Requester.Actor.Kind != model.PrincipalHuman) {
		return model.PullRequestAdoption{}, fmt.Errorf("%w: upgrading auto-merge shepherd %s of %s to finish "+
			"requires an unlimited role (grain/operator)", model.ErrPullRequestForbidden, task.ID, ref)
	}
	if task.Approval == nil && in.Approved {
		if in.CheckApprove != nil {
			if err := in.CheckApprove(ctx, task); err != nil {
				return model.PullRequestAdoption{}, err
			}
		} else if in.Approval != nil && in.Approval.Actor == model.OwnerTrustPrincipal {
			return model.PullRequestAdoption{}, fmt.Errorf("%w: owner-trust only approves new filings, not existing proposed shepherd %s of %s",
				model.ErrPullRequestForbidden, task.ID, ref)
		}
	}
	var detail github.PullRequestDetail
	pushable := false
	if upgrade {
		var err error
		if detail, err = openPullRequestDetail(deps, ref); err != nil {
			return model.PullRequestAdoption{}, err
		}
		pushable = headPushable(deps.Client, ref.Repo, detail)
	}
	if err := deps.Store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		if spec != "" {
			t.Links = append(withoutLinks(t.Links, model.LinkImplementsSpec), model.Link{Kind: model.LinkImplementsSpec, Target: spec})
		}
		if design != "" {
			t.Links = append(withoutLinks(t.Links, model.LinkDesignDoc), model.Link{Kind: model.LinkDesignDoc, Target: design})
		}
		if !upgrade {
			return nil
		}
		t.Links = append(withoutLinks(t.Links, model.LinkTakeoverMode),
			model.Link{Kind: model.LinkTakeoverMode, Target: string(model.TakeoverFinish)})
		if pushable {
			t.Branch = detail.HeadRef
		} else if linkTarget(*t, model.LinkExternalPR) == "" {
			// A fork's head: drop the original as the shepherd's own pull
			// request, and settleTakeover opens the replacement it can
			// push to (on approval, as for the command).
			t.Branch = ""
			t.Links = withoutLinks(t.Links, model.LinkFixes)
		}
		t.Title = strings.Replace(t.Title, "Adopt ", "Take over ", 1)
		t.Body = adoptTaskBody(ref, ref, detail, requesterName(t.Origin.Attribution), t.Branch, false,
			model.TakeoverFinish, linkTarget(*t, model.LinkImplementsSpec), linkTarget(*t, model.LinkDesignDoc))
		return nil
	}); err != nil {
		return model.PullRequestAdoption{}, err
	}
	if task.Approval == nil && in.Approved {
		if err := deps.Store.Approve(ctx, task.ID, in.Requester, now); err != nil {
			return model.PullRequestAdoption{}, err
		}
	}
	if upgrade {
		state, err := deps.Store.State(ctx, task.ID)
		if err != nil {
			return model.PullRequestAdoption{}, err
		}
		if state == model.StateCompleted || state == model.StateAwaitingSubmit || state == model.StateAwaitingReply {
			current, err := deps.Store.GetTask(ctx, task.ID)
			if err != nil || current == nil {
				return model.PullRequestAdoption{}, err
			}
			if err := deliverReplyAndWakeTask(ctx, deps.Store, *current, state, false, "grain",
				"This shepherd was upgraded from review-only to finish: fix the open review findings and red CI "+
					"by pushing new commits onto the pull request's branch (fetch first; never force-push).",
				"takeover-upgrade:"+task.ID+"@"+now.UTC().Format(time.RFC3339Nano), true, now); err != nil {
				return model.PullRequestAdoption{}, err
			}
		}
	}
	if current, err := deps.Store.GetTask(ctx, task.ID); err == nil && current != nil && current.Approval != nil && deps.Client != nil {
		if err := settleTakeover(ctx, deps, task.ID, map[string]bool{}, now); err != nil {
			return model.PullRequestAdoption{}, err
		}
	}
	return adoptionOf(ctx, deps.Store, task.ID, ref, true, upgrade)
}

func adoptTaskBody(ref, own model.PullRequestRef, detail github.PullRequestDetail, requester, branch string,
	replaced bool, mode model.TakeoverMode, spec, design string) string {

	var b strings.Builder
	if mode == model.TakeoverReviewOnly {
		fmt.Fprintf(&b, "Adopt pull request %s in review-only mode and see it to merge. Requested by %s.\n\n", ref, requester)
		b.WriteString("How this works:\n\n" +
			"- grain has filed reviews of the pull request as it stands (a code review, and a security review where " +
			"one is required). Your first run starts once they finish.\n" +
			"- This is review-only: do not push to the pull request's branch, do not open another pull request, " +
			"and do not commit anything. The author fixes findings; each push moves the head and grain reviews " +
			"it again.\n" +
			"- grain holds the merge until every review has passed and CI is green at the head it would merge, " +
			"then submits it to the merge queue. Summarize where the reviews and CI stand with " +
			"`comment_on_issue`, and end your run.\n\n")
	} else {
		fmt.Fprintf(&b, "Take over pull request %s and shepherd it to merge. Requested by %s.\n\n", ref, requester)
		switch {
		case replaced:
			fmt.Fprintf(&b, "grain cannot push to %s's head branch, so it copied it to `%s` and opened %s from it. "+
				"Work on `%s`. Every commit you make must end with a `Co-authored-by: %s "+
				"<%s@users.noreply.github.com>` trailer crediting the original author.\n\n",
				ref, branch, own, branch, detail.AuthorLogin, detail.AuthorLogin)
		case branch != "":
			fmt.Fprintf(&b, "Work on the pull request's own head branch, `%s`, merging into `%s`.\n\n", branch, detail.BaseRef)
		default:
			fmt.Fprintf(&b, "grain could not push to %s's head branch when this was filed; open a pull request from "+
				"a copy of it that links %s and credits its author.\n\n", ref, ref)
		}
		b.WriteString("How this works:\n\n" +
			"- grain has filed reviews of the pull request as it stands. Read their findings in the pull request's " +
			"review threads and either fix each one or answer it in its thread.\n" +
			"- grain's merge queue refuses this pull request until every review has passed at the head it would " +
			"merge. Each push moves the head, and grain reviews it again.\n" +
			"- Never force-push and never rewrite commits that are already on the branch. The author may push too: " +
			"fetch first and build on top of their commits.\n" +
			"- Then make CI pass, resolve conflicts with the base branch, and answer reviewers.\n\n")
	}
	if spec != "" || design != "" {
		b.WriteString("Provenance: this change implements")
		if spec != "" {
			fmt.Fprintf(&b, " spec `%s`", spec)
		}
		if design != "" {
			if spec != "" {
				b.WriteString(" and")
			}
			fmt.Fprintf(&b, " design `%s`", design)
		}
		b.WriteString(". grain's reviews check the change against it and the tests it requires.\n\n")
	}
	if body := strings.TrimSpace(detail.Body); body != "" {
		b.WriteString(quoteExternalContent("Pull request description", body))
	}
	return b.String()
}

// ReviewPullRequest files one review of in.PR, the API's "@<slug> review".
func ReviewPullRequest(ctx context.Context, deps Deps, in model.ReviewPullRequestInput, now time.Time) (model.PullRequestReviewFiling, error) {
	spec, err := model.CleanDesignPath(in.Spec)
	if err != nil {
		return model.PullRequestReviewFiling{}, invalidf("spec: %v", err)
	}
	ref := in.PR
	detail, err := openPullRequestDetail(deps, ref)
	if err != nil {
		return model.PullRequestReviewFiling{}, err
	}
	bodySpec, bodyDesign := docsFromPRBody(detail.Body)
	if spec == "" {
		spec = bodySpec
	}
	requester := in.Requester
	id, err := fileExternalPRReview(ctx, deps, externalReview{
		repo: ref.Repo, number: ref.Number, prTitle: detail.Title, prBody: detail.Body,
		headRef: detail.HeadRef, headSHA: detail.HeadSHA, baseRef: detail.BaseRef,
		sender: requesterName(requester), trusted: in.Approved, requester: &requester,
		eventKey: "api-review:" + ref.String() + "@" + now.UTC().Format(time.RFC3339Nano),
		spec:     spec, design: bodyDesign, designReview: in.Design,
	}, now)
	if err != nil {
		return model.PullRequestReviewFiling{}, err
	}
	state, err := deps.Store.State(ctx, id)
	if err != nil {
		return model.PullRequestReviewFiling{}, err
	}
	return model.PullRequestReviewFiling{TaskID: id, State: string(state), PR: ref.String(), Design: in.Design, Spec: spec}, nil
}

// latestCIRun is the newest grain CI run of headRef in repo -- the newest
// at head when there is one -- or nil.
func latestCIRun(ctx context.Context, store *model.Store, repo model.RepoRef, headRef, head string) (*model.CIRun, error) {
	if headRef == "" {
		return nil, nil
	}
	stamps, err := store.MatchCIRuns(ctx, model.CIRunQuery{Repo: repo.String(), Ref: headRef})
	if err != nil || len(stamps) == 0 {
		return nil, err
	}
	if len(stamps) > 50 {
		stamps = stamps[:50]
	}
	ids := make([]string, len(stamps))
	for i, s := range stamps {
		ids[i] = s.ID
	}
	runs, err := store.ListCIRunsByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := map[string]model.CIRun{}
	for _, r := range runs {
		if strings.EqualFold(r.Ref, headRef) || strings.HasSuffix(r.Ref, "/"+headRef) {
			byID[r.ID] = r
		}
	}
	var newest *model.CIRun
	for _, id := range ids {
		r, ok := byID[id]
		if !ok {
			continue
		}
		if head != "" && r.Commit == head {
			return &r, nil
		}
		if newest == nil {
			newest = &r
		}
	}
	return newest, nil
}

// takeoverCIGreen reports whether CI has passed at head. A repository
// that does not run grain CI is gated on its own checks by the merge
// queue, so grain has nothing more to hold it for here.
func takeoverCIGreen(ctx context.Context, store *model.Store, repo model.RepoRef, headRef, head string) (bool, error) {
	usesGrainCI, err := store.RepoUsesGrainCI(ctx, repo)
	if err != nil || !usesGrainCI {
		return err == nil, err
	}
	run, err := latestCIRun(ctx, store, repo, headRef, head)
	if err != nil || run == nil {
		return false, err
	}
	return run.Commit == head && run.Conclusion == string(ci.ConclusionSuccess), nil
}

// reviewKindOf is "security", "design" or "code", read from the title
// fileExternalPRReview gave the review.
func reviewKindOf(task model.Task) string {
	switch {
	case strings.HasPrefix(task.Title, "Security review"):
		return "security"
	case strings.HasPrefix(task.Title, "Design review"):
		return "design"
	}
	return "code"
}

// PullRequestStatusOf answers GET /api/pull-requests/status for ref:
// where its head is, who holds it, what grain's CI and reviews say, and
// where the merge queue has it. threads fills in every inline review
// thread as well.
func PullRequestStatusOf(ctx context.Context, deps Deps, ref model.PullRequestRef, threads bool) (model.PullRequestReport, error) {
	out := model.PullRequestReport{PR: ref.String(), Reviews: []model.PullRequestReviewInfo{}}
	var detail github.PullRequestDetail
	if deps.Client != nil {
		d, err := deps.Client.GetPullRequest(ref.Repo.Owner, ref.Repo.Name, ref.Number)
		if err != nil {
			return out, readPullRequestErr(ref, err)
		}
		detail = d
		out.Head, out.State = d.HeadSHA, strings.ToLower(d.State)
		if d.Merged {
			out.State = "merged"
		}
	}
	owner, err := takeoverOwner(ctx, deps.Store, ref)
	if err != nil {
		return out, err
	}
	if owner != nil && isTakeoverTask(*owner) {
		state, err := deps.Store.State(ctx, owner.ID)
		if err != nil {
			return out, err
		}
		out.Shepherd = &model.PullRequestShepherd{
			TaskID: owner.ID, Mode: string(takeoverModeOf(*owner)), State: string(state),
			Spec: linkTarget(*owner, model.LinkImplementsSpec), Design: linkTarget(*owner, model.LinkDesignDoc),
		}
	}

	if run, err := latestCIRun(ctx, deps.Store, ref.Repo, detail.HeadRef, detail.HeadSHA); err != nil {
		return out, err
	} else if run != nil {
		status := "completed"
		if run.Conclusion == "" {
			status = "in_progress"
		}
		info := &model.PullRequestCI{RunID: run.ID, Commit: run.Commit, Status: status, Conclusion: run.Conclusion}
		for _, j := range run.Jobs {
			if j.Conclusion != "" && j.Conclusion != string(ci.ConclusionSuccess) &&
				j.Conclusion != "skipped" && j.Conclusion != "neutral" {
				info.FailingJobs = append(info.FailingJobs, j.Name)
			}
		}
		out.CI = info
	}

	reviewPending := false
	reviews, err := pullRequestReviews(ctx, deps.Store, ref, owner, out.Head)
	if err != nil {
		return out, err
	}
	for _, r := range reviews {
		if r.State != "passed" {
			reviewPending = true
		}
	}
	out.Reviews = reviews

	if deps.Client != nil {
		list, err := pullRequestThreads(deps.Client, ref)
		if err != nil {
			log.Printf("orchestrator: reading review threads of %s: %v", ref, err)
		}
		unresolved := 0
		for _, th := range list {
			if !th.Resolved {
				unresolved++
			}
		}
		if n := len(out.Reviews); n > 0 {
			out.Reviews[n-1].UnresolvedThreads = unresolved
			if unresolved > 0 && out.Reviews[n-1].State == "passed" && out.Shepherd == nil {
				out.Reviews[n-1].State = "findings"
				reviewPending = true
			}
		}
		if threads {
			out.Threads = list
		}
	}

	switch {
	case detail.Merged:
		out.MergeQueue = "merged"
	case owner == nil || out.State != "open":
		out.MergeQueue = "not-queued"
	case reviewPending:
		out.MergeQueue = "held: review"
	default:
		if held, _ := takeoverMergeHold(*owner); held {
			out.MergeQueue = "held: review"
			break
		}
		usesGrainCI, err := deps.Store.RepoUsesGrainCI(ctx, ref.Repo)
		if err != nil {
			return out, err
		}
		if out.CI != nil && (out.CI.Conclusion != string(ci.ConclusionSuccess) || out.CI.Commit != out.Head) ||
			out.CI == nil && usesGrainCI {
			out.MergeQueue = "held: ci"
		} else {
			out.MergeQueue = "queued"
		}
	}
	return out, nil
}

// pullRequestReviews is every grain review of ref: a shepherd's set for
// the head (its newest set when the head has none yet), and the open
// standalone reviews filed with `grain pr review` or "@<slug> review".
func pullRequestReviews(ctx context.Context, store *model.Store, ref model.PullRequestRef, owner *model.Task,
	head string) ([]model.PullRequestReviewInfo, error) {

	type entry struct{ id, head string }
	var entries []entry
	seen := map[string]bool{}
	if owner != nil && isTakeoverTask(*owner) {
		set := takeoverReviews(*owner)
		at := head
		found := false
		for _, r := range set {
			found = found || r.head == at
		}
		if !found && len(set) > 0 {
			at = set[len(set)-1].head
		}
		for _, r := range set {
			if r.head == at && !seen[r.id] {
				seen[r.id] = true
				entries = append(entries, entry{r.id, r.head})
			}
		}
	}
	standalone, err := store.OpenTasksWithLinkTarget(ctx, model.LinkExternalPR, ref.String())
	if err != nil {
		return nil, err
	}
	sort.Strings(standalone)
	for _, id := range standalone {
		if !seen[id] {
			seen[id] = true
			entries = append(entries, entry{id: id})
		}
	}
	out := []model.PullRequestReviewInfo{}
	for _, e := range entries {
		task, err := store.GetTask(ctx, e.id)
		if err != nil {
			return nil, err
		}
		if task == nil || !isReviewTask(*task) {
			continue
		}
		state, err := store.State(ctx, e.id)
		if err != nil {
			return nil, err
		}
		info := model.PullRequestReviewInfo{TaskID: e.id, Kind: reviewKindOf(*task), Head: e.head, State: "pending"}
		if state == model.StateCompleted || state == model.StateClosed {
			n, _ := takeoverFindings(*task)
			info.Findings = n
			info.State = "passed"
			if n > 0 {
				info.State = "findings"
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// pullRequestThreads is ref's inline review threads, oldest first, with
// whether each is resolved where the client can say.
func pullRequestThreads(client github.Client, ref model.PullRequestRef) ([]model.PullRequestThread, error) {
	comments, err := client.ListReviewComments(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		return nil, err
	}
	resolved := map[int]bool{}
	if lister, ok := client.(reviewThreadLister); ok {
		if threads, err := lister.ListReviewThreads(ref.Repo.Owner, ref.Repo.Name, ref.Number); err == nil {
			for _, th := range threads {
				for _, id := range th.CommentIDs {
					resolved[id] = th.Resolved
				}
			}
		}
	}
	replies := map[int]int{}
	for _, c := range comments {
		if c.InReplyTo != 0 {
			replies[c.InReplyTo]++
		}
	}
	var out []model.PullRequestThread
	for _, c := range comments {
		if c.InReplyTo != 0 {
			continue
		}
		th := model.PullRequestThread{Path: c.Path, Author: c.User, Body: c.Body, Resolved: resolved[c.ID], Replies: replies[c.ID]}
		if c.Line != nil {
			th.Line = *c.Line
		}
		out = append(out, th)
	}
	return out, nil
}
