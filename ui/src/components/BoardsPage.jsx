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

import { Box, Button, Chip, Typography } from "@mui/material";
import { boardKindLabel } from "../boards.js";
import BoardList from "./BoardList.jsx";
import ItemGlyph from "./ItemGlyph.jsx";
import {
  focusRowLink,
  ListEmpty,
  ListHeader,
  RowLink,
} from "./ListPrimitives.jsx";

// BoardsPage is the list of saved dashboards: what boards this
// deployment has, filed under their folders, and -- the part this page
// exists for -- how to get another one (grain/task-48).
//
// # Why the button files a task instead of opening a form
//
// A board is a row in grain's database, exported to the state
// repository, and the thing usually asked to build one is an agent
// (boards.js explains the whole of that). So "New board" opens the
// new-task form with the task already written: which repository, what to
// add, and the row shape to add it in. What a form here would save is
// the typing; what it would cost is a second way of writing a board that
// has to go on agreeing with the one an agent uses.
//
// The empty state is the same button said louder, because a deployment
// with no boards is where somebody has to learn that boards exist at
// all.
//
// # Except for where a board sits
//
// Which folder a board is filed under and where it comes in that folder
// are dragged here, and written straight through PATCH /api/boards/{id}
// (boardOrder.js says why that one field is not a task like the rest of
// them). This page carries the handle the rail does not: the rail is
// drag-only, and a handle is how the same move is made with a finger or
// from the keyboard -- so on a phone, where no drag exists at all, this
// page is the only place a board can be moved.
// The row is .template-row's own shape (style.css): these pages agreed
// on one row a while ago, and a board's row carries exactly what a
// template's does -- a name, a chip, and a line of hint text saying what
// it is. Plus the handle, in the column every other reorderable list on
// these pages puts it in.
//
// A component of its own out here rather than one declared inside the
// page's render, which React would treat as a new type -- and so tear
// down and rebuild -- on every render, dragging one of these rows
// included.
function Row({ board, handle, dragging, picked, onOpen }) {
  return (
    <div
      className={`template-row${dragging ? " task-row-dragging" : ""}${
        picked ? " task-row-picked" : ""
      }`}
      onClick={(e) => {
        focusRowLink(e);
        onOpen();
      }}
    >
      {handle}
      <RowLink
        className="template-name"
        href={`/boards/${encodeURIComponent(board.id)}`}
        onOpen={onOpen}
      >
        {board.name}
      </RowLink>
      {/* One chip, in the strip the other .template-row shapes put
          theirs in (grain/task-294) -- a board's kind is short enough
          that nothing here is ever dropped, and the strip is what makes
          "chips on this row live in .chips" true without exceptions. */}
      <span className="chips">
        <Chip size="small" label={boardKindLabel(board.kind)} />
      </span>
      <span className="template-title hint">
        {board.description || (board.query ? board.query : "every task")}
      </span>
    </div>
  );
}

export default function BoardsPage({
  boards,
  onOpenBoard,
  onNewBoard,
  onMoveBoard,
}) {
  return (
    // role="main": this page is the shell's content, and without the
    // landmark a screen reader's "jump to main" found nothing on it while
    // Tasks, Inbox and CI had one. A role rather than a <main> so the
    // element keeps .main-column's layout and not main's own padding.
    <div className="main-column" role="main">
      <ListHeader
        title="Boards"
        count={boards.length}
        icon={<ItemGlyph kind="boards" size={20} />}
        action={
          <Button
            size="small"
            variant="contained"
            sx={{ ml: "auto" }}
            onClick={() => onNewBoard()}
          >
            New board
          </Button>
        }
      />
      <Typography
        variant="body2"
        color="text.secondary"
        sx={{ px: 1.2, pt: 1, maxWidth: "46rem" }}
      >
        A board is a saved way of looking at this deployment&apos;s tasks --
        filtered, sorted, as a list or as a Kanban board, filed in a folder of
        your own. Boards live in grain&apos;s own settings, so they are the same
        on every browser you open this in.
      </Typography>
      <Typography
        variant="body2"
        color="text.secondary"
        sx={{ px: 1.2, pt: 1, pb: 1, maxWidth: "46rem" }}
      >
        Boards are made by asking for one: <strong>New board</strong> opens a
        task, already written, that an agent picks up and turns into a pull
        request against this deployment&apos;s state. Say what you want to see
        in it -- or narrow the task list first and click New board there, and
        the narrowing you are looking at comes along as the board&apos;s own.
      </Typography>
      {boards.length > 0 && (
        <Typography
          variant="body2"
          color="text.secondary"
          sx={{ px: 1.2, pt: 1, pb: 1, maxWidth: "46rem" }}
        >
          Where a board sits is yours to move, though, and takes effect at once:
          drag a board by its handle to reorder it, or drop it on a
          folder&apos;s name to file it there.
        </Typography>
      )}

      {boards.length === 0 ? (
        <ListEmpty>
          {/* A span rather than a div: ListEmpty's own element is a
              paragraph, and a block inside one is invalid markup. */}
          <Box
            component="span"
            sx={{
              display: "inline-flex",
              flexDirection: "column",
              alignItems: "center",
              gap: 1.2,
            }}
          >
            <span>No boards yet.</span>
            <Button variant="contained" onClick={() => onNewBoard()}>
              New board
            </Button>
          </Box>
        </ListEmpty>
      ) : (
        <ul className="template-list">
          <BoardList
            boards={boards}
            onMove={onMoveBoard}
            handles
            slotClassName="reorder-slot-gutter"
            renderBoard={(board, state) => (
              <Row
                board={board}
                {...state}
                onOpen={() => onOpenBoard(board.id)}
              />
            )}
            renderFolder={(folder) => (
              /* A folder is a heading over its own boards and nothing
                 else -- there is no folder to open, since a folder is a
                 word a board carries (boards.js). It is a drop target,
                 though: dropping a board on it is how the board comes
                 to carry that word. */
              <Typography
                variant="caption"
                color="text.secondary"
                component="p"
                className="board-folder-heading"
              >
                {folder}
              </Typography>
            )}
          />
        </ul>
      )}
    </div>
  );
}
