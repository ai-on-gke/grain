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

// Reads a run's live transcript (GET /api/tasks/{id}/attempts/{n}/transcript)
// back into the steps it is made of. Every agent framework writes the same
// shape (pkg/agent/*/transcript.go): the agent's own words as paragraphs, a
// tool call as one "> name(<compact JSON>)" line, and the call's result as
// the paragraph after it, prefixed "! " when the call failed. The JSON is
// Go's encoding/json, which writes &, < and > as &, < and >,
// so it is parsed here rather than shown as it is.

export interface LiveStep {
  /** Tool name without an MCP server prefix (mcp__server__tool). */
  tool: string;
  /** The call's decoded arguments, or null when they were not JSON. */
  args: Record<string, unknown> | null;
  /** One line: the agent's own description of the call, or a summary. */
  label: string;
  /** The decoded call: the command for a shell tool, else pretty JSON. */
  call: string;
  /** The call's result; "" while it has not come back. */
  output: string;
  /** True once a result was read for this call. */
  done: boolean;
  isError: boolean;
}

export type LiveItem =
  { kind: "text"; text: string } | { kind: "step"; step: LiveStep };

const CALL_LINE = /^> ([\w.:\-/]+)\((.*)\)\s*$/;
const LABEL_MAX = 120;

// Argument names a tool call is best summed up by, most telling first.
const SUMMARY_KEYS = [
  "description",
  "command",
  "cmd",
  "file_path",
  "path",
  "pattern",
  "query",
  "url",
  "title",
  "summary",
  "task_id",
  "id",
];

// Tools whose call is a shell command, shown as the command itself.
const COMMAND_KEYS = ["command", "cmd"];

export function parseLiveTranscript(
  text: string | null | undefined,
): LiveItem[] {
  const items: LiveItem[] = [];
  if (!text) return items;
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  // Calls whose results have not been read yet, oldest first: a model that
  // calls several tools at once writes every call line before any result.
  const pending: LiveStep[] = [];
  // Where the next result starts: the line after the last call line, or
  // after the blank line that ends the previous result. Every result is
  // written as "<text>\n\n", so an empty one is a blank line at exactly
  // this spot, and must not let the agent's next paragraph pass for it.
  let resultAt = -1;
  let i = 0;

  const pushText = (para: string[]) => {
    const t = para.join("\n").trim();
    if (t) items.push({ kind: "text", text: t });
  };

  while (i < lines.length) {
    const m = CALL_LINE.exec(lines[i]);
    if (m) {
      const step = makeStep(m[1], m[2]);
      items.push({ kind: "step", step });
      pending.push(step);
      i++;
      resultAt = i;
      continue;
    }
    if (!lines[i].trim()) {
      // Two blank lines: one is all a still-running last call ends in.
      if (i === resultAt && pending.length && lines[i + 1]?.trim() === "") {
        const step = pending.shift()!;
        step.done = true;
        i += 2;
        resultAt = i;
        continue;
      }
      i++;
      continue;
    }
    // A paragraph: up to the next blank line or call line.
    const start = i;
    while (i < lines.length && lines[i].trim() && !CALL_LINE.test(lines[i]))
      i++;
    let para = lines.slice(start, i);
    const step = pending.shift();
    if (!step) {
      pushText(para);
      continue;
    }
    // grain's run_command answers "exit=N\nstdout:\n…\n\nstderr:\n…": its
    // stdout can hold blank lines of its own, so the result runs on to the
    // end of its stderr section rather than to the first blank line.
    if (/^(! )?exit=-?\d+/.test(para[0]) && !para.some(isStderr)) {
      let j = i;
      let sawStderr = false;
      while (j < lines.length && !CALL_LINE.test(lines[j])) {
        if (isStderr(lines[j])) sawStderr = true;
        else if (sawStderr && !lines[j].trim()) break;
        j++;
      }
      if (sawStderr) {
        para = lines.slice(start, j);
        i = j;
      }
    }
    let out = para.join("\n");
    if (out.startsWith("! ")) {
      step.isError = true;
      out = out.slice(2);
    }
    step.output = out.replace(/\s+$/, "");
    step.done = true;
    resultAt = i + 1;
  }
  return items;
}

/** The steps alone, in order. */
export function liveSteps(items: readonly LiveItem[]): LiveStep[] {
  return items.flatMap((it) => (it.kind === "step" ? [it.step] : []));
}

function isStderr(line: string): boolean {
  return /^stderr:\s*$/.test(line);
}

function makeStep(rawTool: string, rawArgs: string): LiveStep {
  const tool = rawTool.replace(/^mcp__.+?__/, "");
  let args: Record<string, unknown> | null = null;
  try {
    const v: unknown = JSON.parse(rawArgs);
    if (v && typeof v === "object" && !Array.isArray(v)) {
      args = v as Record<string, unknown>;
    }
  } catch {
    // Not JSON (or cut off): fall back to the text with its escapes decoded.
  }
  return {
    tool,
    args,
    label: stepLabel(tool, args, rawArgs),
    call: stepCall(args, rawArgs),
    output: "",
    done: false,
    isError: false,
  };
}

function stepLabel(
  tool: string,
  args: Record<string, unknown> | null,
  rawArgs: string,
): string {
  const desc = args?.["description"];
  if (typeof desc === "string" && desc.trim()) return oneLine(desc);
  let summary = "";
  if (args) {
    for (const k of SUMMARY_KEYS) {
      const v = args[k];
      if (typeof v === "string" && v.trim()) {
        summary = v;
        break;
      }
    }
    if (!summary) {
      const first = Object.values(args).find(
        (v) => typeof v === "string" && v.trim(),
      );
      if (typeof first === "string") summary = first;
    }
  } else {
    summary = decodeEscapes(rawArgs);
  }
  summary = oneLine(summary);
  return summary ? `${tool}: ${summary}` : tool;
}

function stepCall(
  args: Record<string, unknown> | null,
  rawArgs: string,
): string {
  if (!args) return decodeEscapes(rawArgs);
  for (const k of COMMAND_KEYS) {
    const v = args[k];
    if (typeof v === "string") return v;
  }
  return JSON.stringify(args, null, 2);
}

function oneLine(s: string): string {
  const t = s.replace(/\s+/g, " ").trim();
  return t.length > LABEL_MAX ? t.slice(0, LABEL_MAX - 1) + "…" : t;
}

/** Turns JSON \uXXXX escapes in text that would not parse into characters. */
export function decodeEscapes(s: string): string {
  return s.replace(/\\u([0-9a-fA-F]{4})/g, (_, h: string) =>
    String.fromCharCode(parseInt(h, 16)),
  );
}

/** Splits long output for display: the head, and how many lines are left. */
export function clipOutput(
  out: string,
  maxLines = 12,
  maxChars = 2000,
): { text: string; hidden: number } {
  const lines = out.split("\n");
  let text = lines.slice(0, maxLines).join("\n");
  if (text.length > maxChars) text = text.slice(0, maxChars);
  const shownLines = text.split("\n").length;
  const hidden =
    text.length < out.length ? Math.max(lines.length - shownLines, 1) : 0;
  return { text, hidden };
}
