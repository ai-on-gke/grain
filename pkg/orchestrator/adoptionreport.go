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
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// AdoptionReport is what this daemon's startup adoption pass did with the
// runs a previous daemon left in run hosts (RecoverRunHosts): which it
// adopted and which it lost, and why. The post-upgrade check reports it
// beside the restart gap (docs/non-disruptive-upgrade.md, piece 6 step 6),
// since the upgrade that caused the restart is the one that asks whether
// the work came through it.
type AdoptionReport struct {
	// At is when the pass ran: this process's startup.
	At time.Time
	// Adopted is the runs this daemon took over, to carry to their ends
	// (ResumeAdoptedRun).
	Adopted []AdoptedRunNote
	// Lost is the runs whose rows the pass finished as interrupted, each
	// with the reason the listing, the spool or this daemon gave.
	Lost []AdoptedRunNote
	// Freed is how many run hosts no live run claimed, destroyed.
	Freed int
	// CIAdopted is the in-flight CI jobs this daemon took over across the
	// restart.
	CIAdopted []AdoptedCIJobNote
	// CILost is the in-flight CI jobs whose sandboxes or step runners did
	// not survive the restart.
	CILost []AdoptedCIJobNote
	// Expected is, by framework, why this install's own run location
	// for that CLI loses its runs on a restart
	// (Config.UpgradeInterrupts): a lost run on one of these is what the
	// install was set up to do, not a regression. The check says which
	// lost runs are which, since a cloudtop install keeps claude in the
	// daemon's container on purpose (task 1506).
	Expected map[string]string
}

// AdoptedCIJobNote is one CI job in an AdoptionReport.
type AdoptedCIJobNote struct {
	Run     string
	Job     string
	Repo    string
	Ref     string
	Sandbox string
	Reason  string
}

// AdoptedRunNote is one run in an AdoptionReport.
type AdoptedRunNote struct {
	Run, Task string
	// Framework is the agent framework the run was on (claude, codex,
	// antigravity), when its row names one; empty for a run that ended
	// in setup, before its CLI was chosen. withRows fills it in.
	Framework string
	// Setup is a lost run that ended while it was still setting up: its
	// sandbox building, no agent CLI started, so no framework yet.
	Setup bool
	// Reason is why a lost run was lost; for an adopted one, the gap no
	// daemon was reading its spool for.
	Reason string
	// Session is the console session the run's task is
	// (model.ConsoleSessionIDOf): model.DirectorSessionID for the
	// Director, another id for a console session, empty for ordinary
	// work. A session is a durable task whose turns are task_run
	// attempts, so it is adopted or lost by the same rules as any run,
	// but the check and the pane name it, since a lost session is a
	// conversation a person was in the middle of. withRows fills it in.
	Session string
}

// NewAdoptionReport builds the report from RecoverRunHosts' plan and the
// runs its adopted seam took; an Adopt entry the seam declined was lost.
func NewAdoptionReport(at time.Time, plan runhost.Plan, took map[string]bool) *AdoptionReport {
	r := &AdoptionReport{At: at}
	// A release whose run a row claims is the run host of a lost run (its
	// protocol refused, its deadline past, its CLI killed) or a duplicate,
	// already accounted for in Lost or Adopted: Freed counts only the
	// ones no live run claimed, as its doc and describe say.
	claimed := map[string]bool{}
	for _, a := range plan.Adopt {
		claimed[a.Claim.Run] = true
	}
	for _, f := range plan.Finish {
		claimed[f.Claim.Run] = true
	}
	for _, rel := range plan.Release {
		if !claimed[rel.Host.Labels.Run] {
			r.Freed++
		}
	}
	for _, a := range plan.Adopt {
		if took[a.Claim.Run] {
			r.Adopted = append(r.Adopted, AdoptedRunNote{Run: a.Claim.Run, Task: a.Claim.Task,
				Reason: fmt.Sprintf("its spool went %s unread", a.Gap.Round(time.Second))})
			continue
		}
		r.Lost = append(r.Lost, AdoptedRunNote{Run: a.Claim.Run, Task: a.Claim.Task,
			Reason: fmt.Sprintf("its run host %s was still there, but this daemon could not adopt the run", a.Host.Name)})
	}
	for _, f := range plan.Finish {
		r.Lost = append(r.Lost, AdoptedRunNote{Run: f.Claim.Run, Task: f.Claim.Task, Reason: f.Reason})
	}
	return r
}

// StoppedWithTheDaemon is the reason a lost run is given when its row
// was finished as interrupted by the daemon that stopped, or by the next
// one's startup recovery, rather than by the adoption pass: its CLI was
// a child of the daemon (agent.LocalHost), not in a run host or grain
// hostd, so nothing was left running to adopt.
const StoppedWithTheDaemon = "its CLI ran inside the daemon's own container and stopped with it " +
	"(not in a run host or grain hostd, so nothing was left to adopt)"

// withRows completes the report from the store: the framework of every
// run it names (frameworks, Store.RunFrameworks), and every run that
// ended interrupted across the restart (interrupted,
// Store.InterruptedRunsSince from the upgrade's start) that the adoption
// pass never saw, as lost. Those are the runs a restart loses without a
// run host to show for it -- the CLIs that were children of the daemon,
// and the runs still setting up -- and before this the check counted
// none of them: an upgrade that lost eight runs reported "1 adopted, 0
// lost" (task 1244). r may be nil, for a restart with no adoption pass
// at all (no run hosts, no hostd); the result is nil only when there is
// nothing to report.
//
// sessions is the console session each task named is, by task id
// (AdoptedRunNote.Session), from UpgradeAdoption.
func (r *AdoptionReport) withRows(at time.Time, interrupted []model.RunEnd, frameworks, sessions map[string]string) *AdoptionReport {
	if r == nil {
		if len(interrupted) == 0 {
			return nil
		}
		r = &AdoptionReport{At: at}
	}
	out := *r
	seen := map[string]bool{}
	out.Adopted = append([]AdoptedRunNote(nil), r.Adopted...)
	out.Lost = append([]AdoptedRunNote(nil), r.Lost...)
	out.CIAdopted = append([]AdoptedCIJobNote(nil), r.CIAdopted...)
	out.CILost = append([]AdoptedCIJobNote(nil), r.CILost...)
	for i := range out.Adopted {
		seen[out.Adopted[i].Run] = true
		if out.Adopted[i].Framework == "" {
			out.Adopted[i].Framework = frameworks[out.Adopted[i].Run]
		}
		out.Adopted[i].Session = sessions[out.Adopted[i].Task]
	}
	for i := range out.Lost {
		seen[out.Lost[i].Run] = true
		if out.Lost[i].Framework == "" {
			out.Lost[i].Framework = frameworks[out.Lost[i].Run]
		}
		out.Lost[i].Session = sessions[out.Lost[i].Task]
	}
	for _, e := range interrupted {
		if seen[e.Run] {
			continue
		}
		seen[e.Run] = true
		note := AdoptedRunNote{Run: e.Run, Task: e.Task, Framework: e.Framework, Reason: StoppedWithTheDaemon, Session: sessions[e.Task]}
		if e.Framework == "" {
			note.Setup = true
			note.Reason = "it was still setting up (no agent CLI started yet) when the daemon stopped"
		}
		out.Lost = append(out.Lost, note)
	}
	return &out
}

// TaskIDs is every task the report names, for the sessions withRows
// is given.
func (r *AdoptionReport) TaskIDs() []string {
	if r == nil {
		return nil
	}
	var ids []string
	for _, a := range r.Adopted {
		ids = append(ids, a.Task)
	}
	for _, l := range r.Lost {
		ids = append(ids, l.Task)
	}
	return ids
}

// SessionLabel is how a run of a console session is named after its
// task: " (the Director session)", " (console session ops)", or "" for
// ordinary work.
func SessionLabel(session string) string {
	switch session {
	case "":
		return ""
	case model.DirectorSessionID:
		return " (the Director session)"
	}
	return " (console session " + session + ")"
}

// sessionCounts is how many of the report's runs are sessions, adopted
// and lost.
func (r *AdoptionReport) sessionCounts() (adopted, lost int) {
	for _, a := range r.Adopted {
		if a.Session != "" {
			adopted++
		}
	}
	for _, l := range r.Lost {
		if l.Session != "" {
			lost++
		}
	}
	return adopted, lost
}

// RunIDs is every run the report names, for Store.RunFrameworks.
func (r *AdoptionReport) RunIDs() []string {
	if r == nil {
		return nil
	}
	var ids []string
	for _, a := range r.Adopted {
		ids = append(ids, a.Run)
	}
	for _, l := range r.Lost {
		ids = append(ids, l.Run)
	}
	return ids
}

// LostByFramework is how many runs were lost on each framework, with
// "setup" for runs that had none yet and "unknown" for a run whose row
// names none, as "claude 7, codex 1".
func (r *AdoptionReport) LostByFramework() string {
	counts := map[string]int{}
	for _, l := range r.Lost {
		counts[frameworkLabel(l)]++
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("%s %d", name, counts[name])
	}
	return strings.Join(parts, ", ")
}

func frameworkLabel(l AdoptedRunNote) string {
	switch {
	case l.Framework != "":
		return l.Framework
	case l.Setup:
		return "setup"
	}
	return "unknown"
}

// onFramework is " on <framework>", or "" when the run had none.
func onFramework(framework string) string {
	if framework == "" {
		return ""
	}
	return " on " + framework
}

// describe is the report as the upgrade check's body says it.
func (r *AdoptionReport) describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nWhat happened to the agent runs in flight across the restart (the adoption pass at %s): "+
		"%d adopted, %d lost", r.At.UTC().Format(time.RFC3339), len(r.Adopted), len(r.Lost))
	if len(r.Lost) > 0 {
		fmt.Fprintf(&b, " (lost by framework: %s)", r.LostByFramework())
	}
	if r.Freed > 0 {
		fmt.Fprintf(&b, ", and %d run host(s) no live run claimed were freed", r.Freed)
	}
	if adopted, lost := r.sessionCounts(); adopted+lost > 0 {
		fmt.Fprintf(&b, "; of those, console and Director sessions: %d adopted, %d lost", adopted, lost)
	}
	fmt.Fprintf(&b, "; %d CI jobs adopted, %d lost.\n\n", len(r.CIAdopted), len(r.CILost))
	for _, a := range r.Adopted {
		fmt.Fprintf(&b, "- adopted: run %s of task %s%s%s (%s)\n", a.Run, a.Task, SessionLabel(a.Session), onFramework(a.Framework), a.Reason)
	}
	for _, l := range r.Lost {
		fmt.Fprintf(&b, "- lost: run %s of task %s%s%s, finished as interrupted because %s\n",
			l.Run, l.Task, SessionLabel(l.Session), onFramework(l.Framework), l.Reason)
	}
	for _, a := range r.CIAdopted {
		fmt.Fprintf(&b, "- adopted CI job: %s in run %s (%s@%s, sandbox %s)\n",
			a.Job, a.Run, a.Repo, a.Ref, a.Sandbox)
	}
	for _, l := range r.CILost {
		fmt.Fprintf(&b, "- lost CI job: %s in run %s (%s@%s): %s\n",
			l.Job, l.Run, l.Repo, l.Ref, l.Reason)
	}
	expected, unexpected := r.splitLost()
	if len(expected) > 0 {
		b.WriteString("\nThis install's own run locations lose these frameworks' runs on a restart, so their lost " +
			"runs are expected here rather than a regression -- check only that each lost run's task was re-queued:\n\n")
		for _, fw := range expected {
			fmt.Fprintf(&b, "- %s: %s\n", fw, r.Expected[fw])
		}
	}
	if unexpected > 0 {
		which := "Any lost run"
		if len(expected) > 0 {
			which = "Any other lost run"
		}
		b.WriteString("\n" + which + " is a regression: a CLI whose run location carries it across a " +
			"restart should have been adopted (docs/non-disruptive-upgrade.md, \"Which substrate carries which CLI\"). " +
			"Say which frameworks lost runs, and whether each lost run's task was re-queued.\n")
	}
	if len(r.Adopted) > 0 {
		b.WriteString("\nCheck that each adopted run went on to finish -- its pull request opened, its transcript " +
			"continuous across the restart -- rather than only that it was adopted.\n")
	}
	return b.String()
}

// splitLost is the frameworks, sorted, of the lost runs Expected
// accounts for, and how many lost runs it does not.
func (r *AdoptionReport) splitLost() (expected []string, unexpected int) {
	seen := map[string]bool{}
	for _, l := range r.Lost {
		if why := r.Expected[l.Framework]; l.Framework == "" || why == "" {
			unexpected++
			continue
		}
		if !seen[l.Framework] {
			seen[l.Framework] = true
			expected = append(expected, l.Framework)
		}
	}
	sort.Strings(expected)
	return expected, unexpected
}

// adoptionFor is the adoption report this process made (adoption,
// Config.Adoption), when the restart that started it is the one status's
// upgrade ended with (restartGapFor's rule), or nil.
func adoptionFor(adoption func() *AdoptionReport, startedAt *time.Time) *AdoptionReport {
	if adoption == nil {
		return nil
	}
	r := adoption()
	if r == nil || (startedAt != nil && r.At.Before(*startedAt)) {
		return nil
	}
	return r
}
