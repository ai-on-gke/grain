// pause.go is "grain pause": the two gates that stop this deployment
// dispatching, read at a terminal, and the flags that change the one an
// operator owns.
//
// The agent usage-limit gate (orchestrator.Pause, served as
// GET /api/pause) is the involuntary one -- a provider said one model
// connection's credential has no budget left in this window -- and -lift
// is DELETE /api/pause, the same thing the UI banner's "Resume now"
// button sends: every pause the caller may lift, or with -connection
// just that connection's.
//
// The pipeline gate (model.PipelinePause, GET/POST/DELETE
// /api/pipeline-pause) is the deliberate one: somebody decided this
// deployment should pick no new work up. -set closes it and -resume
// opens it again. grain also takes one of these for itself around a
// check of an upgrade it has just installed, and such a pause names the
// check it is waiting for and lifts itself when that check is done --
// which is why the reading below says which task a pause is waiting on
// rather than only that there is one.
//
// A bare `grain pause` prints both, because "why is nothing being
// dispatched?" is one question and an operator should not have to know
// which of two mechanisms to suspect before they can ask it.
//
// grain/task-132 wired that gate to the browser and nothing else, so an
// operator ssh'd into a deployment, or driving one with `grain -server`,
// had two ways to find out why a queue of ready tasks was dispatching
// nothing: the daemon's journal, or the detail of an attempt they would
// have to know to open. `grain metrics` deliberately does not carry it
// (pkg/ui/pause.go's own doc comment on why a live gauge does not belong
// in a report computed over rows), which leaves this.
//
// It is spelled as a noun with a flag, like `grain settings`, rather
// than as a `grain resume` verb: every verb in this CLI acts on a task
// named by its argument (approve, retry, reopen), so a bare `grain
// resume` would read as one of those with its id left off. `grain pause`
// prints the reading; `grain pause -lift` changes it and prints what it
// left behind.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/ui"
)

func cmdPause(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain pause", flag.ContinueOnError)
	lift := fs.Bool("lift", false,
		"lift the agent usage-limit pauses you may lift: dispatch resumes on the next reconcile tick")
	connection := fs.String("connection", "", "with -lift, lift only this model connection's pauses")
	liftModel := fs.String("model", "", "with -lift -connection, lift only the pause on this model of that connection")
	set := fs.Bool("set", false,
		"stop this deployment picking new tasks up -- runs already in flight are left to finish")
	reason := fs.String("reason", "", "why, alongside -set: shown in this reading and on the UI's banner")
	resume := fs.Bool("resume", false,
		"let this deployment pick work up again, lifting the pipeline pause")
	now := fs.Bool("now", false,
		"with a task id: skip a running task's grace period and stop its run at once")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// `grain pause <id> [-now]` is a different pause: one task's
	// (ui.Client.Pause), told apart from the deployment's by its task id.
	if fs.NArg() == 1 && !*set && !*resume && !*lift && taskIDPattern.MatchString(fs.Arg(0)) {
		id := strings.TrimPrefix(fs.Arg(0), "#")
		if err := c.Pause(ctx, id, ui.PauseOptions{Immediate: *now}); err != nil {
			return err
		}
		return respond(ctx, c, out, id)
	}
	if *now {
		return errors.New("-now applies to pausing one task: grain pause -now <id>")
	}
	// Written out rather than left to noArgs, because the mistake this
	// catches has one obvious intent: -set is a boolean, so `grain pause
	// -set "the queue is wedged"` used to record the pause with grain's
	// own "paused by hand" and drop the sentence the operator typed --
	// leaving the banner, and the next person to run this, with no idea
	// why the deployment had stopped (found by hand, task 209).
	//
	// Only where a reason is what the word could have been, though: a
	// stray word beside -resume or -lift is asking to undo a pause, not
	// to explain one, so that one gets the CLI's ordinary wording.
	if fs.NArg() > 0 {
		if *resume || *lift {
			return noArgs(fs, fs.Args())
		}
		return fmt.Errorf("grain pause takes no arguments, so %q would be dropped -- "+
			"a reason is a flag's value: grain pause -set -reason %q", fs.Arg(0), fs.Arg(0))
	}
	if *connection != "" && !*lift {
		return errors.New("-connection names which pause -lift lifts: pass it with -lift")
	}
	if *liftModel != "" && (*connection == "" || !*lift) {
		return errors.New("-model names which model's pause on a connection -lift lifts: pass it with -lift -connection <id>")
	}
	if *set && *resume {
		return errors.New("-set and -resume ask for opposite things: pass one of them")
	}

	switch {
	case *set:
		status, paused, err := c.PausePipeline(ctx, *reason)
		if err != nil {
			return err
		}
		out.pauses(pauseView{Pipeline: &pipelinePauseView{Pause: status, Paused: &paused}})
		return nil
	case *resume:
		status, resumed, err := c.ResumePipeline(ctx)
		if err != nil {
			return err
		}
		out.pauses(pauseView{Pipeline: &pipelinePauseView{Pause: status, Resumed: &resumed}})
		return nil
	case *lift:
		status, lifted, err := c.LiftAgentPause(ctx, ui.AgentPauseLift{
			Connection: strings.TrimSpace(*connection), Model: strings.TrimSpace(*liftModel)})
		if err != nil {
			return err
		}
		out.pauses(pauseView{Agent: &agentPauseView{Enabled: true, Pause: &status, Lifted: &lifted}})
		return nil
	}

	// A bare read is both gates. Agent first: it is the one that is
	// nobody's decision, so an operator who finds it shut knows there is
	// nothing of theirs to undo.
	agent, enabled, err := c.AgentPause(ctx)
	if err != nil {
		return err
	}
	view := pauseView{Agent: &agentPauseView{Enabled: enabled}}
	if enabled {
		view.Agent.Pause = &agent
	}
	pipeline, err := c.PipelinePause(ctx)
	if err != nil {
		return err
	}
	view.Pipeline = &pipelinePauseView{Pause: pipeline}
	out.pauses(view)
	return nil
}

// pauseView is what `grain pause -json` prints: whichever of the two
// gates this invocation asked about, each in the shape its own route
// answers in, so a reading piped into something else and one fetched
// with curl carry the same fields. (pkg/ui's own response types are
// unexported -- they are the server's private wire shapes -- so these
// restate them.)
type pauseView struct {
	Agent    *agentPauseView    `json:"agent,omitempty"`
	Pipeline *pipelinePauseView `json:"pipeline,omitempty"`
}

type agentPauseView struct {
	Enabled bool                 `json:"enabled"`
	Pause   *ui.AgentPauseStatus `json:"pause,omitempty"`
	Lifted  *bool                `json:"lifted,omitempty"`
}

type pipelinePauseView struct {
	Pause ui.PipelinePauseStatus `json:"pause"`
	// Paused is -set's own report and Resumed is -resume's: whether this
	// command is what changed the gate, as opposed to finding the
	// deployment already that way. Both absent on a plain read.
	Paused  *bool `json:"pausedNow,omitempty"`
	Resumed *bool `json:"resumed,omitempty"`
}

// pauses renders whichever halves of view are present -- as one JSON
// object under -json, so a bare read that asks both routes still prints
// a single document, and as prose otherwise.
func (p *printer) pauses(view pauseView) {
	if p.json {
		p.encode(view)
		return
	}
	if view.Agent != nil {
		p.agentPause(*view.Agent)
	}
	if view.Pipeline != nil {
		if view.Agent != nil {
			fmt.Println()
		}
		p.pipelinePause(*view.Pipeline)
	}
}

// agentPause renders one reading of the usage-limit gate. Lifted is nil
// for a plain read and non-nil for a -lift, where whether there was
// anything to lift is the answer the operator asked for.
//
// A deployment whose UI was handed no gate (enabled false) says so
// rather than printing "not paused": this CLI is pointed at whatever
// -server names, and "nothing is paused here" from a daemon that could
// not tell either way is the one wrong answer -- it is exactly what an
// operator would act on to conclude the limit is not the problem.
func (p *printer) agentPause(view agentPauseView) {
	if !view.Enabled {
		fmt.Println("no agent pause reported: this deployment's UI is not wired to a reconcile loop" +
			"\nthat has one, so nothing here says whether a usage limit has stopped dispatch")
		return
	}
	var status ui.AgentPauseStatus
	if view.Pause != nil {
		status = *view.Pause
	}
	if view.Lifted != nil {
		if *view.Lifted {
			fmt.Println("pause lifted: dispatch resumes on the next reconcile tick")
		} else {
			fmt.Println("nothing to lift: dispatch was not paused")
		}
	}
	if !status.Paused {
		if view.Lifted == nil {
			fmt.Println("dispatch is running: no agent usage limit is in force")
		}
		return
	}
	// One block per pause, each saying whose credential it is first: a
	// pause covers one model connection's key, and so one team's work on
	// it, and leaves the rest of the deployment dispatching
	// (orchestrator.Pause) -- "dispatch is paused" on its own would have
	// an operator reading a stopped deployment when one connection has
	// stopped. The provider's own sentence last and verbatim: it names
	// the window, which is the half of this an operator can act on. The
	// instant is what somebody deciding whether to wait plans around; the
	// duration beside it is what says whether this is nearly over.
	for i, entry := range status.Pauses {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("dispatch is paused onto %s: its credential has no budget left in this window\n", pauseEntryName(entry))
		fmt.Printf("resumes at: %s (in %s)\n",
			entry.Until.Format(time.RFC3339), daemon.Seconds(entry.SecondsRemaining))
		if entry.Reason != "" {
			fmt.Printf("reason:     %s\n", entry.Reason)
		}
	}
	if view.Lifted == nil {
		fmt.Println("\nlift one now with \"grain pause -lift -connection <id> [-model <model>]\" (or every one you may lift\n" +
			"with \"grain pause -lift\") -- worth doing only if you know something this deployment\n" +
			"cannot, such as that the plan behind the refusal has been topped up")
	}
}

// pauseEntryName says whose credential one pause is on: a connection of
// a team, or a team's runs on a framework with no connection.
func pauseEntryName(entry ui.AgentPauseEntry) string {
	if entry.Connection == "" {
		if entry.Framework == "" {
			return fmt.Sprintf("team %s", entry.Team)
		}
		return fmt.Sprintf("team %s's %s runs (no connection)", entry.Team, entry.Framework)
	}
	name := entry.Connection
	if entry.ConnectionName != "" && entry.ConnectionName != entry.Connection {
		name = fmt.Sprintf("%s (%s)", entry.ConnectionName, entry.Connection)
	}
	if entry.Model != "" {
		return fmt.Sprintf("model %s on connection %s of team %s (its other models keep dispatching)", entry.Model, name, entry.Team)
	}
	if entry.Escalated != "" {
		return fmt.Sprintf("connection %s of team %s, every model (escalated: %s)", name, entry.Team, entry.Escalated)
	}
	return fmt.Sprintf("connection %s of team %s", name, entry.Team)
}

// pipelinePause renders one reading of the deliberate gate, and what a
// -set or a -resume just did to it.
//
// A pause that names a task says so, and says it lifts itself: that is
// grain's own pause around a check of an upgrade it installed, and an
// operator who reaches for -resume without being told that would be
// undoing something that was about to undo itself.
func (p *printer) pipelinePause(view pipelinePauseView) {
	status := view.Pause
	if view.Paused != nil {
		if *view.Paused {
			fmt.Println("pipeline paused: no new task will be picked up -- runs already in flight" +
				"\nare left to finish")
		} else {
			fmt.Println("the pipeline was already paused; its reason has been replaced with yours")
		}
	}
	if view.Resumed != nil {
		if *view.Resumed {
			fmt.Println("pipeline resumed: work is picked up again on the next reconcile tick")
		} else {
			fmt.Println("nothing to resume: this deployment was already picking work up")
		}
		return
	}
	if !status.Paused {
		if view.Paused == nil {
			fmt.Println("the pipeline is running: new tasks are being picked up")
		}
		return
	}
	fmt.Printf("paused since: %s\n", status.Since.Format(time.RFC3339))
	if status.Reason != "" {
		fmt.Printf("reason:       %s\n", status.Reason)
	}
	if status.ByID != "" {
		fmt.Printf("paused by:    %s (%s)\n", status.ByID, status.ByKind)
	}
	if status.UntilTask != "" {
		fmt.Printf("waiting on:   task %s -- this pause lifts itself when that task finishes\n",
			status.UntilTask)
	}
	if view.Paused == nil {
		fmt.Println("\nresume now with \"grain pause -resume\"")
	}
}

// taskIDPattern is what `grain pause` reads as a task id rather than a
// stray reason: a task number, optionally with its "#".
var taskIDPattern = regexp.MustCompile(`^#?[0-9]+$`)

// cmdResume is `grain resume <id>`: lift one task's pause (ui.Client.Resume),
// returning it to exactly the state and dispatch standing it had.
func cmdResume(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain resume", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	id, err := taskID(strings.TrimPrefix(fs.Arg(0), "#"))
	if err != nil {
		return err
	}
	if err := c.Resume(ctx, id); err != nil {
		return err
	}
	return respond(ctx, c, out, id)
}
