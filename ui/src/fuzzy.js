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

// fuzzyScore is how well query matches text, or -1 when it does not:
// every character of the query must appear in the text in order, case
// ignored, with spaces in the query ignored too -- so "gem flash", "g37f"
// and "flash" all find "gemini-3.7-flash". Higher is better: a match
// that is one unbroken run, that starts the text, or whose characters
// start words ("-", ".", "/", " ", "_" boundaries) beats one scattered
// through the middle of a longer name.
export function fuzzyScore(query, text) {
  const q = String(query || "")
    .toLowerCase()
    .replace(/\s+/g, "");
  const t = String(text || "").toLowerCase();
  if (!q) return 0;
  if (!t) return -1;
  const at = t.indexOf(q);
  if (at >= 0) {
    // A plain substring beats any scattered match.
    return 1000 - at * 2 - (t.length - q.length) / 100;
  }
  let score = 0;
  let ti = 0;
  let previous = -2;
  for (const ch of q) {
    const found = t.indexOf(ch, ti);
    if (found < 0) return -1;
    if (found === previous + 1) score += 5;
    if (found === 0 || /[-./ _:]/.test(t[found - 1])) score += 3;
    score -= Math.min(found - ti, 10) / 10;
    previous = found;
    ti = found + 1;
  }
  return score - t.length / 100;
}

// fuzzyFilter is the items whose text fuzzyScore matches query, best
// first; ties keep their original order. With no query it is every item,
// in order.
export function fuzzyFilter(items, query, text = (item) => item) {
  if (!String(query || "").trim()) return items;
  return items
    .map((item, index) => ({
      item,
      index,
      score: fuzzyScore(query, text(item)),
    }))
    .filter((entry) => entry.score >= 0)
    .sort((a, b) => b.score - a.score || a.index - b.index)
    .map((entry) => entry.item);
}

// searchMatch is how a searchable picker (SearchSelect.jsx, RepoPicker.jsx)
// reads what was typed against one option's text, or null when it does
// not match. The query is split on spaces and every token must appear
// somewhere in the text, in any order, case ignored -- so "granule" finds
// "gke-labs/granule" and "exp grain" finds "host.example/experimental/grain"
// without being typed from the start of the name. tier says how good the
// match is: 0 when some token starts a "/"-segment (the repo's own name,
// its owner, or the text itself), 1 for any other substring, and 2 for a
// subsequence -- the query's characters in order with gaps between them,
// which searchFilter only falls back to when nothing matched at tier 0
// or 1. ranges are the [start, end) runs of text the query hit, merged
// and in order, for a picker to highlight.
export function searchMatch(query, text) {
  const t = String(text ?? "").toLowerCase();
  const tokens = String(query ?? "")
    .toLowerCase()
    .split(/\s+/)
    .filter((tok) => tok !== "");
  if (tokens.length === 0) return { tier: 0, ranges: [] };
  const ranges = [];
  let segmentStart = false;
  for (const tok of tokens) {
    let at = t.indexOf(tok);
    if (at < 0) return null;
    // Prefer an occurrence that starts a segment over the first one, so
    // "grain" highlights the name in "grain-tools/grain" rather than
    // ranking on whichever it happened to find first.
    let best = at;
    while (at >= 0) {
      if (at === 0 || t[at - 1] === "/") {
        best = at;
        segmentStart = true;
        break;
      }
      at = t.indexOf(tok, at + 1);
    }
    ranges.push([best, best + tok.length]);
  }
  return { tier: segmentStart ? 0 : 1, ranges: mergeRanges(ranges) };
}

// subsequenceMatch is searchMatch's fallback: every character of the
// query, spaces dropped, in order somewhere in the text.
function subsequenceMatch(query, text) {
  const q = String(query ?? "")
    .toLowerCase()
    .replace(/\s+/g, "");
  const t = String(text ?? "").toLowerCase();
  const ranges = [];
  let ti = 0;
  for (const ch of q) {
    const found = t.indexOf(ch, ti);
    if (found < 0) return null;
    ranges.push([found, found + 1]);
    ti = found + 1;
  }
  return { tier: 2, ranges: mergeRanges(ranges) };
}

function mergeRanges(ranges) {
  const sorted = [...ranges].sort((a, b) => a[0] - b[0]);
  const out = [];
  for (const [s, e] of sorted) {
    const last = out[out.length - 1];
    if (last && s <= last[1]) last[1] = Math.max(last[1], e);
    else out.push([s, e]);
  }
  return out;
}

// searchFilter is the items searchMatch keeps for query, best first, each
// as {item, ranges}: segment-start matches, then other substrings, and
// only when neither found anything, subsequences (ranked by fuzzyScore).
// Within a tier shorter texts come first -- the closest thing to an exact
// match -- and ties keep their original order. With no query it is every
// item, in order, with nothing to highlight.
export function searchFilter(items, query, text = (item) => item) {
  if (!String(query ?? "").trim()) {
    return items.map((item) => ({ item, ranges: [] }));
  }
  const rank = (entries) =>
    entries
      .sort(
        (a, b) =>
          a.tier - b.tier ||
          b.score - a.score ||
          a.length - b.length ||
          a.index - b.index,
      )
      .map(({ item, ranges }) => ({ item, ranges }));
  const substring = [];
  items.forEach((item, index) => {
    const t = String(text(item) ?? "");
    const m = searchMatch(query, t);
    if (m) substring.push({ item, index, length: t.length, score: 0, ...m });
  });
  if (substring.length > 0) return rank(substring);
  const fuzzy = [];
  items.forEach((item, index) => {
    const t = String(text(item) ?? "");
    const m = subsequenceMatch(query, t);
    if (m) {
      fuzzy.push({
        item,
        index,
        length: t.length,
        score: fuzzyScore(query, t),
        ...m,
      });
    }
  });
  return rank(fuzzy);
}
