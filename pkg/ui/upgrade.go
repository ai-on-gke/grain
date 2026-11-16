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

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/qa/resolve"
	"github.com/ai-on-gke/grain/pkg/upgrade"
	"github.com/ai-on-gke/grain/pkg/version"
)

// errUpgradeUnavailable is what every upgrade handler reports when
// Config.Upgrader is nil -- this deployment was started with neither
// -upgrade-image (the container path, pkg/upgrade/image.go) nor
// -upgrade-src-dir (the build path), which is the normal case for
// `grain demo`'s throwaway UI and for any deployment that has not opted
// in yet. Mapped to 404, matching errSecretsUnavailable's own reasoning:
// there is no upgrade resource here to act on.
var errUpgradeUnavailable = errors.New(
	"upgrading is not available: this deployment has neither -upgrade-image nor -upgrade-src-dir configured")

// errSelfUpgradeDisabled is reported by every mutating or target-fetching
// upgrade route when Config.DisableSelfUpgrade is true (-disable-self-upgrade).
var errSelfUpgradeDisabled = apierror.Public(
	"in-VM self-upgrade is disabled on this deployment (-disable-self-upgrade); upgrades are managed externally")

func sanitizeManagementURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return u.String()
}

// upgradeNotices is upgrade.Notices with each DocsURL put through
// sanitizeManagementURL, since the pane renders it as a link: a notice
// whose docs link is not an http(s) URL keeps its message and loses the
// link, rather than handing the browser a javascript: href.
func upgradeNotices() []upgrade.Notice {
	notices := upgrade.Notices()
	for i := range notices {
		notices[i].DocsURL = sanitizeManagementURL(notices[i].DocsURL)
	}
	return notices
}

func (s *Server) isSelfUpgradeDisabled() bool {
	return s.tasks.Config.DisableSelfUpgrade
}

func (s *Server) upgradeUnavailableErr() error {
	if s.isSelfUpgradeDisabled() {
		return errSelfUpgradeDisabled
	}
	return errUpgradeUnavailable
}

// upgradeStatusResponse is both GET /api/upgrade's whole body and what
// POST /api/upgrade returns once it has kicked an upgrade off. Enabled
// is false, with every other field its zero value, when this
// deployment's UI has no upgrader configured -- the frontend uses that
// to hide the pane entirely, the same convention secretsResponse's own
// Enabled already establishes.
type upgradeStatusResponse struct {
	Enabled bool `json:"enabled"`
	// Mode is which pipeline this deployment's button actually runs --
	// upgrade.ModeImage or upgrade.ModeSource -- so the pane can
	// describe the one in front of the operator rather than the one this
	// feature happened to be written for first (grain/task-39). Empty
	// when Enabled is false: there is no pipeline to name.
	Mode string `json:"mode"`
	// Running is what this deployment is on *before* anyone presses the
	// button (upgrade.Running, grain/task-50): the image its service
	// starts, or the commit its installed binary was built from. Absent
	// when there is nothing to say -- no upgrader, or a deployment whose
	// own record of that cannot be read -- since the pane's job when it
	// does not know is to show one fewer line, not to refuse to start an
	// upgrade.
	Running *upgradeRunningResponse `json:"running,omitempty"`
	// Channel is the channel this deployment follows, read off what it
	// runs (upgrade.ChannelForRef): latest for a deployment installed on
	// the last promoted release, candidate, main, or the branch whose tag
	// it runs. It is what the pane offers by default and what an upgrade
	// request that names no branch goes to (grain/task-3689). Empty when
	// the deployment cannot say -- one pinned to a sha- build, or the
	// source pipeline.
	Channel   string `json:"channel,omitempty"`
	Branch    string `json:"branch"`
	Phase     string `json:"phase"`
	Detail    string `json:"detail"`
	Unchanged bool   `json:"unchanged,omitempty"`
	// Steps is the upgrade's own plan and how far through it is
	// (pkg/upgrade's Status.Steps): what the pane draws its progress bar
	// and its running commentary from, rather than saying nothing but
	// "running" for the twenty minutes a cold build takes. Absent on a
	// status file an older grain wrote, which is why the pane treats it
	// as optional.
	Steps               []upgradeStepResponse `json:"steps,omitempty"`
	StartedAt           *time.Time            `json:"startedAt,omitempty"`
	FinishedAt          *time.Time            `json:"finishedAt,omitempty"`
	GraceUntil          *time.Time            `json:"graceUntil,omitempty"`
	GraceWindowSeconds  int                   `json:"graceWindowSeconds,omitempty"`
	Cancellable         bool                  `json:"cancellable,omitempty"`
	CancelBlockedReason string                `json:"cancelBlockedReason,omitempty"`
	CancelledBy         string                `json:"cancelledBy,omitempty"`
	CancelReason        string                `json:"cancelReason,omitempty"`
	PreviousImage       string                `json:"previousImage,omitempty"`
	PreviousImageLabel  string                `json:"previousImageLabel,omitempty"`
	PreviousRelease     string                `json:"previousRelease,omitempty"`
	PreviousCommit      string                `json:"previousCommit,omitempty"`
	// InFlightRuns is how many runs are going right now, so the pane can
	// say what pressing the button costs before it is pressed
	// (grain/task-213). An upgrade ends in a restart, and a restart
	// discards every run under way: their agents' work is lost and their
	// tasks are queued again. Nothing here prevents that -- it is what
	// upgrading means -- but an operator deciding whether to do it now
	// or in ten minutes is entitled to know the price.
	//
	// Zero on a deployment whose store cannot be counted, which reads as
	// "nothing to warn about": one line fewer on the pane, on the same
	// reasoning as Running above, rather than a refusal to upgrade.
	InFlightRuns int `json:"inFlightRuns"`
	// AtRisk splits InFlightRuns by what the restart would do to each
	// (task 1389): the runs on a substrate that does not survive it --
	// a CLI that is a child of the daemon (agent.LocalHost), or a run
	// still setting up -- by framework, and how many are in a run host
	// or detached in grain hostd and would be adopted. Absent when
	// nothing is running or the store cannot say.
	AtRisk *upgradeAtRiskResponse `json:"atRisk,omitempty"`
	// LastRestart is what the last restart did to the runs in flight
	// across it: the same report the post-upgrade check is told
	// (orchestrator.UpgradeAdoption), adopted and lost, each lost run with
	// its framework and why. Absent when there is nothing to report, or
	// on a UI with no daemon behind it (Config.LastRestart).
	LastRestart *RestartAdoption `json:"lastRestart,omitempty"`
	// UpgradeInterrupts is every agent CLI whose runs, where they are set
	// to run now, an upgrade would interrupt rather than carry across the
	// restart, each with the daemon's sentence saying so and why
	// (daemon.CLIRunResolution.UpgradeInterrupts) -- the standing fact
	// behind AtRisk, said whether or not a run is in flight. Absent on a
	// UI with no daemon behind it (Config.CLIRuns), and where every CLI
	// survives.
	UpgradeInterrupts []CLIUpgradeNote `json:"upgradeInterrupts,omitempty"`
	// Scheduled is the upgrade this deployment is holding its queue and
	// waiting to apply (model.ScheduledUpgrade, grain/task-156), absent
	// when none is. It is a different thing from every other field here,
	// which describe an upgrade that has already started: this one has
	// not, and what the pane shows for it is a cancel button and what is
	// still being waited for.
	Scheduled *upgradeScheduledResponse `json:"scheduled,omitempty"`
	// Database is the active database status and configuration of this deployment.
	Database *databaseStatusResponse `json:"database,omitempty"`
	// ImageRepo, DefaultImageRepo, DefaultImageRef and ImageRepos say,
	// on an image deployment, what the Upgrade button would pull before
	// anybody presses it: the repository the next upgrade pulls from, the
	// one this deployment's install track defaults to, that repository's
	// reference for the channel the deployment follows, and the
	// repositories an operator may choose instead (startUpgradeRequest's
	// Image). All empty on any other pipeline.
	ImageRepo        string   `json:"imageRepo,omitempty"`
	DefaultImageRepo string   `json:"defaultImageRepo,omitempty"`
	DefaultImageRef  string   `json:"defaultImageRef,omitempty"`
	ImageRepos       []string `json:"imageRepos,omitempty"`
	// ImageRepoWarning is upgrade.Upgrader.RepositoryWarning: ImageRepo
	// names no registry host, so the Upgrade button would be refused,
	// and what to set instead. Shown before anybody presses it.
	ImageRepoWarning string `json:"imageRepoWarning,omitempty"`
	// Edition is this deployment's edition as the pane names it ("oss",
	// or its install track's name). OtherEditionImageRepos are the
	// repositories it could pull from that carry another edition's
	// builds -- never in ImageRepos, and offered only behind the pane's
	// explicit "switch edition" override -- and EditionSwitchLosses is
	// what that override's confirmation says switching gives up
	// (pkg/upgrade/edition.go, grain/task-4153).
	Edition                string                      `json:"edition,omitempty"`
	OtherEditionImageRepos []upgrade.EditionRepository `json:"otherEditionImageRepos,omitempty"`
	EditionSwitchLosses    []string                    `json:"editionSwitchLosses,omitempty"`
	// EditionSwitch is the last upgrade's record of an explicit edition
	// switch (upgrade.Status.EditionSwitch), nil for an ordinary one.
	EditionSwitch *upgrade.EditionSwitch `json:"editionSwitch,omitempty"`
	DiscardedRC   *upgrade.DiscardedRC   `json:"discardedRc,omitempty"`
	// RetractedRelease is set when the release this deployment runs has
	// been retracted since it upgraded onto it: the pane tells the
	// operator to move.
	RetractedRelease *upgrade.RetractedRelease `json:"retractedRelease,omitempty"`
	// SelfUpgradeDisabled is true when -disable-self-upgrade is set, so the
	// UI hides in-VM self-upgrade actions in favour of external lifecycle
	// management (optionally linking to ManagementURL).
	SelfUpgradeDisabled bool   `json:"selfUpgradeDisabled,omitempty"`
	ManagementURL       string `json:"managementUrl,omitempty"`
	// Notices are what the edition has to tell the operator about how
	// this deployment upgrades (upgrade.RegisterNotice), each a warning
	// on the pane with the command that fixes it. Reported whether or not
	// an upgrader is configured: a notice is often about why one is not
	// configured the way it should be. Absent when none is registered.
	Notices []upgrade.Notice `json:"notices,omitempty"`
}

// upgradeScheduledResponse is the pending schedule and how close it is
// to firing.
//
// The two counts are what a scheduled upgrade waits on -- runs an agent
// is actually working in, and runs of grain's own CI -- rather than the
// InFlightRuns above, which is every live run including the console
// sessions that are live only because somebody has a session open.
// Those two answer different questions and one would be wrong for the
// other: InFlightRuns is "what pressing Upgrade right now would throw
// away", and these are "what this deployment is still waiting for".
//
// ByKind and ByID are who asked for it (model.ScheduledUpgrade.By), in
// the same two-field spelling PipelinePauseStatus uses. Both, rather
// than the id alone, because the answer is no longer always this
// deployment's operator: a task holding the self-upgrade capability
// schedules one as {agent, <task id>}, and the kind is what lets the
// pane say "task 353" rather than printing an id beside a word nobody
// asked for (grain/task-353).
type upgradeScheduledResponse struct {
	Branch      string    `json:"branch"`
	RequestedAt time.Time `json:"requestedAt"`
	ByKind      string    `json:"byKind,omitempty"`
	ByID        string    `json:"byId,omitempty"`
	BusyRuns    int       `json:"busyRuns"`
	BusyCIRuns  int       `json:"busyCiRuns"`
}

// upgradeRunningResponse is upgradeStatusResponse's "running" object:
// where this deployment already is, in whichever of the two forms its
// pipeline records (upgrade.Running's own doc comment for why that is
// read off disk rather than from this process).
//
// One object with both halves optional rather than a mode-shaped union:
// the pane shows whichever it is given, and an image deployment
// answering with a ref and no commit needs no separate shape from a
// source one answering with a commit and no ref.
type upgradeRunningResponse struct {
	// Image is the fully qualified reference the deployment's service
	// runs -- "docker.io/grainai/grain:main". Image path only.
	Image string `json:"image,omitempty"`
	// Release, Commit, CommittedAt and Modified are the installed binary's build
	// stamp on the source path, in the same shape and for the same
	// reasons as versionResponse's own fields (version.go): the release
	// name, the full hash, the commit's own time, and whether the build
	// ran against uncommitted changes.
	Release     string     `json:"release,omitempty"`
	Edition     string     `json:"edition,omitempty"`
	Commit      string     `json:"commit,omitempty"`
	CommittedAt *time.Time `json:"committedAt,omitempty"`
	Modified    bool       `json:"modified,omitempty"`
}

// upgradeRunningResponseFrom converts what the upgrader could determine,
// reporting nil when that is nothing at all -- an image deployment whose
// ref file is unreadable comes back from Running with an error, and a
// binary with no VCS stamp comes back with an empty Info and none, and
// both mean the same thing to the pane.
func upgradeRunningResponseFrom(running upgrade.Running) *upgradeRunningResponse {
	resp := &upgradeRunningResponse{
		Image:    running.Ref,
		Release:  running.Build.Release,
		Edition:  running.Build.Edition,
		Commit:   running.Build.Revision,
		Modified: running.Build.Modified,
	}
	if !running.Build.Time.IsZero() {
		t := running.Build.Time
		resp.CommittedAt = &t
	}
	if resp.Image == "" && resp.Commit == "" && resp.Release == "" {
		return nil
	}
	return resp
}

// upgradeStepResponse is one step of that plan: its name, where it
// stands ("pending", "running", "done", "failed", "skipped") and
// whatever it last had to say -- the line its command most recently
// printed while it runs, what it achieved once it is done, why it was
// skipped, or what failed it.
type upgradeStepResponse struct {
	Name       string     `json:"name"`
	State      string     `json:"state"`
	Detail     string     `json:"detail,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

func upgradeStatusResponseFrom(up Upgrader, status upgrade.Status, inFlightRuns int,
	scheduled *upgradeScheduledResponse) upgradeStatusResponse {
	var steps []upgradeStepResponse
	for _, step := range status.Steps {
		steps = append(steps, upgradeStepResponse{
			Name:       step.Name,
			State:      string(step.State),
			Detail:     step.Detail,
			StartedAt:  step.StartedAt,
			FinishedAt: step.FinishedAt,
		})
	}
	// A deployment that cannot say where it is still upgrades: the error
	// is logged for whoever has to fix the ref file or the install path
	// behind it, and the pane simply shows one line fewer.
	var running *upgradeRunningResponse
	channel := ""
	if r, err := up.Running(); err != nil {
		log.Printf("ui: reporting what this deployment is running: %v", err)
	} else {
		running = upgradeRunningResponseFrom(r)
		channel = upgrade.ChannelForRef(r.Ref)
	}
	resp := upgradeStatusResponse{
		Enabled:            true,
		Mode:               string(up.Mode()),
		Running:            running,
		Channel:            channel,
		Branch:             status.Branch,
		Phase:              string(status.Phase),
		Detail:             status.Detail,
		Unchanged:          status.Unchanged,
		Steps:              steps,
		StartedAt:          status.StartedAt,
		FinishedAt:         status.FinishedAt,
		GraceUntil:         status.GraceUntil,
		CancelledBy:        status.CancelledBy,
		CancelReason:       status.CancelReason,
		PreviousImage:      status.PreviousImage,
		PreviousImageLabel: upgrade.ShortImageRef(status.PreviousImage, status.Branch),
		PreviousRelease:    status.PreviousRelease,
		PreviousCommit:     status.PreviousCommit,
		InFlightRuns:       inFlightRuns,
		Scheduled:          scheduled,
		// The record that this upgrade crossed editions on purpose.
		EditionSwitch: status.EditionSwitch,
	}
	if cu, ok := up.(cancellableUpgrader); ok {
		resp.Cancellable, resp.CancelBlockedReason = cu.CancelState(status)
		if gw := cu.GraceWindow(); gw > 0 {
			resp.GraceWindowSeconds = int(gw / time.Second)
		}
	}
	if iu, ok := up.(imageUpgrader); ok {
		if target, ok := iu.ImageTarget(); ok {
			resp.ImageRepo = target.Repository
			resp.DefaultImageRepo = target.DefaultRepository
			resp.ImageRepos = target.Repositories
			resp.ImageRepoWarning = target.RepositoryWarning
			resp.Edition = target.Edition
			resp.OtherEditionImageRepos = target.OtherEditionRepositories
			resp.EditionSwitchLosses = target.EditionSwitchLosses
			if channel != "" && target.Repository != "" {
				resp.DefaultImageRef = target.Repository + ":" + upgrade.TagForBranch(channel)
			}
		}
	}
	if dr, ok := up.(discardedRCReporter); ok {
		resp.DiscardedRC = dr.CachedDiscardedRC(context.Background())
	}
	if rr, ok := up.(retractedReleaseReporter); ok {
		resp.RetractedRelease = rr.CachedRetractedRelease(context.Background())
	}
	return resp
}

type discardedRCReporter interface {
	CachedDiscardedRC(ctx context.Context) *upgrade.DiscardedRC
}

type retractedReleaseReporter interface {
	CachedRetractedRelease(ctx context.Context) *upgrade.RetractedRelease
}

// imageUpgrader is the optional half of Upgrader that lets an operator
// choose the image an upgrade pulls (upgrade.Upgrader.StartImage) and
// see where it would pull from before choosing. Optional for the same
// reason upgradeTargeter is: only the real upgrader has an answer.
type imageUpgrader interface {
	StartImageWith(branch, image string, opts upgrade.ImageOptions) error
	ImageTarget() (upgrade.ImageTarget, bool)
}

// rollbackUpgrader is the optional half of Upgrader that starts the
// one-click rollback (upgrade.Upgrader.StartRollback).
type rollbackUpgrader interface {
	StartRollback() error
}

type cancellableUpgrader interface {
	Cancel(by, reason string) (bool, error)
	CancelState(status upgrade.Status) (cancellable bool, blockedReason string)
	GraceWindow() time.Duration
}

// inFlightRuns is how many runs this deployment has going, for the
// warning the pane shows above the Upgrade button.
//
// Zero on an error, logged rather than returned: a count that cannot be
// read is a sentence the pane leaves out, and it is no reason to fail
// the request that would have started the upgrade.
func (s *Server) inFlightRuns(r *http.Request) int {
	n, err := s.tasks.Store.LiveRunCount(r.Context())
	if err != nil {
		log.Printf("ui: counting the runs an upgrade would discard: %v", err)
		return 0
	}
	return n
}

// upgradeAtRiskResponse is upgradeStatusResponse.AtRisk.
type upgradeAtRiskResponse struct {
	// Lost is how many live runs the restart would lose, and
	// LostByFramework the same runs by framework (frameworkCounts).
	Lost            int              `json:"lost"`
	LostByFramework []FrameworkCount `json:"lostByFramework,omitempty"`
	// Survive is how many are in a run host or detached in grain hostd,
	// for the next daemon to adopt.
	Survive int `json:"survive"`
}

// RestartAdoption is upgradeStatusResponse.LastRestart: an
// orchestrator.AdoptionReport, as the daemon converts it
// (Config.LastRestart), since this package does not import the
// orchestrator. LostByFramework is filled in here.
type RestartAdoption struct {
	// At is when the adoption pass ran: the daemon's startup.
	At              time.Time        `json:"at"`
	Adopted         []RestartRun     `json:"adopted,omitempty"`
	Lost            []RestartRun     `json:"lost,omitempty"`
	LostByFramework []FrameworkCount `json:"lostByFramework,omitempty"`
	// Freed is how many run hosts no live run claimed were destroyed.
	Freed int `json:"freed,omitempty"`
}

// RestartRun is one run in a RestartAdoption
// (orchestrator.AdoptedRunNote).
type RestartRun struct {
	Run       string `json:"run"`
	Task      string `json:"task"`
	Framework string `json:"framework,omitempty"`
	// Setup is a lost run that ended while it was still setting up.
	Setup  bool   `json:"setup,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Session is the console session the run's task is ("director" for
	// the Director), empty for ordinary work
	// (orchestrator.AdoptedRunNote.Session).
	Session string `json:"session,omitempty"`
}

// FrameworkCount is how many runs one framework had, as the pane lists
// lost runs: "setup" for runs with none yet and "unknown" for a run
// whose row names none (orchestrator.AdoptionReport.LostByFramework).
type FrameworkCount struct {
	Framework string `json:"framework"`
	Count     int    `json:"count"`
}

// frameworkLabel is the name a run is counted under in frameworkCounts.
func frameworkLabel(framework string, setup bool) string {
	switch {
	case framework != "":
		return framework
	case setup:
		return "setup"
	}
	return "unknown"
}

// frameworkCounts counts labels, most first and then by name.
func frameworkCounts(labels []string) []FrameworkCount {
	counts := map[string]int{}
	for _, l := range labels {
		counts[l]++
	}
	out := make([]FrameworkCount, 0, len(counts))
	for name, n := range counts {
		out = append(out, FrameworkCount{Framework: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Framework < out[j].Framework
	})
	return out
}

// atRisk is upgradeStatusResponse.AtRisk, read from the store
// (model.Store.LiveRunSubstrates); nil when nothing is running, or on
// an error, logged for inFlightRuns' reason.
func (s *Server) atRisk(r *http.Request) *upgradeAtRiskResponse {
	live, err := s.tasks.Store.LiveRunSubstrates(r.Context())
	if err != nil {
		log.Printf("ui: reading which runs an upgrade would lose: %v", err)
		return nil
	}
	if len(live) == 0 {
		return nil
	}
	resp := &upgradeAtRiskResponse{}
	var lost []string
	for _, run := range live {
		if run.Detached {
			resp.Survive++
			continue
		}
		// A live run with no framework yet is in setup: its CLI has not
		// been chosen, let alone started.
		lost = append(lost, frameworkLabel(run.Framework, run.Framework == ""))
	}
	resp.Lost = len(lost)
	if len(lost) > 0 {
		resp.LostByFramework = frameworkCounts(lost)
	}
	return resp
}

// lastRestart is upgradeStatusResponse.LastRestart, from
// Config.LastRestart, with its lost runs counted by framework.
func (s *Server) lastRestart(r *http.Request) *RestartAdoption {
	if s.tasks.Config.LastRestart == nil {
		return nil
	}
	report := s.tasks.Config.LastRestart(r.Context())
	if report == nil {
		return nil
	}
	out := *report
	if len(out.Lost) > 0 {
		labels := make([]string, len(out.Lost))
		for i, l := range out.Lost {
			labels[i] = frameworkLabel(l.Framework, l.Setup)
		}
		out.LostByFramework = frameworkCounts(labels)
	}
	return &out
}

// CLIUpgradeNote is one entry of upgradeStatusResponse.UpgradeInterrupts.
type CLIUpgradeNote struct {
	Framework string `json:"framework"`
	Note      string `json:"note"`
}

// upgradeInterrupts is upgradeStatusResponse.UpgradeInterrupts, read
// from the daemon's own resolution of where each CLI runs. The
// resolution is the daemon's type, which this package does not import,
// so the fields it needs are read back out of its JSON.
func (s *Server) upgradeInterrupts(r *http.Request) []CLIUpgradeNote {
	if s.tasks.Config.CLIRuns == nil {
		return nil
	}
	resolved, err := s.tasks.resolvedCLIRuns(r.Context())
	if err != nil {
		log.Printf("ui: reading which agent CLIs an upgrade interrupts: %v", err)
		return nil
	}
	return cliUpgradeNotes(resolved)
}

// cliUpgradeNotes is the CLIs in resolved (daemon.ResolveCLIRuns'
// answer) whose runs an upgrade interrupts.
func cliUpgradeNotes(resolved any) []CLIUpgradeNote {
	data, err := json.Marshal(resolved)
	if err != nil {
		return nil
	}
	var runs []struct {
		Framework         string `json:"framework"`
		UpgradeInterrupts string `json:"upgradeInterrupts"`
	}
	if json.Unmarshal(data, &runs) != nil {
		return nil
	}
	var out []CLIUpgradeNote
	for _, r := range runs {
		if r.UpgradeInterrupts != "" {
			out = append(out, CLIUpgradeNote{Framework: r.Framework, Note: r.UpgradeInterrupts})
		}
	}
	return out
}

// upgradeStatusResponse is upgradeStatusResponseFrom with everything this
// server reads from its own store and daemon: the runs in flight and
// what the restart would do to them, the pending schedule, and what the
// last restart did.
func (s *Server) upgradeStatusResponse(r *http.Request, status upgrade.Status) upgradeStatusResponse {
	resp := upgradeStatusResponseFrom(
		s.tasks.Config.Upgrader, status, s.inFlightRuns(r), s.scheduledUpgrade(r))
	resp.AtRisk = s.atRisk(r)
	resp.LastRestart = s.lastRestart(r)
	resp.UpgradeInterrupts = s.upgradeInterrupts(r)
	return resp
}

// scheduledUpgrade is the pending schedule, for whichever route is
// about to report one, or nil when nothing is scheduled.
//
// Errors are logged and read as "nothing scheduled", the same way
// inFlightRuns above reads an uncountable store as nothing to warn
// about: a pane that cannot say what it is waiting for should show one
// section fewer, not refuse to serve the upgrade status that is the rest
// of this response. The reconciler is the one that actually acts on the
// row, and it reads it for itself.
func (s *Server) scheduledUpgrade(r *http.Request) *upgradeScheduledResponse {
	ctx := r.Context()
	scheduled, err := s.tasks.Store.GetScheduledUpgrade(ctx)
	if err != nil {
		log.Printf("ui: reading the scheduled upgrade: %v", err)
		return nil
	}
	if scheduled == nil {
		return nil
	}
	resp := &upgradeScheduledResponse{
		Branch:      scheduled.Branch,
		RequestedAt: scheduled.RequestedAt,
		ByKind:      string(scheduled.By.Kind),
		ByID:        scheduled.By.ID,
	}
	if runs, err := s.tasks.Store.BusyRunCount(ctx); err != nil {
		log.Printf("ui: counting the runs a scheduled upgrade is waiting for: %v", err)
	} else {
		resp.BusyRuns = runs
	}
	if ciRuns, err := s.tasks.Store.LiveCIRunCount(ctx); err != nil {
		log.Printf("ui: counting the CI runs a scheduled upgrade is waiting for: %v", err)
	} else {
		resp.BusyCIRuns = ciRuns
	}
	return resp
}

// handleGetUpgradeStatus answers GET /api/upgrade. What this daemon
// is running, how it upgrades and what its store is are a system
// administrator's to read, as starting an upgrade is theirs to do.
func (s *Server) handleGetUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	if s.isSelfUpgradeDisabled() {
		var running *upgradeRunningResponse
		if s.tasks.Config.Upgrader != nil {
			if cur, err := s.tasks.Config.Upgrader.Running(); err == nil {
				running = upgradeRunningResponseFrom(cur)
			}
		}
		if running == nil {
			info := version.Get()
			if info.Unversioned {
				info.Release = ""
			}
			running = upgradeRunningResponseFrom(upgrade.Running{Build: info})
		}
		writeJSON(w, http.StatusOK, upgradeStatusResponse{
			Enabled:             false,
			SelfUpgradeDisabled: true,
			ManagementURL:       sanitizeManagementURL(s.tasks.Config.ManagementURL),
			Running:             running,
			Database:            s.databaseStatus(),
			Notices:             upgradeNotices(),
		})
		return
	}
	if s.tasks.Config.Upgrader == nil {
		writeJSON(w, http.StatusOK, upgradeStatusResponse{
			Enabled:  false,
			Database: s.databaseStatus(),
			Notices:  upgradeNotices(),
		})
		return
	}
	status, err := s.tasks.Config.Upgrader.Status()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	resp := s.upgradeStatusResponse(r, status)
	resp.Database = s.databaseStatus()
	resp.Notices = upgradeNotices()
	writeJSON(w, http.StatusOK, resp)
}

// upgradeTargeter is the optional half of Upgrader that lists what the
// pane's selector offers (upgrade.Upgrader.Targets). Optional rather than
// on Upgrader itself because nothing about starting an upgrade needs it,
// and an upgrader that cannot list targets still upgrades onto a typed
// branch.
type upgradeTargeter interface {
	Targets(ctx context.Context) upgrade.Targets
}

// handleGetUpgradeTargets answers GET /api/upgrade/targets: the standard
// branches and released versions the pane offers in its selector, each
// marked with whether this deployment may upgrade to it, and the
// -upgrade-allowed-branches patterns a hand-typed branch has to match.
// A route of its own rather than part of GET /api/upgrade because
// listing versions asks the image registry, and that status is polled
// every few seconds while an upgrade runs.
type upgradeTargetsResponse struct {
	upgrade.Targets
	Total      int    `json:"total,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	NextCursor string `json:"nextCursor,omitempty"`
}

func (s *Server) handleGetUpgradeTargets(w http.ResponseWriter, r *http.Request) {
	list, err := parseListQuery(r, listPageMedium)
	if err != nil {
		writeClientError(w, err)
		return
	}
	if s.isSelfUpgradeDisabled() || s.tasks.Config.Upgrader == nil {
		writeError(w, http.StatusNotFound, s.upgradeUnavailableErr())
		return
	}
	var targets upgrade.Targets
	if targeter, ok := s.tasks.Config.Upgrader.(upgradeTargeter); ok {
		targets = targeter.Targets(r.Context())
	} else {
		targets = upgrade.Targets{
			Targets: []upgrade.Target{},
			Default: deploymentChannel(s.tasks.Config.Upgrader),
		}
	}
	if targets.Targets == nil {
		targets.Targets = []upgrade.Target{}
	}
	resp := upgradeTargetsResponse{Targets: targets}
	if list.Paged {
		matched := filterList(targets.Targets, func(t upgrade.Target) bool {
			return list.matches(t.Name, string(t.Kind))
		})
		page := pageOf(matched, list)
		resp.Targets.Targets = page.Items
		resp.Total = page.Total
		resp.Truncated = page.Truncated
		resp.NextCursor = page.NextCursor
	}
	writeJSON(w, http.StatusOK, resp)
}

type startUpgradeRequest struct {
	Branch string `json:"branch"`
	// Image, when set, is the image to pull instead of the branch's tag
	// in the configured repository: a repository the branch's tag is
	// pulled from ("docker.io/grainai/grain"), or a full
	// `repo:tag` or `repo@sha256:...` reference. It must be in a
	// repository this deployment may upgrade onto
	// (installtrack.ValidateTargetRepository); a tag in it names the
	// branch. Image deployments only, and only for an upgrade started
	// now -- a schedule records a branch and nothing else.
	Image string `json:"image,omitempty"`
	// SwitchEdition is the explicit "switch edition" override: it lets
	// Image be another edition's build (a Google-track deployment pulling
	// the open-source image, or the reverse), which is otherwise refused
	// (upgrade.ErrCrossEdition). It needs Image, cannot be scheduled, and
	// is recorded on the upgrade's status as who switched from what to
	// what (upgrade.Status.EditionSwitch).
	SwitchEdition bool `json:"switchEdition,omitempty"`
	// Rollback is the one-click rollback: an upgrade back onto the build
	// the last upgrade replaced (upgrade.Upgrader.StartRollback). It
	// names no target -- the daemon reads PreviousImage or PreviousCommit
	// from its own status -- so Branch and Image must be left empty, and
	// it cannot be scheduled. It is the only request that may go back to
	// a previous commit outside -upgrade-allowed-branches, and only to
	// that one commit, checked out by object id.
	Rollback bool `json:"rollback,omitempty"`
	// AfterTasksFinish asks for the upgrade to be scheduled rather than
	// started: the queue shuts now, the work already under way is left to
	// finish, and the cut-over happens once this deployment is idle
	// (model.ScheduledUpgrade, grain/task-156).
	//
	// A flag on this same request rather than a route of its own, because
	// it is the same act with the same argument -- "upgrade onto this
	// branch" -- and the only thing that differs is when. DELETE
	// /api/upgrade/scheduled is how one is called off.
	AfterTasksFinish bool `json:"afterTasksFinish"`
	// RequestedBy and RequestedByKind are who is asking, for a caller
	// that is not the operator at the console: the self-upgrade
	// capability's upgrade_deployment sends {agent, <task id>}, so that
	// the schedule records the task that wanted the upgrade rather than
	// an operator who did nothing (grain/task-353). Omitted -- which is
	// what the pane's own button sends -- records this deployment's
	// actor, as it always did.
	//
	// They matter only with AfterTasksFinish, since an immediate upgrade
	// writes no row for anyone to be recorded in; the journal line the
	// capability writes before it calls is what attributes that one.
	//
	// This is a claim, not a proof: the API is unauthenticated, so a
	// caller that says it is a task is believed, exactly as
	// memoryRequest's author is. It buys attribution for the honest
	// caller and nothing more -- which is the whole of what is wanted
	// here, since anything that can reach this port can already schedule
	// the upgrade outright and needs no borrowed name to do it.
	RequestedBy     string `json:"requestedBy,omitempty"`
	RequestedByKind string `json:"requestedByKind,omitempty"`
	// StopExperiment stops an active QA experiment (and promotes stranded
	// staging branches fail-open) before starting or scheduling an upgrade.
	StopExperiment bool `json:"stopExperiment,omitempty"`
}

// deploymentChannel is the channel up's deployment follows, or "" when
// it cannot say (upgrade.ChannelForRef; Upgrader.Channel is the same
// answer for the real one).
func deploymentChannel(up Upgrader) string {
	r, err := up.Running()
	if err != nil {
		return ""
	}
	return upgrade.ChannelForRef(r.Ref)
}

// handleStartUpgrade kicks off an upgrade and returns at once -- it does
// not wait for the checkout, build, install or restart it triggers to
// finish (the restart, on a real deployment, ends this very process
// before that could ever happen). GET /api/upgrade is how a caller
// watches it land.
//
// With afterTasksFinish it starts nothing at all: it writes the schedule
// down and leaves it to the reconcile loop, which holds the queue and
// applies it once nothing is running (orchestrator's
// reconcileScheduledUpgrade). The same GET reports it either way.
func (s *Server) handleStartUpgrade(w http.ResponseWriter, r *http.Request) {
	if s.isSelfUpgradeDisabled() || s.tasks.Config.Upgrader == nil {
		writeError(w, http.StatusNotFound, s.upgradeUnavailableErr())
		return
	}
	var req startUpgradeRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.Image = strings.TrimSpace(req.Image)
	if req.Rollback {
		s.startRollback(w, r, req)
		return
	}
	if s.tasks.Store != nil {
		if runningExp, rErr := s.tasks.Store.GetRunningQAExperiment(r.Context()); rErr == nil && runningExp != nil {
			if !req.StopExperiment {
				writeError(w, http.StatusConflict, fmt.Errorf("cannot upgrade while QA experiment %s is running (pass stopExperiment: true or stop the experiment first)", runningExp.ID))
				return
			}
			now := s.tasks.now()
			actor := s.tasks.requestActor(r.Context())
			if _, sErr := s.tasks.Store.UpdateQAExperiment(r.Context(), runningExp.ID, 0, func(e *model.QAExperiment) error {
				if e.State == model.QAExpStateRunning {
					e.State = model.QAExpStateStopped
					e.StoppedAt = &now
					e.Decision = &model.QAExperimentDecision{
						Outcome:   "invalid",
						Reason:    "stopped for deployment upgrade",
						DecidedBy: actor.Actor.ID,
						DecidedAt: now.Format(time.RFC3339),
					}
				}
				return nil
			}); sErr != nil {
				writeError(w, http.StatusInternalServerError, sErr)
				return
			}
			_, _ = s.tasks.Store.RecordConfigAudit(r.Context(), model.ConfigAuditEntry{
				At:         now,
				By:         actor.Actor,
				OnBehalfOf: actor.OnBehalfOf,
				Surface:    "qa-experiment:" + runningExp.ID,
				Summary:    fmt.Sprintf("stopped QA experiment %s before deployment upgrade", runningExp.ID),
			})
			if hooks := resolve.GetRuntimeHooks(); hooks != nil && hooks.PromoteExperimentStages != nil {
				hooks.PromoteExperimentStages(r.Context(), runningExp.ID, "experiment stopped for deployment upgrade")
			}
		}
	}
	if strings.TrimSpace(req.Branch) == "" {
		// No branch means "a newer build of what this deployment already
		// follows": the Upgrade button, a scheduled upgrade and an
		// agent's all land here, so they all follow the channel.
		req.Branch = deploymentChannel(s.tasks.Config.Upgrader)
	}
	if req.Branch == "" {
		writeError(w, http.StatusBadRequest, errors.New("branch is required: this deployment "+
			"follows no channel an upgrade could default to"))
		return
	}
	iu, canChooseImage := s.tasks.Config.Upgrader.(imageUpgrader)
	switch {
	case req.Image != "" && req.AfterTasksFinish:
		writeError(w, http.StatusBadRequest, errors.New("an upgrade onto an explicit image cannot be "+
			"scheduled for after tasks finish: start it now, or schedule the branch alone"))
		return
	case req.Image != "" && !canChooseImage:
		writeError(w, http.StatusBadRequest, errors.New("this deployment cannot upgrade onto an explicit image"))
		return
	case req.SwitchEdition && req.Image == "":
		writeError(w, http.StatusBadRequest, errors.New("switching edition needs the image to switch to: "+
			"name another edition's repository or image"))
		return
	case req.SwitchEdition && req.AfterTasksFinish:
		writeError(w, http.StatusBadRequest, errors.New("an edition switch cannot be scheduled for after tasks finish: start it now"))
		return
	case req.AfterTasksFinish:
		if err := s.scheduleUpgrade(r, req); err != nil {
			writeClientError(w, err)
			return
		}
	case req.Image != "":
		opts := upgrade.ImageOptions{SwitchEdition: req.SwitchEdition}
		if req.SwitchEdition {
			// Who switched is part of the record: the agent a
			// capability names, or this deployment's own actor for the
			// operator at the console.
			ctx, err := withAgentRequester(r.Context(), "an edition switch's requester", req.RequestedBy, req.RequestedByKind)
			if err != nil {
				writeClientError(w, err)
				return
			}
			by := s.tasks.requestActor(ctx).Actor
			opts.RequestedBy = strings.TrimSpace(string(by.Kind) + " " + by.ID)
		}
		if err := iu.StartImageWith(req.Branch, req.Image, opts); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	default:
		if err := s.tasks.Config.Upgrader.Start(req.Branch); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	status, err := s.tasks.Config.Upgrader.Status()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.upgradeStatusResponse(r, status))
}

// startRollback is handleStartUpgrade for a request with rollback set. The
// target is never the caller's: StartRollback reads it from the status
// the last upgrade recorded, so a request naming a branch or an image as
// well is refused rather than half-honoured.
func (s *Server) startRollback(w http.ResponseWriter, r *http.Request, req startUpgradeRequest) {
	ru, ok := s.tasks.Config.Upgrader.(rollbackUpgrader)
	switch {
	case !ok:
		writeError(w, http.StatusBadRequest, errors.New("this deployment cannot roll back"))
		return
	case strings.TrimSpace(req.Branch) != "" || req.Image != "" || req.SwitchEdition:
		writeError(w, http.StatusBadRequest, errors.New("a rollback goes back to the build the last upgrade replaced: "+
			"leave branch, image and switchEdition unset"))
		return
	case req.AfterTasksFinish:
		writeError(w, http.StatusBadRequest, errors.New("a rollback cannot be scheduled for after tasks finish: start it now"))
		return
	}
	if err := ru.StartRollback(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	status, err := s.tasks.Config.Upgrader.Status()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.upgradeStatusResponse(r, status))
}

// scheduleUpgrade writes the schedule down, having first made the branch
// survive the same check Start would have made of it
// (Upgrader.ValidateBranch). Validating here is the point of doing it at
// all: a branch name the upgrader would refuse must be refused while the
// person who typed it is still looking at the form, not silently hours
// later when the deployment finally goes idle.
//
// Nothing here pauses anything. The hold is the reconciler's
// (orchestrator's reconcileScheduledUpgrade), which takes it on the next
// tick and -- being level-triggered -- re-takes it after a restart,
// which no write from this handler could do.
//
// Who it is recorded as is whoever the request named, and this
// deployment's own actor for a request that named nobody
// (startUpgradeRequest.RequestedBy). The defaulting is what keeps the
// pane's button honest -- a person pressing it is the operator -- and
// the naming is what keeps an agent's schedule honest, which matters
// more: "why is this deployment about to restart" is answered by the
// task that asked, and before grain/task-353 the only record of that was
// a line in the daemon journal.
func (s *Server) scheduleUpgrade(r *http.Request, req startUpgradeRequest) error {
	branch, err := s.tasks.Config.Upgrader.ValidateBranch(req.Branch)
	if err != nil {
		return validationErrorf("%v", err)
	}
	ctx, err := withAgentRequester(
		r.Context(), "a scheduled upgrade's requester", req.RequestedBy, req.RequestedByKind)
	if err != nil {
		return err
	}
	return s.tasks.Store.SetScheduledUpgrade(ctx, model.ScheduledUpgrade{
		Branch:      branch,
		RequestedAt: time.Now().UTC(),
		By:          s.tasks.requestActor(ctx).Actor,
	})
}

// handleCancelScheduledUpgrade answers DELETE /api/upgrade/scheduled:
// the upgrade that was waiting is forgotten, and the queue opens again
// on the next tick when the reconciler finds the hold it took standing
// over nothing.
//
// Cancelling one that is not there is not an error, for the reason
// resuming an unpaused deployment is not: whoever pressed the button
// wanted a deployment with no upgrade pending, and it has none. It says
// which of the two happened.
func (s *Server) handleCancelScheduledUpgrade(w http.ResponseWriter, r *http.Request) {
	if s.isSelfUpgradeDisabled() || s.tasks.Config.Upgrader == nil {
		writeError(w, http.StatusNotFound, s.upgradeUnavailableErr())
		return
	}
	cancelled, err := s.tasks.Store.ClearScheduledUpgrade(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	status, err := s.tasks.Config.Upgrader.Status()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	resp := s.upgradeStatusResponse(r, status)
	writeJSON(w, http.StatusOK, cancelScheduledUpgradeResponse{
		upgradeStatusResponse: resp,
		Cancelled:             cancelled,
	})
}

// cancelScheduledUpgradeResponse is the same reading every other upgrade
// route answers with, plus the one thing only this route can say.
type cancelScheduledUpgradeResponse struct {
	upgradeStatusResponse
	// Cancelled is whether this call is what called the upgrade off, as
	// against finding none scheduled.
	Cancelled bool `json:"cancelled"`
}

// CancelUpgradeRequest is the optional body of DELETE /api/upgrade/current.
type CancelUpgradeRequest struct {
	Reason string `json:"reason,omitempty"`
}

func flattenAuditText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

// handleCancelCurrentUpgrade answers DELETE /api/upgrade/current: stops an
// upgrade that is currently in progress before its point of no return, records
// who cancelled it and why in config_audit, and returns 409 Conflict once
// cut-over or restart has begun.
func (s *Server) handleCancelCurrentUpgrade(w http.ResponseWriter, r *http.Request) {
	if s.isSelfUpgradeDisabled() || s.tasks.Config.Upgrader == nil {
		writeError(w, http.StatusNotFound, s.upgradeUnavailableErr())
		return
	}
	cu, ok := s.tasks.Config.Upgrader.(cancellableUpgrader)
	if !ok {
		writeError(w, http.StatusNotFound, errUpgradeUnavailable)
		return
	}
	var req CancelUpgradeRequest
	if !readOptionalJSON(w, r, &req) {
		return
	}
	reason := flattenAuditText(req.Reason)
	if reason == "" {
		reason = flattenAuditText(r.URL.Query().Get("reason"))
	}

	by := s.tasks.requestActor(r.Context())
	actorLabel := strings.TrimSpace(by.Actor.ID)
	if by.Actor.Kind == model.PrincipalAgent && actorLabel != "" {
		actorLabel = "task " + actorLabel
	}
	if actorLabel == "" {
		actorLabel = model.PrincipalString(by.Actor)
	}

	cancelled, err := cu.Cancel(actorLabel, reason)
	if err != nil {
		if tooLate, isLate := upgrade.IsTooLate(err); isLate {
			writeError(w, http.StatusConflict, apierror.Public(tooLate.Error()))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	status, err := s.tasks.Config.Upgrader.Status()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if cancelled && s.tasks.Store != nil {
		target := status.Branch
		if target == "" {
			target = "deployment"
		}
		summary := fmt.Sprintf("cancelled upgrade to %s", flattenAuditText(target))
		if reason != "" {
			summary += fmt.Sprintf(" (%s)", reason)
		}
		if _, auditErr := s.tasks.Store.RecordConfigAudit(context.WithoutCancel(r.Context()), model.ConfigAuditEntry{
			At:         s.tasks.now(),
			By:         by.Actor,
			OnBehalfOf: by.OnBehalfOf,
			Surface:    "upgrade",
			Summary:    summary,
		}); auditErr != nil {
			log.Printf("ui: recording cancelled upgrade in config_audit: %v", auditErr)
		}
	}
	resp := s.upgradeStatusResponse(r, status)
	writeJSON(w, http.StatusOK, cancelScheduledUpgradeResponse{
		upgradeStatusResponse: resp,
		Cancelled:             cancelled,
	})
}
