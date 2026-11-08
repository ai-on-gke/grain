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
export const API_CAPABILITY_SERVER_ID = "https://grain.dev/capabilities/api";
export const API_CAPABILITY_REF_PREFIX = `${API_CAPABILITY_SERVER_ID}#`;

// API_ARGS_SCHEMA is the with: schema every method on
// https://grain.dev/capabilities/api declares (grainserver.buildAPIServer):
// one optional repo limit, so GrantsPicker can render the inline repo field
// before /api/capability-methods finishes loading (or in offline unit tests).
export const API_ARGS_SCHEMA = {
  type: "object",
  additionalProperties: false,
  properties: {
    repo: {
      type: "string",
      pattern: "^([A-Za-z0-9._-]+/)?[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$",
      description:
        "Limit the grant to this repository, owner/name; omit for every repository.",
    },
  },
};

// isApiGrant reports whether id is a legacy api:<what>[@repo] grant name,
// valid or not: unmigrated rows in the store and GET /api/config's metadata
// rows still carry this spelling.
export function isApiGrant(id) {
  return typeof id === "string" && id.startsWith(API_GRANT_PREFIX);
}

// isApiCapabilityRef reports whether id is a canonical
// https://grain.dev/capabilities/api#<what>[=<json>] grant string.
export function isApiCapabilityRef(id) {
  if (typeof id !== "string") return false;
  const trimmed = id.trim();
  const eq = trimmed.indexOf("=");
  const ref = eq < 0 ? trimmed : trimmed.slice(0, eq);
  return ref.startsWith(API_CAPABILITY_REF_PREFIX);
}

// apiRef is the canonical capability reference for shortName ("ci:write" ->
// "https://grain.dev/capabilities/api#ci:write").
export function apiRef(shortName) {
  return `${API_CAPABILITY_REF_PREFIX}${shortName}`;
}

// withoutApiRows is a capability listing with the api rows taken out,
// for the generic multi-select beside ApiGrantPicker.
export function withoutApiRows(rows) {
  return (rows || []).filter((c) => !isApiGrant(c.id));
}

// splitApiGrant reads either a canonical .../api#<what>[={"repo":"..."}]
// grant or a legacy api:<what>[@repo] grant into the offered /api/config row
// id ("api:<what>") and the repository it is limited to ("" for every one).
export function splitApiGrant(id) {
  if (isApiCapabilityRef(id)) {
    const trimmed = id.trim();
    const eq = trimmed.indexOf("=");
    const ref = eq < 0 ? trimmed : trimmed.slice(0, eq);
    const short = ref.slice(API_CAPABILITY_REF_PREFIX.length);
    let repo = "";
    if (eq >= 0) {
      try {
        const parsed = JSON.parse(trimmed.slice(eq + 1));
        if (parsed && typeof parsed.repo === "string") repo = parsed.repo;
      } catch {
        // Malformed JSON keeps repo empty; checkGrants surfaces the error.
      }
    }
    return [`${API_GRANT_PREFIX}${short}`, repo];
  }
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
