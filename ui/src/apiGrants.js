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
export const API_CAPABILITY_SERVER = "https://grain.dev/capabilities/api";
export const API_REF_PREFIX = `${API_CAPABILITY_SERVER}#`;

function validRepoLimit(repo) {
  return (
    typeof repo === "string" &&
    repo.length > 0 &&
    !/\s/.test(repo) &&
    repo.indexOf("/") > 0 &&
    !repo.endsWith("/")
  );
}

// isApiGrant reports whether id is an agent API grant name at all,
// valid or not, in either its api:<name>[@repo] or
// https://grain.dev/capabilities/api#<name>[={"repo":...}] spelling: a
// picker leaves every one of them to ApiGrantPicker / GrantsPicker,
// which can also remove one that no longer parses.
export function isApiGrant(id) {
  if (typeof id !== "string") return false;
  const s = id.trim();
  return s.startsWith(API_GRANT_PREFIX) || s.startsWith(API_REF_PREFIX);
}

// withoutApiRows is a capability listing with the api rows taken out,
// for the generic multi-select beside ApiGrantPicker.
export function withoutApiRows(rows) {
  return (rows || []).filter((c) => !isApiGrant(c.id));
}

// splitApiGrant reads a grant name in either spelling into the offered
// api:<name> row it stands for and the repository it is limited to (""
// for every one): both "api:ci:write@acme/widgets" and
// 'https://grain.dev/capabilities/api#ci:write={"repo":"acme/widgets"}'
// are ["api:ci:write", "acme/widgets"]. A reference whose arguments do
// not map to an optional {repo} keeps its own trimmed text as [s, ""]
// so no offered row matches it and the picker flags it for removal.
export function splitApiGrant(id) {
  const s = typeof id === "string" ? id.trim() : "";
  if (s.startsWith(API_REF_PREFIX)) {
    const rest = s.slice(API_REF_PREFIX.length);
    const eq = rest.indexOf("=");
    const what = eq < 0 ? rest : rest.slice(0, eq);
    if (!what) return [s, ""];
    if (eq < 0) return [`${API_GRANT_PREFIX}${what}`, ""];
    let parsed;
    try {
      parsed = JSON.parse(rest.slice(eq + 1));
    } catch {
      return [s, ""];
    }
    if (parsed === null) return [`${API_GRANT_PREFIX}${what}`, ""];
    if (typeof parsed !== "object" || Array.isArray(parsed)) return [s, ""];
    const keys = Object.keys(parsed);
    if (keys.length === 0) return [`${API_GRANT_PREFIX}${what}`, ""];
    if (keys.length === 1 && keys[0] === "repo") {
      const repo = typeof parsed.repo === "string" ? parsed.repo.trim() : "";
      if (validRepoLimit(repo)) {
        return [`${API_GRANT_PREFIX}${what}`, repo];
      }
    }
    return [s, ""];
  }
  const at = s.indexOf("@");
  return at < 0 ? [s, ""] : [s.slice(0, at), s.slice(at + 1)];
}

// apiGrantId is the name to write for an offered row limited to repo,
// or not limited at all for an empty repo.
export function apiGrantId(rowId, repo) {
  const [base] = splitApiGrant(rowId);
  return repo ? `${base}@${repo}` : base;
}

// storedApiGrant is model.StoredGrantSpelling for an api grant: the
// api:<name>[@repo] spelling slice 2 stores, or id trimmed when its
// arguments do not map to one. Two spellings of the same api grant
// return the same string.
export function storedApiGrant(id) {
  const [base, repo] = splitApiGrant(id);
  return base.startsWith(API_GRANT_PREFIX) ? apiGrantId(base, repo) : base;
}

// apiShortName is the scope or preset a grant name names, without its
// prefix or repository: both "api:ci:write@acme/widgets" and
// 'https://grain.dev/capabilities/api#ci:write={"repo":"acme/widgets"}'
// are "ci:write".
export function apiShortName(id) {
  const [head] = splitApiGrant(id);
  if (head.startsWith(API_REF_PREFIX)) return head.slice(API_REF_PREFIX.length);
  if (head.startsWith(API_GRANT_PREFIX))
    return head.slice(API_GRANT_PREFIX.length);
  return head;
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
