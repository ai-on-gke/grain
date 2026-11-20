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

export { repoCompareMatcher } from "../app.routes";

// compareRefSegment spells one git ref in a compare URL the way classic's
// comparisonRefSegment (ui/src/state.js) does: "/" stays literal, matching
// GitHub's own compare URLs, while characters that end or split a URL path
// ("#", "?", "%", ";") are percent-encoded so a reload or shared link still
// compares the whole branch name (e.g. fix/#5632-hash -> fix/%235632-hash).
export function compareRefSegment(ref: string): string {
  return encodeURIComponent(ref.trim()).replace(/%2F/gi, "/");
}

function encodeRepoPart(part: string): string {
  return encodeURIComponent(part).replace(/%3A/gi, ":");
}

// repoComparePath builds /repos/<owner>/<name>/compare/[<base>...]<head> (or
// /repos/<host>/<owner>/<name>/compare/...) for Router.navigateByUrl /
// Router.parseUrl. An empty base omits "<base>..." so the server resolves the
// repository's own default branch.
export function repoComparePath(
  repo: string,
  base: string,
  head: string,
): string {
  const repoPath = repo
    .trim()
    .split("/")
    .filter(Boolean)
    .map(encodeRepoPart)
    .join("/");
  const b = base.trim();
  const h = head.trim();
  if (!h) return `/repos/${repoPath}/compare`;
  const headSeg = compareRefSegment(h);
  return b
    ? `/repos/${repoPath}/compare/${compareRefSegment(b)}...${headSeg}`
    : `/repos/${repoPath}/compare/${headSeg}`;
}

export interface ParsedComparisonRefs {
  base: string;
  head: string;
  // True when an unescaped "#" in a hand-typed URL split part of a branch
  // name into location.hash and was reattached here.
  recoveredFragment: boolean;
}

// parseComparisonRefs splits the remainder of a compare path on the first
// "..." (git forbids consecutive dots in a ref name). Angular's
// DefaultUrlSerializer has already decoded each UrlSegment once before joining
// them with "/", so "%2F", "%23", "%3F" and "%25" arrive here as "/", "#", "?"
// and "%" without double-decoding.
export function parseComparisonRefs(
  raw: string,
  fragment?: string | null,
): ParsedComparisonRefs {
  let text = raw;
  let recoveredFragment = false;
  const frag = fragment ?? "";
  if (frag && (!text || text.endsWith("/") || text.endsWith("..."))) {
    text = `${text}#${frag}`;
    recoveredFragment = true;
  }
  const at = text.indexOf("...");
  if (at < 0) {
    return {
      base: "",
      head: text.trim(),
      recoveredFragment,
    };
  }
  return {
    base: text.slice(0, at).trim(),
    head: text.slice(at + 3).trim(),
    recoveredFragment,
  };
}
