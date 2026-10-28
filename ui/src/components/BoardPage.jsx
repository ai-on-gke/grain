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

import { Button, Chip, Stack, Typography } from "@mui/material";
import { PageHeaderActions, PageHeaderPageActions } from "./PageHeader.jsx";
import { boardKindLabel } from "../boards.js";
import ItemGlyph from "./ItemGlyph.jsx";

// BoardPage is one saved board: its name and what it is for, the two
// buttons that change it, and the view itself underneath
// (grain/task-48). The view is passed in as children rather than
// rendered here, RepoPage's own split: the list and the board panes are
// App's, wired to the selection and the batch bar, and this page decides
// only what is said above them.
//
// Both buttons file a task rather than opening a form -- see boards.js
// for why that is the road and not a shortcut. "Edit board" carries what
// the reader is looking at *now*, narrowing included, so the way to
// change a board is to change it on screen until it is right and then
// click the button.
export default function BoardPage({
  board,
  onBack,
  onEditBoard,
  onNewBoard,
  children,
}) {
  // The page is the board's one main landmark, header included, whatever
  // kind of view is under it: the board's name is what "jump to main"
  // should land on. A list, Kanban, timeline or tree view is a <main> of
  // its own anywhere else, so App renders it here with landmark={false}
  // rather than nest one main inside another (grain/task-1253).
  return (
    <div className="main-column" role="main">
      <div className="repo-page-header">
        <Button onClick={onBack} sx={{ ml: -0.9, alignSelf: "flex-start" }}>
          &larr; Boards
        </Button>
        <Stack
          direction="row"
          alignItems="center"
          spacing={1}
          flexWrap="wrap"
          useFlexGap
        >
          <ItemGlyph kind="boards" size={20} />
          <Typography
            variant="h6"
            component="h2"
            sx={{ m: 0, fontSize: "1.15rem", fontWeight: 600 }}
          >
            {board.name}
          </Typography>
          {board.folder ? <Chip size="small" label={board.folder} /> : null}
          <Chip size="small" label={boardKindLabel(board.kind)} />
          {/* The two buttons that change the board are the page's
              actions, in its header rail (PageHeader.jsx,
              grain/task-4915). */}
          <PageHeaderActions>
            <PageHeaderPageActions>
              <Stack
                direction="row"
                spacing={1}
                className="board-page-actions"
                sx={{ ml: "auto" }}
              >
                <Button size="small" onClick={() => onEditBoard(board)}>
                  Edit board
                </Button>
                <Button size="small" variant="contained" onClick={onNewBoard}>
                  New board
                </Button>
              </Stack>
            </PageHeaderPageActions>
          </PageHeaderActions>
        </Stack>
        {board.description ? (
          <Typography variant="body2" color="text.secondary">
            {board.description}
          </Typography>
        ) : null}
      </div>
      {children}
    </div>
  );
}
