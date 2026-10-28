package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bwsalmon/grain/pkg/capability/grainci"
	"github.com/bwsalmon/grain/pkg/ghwebhook"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
)

// GitHubEventSource supplies verified inbound GitHub webhook events to
// reconcileGitHubEvents. Events are returned in ascending sequence order
// and acknowledged once their store side-effects settle.
type GitHubEventSource interface {
	Pending() ([]ghwebhook.Event, error)
	Ack(seq uint64) error
}

// AppBotIdentityFunc reports whether repo's configured GitHub credential
// is a GitHub App, and if so the bot login ("<slug>[bot]") it acts as.
type AppBotIdentityFunc func(ctx context.Context, repo model.RepoRef) (botLogin string, isApp bool)

// githubWebhookAutomation names the untrusted webhook relay actor: when a
// non-collaborator mentions the bot on a public repository, filing with
// an automation actor keeps model.LandsQueued false so the task lands in
// StateProposed awaiting human approval.
var githubWebhookAutomation = model.Principal{
	Kind: model.PrincipalAutomation,
	ID:   "github-webhook",
}

const (
	externalReviewCheckName   = "grain/review"
	externalCheckRunLinkPrefx = "checkrun:"
)

func reconcileGitHubEvents(ctx context.Context, deps Deps, now time.Time) error {
	if deps.GitHubEvents == nil || deps.Store == nil {
		return nil
	}
	events, err := deps.GitHubEvents.Pending()
	if err != nil {
		return fmt.Errorf("orchestrator: reading pending GitHub webhook events: %w", err)
	}
	if len(events) == 0 {
		return nil
	}

	allowedRepos, err := deps.Store.TargetRepos(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: reading target repos for GitHub events: %w", err)
	}

	trustCache := map[string]bool{}
	var errs []error
	for _, ev := range events {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := processGitHubEvent(ctx, deps, allowedRepos, trustCache, ev, now); err != nil {
			errs = append(errs, fmt.Errorf("event seq=%d (%s/%s on %s): %w",
				ev.Seq, ev.Event, ev.Action, ev.Repo, err))
			// Stop advancing the ack cursor past a transient failure so the
			// next cycle retries in sequence order; idempotency links prevent
			// duplicate effects for any event that partially succeeded.
			break
		}
		if err := deps.GitHubEvents.Ack(ev.Seq); err != nil {
			errs = append(errs, fmt.Errorf("acknowledging GitHub event seq=%d: %w", ev.Seq, err))
			break
		}
	}
	return errors.Join(errs...)
}

func processGitHubEvent(ctx context.Context, deps Deps, allowedRepos []string,
	trustCache map[string]bool, ev ghwebhook.Event, now time.Time) error {

	owner, name, ok := ghwebhook.RepoSplit(ev.Repo)
	if !ok {
		return nil
	}
	repo := resolveEventRepoRef(allowedRepos, owner, name)
	if !model.RepoAllowed(allowedRepos, repo) {
		return nil
	}

	var botLogin, botSlug string
	if deps.AppBotIdentity != nil {
		if login, isApp := deps.AppBotIdentity(ctx, repo); isApp && login != "" {
			botLogin = login
			botSlug = strings.TrimSuffix(login, "[bot]")
		}
	}

	if isSelfBotEvent(ev, botLogin, botSlug) {
		return nil
	}

	switch ev.Event {
	case "pull_request":
		return handlePullRequestEvent(ctx, deps, repo, botLogin, botSlug, trustCache, ev, now)
	case "pull_request_review":
		if ev.Action == "submitted" {
			return handlePullRequestReviewEvent(ctx, deps, repo, botSlug, trustCache, ev, now)
		}
	case "issue_comment":
		if ev.Action == "created" {
			return handleCommentEvent(ctx, deps, repo, botSlug, trustCache, ev, false, now)
		}
	case "pull_request_review_comment":
		if ev.Action == "created" {
			return handleCommentEvent(ctx, deps, repo, botSlug, trustCache, ev, true, now)
		}
	case "issues":
		if ev.Action == "opened" {
			return handleIssueOpenedEvent(ctx, deps, repo, botSlug, trustCache, ev, now)
		}
	case "check_run", "workflow_job":
		// GitHub's own CI concluding a job: recorded beside grain CI's
		// runs (githubci.go). The ETag invalidation below applies too.
		return recordGitHubCIEvent(ctx, deps, repo, ev, now)
	}
	// check_run, check_suite, status, push, pull_request.synchronize:
	// ETag cache invalidation + cycle wakeup already ran when the event
	// arrived, so SyncPullRequests observes the fresh state on this cycle.
	return nil
}

func resolveEventRepoRef(allowed []string, owner, name string) model.RepoRef {
	candidate := model.RepoRef{Owner: owner, Name: name}
	for _, raw := range allowed {
		r, err := model.ParseRepo(raw)
		if err == nil && strings.EqualFold(r.Owner, owner) && strings.EqualFold(r.Name, name) {
			return r
		}
	}
	return candidate
}

func isSelfBotEvent(ev ghwebhook.Event, botLogin, botSlug string) bool {
	if botLogin != "" && strings.EqualFold(ev.SenderLogin, botLogin) {
		return true
	}
	if botSlug != "" && strings.EqualFold(ev.SenderType, "Bot") &&
		strings.EqualFold(ev.SenderLogin, botSlug+"[bot]") {
		return true
	}
	for _, text := range []string{ev.CommentBody, ev.ReviewBody, ev.Body} {
		if strings.Contains(text, treeMarker) || strings.Contains(text, lineageStart) {
			return true
		}
	}
	return false
}

func senderTrusted(client github.Client, cache map[string]bool, repo model.RepoRef, login string) (bool, error) {
	if login == "" || client == nil {
		return false, nil
	}
	key := strings.ToLower(repo.Owner + "/" + repo.Name + ":" + login)
	if trusted, ok := cache[key]; ok {
		return trusted, nil
	}
	reader, ok := client.(github.CollaboratorReader)
	if !ok {
		cache[key] = false
		return false, nil
	}
	perm, err := reader.CollaboratorPermission(repo.Owner, repo.Name, login)
	if err != nil {
		if github.IsPermissionDenied(err) || strings.Contains(err.Error(), "404") {
			cache[key] = false
			return false, nil
		}
		return false, err
	}
	trusted := github.IsTrustedCollaboratorPermission(perm)
	cache[key] = trusted
	return trusted, nil
}

func handlePullRequestEvent(ctx context.Context, deps Deps, repo model.RepoRef,
	botLogin, botSlug string, trustCache map[string]bool, ev ghwebhook.Event, now time.Time) error {

	if ev.Number <= 0 {
		return nil
	}
	ref := model.PullRequestRef{Repo: repo, Number: ev.Number}

	switch ev.Action {
	case "closed":
		task, err := openGrainTaskForPR(ctx, deps.Store, ref)
		if err != nil || task == nil {
			return err
		}
		return deps.Store.ObserveField(ctx, task.ID, now, func(o *model.Observation) {
			if ev.Merged {
				if o.PrMergedAt == nil {
					o.PrMergedAt = &now
				}
			} else if o.PrClosedAt == nil {
				o.PrClosedAt = &now
			}
		})

	case "labeled":
		if botLogin == "" || !strings.EqualFold(strings.TrimSpace(ev.Label), takeoverLabel(deps)) {
			return nil
		}
		key := "label:" + ev.DeliveryID
		if ev.HeadSHA != "" {
			key = "label:" + strings.ToLower(ev.Label) + "@" + ev.HeadSHA
		}
		return handleTakeoverCommand(ctx, deps, repo, ev.Number, ev.SenderLogin, key, 0, false, trustCache, now)

	case "review_requested":
		if botLogin == "" {
			return nil
		}
		if !strings.EqualFold(ev.RequestedReviewerLogin, botLogin) &&
			!strings.EqualFold(ev.RequestedReviewerLogin, botSlug) {
			return nil
		}
		// Skip PRs opened by grain itself; reconcileReviews already owns those.
		if owned, err := openGrainTaskForPR(ctx, deps.Store, ref); err != nil || owned != nil {
			return err
		}
		eventKey := "review_requested:" + ev.HeadSHA
		if ev.HeadSHA == "" {
			eventKey = "review_requested:" + ev.DeliveryID
		}
		if exists, err := originHasEventKey(ctx, deps.Store, repo, ev.Number, eventKey); err != nil || exists {
			return err
		}
		trusted, err := senderTrusted(deps.Client, trustCache, repo, ev.SenderLogin)
		if err != nil {
			return err
		}
		return fileExternalPRReviewTask(ctx, deps, repo, ev.Number, ev.Title, ev.Body,
			ev.HeadRef, ev.HeadSHA, ev.BaseRef, ev.SenderLogin, trusted, eventKey, "", now)
	}
	return nil
}

func handlePullRequestReviewEvent(ctx context.Context, deps Deps, repo model.RepoRef,
	botSlug string, trustCache map[string]bool, ev ghwebhook.Event, now time.Time) error {

	if ev.Number <= 0 {
		return nil
	}
	ref := model.PullRequestRef{Repo: repo, Number: ev.Number}
	eventKey := "review:" + strconv.FormatInt(ev.ReviewID, 10)

	if botSlug != "" && mentionsBot(ev.ReviewBody, botSlug) {
		instruction := extractBotInstruction(ev.ReviewBody, botSlug)
		if wantsTakeoverCommand(instruction) {
			return handleTakeoverCommand(ctx, deps, repo, ev.Number, ev.SenderLogin, eventKey,
				0, false, trustCache, now)
		}
		if wantsReleaseCommand(instruction) {
			return handleReleaseCommand(ctx, deps, repo, ev.Number, ev.SenderLogin, eventKey,
				ev.AuthorLogin, trustCache, now)
		}
	}

	owned, err := openGrainTaskForPR(ctx, deps.Store, ref)
	if err != nil {
		return err
	}
	if owned != nil {
		if hasLinkTarget(*owned, model.LinkGitHubEvent, eventKey) {
			return nil
		}
		changesRequested := strings.EqualFold(ev.ReviewState, "changes_requested")
		mentioned := botSlug != "" && mentionsBot(ev.ReviewBody, botSlug)
		state, err := deps.Store.State(ctx, owned.ID)
		if err != nil {
			return err
		}
		_, heldQuestions := model.ReviewQuestionsOf(*owned)
		parked := state == model.StateAwaitingReply || heldQuestions
		if !changesRequested && !mentioned && (!parked || strings.TrimSpace(ev.ReviewBody) == "") {
			return nil
		}
		trusted, err := senderTrusted(deps.Client, trustCache, repo, ev.SenderLogin)
		if err != nil || !trusted {
			return err
		}
		note := strings.TrimSpace(ev.ReviewBody)
		if note == "" && changesRequested {
			note = fmt.Sprintf("Requested changes in review on %s.", ref)
		} else {
			note = fmt.Sprintf("Review (%s) on %s:\n\n%s", ev.ReviewState, ref, note)
		}
		return deliverReplyAndWakeTask(ctx, deps.Store, *owned, state, heldQuestions,
			ev.SenderLogin, note, eventKey, changesRequested || mentioned, now)
	}

	if botSlug == "" || !mentionsBot(ev.ReviewBody, botSlug) {
		return nil
	}
	return fileTaskFromMention(ctx, deps, repo, ev.Number, true, ev.Title, ev.ReviewBody,
		ev.HeadRef, ev.HeadSHA, ev.BaseRef, ev.SenderLogin, botSlug, eventKey, trustCache, now)
}

func handleCommentEvent(ctx context.Context, deps Deps, repo model.RepoRef,
	botSlug string, trustCache map[string]bool, ev ghwebhook.Event, inlineReview bool, now time.Time) error {

	if ev.Number <= 0 || strings.TrimSpace(ev.CommentBody) == "" {
		return nil
	}
	kindPrefix := "issue_comment:"
	if inlineReview {
		kindPrefix = "review_comment:"
	}
	eventKey := kindPrefix + strconv.FormatInt(ev.CommentID, 10)
	mentioned := botSlug != "" && mentionsBot(ev.CommentBody, botSlug)

	// The takeover commands come first: on a pull request grain already
	// owns, "take over" is answered with who owns it rather than relayed.
	if mentioned && (ev.IsPullRequest || inlineReview) {
		instruction := extractBotInstruction(ev.CommentBody, botSlug)
		if wantsTakeoverCommand(instruction) {
			return handleTakeoverCommand(ctx, deps, repo, ev.Number, ev.SenderLogin, eventKey,
				ev.CommentID, inlineReview, trustCache, now)
		}
		if wantsReleaseCommand(instruction) {
			return handleReleaseCommand(ctx, deps, repo, ev.Number, ev.SenderLogin, eventKey,
				ev.AuthorLogin, trustCache, now)
		}
	}

	// 1. Check whether this comment is on a grain-owned PR or a parked task
	// whose origin is this GitHub issue/PR.
	targetTask, err := trackedTaskForReply(ctx, deps.Store, repo, ev.Number, ev.IsPullRequest)
	if err != nil {
		return err
	}
	if targetTask != nil {
		if hasLinkTarget(*targetTask, model.LinkGitHubEvent, eventKey) {
			return nil
		}
		state, err := deps.Store.State(ctx, targetTask.ID)
		if err != nil {
			return err
		}
		_, heldQuestions := model.ReviewQuestionsOf(*targetTask)
		parked := state == model.StateAwaitingReply || heldQuestions
		// Inline diff comments on grain-owned PRs without @mention and
		// without a parked question are already batched by SyncReviewThreads.
		if inlineReview && !mentioned && !parked {
			return nil
		}
		if parked || mentioned {
			trusted, err := senderTrusted(deps.Client, trustCache, repo, ev.SenderLogin)
			if err != nil || !trusted {
				return err
			}
			reactEyes(deps.Client, repo, ev.CommentID, inlineReview)
			body := fmt.Sprintf("Comment on %s#%d:\n\n%s", repo, ev.Number, strings.TrimSpace(ev.CommentBody))
			return deliverReplyAndWakeTask(ctx, deps.Store, *targetTask, state, heldQuestions,
				ev.SenderLogin, body, eventKey, mentioned, now)
		}
	}

	// 2. Otherwise, check for an explicit @<slug> mention on an external issue/PR.
	if !mentioned {
		return nil
	}
	trusted, err := senderTrusted(deps.Client, trustCache, repo, ev.SenderLogin)
	if err != nil {
		return err
	}
	if trusted {
		reactEyes(deps.Client, repo, ev.CommentID, inlineReview)
	}
	return fileTaskFromMention(ctx, deps, repo, ev.Number, ev.IsPullRequest || inlineReview,
		ev.Title, ev.CommentBody, ev.HeadRef, ev.HeadSHA, ev.BaseRef,
		ev.SenderLogin, botSlug, eventKey, trustCache, now)
}

func handleIssueOpenedEvent(ctx context.Context, deps Deps, repo model.RepoRef,
	botSlug string, trustCache map[string]bool, ev ghwebhook.Event, now time.Time) error {

	if ev.Number <= 0 || botSlug == "" {
		return nil
	}
	combined := ev.Title + "\n\n" + ev.Body
	if !mentionsBot(combined, botSlug) {
		return nil
	}
	eventKey := "issue:" + strconv.Itoa(ev.Number)
	return fileTaskFromMention(ctx, deps, repo, ev.Number, false,
		ev.Title, ev.Body, "", "", "",
		ev.SenderLogin, botSlug, eventKey, trustCache, now)
}

func fileTaskFromMention(ctx context.Context, deps Deps, repo model.RepoRef, number int, isPR bool,
	subjectTitle, commentBody, headRef, headSHA, baseRef, sender, botSlug, eventKey string,
	trustCache map[string]bool, now time.Time) error {

	if exists, err := originHasEventKey(ctx, deps.Store, repo, number, eventKey); err != nil || exists {
		return err
	}
	trusted, err := senderTrusted(deps.Client, trustCache, repo, sender)
	if err != nil {
		return err
	}

	instruction := extractBotInstruction(commentBody, botSlug)
	if isPR && wantsReviewCommand(instruction) {
		var prBody string
		if headRef == "" && deps.Client != nil {
			if detail, err := deps.Client.GetPullRequest(repo.Owner, repo.Name, number); err == nil {
				headRef = detail.HeadRef
				headSHA = detail.HeadSHA
				baseRef = detail.BaseRef
				if subjectTitle == "" {
					subjectTitle = detail.Title
				}
				prBody = detail.Body
			}
		}
		return fileExternalPRReviewTask(ctx, deps, repo, number, subjectTitle, prBody,
			headRef, headSHA, baseRef, sender, trusted, eventKey, instruction, now)
	}

	var base string
	links := []model.Link{{Kind: model.LinkGitHubEvent, Target: eventKey}}
	if isPR {
		ref := model.PullRequestRef{Repo: repo, Number: number}
		links = append(links, model.Link{Kind: model.LinkExternalPR, Target: ref.String()})
		if headRef == "" && deps.Client != nil {
			if detail, err := deps.Client.GetPullRequest(repo.Owner, repo.Name, number); err == nil {
				headRef = detail.HeadRef
				baseRef = detail.BaseRef
				if subjectTitle == "" {
					subjectTitle = detail.Title
				}
			}
		}
		base = usableRepoBranch(deps.Client, repo, headRef, baseRef)
	}

	title := mentionTaskTitle(repo, number, isPR, subjectTitle, instruction)
	body := mentionTaskBody(repo, number, isPR, subjectTitle, sender, commentBody)

	id, err := deps.Store.NewTaskID(ctx)
	if err != nil {
		return fmt.Errorf("allocating task id: %w", err)
	}
	orderKey, err := deps.Store.OrderKeyForNewTask(ctx, false)
	if err != nil {
		return fmt.Errorf("allocating order key: %w", err)
	}
	grants, err := defaultRepoGrants(ctx, deps.Store, repo)
	if err != nil {
		return err
	}

	origin, approval, approvedAt := attributionForSender(sender, trusted, model.ReasonDirect, now)
	var issueNum int
	if !isPR {
		issueNum = number
	}
	task := model.Task{
		ID:         id,
		Intent:     model.IntentImplement,
		Origin:     origin,
		Approval:   approval,
		ApprovedAt: approvedAt,
		Title:      title,
		Body:       body,
		Target:     &repo,
		Binding:    model.BindingDirective,
		Base:       base,
		Issue:      issueNum,
		WorkOrigin: model.GitHubOrigin(repo, number),
		Grants:     grants,
		Links:      links,
		CreatedAt:  &now,
		OrderKey:   orderKey,
	}
	if err := deps.Store.PutTask(ctx, task); err != nil {
		return fmt.Errorf("filing task %s from GitHub mention: %w", task.ID, err)
	}
	log.Printf("orchestrator: filed task %s (trusted=%v) from @%s mention by %s on %s#%d",
		task.ID, trusted, botSlug, sender, repo, number)
	return nil
}

func fileExternalPRReviewTask(ctx context.Context, deps Deps, repo model.RepoRef, number int,
	prTitle, prBody, headRef, headSHA, baseRef, sender string, trusted bool,
	eventKey, extraNote string, now time.Time) error {

	_, err := fileExternalPRReview(ctx, deps, externalReview{
		repo: repo, number: number, prTitle: prTitle, prBody: prBody,
		headRef: headRef, headSHA: headSHA, baseRef: baseRef, sender: sender,
		trusted: trusted, eventKey: eventKey, extraNote: extraNote,
	}, now)
	return err
}

// externalReview is what fileExternalPRReview files a review of a pull
// request grain did not open from.
type externalReview struct {
	repo                                               model.RepoRef
	number                                             int
	prTitle, prBody, headRef, headSHA, baseRef, sender string
	trusted                                            bool
	eventKey, extraNote                                string
	// shepherdID is the takeover task this review gates (takeover.go):
	// the review carries model.LinkTakeoverReviewOf back to it, and
	// files no grain/review check run of its own, since the shepherd
	// keeps the one check for the whole gate.
	shepherdID string
	// security files the security review (model.SecurityReviewTemplateID)
	// rather than a code review.
	security bool
}

// fileExternalPRReview files r and returns the review task's ID.
func fileExternalPRReview(ctx context.Context, deps Deps, r externalReview, now time.Time) (string, error) {
	repo, number, sender, trusted := r.repo, r.number, r.sender, r.trusted
	prTitle, prBody, headRef, headSHA, baseRef := r.prTitle, r.prBody, r.headRef, r.headSHA, r.baseRef
	eventKey, extraNote := r.eventKey, r.extraNote

	if (headRef == "" || headSHA == "") && deps.Client != nil {
		if detail, err := deps.Client.GetPullRequest(repo.Owner, repo.Name, number); err == nil {
			if headRef == "" {
				headRef = detail.HeadRef
			}
			if headSHA == "" {
				headSHA = detail.HeadSHA
			}
			if baseRef == "" {
				baseRef = detail.BaseRef
			}
			if prTitle == "" {
				prTitle = detail.Title
			}
			if prBody == "" {
				prBody = detail.Body
			}
		}
	}
	base := usableRepoBranch(deps.Client, repo, headRef, baseRef)

	id, err := deps.Store.NewTaskID(ctx)
	if err != nil {
		return "", fmt.Errorf("allocating review task id: %w", err)
	}
	orderKey, err := deps.Store.OrderKeyForNewTask(ctx, true)
	if err != nil {
		return "", fmt.Errorf("allocating review order key: %w", err)
	}
	grants, err := defaultRepoGrants(ctx, deps.Store, repo)
	if err != nil {
		return "", err
	}

	ref := model.PullRequestRef{Repo: repo, Number: number}
	links := []model.Link{
		{Kind: model.LinkExternalPR, Target: ref.String()},
		{Kind: model.LinkGitHubEvent, Target: eventKey},
	}
	if r.shepherdID != "" {
		links = append(links, model.Link{Kind: model.LinkTakeoverReviewOf, Target: r.shepherdID})
	}

	// Best-effort native GitHub Check Run while the review is queued/running.
	if trusted && headSHA != "" && r.shepherdID == "" {
		if checks, ok := deps.Client.(github.CheckRunWriter); ok {
			checkID, err := checks.CreateCheckRun(repo.Owner, repo.Name, github.CheckRunWrite{
				Name:    externalReviewCheckName,
				HeadSHA: headSHA,
				Status:  "queued",
				Output: &github.CheckRunOutput{
					Title:   fmt.Sprintf("Reviewing %s (task %s)", ref, id),
					Summary: fmt.Sprintf("Grain review task %s is queued for %s.", id, ref),
				},
			})
			if err == nil && checkID > 0 {
				links = append(links, model.Link{
					Kind:   model.LinkGitHubEvent,
					Target: externalCheckRunLinkPrefx + strconv.FormatInt(checkID, 10),
				})
			}
		}
	}

	origin, approval, approvedAt := attributionForSender(sender, trusted, model.ReasonReview, now)
	verb := "Review"
	if r.security {
		verb = "Security review"
	}
	title := fmt.Sprintf("%s %s#%d", verb, repo, number)
	if strings.TrimSpace(prTitle) != "" {
		title = fmt.Sprintf("%s %s#%d: %s", verb, repo, number, strings.TrimSpace(prTitle))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s pull request %s#%d", verb, repo, number)
	if strings.TrimSpace(prTitle) != "" {
		fmt.Fprintf(&b, " (%s)", strings.TrimSpace(prTitle))
	}
	b.WriteString(".\n\n")
	if headRef != "" && baseRef != "" {
		fmt.Fprintf(&b, "Head branch: `%s` (merging into `%s`).\n\n", headRef, baseRef)
	}
	if strings.TrimSpace(prBody) != "" {
		if r.shepherdID != "" {
			b.WriteString(quoteExternalContent("Pull request description", prBody))
		} else {
			fmt.Fprintf(&b, "Pull request description:\n\n%s\n\n", strings.TrimSpace(prBody))
		}
	}
	if strings.TrimSpace(extraNote) != "" {
		fmt.Fprintf(&b, "Reviewer request from @%s:\n\n%s\n\n", sender, strings.TrimSpace(extraNote))
	}
	if r.security {
		if tmpl := model.SystemTemplate(model.SecurityReviewTemplateID, model.RepoRef{}); tmpl != nil &&
			strings.TrimSpace(tmpl.Body) != "" {
			fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(tmpl.Body))
		}
		b.WriteString("Review this change as a security review: look for anything that widens what an " +
			"untrusted party can do -- credentials, capabilities, workflow and CI changes, sandbox and " +
			"proxy scoping, injection through prompts or shell. Do not push to the branch under review.\n\n")
	}
	b.WriteString("Inspect the changes against the base branch, run relevant tests, and record each " +
		"finding with `add_review_comment` (supplying `path` and `line` for inline diff findings). " +
		"If the pull request looks clean with no findings, summarize what you checked via `comment_on_issue`.")

	task := model.Task{
		ID:         id,
		Intent:     model.IntentReview,
		Origin:     origin,
		Approval:   approval,
		ApprovedAt: approvedAt,
		Title:      title,
		Body:       b.String(),
		Target:     &repo,
		Binding:    model.BindingDirective,
		Base:       base,
		WorkOrigin: model.GitHubOrigin(repo, number),
		Grants:     grants,
		Links:      links,
		CreatedAt:  &now,
		OrderKey:   orderKey,
	}
	if err := deps.Store.PutTask(ctx, task); err != nil {
		return "", fmt.Errorf("filing external PR review task %s: %w", task.ID, err)
	}
	log.Printf("orchestrator: filed external PR review task %s (trusted=%v) for %s", task.ID, trusted, ref)
	return task.ID, nil
}

// completeExternalReviewOutcome updates any Check Run recorded for a
// standalone external PR review task and, when the review produced zero
// findings, posts the run's clean review summary directly onto the PR.
func completeExternalReviewOutcome(_ context.Context, client github.Client, task model.Task,
	closingComment, nothingNeeded string, findings int) {

	if client == nil || !isReviewTask(task) || linkTarget(task, model.LinkProposedBy) != "" {
		return
	}
	ext := linkTarget(task, model.LinkExternalPR)
	if ext == "" {
		return
	}
	ref, err := model.ParsePullRequestRef(ext)
	if err != nil {
		return
	}

	summary := strings.TrimSpace(closingComment)
	if summary == "" {
		summary = strings.TrimSpace(nothingNeeded)
	}
	if findings == 0 && summary != "" {
		if submitter, ok := client.(github.ReviewSubmitter); ok {
			body := fmt.Sprintf("Review from grain task %s:\n\n%s", task.ID, summary)
			if _, err := submitter.SubmitReview(ref.Repo.Owner, ref.Repo.Name, ref.Number,
				github.ReviewEventComment, body, nil); err != nil {
				log.Printf("orchestrator: submitting clean review on %s for task %s: %v", ref, task.ID, err)
			}
		}
	}

	checkRunID := externalCheckRunID(task)
	if checkRunID == 0 {
		return
	}
	checks, ok := client.(github.CheckRunWriter)
	if !ok {
		return
	}
	conclusion := "success"
	title := fmt.Sprintf("Clean review on %s (task %s)", ref, task.ID)
	checkSummary := summary
	if checkSummary == "" {
		checkSummary = fmt.Sprintf("Grain review task %s completed with no findings.", task.ID)
	}
	if findings > 0 {
		conclusion = "neutral"
		title = fmt.Sprintf("%s posted on %s (task %s)", countOf(findings, "finding"), ref, task.ID)
		checkSummary = fmt.Sprintf("Grain review task %s posted %s on %s.",
			task.ID, countOf(findings, "finding"), ref)
	}
	if err := checks.UpdateCheckRun(ref.Repo.Owner, ref.Repo.Name, checkRunID, github.CheckRunWrite{
		Status:     "completed",
		Conclusion: conclusion,
		Output: &github.CheckRunOutput{
			Title:   title,
			Summary: checkSummary,
		},
	}); err != nil {
		log.Printf("orchestrator: completing check run %d on %s for task %s: %v", checkRunID, ref, task.ID, err)
	}
}

func externalCheckRunID(task model.Task) int64 {
	for _, l := range task.Links {
		if l.Kind != model.LinkGitHubEvent {
			continue
		}
		if raw, ok := strings.CutPrefix(l.Target, externalCheckRunLinkPrefx); ok {
			if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
				return id
			}
		}
	}
	return 0
}

func externalPRNumberForTarget(task model.Task) int {
	if task.Target == nil {
		return 0
	}
	ext := linkTarget(task, model.LinkExternalPR)
	if ext == "" {
		return 0
	}
	ref, err := model.ParsePullRequestRef(ext)
	if err != nil || !ref.Repo.Same(*task.Target) || ref.Number <= 0 {
		return 0
	}
	return ref.Number
}

func attributionForSender(sender string, trusted bool, trustedReason model.OriginReason,
	now time.Time) (model.Origin, *model.Attribution, *time.Time) {

	human := model.Principal{Kind: model.PrincipalHuman, ID: sender}
	if trusted {
		attr := model.Attribution{Actor: human}
		return model.Origin{Attribution: attr, Reason: trustedReason}, &attr, &now
	}
	return model.Origin{
		Attribution: model.Attribution{Actor: githubWebhookAutomation, OnBehalfOf: &human},
		Reason:      model.ReasonProposal,
	}, nil, nil
}

func defaultRepoGrants(ctx context.Context, store *model.Store, repo model.RepoRef) ([]model.Grant, error) {
	usesGrainCI, err := store.RepoUsesGrainCI(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("checking CI mode for %s: %w", repo, err)
	}
	if usesGrainCI {
		return []model.Grant{{Capability: grainci.CapabilityName, Via: model.GrantByDefault}}, nil
	}
	return nil, nil
}

func usableRepoBranch(client github.Client, repo model.RepoRef, preferred, fallback string) string {
	if preferred != "" && client != nil {
		if exists, err := client.BranchExists(repo.Owner, repo.Name, preferred); err == nil && exists {
			return preferred
		}
		if fallback != "" {
			return fallback
		}
	}
	return preferred
}

func deliverReplyAndWakeTask(ctx context.Context, store *model.Store, task model.Task,
	state model.State, heldQuestions bool, sender, body, eventKey string,
	wakeCompleted bool, now time.Time) error {

	recorded := false
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		if hasLinkTarget(*t, model.LinkGitHubEvent, eventKey) {
			return nil
		}
		recorded = true
		t.Links = append(t.Links, model.Link{Kind: model.LinkGitHubEvent, Target: eventKey})
		return nil
	}); err != nil {
		return err
	}
	if !recorded {
		return nil
	}

	if _, err := store.AddComment(ctx, model.Comment{
		TaskID:    task.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: sender}},
		Body:      body,
		CreatedAt: now,
	}); err != nil {
		return err
	}

	// If held on LinkReviewQuestions, adding the human comment above is
	// exactly what reconcileReviewQuestions (next in this cycle) settles.
	if heldQuestions {
		return nil
	}

	shouldWake := state == model.StateAwaitingReply ||
		(wakeCompleted && (state == model.StateCompleted || state == model.StateAwaitingSubmit))
	if !shouldWake {
		return nil
	}
	return store.ObserveField(ctx, task.ID, now, func(o *model.Observation) {
		o.CompletedAt = nil
		o.RetryRequestedAt = &now
		o.PendingQuestionCommentID = nil
		o.PendingSecret = ""
	})
}

func openGrainTaskForPR(ctx context.Context, store *model.Store, ref model.PullRequestRef) (*model.Task, error) {
	links, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		return nil, err
	}
	want := ref.String()
	for _, l := range links {
		if !strings.EqualFold(l.PullRequest, want) {
			continue
		}
		t, err := store.GetTask(ctx, l.TaskID)
		if err != nil || t == nil {
			return nil, err
		}
		if isReviewTask(*t) || t.Origin.Reason == model.ReasonFix {
			continue
		}
		return t, nil
	}
	return nil, nil
}

func trackedTaskForReply(ctx context.Context, store *model.Store, repo model.RepoRef,
	number int, isPR bool) (*model.Task, error) {

	if isPR {
		ref := model.PullRequestRef{Repo: repo, Number: number}
		if owned, err := openGrainTaskForPR(ctx, store, ref); err != nil || owned != nil {
			return owned, err
		}
	}
	tasks, err := store.TasksWithOrigin(ctx, model.GitHubOrigin(repo, number))
	if err != nil {
		return nil, err
	}
	for i := len(tasks) - 1; i >= 0; i-- {
		st, err := store.State(ctx, tasks[i].ID)
		if err != nil {
			return nil, err
		}
		_, held := model.ReviewQuestionsOf(tasks[i])
		if st == model.StateAwaitingReply || held {
			candidate := tasks[i]
			return &candidate, nil
		}
	}
	return nil, nil
}

func originHasEventKey(ctx context.Context, store *model.Store, repo model.RepoRef,
	number int, eventKey string) (bool, error) {

	tasks, err := store.TasksWithOrigin(ctx, model.GitHubOrigin(repo, number))
	if err != nil {
		return false, err
	}
	for _, t := range tasks {
		if hasLinkTarget(t, model.LinkGitHubEvent, eventKey) {
			return true, nil
		}
	}
	return false, nil
}

func hasLinkTarget(task model.Task, kind model.LinkKind, target string) bool {
	for _, l := range task.Links {
		if l.Kind == kind && l.Target == target {
			return true
		}
	}
	return false
}

func reactEyes(client github.Client, repo model.RepoRef, commentID int64, inlineReview bool) {
	if commentID <= 0 || client == nil {
		return
	}
	poster, ok := client.(github.ReactionPoster)
	if !ok {
		return
	}
	var err error
	if inlineReview {
		err = poster.CreateReviewCommentReaction(repo.Owner, repo.Name, commentID, "eyes")
	} else {
		err = poster.CreateIssueCommentReaction(repo.Owner, repo.Name, commentID, "eyes")
	}
	if err != nil {
		log.Printf("orchestrator: posting reaction to comment %d on %s: %v", commentID, repo, err)
	}
}

// mentionsBot reports whether text contains @<slug> or @<slug>[bot] outside
// fenced or inline markdown code spans.
func mentionsBot(text, slug string) bool {
	if slug == "" || text == "" {
		return false
	}
	cleaned := stripMarkdownCode(text)
	lower := strings.ToLower(cleaned)
	needle := "@" + strings.ToLower(slug)
	for offset := 0; offset < len(lower); {
		idx := strings.Index(lower[offset:], needle)
		if idx < 0 {
			return false
		}
		pos := offset + idx
		if pos > 0 {
			prev := rune(lower[pos-1])
			if unicode.IsLetter(prev) || unicode.IsDigit(prev) || prev == '-' || prev == '_' {
				offset = pos + len(needle)
				continue
			}
		}
		after := pos + len(needle)
		if strings.HasPrefix(lower[after:], "[bot]") {
			after += len("[bot]")
		}
		if after >= len(lower) {
			return true
		}
		next := rune(lower[after])
		if !unicode.IsLetter(next) && !unicode.IsDigit(next) && next != '-' && next != '_' {
			return true
		}
		offset = after
	}
	return false
}

func extractBotInstruction(text, slug string) string {
	cleaned := stripMarkdownCode(text)
	lower := strings.ToLower(cleaned)
	needle := "@" + strings.ToLower(slug)
	idx := strings.Index(lower, needle)
	if idx < 0 {
		return strings.TrimSpace(text)
	}
	after := idx + len(needle)
	if strings.HasPrefix(lower[after:], "[bot]") {
		after += len("[bot]")
	}
	rest := strings.TrimSpace(cleaned[after:])
	rest = strings.TrimLeft(rest, ":,;- ")
	return strings.TrimSpace(rest)
}

func wantsReviewCommand(instruction string) bool {
	fields := strings.Fields(strings.ToLower(instruction))
	if len(fields) == 0 {
		return false
	}
	first := strings.Trim(fields[0], "/:.,!?")
	if first == "review" {
		return true
	}
	if len(fields) >= 2 && fields[0] == "please" && strings.Trim(fields[1], "/:.,!?") == "review" {
		return true
	}
	return false
}

func stripMarkdownCode(s string) string {
	var out strings.Builder
	lines := strings.Split(s, "\n")
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		inTick := false
		for i := 0; i < len(line); i++ {
			if line[i] == '`' {
				inTick = !inTick
				continue
			}
			if !inTick {
				out.WriteByte(line[i])
			}
		}
		out.WriteByte('\n')
	}
	return out.String()
}

func mentionTaskTitle(repo model.RepoRef, number int, isPR bool, subjectTitle, instruction string) string {
	first := firstNonEmptyLine(instruction)
	if len(first) > 96 {
		first = first[:96] + "..."
	}
	if first != "" {
		return fmt.Sprintf("%s#%d: %s", repo, number, first)
	}
	if strings.TrimSpace(subjectTitle) != "" {
		return fmt.Sprintf("%s#%d: %s", repo, number, strings.TrimSpace(subjectTitle))
	}
	kind := "issue"
	if isPR {
		kind = "PR"
	}
	return fmt.Sprintf("Handle %s %s#%d", kind, repo, number)
}

func mentionTaskBody(repo model.RepoRef, number int, isPR bool, subjectTitle, sender, commentBody string) string {
	kind := "issue"
	if isPR {
		kind = "pull request"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Triggered by @%s on %s %s#%d", sender, kind, repo, number)
	if strings.TrimSpace(subjectTitle) != "" {
		fmt.Fprintf(&b, " (%s)", strings.TrimSpace(subjectTitle))
	}
	b.WriteString(":\n\n")
	b.WriteString(strings.TrimSpace(commentBody))
	return b.String()
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
