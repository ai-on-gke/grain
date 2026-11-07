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

// Configs for the tests of what a signed-in person's role shows
// (access.js, AccessGate.jsx).

// GET /api/config's access for a grain/viewer session: every read, no
// write (pkg/model/api_scope_set.go's preset).
export const viewerConfig = {
  access: {
    role: "grain/viewer",
    roles: ["grain/viewer"],
    full: false,
    scopes: [
      "tasks:read",
      "ci:read",
      "settings:read",
      "schedules:read",
      "templates:read",
      "boards:read",
      "suites:read",
    ],
  },
};

// A deployment's own role that may file and re-run CI, but only on
// acme/widgets.
export const widgetsConfig = {
  access: {
    role: "widgets-contributor",
    roles: ["widgets-contributor"],
    full: false,
    scopes: ["tasks:read", "tasks:write", "ci:read", "ci:write"],
    repoLimits: {
      "tasks:write": ["acme/widgets"],
      "ci:write": ["acme/widgets"],
    },
  },
};
