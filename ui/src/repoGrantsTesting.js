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

import { screen, within } from "@testing-library/react";

// repoGrantBox is the repo picker of a repository grant still to be given
// a repo (RepoGrants.jsx): kind "read" or "write". It picks "Read a
// repository" or "Write a repository" from the Grants list's "Add
// grant…" menu first, unless a picker of that kind is already open, and
// returns its search box. scope narrows the search to one part of the
// page (a task pane's Access section) where there is more than one list.
export async function repoGrantBox(user, kind, scope = screen) {
  const name = kind === "read" ? "Repo to read" : "Repo to write";
  const open = scope.queryByRole("textbox", { name });
  if (open) return open;
  await user.click(scope.getByRole("button", { name: "Add grant…" }));
  await user.click(
    await screen.findByRole("menuitem", {
      name: kind === "read" ? /Read a repository/ : /Write a repository/,
    }),
  );
  return within(document.body).findByRole("textbox", { name });
}
