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
import { renderCodeCard } from "../shared/code-highlight";

export * from "./session-helpers";

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
