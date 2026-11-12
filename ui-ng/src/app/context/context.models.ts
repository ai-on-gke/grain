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

export type ContextTab = "instructions" | "documents" | "namespace";
export type EditorMode = "edit" | "split" | "preview";
export type FsScheme = "memory" | "config";

export const MAX_MEMORY_LENGTH = 8 * 1024;
export const MAX_MEMORY_DOCUMENT_LENGTH = 64 * 1024;
export const MAX_MEMORY_DOCUMENT_SUMMARY_LENGTH = 200;
export const STALE_AFTER_DAYS = 60;

const DAY_MS = 24 * 60 * 60 * 1000;

export interface Memory {
  repo?: string;
  text: string;
  updatedAt?: string;
  updatedBy?: string;
  updatedByKind?: string;
}

export interface MemoryDocument {
  repo?: string;
  name: string;
  summary: string;
  text?: string;
  length: number;
  updatedAt?: string;
  updatedBy?: string;
  updatedByKind?: string;
}

export interface MemoryScopeRow {
  key: string;
  repo: string;
  label: string;
  text: string;
  updatedAt?: string;
  updatedBy?: string;
  updatedByKind?: string;
  documents: MemoryDocument[];
}

export interface FsEntry {
  name: string;
  type: "file" | "dir" | string;
  size?: number;
}

export interface FsListResponse {
  scheme?: string;
  path?: string;
  entries: FsEntry[];
  truncated?: boolean;
  truncated_reason?: string;
}

export interface FsGrepHit {
  file: string;
  line?: number;
  text: string;
}

export function scopeKey(repo: string | undefined | null): string {
  return (repo || "").trim().toLowerCase();
}

export function scopeLabel(repo: string | undefined | null): string {
  const clean = (repo || "").trim();
  return clean ? clean : "This deployment";
}

export function estimateTokens(text: string | undefined | null): number {
  const len = (text || "").length;
  if (len === 0) return 0;
  return Math.max(1, Math.ceil(len / 4));
}

export function memoryAgeDays(
  iso: string | undefined | null,
  now = Date.now(),
): number | null {
  if (!iso) return null;
  const at = Date.parse(iso);
  if (Number.isNaN(at)) return null;
  const days = Math.floor((now - at) / DAY_MS);
  return days < 0 ? null : days;
}

export function memoryAge(
  iso: string | undefined | null,
  now = Date.now(),
): string {
  const days = memoryAgeDays(iso, now);
  if (days === null) return "";
  if (days === 0) return "written today";
  if (days === 1) return "written yesterday";
  if (days < STALE_AFTER_DAYS) return `written ${days} days ago`;
  return `written ${Math.floor(days / 30)} months ago`;
}

export function isStaleMemory(
  iso: string | undefined | null,
  now = Date.now(),
): boolean {
  const days = memoryAgeDays(iso, now);
  return days !== null && days >= STALE_AFTER_DAYS;
}

export function documentSize(bytes: number | undefined | null): string {
  const n = Number(bytes) || 0;
  if (n < 1024) return `${n} B`;
  return `${(n / 1024).toFixed(1)} KB`;
}

export function buildMemoryScopes(
  memories: readonly Memory[],
  documents: readonly MemoryDocument[],
  extraRepos: readonly string[] = [],
): MemoryScopeRow[] {
  const rows = new Map<string, MemoryScopeRow>();
  const ensure = (repoRaw: string): MemoryScopeRow => {
    const clean = (repoRaw || "").trim();
    const k = scopeKey(clean);
    let r = rows.get(k);
    if (!r) {
      r = {
        key: k,
        repo: clean,
        label: scopeLabel(clean),
        text: "",
        documents: [],
      };
      rows.set(k, r);
    }
    return r;
  };

  ensure("");
  for (const extra of extraRepos) {
    if (extra.trim()) ensure(extra);
  }
  for (const m of memories) {
    const r = ensure(m.repo || "");
    r.text = m.text || "";
    r.updatedAt = m.updatedAt;
    r.updatedBy = m.updatedBy;
    r.updatedByKind = m.updatedByKind;
  }
  for (const d of documents) {
    ensure(d.repo || "").documents.push(d);
  }

  for (const r of rows.values()) {
    r.documents.sort((a, b) => a.name.localeCompare(b.name));
  }

  return [...rows.values()].sort((a, b) => {
    if ((a.repo === "") !== (b.repo === "")) return a.repo === "" ? -1 : 1;
    return a.key.localeCompare(b.key);
  });
}

export function parseGrepOutput(raw: string, basePath: string): FsGrepHit[] {
  const lines = (raw || "").split("\n").filter((l) => l.trim().length > 0);
  const hits: FsGrepHit[] = [];
  for (const row of lines) {
    if (row.startsWith("[truncated:")) continue;
    const m = /^([^:\n]+):(\d+):(.*)$/.exec(row);
    if (m) {
      const rel = m[1].trim();
      const full = basePath ? `${basePath}/${rel}` : rel;
      hits.push({
        file: full.replace(/\/+/g, "/"),
        line: Number(m[2]),
        text: m[3],
      });
    } else {
      const m2 = /^([^:\n]+):(.*)$/.exec(row);
      if (m2 && (m2[1].includes(".") || m2[1].includes("/"))) {
        const rel = m2[1].trim();
        const full = basePath ? `${basePath}/${rel}` : rel;
        hits.push({ file: full.replace(/\/+/g, "/"), text: m2[2] });
      } else {
        hits.push({ file: basePath, text: row });
      }
    }
  }
  return hits;
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

// Safe lightweight syntax highlighter for dark-well namespace previews.
export function highlightSyntaxHtml(text: string, filename = ""): string {
  const ext = filename.split(".").pop()?.toLowerCase() ?? "";
  return (text || "")
    .split("\n")
    .map((line, i) => {
      const num = `<span class="ctx-ln">${i + 1}</span>`;
      const trimmed = line.trimStart();
      if (
        trimmed.startsWith("#") &&
        (ext === "md" || ext === "yml" || ext === "yaml")
      ) {
        return `${num}<span class="ctx-tok-hd">${escapeHtml(line)}</span>`;
      }
      if (trimmed.startsWith("//")) {
        return `${num}<span class="ctx-tok-cmt">${escapeHtml(line)}</span>`;
      }
      const escaped = escapeHtml(line)
        .replace(/(&quot;[^&]*?&quot;)/g, '<span class="ctx-tok-str">$1</span>')
        .replace(
          /\b(true|false|null|func|type|struct|return|const|let|var|export|import)\b/g,
          '<span class="ctx-tok-kw">$1</span>',
        )
        .replace(/\b(\d+(?:\.\d+)?)\b/g, '<span class="ctx-tok-num">$1</span>');
      return `${num}${escaped}`;
    })
    .join("\n");
}
