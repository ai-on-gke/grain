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

function stripDiffPrefix(raw: string): string {
  const clean = raw.trim().replace(/\t.*$/, "");
  if (clean === "/dev/null") return "";
  if (clean.startsWith("a/") || clean.startsWith("b/")) return clean.slice(2);
  return clean;
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
      const m = /^diff --git a\/(.+?) b\/(.+)$/.exec(line);
      const oldP = m ? m[1] : "";
      const newP = m ? m[2] : line.slice("diff --git ".length).trim();
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
      f.oldPath = line.slice("rename from ".length).trim();
      f.status = "renamed";
      continue;
    }
    if (line.startsWith("rename to ")) {
      const f = ensureFile();
      f.path = line.slice("rename to ".length).trim();
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
