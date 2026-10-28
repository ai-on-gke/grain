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

// linkedNote renders one of grain's own sentences with every http(s)
// address in it turned into a link, and nothing else changed.
//
// The merge queue's notes name the page a person has to go to: the
// review page a GitHub approval is waited for on (orchestrator's
// approvalNote), the pull request a refusal came from. Shown as plain
// text, that address has to be selected and pasted by hand from the one
// pane somebody opened to find out what to do next.
//
// Only http and https, and the text is still rendered by React rather
// than as HTML, so a note can never carry markup or a javascript: link
// in. Trailing sentence punctuation is left outside the link, since a
// note ends "... at https://.../files." as often as not.
const URL_RE = /https?:\/\/[^\s<>"'`]+/g;
const TRAILING = /[.,;:!?)\]]+$/;

export function noteParts(text) {
  const parts = [];
  if (!text) return parts;
  let last = 0;
  for (const m of text.matchAll(URL_RE)) {
    let url = m[0];
    const trail = url.match(TRAILING);
    if (trail) url = url.slice(0, -trail[0].length);
    if (!url.match(/^https?:\/\/[^/]/)) continue;
    if (m.index > last) parts.push({ text: text.slice(last, m.index) });
    parts.push({ text: url, href: url });
    last = m.index + url.length;
  }
  if (last < text.length) parts.push({ text: text.slice(last) });
  return parts;
}

export default function LinkedNote({ text }) {
  return noteParts(text).map((p, i) =>
    p.href ? (
      <a key={i} href={p.href} target="_blank" rel="noopener noreferrer">
        {p.text}
      </a>
    ) : (
      <span key={i}>{p.text}</span>
    ),
  );
}
