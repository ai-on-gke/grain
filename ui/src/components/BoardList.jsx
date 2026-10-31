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

import { Fragment, useEffect, useRef, useState } from "react";
import {
  MoveHandle,
  MoveSlot,
  MovingBar,
  usePickup,
} from "./ReorderPickup.jsx";
import { boardPlaces, boardSections, placeKey } from "../boardOrder.js";

// BoardList is the boards, in their folders, as something you can move a
// board around in: drag it up or down where it is, or drop it into
// another folder (grain/task-81). The nav rail and the boards page both
// render through it, so the two agree on what a drop means by
// construction rather than by two implementations staying in step.
//
// # Why not ReorderableList
//
// The four ordinary list pages reorder through ListPrimitives.jsx's
// ReorderableList, and this is deliberately not that. That component
// owns one flat <ul> whose every child is a draggable row, and a drop in
// it means one thing: "put this row in this gap". A board's list is not
// flat -- it is sections, one per folder, with the loose boards above
// them -- and a drop in it has to answer two questions, since the gap it
// lands in decides the folder as well as the position. The folder
// headings are part of that answer (dropping on one files the board
// there), so they are rows of this list rather than decoration around
// it, and they are the one row that cannot be picked up.
//
// Everything the two do share is shared for real: the handle, the "Move
// here" slots, the moving bar and the pick-up state are ReorderPickup's
// own (grain/task-23, grain/task-51), so a board is picked up, dragged
// with a finger and dropped exactly the way a task in the backlog is.
//
// # What the caller renders
//
// The rows and the headings, through `renderBoard` and `renderFolder`,
// because the rail's row is a nav entry and the page's is a list row and
// nothing about them is the same. The <li>s, the drop targets and the
// slots between them are this component's; the container <ul> is the
// caller's, so the rail's entries stay inside the one <List> the rest of
// the rail is built from.
//
// `handles` is what separates the two callers. The page carries them:
// a handle is the tap-to-move gesture, which is the only reordering a
// phone has at all, and the only one reachable from a keyboard. The rail
// does not: it is 232px wide with a dot and a name already in it, the
// drawer it becomes on a phone is not where somebody tidies their
// dashboards, and the page next door offers every board the same moves
// with room to show them.
export default function BoardList({
  boards,
  onMove,
  handles = false,
  slotClassName,
  renderBoard,
  renderFolder,
}) {
  // dragId is the board being dragged right now; overId is only the
  // highlight -- which target the pointer is over -- and never decides
  // the move, which happens in onDrop alone. Same split ReorderableList
  // makes, and for the same reason: a dragover that fired over the wrong
  // element must not be able to move anything.
  const [dragId, setDragId] = useState(null);
  const [overId, setOverId] = useState(null);
  const enabled = !!onMove;

  const { picked, toggle, cancel } = usePickup();
  const pickedPresent =
    enabled &&
    handles &&
    Boolean(picked?.ids?.length) &&
    boards.some((b) => b.id === picked.ids[0]);
  useEffect(() => {
    if (!enabled || !handles || (picked && !pickedPresent)) cancel();
  }, [enabled, handles, picked, pickedPresent, cancel]);
  const pickedId = pickedPresent ? picked.ids[0] : null;
  // landedId is the board last put down from its handle. A move into
  // another folder renders it as a new row under another heading once
  // the boards come back, and that row's handle is the one focus has to
  // return to (MoveHandle's `landed`).
  const [landedId, setLandedId] = useState(null);

  // A drag is marked a tick after dragstart rather than inside it.
  // Marking it renders the trailing drop zones (below), and the one
  // closing the section above shifts every row under it down -- and
  // Chromium, once dragstart returns, checks the row is still under the
  // point the drag began at and cancels the drag outright when it is
  // not. Inside the handler, every board below the first section was
  // undraggable by mouse: dragstart, then dragend at once, and no drop.
  const starting = useRef(null);
  useEffect(() => () => clearTimeout(starting.current), []);
  const start = (id) => {
    clearTimeout(starting.current);
    starting.current = setTimeout(() => setDragId(id), 0);
  };
  const stop = () => {
    clearTimeout(starting.current);
    setDragId(null);
    setOverId(null);
  };
  const drop = (place) => {
    if (dragId !== null) onMove(dragId, place);
    setLandedId(null);
    stop();
  };
  const move = (place) => {
    if (pickedId !== null) onMove(pickedId, place);
    setLandedId(pickedId);
    cancel();
  };

  const sections = boardSections(boards);
  // Where the picked-up board is allowed to land, as the keys the slots
  // below are looked up by. Its own place is not among them: a slot that
  // promises a move and writes nothing reads as a broken button.
  const places = pickedId !== null ? boardPlaces(boards, pickedId) : [];
  const offered = new Set(places.map(placeKey));

  const slot = (place, label) =>
    pickedId !== null && offered.has(placeKey(place)) ? (
      <MoveSlot
        className={slotClassName}
        label={label}
        onMove={() => move(place)}
      />
    ) : null;

  // A drop target's own marking: the same 2px rule a task row wears
  // while a drag is over it, drawn on whatever the board would land
  // above.
  const overClass = (key) =>
    dragId !== null && overId === key ? "task-drop-target" : undefined;
  const dragOver = (key) => (e) => {
    if (dragId === null) return;
    e.preventDefault();
    setOverId(key);
  };
  const onDrop = (place) => (e) => {
    e.preventDefault();
    drop(place);
  };

  return (
    <>
      {pickedId !== null && (
        <MovingBar
          count={1}
          noun="board"
          nowhere={places.length === 0}
          onCancel={cancel}
        />
      )}
      {sections.map((section) => {
        const folder = section.folder;
        const top = { folder, beforeId: section.boards[0]?.id ?? null };
        const end = { folder, beforeId: null };
        // The key is prefixed rather than the folder's own name: the
        // loose section's name is the empty string, and a prefix keeps
        // it from colliding with a folder somebody calls "loose".
        return (
          <Fragment key={`folder:${folder}`}>
            {folder ? (
              // The heading is a drop target of its own, meaning the top
              // of the folder under it: "file this board here" is the
              // move somebody dragging towards a folder name is making,
              // and the alternative -- only the boards themselves being
              // droppable -- leaves an empty-looking folder heading that
              // rejects the one drag aimed at it.
              <li
                className={overClass(`folder:${folder}`)}
                onDragOver={enabled ? dragOver(`folder:${folder}`) : undefined}
                onDrop={enabled ? onDrop(top) : undefined}
              >
                {renderFolder(folder)}
              </li>
            ) : null}
            {section.boards.map((board) => {
              const id = board.id;
              const handle =
                enabled && handles ? (
                  <MoveHandle
                    label={
                      pickedId === id
                        ? `Stop moving ${board.name}`
                        : `Move ${board.name}`
                    }
                    picked={pickedId === id}
                    landed={landedId === id}
                    disabled={boardPlaces(boards, id).length === 0}
                    onToggle={() => {
                      setLandedId(null);
                      toggle([id]);
                    }}
                  />
                ) : null;
              return (
                <Fragment key={id}>
                  {slot(
                    { folder, beforeId: id },
                    `Move here, above ${board.name}`,
                  )}
                  <li
                    className={overClass(`board:${id}`)}
                    draggable={enabled}
                    onDragStart={enabled ? () => start(id) : undefined}
                    onDragEnd={enabled ? stop : undefined}
                    onDragOver={enabled ? dragOver(`board:${id}`) : undefined}
                    onDrop={
                      enabled ? onDrop({ folder, beforeId: id }) : undefined
                    }
                  >
                    {renderBoard(board, {
                      handle,
                      dragging: dragId === id,
                      picked: pickedId === id,
                    })}
                  </li>
                </Fragment>
              );
            })}
            {slot(
              end,
              folder
                ? `Move here, at the end of ${folder}`
                : "Move here, outside every folder",
            )}
            {/* The end of this section, for a mouse. Only while a drag is
                in flight, the way ReorderableList's own trailing zone is
                -- it exists so that "last in this folder" is reachable
                at all, since the row below it is the next folder's
                heading and dropping on that means something else. */}
            {enabled && dragId !== null && (
              <li
                className={`board-drop-end${
                  overId === `end:${folder}` ? " task-drop-target" : ""
                }`}
                onDragOver={dragOver(`end:${folder}`)}
                onDrop={onDrop(end)}
              />
            )}
          </Fragment>
        );
      })}
    </>
  );
}
