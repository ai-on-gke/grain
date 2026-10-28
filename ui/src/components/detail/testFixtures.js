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

// What every DetailOverlay test file shares: the task and config they
// render, the PATCH filter they assert on, and the reset each one runs
// after every test. Each test file still mocks ../../api.js and
// ../../attachments.js itself, since vi.mock is hoisted per file; this
// module's own imports of them resolve to those mocks.
import api from "../../api.js";
import { clearBranchCache } from "../BranchField.jsx";

export const baseTask = {
  id: "12",
  title: "Fix the login bug",
  description: "Login fails on retry",
  state: "queued",
  autoMerge: false,
  capabilities: [],
  comments: [],
};

export const config = {
  capabilities: [
    { id: "web-search", name: "Web search", description: "search the web" },
  ],
};

// The edits this pane sends about the task on screen, which is what
// every "was this saved?" assertion below is about -- the pane also
// *reads* (a repo's branches, for the base branch box's own
// suggestions), and a read is not an edit.
export const patches = () =>
  api.mock.calls.filter(([path]) => path === "/api/tasks/12");

export function resetDetailMocks() {
  // Reset rather than clear, and put the module's own default back: a
  // test that answers one path differently (the branch refs the
  // Internal PR link is gated on) must not answer the next test's
  // reads with it.
  api.mockReset();
  api.mockResolvedValue(null);
  // The branch box caches a repo's branches for as long as the module
  // is loaded, so without this one test's fetch would answer the
  // next test's box and the call counts here would depend on order.
  // It clears the shared branch-ref cache with it.
  clearBranchCache();
}
