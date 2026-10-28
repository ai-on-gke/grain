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

// The agent API's grants (docs/agent-api.md) as a capability picker
// sees them. Every scope and preset is an ordinary capability name --
// api:<scope>, api:<preset>, either optionally limited to one
// repository as api:<name>@owner/repo -- so GET /api/config lists them
// among the other capability rows (ui.APICapabilities), presets first,
// each carrying `preset`, `scopes`, `dangerous` and `summary`. Offered
// flat they are some fifty rows appended to every picker; these helpers
// are what lets ApiGrantPicker draw them as one group instead, and the
// pickers keep them out of their own lists.

export const API_GRANT_PREFIX = "api:";

// isApiGrant reports whether id is an agent API grant name at all,
// valid or not: a picker leaves every one of them to ApiGrantPicker,
// which can also remove one that no longer parses.
export function isApiGrant(id) {
  return typeof id === "string" && id.startsWith(API_GRANT_PREFIX);
}

// withoutApiRows is a capability listing with the api rows taken out,
// for the generic multi-select beside ApiGrantPicker.
export function withoutApiRows(rows) {
  return (rows || []).filter((c) => !isApiGrant(c.id));
}

// splitApiGrant reads a grant name into the offered row it is spelled
// from and the repository it is limited to ("" for every one):
// "api:ci:write@acme/widgets" is ["api:ci:write", "acme/widgets"].
export function splitApiGrant(id) {
  const at = id.indexOf("@");
  return at < 0 ? [id, ""] : [id.slice(0, at), id.slice(at + 1)];
}

// apiGrantId is the name to write for an offered row limited to repo,
// or not limited at all for an empty repo.
export function apiGrantId(rowId, repo) {
  return repo ? `${rowId}@${repo}` : rowId;
}

// apiShortName is the scope or preset a grant name names, without its
// prefix or repository: "api:ci:write@acme/widgets" is "ci:write".
export function apiShortName(id) {
  return splitApiGrant(id)[0].slice(API_GRANT_PREFIX.length);
}

// apiArea is a scope's area -- "tasks" for tasks:write -- which is what
// the picker groups scopes under.
export function apiArea(row) {
  const scope = apiShortName(row.id);
  const colon = scope.indexOf(":");
  return colon < 0 ? scope : scope.slice(0, colon);
}

// groupApiRows splits the offered api rows into presets and scopes, the
// scopes grouped by area in the order the vocabulary lists them
// (docs/agent-api.md), which is the order the server sends them in.
export function groupApiRows(offered) {
  const rows = (offered || []).filter((c) => isApiGrant(c.id));
  const presets = rows.filter((c) => c.preset);
  const areas = [];
  for (const row of rows.filter((c) => !c.preset)) {
    const area = apiArea(row);
    let group = areas.find((a) => a.area === area);
    if (!group) {
      group = { area, rows: [] };
      areas.push(group);
    }
    group.rows.push(row);
  }
  return { presets, areas };
}

export const DANGEROUS_API_HINT =
  "Dangerous: only a person may grant this -- never an agent -- and every call made under it must carry a reason.";
