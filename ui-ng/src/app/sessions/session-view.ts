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

import { Marked } from "marked";
import type {
  CapabilityOption,
  Comment,
  ConsoleSession,
  Task,
} from "../api/models";
import { renderCodeCard } from "../shared/code-highlight";

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

// isApiCapability is true for a grant of grain's own API, which the
// classic grants picker manages per method; the inline toggles leave
// those alone (ui/src/apiGrants.js isLegacyApiGrant).
export function isApiCapability(id: string): boolean {
  return (
    id.startsWith("api:") ||
    id.startsWith("https://grain.dev/capabilities/api#")
  );
}

// toggleRows is what the inline capability toggles list: what this
// deployment offers and has set up (or the session already holds), less
// the API presets (ui/src/state.js configuredCapabilities plus
// withoutApiRows).
export function toggleRows(
  offered: readonly CapabilityOption[] | undefined,
  held: readonly string[] | undefined,
): CapabilityOption[] {
  const has = new Set(held ?? []);
  return (offered ?? []).filter(
    (c) => (c.ready !== false || has.has(c.id)) && !isApiCapability(c.id),
  );
}

// otherGrants is everything the session holds that no toggle shows and
// that is not an API grant: capability-server grants such as health or
// source. They are listed by name; the classic page edits them.
export function otherGrants(
  session: ConsoleSession | null | undefined,
  rows: readonly CapabilityOption[],
): string[] {
  const shown = new Set(rows.map((r) => r.id));
  return (session?.capabilities ?? [])
    .filter((id) => !isApiCapability(id) && !shown.has(id))
    .map(grantName);
}

// grantName turns a capability reference into a short readable name:
// "https://grain.dev/capabilities/grain#health={}" reads "health",
// and a config-map grant names its maps.
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

// apiGrantCount is how many API grants the session holds, for the line
// that points at the classic grants picker.
export function apiGrantCount(session: ConsoleSession | null | undefined) {
  return (session?.capabilities ?? []).filter(isApiCapability).length;
}

// authorOf names who wrote a comment the way the classic conversation
// does: the label when the server gives one, else the author.
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

// lastLine is the one line the list shows under a session: the start of
// the newest message, without Markdown punctuation.
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

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function rewriteGrainPath(href: string): string {
  if (href.startsWith("/api/") || href === "/api") return href;
  if (href.startsWith("/next/") || href === "/next") return href;
  const parts = /^([^?#]*)(.*)$/.exec(href);
  if (!parts) return href;
  const [, path, suffix] = parts;

  const taskMatch = /^\/tasks?\/([^/]+)\/?$/.exec(path);
  if (taskMatch) return `/next/task/${taskMatch[1]}${suffix}`;
  if (/^\/tasks\/?$/.test(path)) return `/next/tasks${suffix}`;
  if (/^\/director\/?$/.test(path)) return `/next/sessions/director${suffix}`;

  const direct =
    /^\/(sessions|workstreams|inbox|ci|processes|settings|telemetry|fleet|context|agents)(\/.*)?$/.exec(
      path,
    );
  if (direct) return `/next/${direct[1]}${direct[2] ?? ""}${suffix}`;

  const alias: Record<string, string> = {
    metrics: "telemetry",
    system: "fleet",
    memory: "context",
  };
  const mapped = /^\/(metrics|system|memory)(\/.*)?$/.exec(path);
  if (mapped) return `/next/${alias[mapped[1]]}${mapped[2] ?? ""}${suffix}`;

  return href;
}

// safeHref keeps http(s), mailto, fragment #anchors, and paths on this server
// (rewriting Classic root-relative Grain UI paths to /next/... while leaving
// /api/... untouched); everything else is dropped (javascript:, data:, //host,
// /\host, and control/whitespace characters after /).
export function safeHref(href: string): string | null {
  for (let i = 0; i < href.length; i++) {
    if (href.charCodeAt(i) <= 0x1f) return null;
  }
  if (href.startsWith("#")) return href;
  if (href.startsWith("/") && !/^\/[\\/\s]/.test(href)) {
    return rewriteGrainPath(href);
  }
  try {
    const u = new URL(href);
    return ["http:", "https:", "mailto:"].includes(u.protocol) ? u.href : null;
  } catch {
    return null;
  }
}

function isLocal(href: string): boolean {
  return href.startsWith("/") || href.startsWith("#");
}

// Raw HTML in a message is shown as text, so a message can only ever
// produce Markdown's own elements; leaf text is always escaped even when
// Marked v15 sets inRawBlock (e.g. after <kbd>), and code blocks render
// inside syntax-colored .code-card containers with a Copy button.
const md = new Marked({
  gfm: true,
  breaks: true,
  renderer: {
    html({ text }) {
      return escapeHtml(text);
    },
    text(token) {
      return "tokens" in token && token.tokens
        ? this.parser.parseInline(token.tokens)
        : escapeHtml(token.text);
    },
    code({ text, lang }) {
      return renderCodeCard(text, lang);
    },
    // Links open in a new tab and only to web or mail addresses; images
    // become links, so a message cannot load a remote image (a tracking
    // pixel) just by being read.
    link({ href, title, tokens }) {
      const label = this.parser.parseInline(tokens);
      const safe = safeHref(href);
      if (!safe) return label;
      const t = title ? ` title="${escapeHtml(title)}"` : "";
      // Links on this server open in place; others in a new tab.
      const tab = isLocal(safe)
        ? ""
        : ' target="_blank" rel="noopener noreferrer"';
      return `<a href="${escapeHtml(safe)}"${t}${tab}>${label}</a>`;
    },
    image({ href, text }) {
      const safe = safeHref(href);
      const label = escapeHtml(text || href);
      return safe
        ? `<a href="${escapeHtml(safe)}" target="_blank" rel="noopener noreferrer">${label}</a>`
        : label;
    },
  },
});

// renderMarkdown turns a message into HTML for [innerHTML].
export function renderMarkdown(src: string): string {
  return md.parse(src, { async: false });
}

// isStaleEdit is the server refusing a capability save because the
// session's grants changed since they were read (409 coded "stale").
export function isStaleEdit(err: unknown): boolean {
  const e = err as { status?: number; error?: { code?: string } };
  return e?.status === 409 && e?.error?.code === "stale";
}

export function errorText(err: unknown): string {
  const e = err as {
    error?: { error?: string; message?: string } | string;
    message?: string;
  };
  if (typeof e?.error === "string" && e.error) return e.error;
  if (e?.error && typeof e.error === "object") {
    const m = e.error.error ?? e.error.message;
    if (m) return m;
  }
  return e?.message || "The request failed.";
}
