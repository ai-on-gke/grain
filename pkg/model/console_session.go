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
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// ConsoleSession is one standing interactive session this deployment
// keeps open -- the director, and whatever else an operator wants a
// permanent chat with (grain/task-415).
//
// # What a standing session is, and what it is not
//
// An ordinary task is filed, dispatched, finished and read. A standing
// session is the other shape: one task that is never finished, whose
// conversation is the whole of it. Somebody opens the chat, says what
// they want, and the agent on the other end answers, files tasks, or
// goes and looks at the deployment -- and when it has said its piece the
// task goes back to waiting rather than completing. That waiting is the
// whole difference (orchestrator.reconcileConsoleSessions), and it is
// why this is a config row rather than a task somebody files by hand: a
// task filed by hand ends, and this one may not.
//
// A session is defined here, in this deployment's settings, and
// materialized as exactly one task carrying ConsoleSessionTag(ID). The
// task holds the conversation; this row holds what the deployment gets
// to decide about it. So there is one director task in a deployment's
// whole history, with every exchange anybody has ever had with it, and
// removing the row does not delete it -- the task stays where it is,
// with its record, and stops being kept alive.
//
// # What is a deployment's to choose, and what is grain's
//
// The same split model.SystemTemplate makes for the memory agent, for
// the same reason. What the session *is* -- the prompt telling the
// director what it is for and what it may do -- is code
// (DirectorSessionBody), reviewed in grain's own repository, so a
// session holding self-repair cannot be quietly rewritten into something
// else by whoever can edit settings. What this deployment chooses is the
// envelope: whether the session exists, what it is called, what it may
// reach (Capabilities, NetworkAccess), and whether the tasks it files
// are queued or wait for a person (TasksApproved).
type ConsoleSession struct {
	// ID names the session, and is the whole of its identity: it is what
	// the task carrying this session's conversation is tagged with
	// (ConsoleSessionTag), so renaming it starts a new conversation and
	// leaves the old one where it was. DirectorSessionID for the one
	// grain ships.
	ID string
	// Title is what the session is called in the UI, and the title of
	// the task behind it.
	Title string
	// TitleSource is who chose Title: the first-line heuristic, the
	// session's own agent through rename_session, or a person. It is
	// what decides whether the session's agent is asked to name the
	// conversation (DirectorSessionBody) and whether rename_session may
	// write at all: an agent names an auto-titled session once, and
	// never overwrites a name an agent or a person already gave it.
	// Empty on every session saved before this field existed -- read it
	// through ResolvedTitleSource, which works out what such a session's
	// title most likely was.
	TitleSource ConsoleSessionTitleSource `json:"titleSource,omitempty"`
	// Team is the team this session belongs to (DefaultTeamID when unset).
	Team string `json:"team,omitempty"`
	// Capabilities are the capability ids the session's task is granted,
	// by name, exactly as Config.DefaultCapabilities names them. This is
	// the field the UI's own console-session pane edits, and it is
	// applied to the task on the next cycle: a capability ticked here is
	// held by the session's next exchange, one unticked is gone from it.
	Capabilities []string
	// NetworkAccess is the session's sandbox's reach, as a task-level
	// override (Task.NetworkAccess): "" for this deployment's own
	// default, NetworkNone for a session that may not reach the internet
	// at all. The director ships NetworkOpen -- see DefaultConsoleSessions.
	NetworkAccess NetworkAccess
	// TasksApproved is whether a task this session proposes is filed
	// approved (queued, dispatchable by the next cycle) or proposed
	// (waiting for a person). False -- approval required -- is the
	// default and the shipped answer: a chat is a fast way to ask for a
	// lot of work, and a director that could queue its own tasks would
	// be one conversation away from spending a day of this deployment's
	// capacity on a misunderstanding. An operator who trusts theirs can
	// turn it on.
	TasksApproved bool
	// Reads is the repos this session is granted read-only access to
	// (Task.Reads). The session's sandbox may clone or fetch from them,
	// but never push commits directly.
	Reads []RepoRef `json:"reads,omitempty"`
	// Writes is the repos this session is granted write access to
	// (Task.Writes). The session's sandbox may clone, fetch, and push
	// commits to them.
	Writes []WriteTarget `json:"writes,omitempty"`
	// GoogleChatSpace is the Google Chat space resource name or URL
	// linked to this session (Task.Links with LinkGoogleChat).
	GoogleChatSpace string `json:"googleChatSpace,omitempty"`
	// Model is the model this session's agent runs on (Task.Model): a
	// model id some connection of the session's team advertises. Empty,
	// the default, means the team's own default model -- what every
	// session saved before this field existed reads back as. Like the
	// rest of the envelope it is carried onto the session's task on the
	// next cycle (orchestrator.syncConsoleSessionTask), so a change here
	// is what the session's next exchange runs on.
	Model string `json:"model,omitempty"`
}

// ConsoleSessionPrincipal is who grain approves a session's own
// proposals as, for a session whose deployment says it may queue its own
// work (ConsoleSession.TasksApproved).
//
// An automation actor on nobody's behalf, the same shape
// AutoSubmitPrincipal has and for the same reason: no person saw the
// task before it was queued, and distinguishing that from an approval
// somebody gave is what keeps "who let this run?" answerable afterwards.
// It lives here rather than in pkg/orchestrator, which writes it, because
// SelfApproved has to recognize it: a task queued this way is a level of
// self-approved work like any other when what grows below it is being
// counted.
var ConsoleSessionPrincipal = Principal{Kind: PrincipalAutomation, ID: "console-session"}

// QueuesProposals reports whether a task this session proposes is filed
// approved -- queued, dispatchable with nobody having approved it -- and
// it is the single answer everything about that permission is written
// from.
//
// Three things ask it, and before grain/task-311 two of them answered it
// for themselves. The gate on the end-of-run replay
// (orchestrator.relayProposedTasks, through
// consoleSessionApprovesProposals) read this field; the gate on a
// proposal filed while the run is still going (ui.Client.CreateTask) did
// not know sessions existed at all, so it fell through to AutoSubmits
// and filed every director proposal `proposed`; and
// DirectorSessionBody's own sentence read the field directly and told
// the session its work was queued. A standing session files through the
// live path, which is how the prompt came to assert a permission the
// gate it actually met did not grant.
//
// So it is a method rather than a bare field read: the prompt and both
// gates call this, and console_session_test's own
// TestWhatTheSessionIsToldMatchesWhatTheGateDoes holds them to the same
// answer.
func (s ConsoleSession) QueuesProposals() bool { return s.TasksApproved }

// ConsoleSessionQueuesProposals is QueuesProposals asked about the task
// holding a session's conversation rather than about the session row --
// what both filing gates have in hand when a proposal arrives.
//
// False for every task that is not a session's, which is nearly all of
// them: propose_task's contract is that a human decides, and the one
// thing that may relax it is this deployment saying so about a session
// somebody is sitting in front of. False, too, for a task tagged with a
// session id the settings no longer name -- the permission belongs to
// the settings row, so a session removed from them takes it with it,
// even though its task is still there holding the conversation.
func ConsoleSessionQueuesProposals(sessions []ConsoleSession, task Task) bool {
	id, ok := ConsoleSessionIDOf(task)
	if !ok {
		return false
	}
	session, ok := ConsoleSessionByID(sessions, id)
	if !ok {
		return false
	}
	if slices.Contains(task.Tags, ConsoleSessionTasksApprovedTag) {
		return true
	}
	if slices.Contains(task.Tags, ConsoleSessionTasksUnapprovedTag) {
		return false
	}
	return session.QueuesProposals() || task.AutoApproveProposals
}

const (
	// ConsoleSessionTasksApprovedTag records an operator's explicit decision on
	// an active console-session task to queue its proposed tasks without waiting
	// for approval, overriding the static ConsoleSession.TasksApproved template.
	ConsoleSessionTasksApprovedTag = "console-session-tasks-approved"
	// ConsoleSessionTasksUnapprovedTag records an operator's explicit decision on
	// an active console-session task to hold its proposed tasks for approval,
	// overriding the static ConsoleSession.TasksApproved template.
	ConsoleSessionTasksUnapprovedTag = "console-session-tasks-unapproved"
)

// DirectorSessionID names the director: the standing session grain
// ships, and the one a deployment gets if it has never chosen otherwise.
const DirectorSessionID = "director"

// ConsoleSessionTagPrefix marks the tag that ties a task to the session
// whose conversation it holds. A tag rather than a well-known task id
// because task ids are allocated from task_sequence and are numeric
// (Store.NewTaskID) -- everything from a branch name to a URL assumes
// that shape -- and because a tag is already how grain finds a task it
// filed itself (Store.HasOpenTaskWithTag, docs/data-model.md's
// idempotency tag).
const ConsoleSessionTagPrefix = "console-session:"

// ConsoleSessionResetTag marks a closed console-session task whose
// session was reset or deleted, so a future session with the same ID starts
// fresh without linking to or carrying over its handover summary, while
// still keeping ConsoleSessionTag so the closed task remains classified as a
// console session for list filtering and metrics.
const ConsoleSessionResetTag = "console-session-reset"

// ConsoleSessionTag is the tag carried by the task holding id's
// conversation.
func ConsoleSessionTag(id string) string { return ConsoleSessionTagPrefix + id }

// ConsoleSessionIDFromTag is ConsoleSessionTag read back: the session id
// a tag names, and false for a tag that names no session at all.
func ConsoleSessionIDFromTag(tag string) (string, bool) {
	id, ok := strings.CutPrefix(tag, ConsoleSessionTagPrefix)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// ConsoleSessionIDOf is the session a task belongs to, if any -- read
// off its tags, which is the only place the association is recorded.
func ConsoleSessionIDOf(t Task) (string, bool) {
	for _, tag := range t.Tags {
		if id, ok := ConsoleSessionIDFromTag(tag); ok {
			return id, true
		}
	}
	return "", false
}

// IsConsoleSession reports whether t is a console-session task
// (directorTaskSQL): filed as ReasonConsoleSession, Interactive, and
// tagged with a ConsoleSessionTag.
func (t Task) IsConsoleSession() bool {
	if t.Origin.Reason != ReasonConsoleSession || !t.Interactive {
		return false
	}
	_, ok := ConsoleSessionIDOf(t)
	return ok
}

// registeredDefaultCapabilities are the capabilities a package outside
// core registered as on by default for the build
// (pkg/capability.Registration.DefaultOn, which calls
// RegisterDefaultConsoleSessionCapability). DefaultConsoleSessions
// grants them to the director alongside core's own four.
var (
	registeredDefaultCapabilitiesMu sync.RWMutex
	registeredDefaultCapabilities   = map[string]bool{}
)

// RegisterDefaultConsoleSessionCapability adds id to the capabilities
// DefaultConsoleSessions grants the director until the returned func is
// called. pkg/capability.RegisterProvider calls it for each Registration
// with DefaultOn set, so a build that turns a capability on by default
// for new tasks also turns it on by default for the director.
func RegisterDefaultConsoleSessionCapability(id string) (unregister func()) {
	registeredDefaultCapabilitiesMu.Lock()
	defer registeredDefaultCapabilitiesMu.Unlock()
	registeredDefaultCapabilities[id] = true
	return func() {
		registeredDefaultCapabilitiesMu.Lock()
		defer registeredDefaultCapabilitiesMu.Unlock()
		delete(registeredDefaultCapabilities, id)
	}
}

func defaultConsoleSessionCapabilities() []string {
	registeredDefaultCapabilitiesMu.RLock()
	defer registeredDefaultCapabilitiesMu.RUnlock()
	if len(registeredDefaultCapabilities) == 0 {
		return nil
	}
	out := make([]string, 0, len(registeredDefaultCapabilities))
	for id := range registeredDefaultCapabilities {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// DefaultConsoleSessions is what a deployment that has never configured
// any gets: the director, and nothing else.
//
// Its capabilities are the "look at this deployment and explain it"
// set -- the source checkout this host runs from (self-debug), the
// journal it writes (grain-log), bounded host and state diagnostics
// (deployment-health), and the runbooks for setting a
// deployment up (bootstrap-playbooks), which is the capability whose own
// description already says it is for walking whoever is on the other end
// of a chat through one -- plus whatever capabilities this build
// registers as on by default (RegisterDefaultConsoleSessionCapability,
// e.g. corp-research on the Google build). Read access to the state
// repository comes with
// the task rather than from a grant (orchestrator.consoleSessionTask),
// since that is grain's own database as text and it is what "what is
// this deployment actually doing" is answered out of.
//
// Deliberately not granted, and both are one tick away in the UI for an
// operator who wants them: self-repair, which lets a run change things
// on grain's own host, and which is the one capability here that can
// break the deployment it is holding a conversation with; and memory,
// which writes the standing instructions every later run is given.
//
// NetworkOpen because the shipped host-directory sandbox cannot honour
// NetworkNone: it runs on the host's own network stack, and correctly
// refuses to pretend that network is absent. A deployment with an
// isolating backend can close the director in the same pane the
// capabilities are in.
func DefaultConsoleSessions() []ConsoleSession {
	caps := []string{"self-debug", GrainJournalGrant, "api:deployment:read", GrainHealthGrant, "bootstrap-playbooks"}
	for _, id := range defaultConsoleSessionCapabilities() {
		if !slices.Contains(caps, id) {
			caps = append(caps, id)
		}
	}
	return []ConsoleSession{{
		ID:    DirectorSessionID,
		Title: "Director",
		Team:  DefaultTeamID,
		// Named as strings rather than through each capability package's
		// own CapabilityName: those packages import this one.
		Capabilities:  caps,
		NetworkAccess: NetworkOpen,
	}}
}

// ConsoleSessionsOr is how every reader takes Config.ConsoleSessions:
// the configured list, or the shipped default for a deployment that has
// never said anything about them.
//
// The distinction it rests on is the one EncodeConsoleSessions and
// DecodeConsoleSessions keep, and it is the difference between a nil
// slice and an empty one: an unset setting is not an empty list. A
// deployment that has never been asked gets a director; one that has
// deliberately saved an empty list gets none, and does not grow one back
// on the next tick.
func ConsoleSessionsOr(configured []ConsoleSession) []ConsoleSession {
	if configured == nil {
		return DefaultConsoleSessions()
	}
	return configured
}

// EncodeConsoleSessions is Config.ConsoleSessions as it is stored: JSON,
// in one grain_config column.
//
// JSON rather than a table of its own because these are settings, not
// records -- a handful of rows a person edits in a form, exported to the
// state repository with the rest of grain_config, and never queried or
// joined against. A nil slice encodes as "", which is the unset column
// ConsoleSessionsOr reads as "this deployment has never chosen"; an
// empty non-nil slice encodes as "[]", which is a deployment saying it
// wants none.
func EncodeConsoleSessions(sessions []ConsoleSession) (string, error) {
	if sessions == nil {
		return "", nil
	}
	data, err := json.Marshal(sessions)
	if err != nil {
		return "", fmt.Errorf("encoding the console sessions: %w", err)
	}
	return string(data), nil
}

// DecodeConsoleSessions is EncodeConsoleSessions read back. A nil slice
// is the unset column -- a deployment that has never chosen -- and a
// non-nil empty one is a deployment that chose to have none, which is
// the distinction ConsoleSessionsOr turns into an answer.
//
// A column that does not parse is an error rather than a silent fall
// back to the default: the default would file a director a deployment
// may have deliberately removed, and doing that on every tick because
// one row is malformed is worse than saying so.
func DecodeConsoleSessions(stored string) ([]ConsoleSession, error) {
	if strings.TrimSpace(stored) == "" {
		return nil, nil
	}
	var sessions []ConsoleSession
	if err := json.Unmarshal([]byte(stored), &sessions); err != nil {
		return nil, fmt.Errorf("reading the stored console sessions: %w", err)
	}
	if sessions == nil {
		sessions = []ConsoleSession{}
	}
	return sessions, nil
}

// ValidateConsoleSessions is what a save is checked against: every
// session needs an id, no two may share one (they would fight over the
// same task), and a named network access has to be one grain can
// actually build a sandbox for.
//
// It says nothing about the capability ids -- ui.UpdateSettings checks
// those against the capabilities this build actually offers, which this
// package cannot see.
func ValidateConsoleSessions(sessions []ConsoleSession) error {
	seen := map[string]bool{}
	for _, s := range sessions {
		id := strings.TrimSpace(s.ID)
		if id == "" {
			return fmt.Errorf("a console session needs an id")
		}
		if strings.ContainsAny(id, " \t\n") {
			return fmt.Errorf("console session id %q may not contain spaces", s.ID)
		}
		if seen[id] {
			return fmt.Errorf("two console sessions share the id %q", id)
		}
		seen[id] = true
		if s.NetworkAccess != "" && !ValidNetworkAccess(s.NetworkAccess) {
			return fmt.Errorf("console session %q asks for network access %q, which is not one grain knows", id, s.NetworkAccess)
		}
		if !validConsoleSessionTitleSource(s.TitleSource) {
			return fmt.Errorf("console session %q has title source %q, which is not one grain knows", id, s.TitleSource)
		}
	}
	return nil
}

// ConsoleSessionByID picks one session out of a list, or reports that
// this deployment does not define it.
func ConsoleSessionByID(sessions []ConsoleSession, id string) (ConsoleSession, bool) {
	for _, s := range sessions {
		if s.ID == id {
			return s, true
		}
	}
	return ConsoleSession{}, false
}

// ConsoleSessionTitle is what a session is called when its own Title is
// blank -- its id, rather than an empty heading in the rail.
func ConsoleSessionTitle(s ConsoleSession) string {
	if title := strings.TrimSpace(s.Title); title != "" {
		return title
	}
	return s.ID
}

// ConsoleSessionTitleSource is who gave a console session its title
// (ConsoleSession.TitleSource).
type ConsoleSessionTitleSource string

const (
	// TitleSourceHeuristic is a title read off the session's first
	// message by ConsoleSessionTitleFromMessage -- a placeholder the
	// session's agent is asked to replace once.
	TitleSourceHeuristic ConsoleSessionTitleSource = "heuristic"
	// TitleSourceAgent is a title the session's own agent chose through
	// rename_session. It is final as far as agents are concerned.
	TitleSourceAgent ConsoleSessionTitleSource = "agent"
	// TitleSourcePerson is a title a person typed: on the new-session
	// form, or in the settings pane. No agent may overwrite it.
	TitleSourcePerson ConsoleSessionTitleSource = "person"
)

// validConsoleSessionTitleSource reports whether source is one grain
// knows, or empty (a session saved before the field existed).
func validConsoleSessionTitleSource(source ConsoleSessionTitleSource) bool {
	switch source {
	case "", TitleSourceHeuristic, TitleSourceAgent, TitleSourcePerson:
		return true
	}
	return false
}

// ResolvedTitleSource is s.TitleSource, or for a session saved before
// that field existed, what its title most likely was: the heuristic one
// only if it is exactly what ConsoleSessionTitleFromMessage makes of
// firstMessage (the first thing a person said in the session), and a
// person's otherwise. Leaning that way is deliberate -- reading a name
// somebody chose as a placeholder would let an agent overwrite it, while
// reading an agent's earlier retitle as a person's only means it is not
// renamed again, which is what is wanted anyway. The shipped director's
// name is the deployment's, so it is never heuristic.
func (s ConsoleSession) ResolvedTitleSource(firstMessage string) ConsoleSessionTitleSource {
	if s.TitleSource != "" {
		return s.TitleSource
	}
	if s.ID != DirectorSessionID && strings.TrimSpace(s.Title) == ConsoleSessionTitleFromMessage(firstMessage) {
		return TitleSourceHeuristic
	}
	return TitleSourcePerson
}

// ConsoleSessionFirstMessage is the first thing a person said in a
// session's conversation, given its task's comments in order -- what a
// session started from a message was titled from, and so what
// ResolvedTitleSource compares a legacy session's title against. "" when
// no person has said anything yet.
func ConsoleSessionFirstMessage(comments []Comment) string {
	for _, c := range comments {
		if c.Author.Actor.Kind == PrincipalHuman {
			return c.Body
		}
	}
	return ""
}

// ConsoleSessionUntitled is what a session is called when it was started
// from a message nothing could be read off -- a first message that was
// only an attachment, or only a code block. It is the name a chat app
// gives a chat before the conversation has given it one.
const ConsoleSessionUntitled = "New chat"

// ConsoleSessionTitleMaxLength bounds a title ConsoleSessionTitleFromMessage
// derives, in runes: a session's name sits in the rail, on a task row
// and in a tab title, and a first message is often a paragraph.
const ConsoleSessionTitleMaxLength = 60

// consoleSessionTitleMaxWords is the other bound on a derived title: a
// short overview reads as a handful of words, not a clause that happens
// to fit in sixty characters.
const consoleSessionTitleMaxWords = 8

// ConsoleSessionTitleFromMessage is the name a session started from a
// chat gets, read off the first thing said in it -- the way a chat
// product names a conversation from its opening line rather than asking
// for a title before there is anything to title.
//
// It is the message's first real line of prose: code fences, quoted
// lines, heading and list markers and bare URLs are skipped, since none
// of them is a sentence; the first sentence of that line is kept, cut to
// consoleSessionTitleMaxWords words and ConsoleSessionTitleMaxLength
// runes at a word boundary with its closing punctuation dropped, and its
// first letter is capitalised. A message that leaves nothing to read --
// an attachment alone, or only a code block -- is ConsoleSessionUntitled.
//
// It is a heuristic and says so by being short: it is the session's
// first name, not its last. UpdateConsoleSession's title field is still
// there for a person who wants a better one, and the session's own agent
// is told (DirectorSessionBody) to give it one, once, through
// rename_session once it can see what the conversation is about.
func ConsoleSessionTitleFromMessage(message string) string {
	line := firstProseLine(message)
	if line == "" {
		return ConsoleSessionUntitled
	}
	line = firstSentence(line)
	words := strings.Fields(line)
	if len(words) > consoleSessionTitleMaxWords {
		words = words[:consoleSessionTitleMaxWords]
	}
	title := strings.Join(words, " ")
	if len([]rune(title)) > ConsoleSessionTitleMaxLength {
		runes := []rune(title)[:ConsoleSessionTitleMaxLength]
		cut := strings.LastIndex(string(runes), " ")
		if cut > 0 {
			runes = []rune(string(runes)[:cut])
		}
		title = string(runes)
	}
	title = strings.TrimRight(title, " .,;:!?-–—")
	if title == "" {
		return ConsoleSessionUntitled
	}
	runes := []rune(title)
	runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	return string(runes)
}

// firstProseLine is the first line of message that reads as prose: not
// inside a code fence, not a quote, not a bare URL, and not empty once
// heading and list markers are stripped off it.
func firstProseLine(message string) string {
	inFence := false
	for _, raw := range strings.Split(message, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || line == "" || strings.HasPrefix(line, ">") {
			continue
		}
		line = strings.TrimLeft(line, "#*-+ \t")
		if i := strings.IndexAny(line, ".)"); i > 0 && i <= 3 && isDigits(line[:i]) {
			// A numbered list item: "1. " or "2) ".
			line = strings.TrimSpace(line[i+1:])
		}
		words := strings.Fields(line)
		kept := words[:0]
		for _, w := range words {
			if strings.HasPrefix(w, "http://") || strings.HasPrefix(w, "https://") {
				continue
			}
			kept = append(kept, strings.Trim(w, "`*_"))
		}
		line = strings.TrimSpace(strings.Join(kept, " "))
		if line != "" {
			return line
		}
	}
	return ""
}

// firstSentence cuts line at the end of its first sentence -- a full
// stop, question or exclamation mark followed by a space -- leaving a
// line with no such break whole.
func firstSentence(line string) string {
	for i, r := range line {
		if (r == '.' || r == '?' || r == '!') && i+1 < len(line) && line[i+1] == ' ' {
			return line[:i]
		}
	}
	return line
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// DirectorSessionBody is what the director is actually told, and it is
// code for SystemTemplate's own reason: what a standing session with
// this deployment's own host under it is for is grain's to say, reviewed
// in grain's repository, rather than a settings field somebody can
// rewrite into a different agent with the same grants.
//
// It is written against what the session can actually reach -- the state
// repository checkout for this deployment's configuration and memory,
// the self-debug task readers for the record of what it has run, grain's
// own source and journal through the capabilities the deployment ticked,
// and propose_task for anything that is real work. It says which of
// those two answers which kind of question, because the state repository
// exports configuration only (pkg/staterepo's ExportConfig) and a
// director that goes looking for tables/task.json finds nothing and has
// to guess. Most of its length is about the one thing a chat
// makes too easy: doing the work in the conversation instead of filing
// it. A director that starts editing repositories itself is an
// unreviewed agent with a permanent sandbox; a director that files tasks
// is a person's assistant, and every task it files is dispatched,
// recorded and reviewed like any other.
// What it says about the session's own filing rights is not written from
// the settings field but from session.QueuesProposals, which is the
// predicate both filing gates are written from as well: a prompt that
// asserts a permission out of a second reading of the same row is a
// prompt that can be made to lie by changing one of them
// (grain/task-311, where it did).
//
// One paragraph is about the session's own name, and it is there only
// while session.TitleSource is TitleSourceHeuristic: the title is still
// the placeholder ConsoleSessionTitleFromMessage read off the first
// message, and the agent is asked to replace it once through
// rename_session. A standing session is redispatched for every message,
// each time to a fresh agent, so the decision cannot be left to the
// agent's judgment of the title -- asking every run to judge it is how
// sessions came to be retitled whenever the latest turn changed topic.
// Once an agent or a person has named the session the paragraph is gone,
// and rename_session refuses besides (ui.Client.RenameConsoleSessionOfTask).
// A caller holding a session saved before TitleSource existed resolves
// it first (ResolvedTitleSource); an unresolved empty source reads as
// not heuristic, so the paragraph is never added by default. Since the
// body changes with the source, a rename changes the body too, and the
// reconciler writes both onto the task in the same cycle
// (orchestrator.syncConsoleSessionTask).
func DirectorSessionBody(stateRepo RepoRef, session ConsoleSession) string {
	var b strings.Builder
	b.WriteString("You are this deployment's director. Somebody is talking to you in grain's " +
		"own UI, in a chat that stays open: this task is never finished, and every message " +
		"you get is the next turn of one long conversation.\n\n")

	b.WriteString("What you are for: helping whoever is on the other end run this deployment. " +
		"Getting it set up, working out why something went wrong, deciding what should happen " +
		"next, and turning any of that into tasks.\n\n")

	if session.ID != DirectorSessionID && session.TitleSource == TitleSourceHeuristic {
		b.WriteString("**Name the conversation.** This session is titled " +
			fmt.Sprintf("%q", ConsoleSessionTitle(session)) + ", which is only the opening words " +
			"of its first message: the best anything could do before the conversation had a " +
			"shape, and you know the shape. Early in this exchange, once you can see what the " +
			"conversation is about, call `rename_session` once with a short overview of the " +
			"subject: at most " + fmt.Sprint(ConsoleSessionTitleMaxLength) + " characters, the " +
			"way a chat app names a conversation -- \"Nightly CI failure\" rather than \"Hi, " +
			"can you look at why the nightly\". That names the session for good: it keeps " +
			"that name however the conversation goes on, and `rename_session` refuses any " +
			"further call.\n\n")
	}

	if stateRepo.Owner != "" {
		b.WriteString("This deployment's configured state repository is " + stateRepo.String() + ". When this session has read access, it holds this " +
			"deployment's editable configuration and memory exported as text, one file per " +
			"table under `tables/`: `tables/grain_config.json` and `tables/repo_config.json` " +
			"for what this deployment is configured to do, the templates, suites, schedules, " +
			"boards and custom fields beside them, and `tables/memory.json` for what its " +
			"memory agent has written. That is where you look those up rather than guessing, " +
			"and a change to any of them is a pull request against that repository rather " +
			"than something you can edit from here.\n\n" +
			"What that repository does not hold is the record of what has been run. grain " +
			"exports its configuration and memory and keeps tasks, runs, comments and " +
			"observations in its own database, so there is no `tables/task.json`, " +
			"`tables/task_run.json` or `tables/task_comment.json` there however much a " +
			"question calls for one.\n\n")
	} else {
		b.WriteString("This deployment keeps its configuration locally rather than in a state " +
			"repository, so there is no export of its settings or its memory for you to read. " +
			"Say so plainly when somebody asks you something only that could answer.\n\n")
	}

	b.WriteString("The record of what this deployment has actually done is read through the " +
		"self-debug capability, if this session holds it, with the `grain` CLI in your sandbox " +
		"(`grain help`): `grain task list` for the tasks, `grain task read <id>` for one task's whole " +
		"record -- every attempt, how each ended, and the conversation on it -- " +
		"`grain task prompt <id>` for what an agent was told, and " +
		"`grain task transcript <id>` for one attempt's own story, and `grain metrics` " +
		"for what this deployment measures about itself -- throughput, latency, how full it " +
		"has been, the tool-use census, prompt sizes, CI waits, what sandboxes have cost. " +
		"Any question about how this deployment behaves in general is that command's rather " +
		"than a guess from the tasks you happened to read. If you do not hold it, or " +
		"those commands refuse because this session has no route back to the daemon, then the " +
		"history is not available to you: say that, and say it would need the self-debug " +
		"capability, rather than answering from configuration or from what you remember of " +
		"this conversation.\n\n")

	b.WriteString("Whatever else you hold is on this task's capabilities, and the person you " +
		"are talking to can change them in the same pane this chat is in. Not every change " +
		"reaches you at the same moment: repositories added to or removed from this " +
		"session's reads and writes, and `api:*` scopes called through `grain api`, take " +
		"effect at once; everything else -- a new or removed capability, and an `api:*` " +
		"route served only as an `api_*` tool -- takes effect when grain restarts this " +
		"session to pick it up, which it says in this chat and which loses nothing said " +
		"here. Check what you " +
		"actually have before promising anything: if you are asked to do something you have no " +
		"capability for, say which one it would need rather than working around it.\n\n")

	b.WriteString("Anything a person can do in grain's UI you can do through grain's own API, if " +
		"you are granted its scope: an `api:<scope>` or `api:<preset>` capability on this task " +
		"serves you its routes (docs/agent-api.md): `grain api routes` lists them and `grain api <METHOD> " +
		"<path>` calls one from your sandbox, while a route no sandbox process may call stays an " +
		"`api_*` tool call -- `api:task-admin` " +
		"to edit, reorder, retry and close tasks and re-run their CI, `api:ci-read` to read CI " +
		"logs, `api:release-manager` to run schedules and cut and promote release candidates. " +
		"When you need one you do not hold, ask the person for that grant by name; never try to " +
		"grant it yourself. Scopes marked dangerous (secrets, settings, connections, capability " +
		"admin, force-merge, task admin, deployment) only a person can grant, and every call " +
		"under one, and every delete, needs a reason. Each call is recorded as yours.\n\n")

	b.WriteString("**File work rather than doing it.** propose_task is your main instrument: " +
		"anything that means changing a repository, running a long job, or spending a sandbox " +
		"belongs in a task, described well enough that whoever picks it up needs nothing from " +
		"this conversation. ")
	if session.QueuesProposals() {
		b.WriteString("This deployment has said your tasks are filed approved, so what you " +
			"file is queued and will be dispatched -- file carefully, and say in the chat what " +
			"you have just set going. That is a ceiling, not an order: pass hold_for_approval " +
			"on any proposal the person should look at before it runs, and it is filed " +
			"unapproved for them instead. Say in the chat which you held and which you let " +
			"queue -- propose_task's result tells you which happened.\n\n")
	} else {
		b.WriteString("What you file waits for a person to approve it, which is the point: " +
			"say in the chat what you filed and why, so they can decide.\n\n")
	}

	b.WriteString("Answer in the chat, briefly. You are talking to somebody who is reading " +
		"this on a screen next to their work, not writing them a report: a few sentences, the " +
		"thing they asked for, and what you would do next. Ask when you are unsure rather than " +
		"guessing -- there is a person right there. Any link to a grain UI route (`/tasks/<id>`, " +
		"`/settings/...`, `/system/...`, etc.) must always be a relative path (such as " +
		"`[Task #31](/tasks/31)` or `[Settings → CI](/settings/ci)`) and never an absolute " +
		"`http://127.0.0.1:...` or `http://localhost:...` URL, so links work when viewed " +
		"through UberProxy or any reverse proxy.\n\n")

	b.WriteString("When you have nothing further to say, stop. The conversation stays open and " +
		"the next message picks it back up, so there is no need to keep going, to summarise " +
		"what you just said, or to invent the next piece of work.")
	return b.String()
}

// ConsoleSessionOpening is the first thing in a session's conversation,
// and it is also what the session is parked on until somebody says
// something (ParkConsoleSessionTask).
const ConsoleSessionOpening = "I'm here. Ask me about this deployment, tell me what you want set up, " +
	"or say what has gone wrong -- I can look at the record, read grain's own source, and file " +
	"tasks for anything that needs doing."

// ConsoleSessionResting is what grain says when a session's run ended
// without asking anything, which is the ordinary way an exchange ends.
const ConsoleSessionResting = "_Waiting for you._"

// ConsoleSessionHandoverNote frames the summary a replacement session is
// handed.
const ConsoleSessionHandoverNote = "_What the last conversation left, carried over from task %s -- " +
	"its own chat is still there, closed, and everything said in it is still readable._\n\n%s"

// FileConsoleSessionOptions configures how FileConsoleSessionTask creates the
// underlying task for a console session.
type FileConsoleSessionOptions struct {
	StateRepo            RepoRef
	NetworkIsolates      bool
	PreviousTaskID       string
	CarryHandoverSummary bool
	Now                  time.Time
}

// ConsoleSessionGrants returns the session's configured capabilities as
// GrantByGrain grants, always including DurableCapability, followed by
// defaults -- this deployment's Config.DefaultCapabilities -- as
// GrantByDefault for any the session does not already name.
//
// The defaults are there because every task a session files is seeded
// with them (ui.Client.defaultCapabilities), and a capability reference
// its proposer does not hold is an escalation however it came to be on
// the proposal (EscalatedCapabilities, docs/capabilities-spec.md
// §2.3.4). A session left without them could never queue anything on a
// deployment whose defaults name a reference, whatever TasksApproved
// says (grain/task-1983): holding what its own proposals are seeded with
// is what lets a session's approval mean what it says.
func ConsoleSessionGrants(session ConsoleSession, defaults ...string) []Grant {
	grants := make([]Grant, 0, len(session.Capabilities)+len(defaults)+1)
	grants = append(grants, Grant{Capability: DurableCapability, Via: GrantByGrain})
	seen := map[string]bool{DurableCapability: true}
	for _, name := range session.Capabilities {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		grants = append(grants, Grant{Capability: name, Via: GrantByGrain})
	}
	for _, name := range defaults {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		grants = append(grants, Grant{Capability: name, Via: GrantByDefault})
	}
	return grants
}

// DeploymentDefaultCapabilities is this deployment's
// Config.DefaultCapabilities as stored, or none when there is no config
// row yet: what ConsoleSessionGrants seeds a standing session with.
func DeploymentDefaultCapabilities(ctx context.Context, store *Store) ([]string, error) {
	cfg, err := store.GetConfig(ctx)
	if err != nil || cfg == nil {
		return nil, err
	}
	return cfg.DefaultCapabilities, nil
}

// ConsoleSessionNetworkAccess resolves the network access for a console
// session task given whether the sandbox backend isolates network access.
func ConsoleSessionNetworkAccess(session ConsoleSession, networkIsolates bool) NetworkAccess {
	if session.NetworkAccess != NetworkNone || networkIsolates {
		return session.NetworkAccess
	}
	return NetworkOpen
}

// ConsoleSessionConfigSnapshot records the static ConsoleSession config values
// last applied to a console session task, so syncConsoleSessionTask can distinguish
// config changes in grain_config from operator edits made directly on the active
// session task.
type ConsoleSessionConfigSnapshot struct {
	Capabilities    []string
	Defaults        []string
	Reads           []RepoRef
	Writes          []WriteTarget
	NetworkAccess   NetworkAccess
	Model           string
	Title           string
	TasksApproved   bool
	GoogleChatSpace string
}

type consoleSessionSnapshotKey struct {
	store  *Store
	taskID string
}

var (
	consoleSessionSnapshotsMu sync.Mutex
	consoleSessionSnapshots   = map[consoleSessionSnapshotKey]ConsoleSessionConfigSnapshot{}
)

// LastSyncedConsoleSession returns the ConsoleSessionConfigSnapshot last synced
// onto taskID in store, if any.
func LastSyncedConsoleSession(store *Store, taskID string) (ConsoleSessionConfigSnapshot, bool) {
	consoleSessionSnapshotsMu.Lock()
	defer consoleSessionSnapshotsMu.Unlock()
	snap, ok := consoleSessionSnapshots[consoleSessionSnapshotKey{store: store, taskID: taskID}]
	return snap, ok
}

// RecordSyncedConsoleSession stores the ConsoleSessionConfigSnapshot last synced
// onto taskID in store.
func RecordSyncedConsoleSession(store *Store, taskID string, snap ConsoleSessionConfigSnapshot) {
	consoleSessionSnapshotsMu.Lock()
	defer consoleSessionSnapshotsMu.Unlock()
	consoleSessionSnapshots[consoleSessionSnapshotKey{store: store, taskID: taskID}] = ConsoleSessionConfigSnapshot{
		Capabilities:    slices.Clone(snap.Capabilities),
		Defaults:        slices.Clone(snap.Defaults),
		Reads:           slices.Clone(snap.Reads),
		Writes:          slices.Clone(snap.Writes),
		NetworkAccess:   snap.NetworkAccess,
		Model:           snap.Model,
		Title:           snap.Title,
		TasksApproved:   snap.TasksApproved,
		GoogleChatSpace: snap.GoogleChatSpace,
	}
}

// FileConsoleSessionTask files a new underlying task for session, optionally
// linking it to opts.PreviousTaskID and carrying over its handover summary
// when opts.CarryHandoverSummary is true, and parks it on
// ConsoleSessionOpening.
func FileConsoleSessionTask(ctx context.Context, store *Store, session ConsoleSession, opts FileConsoleSessionOptions) (string, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	id, err := store.NewTaskID(ctx)
	if err != nil {
		return "", fmt.Errorf("allocating an id: %w", err)
	}
	orderKey, err := store.OrderKeyForNewTask(ctx, true)
	if err != nil {
		return "", err
	}
	stateRepo := opts.StateRepo
	defaults, err := DeploymentDefaultCapabilities(ctx, store)
	if err != nil {
		return "", err
	}
	task := Task{
		ID:     id,
		Intent: IntentAnalyze,
		Title:  ConsoleSessionTitle(session),
		Body:   DirectorSessionBody(stateRepo, session),
		Origin: Origin{
			Attribution: Attribution{Actor: ConsoleSessionPrincipal},
			Reason:      ReasonConsoleSession,
		},
		Binding:       BindingDirective,
		Grants:        ConsoleSessionGrants(session, defaults...),
		Tags:          []string{ConsoleSessionTag(session.ID)},
		Interactive:   true,
		NetworkAccess: ConsoleSessionNetworkAccess(session, opts.NetworkIsolates),
		Model:         strings.TrimSpace(session.Model),
		CreatedAt:     &now,
		OrderKey:      orderKey,
		Approval:      &Attribution{Actor: ConsoleSessionPrincipal},
		ApprovedAt:    &now,
	}
	if stateRepo.Owner != "" {
		task.Reads = append(task.Reads, stateRepo)
	}
	for _, r := range session.Reads {
		if r.Owner != "" && r.Name != "" && !r.Same(stateRepo) {
			task.Reads = append(task.Reads, r)
		}
	}
	task.Writes = append([]WriteTarget{}, session.Writes...)
	if opts.PreviousTaskID != "" {
		task.Links = []Link{{Kind: LinkContinues, Target: opts.PreviousTaskID}}
	}
	if session.GoogleChatSpace != "" {
		task.Links = append(task.Links, Link{Kind: LinkGoogleChat, Target: session.GoogleChatSpace})
	}
	if err := store.PutTask(ctx, task); err != nil {
		return "", err
	}
	RecordSyncedConsoleSession(store, id, ConsoleSessionConfigSnapshot{
		Capabilities:    session.Capabilities,
		Defaults:        defaults,
		Reads:           session.Reads,
		Writes:          session.Writes,
		NetworkAccess:   task.NetworkAccess,
		Model:           task.Model,
		Title:           task.Title,
		TasksApproved:   session.TasksApproved,
		GoogleChatSpace: session.GoogleChatSpace,
	})
	if err := store.SetTaskGoal(ctx, id, GoalDecl{Kind: GoalPersonCloses}, PrincipalString(ConsoleSessionPrincipal), now); err != nil {
		return "", err
	}
	if opts.CarryHandoverSummary && opts.PreviousTaskID != "" {
		if err := CarryConsoleSessionHandover(ctx, store, opts.PreviousTaskID, id, now); err != nil {
			return "", err
		}
	}
	if err := ParkConsoleSessionTask(ctx, store, id, ConsoleSessionOpening, now); err != nil {
		return "", err
	}
	return id, nil
}

// CarryConsoleSessionHandover copies the most recent handover comment off a
// session's closed task onto its replacement.
func CarryConsoleSessionHandover(ctx context.Context, store *Store, previous, taskID string, now time.Time) error {
	if previous == "" {
		return nil
	}
	comments, err := store.Comments(ctx, previous)
	if err != nil {
		return err
	}
	handover := ""
	for _, c := range comments {
		if c.Handover && strings.TrimSpace(c.Body) != "" {
			handover = c.Body
		}
	}
	if handover == "" {
		return nil
	}
	_, err = store.AddComment(ctx, Comment{
		TaskID:    taskID,
		Author:    Attribution{Actor: ConsoleSessionPrincipal},
		Body:      fmt.Sprintf(ConsoleSessionHandoverNote, previous, handover),
		CreatedAt: now,
		Handover:  true,
	})
	return err
}

// ParkConsoleSessionTask puts a session's task back to waiting for a person:
// grain says one line, and the task is parked on it.
func ParkConsoleSessionTask(ctx context.Context, store *Store, taskID, note string, now time.Time) error {
	var commentID int64
	comments, err := store.Comments(ctx, taskID)
	if err != nil {
		return err
	}
	if len(comments) > 0 {
		last := comments[len(comments)-1]
		if last.Author.Actor == ConsoleSessionPrincipal && last.Body == note {
			commentID = last.ID
		}
	}
	if commentID == 0 {
		var err error
		commentID, err = store.AddComment(ctx, Comment{
			TaskID:    taskID,
			Author:    Attribution{Actor: ConsoleSessionPrincipal},
			Body:      note,
			CreatedAt: now,
		})
		if err != nil {
			return err
		}
	}
	if err := store.ObserveField(ctx, taskID, now, func(o *Observation) {
		o.PendingQuestionCommentID = &commentID
		o.PendingSecret = ""
	}); err != nil {
		return err
	}
	if note == ConsoleSessionResting {
		runs, err := store.Runs(ctx, taskID)
		if err != nil {
			return err
		}
		var latest *Run
		for i := range runs {
			if runs[i].FinishedAt != nil && (latest == nil || runs[i].StartedAt.After(latest.StartedAt)) {
				latest = &runs[i]
			}
		}
		if latest != nil && latest.SuspendKind == "" && EndingOf(latest.Outcome, latest.Detail) == EndingSucceeded {
			on := ResumeOn{Message: true}
			at := now
			if latest.StartedAt.After(at) {
				at = latest.StartedAt
			}
			if latest.FinishedAt.After(at) {
				at = *latest.FinishedAt
			}
			if err := store.Suspend(ctx, Suspension{
				RunID:     latest.ID,
				TaskID:    taskID,
				Kind:      SuspendRequested,
				CreatedAt: at,
				Detail:    "the agent ended its run; the session rests until the next message",
				ResumeOn:  on.JSON(),
			}); err != nil {
				return err
			}
			if err := store.SetDeferred(ctx, taskID, &at, nil, at); err != nil {
				return err
			}
		}
	}
	return nil
}
