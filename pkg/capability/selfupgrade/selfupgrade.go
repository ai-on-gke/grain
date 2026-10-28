// Package selfupgrade is a GRANT model.CapabilityProvider: a task
// holding it may drive this deployment's own upgrades -- read where the
// deployment is, schedule a cut-over onto a branch, and call a scheduled
// one off -- so that upgrading can be an agent's job rather than an
// operator's, watching an in-flight run count and pressing a button at
// the right moment.
//
// It is the three routes pkg/ui already serves (GET /api/upgrade, POST
// /api/upgrade, DELETE /api/upgrade/scheduled) offered as tools, in the
// shape pkg/capability/deploymenthealth established: the tools live
// here, they take a Deployment interface rather than a store or an HTTP
// client, and pkg/cli/mcpserver.go implements that interface over the
// daemon's own REST API. Nothing here performs an upgrade; the daemon
// does, exactly as it does for the button.
//
// # Why this is not part of self-repair
//
// pkg/capability/selfrepair can technically already do this -- it runs
// shell on the controller host -- but its own contract rules the use
// case out twice over: every call is gated on a live human reply in the
// task's chat, and its tools are only ever added to an Interactive
// task's run, because nobody is necessarily watching a non-interactive
// task's chat closely enough to answer in time. An agent that manages
// upgrades is precisely a non-interactive, probably scheduled task with
// nobody watching. So this is its own capability with its own name,
// granted deliberately -- which is also what lets an operator grant "may
// upgrade this deployment" without granting "may run any command on the
// controller".
//
// # What stands in for the confirmation step
//
// Nothing here pauses for a human, and that is a real widening of what a
// granted task can do to the deployment it runs on. In exchange:
//
//   - it is off unless granted, and named so that granting it is a
//     conscious act;
//   - every call is logged in the daemon's journal with the task that
//     made it, the branch and the mode, so "why did this deployment
//     restart at 03:00" has an answer in the log -- and an upgrade
//     scheduled through these tools records that task in the schedule
//     itself (model.ScheduledUpgrade.By, grain/task-353), so the upgrade
//     pane can answer the same question without anybody reading a log;
//   - the safe mode is the default. afterTasksFinish is what an agent
//     gets unless it explicitly asks for something else, and an
//     immediate upgrade is documented as the expensive, destructive
//     thing it is.
//
// That last point is not a matter of taste. Over the seven days to
// 2026-09-19 the "release images" job on bwsalmon/grain was cancelled 25
// times, and 20 of those were an upgrade restarting the daemon out from
// under a 22-to-91-minute image build. A scheduled upgrade waits for
// live CI runs as well as agent runs (orchestrator's
// scheduledUpgradeWait counts both), so every one of those 20 would have
// been avoided by scheduling rather than starting at once.
package selfupgrade

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
)

// CapabilityName is the grant id, and the string ui.OfferedCapabilities,
// orchestrator's toolGrants and `grain mcpserver -grant` all name it by.
const CapabilityName = "self-upgrade"

// ProviderConfig configures self-upgrade capability settings.
type ProviderConfig struct{}

// Provider is the self-upgrade capability. Every method but Spec is
// model.BaseCapability's default -- see selfdebug.Provider's own doc
// comment for why a GRANT capability whose only effect is which tools a
// run gets needs nothing more: it mints nothing, places nothing and
// writes no prompt section.
type Provider struct {
	model.BaseCapability
}

// New returns a self-upgrade Provider, ready to register.
func New() *Provider {
	return NewWithConfig(ProviderConfig{})
}

// NewWithConfig returns a self-upgrade Provider with custom configuration.
func NewWithConfig(_ ProviderConfig) *Provider {
	return &Provider{}
}

// Spec implements model.CapabilityProvider.
func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        CapabilityName,
		Label:       "Self upgrade",
		Description: Description,
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
	}
}

// Description is the one sentence an operator reads before ticking this
// box, kept here so the picker (ui.OfferedCapabilities), the Settings
// pane and the provider's own Spec cannot drift apart.
const Description = "Let this task manage this deployment's own upgrades -- read the upgrade status, " +
	"schedule a cut-over onto a branch for when the deployment is idle, and cancel one -- each upgrade " +
	"needing a live reply in the task's chat before it runs. An upgrade restarts grain itself."

// ErrNotConfigured is what a Deployment reports when this deployment has
// no upgrader at all: it was started with neither -upgrade-image nor
// -upgrade-src-dir, so there is no pipeline for an upgrade to run
// (ui.errUpgradeUnavailable, which POST and DELETE answer with a 404 and
// GET answers by reporting enabled false).
//
// It is a sentinel rather than a string comparison because the two
// halves of the hop are in different packages: the adapter that makes
// the REST call recognises the 404, and the tools here turn it into a
// sentence. Both tools treat it as a fact about the deployment rather
// than as a failed call -- see notConfigured.
var ErrNotConfigured = errors.New("this deployment has no upgrader configured")

// Status is what the three routes report back, in the shape these tools
// render rather than the shape the wire carries: pkg/ui's own response
// struct is unexported, and importing pkg/ui from a capability package
// would put the daemon's whole HTTP surface behind a tool roster.
// pkg/cli/mcpserver.go converts.
type Status struct {
	// Enabled is false on a deployment with no upgrader configured,
	// with every other field zero. It is the read half of
	// ErrNotConfigured.
	Enabled bool
	// Mode is which pipeline this deployment's upgrade actually runs --
	// "image" or "source" (upgrade.Mode).
	Mode string
	// RunningImage and RunningCommit are where this deployment already
	// is: the image reference its service starts, or the commit its
	// installed binary was built from. Either may be empty -- a
	// deployment that cannot say shows one line fewer rather than
	// refusing to answer.
	RunningImage  string
	RunningCommit string
	// Channel is the channel this deployment follows -- latest,
	// candidate, main, or a branch -- read off the image it runs. It is
	// what upgrade_deployment goes to when no branch is named; empty when
	// the deployment cannot say (a sha- pin, or the source pipeline).
	Channel string
	// Branch, Phase and Detail describe the last upgrade *attempt*, not
	// a scheduled one: "how did the last cut-over go", which is empty on
	// a deployment that has never upgraded through this pipeline.
	Branch     string
	Phase      string
	Detail     string
	StartedAt  *time.Time
	FinishedAt *time.Time
	// InFlightRuns is every live run, console sessions included: what an
	// immediate upgrade would discard right now. It is deliberately a
	// different count from Scheduled's, which is what a scheduled
	// upgrade is still waiting for (ui.upgradeScheduledResponse's own
	// note on why one would be wrong for the other).
	InFlightRuns int
	// Scheduled is the upgrade this deployment is holding its queue and
	// waiting to apply, or nil when none is.
	Scheduled *Scheduled
}

// Scheduled is the pending schedule and how close it is to firing.
//
// ByKind and ByID are who asked for it: "human"/"automation" and this
// deployment's own actor for an operator at the pane, and "agent" with a
// task id for one of these tools' own calls -- which is how an agent
// reading the status can tell an upgrade another task scheduled from one
// a person did, and from one it scheduled itself on an earlier run.
type Scheduled struct {
	Branch      string
	RequestedAt time.Time
	ByKind      string
	ByID        string
	// BusyRuns is runs an agent is actually working in, and BusyCIRuns
	// jobs of grain's own CI that have not concluded. The cut-over
	// happens on the first reconcile tick where both are zero.
	BusyRuns   int
	BusyCIRuns int
}

// Deployment is the deployment these tools act on: the daemon, reached
// however the process serving the tools can reach it.
//
// An interface, and one whose methods name no deployment, for
// SandboxRecreator's own reason (pkg/mcp): the only deployment a run can
// ever act on is the one it is running inside, and nothing an agent puts
// in a tool call chooses another.
type Deployment interface {
	// UpgradeStatus reads GET /api/upgrade.
	UpgradeStatus(ctx context.Context) (Status, error)
	// StartUpgrade posts {branch, afterTasksFinish}. With
	// afterTasksFinish it writes the schedule down and starts nothing;
	// without it, the cut-over begins at once and the process serving
	// this call is among the things that will be restarted.
	StartUpgrade(ctx context.Context, branch string, afterTasksFinish bool) (Status, error)
	// CancelScheduledUpgrade deletes the pending schedule, reporting
	// whether there was one to cancel.
	CancelScheduledUpgrade(ctx context.Context) (status Status, cancelled bool, err error)
}

// Config is what Tools needs: which task is calling (for the journal and chat confirmation),
// what to call, where to log it, and how to obtain interactive human confirmation.
type Config struct {
	TaskID       string
	Deployment   Deployment
	Store        *model.Store
	PollInterval time.Duration
	Timeout      time.Duration
	// Confirm allows overriding the confirmation mechanism (for testing or custom confirmers).
	Confirm func(ctx context.Context, question string) (approved bool, reply string, err error)
	// Log is where every call is recorded. nil means log.Printf, which
	// is the daemon's journal.
	Log func(string, ...any)
}

// DefaultPollInterval is how often Confirm re-reads a task's comments
// while waiting on a reply, absent a caller-supplied interval.
const DefaultPollInterval = 5 * time.Second

// DefaultConfirmTimeout is how long Confirm waits for a reply before
// giving up and refusing, absent a caller-supplied timeout.
const DefaultConfirmTimeout = 15 * time.Minute

// Confirm posts question as a comment on taskID's conversation,
// marks the task awaiting reply, and polls until a human replies
// or timeout elapses.
func Confirm(ctx context.Context, store *model.Store, taskID, question string, pollInterval, timeout time.Duration) (approved bool, reply string, err error) {
	if store == nil {
		return false, "no store available for confirmation", errors.New("selfupgrade: cannot confirm without a store")
	}
	if taskID == "" {
		return false, "no task ID available for confirmation", errors.New("selfupgrade: cannot confirm without a task ID")
	}
	if pollInterval <= 0 {
		pollInterval = DefaultPollInterval
	}
	if timeout <= 0 {
		timeout = DefaultConfirmTimeout
	}

	agentPrincipal := model.Principal{Kind: model.PrincipalAgent, ID: taskID}
	commentID, err := store.AddComment(ctx, model.Comment{
		TaskID: taskID,
		Author: model.Attribution{
			Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
			OnBehalfOf: &agentPrincipal,
		},
		Body:      question,
		CreatedAt: time.Now(),
	})
	if err != nil {
		return false, "", fmt.Errorf("selfupgrade: posting confirmation question: %w", err)
	}
	markPending := func(now time.Time) {
		_ = store.ObserveField(context.Background(), taskID, now, func(o *model.Observation) {
			o.PendingQuestionCommentID = &commentID
		})
	}
	clearPending := func(now time.Time) {
		_ = store.ObserveField(context.Background(), taskID, now, func(o *model.Observation) {
			o.PendingQuestionCommentID = nil
		})
	}
	markPending(time.Now())

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			clearPending(time.Now())
			return false, "the run was cancelled while waiting for a reply", nil
		case now := <-ticker.C:
			comments, err := store.Comments(ctx, taskID)
			if err != nil {
				continue
			}
			for _, c := range comments {
				if c.ID <= commentID || c.Author.Actor.Kind != model.PrincipalHuman {
					continue
				}
				clearPending(now)
				return Decide(c.Body), c.Body, nil
			}
			if now.After(deadline) {
				clearPending(now)
				return false, "no reply within the timeout", nil
			}
		}
	}
}

// Decide reads a human's reply as approve or deny -- deliberately
// biased toward refusing: an unrecognised reply (a question back, a
// typo, small talk) must never be read as approval just because it
// wasn't a clear "no".
func Decide(reply string) bool {
	lower := strings.ToLower(strings.TrimSpace(reply))
	if strings.Contains(lower, "deny") || strings.Contains(lower, "denied") || strings.Contains(lower, "refuse") {
		return false
	}
	return strings.HasPrefix(lower, "approve") || strings.HasPrefix(lower, "yes")
}

// Tools returns the three tools a self-upgrade grant adds to a run.
//
// cfg.Deployment nil returns all three anyway, each refusing every call
// with a sentence saying why, for selfdebug.SourceTools' own reason: a
// run's tool roster should be a property of the grants it holds rather
// than of how the process serving them was configured, and every agent
// framework's allowedTools needs the names whether or not there is a
// live daemon behind them.
func Tools(cfg Config) []mcp.Tool {
	if cfg.Log == nil {
		cfg.Log = log.Printf
	}
	return []mcp.Tool{statusTool(cfg), upgradeTool(cfg), cancelTool(cfg)}
}

// noDeployment is what all three tools answer when this process has no
// route back to the daemon that owns the deployment -- a run started
// without -server/-task. An error, unlike notConfigured below: this is a
// broken run rather than a deployment that does not upgrade.
const noDeployment = "This run cannot reach the grain daemon it is running under, so it cannot " +
	"read or change this deployment's upgrades. Say so in a comment before you finish, so whoever " +
	"is waiting on the upgrade knows why nothing happened."

// notConfigured is the refusal for a deployment that has no upgrader at
// all -- started with neither -upgrade-image nor -upgrade-src-dir.
//
// Returned as an ordinary result rather than an error on purpose. It is
// not a call that failed and might work on a retry: it is a fact about
// how this deployment was installed, and an agent told "error" will
// reasonably try again, and again. So it is a plain sentence that says
// what is true, that nothing the agent does will change it, and what to
// do instead.
const notConfigured = "This deployment cannot upgrade itself: it was started with neither " +
	"-upgrade-image nor -upgrade-src-dir, so grain has no upgrade pipeline to run here. Nothing " +
	"you can do from this task will change that -- it is a flag on the daemon, which an operator " +
	"sets. No upgrade was scheduled, started or cancelled by this call. Say so in your closing " +
	"note rather than trying again."

var statusInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties":           map[string]any{},
}

func statusTool(cfg Config) mcp.Tool {
	return mcp.Tool{
		Name: "deployment_upgrade_status",
		Description: "Read where this deployment stands on upgrading itself: which pipeline it " +
			"upgrades through, what it is running now, how the last upgrade went, how many runs " +
			"an immediate upgrade would discard, and any upgrade already scheduled -- including " +
			"the agent runs and the live CI runs that scheduled one is still waiting for. " +
			"Read-only. It cannot tell you whether the image for the branch you mean to upgrade " +
			"onto has actually been published, or whether that branch's own CI is green: those " +
			"are the two preconditions for an upgrade worth making, and list_ci_runs answers " +
			"both for a task that also holds the grain-ci capability.",
		InputSchema: statusInputSchema,
		Handler: func(ctx context.Context, _ map[string]any) mcp.Result {
			if cfg.Deployment == nil {
				return mcp.Result{Text: noDeployment, IsError: true}
			}
			status, err := cfg.Deployment.UpgradeStatus(ctx)
			if err != nil {
				if errors.Is(err, ErrNotConfigured) {
					return mcp.Result{Text: notConfigured}
				}
				cfg.Log("grain self-upgrade: task=%q reading the upgrade status failed: %v", cfg.TaskID, err)
				return mcp.Result{Text: "Reading this deployment's upgrade status failed: " + err.Error(), IsError: true}
			}
			if !status.Enabled {
				return mcp.Result{Text: notConfigured}
			}
			cfg.Log("grain self-upgrade: task=%q read the upgrade status (mode=%s, scheduled=%s)",
				cfg.TaskID, status.Mode, scheduledBranch(status))
			return mcp.Result{Text: renderStatus(status)}
		},
	}
}

var upgradeInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"branch": map[string]any{
			"type": "string",
			"description": "the branch or channel of grain's own repository to upgrade this deployment " +
				"onto. Leave it out to upgrade onto the newest build of the channel this deployment already " +
				"follows (deployment_upgrade_status names it): latest on a deployment installed on a release, " +
				"which is the usual answer",
		},
		"after_tasks_finish": map[string]any{
			"type": "boolean",
			"description": "wait for this deployment to finish the work it is already doing before " +
				"cutting over. Defaults to true, which is the mode you want; false restarts grain " +
				"immediately and throws that work away",
		},
	},
}

func upgradeTool(cfg Config) mcp.Tool {
	return mcp.Tool{
		Name: "upgrade_deployment",
		Description: "Upgrade this deployment -- the grain that is running you -- onto a branch. " +
			"An upgrade ends in grain restarting itself.\n\n" +
			"Before proceeding, this tool posts a confirmation question to this task's conversation and " +
			"waits for an authorized human to approve the upgrade.\n\n" +
			"By default (after_tasks_finish true) this starts nothing immediately: once approved, it writes the upgrade " +
			"down, grain shuts its queue to new work, the runs and CI jobs already going are left " +
			"to finish, and the cut-over happens on the first moment there is nothing left to " +
			"lose. Use this mode. Your own run is one of the runs it waits for, so the upgrade " +
			"cannot land while you are still working: do not poll deployment_upgrade_status " +
			"waiting to see it applied -- you would wait forever. Schedule it, say what you did " +
			"and why, and end your run.\n\n" +
			"after_tasks_finish false begins the cut-over at once after human approval. Every run in flight is " +
			"discarded (each agent's work is lost and its task is queued again) and every live CI " +
			"job is killed mid-build, which on this deployment has repeatedly destroyed image " +
			"builds an hour or more in. It also very likely destroys you: the process that would " +
			"answer this call is the one being restarted, so expect no result and no further " +
			"turns. Write down what you are doing and why, in the task, BEFORE you call it that " +
			"way.\n\n" +
			"A scheduled upgrade is recorded as having been asked for by your task, and the " +
			"upgrade pane names you for as long as it waits; an immediate one is named in the " +
			"daemon's journal, which is all a restart leaves time for. Either way somebody will " +
			"see whose doing this was, so say in the task why you asked for it.\n\n" +
			"Check the preconditions first, whichever mode you use: that an image has actually " +
			"been published for the commit you are upgrading onto (an upgrade onto a branch with " +
			"no published image does nothing at all), and that the branch's own CI is green. With " +
			"the grain-ci capability, list_ci_runs answers both.",
		InputSchema: upgradeInputSchema,
		Handler: func(ctx context.Context, args map[string]any) mcp.Result {
			branch, _ := args["branch"].(string)
			branch = strings.TrimSpace(branch)
			afterTasksFinish := true
			if raw, ok := args["after_tasks_finish"]; ok {
				if b, isBool := raw.(bool); isBool {
					afterTasksFinish = b
				} else {
					return mcp.Result{
						Text:    "after_tasks_finish must be true or false.",
						IsError: true,
					}
				}
			}
			if cfg.Deployment == nil {
				return mcp.Result{Text: noDeployment, IsError: true}
			}
			if branch == "" {
				// Resolved here rather than left to the daemon, so the
				// question a human approves names where the upgrade goes.
				status, err := cfg.Deployment.UpgradeStatus(ctx)
				if err != nil {
					return mcp.Result{Text: fmt.Sprintf("could not read which channel this deployment follows: %v", err), IsError: true}
				}
				if !status.Enabled {
					return mcp.Result{Text: notConfigured}
				}
				if status.Channel == "" {
					return mcp.Result{Text: "This deployment follows no channel an upgrade could default to " +
						"(it is pinned to one build, or runs a binary it built): name the branch to upgrade onto.", IsError: true}
				}
				branch = status.Channel
			}

			var question string
			if afterTasksFinish {
				question = fmt.Sprintf(
					"This task wants to schedule a deployment upgrade onto branch `%s` (after in-flight tasks finish).\n\n"+
						"Reply **approve** to proceed with the upgrade, or **deny** (optionally with a reason) to refuse.",
					branch)
			} else {
				question = fmt.Sprintf(
					"This task wants to perform an **immediate** deployment upgrade onto branch `%s` (discarding all in-flight tasks and restarting grain immediately).\n\n"+
						"Reply **approve** to proceed with the upgrade, or **deny** (optionally with a reason) to refuse.",
					branch)
			}

			var approved bool
			var reply string
			var err error
			if cfg.Confirm != nil {
				approved, reply, err = cfg.Confirm(ctx, question)
			} else if cfg.Store != nil && cfg.TaskID != "" {
				approved, reply, err = Confirm(ctx, cfg.Store, cfg.TaskID, question, cfg.PollInterval, cfg.Timeout)
			} else {
				return mcp.Result{
					Text:    "Upgrading this deployment requires interactive human confirmation in chat, but no confirmation channel is available for this run.",
					IsError: true,
				}
			}

			if err != nil {
				cfg.Log("grain self-upgrade: task=%q confirmation failed: %v", cfg.TaskID, err)
				return mcp.Result{
					Text:    fmt.Sprintf("could not get confirmation: %v", err),
					IsError: true,
				}
			}
			if !approved {
				cfg.Log("grain self-upgrade: task=%q upgrade onto %q was denied: %s", cfg.TaskID, branch, reply)
				return mcp.Result{
					Text:    fmt.Sprintf("denied: %s", reply),
					IsError: true,
				}
			}

			// Logged before the call, not after. An immediate upgrade
			// restarts the daemon out from under this very process, so a
			// line written afterwards may never be written at all -- and
			// the whole point of logging this is that the restart has an
			// explanation in the journal beside it.
			cfg.Log("grain self-upgrade: task=%q asked to upgrade this deployment onto %q (%s)",
				cfg.TaskID, branch, modeWord(afterTasksFinish))

			status, err := cfg.Deployment.StartUpgrade(ctx, branch, afterTasksFinish)
			if err != nil {
				if errors.Is(err, ErrNotConfigured) {
					return mcp.Result{Text: notConfigured}
				}
				cfg.Log("grain self-upgrade: task=%q upgrading onto %q was refused: %v",
					cfg.TaskID, branch, err)
				return mcp.Result{
					Text:    fmt.Sprintf("Upgrading this deployment onto %s was refused: %v", branch, err),
					IsError: true,
				}
			}
			return mcp.Result{Text: renderStarted(branch, afterTasksFinish, status)}
		},
	}
}

var cancelInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties":           map[string]any{},
}

func cancelTool(cfg Config) mcp.Tool {
	return mcp.Tool{
		Name: "cancel_deployment_upgrade",
		Description: "Call off the upgrade this deployment is waiting to apply. The schedule is " +
			"forgotten and the queue this deployment was holding opens again on the next tick, so " +
			"work starts dispatching as before. It cancels nothing else: an upgrade that has " +
			"already begun cannot be called off from here, and cancelling when nothing is " +
			"scheduled is not an error -- it says which of the two happened.",
		InputSchema: cancelInputSchema,
		Handler: func(ctx context.Context, _ map[string]any) mcp.Result {
			if cfg.Deployment == nil {
				return mcp.Result{Text: noDeployment, IsError: true}
			}
			status, cancelled, err := cfg.Deployment.CancelScheduledUpgrade(ctx)
			if err != nil {
				if errors.Is(err, ErrNotConfigured) {
					return mcp.Result{Text: notConfigured}
				}
				cfg.Log("grain self-upgrade: task=%q cancelling the scheduled upgrade failed: %v",
					cfg.TaskID, err)
				return mcp.Result{Text: "Cancelling the scheduled upgrade failed: " + err.Error(), IsError: true}
			}
			cfg.Log("grain self-upgrade: task=%q cancelled the scheduled upgrade (cancelled=%t)",
				cfg.TaskID, cancelled)
			var b strings.Builder
			if cancelled {
				b.WriteString("The scheduled upgrade has been called off. This deployment is no longer " +
					"holding its queue for it, and will pick work up again on the next tick.\n\n")
			} else {
				b.WriteString("There was no upgrade scheduled, so this call changed nothing.\n\n")
			}
			b.WriteString(renderStatus(status))
			return mcp.Result{Text: b.String()}
		},
	}
}

// modeWord is how a call is named in the journal and in what the agent
// reads back -- the one thing a person looking at an unexplained restart
// most needs from the log line beside it.
func modeWord(afterTasksFinish bool) string {
	if afterTasksFinish {
		return "scheduled, after this deployment finishes its work"
	}
	return "immediately, discarding every run in flight"
}

func scheduledBranch(status Status) string {
	if status.Scheduled == nil {
		return "none"
	}
	return status.Scheduled.Branch
}

// renderStarted is what the agent reads after asking for an upgrade.
//
// The scheduled case spends its first paragraph on the one thing an
// agent will otherwise get wrong: it is itself one of the runs the
// cut-over is waiting for. An agent that schedules and then polls would
// hold up the very upgrade it scheduled, indefinitely, and read its own
// liveness as the deployment being busy.
func renderStarted(branch string, afterTasksFinish bool, status Status) string {
	var b strings.Builder
	if afterTasksFinish {
		fmt.Fprintf(&b, "The upgrade onto %s is scheduled. Nothing has restarted and nothing has "+
			"been discarded: this deployment has shut its queue to new work and will cut over on "+
			"the first moment no run and no CI job is still going.\n\n", branch)
		b.WriteString("Your own run counts as one of those runs, so the upgrade cannot happen " +
			"while you are still working. Do not wait for it -- write down what you scheduled and " +
			"why, and finish. If you change your mind, cancel_deployment_upgrade calls it off and " +
			"opens the queue again.\n\n")
	} else {
		fmt.Fprintf(&b, "An immediate upgrade onto %s has been accepted and is starting now. "+
			"grain is about to restart: every run in flight, including this one, is being "+
			"discarded, and every live CI job with it. If you are reading this at all, you were "+
			"lucky -- do not count on another turn.\n\n", branch)
	}
	b.WriteString(renderStatus(status))
	return b.String()
}

// renderStatus is the reading itself, as prose rather than JSON: the
// reader is a model deciding whether to upgrade, and "waiting on 2 runs
// and 1 CI run" is the useful shape of that answer.
func renderStatus(status Status) string {
	if !status.Enabled {
		return notConfigured
	}
	var b strings.Builder
	fmt.Fprintf(&b, "This deployment upgrades through its %s pipeline.\n", status.Mode)
	switch {
	case status.RunningImage != "":
		fmt.Fprintf(&b, "It is running %s.\n", status.RunningImage)
	case status.RunningCommit != "":
		fmt.Fprintf(&b, "It is running a binary built from %s.\n", status.RunningCommit)
	default:
		b.WriteString("It cannot say what it is running (no image reference and no build stamp).\n")
	}
	if status.Channel != "" {
		fmt.Fprintf(&b, "It follows the %s channel: an upgrade that names no branch goes to %s.\n", status.Channel, status.Channel)
	}

	if status.Phase == "" {
		b.WriteString("No upgrade has been attempted through this pipeline.\n")
	} else {
		fmt.Fprintf(&b, "The last upgrade attempt was onto %s: %s", branchOrUnknown(status.Branch), status.Phase)
		if status.Detail != "" {
			fmt.Fprintf(&b, " -- %s", status.Detail)
		}
		b.WriteString(".\n")
		if status.StartedAt != nil {
			fmt.Fprintf(&b, "It started at %s", status.StartedAt.UTC().Format(time.RFC3339))
			if status.FinishedAt != nil {
				fmt.Fprintf(&b, " and finished at %s", status.FinishedAt.UTC().Format(time.RFC3339))
			}
			b.WriteString(".\n")
		}
	}

	fmt.Fprintf(&b, "%s in flight right now -- what an immediate upgrade would discard.\n",
		plural(status.InFlightRuns, "run"))

	if status.Scheduled == nil {
		b.WriteString("No upgrade is scheduled.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "\nAn upgrade onto %s is already scheduled, asked for by %s at %s.\n",
		status.Scheduled.Branch,
		principalOrUnknown(status.Scheduled.ByKind, status.Scheduled.ByID),
		status.Scheduled.RequestedAt.UTC().Format(time.RFC3339))
	switch {
	case status.Scheduled.BusyRuns > 0 && status.Scheduled.BusyCIRuns > 0:
		fmt.Fprintf(&b, "It is waiting for %s and %s to finish.\n",
			plural(status.Scheduled.BusyRuns, "run"), plural(status.Scheduled.BusyCIRuns, "CI run"))
	case status.Scheduled.BusyRuns > 0:
		fmt.Fprintf(&b, "It is waiting for %s to finish.\n", plural(status.Scheduled.BusyRuns, "run"))
	case status.Scheduled.BusyCIRuns > 0:
		fmt.Fprintf(&b, "It is waiting for %s to finish -- a CI job an upgrade would kill mid-build.\n",
			plural(status.Scheduled.BusyCIRuns, "CI run"))
	default:
		b.WriteString("Nothing is left for it to wait for; it cuts over on the next tick.\n")
	}
	return b.String()
}

func branchOrUnknown(branch string) string {
	if branch == "" {
		return "an unrecorded branch"
	}
	return branch
}

// principalOrUnknown names whoever scheduled an upgrade, in the terms
// the reader can act on: an agent's id is a task id, and saying so is
// the difference between "task 353" -- which an agent can read, and
// whose reasoning it can go and find -- and an opaque number beside a
// branch name.
func principalOrUnknown(kind, id string) string {
	switch {
	case id == "":
		return "somebody this deployment did not record"
	case kind == string(model.PrincipalAgent):
		return "task " + id
	default:
		return id
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
