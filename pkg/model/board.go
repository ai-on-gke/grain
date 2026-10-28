package model

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Board is a saved dashboard: one named way of looking at this
// deployment's tasks, kept in the store so that everybody looking at
// this deployment sees it and so that an agent can make one
// (grain/task-48).
//
// # Why this is a table and not another localStorage key
//
// The Kanban board's own columns already live in the browser
// (ui/src/board.js, BOARD_STORAGE_KEY) on the argument that they are how
// one person wants to look at the deployment rather than a fact about
// it. That argument runs out here for one reason: the thing asking for
// a board is an agent. "Make me a dashboard of everything waiting on me
// in acme/widgets" is a task somebody files, and a run has no way to
// reach into a browser's localStorage -- it has a checkout and a git
// remote. A board in the store is exported to the state repository like
// every other setting (staterepo.SettingsTables), which makes creating
// one an ordinary pull request against tables/board.json and makes it
// live on the tick after that pull request merges.
//
// The columns stay where they are: a Kanban board carries its own
// (Columns, below) and falls back to whatever the browser holds when it
// names none, so nothing about the existing board changes.
//
// # Query, and why it is a query string
//
// A board's filtering and sorting is stored exactly as the UI already
// writes it into the address bar -- "q=ci&sort=created:desc&f=state:any:
// running" (ui/src/paths.js). Nothing here parses it; the UI that
// renders a board is the only reader, and it is the same parser that
// opens a link somebody pasted.
//
// That is worth more than a structured filter table would be. A person
// (or an agent) building a board narrows the ordinary task list until it
// says what they meant, copies the query out of the address bar, and
// that string *is* the board -- no second vocabulary to learn, no way
// for the two to drift, and a board that names an attribute this build
// does not have degrades exactly the way such a link does rather than
// failing to load.
//
// # Kind, and the dashboards that are not yet here
//
// Kind is the one field that says what sort of dashboard this is, and it
// has one value per view of tasks the UI has: a list, a Kanban board and
// a timeline (grain/task-55) -- and, since grain/task-88, "panels", a
// page composed of several of those at once rather than one of them
// filling the pane. It is a string rather than a bool for the
// obvious reason -- the next dashboard type, a repo summary or somebody's
// own report, adds a value to BoardKinds and a renderer in the UI, and
// everything else on this row (name, folder, order, the query) goes on
// meaning what it means. The timeline is the first kind added that way,
// and it needed exactly that: a word here and a branch in App.jsx.
//
// "report" (grain/task-83) is where that claim was actually tested,
// because it is the first kind that is not a view of the backlog: it
// draws the measurement report (GET /api/metrics) rather than task rows,
// and it still needed nothing of this row but the query. What it needed
// instead was two things worth writing down.
//
// The first is that the query had to *mean* something to a report, and
// it does, in the one way it can: a query is a set of conditions on
// tasks, and the conditions a report can honestly be computed under --
// which repo, which author, which dates -- become metrics.Scope and the
// report's own window. The rest of a query (a text search, a sort, a
// state) has nothing to narrow in a distribution, and the page says so
// rather than pretending. That is a strictly weaker reading of the same
// string than a task view gives it, which is the price of one field
// serving every kind, and it is the right price: no second vocabulary,
// and a board narrowed on screen still scopes what it can.
//
// The second is where a setting belonging to one kind goes, and the
// answer was already written down twice -- BoardColumn and BoardPanel
// are both child tables, and schema.go says why. So Sections is a third
// (`board_section`), and the pattern is now the rule rather than a
// coincidence: a kind's own setting is rows an agent can edit in the
// state repository's dump, never JSON escaped into a column of `board`.
type Board struct {
	ID   string
	Name string
	// Folder is what the board is filed under in the nav rail, "" for a
	// board filed directly under Boards. Free text rather than a table of
	// its own: a folder has no properties, nothing refers to one, and it
	// exists exactly as long as a board names it.
	Folder string
	Kind   BoardKind
	// Description is the one line the board's own page prints under its
	// name -- what this dashboard is for, in the words of whoever asked
	// for it. Optional.
	Description string
	// Query is the narrowing, in the encoding ui/src/paths.js reads and
	// writes. Empty is a board nobody has narrowed rather than a board
	// over every task, and the UI reads it the way it reads a link that
	// narrows nothing (ui/src/boards.js's boardNarrowing): a list board
	// and a dashboard of panels open on whatever the deployment's task
	// list opens on, so that a panel and the list its "Open" link opens
	// are about the same tasks; a Kanban board, a timeline and a report
	// open over every task, each answering the question itself.
	Query string
	// Columns are a Kanban board's own columns; empty means "whatever
	// columns this browser is set to". Ignored by every other kind.
	Columns []BoardColumn
	// Panels are what a BoardPanels board is made of, in display order.
	// Ignored by every other kind, which is one view and needs no
	// composition to say so.
	Panels []BoardPanel
	// Sections are which parts of the report a BoardReport board prints,
	// in order; empty is the whole report. Ignored by every other kind.
	Sections []BoardSection
	// OrderKey is where this board sits among its folder's own, ordered
	// ascending -- task.order_key's own treatment, so a board can be
	// moved without renumbering the rest.
	OrderKey float64
}

// BoardKind is which sort of dashboard a board is.
type BoardKind string

const (
	// BoardList is the flat task list (ui/src/components/TaskList.jsx).
	BoardList BoardKind = "list"
	// BoardKanban is the Kanban board (TaskBoard.jsx).
	BoardKanban BoardKind = "kanban"
	// BoardGantt is the timeline (GanttChart.jsx): the same tasks as bars
	// against a clock, one bar per task from when it was filed to when it
	// stopped being work in progress.
	BoardGantt BoardKind = "gantt"
	// BoardPanels is the one kind that is not a single view of tasks: a
	// page of several of them at once, each panel narrowed to its own
	// question (Panels below, ui/src/components/PanelBoard.jsx).
	BoardPanels BoardKind = "panels"
	// BoardReport is the first kind that is not a view of the backlog at
	// all (grain/task-83): the measurement report -- throughput, latency,
	// how attempts ended, what is still waiting -- over the tasks the
	// board's query names. Every other kind draws the task rows this UI
	// already holds; this one asks GET /api/metrics, which is the only
	// thing that can answer a question about a distribution.
	BoardReport BoardKind = "report"
	// BoardTree is the backlog nested by who filed what (TaskTree.jsx):
	// each task under the task whose run filed it, the tree of work a
	// task's own page draws (docs/work-trees.md) over the board's query.
	BoardTree BoardKind = "tree"
)

// BoardKinds is every kind this build can render, in the order the UI
// offers them. Valid answers from it and KindsPhrase quotes it back at a
// request naming something else, so adding a kind is this one line and a
// renderer rather than three places that can disagree about what exists.
var BoardKinds = []BoardKind{BoardList, BoardKanban, BoardGantt, BoardPanels, BoardReport, BoardTree}

func (k BoardKind) Valid() bool {
	return slices.Contains(BoardKinds, k)
}

// BoardKindsPhrase is the kinds as they read in a sentence -- `"list",
// "kanban" or "gantt"` -- for the error a request naming something else
// gets back. Written out rather than left as a bare list because the
// reader of that error is somebody who has just guessed a word, and the
// useful reply is which words there are.
func BoardKindsPhrase() string {
	quoted := make([]string, len(BoardKinds))
	for i, k := range BoardKinds {
		quoted[i] = strconv.Quote(string(k))
	}
	return joinPhrase(quoted)
}

// BoardColumn is one column of a Kanban board: a heading, and the states
// it collects. It is ui/src/board.js's own column, moved into the store
// for a board that wants its own -- the same shape, so a stored board
// and a browser's default render through one piece of code.
type BoardColumn struct {
	Name   string
	States []State
}

// BoardPanel is one panel of a BoardPanels board: a view of tasks, the
// question that view is asked, and how wide it sits on the page
// (grain/task-88).
//
// # Why a panel lives here and not in the browser
//
// The Kanban board's columns and the timeline's range are localStorage
// keys (ui/src/board.js, ui/src/gantt.js) on the argument that they are
// how one person is reading the deployment rather than a fact about it.
// A panel layout looks like the same kind of thing and is not, for the
// reason a board is not (Board's own doc comment): it is asked for by
// name -- "a page showing what is running, what is waiting on me and
// how last week went" -- it is meant to be there tomorrow and on
// somebody else's screen, and the thing usually asked to build one is
// an agent, which has a checkout and a git remote and no way at all
// into a browser's storage. A dashboard is also the first thing here
// anybody wants to *share*, and a link to a browser preference shares
// nothing.
//
// So a panel is a row of `board_panel`, exported to the state
// repository with every other setting, and assembling a dashboard is
// the same pull request that creating a board already is.
//
// # What a panel does not carry
//
// Columns. A Kanban panel would want its own, which is a second child
// table hanging off a child row, and a Kanban board already exists as a
// board of its own -- so the panel kinds below are the four that need
// nothing but a query.
type BoardPanel struct {
	// Kind is which view this panel draws (PanelKinds).
	Kind PanelKind
	// Title is the panel's heading. Empty means the UI's own name for
	// the kind, so a panel that is exactly what it looks like needs no
	// title invented for it.
	Title string
	// Query is what this panel is narrowed to, in the encoding the
	// address bar carries (Board.Query's own note). Empty means the
	// dashboard's own question -- a dashboard narrowed to one repo whose
	// panels each ask a further question of it would need two narrowings
	// combined, and one of them written down twice is the honest way to
	// say it.
	//
	// The dashboard's question is the board's Query when its page is
	// opened and whatever its reader has since narrowed it to from the
	// toolbar over the panels (grain/task-111): a panel that inherits
	// follows that narrowing, and a panel that carries a query of its
	// own is left asking it, for the same reason the two are never
	// combined.
	Query string
	// Range is the span a panel over time covers (PanelRangesFor), for
	// the kinds that have one -- the timeline and the metrics trend.
	// Empty is each kind's own default. Validate refuses a non-empty
	// range outside that kind's vocabulary when a board is created or
	// its panels are replaced. UpdateBoard deliberately does not judge
	// stored ranges when a PATCH omits panels, so a word written by an
	// older or newer build cannot make an unrelated rename fail.
	//
	// Rows imported from the state repository remain tolerant so that a
	// newer word cannot make a board unloadable during an upgrade.
	// `grain state check` warns before those rows are imported
	// (pkg/staterepo/vocabulary.go), and the panel itself explains the
	// fallback if one nevertheless reaches the browser
	// (ui/src/panels.js's panelRangeNote).
	Range string
	// Width is how much of the page's width the panel takes: "third",
	// "half" or "full". Empty is "half", which is the width a dashboard
	// of two-up panels wants and the one a panel written without a
	// layout in mind should get.
	Width PanelWidth
	// OrderKey is where the panel sits among the board's own, ascending.
	OrderKey float64
}

// PanelKind is which view one panel of a dashboard draws.
type PanelKind string

const (
	// PanelList is the flat task list, narrowed to the panel's query.
	PanelList PanelKind = "list"
	// PanelGantt is the timeline (GanttChart.jsx), over the panel's own
	// range.
	PanelGantt PanelKind = "gantt"
	// PanelStates is how many tasks are in each state -- the board's own
	// arithmetic (ui/src/board.js) with no cards under it, which is the
	// one number an operator glances at.
	PanelStates PanelKind = "states"
	// PanelMetrics is the throughput trend from /api/metrics, drawn as a
	// sparkline: the one panel here whose numbers are not the task list
	// this page already holds. Its query is read as a metrics.Scope
	// rather than as a filter on rows -- a repo, an author and a date
	// range narrow the report and the rest is dropped, which is the
	// reading a report board's query already gets (ReportSections' own
	// note, and ui/src/report.js at length).
	PanelMetrics PanelKind = "metrics"
)

// PanelKinds is every panel a board can be made of, in the order the UI
// offers them -- BoardKinds' own reasoning: a kind is this line and a
// renderer, not three lists that can disagree.
var PanelKinds = []PanelKind{PanelList, PanelGantt, PanelStates, PanelMetrics}

func (k PanelKind) Valid() bool { return slices.Contains(PanelKinds, k) }

// PanelKindsPhrase is the panel kinds as they read in a sentence, for
// the error a request naming something else gets back
// (BoardKindsPhrase's own reasoning and its own shape).
func PanelKindsPhrase() string {
	quoted := make([]string, len(PanelKinds))
	for i, k := range PanelKinds {
		quoted[i] = strconv.Quote(string(k))
	}
	return joinPhrase(quoted)
}

// BoardSection is one section of a BoardReport board: which part of the
// measurement report it prints, and the heading to print over it
// (grain/task-83).
//
// # Why a report board carries any rows at all
//
// The report is long. Somebody who asks for "a board showing how
// acme/widgets is going" wants throughput and latency; somebody watching
// the CI loop wants the checks section and nothing else; and a dashboard
// on a wall wants three numbers. A report board with no sections is the
// whole report, exactly as /api/metrics returns it, which is what an
// agent asked for a report board with no further instruction should
// produce -- BoardColumn's own "a board that names none gets the
// default" rather than a layout it had to invent.
//
// # Why these are rows and not a list on the board
//
// Because BoardColumn and BoardPanel are (schema.go's note on
// `board_column`): a board's kind-specific settings are what an agent
// edits in the state repository's dump, and a column of escaped JSON is
// not something anybody edits correctly. Nothing about this kind made
// that argument weaker, which is the useful thing the third instance of
// it proves.
type BoardSection struct {
	Kind ReportSection
	// Title is the heading, or "" for the section's own name. A heading
	// is worth overriding here for the reason a panel's is: "Last
	// quarter's delivery" over the throughput numbers says why the board
	// exists where "Throughput" only says what the rows are.
	Title string
	// OrderKey is where the section sits among the board's own,
	// ascending -- BoardPanel.OrderKey's own treatment.
	OrderKey float64
}

// ReportSection is one part of the measurement report.
type ReportSection string

const (
	// SectionThroughput is what crossed each line over the window, and
	// the trend lines drawn from the same counts.
	SectionThroughput ReportSection = "throughput"
	// SectionLatency is where a task's wall-clock time went: one
	// distribution per stage of its life.
	SectionLatency ReportSection = "latency"
	// SectionRuns is how the window's attempts ended -- the outcomes and
	// the endings behind them -- and what share of the deployment's
	// capacity they took.
	SectionRuns ReportSection = "runs"
	// SectionBacklog is what was still unfinished when the report was
	// taken. A gauge rather than a window (MetricsBacklog).
	SectionBacklog ReportSection = "backlog"
	// SectionTools is the per-run tool census: what a run's tool calls
	// cost it and which tool its errors are in.
	SectionTools ReportSection = "tools"
	// SectionPrompts is the other side of that census: how big the
	// prompts this deployment handed its runs were, how many crossed the
	// size grain warns about, and which tasks carry the heaviest.
	SectionPrompts ReportSection = "prompts"
	// SectionChecks is the CI loop every run is told to go round -- push,
	// wait, fix, push again -- measured end to end.
	SectionChecks ReportSection = "checks"
	// SectionCIQueueing is the other side of that loop: how much of this
	// deployment's CI job-time went on waiting for a machine rather than
	// on running the tree, what held it, and which repositories and jobs
	// wore it. Narrowed by a scope's repos and, alone among the sections
	// a scope touches at all, not by its authors -- a CI job belongs to a
	// repository and a branch rather than to whoever filed a task
	// (metrics.CIQueueing).
	SectionCIQueueing ReportSection = "ci_queueing"
	// SectionGitHubCI is the CI beside grain's own: GitHub Actions jobs
	// and other providers' check runs, their pass/fail/cancel rates per
	// job, time to green, flaky jobs and base-branch red time, labelled
	// source "github" and read from github_ci_job alone. Narrowed by a
	// scope's repos and not its authors, for SectionCIQueueing's reason
	// (metrics.GitHubCI).
	SectionGitHubCI ReportSection = "github_ci"
	// SectionCycles is the daemon's own scheduling ticks. The one section
	// a scope never narrows: a tick belongs to the deployment rather than
	// to any set of tasks (metrics.Scope).
	SectionCycles ReportSection = "cycles"
	// SectionSandboxes is how many sandboxes this deployment has built,
	// agent and CI, and how many sandbox-hours they came to. Like
	// SectionCycles it belongs to the deployment rather than to any set
	// of tasks, and unlike every other section here it is not over the
	// report's window at all: it is a lifetime meter with its own "since"
	// date (ui.SandboxLifetime, grain/task-315).
	SectionSandboxes ReportSection = "sandboxes"
	// SectionMerges is what this deployment landed during the window, how
	// long each change took to get there, batching outcomes, and branch repair
	// telemetry (metrics.Merges).
	SectionMerges ReportSection = "merges"
	// SectionAgents is durable-agent tool hygiene and steward report
	// verification per template (metrics.Agents).
	SectionAgents ReportSection = "agents"
)

// ReportSections is every section there is, in the order the report
// itself is written -- which is both the order a board with no sections
// of its own prints and the order the UI offers them. BoardKinds' own
// reasoning: one list, so nothing can disagree about what exists.
var ReportSections = []ReportSection{
	SectionThroughput, SectionLatency, SectionRuns, SectionBacklog,
	SectionTools, SectionPrompts, SectionChecks, SectionCIQueueing,
	SectionGitHubCI, SectionCycles, SectionSandboxes, SectionMerges,
	SectionAgents,
}

func (s ReportSection) Valid() bool { return slices.Contains(ReportSections, s) }

// ReportSectionsPhrase is the sections as they read in a sentence, for
// the error a request naming something else gets back
// (BoardKindsPhrase's own reasoning and its own shape).
func ReportSectionsPhrase() string {
	quoted := make([]string, len(ReportSections))
	for i, s := range ReportSections {
		quoted[i] = strconv.Quote(string(s))
	}
	return joinPhrase(quoted)
}

// PanelRanges is every span a panel over time can be asked for, in the
// order the UI offers them: four windows that end now, and "all", which
// sizes itself to whatever the tasks on screen actually cover.
//
// It is ui/src/gantt.js's RANGES, and the four fixed ones are
// ui/src/report.js's own REPORT_WINDOWS -- one vocabulary for one span,
// so that an operator reading a 7-day report and then opening the
// timeline is looking at the same seven days rather than at a
// conversion. Written
// out again here because Go has to know the words to be able to say
// which one it did not recognise (pkg/staterepo/vocabulary.go), which
// it could not do while the vocabulary lived only in a browser. The
// UI's list stays the definition, and a range is now the same deal the
// kinds beside it already are: a word here, a word in gantt.js's
// RANGES, and nothing between them but this sentence.
var PanelRanges = []string{"24h", "7d", "30d", "90d", "all"}

// MetricsPanelRanges is the subset a throughput panel can be asked for:
// every fixed window, but not "all". A report is computed over a window
// that ends now (pkg/metrics), and "all" is a span the chart sizes to
// its own bars rather than one the API takes -- so on a metrics panel
// it is a word this build has and still cannot use, which is worth
// saying as the different thing it is (ui/src/panels.js's
// METRICS_RANGES makes the same cut).
var MetricsPanelRanges = PanelRanges[:len(PanelRanges)-1]

// PanelRangesFor is the vocabulary one kind of panel's Range takes, and
// nil for the kinds that draw no span at all -- a list or a state count
// carrying a range is not a range this build has dropped, it is a sign
// somebody meant something else, and the callers that report on a range
// report that case too.
func PanelRangesFor(kind PanelKind) []string {
	switch kind {
	case PanelGantt:
		return PanelRanges
	case PanelMetrics:
		return MetricsPanelRanges
	}
	return nil
}

// PanelRangesPhrase is one kind's ranges as they read in a sentence
// (PanelWidthsPhrase's own shape), and "" for a kind that has none.
func PanelRangesPhrase(kind PanelKind) string {
	ranges := PanelRangesFor(kind)
	quoted := make([]string, len(ranges))
	for i, r := range ranges {
		quoted[i] = strconv.Quote(r)
	}
	return joinPhrase(quoted)
}

// PanelWidth is how wide a panel sits on the dashboard. Three fixed
// widths rather than a column count or a pixel size: a dashboard is
// read on a laptop and on a phone, the panels reflow, and "a third of
// the row" is a thing somebody can mean on both where "480px" is not.
type PanelWidth string

const (
	PanelThird PanelWidth = "third"
	PanelHalf  PanelWidth = "half"
	PanelFull  PanelWidth = "full"
)

// PanelWidths is every width there is, in the order the UI offers them.
var PanelWidths = []PanelWidth{PanelThird, PanelHalf, PanelFull}

func (w PanelWidth) Valid() bool { return slices.Contains(PanelWidths, w) }

// PanelWidthsPhrase is the widths as they read in a sentence.
func PanelWidthsPhrase() string {
	quoted := make([]string, len(PanelWidths))
	for i, w := range PanelWidths {
		quoted[i] = strconv.Quote(string(w))
	}
	return joinPhrase(quoted)
}

// joinPhrase writes an already-quoted list the way a sentence does --
// `"a", "b" or "c"` -- so the three vocabularies a board request can get
// wrong all quote themselves back the same way.
func joinPhrase(quoted []string) string {
	switch len(quoted) {
	case 0:
		return ""
	case 1:
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
}

// Validate reports whether a board is one this build can render --
// checked once, here, so that a bad row is refused by the API that
// writes it rather than discovered by a browser that cannot draw it.
//
// It is deliberately shallow about Query: what attributes exist depends
// on the deployment's configuration, which this package has never seen,
// and a board naming one that has since gone is a board that shows
// nothing rather than a board that is invalid (ui/src/paths.js makes the
// same argument about a link).
func (b Board) Validate() error {
	if strings.TrimSpace(b.Name) == "" {
		return fmt.Errorf("a board needs a name")
	}
	if !b.Kind.Valid() {
		return fmt.Errorf("unknown board kind %q", b.Kind)
	}
	if b.Kind != BoardKanban && len(b.Columns) > 0 {
		return fmt.Errorf("only a kanban board has columns")
	}
	if b.Kind != BoardPanels && len(b.Panels) > 0 {
		return fmt.Errorf("only a panels board has panels")
	}
	if b.Kind != BoardReport && len(b.Sections) > 0 {
		return fmt.Errorf("only a report board has sections")
	}
	// A report board with no sections is the whole report, for
	// BoardSection's own reason -- so, like a panels board with no panels
	// yet, an empty list is valid rather than a board that cannot render.
	for i, s := range b.Sections {
		if !s.Kind.Valid() {
			return fmt.Errorf("section %d: kind must be %s, got %q", i+1, ReportSectionsPhrase(), s.Kind)
		}
	}
	// A panels board with no panels yet is valid, and renders as the page
	// that says how to add one: a dashboard is assembled a row at a time
	// in the state repository, and the board has to exist before a panel
	// can name it.
	for i, p := range b.Panels {
		if !p.Kind.Valid() {
			return fmt.Errorf("panel %d: kind must be %s, got %q", i+1, PanelKindsPhrase(), p.Kind)
		}
		// Empty is the default width rather than an error: a panel row
		// written by hand with no layout in mind is a panel, and PutBoard
		// stores exactly what it was given.
		if p.Width != "" && !p.Width.Valid() {
			return fmt.Errorf("panel %d: width must be %s, got %q", i+1, PanelWidthsPhrase(), p.Width)
		}
		if p.Range != "" {
			ranges := PanelRangesFor(p.Kind)
			if len(ranges) == 0 {
				return fmt.Errorf("panel %d: %q panels do not have a range, got %q", i+1, p.Kind, p.Range)
			}
			if !slices.Contains(ranges, p.Range) {
				return fmt.Errorf("panel %d: range must be %s, got %q", i+1, PanelRangesPhrase(p.Kind), p.Range)
			}
		}
	}
	for _, c := range b.Columns {
		if strings.TrimSpace(c.Name) == "" {
			return fmt.Errorf("a board column needs a name")
		}
		if len(c.States) == 0 {
			return fmt.Errorf("column %q collects no states", c.Name)
		}
		for _, s := range c.States {
			if !ValidState(s) {
				return fmt.Errorf("column %q names unknown state %q", c.Name, s)
			}
		}
	}
	return nil
}

// ValidState reports whether s is one of the states StateOf can produce.
// A board's columns are the first thing here written by somebody other
// than grain -- an agent editing the state repository's dump -- so
// "queued" being a state and "Queued" not being one has to be answerable
// somewhere other than in a UI.
func ValidState(s State) bool {
	switch s {
	case StateProposed, StateQueued, StateRunning, StateAwaitingReply,
		StateFailed, StateAwaitingSubmit, StateCompleted,
		StateDeferred, StateClosed:
		return true
	}
	return false
}
