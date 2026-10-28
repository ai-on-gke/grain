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

import { isNarrowed, narrowTasks } from "../taskFilters.js";
import { forestFromTasks } from "../workTree.js";
import { ListEmpty } from "./ListPrimitives.jsx";
import NarrowingBar from "./NarrowingBar.jsx";
import TaskTree from "./TaskTree.jsx";

// TaskTreeBoard is a `tree` board's pane: the tasks its narrowing admits,
// each nested under the task whose run filed it (TaskTree.jsx,
// workTree.js).
//
// It narrows for itself, the way GanttChart does, because App hands both
// the whole backlog: the board's saved query and whatever the reader has
// typed since are App's narrowing (paths.js), and a tree that drew
// `tasks` as given would ignore both -- a board saved as "repo is
// acme/widgets" would still be every task. The narrowing bar is the same
// one every other task view wears, so a tree is narrowed the same way a
// list is.
//
// A task whose parent the narrowing leaves out is a root of its own
// (forestFromTasks), so a search for one task still shows it rather than
// nothing. Unsorted, the tree is newest first, which is what its toolbar
// calls the order; a sort the reader picks orders roots and siblings
// alike, each follow-up still under the task that filed it.
// `landmark` false draws the view's root as a plain element rather than
// a <main>: a saved board's page (BoardPage.jsx) is the main landmark
// itself, so its heading is inside it, and a second main nested in the
// first would be one landmark too many (grain/task-1253).
export default function TaskTreeBoard({
  tasks,
  config,
  narrowing,
  onNarrow,
  onOpenTask,
  landmark = true,
}) {
  const narrowed = narrowTasks(tasks, narrowing, config, {
    keepSessions: true,
  });
  const Root = landmark ? "main" : "div";
  return (
    <Root
      className={`main-column task-tree-board${landmark ? "" : " task-view"}`}
    >
      {tasks.length > 0 && (
        <NarrowingBar
          tasks={tasks}
          config={config}
          narrowing={narrowing}
          onNarrow={onNarrow}
          orderLabel="Newest first"
          orderNote="The newest task first, with each task's follow-ups under it in the order they were filed."
        />
      )}
      {tasks.length === 0 ? (
        <ListEmpty>No tasks yet.</ListEmpty>
      ) : narrowed.length === 0 ? (
        <ListEmpty>
          {isNarrowed(narrowing)
            ? "No tasks match this search and these filters."
            : "No tasks match."}
        </ListEmpty>
      ) : (
        <TaskTree
          nodes={forestFromTasks(narrowed, {
            ordered: (narrowing?.sort || []).length > 0,
          })}
          onOpenTask={onOpenTask}
        />
      )}
    </Root>
  );
}
