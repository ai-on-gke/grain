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

import { safeUrl } from "./safeUrl.js";

// Lazy-loaded, theme-synced Mermaid rendering with a serialized queue,
// bounded SVG cache, and defense-in-depth SVG sanitization.
//
// Why lazy (`import("mermaid")`): Mermaid and its layout engines weigh
// well over a megabyte minified. Keeping it behind a dynamic import means
// the main UI bundle pays only for the wrapper until a task, plan or
// comment on screen actually contains a ```mermaid fence.
//
// Why serialized: `mermaid.initialize` mutates module-global configuration
// inside Mermaid, while `mermaid.render` is asynchronous (loading per-type
// grammar chunks on demand). Running renders one at a time guarantees the
// active palette matches the diagram being drawn and avoids ID collisions
// when several diagrams mount together.

const FONT_FAMILY =
  '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif';

// Tuned to match src/theme.js and src/style.css (bronze on warm paper in
// light mode, wheat on dark slate in dark mode). Authors can still colour
// individual added/removed nodes in Before/After diagrams with Mermaid's
// own `style` / `classDef` directives.
const THEME_VARIABLES = {
  light: {
    darkMode: false,
    background: "#ffffff",
    fontFamily: FONT_FAMILY,
    fontSize: "13px",
    primaryColor: "#f7f3ea",
    primaryTextColor: "#24221d",
    primaryBorderColor: "#8a6a2e",
    secondaryColor: "#eef3f7",
    secondaryTextColor: "#24221d",
    secondaryBorderColor: "#3f6b8a",
    tertiaryColor: "#f3f2ee",
    tertiaryTextColor: "#24221d",
    tertiaryBorderColor: "#c9c4b8",
    lineColor: "#6e6a60",
    textColor: "#24221d",
    mainBkg: "#f7f3ea",
    nodeBorder: "#8a6a2e",
    clusterBkg: "#f7f6f3",
    clusterBorder: "#d5d1c7",
    titleColor: "#24221d",
    edgeLabelBackground: "#ffffff",
    actorBkg: "#f7f3ea",
    actorBorder: "#8a6a2e",
    actorTextColor: "#24221d",
    actorLineColor: "#6e6a60",
    signalColor: "#24221d",
    signalTextColor: "#24221d",
    labelBoxBkgColor: "#f7f6f3",
    labelBoxBorderColor: "#d5d1c7",
    labelTextColor: "#24221d",
    noteBkgColor: "#f6eeda",
    noteBorderColor: "#8a6a2e",
    noteTextColor: "#24221d",
  },
  dark: {
    darkMode: true,
    background: "#14161a",
    fontFamily: FONT_FAMILY,
    fontSize: "13px",
    primaryColor: "#262118",
    primaryTextColor: "#e9e7e1",
    primaryBorderColor: "#d9a441",
    secondaryColor: "#19232c",
    secondaryTextColor: "#e9e7e1",
    secondaryBorderColor: "#7fa8c7",
    tertiaryColor: "#1c1f26",
    tertiaryTextColor: "#e9e7e1",
    tertiaryBorderColor: "#3d4049",
    lineColor: "#9b988f",
    textColor: "#e9e7e1",
    mainBkg: "#262118",
    nodeBorder: "#d9a441",
    clusterBkg: "#181a20",
    clusterBorder: "#343741",
    titleColor: "#e9e7e1",
    edgeLabelBackground: "#14161a",
    actorBkg: "#262118",
    actorBorder: "#d9a441",
    actorTextColor: "#e9e7e1",
    actorLineColor: "#9b988f",
    signalColor: "#e9e7e1",
    signalTextColor: "#e9e7e1",
    labelBoxBkgColor: "#1c1f24",
    labelBoxBorderColor: "#343741",
    labelTextColor: "#e9e7e1",
    noteBkgColor: "#2a2314",
    noteBorderColor: "#d9a441",
    noteTextColor: "#e9e7e1",
  },
};

export function mermaidConfigForMode(mode) {
  const paletteKey = mode === "dark" ? "dark" : "light";
  return {
    startOnLoad: false,
    securityLevel: "strict",
    // Pure SVG <text>/<tspan> labels rather than <foreignObject> HTML:
    // keeps untrusted node labels from triggering outbound <img> beacons or
    // hanging Mermaid's waitForImages pass, and scales crisply at any zoom.
    htmlLabels: false,
    // Lock security-sensitive keys against `%%{init: ...}%%` overrides in
    // untrusted task/comment Markdown.
    secure: [
      "secure",
      "securityLevel",
      "startOnLoad",
      "maxTextSize",
      "maxEdges",
      "htmlLabels",
      "flowchart",
    ],
    suppressErrorRendering: true,
    theme: "base",
    fontFamily: FONT_FAMILY,
    themeVariables: THEME_VARIABLES[paletteKey],
    flowchart: {
      useMaxWidth: true,
      htmlLabels: false,
      curve: "basis",
    },
    sequence: {
      useMaxWidth: true,
    },
  };
}

const FORBIDDEN_SVG_TAGS = new Set([
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
]);

// Defense-in-depth sanitizer over Mermaid's SVG output. Even with
// `securityLevel: "strict"`, diagram sources come from agents and GitHub
// comments, so every tag, event attribute and link destination is checked
// against the same `safeUrl` policy Markdown.jsx enforces on prose.
export function sanitizeMermaidSvg(rawSvg) {
  const parser = new DOMParser();
  const doc = parser.parseFromString(String(rawSvg ?? ""), "text/html");
  const svg = doc.body.querySelector("svg");
  if (!svg) {
    throw new Error("Mermaid did not produce an SVG element");
  }

  for (const tag of FORBIDDEN_SVG_TAGS) {
    for (const bad of [...svg.querySelectorAll(tag)]) {
      bad.remove();
    }
  }

  const allNodes = [svg, ...svg.querySelectorAll("*")];
  for (const el of allNodes) {
    for (const attr of [...el.attributes]) {
      const name = attr.name.toLowerCase();
      if (name.startsWith("on")) {
        el.removeAttribute(attr.name);
        continue;
      }
      if (name === "href" || name === "xlink:href") {
        const val = attr.value.trim();
        // Keep internal SVG fragment references (`#arrowhead`, `#clip-0`)
        // that Mermaid's markers and edges rely on.
        if (val.startsWith("#")) {
          continue;
        }
        const cleaned = safeUrl(val);
        if (!cleaned) {
          el.removeAttribute(attr.name);
        } else {
          el.setAttribute(attr.name, cleaned);
        }
        continue;
      }
      if (
        name === "style" &&
        /(?:javascript\s*:|vbscript\s*:|expression\s*\()/i.test(attr.value)
      ) {
        el.removeAttribute(attr.name);
      }
    }

    if (el.tagName.toLowerCase() === "a") {
      const href =
        el.getAttribute("href") ?? el.getAttribute("xlink:href") ?? "";
      if (href && !href.startsWith("#")) {
        el.setAttribute("target", "_blank");
        el.setAttribute("rel", "noopener noreferrer");
      }
    }
  }

  // Ensure the root SVG scales down cleanly inside narrow panes without
  // overflowing horizontally unless zoomed.
  svg.removeAttribute("height");
  const existingStyle = svg.getAttribute("style") || "";
  if (!/max-width/i.test(existingStyle)) {
    svg.setAttribute(
      "style",
      `${existingStyle ? `${existingStyle}; ` : ""}max-width: 100%; height: auto;`,
    );
  }

  return svg.outerHTML;
}

const MAX_CACHE_ENTRIES = 100;
const svgCache = new Map();

function cacheKey(mode, source) {
  return `${mode === "dark" ? "dark" : "light"}\0${String(source ?? "").trim()}`;
}

export function getCachedMermaidSvg(mode, source) {
  const key = cacheKey(mode, source);
  if (!svgCache.has(key)) return null;
  const hit = svgCache.get(key);
  // Refresh LRU order.
  svgCache.delete(key);
  svgCache.set(key, hit);
  return hit;
}

function setCachedMermaidSvg(mode, source, svg) {
  const key = cacheKey(mode, source);
  if (svgCache.has(key)) {
    svgCache.delete(key);
  } else if (svgCache.size >= MAX_CACHE_ENTRIES) {
    const oldest = svgCache.keys().next().value;
    svgCache.delete(oldest);
  }
  svgCache.set(key, svg);
}

export function clearMermaidCache() {
  svgCache.clear();
}

let mermaidModulePromise = null;
function loadMermaid() {
  if (!mermaidModulePromise) {
    mermaidModulePromise = import("mermaid").then((mod) => mod.default ?? mod);
  }
  return mermaidModulePromise;
}

let nextId = 0;
let renderQueue = Promise.resolve();
const RENDER_TIMEOUT_MS = 10000;

// MAX_DIAGRAM_CHARS is Mermaid's own maxTextSize default. Past it,
// mermaid.render does not throw: it swaps the source for a stock
// "Maximum text size in diagram exceeded" graph and resolves, so the
// reader got that graph and never the source they wrote. Refusing here
// instead sends the diagram down the same fallback a syntax error takes
// (DiagramCanvas), which shows the source and says why.
export const MAX_DIAGRAM_CHARS = 50000;

function withRenderTimeout(promise, ms) {
  let timer = null;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => {
      reject(new Error("Mermaid diagram render timed out"));
    }, ms);
  });
  return Promise.race([promise, timeout]).finally(() => {
    if (timer !== null) clearTimeout(timer);
  });
}

function cleanupTempElement(id) {
  if (typeof document === "undefined") return;
  for (const candidate of [id, `d${id}`]) {
    const stray = document.getElementById(candidate);
    if (stray && stray.parentElement === document.body) {
      stray.remove();
    }
  }
}

export function renderMermaidSvg(source, mode = "light") {
  const trimmed = String(source ?? "").trim();
  if (!trimmed) {
    return Promise.reject(new Error("Empty Mermaid diagram"));
  }
  if (trimmed.length > MAX_DIAGRAM_CHARS) {
    return Promise.reject(
      new Error(
        `diagram is ${trimmed.length.toLocaleString("en-US")} characters, ` +
          `over the ${MAX_DIAGRAM_CHARS.toLocaleString("en-US")} Mermaid renders`,
      ),
    );
  }
  const cached = getCachedMermaidSvg(mode, trimmed);
  if (cached) {
    return Promise.resolve(cached);
  }

  const job = renderQueue.then(async () => {
    const again = getCachedMermaidSvg(mode, trimmed);
    if (again) return again;

    const mermaid = await loadMermaid();
    const id = `grain-mermaid-${++nextId}`;
    try {
      mermaid.initialize(mermaidConfigForMode(mode));
      const { svg } = await withRenderTimeout(
        mermaid.render(id, trimmed),
        RENDER_TIMEOUT_MS,
      );
      const clean = sanitizeMermaidSvg(svg);
      setCachedMermaidSvg(mode, trimmed, clean);
      return clean;
    } finally {
      cleanupTempElement(id);
    }
  });

  // Advance the queue regardless of whether this diagram succeeded so one
  // syntax error never stalls subsequent diagrams on the page.
  renderQueue = job.catch(() => {});
  return job;
}
