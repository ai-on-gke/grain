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

// The scope of a batch: which repositories and owners the rows it acts
// on belong to, counted when the person pressed the button. A bulk
// confirmation names every repository, and asks for a second, explicit
// tick when more than one is involved (devfirst review, safety rule 3).

export interface ScopeCount {
  name: string;
  n: number;
}

export interface Scope {
  total: number;
  repos: ScopeCount[];
  owners: ScopeCount[];
  // Set when the batch spans more than one repository.
  multiRepo: boolean;
  // Set when the batch includes work owned by someone other than the viewer.
  otherOwner: boolean;
}

export const NO_REPO = "no repository";

function counted(values: string[]): ScopeCount[] {
  const m = new Map<string, number>();
  for (const v of values) m.set(v, (m.get(v) ?? 0) + 1);
  return [...m]
    .map(([name, n]) => ({ name, n }))
    .sort((a, b) => b.n - a.n || a.name.localeCompare(b.name));
}

export function scopeOf(
  rows: readonly { repo?: string | null; owner?: string | null }[],
  viewer = "",
): Scope {
  const repos = counted(rows.map((r) => r.repo || NO_REPO));
  const owners = counted(rows.map((r) => r.owner || "").filter(Boolean));
  const otherOwner =
    !!viewer &&
    owners.some((o) => o.name.toLowerCase() !== viewer.toLowerCase());
  return {
    total: rows.length,
    repos,
    owners,
    multiRepo: repos.length > 1,
    otherOwner,
  };
}

// scopeLines is the scope in plain sentences, for the dialog and tests:
// one line per repository, then one per owner when there is more than
// one owner or another person owns the work.
export function scopeLines(s: Scope): string[] {
  const lines = s.repos.map((r) => `${r.n} in ${r.name}`);
  if (s.owners.length > 1 || s.otherOwner) {
    for (const o of s.owners) lines.push(`${o.n} owned by ${o.name}`);
  }
  return lines;
}
