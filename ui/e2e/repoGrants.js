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

// repoGrantBox is the repo picker of a repository grant still to be
// given a repo (ui/src/components/RepoGrants.jsx): kind "read" or
// "write". The repos a task reads and writes are lines of the Grants
// list (unified-grants slice 5), added from its "Add grant…" menu, so
// this picks "Read a repository" or "Write a repository" there -- unless
// a picker of that kind is open already -- and returns its search box.
// scope is the part of the page holding the list (a page, a dialog, a
// task pane's Access section).
export async function repoGrantBox(scope, kind) {
  const page = scope.page ? scope.page() : scope;
  const name = kind === "read" ? "Repo to read" : "Repo to write";
  const box = scope.getByRole("textbox", { name, exact: true });
  if ((await box.count()) > 0) return box.first();
  await scope.getByRole("button", { name: "Add grant…" }).first().click();
  await page
    .getByRole("menuitem", {
      name: kind === "read" ? /Read a repository/ : /Write a repository/,
    })
    .click();
  await box.first().waitFor();
  return box.first();
}
