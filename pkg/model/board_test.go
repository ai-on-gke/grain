package model_test

// Board.Validate (grain/task-48) -- a pure reduction over values, so it
// is pinned here rather than only through the API that calls it.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

func kanbanBoard() model.Board {
	return model.Board{
		ID: "board-1", Name: "Flow", Kind: model.BoardKanban,
		Columns: []model.BoardColumn{
			{Name: "Working", States: []model.State{model.StateRunning}},
		},
	}
}

func TestBoardKindValid(t *testing.T) {
	for _, k := range []model.BoardKind{model.BoardList, model.BoardKanban, model.BoardGantt, model.BoardPanels} {
		if !k.Valid() {
			t.Errorf("%q.Valid() = false, want true", k)
		}
	}
	for _, k := range []model.BoardKind{"", "List", "table", "burndown"} {
		if k.Valid() {
			t.Errorf("%q.Valid() = true, want false", k)
		}
	}
}

// The kinds an error message quotes back are the kinds Valid accepts,
// which is the point of there being one list: a build that grows a kind
// should not go on telling people it has two.
func TestBoardKindsPhraseNamesEveryKind(t *testing.T) {
	phrase := model.BoardKindsPhrase()
	for _, k := range model.BoardKinds {
		if !strings.Contains(phrase, strconv.Quote(string(k))) {
			t.Errorf("BoardKindsPhrase() = %q, does not name %q", phrase, k)
		}
	}
	if want := `"list", "kanban", "gantt", "panels", "report" or "tree"`; phrase != want {
		t.Errorf("BoardKindsPhrase() = %q, want %q", phrase, want)
	}
}

func TestBoardValidateAcceptsEveryKind(t *testing.T) {
	if err := kanbanBoard().Validate(); err != nil {
		t.Errorf("kanban board: %v", err)
	}
	list := model.Board{ID: "board-2", Name: "Everything", Kind: model.BoardList}
	if err := list.Validate(); err != nil {
		t.Errorf("list board: %v", err)
	}
	// A timeline board (grain/task-55) carries nothing a list does not:
	// its range and its grouping are the reader's browser setting, so
	// there is no extra field here for one to be invalid in.
	gantt := model.Board{ID: "board-3", Name: "This quarter", Kind: model.BoardGantt}
	if err := gantt.Validate(); err != nil {
		t.Errorf("gantt board: %v", err)
	}
}

// Columns belong to a Kanban board and to nothing else -- a timeline has
// no columns to honour, so a row carrying them is a row somebody has
// misunderstood rather than one to render half of.
func TestBoardValidateRefusesColumnsOnATimeline(t *testing.T) {
	b := kanbanBoard()
	b.Kind = model.BoardGantt
	if err := b.Validate(); err == nil {
		t.Error("gantt board with columns: want an error, got none")
	}
}

// A board with no query at all is a board over every task, not an
// invalid one: the whole point of the plainest dashboard is that it
// narrows nothing.
func TestBoardValidateAcceptsAnEmptyQuery(t *testing.T) {
	b := kanbanBoard()
	b.Query = ""
	if err := b.Validate(); err != nil {
		t.Errorf("board with no query: %v", err)
	}
}

// ...and a query naming an attribute this build has never heard of is
// still valid here, because what attributes exist is the deployment's
// configuration and this package has never seen it (Board's own doc
// comment).
func TestBoardValidateLeavesTheQueryAlone(t *testing.T) {
	b := kanbanBoard()
	b.Query = "f=custom.squad:any:platform&sort=nonsense:desc"
	if err := b.Validate(); err != nil {
		t.Errorf("board with an unfamiliar query: %v", err)
	}
}

func TestBoardValidateRejectsAnEmptyName(t *testing.T) {
	b := kanbanBoard()
	b.Name = "  "
	if err := b.Validate(); err == nil {
		t.Fatal("a nameless board validated, want an error")
	}
}

func TestBoardValidateRejectsAnUnknownKind(t *testing.T) {
	b := kanbanBoard()
	b.Columns = nil
	b.Kind = "burndown"
	if err := b.Validate(); err == nil {
		t.Fatal("a board of an unknown kind validated, want an error")
	}
}

// Columns belong to a Kanban board and to nothing else: a list carrying
// them is a board somebody built wrong, and saying so is cheaper than
// rendering columns nobody can see.
func TestBoardValidateRejectsColumnsOnAList(t *testing.T) {
	b := kanbanBoard()
	b.Kind = model.BoardList
	if err := b.Validate(); err == nil {
		t.Fatal("a list with columns validated, want an error")
	}
}

func TestBoardValidateRejectsAColumnWithNoStates(t *testing.T) {
	b := kanbanBoard()
	b.Columns[0].States = nil
	if err := b.Validate(); err == nil {
		t.Fatal("a column collecting nothing validated, want an error")
	}
}

func TestBoardValidateRejectsAnUnknownState(t *testing.T) {
	b := kanbanBoard()
	b.Columns[0].States = []model.State{"Queued"}
	if err := b.Validate(); err == nil {
		t.Fatal("a column naming a state that does not exist validated, want an error")
	}
}

// Every state StateOf can produce is one a column may collect: a
// vocabulary this rejected half of would quietly make some tasks
// unreachable on a board.
func TestValidStateCoversEveryState(t *testing.T) {
	for _, s := range []model.State{
		model.StateProposed, model.StateQueued, model.StateRunning,
		model.StateAwaitingReply, model.StateFailed, model.StateAwaitingSubmit,
		model.StateCompleted, model.StateDeferred,
		model.StateClosed,
	} {
		if !model.ValidState(s) {
			t.Errorf("ValidState(%q) = false, want true", s)
		}
	}
	for _, s := range []model.State{"", "Queued", "in_progress"} {
		if model.ValidState(s) {
			t.Errorf("ValidState(%q) = true, want false", s)
		}
	}
}

// Panels (grain/task-88) -- the composed dashboard's own rules. Same
// treatment as a column: what a panel is narrowed to is left alone, and
// the two closed vocabularies it does carry are checked here so that a
// row hand-written into the state repository is refused by the API that
// writes it rather than by a browser that cannot draw it.

func panelBoard() model.Board {
	return model.Board{
		ID: "board-4", Name: "Operations", Kind: model.BoardPanels,
		Panels: []model.BoardPanel{
			{Kind: model.PanelStates, Title: "Where everything is", Width: model.PanelHalf},
			{Kind: model.PanelGantt, Range: "7d", Width: model.PanelFull, OrderKey: 1},
		},
	}
}

func TestBoardValidateAcceptsAPanelsBoard(t *testing.T) {
	if err := panelBoard().Validate(); err != nil {
		t.Errorf("panels board: %v", err)
	}
}

// A dashboard is assembled a row at a time, so the board exists before
// its first panel does -- and an empty one renders the page that says
// how to add one rather than failing to load.
func TestBoardValidateAcceptsAPanelsBoardWithNoPanels(t *testing.T) {
	b := panelBoard()
	b.Panels = nil
	if err := b.Validate(); err != nil {
		t.Errorf("panels board with no panels: %v", err)
	}
}

// Panels belong to a panels board, columns to a Kanban one: neither
// kind honours the other's rows, and a board carrying rows nothing will
// draw is one somebody has misunderstood.
func TestBoardValidateRejectsPanelsOnAList(t *testing.T) {
	b := panelBoard()
	b.Kind = model.BoardList
	if err := b.Validate(); err == nil {
		t.Fatal("a list with panels validated, want an error")
	}
}

func TestBoardValidateRejectsAnUnknownPanelKind(t *testing.T) {
	b := panelBoard()
	b.Panels[0].Kind = "burndown"
	if err := b.Validate(); err == nil {
		t.Fatal("a panel of an unknown kind validated, want an error")
	}
}

func TestBoardValidateRejectsAnUnknownPanelWidth(t *testing.T) {
	b := panelBoard()
	b.Panels[0].Width = "wide"
	if err := b.Validate(); err == nil {
		t.Fatal("a panel of an unknown width validated, want an error")
	}
}

// An unset width is the default one, not an invalid one: a panel row
// written without a layout in mind is still a panel.
func TestBoardValidateAcceptsAPanelWithNoWidth(t *testing.T) {
	b := panelBoard()
	b.Panels[0].Width = ""
	if err := b.Validate(); err != nil {
		t.Errorf("panel with no width: %v", err)
	}
}

func TestBoardValidateRejectsAnUnknownPanelRange(t *testing.T) {
	b := panelBoard()
	b.Panels[1].Range = "since-tuesday"
	if err := b.Validate(); err == nil {
		t.Fatal("a panel with an unknown range validated, want an error")
	}
}

func TestBoardValidateRejectsARangeThePanelKindCannotUse(t *testing.T) {
	b := panelBoard()
	b.Panels[0].Range = "7d"
	if err := b.Validate(); err == nil {
		t.Fatal("a states panel with a range validated, want an error")
	}
}

// The kinds and widths an error quotes back are the ones Valid accepts
// -- BoardKindsPhrase's own reason for existing, applied to the two
// vocabularies a panel adds.
func TestPanelPhrasesNameEveryValue(t *testing.T) {
	kinds := model.PanelKindsPhrase()
	for _, k := range model.PanelKinds {
		if !strings.Contains(kinds, strconv.Quote(string(k))) {
			t.Errorf("PanelKindsPhrase() = %q, does not name %q", kinds, k)
		}
	}
	if want := `"list", "gantt", "states" or "metrics"`; kinds != want {
		t.Errorf("PanelKindsPhrase() = %q, want %q", kinds, want)
	}
	widths := model.PanelWidthsPhrase()
	for _, w := range model.PanelWidths {
		if !strings.Contains(widths, strconv.Quote(string(w))) {
			t.Errorf("PanelWidthsPhrase() = %q, does not name %q", widths, w)
		}
	}
}

// Sections (grain/task-83) -- the report board's own rows, and the third
// instance of the same rule: a setting belonging to one kind of board is
// child rows, checked here so that a hand-written one is refused by the
// API that writes it rather than by a browser that cannot draw it.

func reportBoard() model.Board {
	return model.Board{
		ID: "board-5", Name: "How payments is going", Kind: model.BoardReport,
		Query: "f=repo:any:acme/widgets&f=created:within:30",
		Sections: []model.BoardSection{
			{Kind: model.SectionThroughput, Title: "Last thirty days"},
			{Kind: model.SectionLatency, OrderKey: 1},
		},
	}
}

func TestBoardValidateAcceptsAReportBoard(t *testing.T) {
	if err := reportBoard().Validate(); err != nil {
		t.Errorf("report board: %v", err)
	}
}

// A report board with no sections is the whole report rather than an
// empty page -- the fallback a Kanban board with no columns gets, for
// the same reason: an agent asked for "a report board for acme/widgets"
// should not have to choose a layout to have made one.
func TestBoardValidateAcceptsAReportBoardWithNoSections(t *testing.T) {
	b := reportBoard()
	b.Sections = nil
	if err := b.Validate(); err != nil {
		t.Errorf("report board with no sections: %v", err)
	}
}

func TestBoardValidateRejectsSectionsOnAList(t *testing.T) {
	b := reportBoard()
	b.Kind = model.BoardList
	if err := b.Validate(); err == nil {
		t.Fatal("a list with report sections validated, want an error")
	}
}

func TestBoardValidateRejectsAnUnknownSection(t *testing.T) {
	b := reportBoard()
	b.Sections[0].Kind = "burndown"
	if err := b.Validate(); err == nil {
		t.Fatal("a section of an unknown kind validated, want an error")
	}
}

// A report board's query is left exactly as alone as every other kind's
// (Board's own doc comment). What a report can honestly narrow by is a
// question for the page that draws one, not for this package: a query
// naming a state or a text search is a query with nothing in it for a
// distribution, which is a weaker reading rather than an invalid board.
func TestBoardValidateLeavesAReportQueryAlone(t *testing.T) {
	b := reportBoard()
	b.Query = "q=flaky&f=state:any:running&sort=created:desc"
	if err := b.Validate(); err != nil {
		t.Errorf("report board with a query a report cannot fully use: %v", err)
	}
}

func TestReportSectionsPhraseNamesEverySection(t *testing.T) {
	phrase := model.ReportSectionsPhrase()
	for _, s := range model.ReportSections {
		if !strings.Contains(phrase, strconv.Quote(string(s))) {
			t.Errorf("ReportSectionsPhrase() = %q, does not name %q", phrase, s)
		}
	}
	for _, s := range model.ReportSections {
		if !s.Valid() {
			t.Errorf("ReportSection(%q).Valid() = false, want true", s)
		}
	}
	if model.ReportSection("burndown").Valid() {
		t.Error(`ReportSection("burndown").Valid() = true, want false`)
	}
}
