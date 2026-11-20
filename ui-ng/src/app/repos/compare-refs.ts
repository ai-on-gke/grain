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

// Helpers for the compare page address: /repos/<repo>/compare/<base>...<head>
// (or /repos/<repo>/compare/<head> when the base is omitted and resolved by
// the server to the repository's default branch).
//
// Branch names carry "/" (e.g. feature/a/b) and may carry "#", "?" or "%"
// (e.g. fix/#7546-hash). Leaving "/" literal keeps URLs readable and matches
// github.com and the classic UI (ui/src/state.js comparisonRefSegment), while
// encoding "#", "?" and "%" keeps the browser from truncating the path at a
// fragment or query separator on reload or deep link.

export function comparisonRefSegment(ref: string): string {
  return encodeURIComponent(ref.trim()).replace(/%2F/gi, "/");
}

export function comparePath(repo: string, base: string, head: string): string {
  const cleanRepo = repo.trim().replace(/^\/+|\/+$/g, "");
  const cleanBase = base.trim();
  const cleanHead = head.trim();
  if (!cleanHead) {
    return `/repos/${cleanRepo}/compare`;
  }
  const encodedHead = comparisonRefSegment(cleanHead);
  const refs = cleanBase
    ? `${comparisonRefSegment(cleanBase)}...${encodedHead}`
    : encodedHead;
  return `/repos/${cleanRepo}/compare/${refs}`;
}

// parseComparisonRefs splits the joined path tail after "/compare/" into
// { base, head }. Git refuses consecutive dots ("...") inside a ref name, so
// "..." is an unambiguous separator. Angular's DefaultUrlSerializer already
// decodes each URL segment on the way in, so no second decodeURIComponent is
// run here (a literal "%" in a branch name survives intact).
export function parseComparisonRefs(raw: string): {
  base: string;
  head: string;
} {
  const s = (raw ?? "").trim();
  if (!s) return { base: "", head: "" };
  const at = s.indexOf("...");
  if (at < 0) return { base: "", head: s };
  return {
    base: s.slice(0, at).trim(),
    head: s.slice(at + 3).trim(),
  };
}
