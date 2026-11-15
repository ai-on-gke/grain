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
  CapabilityOption,
  Comment,
  ConsoleSession,
  Task,
} from "../api/models";

export const DIRECTOR_ID = "director";

// SessionStatus is what the list and the conversation header say about a
// session, read from its task's state. A session has no idle state of
// its own on the server: between messages its task waits on the person.
export interface SessionStatus {
  label: string;
  tone: "reply" | "neutral" | "error" | "quiet" | "setup";
  tip: string;
  // live is true while a run is going or about to: Stop applies.
  live: boolean;
}

export function sessionStatus(task: Task | null | undefined): SessionStatus {
  switch (task?.state) {
    case "running":
      return {
        label: "Working",
        tone: "setup",
        tip: "The agent is working on your last message",
        live: true,
      };
    case "queued":
      return {
        label: "Starting",
        tone: "neutral",
        tip: "A run is queued for your last message",
        live: true,
      };
    case "awaiting_reply":
      return {
        label: "Asked",
        tone: "reply",
        tip: "Waiting for your message",
        live: false,
      };
    case "failed":
      return {
        label: "Last reply failed",
        tone: "error",
        tip: "The last run on this conversation failed",
        live: false,
      };
    case "closed":
      return {
        label: "Closed",
        tone: "quiet",
        tip: "This conversation is closed",
        live: false,
      };
    case undefined:
      return {
        label: "No conversation",
        tone: "quiet",
        tip: "This session has no task yet",
        live: false,
      };
    default:
      return {
        label: "Idle",
        tone: "quiet",
        tip: "Nothing running; send a message to start a run",
        live: false,
      };
  }
}

export function isApiCapability(id: string): boolean {
  return (
    id.startsWith("api:") ||
    id.startsWith("https://grain.dev/capabilities/api#")
  );
}

export function toggleRows(
  offered: readonly CapabilityOption[] | undefined,
  held: readonly string[] | undefined,
): CapabilityOption[] {
  const has = new Set(held ?? []);
  return (offered ?? []).filter(
    (c) => (c.ready !== false || has.has(c.id)) && !isApiCapability(c.id),
  );
}

export function otherGrants(
  session: ConsoleSession | null | undefined,
  rows: readonly CapabilityOption[],
): string[] {
  const shown = new Set(rows.map((r) => r.id));
  return (session?.capabilities ?? [])
    .filter((id) => !isApiCapability(id) && !shown.has(id))
    .map(grantName);
}

export function grantName(id: string): string {
  const hash = id.indexOf("#");
  if (hash < 0) return id;
  const frag = id.slice(hash + 1);
  const eq = frag.indexOf("=");
  const name = eq < 0 ? frag : frag.slice(0, eq);
  if (eq >= 0) {
    try {
      const args = JSON.parse(frag.slice(eq + 1)) as Record<string, unknown>;
      const vals = Object.values(args)
        .flatMap((v) => (Array.isArray(v) ? v : [v]))
        .filter((v) => typeof v === "string") as string[];
      if (vals.length) return `${name}: ${vals.join(", ")}`;
    } catch {
      // Not JSON arguments: the name alone.
    }
  }
  return name;
}

export function apiGrantCount(session: ConsoleSession | null | undefined) {
  return (session?.capabilities ?? []).filter(isApiCapability).length;
}

export function authorOf(c: Comment): string {
  if (c.authorKind === "automation" && c.author === "console-session") {
    return "Grain";
  }
  const who = c.authorLabel || c.author || "Unknown";
  return c.onBehalfOfLabel || c.onBehalfOf
    ? `${who} for ${c.onBehalfOfLabel || c.onBehalfOf}`
    : who;
}

export function isHuman(c: Comment): boolean {
  return c.authorKind === "human";
}

export function lastLine(comments: readonly Comment[] | undefined): string {
  const last = comments?.length ? comments[comments.length - 1] : null;
  if (!last) return "";
  const text = (last.summary || last.body || "")
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/[#>*_`~[\]]+/g, "")
    .replace(/\s+/g, " ")
    .trim();
  return text.length > 120 ? `${text.slice(0, 119)}…` : text;
}

export function isStaleEdit(err: unknown): boolean {
  const e = err as { status?: number; error?: { code?: string } };
  return e?.status === 409 && e?.error?.code === "stale";
}

export function errorText(err: unknown): string {
  const e = err as {
    error?: { error?: string; message?: string } | string;
    message?: string;
    status?: number;
    statusText?: string;
  };
  if (typeof e?.error === "string" && e.error) return e.error;
  // An error answer whose body is not JSON -- a proxy's HTML 502 page, a
  // plain-text 500 -- reaches here as the parser's SyntaxError ("Unexpected
  // token '<' ..."), which says nothing about what went wrong: name the
  // HTTP status instead.
  if (e?.error instanceof Error && typeof e.status === "number" && e.status) {
    const text =
      e.statusText && e.statusText !== "OK" ? ` ${e.statusText}` : "";
    return `The server answered ${e.status}${text}.`;
  }
  if (e?.error && typeof e.error === "object") {
    const m = e.error.error ?? e.error.message;
    if (m) return m;
  }
  return e?.message || "The request failed.";
}
