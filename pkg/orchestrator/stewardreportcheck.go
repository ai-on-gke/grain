package orchestrator

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/model"
)

// StewardRegressionWindow is the rolling window over which the steward's
// report-check failure rate is evaluated (docs/design/durable-tasks.md#DT-14).
const StewardRegressionWindow = 7 * 24 * time.Hour

// StewardRegressionThreshold is the 7-day failure-rate threshold (5% of claims)
// above which grain raises pipeline.steward-regression.
const StewardRegressionThreshold = 0.05

// StewardCheckTask is the task record snapshot stewardReportCheck compares a
// report's claims against.
type StewardCheckTask struct {
	ID    string
	State model.State
	Kinds []string
}

// StewardReportCheckResult is the outcome of running stewardReportCheck on one
// steward wake's report.
type StewardReportCheckResult struct {
	Check          model.RunReportCheck
	UncitedLines   []string
	UnreadIDs      []string
	MismatchLines  []string
	OffendingLines []string
}

// HasMismatch reports whether the report had any uncited claim, unread
// citation, or attribute mismatch.
func (r StewardReportCheckResult) HasMismatch() bool {
	return r.Check.Uncited > 0 || r.Check.Unread > 0 || r.Check.Mismatched > 0
}

var (
	citeBracketTaskRe = regexp.MustCompile(`(?i)\[\s*(?:task\s*#?|#)([0-9]+)\s*\]`)
	citeHashTaskRe    = regexp.MustCompile(`(?:^|[^\w/])#([0-9]+)\b`)
	citeWordTaskRe    = regexp.MustCompile(`(?i)\btasks?\s+#?([0-9]+(?:\s*(?:,|/|\band\b)\s*#?[0-9]+)*)`)
	citeNumberRe      = regexp.MustCompile(`[0-9]+`)
	citeSlashTasksRe  = regexp.MustCompile(`\b([0-9]{2,}(?:/[0-9]{2,})+)\b`)
	citeLinkTaskRe    = regexp.MustCompile(`/tasks/([0-9]+)\b`)
	citeOtherRe       = regexp.MustCompile(`(?i)\[\s*(?:ci(?:\s+run)?|run|batch)\s+[^\]]+\]|\b(?:ci-[a-z0-9_-]+|batch-[a-z0-9_-]+)\b`)
	auditLineRe       = regexp.MustCompile(`\[\s*run\s+([^\s\]]+)\s*\]\s*claims=(\d+)\s+uncited=(\d+)\s+unread=(\d+)\s+mismatched=(\d+)`)
)

// IsReadAPICall reports whether tool is an agent-API read call whose
// target_ids count as having been read in the wake.
func IsReadAPICall(tool string) bool {
	switch tool {
	case "read_grain_task", "read_task", "read_grain_task_prompt", "read_grain_task_transcript",
		"list_grain_tasks", "read_pipeline_changes", "query_merge_queue",
		"list_ci_runs", "get_ci_log", "pull_request_status", "base_branch_status",
		"query_test_history", "grain_read", "grain_ls", "grain_grep",
		"api_get_task", "api_list_tasks", "api_get_ci_run", "api_list_ci_runs":
		return true
	}
	return false
}

// ReadIDsFromAPICalls returns the set of task or CI run IDs read in calls
// prior to the closing report call.
func ReadIDsFromAPICalls(calls []model.RunAPICall) map[string]bool {
	read := map[string]bool{}
	for _, call := range calls {
		if call.Tool == "comment_on_issue" || call.Tool == "nothing_needed" {
			break
		}
		if !IsReadAPICall(call.Tool) || call.TargetIDs == "" {
			continue
		}
		for _, raw := range strings.Split(call.TargetIDs, ",") {
			id := strings.TrimSpace(raw)
			if id == "" {
				continue
			}
			read[id] = true
			if norm := model.NormalizeTaskID(id); norm != "" {
				read[norm] = true
			}
		}
	}
	return read
}

// stewardReportCheck checks a steward wake's report for uncited claims,
// cited-but-unread IDs, and attribute mismatches against the fixed vocabulary
// of kind tags and task states (docs/design/durable-tasks.md#DT-14).
func stewardReportCheck(runID, report string, calls []model.RunAPICall, tasks map[string]StewardCheckTask, promptIDs ...string) StewardReportCheckResult {
	res := StewardReportCheckResult{
		Check: model.RunReportCheck{RunID: runID},
	}
	if strings.TrimSpace(report) == "" {
		return res
	}

	readIDs := ReadIDsFromAPICalls(calls)
	for _, id := range promptIDs {
		if norm := model.NormalizeTaskID(id); norm != "" {
			readIDs[norm] = true
		}
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			readIDs[trimmed] = true
		}
	}

	inFence := false
	seenOffending := map[string]bool{}
	addOffending := func(line string) {
		if !seenOffending[line] {
			seenOffending[line] = true
			res.OffendingLines = append(res.OffendingLines, line)
		}
	}
	seenUnread := map[string]bool{}

	for _, rawLine := range strings.Split(report, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence || line == "" || strings.HasPrefix(line, "#") || isMarkdownTableSeparator(line) {
			continue
		}

		citedTasks := extractCitedTaskIDs(line)
		hasOtherCite := citeOtherRe.MatchString(line)
		if len(citedTasks) == 0 && !hasOtherCite && !looksLikeUncitedClaim(line) {
			continue
		}

		if len(citedTasks) == 0 {
			res.Check.Claims++
			if !hasOtherCite {
				res.Check.Uncited++
				res.UncitedLines = append(res.UncitedLines, line)
				addOffending(line)
			}
			continue
		}

		lineMismatched := false
		lineUnread := false
		for _, id := range citedTasks {
			res.Check.Claims++
			if !readIDs[id] {
				res.Check.Unread++
				lineUnread = true
				if !seenUnread[id] {
					seenUnread[id] = true
					res.UnreadIDs = append(res.UnreadIDs, id)
				}
			}
			if task, ok := tasks[id]; ok {
				if hasTaskAttributeMismatch(line, id, citedTasks, task, tasks) {
					res.Check.Mismatched++
					lineMismatched = true
				}
			}
		}
		if lineMismatched {
			res.MismatchLines = append(res.MismatchLines, line)
			addOffending(line)
		}
		if lineUnread {
			addOffending(line)
		}
	}

	return res
}

func isMarkdownTableSeparator(line string) bool {
	if !strings.Contains(line, "|") {
		return false
	}
	cleaned := strings.ReplaceAll(line, "|", "")
	cleaned = strings.ReplaceAll(cleaned, "-", "")
	cleaned = strings.ReplaceAll(cleaned, ":", "")
	return strings.TrimSpace(cleaned) == ""
}

// extractCitedTaskIDs extracts normalized task IDs cited on line.
func extractCitedTaskIDs(line string) []string {
	var ids []string
	seen := map[string]bool{}
	add := func(raw string) {
		id := model.NormalizeTaskID(raw)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, m := range citeBracketTaskRe.FindAllStringSubmatch(line, -1) {
		add(m[1])
	}
	for _, m := range citeLinkTaskRe.FindAllStringSubmatch(line, -1) {
		add(m[1])
	}
	for _, m := range citeHashTaskRe.FindAllStringSubmatch(line, -1) {
		add(m[1])
	}
	for _, m := range citeWordTaskRe.FindAllStringSubmatch(line, -1) {
		for _, num := range citeNumberRe.FindAllString(m[1], -1) {
			add(num)
		}
	}
	for _, m := range citeSlashTasksRe.FindAllStringSubmatch(line, -1) {
		for _, num := range strings.Split(m[1], "/") {
			add(num)
		}
	}
	return ids
}

var uncitedClaimWords = []string{
	"queued", "running", "completed", "awaiting_submit", "awaiting_reply",
	"deferred", "proposed", "failed", "closed", "merged", "stalled", "blocked",
	"port task", "repair task", "retriggered", "reordered", "paused", "resumed",
	"failure streak", "attempts",
}

func looksLikeUncitedClaim(line string) bool {
	lower := strings.ToLower(line)
	// Ignore generic "nothing needed" / "no action" / header lines that make no task claim.
	if strings.HasPrefix(lower, "nothing ") || strings.HasPrefix(lower, "no ") ||
		strings.HasPrefix(lower, "next:") || strings.HasPrefix(lower, "cursor:") {
		return false
	}
	isBullet := strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") ||
		(len(line) > 2 && line[0] >= '1' && line[0] <= '9' && line[1] == '.')
	hasTaskWord := containsWord(lower, "task") || containsWord(lower, "tasks") ||
		containsWord(lower, "pr") || strings.Contains(lower, "pull request") ||
		containsWord(lower, "port") || containsWord(lower, "repair")
	if !isBullet && !hasTaskWord {
		return false
	}
	for _, w := range uncitedClaimWords {
		if containsPhrase(lower, w) {
			return true
		}
	}
	return false
}

// hasTaskAttributeMismatch checks whether line attributes a kind tag or state
// from the fixed vocabulary to task that contradicts task's record.
func hasTaskAttributeMismatch(line, taskID string, allCited []string, task StewardCheckTask, allTasks map[string]StewardCheckTask) bool {
	scope := strings.ToLower(clauseForTask(line, taskID, allCited))

	// 1. Check fixed vocabulary of kind tags: port, repair, session, proposal, durable.
	for _, kind := range model.TaskKinds {
		if !mentionsKind(scope, kind) {
			continue
		}
		if slices.Contains(task.Kinds, kind) {
			continue
		}
		// Proposal is also satisfied if the task is in state proposed.
		if kind == model.TaskKindProposal && task.State == model.StateProposed {
			continue
		}
		// If the whole line was used as scope and another cited task on the line
		// actually has this kind (e.g. "Task 850 is blocked on repair task 849"),
		// only flag if none of the other cited tasks in scope carries the kind.
		if otherTaskHasKind(taskID, allCited, kind, allTasks) && !mentionsKindNearTask(line, taskID, kind) {
			continue
		}
		return true
	}

	// 2. Check fixed vocabulary of task states.
	for _, st := range stateVocabularyChecks {
		if !st.matches(scope) {
			continue
		}
		if st.satisfiedBy(task.State) {
			continue
		}
		if otherTaskHasState(taskID, allCited, st, allTasks) {
			continue
		}
		return true
	}

	return false
}

type stateVocabCheck struct {
	name        string
	phrases     []string
	ignoreAfter []string
	satisfiedBy func(model.State) bool
}

var stateVocabularyChecks = []stateVocabCheck{
	{
		name:    "merged",
		phrases: []string{"merged"},
		satisfiedBy: func(s model.State) bool {
			return s == model.StateClosed || s == model.StateCompleted
		},
	},
	{
		name:    "completed",
		phrases: []string{"completed"},
		satisfiedBy: func(s model.State) bool {
			return s == model.StateCompleted || s == model.StateClosed
		},
	},
	{
		name:        "closed",
		phrases:     []string{"closed"},
		ignoreAfter: []string{"closed unmerged"},
		satisfiedBy: func(s model.State) bool {
			return s == model.StateClosed
		},
	},
	{
		name:    "queued",
		phrases: []string{"queued"},
		satisfiedBy: func(s model.State) bool {
			return s == model.StateQueued
		},
	},
	{
		name:        "running",
		phrases:     []string{"running"},
		ignoreAfter: []string{"running ci", "running job", "running check"},
		satisfiedBy: func(s model.State) bool {
			return s == model.StateRunning
		},
	},
	{
		name:        "failed",
		phrases:     []string{"failed"},
		ignoreAfter: []string{"failed attempt", "failed attempts", "failed run", "failed runs", "failed job", "failed jobs", "failed check", "failed checks", "failed ci"},
		satisfiedBy: func(s model.State) bool {
			return s == model.StateFailed
		},
	},
	{
		name:        "proposed",
		phrases:     []string{"proposed"},
		ignoreAfter: []string{"proposed by", "proposed task", "proposed tasks", "proposed a", "proposed follow"},
		satisfiedBy: func(s model.State) bool {
			return s == model.StateProposed
		},
	},
	{
		name:    "deferred",
		phrases: []string{"deferred"},
		satisfiedBy: func(s model.State) bool {
			return s == model.StateDeferred
		},
	},
	{
		name:    "awaiting_reply",
		phrases: []string{"awaiting_reply", "awaiting reply"},
		satisfiedBy: func(s model.State) bool {
			return s == model.StateAwaitingReply
		},
	},
	{
		name:    "awaiting_submit",
		phrases: []string{"awaiting_submit", "awaiting submit"},
		satisfiedBy: func(s model.State) bool {
			return s == model.StateAwaitingSubmit
		},
	},
}

func (c stateVocabCheck) matches(scope string) bool {
	for _, ignore := range c.ignoreAfter {
		scope = strings.ReplaceAll(scope, ignore, "")
	}
	for _, p := range c.phrases {
		if containsUnnegatedPhrase(scope, p) {
			return true
		}
	}
	return false
}

func mentionsKind(scope, kind string) bool {
	switch kind {
	case model.TaskKindPort:
		return containsUnnegatedPhrase(scope, "port") || containsUnnegatedPhrase(scope, "ports")
	case model.TaskKindRepair:
		// Ignore "repair_needed" / "repair_queued" merge-queue states unless "repair" stands as a kind.
		cleaned := strings.ReplaceAll(scope, "repair_needed", "")
		cleaned = strings.ReplaceAll(cleaned, "repair_queued", "")
		return containsUnnegatedPhrase(cleaned, "repair") || containsUnnegatedPhrase(cleaned, "repairs")
	case model.TaskKindSession:
		return containsUnnegatedPhrase(scope, "session") || containsUnnegatedPhrase(scope, "sessions")
	case model.TaskKindProposal:
		return containsUnnegatedPhrase(scope, "proposal") || containsUnnegatedPhrase(scope, "proposals")
	case model.TaskKindDurable:
		return containsUnnegatedPhrase(scope, "durable")
	}
	return false
}

func mentionsKindNearTask(line, taskID, kind string) bool {
	lower := strings.ToLower(line)
	return mentionsKind(lower, kind) && !strings.Contains(lower, "behind") && !strings.Contains(lower, "blocked on") && !strings.Contains(lower, "waiting on")
}

func otherTaskHasKind(taskID string, allCited []string, kind string, allTasks map[string]StewardCheckTask) bool {
	for _, otherID := range allCited {
		if otherID == taskID {
			continue
		}
		if other, ok := allTasks[otherID]; ok && slices.Contains(other.Kinds, kind) {
			return true
		}
	}
	return false
}

func otherTaskHasState(taskID string, allCited []string, st stateVocabCheck, allTasks map[string]StewardCheckTask) bool {
	for _, otherID := range allCited {
		if otherID == taskID {
			continue
		}
		if other, ok := allTasks[otherID]; ok && st.satisfiedBy(other.State) {
			return true
		}
	}
	return false
}

func clauseForTask(line, taskID string, allCited []string) string {
	if len(allCited) <= 1 {
		return line
	}
	// If the line splits into semicolon-separated clauses, return the clause mentioning taskID.
	for _, part := range strings.Split(line, ";") {
		if slices.Contains(extractCitedTaskIDs(part), taskID) {
			return part
		}
	}
	return line
}

func containsUnnegatedPhrase(s, phrase string) bool {
	idx := 0
	for {
		pos := indexPhrase(s[idx:], phrase)
		if pos < 0 {
			return false
		}
		abs := idx + pos
		prefix := strings.TrimSpace(s[:abs])
		if !endsWithNegation(prefix) {
			return true
		}
		idx = abs + len(phrase)
	}
}

func endsWithNegation(prefix string) bool {
	for _, neg := range []string{"not", "not a", "not an", "no", "non", "never", "neither", "nor", "isn't", "isnt", "wasn't", "wasnt", "aren't", "arent", "without"} {
		if prefix == neg || strings.HasSuffix(prefix, " "+neg) || strings.HasSuffix(prefix, "-"+neg) {
			return true
		}
	}
	return false
}

func containsWord(s, word string) bool {
	return indexPhrase(s, word) >= 0
}

func containsPhrase(s, phrase string) bool {
	return indexPhrase(s, phrase) >= 0
}

func indexPhrase(s, phrase string) int {
	start := 0
	for {
		i := strings.Index(s[start:], phrase)
		if i < 0 {
			return -1
		}
		abs := start + i
		beforeOK := abs == 0 || !isWordChar(s[abs-1])
		end := abs + len(phrase)
		afterOK := end == len(s) || !isWordChar(s[end])
		if beforeOK && afterOK {
			return abs
		}
		start = abs + 1
	}
}

func isWordChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// loadStewardCheckTasks loads the current StewardCheckTask snapshot for every
// task in store.
func loadStewardCheckTasks(ctx context.Context, store *model.Store) (map[string]StewardCheckTask, error) {
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	states, err := store.States(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]StewardCheckTask, len(tasks))
	for _, t := range tasks {
		st := states[t.ID]
		out[t.ID] = StewardCheckTask{
			ID:    t.ID,
			State: st,
			Kinds: model.TaskKindTags(t, st),
		}
	}
	return out, nil
}

// checkAndRecordStewardReport runs stewardReportCheck when a pipeline steward
// run finishes with a report, stores the result in task_run_report_check,
// raises agent.steward:report-mismatch when the report has any mismatch, and
// evaluates the 7-day rolling regression rate.
func checkAndRecordStewardReport(ctx context.Context, store *model.Store, task model.Task, runID string,
	result *agent.Result, now time.Time) {

	if result == nil || runID == "" {
		return
	}
	if task.TemplateID == model.StewardAuditTemplateID || slices.Contains(task.Tags, model.ScheduleFiringTag(model.StewardAuditTemplateID)) {
		recordStewardAuditReport(ctx, store, result, now)
		return
	}
	if task.TemplateID != model.PipelineStewardTemplateID && !slices.Contains(task.Tags, model.ScheduleFiringTag(model.PipelineStewardTemplateID)) {
		return
	}
	report, ok := effectiveCommentOnIssue(result)
	if !ok || strings.TrimSpace(report) == "" {
		return
	}

	calls, err := store.RunAPICallsForRun(ctx, runID)
	if err != nil {
		log.Printf("orchestrator: run %s: reading API calls for report check: %v", runID, err)
	}
	if len(calls) == 0 {
		calls = runTelemetryAt(result, now).APICalls
	}

	tasks, err := loadStewardCheckTasks(ctx, store)
	if err != nil {
		log.Printf("orchestrator: run %s: loading tasks for steward report check: %v", runID, err)
		return
	}

	promptIDs := extractCitedTaskIDs(task.Body)
	if comments, err := store.Comments(ctx, task.ID); err == nil {
		for _, c := range comments {
			if c.Author.Actor == model.SchedulePrincipal {
				promptIDs = append(promptIDs, extractCitedTaskIDs(c.Body)...)
			}
		}
	}

	checkRes := stewardReportCheck(runID, report, calls, tasks, promptIDs...)
	if err := store.RecordRunReportCheck(ctx, checkRes.Check); err != nil {
		log.Printf("orchestrator: run %s: recording steward report check: %v", runID, err)
		return
	}

	if checkRes.HasMismatch() {
		var body strings.Builder
		fmt.Fprintf(&body, "Steward wake `%s` on [Task #%s](/tasks/%s) produced a report with %d claim(s): %d uncited, %d unread, %d mismatched.\n",
			runID, task.ID, task.ID,
			checkRes.Check.Claims, checkRes.Check.Uncited, checkRes.Check.Unread, checkRes.Check.Mismatched)
		if len(checkRes.OffendingLines) > 0 {
			body.WriteString("\nOffending lines:\n")
			for _, l := range checkRes.OffendingLines {
				fmt.Fprintf(&body, "- `%s`\n", l)
			}
		}
		if _, err := raisePipelineNotification(ctx, store, model.Notification{
			Subject:  model.SubjectStewardReportMismatch,
			Family:   "agent.steward",
			Severity: model.NotificationWarning,
			Title: fmt.Sprintf("Steward report mismatch on wake %s (%d mismatched, %d unread, %d uncited)",
				runID, checkRes.Check.Mismatched, checkRes.Check.Unread, checkRes.Check.Uncited),
			Body:     strings.TrimSpace(body.String()),
			TaskID:   task.ID,
			RaisedBy: stewardArmsNotifier,
			Actions:  []string{"open-task", "investigate"},
		}, now); err != nil {
			log.Printf("orchestrator: run %s: raising %s: %v", runID, model.SubjectStewardReportMismatch, err)
		}
	}

	if err := ReconcileStewardRegression(ctx, store, now); err != nil {
		log.Printf("orchestrator: run %s: reconciling steward regression: %v", runID, err)
	}
}

// ParseStewardAuditReport parses `[run <run_id>] claims=<n> uncited=<n> unread=<n> mismatched=<n>`
// lines from a weekly steward-audit task's closing comment.
func ParseStewardAuditReport(report string) []model.RunReportCheck {
	var out []model.RunReportCheck
	for _, m := range auditLineRe.FindAllStringSubmatch(report, -1) {
		claims, _ := strconv.Atoi(m[2])
		uncited, _ := strconv.Atoi(m[3])
		unread, _ := strconv.Atoi(m[4])
		mismatched, _ := strconv.Atoi(m[5])
		out = append(out, model.RunReportCheck{
			RunID:      m[1],
			Claims:     claims,
			Uncited:    uncited,
			Unread:     unread,
			Mismatched: mismatched,
		})
	}
	return out
}

func recordStewardAuditReport(ctx context.Context, store *model.Store, result *agent.Result, now time.Time) {
	report, ok := effectiveCommentOnIssue(result)
	if !ok || strings.TrimSpace(report) == "" {
		return
	}
	checks := ParseStewardAuditReport(report)
	if len(checks) == 0 {
		return
	}
	for _, c := range checks {
		// Merge with any deterministic check already recorded for this wake so
		// an audited report can surface additional mismatches without erasing
		// or lowering deterministic findings.
		if existing, exists, err := store.GetRunReportCheck(ctx, c.RunID); err == nil && exists {
			c.Claims = max(existing.Claims, c.Claims)
			c.Uncited = max(existing.Uncited, c.Uncited)
			c.Unread = max(existing.Unread, c.Unread)
			c.Mismatched = max(existing.Mismatched, c.Mismatched)
		}
		if minClaims := max(c.Uncited, c.Unread, c.Mismatched); c.Claims < minClaims {
			c.Claims = minClaims
		}
		if err := store.RecordRunReportCheck(ctx, c); err != nil {
			log.Printf("orchestrator: recording audited report check for %s: %v", c.RunID, err)
		}
	}
	if err := ReconcileStewardRegression(ctx, store, now); err != nil {
		log.Printf("orchestrator: reconciling steward regression after audit: %v", err)
	}
}

// SampleStewardWakesForAudit selects 1 wake in every (every = 10 by default)
// from runIDs in deterministic order.
func SampleStewardWakesForAudit(runIDs []string, every int) []string {
	if every <= 0 {
		every = model.StewardAuditSampleEvery
	}
	if len(runIDs) == 0 {
		return nil
	}
	var sampled []string
	for i := 0; i < len(runIDs); i += every {
		sampled = append(sampled, runIDs[i])
	}
	return sampled
}

// ReconcileStewardRegression evaluates the 7-day rolling steward report-check
// failure rate and raises or clears pipeline.steward-regression when the rate
// passes 5% of claims.
func ReconcileStewardRegression(ctx context.Context, store *model.Store, now time.Time) error {
	checks, err := store.RunReportChecks(ctx)
	if err != nil {
		return err
	}
	runs, err := store.RunTimings(ctx)
	if err != nil {
		return err
	}
	runFinished := make(map[string]time.Time, len(runs))
	for _, r := range runs {
		switch {
		case r.FinishedAt != nil:
			runFinished[r.RunID] = *r.FinishedAt
		default:
			runFinished[r.RunID] = r.StartedAt
		}
	}

	cutoff := now.Add(-StewardRegressionWindow)
	var totalClaims, totalBad int
	for _, c := range checks {
		at, ok := runFinished[c.RunID]
		if ok && !at.IsZero() && at.Before(cutoff) {
			continue
		}
		totalClaims += c.Claims
		bad := c.Uncited + c.Unread + c.Mismatched
		if c.Claims > 0 && bad > c.Claims {
			bad = c.Claims
		}
		totalBad += bad
	}

	if totalClaims > 0 && float64(totalBad)/float64(totalClaims) > StewardRegressionThreshold {
		ratePct := float64(totalBad) * 100 / float64(totalClaims)
		return ensurePipelineNotification(ctx, store, model.Notification{
			Subject:  model.SubjectStewardRegression,
			Family:   "pipeline.steward-regression",
			Severity: model.NotificationWarning,
			Title:    fmt.Sprintf("Pipeline steward report failure rate is %.1f%% over the last 7 days", ratePct),
			Body: fmt.Sprintf("Over the last 7 days, %d of %d steward report claim(s) (%.1f%%) were uncited, cited-but-unread, or mismatched the record (threshold: 5%%).",
				totalBad, totalClaims, ratePct),
			RaisedBy: stewardArmsNotifier,
			Actions:  []string{"investigate"},
		}, now)
	}
	return clearPipelineNotification(ctx, store, model.SubjectStewardRegression, stewardArmsNotifier)
}
