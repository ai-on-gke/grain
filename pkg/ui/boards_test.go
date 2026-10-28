package ui_test

// Boards (grain/task-48) end to end through the client, against a real
// embedded store -- schedules_test.go's own doc comment gives the
// reasoning for a store rather than a fake.

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

func TestCreateBoardFilesABoard(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)

	board, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name:        "Waiting on me",
		Folder:      "Mine",
		Kind:        "list",
		Description: "Everything parked on a question",
		Query:       "f=state:any:awaiting_reply&sort=created:desc",
	})
	if err != nil {
		t.Fatalf("creating a board: %v", err)
	}
	if board.ID == "" {
		t.Fatal("want a non-empty id")
	}
	if board.Name != "Waiting on me" || board.Folder != "Mine" || board.Kind != "list" {
		t.Errorf("board = %+v, want the fields just given", board)
	}
	// The query is stored exactly as handed over: the UI wrote it and
	// the UI is the only thing that reads it (model.Board's own doc
	// comment), so nothing here may normalise it.
	if board.Query != "f=state:any:awaiting_reply&sort=created:desc" {
		t.Errorf("query = %q, want it kept verbatim", board.Query)
	}

	list, err := c.ListBoards(ctx)
	if err != nil {
		t.Fatalf("listing boards: %v", err)
	}
	if len(list) != 1 || list[0].ID != board.ID {
		t.Fatalf("list = %+v, want the one board just created", list)
	}
}

// A board with no kind is a list: the plainest dashboard there is.
func TestCreateBoardDefaultsToAList(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	board, err := c.CreateBoard(ctx, ui.CreateBoardRequest{Name: "Everything"})
	if err != nil {
		t.Fatalf("creating a board: %v", err)
	}
	if board.Kind != "list" {
		t.Errorf("kind = %q, want %q", board.Kind, "list")
	}
}

func TestCreateBoardKeepsKanbanColumns(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	board, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name: "Flow", Kind: "kanban",
		Columns: []ui.BoardColumn{
			{Name: "Waiting", States: []string{"proposed", "queued"}},
			{Name: "Working", States: []string{"running"}},
		},
	})
	if err != nil {
		t.Fatalf("creating a board: %v", err)
	}
	if len(board.Columns) != 2 {
		t.Fatalf("columns = %+v, want two", board.Columns)
	}
	// Order is the order they were given in, which is the order they are
	// drawn in.
	if board.Columns[0].Name != "Waiting" || len(board.Columns[0].States) != 2 {
		t.Errorf("first column = %+v, want Waiting over two states", board.Columns[0])
	}
	if board.Columns[1].Name != "Working" || board.Columns[1].States[0] != "running" {
		t.Errorf("second column = %+v, want Working over running", board.Columns[1])
	}
}

func TestCreateBoardRejectsABadKind(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateBoard(ctx, ui.CreateBoardRequest{Name: "Odd", Kind: "burndown"})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	// The reply names the kinds there are, since the reader of it has
	// just guessed a word (model.BoardKindsPhrase).
	if !strings.Contains(err.Error(), `"gantt"`) {
		t.Errorf("error = %v, want it to name the kinds this build has", err)
	}
}

// The timeline is a kind like the other two (grain/task-55): the API
// takes one, and what comes back is a board that renders as the chart.
func TestCreateBoardTakesATimeline(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	board, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name: "This quarter", Kind: "gantt", Query: "f=repo:any:acme/widgets",
	})
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if board.Kind != "gantt" {
		t.Errorf("kind = %q, want gantt", board.Kind)
	}
	if board.Query != "f=repo:any:acme/widgets" {
		t.Errorf("query = %q, want the one asked for", board.Query)
	}
}

func TestCreateBoardRejectsAMissingName(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateBoard(ctx, ui.CreateBoardRequest{Kind: "list"})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

// A state nobody has is refused at the door rather than left for a
// browser to fail to draw -- an agent editing the state repository's
// dump is the one writing these.
func TestCreateBoardRejectsAnUnknownState(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name: "Flow", Kind: "kanban",
		Columns: []ui.BoardColumn{{Name: "Odd", States: []string{"Queued"}}},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}

func TestUpdateBoardEditsInPlace(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	board, err := c.CreateBoard(ctx, ui.CreateBoardRequest{Name: "Old", Query: "q=old"})
	if err != nil {
		t.Fatalf("creating a board: %v", err)
	}
	name, query, folder := "New", "q=new", "Reports"
	updated, err := c.UpdateBoard(ctx, board.ID, ui.UpdateBoardRequest{
		Name: &name, Query: &query, Folder: &folder,
	})
	if err != nil {
		t.Fatalf("updating a board: %v", err)
	}
	if updated.Name != "New" || updated.Query != "q=new" || updated.Folder != "Reports" {
		t.Errorf("board = %+v, want the edited fields", updated)
	}
	if updated.ID != board.ID {
		t.Errorf("id = %q, want %q", updated.ID, board.ID)
	}
	// Read back, rather than trusting what the call returned.
	list, err := c.ListBoards(ctx)
	if err != nil {
		t.Fatalf("listing boards: %v", err)
	}
	if len(list) != 1 || list[0].Name != "New" {
		t.Errorf("list = %+v, want the edit to have landed", list)
	}
}

func TestUpdateBoardRejectsAnUnknownBoard(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	name := "x"
	_, err := c.UpdateBoard(ctx, "board-404", ui.UpdateBoardRequest{Name: &name})
	if err == nil {
		t.Fatal("updating a board that does not exist succeeded, want an error")
	}
}

// An unknown board over HTTP is a 404, not the 500 an unmapped error
// falls through to in writeClientError.
func TestUnknownBoardIsNotFoundOverHTTP(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	for _, req := range []struct{ method, body string }{
		{http.MethodPatch, `{"name":"x"}`},
		{http.MethodDelete, ""},
	} {
		if rec := do(t, srv, req.method, "/api/boards/board-404", req.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s /api/boards/board-404: %d, want 404: %s", req.method, rec.Code, rec.Body)
		}
	}
}

// Boards are the deployment's (single-tenant reset, stage 4d): an old
// client's team is ignored, the board is listed for everybody, and the
// wire carries no team.
func TestBoardsAreTheDeployments(t *testing.T) {
	t.Parallel()
	srv, _ := testServer(t)
	rec := do(t, srv, http.MethodPost, "/api/boards", `{"name":"Platform Board","team":"platform"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/boards with a team: %d, want 201: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"team"`) {
		t.Errorf("POST /api/boards answered %s, want no team on a board", rec.Body)
	}
	rec = do(t, srv, http.MethodGet, "/api/boards", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Platform Board") {
		t.Errorf("GET /api/boards: %d %s, want the board listed", rec.Code, rec.Body)
	}
}

func TestDeleteBoardRemovesItAndItsColumns(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	board, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name: "Flow", Kind: "kanban",
		Columns: []ui.BoardColumn{{Name: "Working", States: []string{"running"}}},
	})
	if err != nil {
		t.Fatalf("creating a board: %v", err)
	}
	if err := c.DeleteBoard(ctx, board.ID); err != nil {
		t.Fatalf("deleting a board: %v", err)
	}
	list, err := c.ListBoards(ctx)
	if err != nil {
		t.Fatalf("listing boards: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("list = %+v, want nothing left", list)
	}
	if err := c.DeleteBoard(ctx, board.ID); err == nil {
		t.Fatal("deleting the same board twice succeeded, want an error")
	}
}

// Boards come back in the order the nav rail draws them -- by folder,
// then by each board's own place within it -- rather than in whatever
// order they were created.
func TestListBoardsOrdersByFolderThenPlace(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	second := 2.0
	first := 1.0
	if _, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name: "Second", Folder: "Reports", OrderKey: &second,
	}); err != nil {
		t.Fatalf("creating a board: %v", err)
	}
	if _, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name: "First", Folder: "Reports", OrderKey: &first,
	}); err != nil {
		t.Fatalf("creating a board: %v", err)
	}
	if _, err := c.CreateBoard(ctx, ui.CreateBoardRequest{Name: "Loose"}); err != nil {
		t.Fatalf("creating a board: %v", err)
	}
	list, err := c.ListBoards(ctx)
	if err != nil {
		t.Fatalf("listing boards: %v", err)
	}
	var names []string
	for _, b := range list {
		names = append(names, b.Name)
	}
	want := []string{"Loose", "First", "Second"}
	for i := range want {
		if i >= len(names) || names[i] != want[i] {
			t.Fatalf("order = %v, want %v", names, want)
		}
	}
}

// Boards nobody has ordered come back in the order they were filed,
// past the ninth as well: compared as text, board-10 sorted above
// board-2 and a fresh folder of boards came back shuffled (2026-10-12
// boards walkthrough).
func TestListBoardsKeepsUnorderedBoardsInFilingOrder(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	var want []string
	for i := 1; i <= 12; i++ {
		name := fmt.Sprintf("Filed %d", i)
		if _, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
			Name: name, Folder: "Reports",
		}); err != nil {
			t.Fatalf("creating a board: %v", err)
		}
		want = append(want, name)
	}
	list, err := c.ListBoards(ctx)
	if err != nil {
		t.Fatalf("listing boards: %v", err)
	}
	var names []string
	for _, b := range list {
		if b.Folder == "Reports" {
			names = append(names, b.Name)
		}
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("order = %v, want %v", names, want)
	}
}

// A dashboard of panels (grain/task-88): the panels go in with the
// board, come back in display order, and survive the round trip through
// the two child rows a board can now carry.
func TestCreateBoardKeepsPanels(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	board, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name: "Operations", Kind: "panels", Query: "f=repo:any:acme/widgets",
		Panels: []ui.BoardPanel{
			{Kind: "states", Title: "Where everything is", Width: "half"},
			{Kind: "gantt", Range: "7d", Width: "full"},
			{Kind: "list", Title: "Waiting on me", Query: "f=state:any:awaiting_reply"},
		},
	})
	if err != nil {
		t.Fatalf("creating a board: %v", err)
	}
	if len(board.Panels) != 3 {
		t.Fatalf("panels = %+v, want three", board.Panels)
	}
	if board.Panels[0].Kind != "states" || board.Panels[0].Title != "Where everything is" {
		t.Errorf("first panel = %+v, want the state counts", board.Panels[0])
	}
	if board.Panels[1].Range != "7d" || board.Panels[1].Width != "full" {
		t.Errorf("second panel = %+v, want a full-width 7d timeline", board.Panels[1])
	}
	// A panel's own query is kept verbatim, the board's own is left
	// where it is: the two are different questions and neither is
	// combined with the other here (model.BoardPanel's doc comment).
	if board.Panels[2].Query != "f=state:any:awaiting_reply" {
		t.Errorf("third panel query = %q, want it kept verbatim", board.Panels[2].Query)
	}

	list, err := c.ListBoards(ctx)
	if err != nil {
		t.Fatalf("listing boards: %v", err)
	}
	if len(list) != 1 || len(list[0].Panels) != 3 {
		t.Fatalf("list = %+v, want the board with its three panels", list)
	}
	if list[0].Panels[0].Kind != "states" || list[0].Panels[2].Kind != "list" {
		t.Errorf("panels = %+v, want them in the order they were given", list[0].Panels)
	}
}

// Adding a panel is the whole gesture the dashboard exists for, and it
// is an update to the board: the panels sent replace the ones stored,
// the way a Kanban board's columns do.
func TestUpdateBoardReplacesPanels(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	board, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name: "Operations", Kind: "panels",
		Panels: []ui.BoardPanel{{Kind: "states"}},
	})
	if err != nil {
		t.Fatalf("creating a board: %v", err)
	}
	panels := []ui.BoardPanel{{Kind: "states"}, {Kind: "metrics", Range: "30d"}}
	updated, err := c.UpdateBoard(ctx, board.ID, ui.UpdateBoardRequest{Panels: &panels})
	if err != nil {
		t.Fatalf("updating a board: %v", err)
	}
	if len(updated.Panels) != 2 || updated.Panels[1].Kind != "metrics" {
		t.Errorf("panels = %+v, want the two just sent", updated.Panels)
	}
}

func TestCreateBoardRejectsABadPanelRange(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name: "Operations", Kind: "panels",
		Panels: []ui.BoardPanel{{Kind: "gantt", Range: "since-tuesday"}},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if !strings.Contains(err.Error(), `"24h"`) {
		t.Errorf("error = %v, want it to name the ranges this panel accepts", err)
	}
}

func TestUpdateBoardValidatesRangesOnlyWhenPanelsAreSupplied(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	legacy := model.Board{
		ID: "board-legacy", Name: "Legacy", Kind: model.BoardPanels,
		Panels: []model.BoardPanel{{Kind: model.PanelGantt, Range: "since-tuesday"}},
	}
	if err := store.PutBoard(ctx, legacy); err != nil {
		t.Fatalf("seeding a board with a legacy range: %v", err)
	}

	name := "Renamed"
	updated, err := c.UpdateBoard(ctx, legacy.ID, ui.UpdateBoardRequest{Name: &name})
	if err != nil {
		t.Fatalf("renaming a board with a legacy range: %v", err)
	}
	if updated.Name != name || updated.Panels[0].Range != "since-tuesday" {
		t.Errorf("updated board = %+v, want the rename and untouched legacy range", updated)
	}

	panels := []ui.BoardPanel{{Kind: "gantt", Range: "since-tuesday"}}
	_, err = c.UpdateBoard(ctx, legacy.ID, ui.UpdateBoardRequest{Panels: &panels})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("panel update error = %v, want a ValidationError", err)
	}
}

func TestCreateBoardRejectsABadPanelKind(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name: "Operations", Kind: "panels",
		Panels: []ui.BoardPanel{{Kind: "burndown"}},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	// Same reply a bad board kind gets: the words there are, for
	// somebody who has just guessed one (model.PanelKindsPhrase).
	if !strings.Contains(err.Error(), `"states"`) {
		t.Errorf("error = %v, want it to name the panel kinds this build has", err)
	}
}

// Deleting a dashboard deletes its panels too -- they carry no foreign
// key onto it, so nothing else would. What this can check from out here
// is that the delete runs and leaves nothing listed; the row-level
// cascade is board_store.go's, next to the DELETE that does it.
func TestDeleteBoardWithPanels(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	board, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name: "Operations", Kind: "panels",
		Panels: []ui.BoardPanel{{Kind: "states"}, {Kind: "metrics"}},
	})
	if err != nil {
		t.Fatalf("creating a board: %v", err)
	}
	if err := c.DeleteBoard(ctx, board.ID); err != nil {
		t.Fatalf("deleting a board: %v", err)
	}
	list, err := c.ListBoards(ctx)
	if err != nil {
		t.Fatalf("listing boards: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("list = %+v, want nothing left", list)
	}
}

// A report board (grain/task-83): the API takes one, its sections make
// the round trip in print order, and they come back off the store rather
// than out of the request -- which is what a board an agent adds by
// editing tables/board_section.json depends on.
func TestCreateBoardTakesAReportBoardWithSections(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	board, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name: "How payments is going", Kind: "report",
		Query: "f=repo:any:acme/widgets&f=created:within:30",
		Sections: []ui.BoardSection{
			{Kind: "throughput", Title: "Last thirty days"},
			{Kind: "latency", OrderKey: 1},
		},
	})
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if board.Kind != "report" {
		t.Errorf("kind = %q, want report", board.Kind)
	}
	stored, err := c.ListBoards(ctx)
	if err != nil || len(stored) != 1 {
		t.Fatalf("ListBoards: %v (%d boards)", err, len(stored))
	}
	if len(stored[0].Sections) != 2 {
		t.Fatalf("sections = %+v, want two", stored[0].Sections)
	}
	if stored[0].Sections[0].Kind != "throughput" || stored[0].Sections[0].Title != "Last thirty days" {
		t.Errorf("first section = %+v, want the throughput one with its own heading", stored[0].Sections[0])
	}
	if stored[0].Sections[1].Kind != "latency" {
		t.Errorf("second section = %+v, want latency", stored[0].Sections[1])
	}
}

// No sections is the whole report, not an unrenderable board -- so the
// plainest thing an agent can be asked for ("a report board for
// acme/widgets") is a board with no child rows at all.
func TestCreateBoardTakesAReportBoardWithNoSections(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	board, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name: "Everything, measured", Kind: "report",
	})
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if len(board.Sections) != 0 {
		t.Errorf("sections = %+v, want none", board.Sections)
	}
}

func TestCreateBoardRejectsAnUnknownSection(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name: "Odd", Kind: "report",
		Sections: []ui.BoardSection{{Kind: "burndown"}},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if !strings.Contains(err.Error(), `"throughput"`) {
		t.Errorf("error = %v, want it to name the sections there are", err)
	}
}

// Sections belong to a report board and to nothing else, exactly as
// columns belong to a Kanban one: a board carrying rows nothing will
// draw is one somebody has misunderstood.
func TestCreateBoardRejectsSectionsOnAList(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	_, err := c.CreateBoard(ctx, ui.CreateBoardRequest{
		Name: "Everything", Kind: "list",
		Sections: []ui.BoardSection{{Kind: "throughput"}},
	})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
}
