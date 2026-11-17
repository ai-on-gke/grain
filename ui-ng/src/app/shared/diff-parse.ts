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

import type {
  DiffPreviewFile,
  DiffPreviewHunk,
  DiffPreviewLine,
} from "../api/models";

function unquoteGitPath(raw: string): string {
  const s = raw.trim();
  if (!(s.startsWith('"') && s.endsWith('"') && s.length >= 2)) return s;
  const inner = s.slice(1, -1);
  const bytes: number[] = [];
  for (let i = 0; i < inner.length; i++) {
    const ch = inner[i];
    if (ch !== "\\" || i + 1 >= inner.length) {
      const encoded = new TextEncoder().encode(ch);
      for (const b of encoded) bytes.push(b);
      continue;
    }
    const next = inner[++i];
    if (/[0-7]/.test(next)) {
      let oct = next;
      for (
        let k = 0;
        k < 2 && i + 1 < inner.length && /[0-7]/.test(inner[i + 1]);
        k++
      ) {
        oct += inner[++i];
      }
      bytes.push(parseInt(oct, 8));
      continue;
    }
    const escMap: Record<string, number> = {
      a: 7,
      b: 8,
      t: 9,
      n: 10,
      v: 11,
      f: 12,
      r: 13,
      '"': 34,
      "\\": 92,
    };
    if (next in escMap) {
      bytes.push(escMap[next]);
    } else {
      const encoded = new TextEncoder().encode(next);
      for (const b of encoded) bytes.push(b);
    }
  }
  return new TextDecoder().decode(new Uint8Array(bytes));
}

function stripDiffPrefix(raw: string): string {
  const withoutTab = raw.trim().replace(/\t.*$/, "");
  const clean = unquoteGitPath(withoutTab);
  if (clean === "/dev/null") return "";
  if (clean.startsWith("a/") || clean.startsWith("b/")) return clean.slice(2);
  return clean;
}

function readDiffToken(s: string, start: number): [string, number] {
  let i = start;
  while (i < s.length && s[i] === " ") i++;
  if (i >= s.length) return ["", i];
  if (s[i] === '"') {
    let j = i + 1;
    while (j < s.length) {
      if (s[j] === "\\") j += 2;
      else if (s[j] === '"') {
        j++;
        break;
      } else j++;
    }
    return [unquoteGitPath(s.slice(i, j)), j];
  }
  const space = s.indexOf(" ", i);
  if (space === -1) return [s.slice(i), s.length];
  return [s.slice(i, space), space];
}

function parseDiffGitPaths(line: string): { oldP: string; newP: string } {
  const rest = line.slice("diff --git ".length).trim();
  if (rest.includes('"')) {
    const [rawA, nextIdx] = readDiffToken(rest, 0);
    const [rawB] = readDiffToken(rest, nextIdx);
    const oldP = rawA.startsWith("a/") ? rawA.slice(2) : rawA;
    const newP = rawB.startsWith("b/") ? rawB.slice(2) : rawB;
    return { oldP, newP };
  }
  const m = /^a\/(.+?) b\/(.+)$/.exec(rest);
  if (m) return { oldP: m[1], newP: m[2] };
  return { oldP: "", newP: rest };
}

// Parses a unified git diff (e.g. GET /api/repos/{owner}/{name}/compare or
// PendingConfigChange.diff) into DiffPreviewFile[] so dual-gutter line numbers
// and intra-line word-diff highlighting work identically to diff-preview.
export function parseUnifiedDiff(raw: string): DiffPreviewFile[] {
  if (!raw || !raw.trim()) return [];
  const lines = raw.replace(/\r\n/g, "\n").split("\n");
  const files: DiffPreviewFile[] = [];
  let cur: DiffPreviewFile | null = null;
  let hunk: DiffPreviewHunk | null = null;

  const ensureFile = (fallbackPath = "diff"): DiffPreviewFile => {
    if (!cur) {
      cur = {
        path: fallbackPath,
        status: "modified",
        additions: 0,
        deletions: 0,
        hunks: [],
      };
      files.push(cur);
    }
    return cur;
  };

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];

    if (line.startsWith("diff --git ")) {
      hunk = null;
      const { oldP, newP } = parseDiffGitPaths(line);
      cur = {
        path: newP || oldP || "file",
        ...(oldP && newP && oldP !== newP ? { oldPath: oldP } : {}),
        status: oldP && newP && oldP !== newP ? "renamed" : "modified",
        additions: 0,
        deletions: 0,
        hunks: [],
      };
      files.push(cur);
      continue;
    }

    if (line.startsWith("new file mode ")) {
      ensureFile().status = "added";
      continue;
    }
    if (line.startsWith("deleted file mode ")) {
      ensureFile().status = "deleted";
      continue;
    }
    if (line.startsWith("rename from ")) {
      const f = ensureFile();
      f.oldPath = unquoteGitPath(line.slice("rename from ".length).trim());
      f.status = "renamed";
      continue;
    }
    if (line.startsWith("rename to ")) {
      const f = ensureFile();
      f.path = unquoteGitPath(line.slice("rename to ".length).trim());
      f.status = "renamed";
      continue;
    }
    if (
      line.startsWith("Binary files ") ||
      line.startsWith("GIT binary patch")
    ) {
      ensureFile().binary = true;
      continue;
    }
    if (!hunk && line.startsWith("--- ")) {
      const p = stripDiffPrefix(line.slice(4));
      const f = ensureFile(p || "file");
      if (!p) f.status = "added";
      else if (f.path === "diff") f.path = p;
      continue;
    }
    if (!hunk && line.startsWith("+++ ")) {
      const p = stripDiffPrefix(line.slice(4));
      const f = ensureFile(p || "file");
      if (!p) f.status = "deleted";
      else f.path = p;
      continue;
    }
    if (line.startsWith("@@ ")) {
      const f = ensureFile();
      hunk = { header: line, lines: [] };
      f.hunks.push(hunk);
      continue;
    }
    if (hunk) {
      if (line.startsWith("\\ No newline at end of file")) continue;
      if (line === "" && i === lines.length - 1) continue;
      const prefix = line[0];
      let kind: DiffPreviewLine["kind"] = "ctx";
      if (prefix === "+") {
        kind = "add";
        ensureFile().additions++;
      } else if (prefix === "-") {
        kind = "del";
        ensureFile().deletions++;
      }
      hunk.lines.push({
        kind,
        text:
          prefix === "+" || prefix === "-" || prefix === " "
            ? line.slice(1)
            : line,
      });
    }
  }

  return files;
}
