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

import type { Comment, DiffPreviewFile, TaskDetail } from "../api/models";
import { parseUnifiedDiff } from "../shared/diff-parse";

export type TaskDocumentSource = "prompt" | "pr" | "comment";

export interface TaskDocumentItem {
  id: string;
  title: string;
  source: TaskDocumentSource;
  path?: string;
  subtitle?: string;
  markdown: string;
  lineCount: number;
  updatedAt?: string;
}

const DOC_EXT_RE = /\.(md|markdown|mdx|rst|adoc|txt)$/i;
const DOC_BASENAME_RE =
  /(^|\/)(README|CHANGELOG|DESIGN|NOTES|SUMMARY)(\.[^/]+)?$/i;
const HEADING_RE = /^#{1,6}\s+(.+)$/m;
const LIST_ITEM_RE = /^\s*(?:[-*+]|\d+\.)\s+\S/gm;
const TABLE_ROW_RE = /^\|.+\|\s*$/m;
const FENCE_RE = /^```/m;

function lineCountOf(markdown: string): number {
  const trimmed = markdown.trim();
  return trimmed ? trimmed.split(/\r?\n/).length : 0;
}

function normalizeForDedupe(markdown: string): string {
  return markdown.replace(/\r\n/g, "\n").trim().replace(/\s+/g, " ");
}

function isDocPath(path: string): boolean {
  const clean = path.trim();
  if (!clean) return false;
  return DOC_EXT_RE.test(clean) || DOC_BASENAME_RE.test(clean);
}

function basenameOf(path: string): string {
  const parts = path.split("/").filter(Boolean);
  return parts[parts.length - 1] || path;
}

function stripInlineMarkdown(raw: string): string {
  return raw
    .replace(/`([^`]+)`/g, "$1")
    .replace(/\*\*([^*]+)\*\*/g, "$1")
    .replace(/__([^_]+)__/g, "$1")
    .replace(/\*([^*]+)\*/g, "$1")
    .trim();
}

function extractHeading(markdown: string): string {
  const m = HEADING_RE.exec(markdown);
  return m ? stripInlineMarkdown(m[1]) : "";
}

function diffFileToMarkdown(file: DiffPreviewFile): string {
  if (file.binary || file.status === "deleted") return "";
  const sections: string[] = [];
  for (const hunk of file.hunks) {
    const lines: string[] = [];
    for (const line of hunk.lines) {
      if (line.kind !== "del") lines.push(line.text);
    }
    let block = lines.join("\n").replace(/^\n+|\n+$/g, "");
    if (!block) continue;
    const fenceCount = (block.match(/^\s*```/gm) ?? []).length;
    if (fenceCount % 2 === 1) {
      block += "\n```";
    }
    sections.push(block);
  }
  return sections.join("\n\n---\n\n").trim();
}

function isStructuredComment(c: Comment): boolean {
  const body = (c.body ?? "").trim();
  if (!body) return false;
  if (c.handover) return true;
  if (HEADING_RE.test(body)) return true;
  if (TABLE_ROW_RE.test(body)) return true;
  if (FENCE_RE.test(body) && body.includes("\n")) return true;
  const listMatches = body.match(LIST_ITEM_RE);
  if (listMatches && listMatches.length >= 2) return true;
  const nonEmptyLines = body
    .split(/\r?\n/)
    .filter((l) => l.trim().length > 0).length;
  if (c.summary?.trim() && nonEmptyLines >= 3) return true;
  return nonEmptyLines >= 4 && body.length >= 180;
}

function commentTitle(c: Comment, body: string): string {
  const summary = c.summary?.trim();
  if (summary) return stripInlineMarkdown(summary) || summary;
  const heading = extractHeading(body);
  if (heading) return heading;
  if (c.handover) return "Handover note";
  const firstLine = stripInlineMarkdown(
    body
      .split(/\r?\n/)
      .find((l) => l.trim().length > 0)
      ?.trim() ?? "",
  );
  if (firstLine) {
    return firstLine.length > 64 ? `${firstLine.slice(0, 61)}…` : firstLine;
  }
  return `Comment #${c.id}`;
}

function commentActor(c: Comment): string {
  if (c.authorKind === "automation" && c.author === "console-session") {
    return "Grain";
  }
  return c.authorLabel || c.author || "Agent";
}

export function extractTaskDocuments(
  task: TaskDetail,
  opts?: { diffText?: string },
): TaskDocumentItem[] {
  const docs: TaskDocumentItem[] = [];
  const seenContent = new Set<string>();
  const seenIds = new Set<string>();
  const seenPaths = new Set<string>();

  const pushDoc = (item: TaskDocumentItem): void => {
    const markdown = item.markdown.trim();
    if (!markdown) return;
    const norm = normalizeForDedupe(markdown);
    if (!norm || seenContent.has(norm) || seenIds.has(item.id)) return;
    if (item.path && seenPaths.has(item.path)) return;
    seenContent.add(norm);
    seenIds.add(item.id);
    if (item.path) seenPaths.add(item.path);
    docs.push({
      ...item,
      markdown,
      lineCount: lineCountOf(markdown),
    });
  };

  // 1. Task prompt / description
  const promptText = (task.description || task.body || "").trim();
  if (promptText) {
    pushDoc({
      id: "prompt",
      title: "Task prompt",
      source: "prompt",
      subtitle: task.repo ? `${task.repo} · Prompt` : "Task specification",
      markdown: promptText,
      lineCount: lineCountOf(promptText),
      updatedAt: task.updatedAt || task.createdAt,
    });
  }

  // 2. Markdown/doc files touched in PR diffs or pending config diffs
  const diffSources = [opts?.diffText, task.pendingConfigChange?.diff];
  for (const rawDiff of diffSources) {
    if (!rawDiff || !rawDiff.trim()) continue;
    for (const file of parseUnifiedDiff(rawDiff)) {
      if (!isDocPath(file.path)) continue;
      const md = diffFileToMarkdown(file);
      if (!md) continue;
      const base = basenameOf(file.path);
      pushDoc({
        id: `pr:${file.path}`,
        title: base,
        source: "pr",
        path: file.path,
        subtitle: file.status ? `${file.status} in PR` : "PR document",
        markdown: md,
        lineCount: lineCountOf(md),
        updatedAt: task.updatedAt,
      });
    }
  }

  // 3. Structured / multi-line report comments in conversation
  for (const c of task.comments ?? []) {
    if (!isStructuredComment(c)) continue;
    const body = (c.body ?? "").trim();
    const actor = commentActor(c);
    pushDoc({
      id: `comment-${c.id}`,
      title: commentTitle(c, body),
      source: "comment",
      subtitle: c.handover ? `${actor} · Handover` : actor,
      markdown: body,
      lineCount: lineCountOf(body),
      updatedAt: c.createdAt,
    });
  }

  return docs;
}
