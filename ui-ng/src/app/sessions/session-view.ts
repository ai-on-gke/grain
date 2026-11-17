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
import {
  hasMermaidFence,
  transformMermaidTokens,
  type MermaidToken,
} from "../shared/mermaid";

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

// escapeText is escapeHtml for prose, which leaves character references
// (&quot;, &amp;, &#39;) whole, so they read as the characters they name --
// as CommonMark, GitHub and the classic UI read them -- rather than as
// "&quot;". The browser only ever decodes one into text, never markup.
const CHAR_REF_RE =
  /&(?![A-Za-z][A-Za-z0-9]{1,31};|#\d{1,7};|#[xX][0-9A-Fa-f]{1,6};)/g;

function escapeText(s: string): string {
  return s
    .replace(CHAR_REF_RE, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

// The raw HTML a message may use, as GitHub allows it in a comment: a few
// inline tags, bare -- written with no attribute at all -- so nothing can
// ride in on one (no href, no src, no on*, no style). Any other tag, or one
// of these with an attribute, is shown as text.
const INLINE_HTML_RE =
  /^<(\/?)(code|strong|em|b|i|kbd|sub|sup)\s*>$|^<br\s*\/?>$/i;

function inlineHtml(raw: string): string | null {
  const m = INLINE_HTML_RE.exec(raw);
  if (!m) return null;
  return m[2] ? `<${m[1]}${m[2].toLowerCase()}>` : "<br>";
}

function isLocal(href: string): boolean {
  return href.startsWith("/") || href.startsWith("#");
}

// Raw HTML in a message is shown as text, except the bare inline tags
// above, so a message can only ever produce Markdown's own elements and
// those; leaf text is always escaped even when Marked v15 sets inRawBlock
// (e.g. after <kbd>), and code blocks render inside syntax-colored
// .code-card containers with a Copy button.
const md = new Marked({
  gfm: true,
  breaks: true,
  // "mermaid" tokens come from transformMermaidTokens, which builds their
  // markup from escaped text.
  extensions: [
    {
      name: "mermaid",
      renderer: (token) => (token as unknown as MermaidToken).html,
    },
  ],
  renderer: {
    html({ text }) {
      return text.replace(
        /<[^<>]*>|[^<]+|</g,
        (piece) => inlineHtml(piece) ?? escapeText(piece),
      );
    },
    text(token) {
      return "tokens" in token && token.tokens
        ? this.parser.parseInline(token.tokens)
        : escapeText(token.text);
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

// renderMarkdown turns a message into HTML for [innerHTML]. With
// {mermaid: true} (the [gMarkdown] directive, which draws them) ```mermaid
// fences become diagram blocks; otherwise they stay code cards.
export function renderMarkdown(
  src: string,
  opts: { mermaid?: boolean } = {},
): string {
  if (!opts.mermaid || !hasMermaidFence(src)) {
    return md.parse(src, { async: false });
  }
  const lexed = md.lexer(src);
  const tokens = Object.assign(transformMermaidTokens(lexed), {
    links: lexed.links,
  });
  return md.parser(tokens);
}
