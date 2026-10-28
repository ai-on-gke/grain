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

// How a menu that picks a template names each one. A template's name
// is how a human picks it out of a list (model.Template.Name), but
// nothing makes it unique: two repos may each declare a review.yml in
// the same team, and a hand-made template may share a name with either
// (grain/task-1968). Where a name stands alone in the list it is the
// whole label; where it does not, the label carries the title and the
// id as well, so the rows that share it can still be told apart.

function nameKey(t) {
  return String(t?.name || "")
    .trim()
    .toLowerCase();
}

// templateLabeler returns a function from a template to its label in a
// list of templates. Names are compared case-insensitively, the way a
// person reading the menu would compare them.
export function templateLabeler(templates) {
  const counts = new Map();
  for (const t of templates || []) {
    const key = nameKey(t);
    counts.set(key, (counts.get(key) || 0) + 1);
  }
  return (t) => {
    const name = String(t?.name || "").trim() || t?.id || "";
    if ((counts.get(nameKey(t)) || 0) < 2) return name;
    const title = String(t?.title || "").trim();
    return title ? `${name} — ${title} (${t.id})` : `${name} (${t.id})`;
  };
}
