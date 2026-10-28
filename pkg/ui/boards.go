package ui

import (
	"context"
	"net/http"
	"strings"

	"github.com/bwsalmon/grain/pkg/model"
)

// boardNotFoundError marks a board ID with no board behind it --
// suiteNotFoundError's own reasoning, so the message a caller sees names
// a board rather than a task.
type boardNotFoundError struct{ ID string }

func (e *boardNotFoundError) Error() string { return "no board " + e.ID }

// BoardColumn is one column of a Kanban board over the wire.
type BoardColumn struct {
	Name   string   `json:"name"`
	States []string `json:"states"`
}

// BoardPanel is one panel of a "panels" board over the wire
// (model.BoardPanel, grain/task-88). Query, range and width are the
// panel's own; empty means the default the browser applies, which is
// where the vocabulary for all three lives.
type BoardPanel struct {
	Kind     string  `json:"kind"`
	Title    string  `json:"title,omitempty"`
	Query    string  `json:"query,omitempty"`
	Range    string  `json:"range,omitempty"`
	Width    string  `json:"width,omitempty"`
	OrderKey float64 `json:"orderKey"`
}

// BoardSection is one section of a "report" board over the wire
// (model.BoardSection, grain/task-83). Title empty is the section's own
// name, and a board with no sections at all is the whole report -- the
// vocabulary and both fallbacks live in the browser, like a panel's.
type BoardSection struct {
	Kind     string  `json:"kind"`
	Title    string  `json:"title,omitempty"`
	OrderKey float64 `json:"orderKey"`
}

// Board is a saved dashboard's JSON shape (model.Board, grain/task-48).
//
// Query is the filtering and sorting in the encoding the address bar
// carries ("q=ci&sort=created:desc&f=state:any:running") -- see
// model.Board's own doc comment for why a board's question is stored the
// same way a link to one is written. Nothing on this side reads it; it
// is handed to the browser as it stands.
type Board struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Folder      string         `json:"folder,omitempty"`
	Kind        string         `json:"kind"`
	Description string         `json:"description,omitempty"`
	Query       string         `json:"query,omitempty"`
	Columns     []BoardColumn  `json:"columns,omitempty"`
	Panels      []BoardPanel   `json:"panels,omitempty"`
	Sections    []BoardSection `json:"sections,omitempty"`
	OrderKey    float64        `json:"orderKey"`
}

func boardFrom(b model.Board) Board {
	out := Board{
		ID:          b.ID,
		Name:        b.Name,
		Folder:      b.Folder,
		Kind:        string(b.Kind),
		Description: b.Description,
		Query:       b.Query,
		OrderKey:    b.OrderKey,
	}
	for _, c := range b.Columns {
		states := make([]string, 0, len(c.States))
		for _, s := range c.States {
			states = append(states, string(s))
		}
		out.Columns = append(out.Columns, BoardColumn{Name: c.Name, States: states})
	}
	for _, p := range b.Panels {
		out.Panels = append(out.Panels, BoardPanel{
			Kind:     string(p.Kind),
			Title:    p.Title,
			Query:    p.Query,
			Range:    p.Range,
			Width:    string(p.Width),
			OrderKey: p.OrderKey,
		})
	}
	for _, sec := range b.Sections {
		out.Sections = append(out.Sections, BoardSection{
			Kind:     string(sec.Kind),
			Title:    sec.Title,
			OrderKey: sec.OrderKey,
		})
	}
	return out
}

func parseBoardSections(in []BoardSection) []model.BoardSection {
	out := make([]model.BoardSection, 0, len(in))
	for _, s := range in {
		out = append(out, model.BoardSection{
			Kind:     model.ReportSection(strings.TrimSpace(s.Kind)),
			Title:    strings.TrimSpace(s.Title),
			OrderKey: s.OrderKey,
		})
	}
	return out
}

func parseBoardPanels(in []BoardPanel) []model.BoardPanel {
	out := make([]model.BoardPanel, 0, len(in))
	for _, p := range in {
		out = append(out, model.BoardPanel{
			Kind:     model.PanelKind(strings.TrimSpace(p.Kind)),
			Title:    strings.TrimSpace(p.Title),
			Query:    strings.TrimSpace(p.Query),
			Range:    strings.TrimSpace(p.Range),
			Width:    model.PanelWidth(strings.TrimSpace(p.Width)),
			OrderKey: p.OrderKey,
		})
	}
	return out
}

func parseBoardColumns(in []BoardColumn) []model.BoardColumn {
	out := make([]model.BoardColumn, 0, len(in))
	for _, c := range in {
		col := model.BoardColumn{Name: strings.TrimSpace(c.Name)}
		for _, s := range c.States {
			col.States = append(col.States, model.State(strings.TrimSpace(s)))
		}
		out = append(out, col)
	}
	return out
}

// ListBoards returns every board, in the order the nav rail shows them
// (by folder, then within it). Boards are the deployment's, so there is
// one list for everybody (single-tenant reset, stage 4d).
func (c *Client) ListBoards(ctx context.Context) ([]Board, error) {
	list, err := c.Store.ListBoards(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Board, 0, len(list))
	for _, b := range list {
		out = append(out, boardFrom(b))
	}
	return out, nil
}

// CreateBoardRequest is a new board's fields. Kind, omitted, is a list:
// the plainest dashboard there is, and the one a caller who did not say
// meant. An old client's `team` is ignored.
type CreateBoardRequest struct {
	Name        string         `json:"name"`
	Folder      string         `json:"folder"`
	Kind        string         `json:"kind"`
	Description string         `json:"description"`
	Query       string         `json:"query"`
	Columns     []BoardColumn  `json:"columns"`
	Panels      []BoardPanel   `json:"panels"`
	Sections    []BoardSection `json:"sections"`
	OrderKey    *float64       `json:"orderKey"`
}

// CreateBoard files a new board straight into the store.
//
// A board created here is exported to the state repository within
// seconds like every other setting, so this endpoint and a merged pull
// request against tables/board.json are two ways in to the same row --
// which is the point: the UI writes one by hand, an agent writes one by
// editing the dump, and neither has to know about the other.
func (c *Client) CreateBoard(ctx context.Context, req CreateBoardRequest) (Board, error) {
	if err := c.checkSettingsWritable(ctx, "board"); err != nil {
		return Board{}, err
	}
	if strings.TrimSpace(req.Name) == "" {
		return Board{}, validationErrorf("name is required")
	}
	kind := model.BoardKind(strings.TrimSpace(req.Kind))
	if kind == "" {
		kind = model.BoardList
	}
	if !kind.Valid() {
		return Board{}, validationErrorf("kind must be %s, got %q", model.BoardKindsPhrase(), req.Kind)
	}
	id, err := c.Store.NewBoardID(ctx)
	if err != nil {
		return Board{}, err
	}
	board := model.Board{
		ID:          id,
		Name:        strings.TrimSpace(req.Name),
		Folder:      strings.TrimSpace(req.Folder),
		Kind:        kind,
		Description: strings.TrimSpace(req.Description),
		Query:       strings.TrimSpace(req.Query),
		Columns:     parseBoardColumns(req.Columns),
		Panels:      parseBoardPanels(req.Panels),
		Sections:    parseBoardSections(req.Sections),
	}
	if req.OrderKey != nil {
		board.OrderKey = *req.OrderKey
	}
	if err := board.Validate(); err != nil {
		return Board{}, &ValidationError{err: err}
	}
	if err := c.checkSettingsWritable(ctx, "board"); err != nil {
		return Board{}, err
	}
	if err := c.Store.PutBoard(ctx, board); err != nil {
		return Board{}, err
	}
	return boardFrom(board), nil
}

// UpdateBoardRequest is a board's editable fields -- nil means "leave
// this one alone", UpdateSuiteRequest's own convention. An old client's
// `team` is ignored.
type UpdateBoardRequest struct {
	Name        *string         `json:"name,omitempty"`
	Folder      *string         `json:"folder,omitempty"`
	Kind        *string         `json:"kind,omitempty"`
	Description *string         `json:"description,omitempty"`
	Query       *string         `json:"query,omitempty"`
	Columns     *[]BoardColumn  `json:"columns,omitempty"`
	Panels      *[]BoardPanel   `json:"panels,omitempty"`
	Sections    *[]BoardSection `json:"sections,omitempty"`
	OrderKey    *float64        `json:"orderKey,omitempty"`
}

// UpdateBoard edits a board's fields in place.
func (c *Client) UpdateBoard(ctx context.Context, id string, req UpdateBoardRequest) (Board, error) {
	if err := c.checkSettingsWritable(ctx, "board"); err != nil {
		return Board{}, err
	}
	existing, err := c.Store.GetBoard(ctx, id)
	if err != nil {
		return Board{}, err
	}
	if existing == nil {
		return Board{}, &boardNotFoundError{ID: id}
	}
	// Merged and validated before anything is written, for the reason
	// UpdateSuite spells out: the store's own read-modify-write closure
	// may run more than once, and model.Board.Validate's error is a
	// plain error rather than this package's ValidationError.
	merged := *existing
	if req.Name != nil {
		if strings.TrimSpace(*req.Name) == "" {
			return Board{}, validationErrorf("name cannot be empty")
		}
		merged.Name = strings.TrimSpace(*req.Name)
	}
	if req.Folder != nil {
		merged.Folder = strings.TrimSpace(*req.Folder)
	}
	if req.Kind != nil {
		kind := model.BoardKind(strings.TrimSpace(*req.Kind))
		if !kind.Valid() {
			return Board{}, validationErrorf("kind must be %s, got %q", model.BoardKindsPhrase(), *req.Kind)
		}
		merged.Kind = kind
	}
	if req.Description != nil {
		merged.Description = strings.TrimSpace(*req.Description)
	}
	if req.Query != nil {
		merged.Query = strings.TrimSpace(*req.Query)
	}
	if req.Columns != nil {
		merged.Columns = parseBoardColumns(*req.Columns)
	}
	if req.Panels != nil {
		merged.Panels = parseBoardPanels(*req.Panels)
	}
	if req.Sections != nil {
		merged.Sections = parseBoardSections(*req.Sections)
	}
	if req.OrderKey != nil {
		merged.OrderKey = *req.OrderKey
	}
	// Panels are one atomic PATCH field. Validate ranges when the caller
	// supplies that field, but do not let an unfamiliar range already in
	// the store prevent an unrelated edit to the board.
	validate := merged
	if req.Panels == nil {
		validate.Panels = append([]model.BoardPanel(nil), merged.Panels...)
		for i := range validate.Panels {
			validate.Panels[i].Range = ""
		}
	}
	if err := validate.Validate(); err != nil {
		return Board{}, &ValidationError{err: err}
	}
	if err := c.checkSettingsWritable(ctx, "board"); err != nil {
		return Board{}, err
	}
	if err := c.Store.UpdateBoard(ctx, id, func(b *model.Board) error {
		*b = merged
		return nil
	}); err != nil {
		return Board{}, err
	}
	return boardFrom(merged), nil
}

// DeleteBoard removes a board outright. Nothing refers to a board and no
// run reads one, so there is nothing to check first.
func (c *Client) DeleteBoard(ctx context.Context, id string) error {
	if err := c.checkSettingsWritable(ctx, "board"); err != nil {
		return err
	}
	existing, err := c.Store.GetBoard(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return &boardNotFoundError{ID: id}
	}
	if err := c.checkSettingsWritable(ctx, "board"); err != nil {
		return err
	}
	return c.Store.DeleteBoard(ctx, id)
}

func (s *Server) handleListBoards(w http.ResponseWriter, r *http.Request) {
	list, err := s.tasks.ListBoards(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateBoard(w http.ResponseWriter, r *http.Request) {
	var req CreateBoardRequest
	if !readJSON(w, r, &req) {
		return
	}
	board, err := s.tasks.CreateBoard(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, board)
}

func (s *Server) handleUpdateBoard(w http.ResponseWriter, r *http.Request) {
	var req UpdateBoardRequest
	if !readJSON(w, r, &req) {
		return
	}
	board, err := s.tasks.UpdateBoard(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, board)
}

func (s *Server) handleDeleteBoard(w http.ResponseWriter, r *http.Request) {
	if err := s.tasks.DeleteBoard(r.Context(), r.PathValue("id")); err != nil {
		writeClientError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
