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

// Inline Mermaid diagrams for [gMarkdown], ported from the classic UI
// (ui/src/mermaidRender.js, ui/src/mermaidPair.js,
// ui/src/components/MermaidDiagram.jsx).
//
// Rendering is in two halves. renderMarkdown (with {mermaid: true}) turns
// every ```mermaid fence into a static .mermaid-block: a toolbar, a canvas
// that says "Rendering diagram…", and the escaped source in a <pre>. That
// markup is plain text and passes sanitizeMarkdownHtml's allowlist like any
// other Markdown. hydrateMermaid then finds those blocks in the live DOM,
// imports mermaid lazily (only a page that shows a diagram pays for the
// chunk), renders each source in the active theme, runs the SVG through
// sanitizeMermaidSvg and puts it in the canvas. A source that does not
// parse leaves the canvas saying why, with the source shown beneath.
//
// Consecutive Before / After diagrams -- named in the fence info string
// (```mermaid before / ```mermaid after) or by a heading or bold label
// just above each fence (### Before / ### After) -- become one comparison
// block that shows them side by side, one at a time, or stacked.

import { JSON_SCHEMA, load as loadYaml } from "js-yaml";
import type { Token, Tokens } from "marked";
import { safeHref } from "../sessions/session-view";

export type MermaidMode = "light" | "dark";

function esc(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

// ---------- Fence and label parsing (from ui/src/mermaidPair.js) ----------

const BEFORE_PREFIX = /^before(?:$|[\s:\-–—(])/i;
const AFTER_PREFIX = /^after(?:$|[\s:\-–—(])/i;

export type MermaidRole = "before" | "after" | null;

export interface MermaidFenceMeta {
  role: MermaidRole;
  label: string;
  title: string;
}

// splitMermaidInfo reads a fence info string. It returns null unless the
// fence's language is mermaid, else the rest of the line ("before: x").
export function splitMermaidInfo(info: string | undefined): string | null {
  const m = /^(\S*)\s*(.*)$/s.exec((info ?? "").trim());
  if (!m || m[1].toLowerCase() !== "mermaid") return null;
  return m[2].trim();
}

function unwrapMetaTitle(meta: string): string {
  const raw = meta.trim();
  if (!raw) return "";
  const quoted = /^title\s*=\s*(?:"([^"]*)"|'([^']*)')$/i.exec(raw);
  if (quoted) return (quoted[1] ?? quoted[2] ?? "").trim();
  const unquoted = /^title\s*=\s*(\S.*)$/i.exec(raw);
  if (unquoted) return unquoted[1].trim();
  return raw;
}

function normalizeStateLabel(text: string, fallback: string): string {
  const trimmed = text.replace(/\s*:\s*$/, "").trim();
  if (!trimmed) return fallback;
  if (trimmed.toLowerCase() === fallback.toLowerCase()) return fallback;
  return trimmed.charAt(0).toUpperCase() + trimmed.slice(1);
}

function roleOf(text: string): MermaidRole {
  if (BEFORE_PREFIX.test(text)) return "before";
  if (AFTER_PREFIX.test(text)) return "after";
  return null;
}

export function parseMermaidFenceMeta(meta: string): MermaidFenceMeta {
  const clean = unwrapMetaTitle(meta);
  const role = roleOf(clean);
  if (!role) return { role: null, label: "", title: clean };
  return {
    role,
    label: normalizeStateLabel(clean, role === "before" ? "Before" : "After"),
    title: clean,
  };
}

function inlineText(tokens: Token[] | undefined): string {
  if (!tokens) return "";
  return tokens
    .map((t) =>
      "tokens" in t && t.tokens
        ? inlineText(t.tokens)
        : "text" in t
          ? String(t.text)
          : "",
    )
    .join("");
}

interface StateHeader {
  kind: string;
  role: "before" | "after";
  label: string;
}

// classifyBeforeAfterHeader recognises a short heading (### Before: x) or a
// bold label paragraph (**Before:**) above a fence. kind carries the heading
// depth so ## Before only pairs with ## After.
export function classifyBeforeAfterHeader(
  token: Token | undefined,
): StateHeader | null {
  if (!token) return null;
  const header = (kind: string, text: string): StateHeader | null => {
    const role = roleOf(text);
    if (!role) return null;
    const label = normalizeStateLabel(
      text,
      role === "before" ? "Before" : "After",
    );
    return { kind, role, label };
  };
  if (token.type === "heading") {
    const h = token as Tokens.Heading;
    const text = inlineText(h.tokens).trim();
    if (!text || text.length > 140) return null;
    return header(`heading:${h.depth}`, text);
  }
  if (token.type === "paragraph") {
    const p = token as Tokens.Paragraph;
    const first = p.tokens?.[0];
    if (!first || first.type !== "strong") return null;
    const text = inlineText(p.tokens).trim();
    if (!text || text.length > 140 || text.includes("\n")) return null;
    // A label, not a sentence that opens with "**Before** we deploy, ...".
    const strongText = inlineText((first as Tokens.Strong).tokens).trim();
    const rest = text.slice(strongText.length).trim();
    if (rest && !/^[:\-–—(]/.test(rest)) return null;
    return header("label", text);
  }
  return null;
}

// ---------- Markup ----------

function viewButtons(
  label: string,
  views: [string, string][],
  selected: string,
): string {
  const buttons = views
    .map(
      ([value, text]) =>
        `<button type="button" class="mermaid-view-btn" data-mermaid-view="${value}" aria-pressed="${value === selected}">${text}</button>`,
    )
    .join("");
  return `<span class="mermaid-views" role="group" aria-label="${esc(label)}">${buttons}</span>`;
}

// Expand opens the drawn diagram(s) in the shell's full-screen viewer
// ([gMarkdown] handles the click; shared/media-viewer.component.ts).
function expandButton(): string {
  return `<button type="button" class="mermaid-expand-btn" data-mermaid-expand="1" aria-label="Open diagram full screen">Expand</button>`;
}

function pane(source: string, pane: string, label: string): string {
  const tag =
    pane === "only"
      ? ""
      : `<span class="mermaid-state-tag mermaid-state-${pane}">${esc(label)}</span>`;
  return (
    `<div class="mermaid-pane" data-mermaid-pane="${pane}"${pane === "only" ? "" : ` role="group" aria-label="${esc(label)}"`}>` +
    tag +
    `<div class="mermaid-canvas" data-mermaid-canvas="1" role="group" aria-label="${esc(label)} diagram">` +
    `<span class="mermaid-note" role="status">Rendering diagram…</span>` +
    `</div>` +
    `<pre class="mermaid-source" aria-label="${esc(label)} source"><code class="language-mermaid">${esc(source.replace(/\n$/, ""))}</code></pre>` +
    `</div>`
  );
}

export function renderMermaidBlock(source: string, title: string): string {
  const name = title || "Diagram";
  return (
    `<div class="mermaid-block" data-mermaid="diagram" role="region" aria-label="${esc(name)}">` +
    `<div class="mermaid-toolbar">` +
    `<span class="mermaid-title">${esc(title || "diagram")}</span>` +
    viewButtons(
      "Diagram view",
      [
        ["diagram", "Diagram"],
        ["source", "Source"],
      ],
      "diagram",
    ) +
    expandButton() +
    `</div>` +
    pane(source, "only", name) +
    `</div>`
  );
}

export function renderMermaidComparison(
  before: string,
  after: string,
  beforeLabel: string,
  afterLabel: string,
): string {
  return (
    `<div class="mermaid-block mermaid-comparison" data-mermaid="pair" role="region" aria-label="Before and after diagrams">` +
    `<div class="mermaid-toolbar">` +
    `<span class="mermaid-title">before &amp; after</span>` +
    viewButtons(
      "Diagram comparison layout",
      [
        ["split", "Side by side"],
        ["before", "Before"],
        ["after", "After"],
        ["stacked", "Stacked"],
        ["source", "Source"],
      ],
      "split",
    ) +
    expandButton() +
    `</div>` +
    `<div class="mermaid-panes">` +
    pane(before, "before", beforeLabel || "Before") +
    pane(after, "after", afterLabel || "After") +
    `</div>` +
    `</div>`
  );
}

// ---------- Marked token transform ----------

export interface MermaidToken {
  type: "mermaid";
  raw: string;
  html: string;
}

function isMermaidFence(t: Token | undefined): t is Tokens.Code {
  return (
    !!t &&
    t.type === "code" &&
    splitMermaidInfo((t as Tokens.Code).lang) !== null
  );
}

function fenceMeta(t: Tokens.Code): MermaidFenceMeta {
  return parseMermaidFenceMeta(splitMermaidInfo(t.lang) ?? "");
}

function mermaidToken(raw: string, html: string): MermaidToken {
  return { type: "mermaid", raw, html };
}

// transformMermaidTokens replaces mermaid fences in a Marked token list
// (and in blockquotes and list items) with "mermaid" tokens, pairing
// Before / After diagrams. Blank-line space tokens between blocks are
// skipped when looking for a pair.
export function transformMermaidTokens(tokens: Token[]): Token[] {
  const blocks = tokens.filter((t) => t.type !== "space");
  const out: Token[] = [];
  let i = 0;
  while (i < blocks.length) {
    const t = blocks[i];
    const h1 = classifyBeforeAfterHeader(t);
    const h2 = classifyBeforeAfterHeader(blocks[i + 2]);
    if (
      h1 &&
      h2 &&
      h1.role === "before" &&
      h2.role === "after" &&
      h1.kind === h2.kind &&
      isMermaidFence(blocks[i + 1]) &&
      isMermaidFence(blocks[i + 3])
    ) {
      const a = blocks[i + 1] as Tokens.Code;
      const b = blocks[i + 3] as Tokens.Code;
      out.push(
        mermaidToken(
          t.raw + a.raw + blocks[i + 2].raw + b.raw,
          renderMermaidComparison(a.text, b.text, h1.label, h2.label),
        ),
      );
      i += 4;
      continue;
    }
    if (isMermaidFence(t)) {
      const next = blocks[i + 1];
      const m1 = fenceMeta(t);
      if (isMermaidFence(next)) {
        const m2 = fenceMeta(next);
        if (m1.role === "before" && m2.role === "after") {
          out.push(
            mermaidToken(
              t.raw + next.raw,
              renderMermaidComparison(t.text, next.text, m1.label, m2.label),
            ),
          );
          i += 2;
          continue;
        }
      }
      out.push(mermaidToken(t.raw, renderMermaidBlock(t.text, m1.title)));
      i++;
      continue;
    }
    if (t.type === "blockquote") {
      const q = t as Tokens.Blockquote;
      q.tokens = transformMermaidTokens(q.tokens);
    } else if (t.type === "list") {
      for (const item of (t as Tokens.List).items) {
        item.tokens = transformMermaidTokens(item.tokens);
      }
    }
    out.push(t);
    i++;
  }
  return out;
}

export function hasMermaidFence(src: string): boolean {
  // A cheap prefilter, unanchored so a fence opening on a list marker
  // ("1. ```mermaid") or inside a quote counts; a false positive only
  // costs a token walk.
  return /(?:`{3,}|~{3,})[ \t]*mermaid\b/i.test(src);
}

// ---------- Theme ----------

const FONT_FAMILY =
  '"Google Sans Text", "Google Sans", Roboto, system-ui, -apple-system, sans-serif';

// Material 3 tokens from src/styles.css (design book, "Colour"): nodes sit
// on the card surface with a primary hairline, clusters recess, notes use
// the warning container.
const THEME_VARIABLES: Record<MermaidMode, Record<string, string | boolean>> = {
  light: {
    darkMode: false,
    background: "#ffffff",
    fontFamily: FONT_FAMILY,
    fontSize: "13px",
    primaryColor: "#eef3fd",
    primaryTextColor: "#1b1d22",
    primaryBorderColor: "#0b57d0",
    secondaryColor: "#e3f2fc",
    secondaryTextColor: "#1b1d22",
    secondaryBorderColor: "#00639b",
    tertiaryColor: "#f7f9fc",
    tertiaryTextColor: "#1b1d22",
    tertiaryBorderColor: "#d8e0eb",
    lineColor: "#727885",
    textColor: "#1b1d22",
    mainBkg: "#eef3fd",
    nodeBorder: "#0b57d0",
    clusterBkg: "#f7f9fc",
    clusterBorder: "#d8e0eb",
    titleColor: "#1b1d22",
    edgeLabelBackground: "#ffffff",
    actorBkg: "#eef3fd",
    actorBorder: "#0b57d0",
    actorTextColor: "#1b1d22",
    actorLineColor: "#727885",
    signalColor: "#1b1d22",
    signalTextColor: "#1b1d22",
    labelBoxBkgColor: "#f7f9fc",
    labelBoxBorderColor: "#d8e0eb",
    labelTextColor: "#1b1d22",
    noteBkgColor: "#fef0c3",
    noteBorderColor: "#8a5300",
    noteTextColor: "#3d2800",
  },
  dark: {
    darkMode: true,
    background: "#13151a",
    fontFamily: FONT_FAMILY,
    fontSize: "13px",
    primaryColor: "#1f2a3d",
    primaryTextColor: "#e4e6eb",
    primaryBorderColor: "#a8c7fa",
    secondaryColor: "#173045",
    secondaryTextColor: "#e4e6eb",
    secondaryBorderColor: "#7fcfff",
    tertiaryColor: "#1e2128",
    tertiaryTextColor: "#e4e6eb",
    tertiaryBorderColor: "#383d4a",
    lineColor: "#8b91a0",
    textColor: "#e4e6eb",
    mainBkg: "#1f2a3d",
    nodeBorder: "#a8c7fa",
    clusterBkg: "#1e2128",
    clusterBorder: "#383d4a",
    titleColor: "#e4e6eb",
    edgeLabelBackground: "#13151a",
    actorBkg: "#1f2a3d",
    actorBorder: "#a8c7fa",
    actorTextColor: "#e4e6eb",
    actorLineColor: "#8b91a0",
    signalColor: "#e4e6eb",
    signalTextColor: "#e4e6eb",
    labelBoxBkgColor: "#1e2128",
    labelBoxBorderColor: "#383d4a",
    labelTextColor: "#e4e6eb",
    noteBkgColor: "#4a3400",
    noteBorderColor: "#ffdf99",
    noteTextColor: "#ffdf99",
  },
};

export function mermaidConfigForMode(mode: MermaidMode) {
  return {
    startOnLoad: false,
    securityLevel: "strict" as const,
    // SVG <text> labels rather than <foreignObject> HTML, so a node label
    // cannot load a remote image or hang Mermaid's image wait.
    htmlLabels: false,
    // Diagram sources are untrusted: %%{init}%% may not loosen these, inject
    // themeCSS rules that fetch external resources during layout, or restyle
    // a diagram out of the palette ThemeService picked (a themeCSS or
    // themeVariables directive otherwise pins its colours and fonts through
    // every theme switch).
    secure: [
      "secure",
      "securityLevel",
      "startOnLoad",
      "maxTextSize",
      "maxEdges",
      "htmlLabels",
      "flowchart",
      "suppressErrorRendering",
      "theme",
      "themeVariables",
      "themeCSS",
      "darkMode",
      "fontFamily",
    ],
    suppressErrorRendering: true,
    theme: "base" as const,
    fontFamily: FONT_FAMILY,
    themeVariables: THEME_VARIABLES[mode],
    flowchart: { useMaxWidth: true, htmlLabels: false, curve: "basis" },
    sequence: { useMaxWidth: true },
  };
}

// ---------- SVG sanitizer ----------

const FORBIDDEN_SVG_TAGS = [
  "script",
  "iframe",
  "object",
  "embed",
  "applet",
  "meta",
  "base",
  "link",
  "form",
  "input",
  "button",
  "textarea",
  "select",
  "foreignObject",
  "img",
  "image",
  "feImage",
  "audio",
  "video",
  "source",
  "track",
  "mpath",
  "discard",
  "animate",
  "set",
  "animateMotion",
  "animateTransform",
];

const FORBIDDEN_FETCH_ATTRS = new Set([
  "src",
  "srcset",
  "ping",
  "poster",
  "data",
  "action",
  "formaction",
]);

// Strip CSS comments and decode CSS backslash escapes so obfuscated
// `@import`, `url(...)` or `image-set(...)` tokens cannot slip past regexes.
function normalizeCss(css: string): string {
  return css
    .replace(/\/\*[\s\S]*?(?:\*\/|$)/g, "")
    .replace(/\\(?:\r\n|[\n\r\f])/g, "")
    .replace(
      /\\(?:([0-9a-fA-F]{1,6})[ \t\r\n\f]?|([^\r\n\f]))/g,
      (_, hex: string | undefined, lit: string | undefined) => {
        if (hex !== undefined) {
          const cp = parseInt(hex, 16);
          return cp > 0 && cp <= 0x10ffff ? String.fromCodePoint(cp) : "";
        }
        return lit ?? "";
      },
    );
}

function hasUnsafeCssFetch(css: string): boolean {
  return /@import|(?:url|image-set)\s*\(\s*['"]?\s*(?!#)/i.test(
    normalizeCss(css),
  );
}

// sanitizeMermaidSvg is defence in depth over Mermaid's strict mode: the
// sources come from agents and GitHub comments, so every element, event
// attribute and link in the SVG is checked again before it reaches the DOM.
// Elements that can fetch (images, embedded HTML) or rewrite attributes
// (SMIL animation) are dropped; links keep only safeHref destinations.
export function sanitizeMermaidSvg(
  rawSvg: string,
  win?: (Window & typeof globalThis) | null,
): string {
  const ParserCtor =
    win?.DOMParser ?? (typeof DOMParser === "function" ? DOMParser : undefined);
  if (!ParserCtor) throw new Error("No DOMParser to check the diagram with");
  const doc = new ParserCtor().parseFromString(rawSvg ?? "", "text/html");
  const svg = doc.body.querySelector("svg");
  if (!svg) throw new Error("Mermaid did not produce an SVG element");

  for (const el of Array.from(svg.querySelectorAll("*"))) {
    const tag = el.localName.toLowerCase();
    if (FORBIDDEN_SVG_TAGS.some((t) => t.toLowerCase() === tag)) el.remove();
  }
  for (const el of [svg, ...Array.from(svg.querySelectorAll("*"))]) {
    for (const attr of Array.from(el.attributes)) {
      const name = attr.name.toLowerCase();
      if (name.startsWith("on") || FORBIDDEN_FETCH_ATTRS.has(name)) {
        el.removeAttribute(attr.name);
        continue;
      }
      if (name === "href" || name === "xlink:href") {
        const val = attr.value.trim();
        // Marker and clip-path references (#arrowhead) stay.
        if (val.startsWith("#")) continue;
        // Only a link may point outside the diagram: on any other element
        // (<use>, <pattern>, ...) a remote href is fetched just by drawing.
        if (el.localName.toLowerCase() !== "a") {
          el.removeAttribute(attr.name);
          continue;
        }
        const safe = safeHref(val);
        if (!safe) el.removeAttribute(attr.name);
        else el.setAttribute(attr.name, safe);
        continue;
      }
      if (name === "style") {
        if (
          /(?:javascript\s*:|vbscript\s*:|expression\s*\()/i.test(
            normalizeCss(attr.value),
          ) ||
          hasUnsafeCssFetch(attr.value)
        ) {
          el.removeAttribute(attr.name);
        }
        continue;
      }
      // SVG presentation attributes (fill, filter, clip-path, mask, marker-*)
      // also accept functional url(...) references; only internal #ids stay.
      if (name !== "aria-label" && hasUnsafeCssFetch(attr.value)) {
        el.removeAttribute(attr.name);
      }
    }
    if (el.localName === "a") {
      const href =
        el.getAttribute("href") ?? el.getAttribute("xlink:href") ?? "";
      if (href && !href.startsWith("#")) {
        el.setAttribute("target", "_blank");
        el.setAttribute("rel", "noopener noreferrer");
      }
    }
  }
  // Mermaid's own <style> may only style the diagram: no @import and no
  // url() fetch beyond local fragments.
  for (const style of Array.from(svg.querySelectorAll("style"))) {
    const css = style.textContent ?? "";
    if (hasUnsafeCssFetch(css)) style.remove();
  }

  // Scale down to the pane rather than overflow it, but not below three
  // quarters of natural size: past that labels stop being legible, so a
  // wide diagram on a phone scrolls inside its canvas instead.
  svg.removeAttribute("height");
  let style = svg.getAttribute("style") || "";
  if (!/max-width/i.test(style)) {
    style = `${style ? `${style}; ` : ""}max-width: 100%; height: auto;`;
  }
  const natural = Number(
    (svg.getAttribute("viewBox") || "").trim().split(/[\s,]+/)[2],
  );
  if (natural > 0 && !/min-width/i.test(style)) {
    style = `${style}; min-width: ${Math.round(natural * 0.75)}px;`;
  }
  svg.setAttribute("style", style.replace(/;\s*;/g, ";"));
  return svg.outerHTML;
}

// ---------- Lazy render queue ----------

interface MermaidApi {
  initialize(config: unknown): void;
  render(id: string, text: string): Promise<{ svg: string }>;
}

type Loader = () => Promise<MermaidApi>;

const defaultLoader: Loader = () =>
  import("mermaid").then((mod) => (mod.default ?? mod) as MermaidApi);

let loader: Loader = defaultLoader;
let mermaidPromise: Promise<MermaidApi> | null = null;

// setMermaidLoaderForTest swaps the import("mermaid") seam; pass null to
// restore it.
export function setMermaidLoaderForTest(next: Loader | null): void {
  loader = next ?? defaultLoader;
  mermaidPromise = null;
  svgCache.clear();
  renderQueue = Promise.resolve();
}

function loadMermaid(): Promise<MermaidApi> {
  if (!mermaidPromise) {
    mermaidPromise = loader().catch((err) => {
      mermaidPromise = null;
      throw err;
    });
  }
  return mermaidPromise;
}

const MAX_CACHE_ENTRIES = 100;
const svgCache = new Map<string, string>();

function cacheKey(mode: MermaidMode, source: string): string {
  return `${mode}\0${source}`;
}

export function cachedMermaidSvg(
  mode: MermaidMode,
  source: string,
): string | null {
  const key = cacheKey(mode, source.trim());
  const hit = svgCache.get(key);
  if (hit === undefined) return null;
  svgCache.delete(key);
  svgCache.set(key, hit);
  return hit;
}

function remember(mode: MermaidMode, source: string, svg: string): void {
  const key = cacheKey(mode, source);
  svgCache.delete(key);
  if (svgCache.size >= MAX_CACHE_ENTRIES) {
    const oldest = svgCache.keys().next().value;
    if (oldest !== undefined) svgCache.delete(oldest);
  }
  svgCache.set(key, svg);
}

let nextRenderId = 0;
let renderQueue: Promise<unknown> = Promise.resolve();
const RENDER_TIMEOUT_MS = 10000;

function withTimeout<T>(p: Promise<T>, ms: number): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(
      () => reject(new Error("Mermaid diagram render timed out")),
      ms,
    );
  });
  return Promise.race([p, timeout]).finally(() => clearTimeout(timer));
}

const MERMAID_DIRECTIVE_RE =
  /%{2}\{\s*(?:(\w+)\s*:|(\w+))\s*(?:(\w+)|((?:(?!\}%{2}).|\r?\n)*))?\s*(?:\}%{2})?/gi;

const UNTERMINATED_SHAPE_BLOCK = "\0unterminated";

// Strip leading YAML front-matter (`---\n...\n---\n`) the same way Mermaid's
// `matchFrontMatter` does before handing diagram text to the lexer.
function stripMermaidFrontMatter(text: string): string {
  let cursor = 0;
  while (cursor < text.length && /[ \t]/.test(text[cursor])) cursor++;
  const indent = text.slice(0, cursor);
  if (!text.startsWith("---", cursor)) return text;

  const openingEnds: number[] = [];
  for (let i = cursor + 3; i < text.length && /\s/.test(text[i]); i++) {
    if (text[i] === "\n") openingEnds.push(i);
  }
  if (openingEnds.length === 0) return text;

  const closingFence = `${indent}---`;
  const closingEnd = (i: number): number => {
    if (text[i] !== "\n" || !text.startsWith(closingFence, i + 1)) return -1;
    let last = -1;
    for (
      let k = i + 1 + closingFence.length;
      k < text.length && /\s/.test(text[k]);
      k++
    ) {
      if (text[k] === "\n") last = k;
    }
    return last;
  };

  let lastClosingFence = -1;
  for (let i = openingEnds[0] + 1; i < text.length; i++) {
    if (closingEnd(i) !== -1) lastClosingFence = i;
  }
  if (lastClosingFence === -1) return text;

  let openingEnd = openingEnds[0];
  for (const candidate of openingEnds) {
    if (candidate < lastClosingFence) openingEnd = candidate;
  }
  for (let i = openingEnd + 1; i <= lastClosingFence; i++) {
    const matchEnd = closingEnd(i);
    if (matchEnd !== -1) return text.slice(matchEnd + 1);
  }
  return text;
}

function encodeMermaidEntities(text: string): string {
  const encoded = text
    .replace(/style.*:\S*#.*;/g, (s) => s.slice(0, -1))
    .replace(/classDef.*:\S*#.*;/g, (s) => s.slice(0, -1))
    .replace(/#\w+;/g, (s) => {
      const inner = s.slice(1, -1);
      return /^\+?\d+$/.test(inner)
        ? `\ufb02°°${inner}¶ß`
        : `\ufb02°${inner}¶ß`;
    });
  return `${encoded}\n`;
}

// Mirror Mermaid 12.2.0's `preprocessDiagram` + `encodeEntities` pipeline across
// both comment-stripped (`code.cleaned`) and comment-preserving
// (`code.withComments`, used when `db.preserveCommentsWhenParsing` is set)
// inputs so directives, `%%` comment lines, CR line endings and `#word;`
// placeholders cannot split or comment-hide a shape token.
function preprocessMermaidVariants(src: string): {
  cleaned: string;
  withComments: string;
} {
  const normalized = src
    .replace(/\r\n?/g, "\n")
    .replace(
      /<(\w+)([^>]*)>/g,
      (_, tag: string, attrs: string) =>
        "<" + tag + attrs.replace(/="([^"]*)"/g, "='$1'") + ">",
    );
  const withoutFrontMatter = stripMermaidFrontMatter(normalized);
  const withoutDirectives = withoutFrontMatter.replace(
    MERMAID_DIRECTIVE_RE,
    "",
  );
  const cleanedRaw = withoutDirectives
    .replace(/^\s*%%(?!\{)[^\n]+\n?/gm, "")
    .trimStart();
  const cleaned = encodeMermaidEntities(cleanedRaw);
  const withComments =
    withoutDirectives.trimStart() === cleanedRaw
      ? cleaned
      : encodeMermaidEntities(withoutDirectives);
  return { cleaned, withComments };
}

// Read one `@{...}` shape block starting right after `@{` using Mermaid's Jison
// `shapeData` / `shapeDataStr` rules: `"` toggles string mode (collapsing
// `\n\s*` to `<br/>` without backslash escapes) and the first unquoted `}`
// closes the block.
function readShapeBlockAt(
  src: string,
  startAfterBrace: number,
): { block: string; endIndex: number } {
  let j = startAfterBrace;
  let buf = "";
  let inStr = false;
  let closed = false;
  while (j < src.length) {
    const ch = src[j];
    if (!inStr) {
      if (ch === '"') {
        inStr = true;
        buf += '"';
        j++;
        continue;
      }
      if (ch === "}") {
        closed = true;
        j++;
        break;
      }
      buf += ch;
      j++;
    } else {
      if (ch === '"') {
        inStr = false;
        buf += '"';
        j++;
        continue;
      }
      if (ch === "\n") {
        j++;
        while (j < src.length && /\s/.test(src[j])) j++;
        buf += "<br/>";
        continue;
      }
      buf += ch;
      j++;
    }
  }
  return {
    block: closed && !inStr ? buf : UNTERMINATED_SHAPE_BLOCK,
    endIndex: j,
  };
}

// Extract top-level `@{...}` shape blocks while skipping outer `"..."` and
// `` "`...`" `` label strings so literal `@{` inside a quoted node label does
// not look like an unterminated or malformed shape block.
function extractTopLevelShapeBlocks(src: string): string[] {
  const blocks: string[] = [];
  let outerStr: "none" | "str" | "md" = "none";
  for (let i = 0; i < src.length - 1; i++) {
    if (outerStr === "md") {
      if (src[i] === "`" && src[i + 1] === '"') {
        outerStr = "none";
        i++;
      }
      continue;
    }
    if (outerStr === "str") {
      if (src[i] === '"') outerStr = "none";
      continue;
    }
    if (src[i] === '"' && src[i + 1] === "`") {
      outerStr = "md";
      i++;
      continue;
    }
    if (src[i] === '"') {
      outerStr = "str";
      continue;
    }
    if (src[i] === "@" && src[i + 1] === "{") {
      const { block, endIndex } = readShapeBlockAt(src, i + 2);
      blocks.push(block);
      i = endIndex - 1;
    }
  }
  return blocks;
}

// Mirror AgentFlowDB's `stripLineTrailingCommas` fallback so multi-line shape
// blocks with trailing commas are parsed identically when `js-yaml` rejects the
// unstripped text.
function stripLineTrailingCommas(body: string): string {
  let inSingle = false;
  let inDouble = false;
  let flowDepth = 0;
  let blockScalarIndent: number | undefined;
  const out: string[] = [];
  for (const line of body.split("\n")) {
    if (blockScalarIndent !== undefined) {
      const indent = line.length - line.trimStart().length;
      if (line.trim() === "" || indent > blockScalarIndent) {
        out.push(line);
        continue;
      }
      blockScalarIndent = undefined;
    }
    let commentStart = -1;
    for (let i = 0; i < line.length; i++) {
      const ch = line[i];
      if (inDouble) {
        if (ch === "\\") i++;
        else if (ch === '"') inDouble = false;
      } else if (inSingle) {
        if (ch === "'") inSingle = false;
      } else if (ch === '"') {
        inDouble = true;
      } else if (ch === "'") {
        inSingle = true;
      } else if (
        ch === "#" &&
        (i === 0 || line[i - 1] === " " || line[i - 1] === "\t")
      ) {
        commentStart = i;
        break;
      } else if (ch === "[" || ch === "{") {
        flowDepth++;
      } else if (ch === "]" || ch === "}") {
        flowDepth = Math.max(0, flowDepth - 1);
      }
    }
    if (inSingle || inDouble || flowDepth > 0) {
      out.push(line);
      continue;
    }
    const code = commentStart >= 0 ? line.slice(0, commentStart) : line;
    const comment = commentStart >= 0 ? line.slice(commentStart) : "";
    if (/:\s*[>|][\d+-]*\s*$/.test(code)) {
      blockScalarIndent = line.length - line.trimStart().length;
      out.push(line);
      continue;
    }
    out.push(code.replace(/,([\t ]*)$/, "$1") + comment);
  }
  return out.join("\n");
}

function docHasImgKey(value: unknown, seen: Set<unknown>): boolean {
  if (!value || typeof value !== "object") return false;
  if (seen.has(value)) return false;
  seen.add(value);
  if (Array.isArray(value)) {
    return value.some((item) => docHasImgKey(item, seen));
  }
  const obj = value as Record<string, unknown>;
  for (const key of Object.getOwnPropertyNames(obj)) {
    if (key.trim().toLowerCase() === "img") return true;
    if (docHasImgKey(obj[key], seen)) return true;
  }
  return false;
}

// Run a `@{...}` block through the same `js-yaml` `JSON_SCHEMA` load that
// Mermaid's `FlowDB.addVertex` uses (`{\n...\n}` for single-line blocks, raw
// text + `\n` when it contains a newline). Refuse if `img` appears at any
// object level or if `load` throws.
function blockHasImgKey(block: string): boolean {
  if (block === UNTERMINATED_SHAPE_BLOCK) return true;
  const yamlData = block.includes("\n") ? `${block}\n` : `{\n${block}\n}`;
  try {
    const doc = loadYaml(yamlData, { schema: JSON_SCHEMA });
    return docHasImgKey(doc, new Set());
  } catch {
    return true;
  }
}

// Belt-and-suspenders check independent of outer quote/lexer state: test every
// raw `@{` offset in `src` without skipping ahead, so no earlier token in any
// diagram grammar can mask a subsequent `@{...}` block that parses with `img`.
function anyRawShapeOffsetHasImgKey(src: string): boolean {
  for (let i = 0; i < src.length - 1; i++) {
    if (src[i] !== "@" || src[i + 1] !== "{") continue;
    const { block } = readShapeBlockAt(src, i + 2);
    if (block === UNTERMINATED_SHAPE_BLOCK) continue;
    const yamlData = block.includes("\n") ? `${block}\n` : `{\n${block}\n}`;
    try {
      const doc = loadYaml(yamlData, { schema: JSON_SCHEMA });
      if (docHasImgKey(doc, new Set())) return true;
    } catch {
      if (block.includes("\n")) {
        const stripped = stripLineTrailingCommas(block);
        if (stripped !== block) {
          try {
            const doc = loadYaml(`${stripped}\n`, { schema: JSON_SCHEMA });
            if (docHasImgKey(doc, new Set())) return true;
          } catch {
            // Non-shape or invalid block; top-level malformed blocks fail in
            // extractTopLevelShapeBlocks.
          }
        }
      }
    }
  }
  return false;
}

function decodeHtmlEntities(s: string): string {
  return s
    .replace(/&#(\d{1,7});/g, (_, dec: string) => {
      const cp = parseInt(dec, 10);
      return cp > 0 && cp <= 0x10ffff ? String.fromCodePoint(cp) : "";
    })
    .replace(/&#x([0-9a-fA-F]{1,6});/g, (_, hex: string) => {
      const cp = parseInt(hex, 16);
      return cp > 0 && cp <= 0x10ffff ? String.fromCodePoint(cp) : "";
    })
    .replace(/&quot;/gi, '"')
    .replace(/&apos;/gi, "'")
    .replace(/&equals;/gi, "=")
    .replace(/&lt;/gi, "<")
    .replace(/&gt;/gi, ">")
    .replace(/&tab;/gi, "\t")
    .replace(/&newline;/gi, "\n")
    .replace(/&amp;/gi, "&");
}

function hasSequenceRemoteIcon(
  src: string,
  isSequenceDiagram: boolean,
): boolean {
  if (
    isSequenceDiagram &&
    /(?:^|[;\n])[ \t]*details(?=[ \t]+[^/\\+()<>:\n,;\s-])[ \t]+[^:\n;#]+:/i.test(
      src,
    )
  ) {
    return true;
  }
  const re =
    /(?:^|[;\n])[ \t]*properties[ \t]+[^:\n;#]+:[ \t]*(?:[:]?(?:no)?wrap\b[ \t]*)?([^\n]*)/gi;
  let m: RegExpExecArray | null;
  while ((m = re.exec(src)) !== null) {
    const full = m[1].trim().replace(/;+\s*$/, "");
    const lexed = m[1].split(/[#;]/, 1)[0].trim();
    const candidates = new Set<string>();
    for (const piece of [lexed, full]) {
      if (!piece) continue;
      const withoutHtml = piece
        .replace(/<!--[\s\S]*?(?:-->|$)/g, "")
        .replace(/<(?:style|script)\b[^>]*>[\s\S]*?<\/(?:style|script)>/gi, "")
        .replace(/<\/?[A-Za-z!][^>]*>/g, "");
      candidates.add(piece);
      candidates.add(decodeHtmlEntities(piece));
      candidates.add(withoutHtml);
      candidates.add(decodeHtmlEntities(withoutHtml));
    }
    let parsedAnyObject = false;
    for (const candidate of candidates) {
      try {
        const parsed = JSON.parse(candidate) as unknown;
        if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
          parsedAnyObject = true;
          const icon = (parsed as Record<string, unknown>)["icon"];
          if (typeof icon === "string") {
            const trimmed = icon.trim();
            if (trimmed && !trimmed.startsWith("@")) return true;
          }
        }
      } catch {
        // Try next candidate; sequenceDiagram fails closed below.
      }
    }
    if (isSequenceDiagram && (!parsedAnyObject || lexed.includes("<"))) {
      return true;
    }
  }
  return false;
}

// hasImageShape reports a diagram written with Mermaid's YAML/JSON image
// shape (`A@{ img: "https://...", ... }`, parsed with js-yaml's JSON_SCHEMA to
// match FlowDB.addVertex across anchors, tags, aliases, escapes and block
// mappings) or a sequenceDiagram actor `icon` property, whose URL Mermaid
// loads in the live document before sanitizeMermaidSvg runs.
export function hasImageShape(src: string): boolean {
  const { cleaned, withComments } = preprocessMermaidVariants(src);
  const variants =
    cleaned === withComments ? [cleaned] : [cleaned, withComments];
  for (const variant of variants) {
    if (!variant.includes("@{")) continue;
    if (extractTopLevelShapeBlocks(variant).some(blockHasImgKey)) return true;
    if (anyRawShapeOffsetHasImgKey(variant)) return true;
  }
  const isSequence = /^\s*sequenceDiagram\b/i.test(cleaned);
  return (
    hasSequenceRemoteIcon(src, isSequence) ||
    hasSequenceRemoteIcon(cleaned, isSequence)
  );
}

function sourceHasExternalStyleUrl(src: string): boolean {
  const stmtRe = /(?:^|[;\n])[ \t]*(?:classDef|style|linkStyle)\b([^\n;]*)/gi;
  let m: RegExpExecArray | null;
  while ((m = stmtRe.exec(src)) !== null) {
    if (hasUnsafeCssFetch(m[1])) return true;
  }
  const c4Re =
    /Update(?:Element|Boundary|Rel)Style\s*\(((?:[^)"']|"[^"]*"|'[^']*')*)\)/gi;
  while ((m = c4Re.exec(src)) !== null) {
    if (hasUnsafeCssFetch(m[1])) return true;
  }
  return false;
}

// hasExternalStyleUrl reports a `classDef`, `style`, `linkStyle` or C4 style
// directive carrying `@import` or a non-`#` `url(...)` / `image-set(...)`,
// which Mermaid would attach to `document.body` while measuring the diagram.
export function hasExternalStyleUrl(src: string): boolean {
  const { cleaned, withComments } = preprocessMermaidVariants(src);
  return (
    sourceHasExternalStyleUrl(src) ||
    sourceHasExternalStyleUrl(cleaned) ||
    (withComments !== cleaned && sourceHasExternalStyleUrl(withComments))
  );
}

// renderMermaidSvg renders one source to sanitized SVG. Renders run one
// at a time (mermaid.initialize is global) and a failure never stalls the
// diagrams behind it.
export function renderMermaidSvg(
  source: string,
  mode: MermaidMode,
  doc?: Document | null,
): Promise<string> {
  const trimmed = source.trim();
  if (!trimmed) return Promise.reject(new Error("Empty Mermaid diagram"));
  // Mermaid fetches an image shape's img: URL (and mounts actor icons / user
  // style sheets in document.body) while laying the diagram out, before
  // sanitizeMermaidSvg sees anything, so such diagrams are refused upfront.
  if (hasImageShape(trimmed)) {
    return Promise.reject(new Error("Diagrams with images are not drawn"));
  }
  if (hasExternalStyleUrl(trimmed)) {
    return Promise.reject(
      new Error("Diagrams with external style URLs are not drawn"),
    );
  }
  const cached = cachedMermaidSvg(mode, trimmed);
  if (cached) return Promise.resolve(cached);

  const job = renderQueue.then(async () => {
    const again = cachedMermaidSvg(mode, trimmed);
    if (again) return again;
    const mermaid = await loadMermaid();
    const id = `grain-mermaid-${++nextRenderId}`;
    try {
      mermaid.initialize(mermaidConfigForMode(mode));
      const { svg } = await withTimeout(
        mermaid.render(id, trimmed),
        RENDER_TIMEOUT_MS,
      );
      const clean = sanitizeMermaidSvg(
        svg,
        doc?.defaultView as (Window & typeof globalThis) | null,
      );
      remember(mode, trimmed, clean);
      return clean;
    } finally {
      // Mermaid leaves its measuring element behind when render throws.
      for (const stray of [id, `d${id}`]) {
        const el = doc?.getElementById(stray);
        if (el && el.parentElement === doc?.body) el.remove();
      }
    }
  });
  renderQueue = job.catch(() => undefined);
  return job;
}

// ---------- Hydration ----------

const PHONE_QUERY = "(max-width: 599px)";

function syncView(block: HTMLElement, view: string): void {
  block.dataset["view"] = view;
  for (const btn of Array.from(
    block.querySelectorAll<HTMLButtonElement>("[data-mermaid-view]"),
  )) {
    btn.setAttribute(
      "aria-pressed",
      String(btn.dataset["mermaidView"] === view),
    );
  }
}

function renderPane(pane: HTMLElement, mode: MermaidMode, doc: Document) {
  const canvas = pane.querySelector<HTMLElement>("[data-mermaid-canvas]");
  const source =
    pane.querySelector(".mermaid-source code")?.textContent?.trim() ?? "";
  if (!canvas) return;
  if (canvas.dataset["mode"] === mode) return;
  canvas.dataset["mode"] = mode;
  const show = (svg: string) => {
    if (!canvas.isConnected || canvas.dataset["mode"] !== mode) return;
    // Safe: svg came out of Mermaid's strict mode and sanitizeMermaidSvg.
    canvas.innerHTML = svg;
    // A diagram wider than a phone scrolls; keyboards must reach it too.
    canvas.tabIndex = 0;
    pane.classList.remove("is-error");
  };
  const cached = cachedMermaidSvg(mode, source);
  if (cached) {
    show(cached);
    return;
  }
  renderMermaidSvg(source, mode, doc).then(show, (err: unknown) => {
    if (!canvas.isConnected || canvas.dataset["mode"] !== mode) return;
    const why =
      err instanceof Error && err.message
        ? err.message.split("\n")[0].slice(0, 200)
        : "invalid Mermaid syntax";
    const note = doc.createElement("span");
    note.className = "mermaid-note mermaid-error";
    note.setAttribute("role", "status");
    note.textContent = `Diagram could not be rendered (${why}). Showing the Mermaid source.`;
    canvas.replaceChildren(note);
    canvas.removeAttribute("tabindex");
    pane.classList.add("is-error");
  });
}

// hydrateMermaid renders every .mermaid-block under root in mode. It is
// cheap to call again: a pane already drawn (or drawing) in this mode is
// left alone, so a theme change redraws and a repeat call does nothing.
export function hydrateMermaid(
  root: HTMLElement,
  mode: MermaidMode,
  doc: Document,
): void {
  const blocks = root.querySelectorAll<HTMLElement>("[data-mermaid]");
  for (const block of Array.from(blocks)) {
    if (!block.dataset["view"]) {
      const phone = !!doc.defaultView?.matchMedia?.(PHONE_QUERY).matches;
      const pair = block.dataset["mermaid"] === "pair";
      syncView(block, pair ? (phone ? "before" : "split") : "diagram");
    }
    for (const p of Array.from(
      block.querySelectorAll<HTMLElement>("[data-mermaid-pane]"),
    )) {
      renderPane(p, mode, doc);
    }
  }
}

// handleMermaidClick switches a block's view when one of its toolbar
// buttons is pressed. It reports whether the click was one.
export function handleMermaidClick(target: HTMLElement | null): boolean {
  const btn = target?.closest<HTMLButtonElement>("[data-mermaid-view]");
  const block = btn?.closest<HTMLElement>("[data-mermaid]");
  if (!btn || !block) return false;
  syncView(block, btn.dataset["mermaidView"] ?? "diagram");
  return true;
}
