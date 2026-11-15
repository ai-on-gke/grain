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

import { safeHref } from "../sessions/session-view";

// Lazy, serialized Mermaid rendering for ```mermaid fences in [gMarkdown]
// (the same contract as ui/src/mermaidRender.js). Mermaid weighs well over
// a megabyte, so it is reached only through a dynamic import: no page pays
// for it until a diagram is on screen. Renders run one at a time because
// mermaid.initialize mutates module-global config, so the palette always
// matches the diagram being drawn.

export type MermaidTheme = "light" | "dark";

// Colours come from the page's own tokens (styles.css) so diagrams follow
// both themes without a second palette to keep in step.
function token(style: CSSStyleDeclaration, name: string, fallback: string) {
  return style.getPropertyValue(`--md-sys-color-${name}`).trim() || fallback;
}

export function mermaidConfig(
  theme: MermaidTheme,
  style?: CSSStyleDeclaration,
) {
  const s =
    style ??
    (typeof document !== "undefined" && typeof getComputedStyle === "function"
      ? getComputedStyle(document.documentElement)
      : undefined);
  const dark = theme === "dark";
  const t = (name: string, light: string, darkVal: string) =>
    s ? token(s, name, dark ? darkVal : light) : dark ? darkVal : light;
  const surface = t("surface", "#ffffff", "#181a20");
  const onSurface = t("on-surface", "#1b1d22", "#e4e6eb");
  const container = t("surface-container", "#eff3f8", "#242830");
  const containerLow = t("surface-container-low", "#f7f9fc", "#1e2128");
  const primary = t("primary", "#0b57d0", "#a8c7fa");
  const outline = t("outline", "#727885", "#8b91a0");
  const outlineVariant = t("outline-variant", "#d8e0eb", "#383d4a");
  const secondaryContainer = t("secondary-container", "#c2e7ff", "#004a77");
  const warningContainer = t("warning-container", "#fef0c3", "#4a3400");
  const fontFamily =
    s?.getPropertyValue("--grain-font-body").trim() || "system-ui, sans-serif";
  return {
    startOnLoad: false,
    securityLevel: "strict" as const,
    // SVG <text> labels, not <foreignObject> HTML: untrusted labels cannot
    // load images, and the sanitizer below never has to admit HTML.
    htmlLabels: false,
    // %%{init}%% in a task description cannot loosen any of these.
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
    theme: "base" as const,
    fontFamily,
    themeVariables: {
      darkMode: dark,
      background: surface,
      fontFamily,
      fontSize: "13px",
      primaryColor: container,
      primaryTextColor: onSurface,
      primaryBorderColor: primary,
      secondaryColor: secondaryContainer,
      secondaryTextColor: onSurface,
      secondaryBorderColor: outline,
      tertiaryColor: containerLow,
      tertiaryTextColor: onSurface,
      tertiaryBorderColor: outlineVariant,
      lineColor: outline,
      textColor: onSurface,
      mainBkg: container,
      nodeBorder: primary,
      clusterBkg: containerLow,
      clusterBorder: outlineVariant,
      titleColor: onSurface,
      edgeLabelBackground: surface,
      actorBkg: container,
      actorBorder: primary,
      actorTextColor: onSurface,
      actorLineColor: outline,
      signalColor: onSurface,
      signalTextColor: onSurface,
      labelBoxBkgColor: containerLow,
      labelBoxBorderColor: outlineVariant,
      labelTextColor: onSurface,
      noteBkgColor: warningContainer,
      noteBorderColor: outline,
      noteTextColor: onSurface,
    },
    flowchart: { useMaxWidth: true, htmlLabels: false, curve: "basis" },
    sequence: { useMaxWidth: true },
  };
}

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
  "foreignobject",
  // An <image> or <feImage> with a remote href loads as soon as the
  // diagram is on screen: a tracking pixel, as Markdown images may not be.
  "image",
  "feImage",
  "feimage",
];

// sanitizeMermaidSvg is defense in depth over Mermaid's strict-mode output:
// diagram sources come from agents and people, so every element, event
// handler and link is checked again before the SVG reaches the page.
export function sanitizeMermaidSvg(raw: string): string {
  const doc = new DOMParser().parseFromString(String(raw ?? ""), "text/html");
  const svg = doc.body.querySelector("svg");
  if (!svg) throw new Error("Mermaid did not produce an SVG element");
  for (const tag of FORBIDDEN_SVG_TAGS) {
    for (const bad of Array.from(svg.getElementsByTagName(tag))) bad.remove();
  }
  // classDef and style directives land in the diagram's <style>; a url()
  // there could fetch a remote image (a tracking pixel) just by being
  // read, as Markdown images may not. Internal #references stay.
  for (const st of Array.from(svg.getElementsByTagName("style"))) {
    st.textContent = (st.textContent ?? "")
      .replace(/@import[^;]*;?/gi, "")
      .replace(/url\s*\(\s*(['"]?)(?!#)[^)]*\)/gi, "none");
  }
  for (const el of [svg, ...Array.from(svg.querySelectorAll("*"))]) {
    for (const attr of Array.from(el.attributes)) {
      const name = attr.name.toLowerCase();
      if (name.startsWith("on")) {
        el.removeAttribute(attr.name);
      } else if (name === "href" || name === "xlink:href") {
        const val = attr.value.trim();
        // Markers and clip paths refer to #ids inside the SVG itself.
        if (val.startsWith("#")) continue;
        // Only a link may point outside the diagram: on any other element
        // (<use>, <pattern>, ...) a remote href is fetched just by drawing.
        if (el.tagName.toLowerCase() !== "a") {
          el.removeAttribute(attr.name);
          continue;
        }
        const safe = safeHref(val);
        if (!safe) el.removeAttribute(attr.name);
        else el.setAttribute(attr.name, safe);
      } else if (
        name === "style" &&
        /(?:javascript\s*:|vbscript\s*:|expression\s*\(|url\s*\(\s*['"]?(?!#))/i.test(
          attr.value,
        )
      ) {
        el.removeAttribute(attr.name);
      }
    }
    if (el.tagName.toLowerCase() === "a") {
      el.setAttribute("target", "_blank");
      el.setAttribute("rel", "noopener noreferrer");
    }
  }
  // Scale down inside narrow panes; the full-screen viewer zooms it back up.
  svg.removeAttribute("height");
  svg.setAttribute("role", "img");
  if (!svg.hasAttribute("aria-label") && !svg.hasAttribute("aria-labelledby")) {
    svg.setAttribute("aria-label", "Diagram");
  }
  const style = svg.getAttribute("style") || "";
  if (!/max-width/i.test(style)) {
    svg.setAttribute(
      "style",
      `${style ? `${style}; ` : ""}max-width: 100%; height: auto;`,
    );
  }
  return svg.outerHTML;
}

const MAX_CACHE = 100;
const cache = new Map<string, string>();

function key(theme: MermaidTheme, src: string) {
  return `${theme}\0${src}`;
}

export function cachedMermaidSvg(
  theme: MermaidTheme,
  src: string,
): string | null {
  return cache.get(key(theme, src.trim())) ?? null;
}

interface MermaidApi {
  initialize(config: unknown): void;
  render(id: string, text: string): Promise<{ svg: string }>;
}

let loading: Promise<MermaidApi> | null = null;
function loadMermaid(): Promise<MermaidApi> {
  loading ??= import("mermaid").then(
    (m) => (m.default ?? m) as unknown as MermaidApi,
  );
  // A failed chunk load (offline, a deploy in between) is retried next time.
  loading.catch(() => (loading = null));
  return loading;
}

let nextId = 0;

// withFreshIds gives one copy of a rendered diagram ids of its own. Mermaid
// scopes its <style>, markers and node ids under the root svg id, so the
// same diagram shown twice (a cache hit, or inline and in the full-screen
// viewer) would otherwise put duplicate ids on the page.
export function withFreshIds(svg: string): string {
  const root = /<svg[^>]*\sid="(g-mermaid-\d+)"/.exec(svg)?.[1];
  if (!root) return svg;
  return svg.replace(
    new RegExp(`${root}(?!\\d)`, "g"),
    `g-mermaid-${++nextId}`,
  );
}

let queue: Promise<unknown> = Promise.resolve();
const RENDER_TIMEOUT_MS = 10_000;

// hasImageShape reports a node written with Mermaid's image shape
// (A@{ img: "https://...", ... }), whose URL Mermaid loads itself.
export function hasImageShape(src: string): boolean {
  return /@\{[^}]*\bimg\s*:/i.test(src);
}

// renderMermaidSvg resolves to sanitized SVG markup, or rejects with the
// parse error Mermaid reported (the caller then shows the source instead).
export function renderMermaidSvg(
  source: string,
  theme: MermaidTheme,
): Promise<string> {
  const src = String(source ?? "").trim();
  if (!src) return Promise.reject(new Error("Empty diagram"));
  // Mermaid fetches an image shape's img: URL while it lays the node out,
  // before any sanitizer sees the SVG, so such a diagram is never drawn.
  if (hasImageShape(src)) {
    return Promise.reject(new Error("Diagrams with images are not drawn"));
  }
  const hit = cachedMermaidSvg(theme, src);
  if (hit) return Promise.resolve(withFreshIds(hit));

  const job = queue.then(async () => {
    const again = cachedMermaidSvg(theme, src);
    if (again) return withFreshIds(again);
    const mermaid = await loadMermaid();
    const id = `g-mermaid-${++nextId}`;
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      mermaid.initialize(mermaidConfig(theme));
      const { svg } = await Promise.race([
        mermaid.render(id, src),
        new Promise<never>((_, reject) => {
          timer = setTimeout(
            () => reject(new Error("Diagram took too long to render")),
            RENDER_TIMEOUT_MS,
          );
        }),
      ]);
      const clean = sanitizeMermaidSvg(svg);
      if (cache.size >= MAX_CACHE) {
        cache.delete(cache.keys().next().value as string);
      }
      cache.set(key(theme, src), clean);
      return clean;
    } finally {
      clearTimeout(timer);
      // Mermaid leaves its scratch element behind when a render fails.
      for (const stray of [id, `d${id}`]) {
        const el = document.getElementById(stray);
        if (el?.parentElement === document.body) el.remove();
      }
    }
  });
  // One bad diagram never stalls the ones after it.
  queue = job.catch(() => undefined);
  return job;
}

// mermaidErrorText trims Mermaid's multi-line parse errors to their first
// line, which names the problem ("Parse error on line 2").
export function mermaidErrorText(err: unknown): string {
  const msg =
    err instanceof Error ? err.message : String(err ?? "invalid syntax");
  return (msg.split("\n")[0] || "invalid syntax").replace(/:\s*$/, "");
}
