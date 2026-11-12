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

import { DOCUMENT } from "@angular/common";
import {
  Directive,
  ElementRef,
  HostListener,
  effect,
  inject,
  input,
} from "@angular/core";
import { DomSanitizer, type SafeHtml } from "@angular/platform-browser";
import { Router } from "@angular/router";
import type { DiffPreviewHunk, DiffPreviewLine } from "../api/models";
import { renderMarkdown } from "../sessions/session-view";

const KEYWORDS = new Set([
  "async",
  "await",
  "break",
  "case",
  "catch",
  "chan",
  "class",
  "const",
  "continue",
  "def",
  "default",
  "defer",
  "delete",
  "do",
  "elif",
  "else",
  "enum",
  "export",
  "extends",
  "fallthrough",
  "false",
  "finally",
  "fn",
  "for",
  "from",
  "func",
  "function",
  "go",
  "if",
  "implements",
  "import",
  "in",
  "interface",
  "let",
  "map",
  "match",
  "new",
  "nil",
  "none",
  "null",
  "of",
  "package",
  "private",
  "protected",
  "pub",
  "public",
  "range",
  "readonly",
  "return",
  "select",
  "self",
  "static",
  "struct",
  "super",
  "switch",
  "then",
  "this",
  "throw",
  "true",
  "try",
  "type",
  "typeof",
  "undefined",
  "use",
  "var",
  "void",
  "where",
  "while",
  "with",
  "yield",
]);

const BUILTIN_TYPES = new Set([
  "any",
  "bool",
  "boolean",
  "byte",
  "context",
  "error",
  "float32",
  "float64",
  "int",
  "int32",
  "int64",
  "never",
  "number",
  "object",
  "Promise",
  "Record",
  "rune",
  "Set",
  "Map",
  "string",
  "uint",
  "uint32",
  "uint64",
  "unknown",
]);

export function escapeCodeHtml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function span(cls: string, text: string): string {
  return `<span class="${cls}">${escapeCodeHtml(text)}</span>`;
}

function highlightDiffText(src: string): string {
  return src
    .split("\n")
    .map((line) => {
      if (line.startsWith("@@")) return span("tok-cmt", line);
      if (line.startsWith("+") && !line.startsWith("+++")) {
        return span("tok-add", line);
      }
      if (line.startsWith("-") && !line.startsWith("---")) {
        return span("tok-del", line);
      }
      return escapeCodeHtml(line);
    })
    .join("\n");
}

// Single-pass lexical highlighter for code blocks (zero catastrophic regexes).
export function highlightCode(src: string, lang = ""): string {
  const cleanLang = lang.trim().toLowerCase().split(/\s+/)[0] || "";
  if (cleanLang === "diff" || cleanLang === "patch") {
    return highlightDiffText(src);
  }
  const hashComments =
    cleanLang === "sh" ||
    cleanLang === "bash" ||
    cleanLang === "zsh" ||
    cleanLang === "py" ||
    cleanLang === "python" ||
    cleanLang === "yaml" ||
    cleanLang === "yml" ||
    cleanLang === "toml" ||
    cleanLang === "dockerfile";

  const out: string[] = [];
  const n = src.length;
  let i = 0;

  while (i < n) {
    const ch = src[i];
    const next = i + 1 < n ? src[i + 1] : "";

    // Line comments: // or #
    if (
      (ch === "/" && next === "/") ||
      (hashComments && ch === "#") ||
      (cleanLang === "sql" && ch === "-" && next === "-")
    ) {
      let end = src.indexOf("\n", i);
      if (end < 0) end = n;
      out.push(span("tok-cmt", src.slice(i, end)));
      i = end;
      continue;
    }

    // Block comments: /* ... */
    if (ch === "/" && next === "*") {
      const close = src.indexOf("*/", i + 2);
      const end = close < 0 ? n : close + 2;
      out.push(span("tok-cmt", src.slice(i, end)));
      i = end;
      continue;
    }

    // Strings: "...", '...', `...`
    if (ch === '"' || ch === "'" || ch === "`") {
      const quote = ch;
      let j = i + 1;
      while (j < n) {
        if (src[j] === "\\") {
          j += 2;
          continue;
        }
        if (src[j] === quote) {
          j++;
          break;
        }
        if (quote !== "`" && src[j] === "\n") break;
        j++;
      }
      // JSON / object property key before ':'
      let k = j;
      while (k < n && (src[k] === " " || src[k] === "\t")) k++;
      const cls =
        quote === '"' && src[k] === ":" && cleanLang === "json"
          ? "tok-prop"
          : "tok-str";
      out.push(span(cls, src.slice(i, j)));
      i = j;
      continue;
    }

    // Numbers
    if (
      (ch >= "0" && ch <= "9") ||
      (ch === "." && next >= "0" && next <= "9")
    ) {
      let j = i + 1;
      while (j < n && /[0-9a-fA-FxXoObB_.-]/.test(src[j])) j++;
      out.push(span("tok-num", src.slice(i, j)));
      i = j;
      continue;
    }

    // Identifiers, keywords, types, and function calls
    if (/[A-Za-z_$@]/.test(ch)) {
      let j = i + 1;
      while (j < n && /[A-Za-z0-9_$-]/.test(src[j])) j++;
      const word = src.slice(i, j);
      let k = j;
      while (k < n && (src[k] === " " || src[k] === "\t")) k++;
      if (word.startsWith("@")) {
        out.push(span("tok-kw", word));
      } else if (KEYWORDS.has(word)) {
        out.push(span("tok-kw", word));
      } else if (
        BUILTIN_TYPES.has(word) ||
        (/^[A-Z][A-Za-z0-9_]*$/.test(word) && word.length > 1)
      ) {
        out.push(span("tok-typ", word));
      } else if (src[k] === "(") {
        out.push(span("tok-fn", word));
      } else if (
        src[k] === ":" &&
        (cleanLang === "yaml" || cleanLang === "yml")
      ) {
        out.push(span("tok-prop", word));
      } else {
        out.push(escapeCodeHtml(word));
      }
      i = j;
      continue;
    }

    out.push(escapeCodeHtml(ch));
    i++;
  }

  return out.join("");
}

export function renderCodeCard(text: string, lang?: string): string {
  const clean = (lang || "").trim().split(/\s+/)[0] || "code";
  const body = text.replace(/\n$/, "");
  const highlighted = highlightCode(body, clean);
  return (
    `<div class="code-card">` +
    `<div class="code-card-head">` +
    `<span class="code-lang">${escapeCodeHtml(clean)}</span>` +
    `<button type="button" class="code-copy-btn" data-copy-code="1">Copy</button>` +
    `</div>` +
    `<pre><code class="hljs">${highlighted}</code></pre>` +
    `</div>`
  );
}

// ---------- Diff Hunk Line Numbers & Intra-Line Word Diff ----------

export interface DiffWordSegment {
  text: string;
  changed: boolean;
}

export interface AnnotatedDiffLine {
  kind: DiffPreviewLine["kind"];
  text: string;
  oldNo: number | null;
  newNo: number | null;
  segments: DiffWordSegment[];
}

function splitWordTokens(s: string): string[] {
  return s.match(/[A-Za-z0-9_]+|\s+|[^A-Za-z0-9_\s]+/g) ?? (s ? [s] : []);
}

function mergeSegments(segs: DiffWordSegment[]): DiffWordSegment[] {
  const out: DiffWordSegment[] = [];
  for (const s of segs) {
    if (!s.text) continue;
    const prev = out[out.length - 1];
    if (prev && prev.changed === s.changed) {
      prev.text += s.text;
    } else {
      out.push({ ...s });
    }
  }
  return out;
}

export function diffWordPair(
  delText: string,
  addText: string,
): { del: DiffWordSegment[]; add: DiffWordSegment[] } {
  const a = splitWordTokens(delText);
  const b = splitWordTokens(addText);
  let pre = 0;
  const minLen = Math.min(a.length, b.length);
  while (pre < minLen && a[pre] === b[pre]) pre++;

  let suf = 0;
  while (
    suf < minLen - pre &&
    a[a.length - 1 - suf] === b[b.length - 1 - suf]
  ) {
    suf++;
  }

  // If lines share no non-whitespace anchor at all and are long, highlight middle cleanly.
  const aMid = a.slice(pre, a.length - suf);
  const bMid = b.slice(pre, b.length - suf);

  // Run token LCS on bounded middle slice for crisp multi-word edits.
  if (aMid.length > 0 && bMid.length > 0 && aMid.length * bMid.length <= 1600) {
    const m = aMid.length;
    const n = bMid.length;
    const dp: number[][] = Array.from({ length: m + 1 }, () =>
      new Array<number>(n + 1).fill(0),
    );
    for (let i = m - 1; i >= 0; i--) {
      for (let j = n - 1; j >= 0; j--) {
        dp[i][j] =
          aMid[i] === bMid[j]
            ? 1 + dp[i + 1][j + 1]
            : Math.max(dp[i + 1][j], dp[i][j + 1]);
      }
    }
    const delMid: DiffWordSegment[] = [];
    const addMid: DiffWordSegment[] = [];
    let i = 0;
    let j = 0;
    while (i < m && j < n) {
      if (aMid[i] === bMid[j]) {
        delMid.push({ text: aMid[i], changed: false });
        addMid.push({ text: bMid[j], changed: false });
        i++;
        j++;
      } else if (dp[i + 1][j] >= dp[i][j + 1]) {
        delMid.push({ text: aMid[i++], changed: true });
      } else {
        addMid.push({ text: bMid[j++], changed: true });
      }
    }
    while (i < m) delMid.push({ text: aMid[i++], changed: true });
    while (j < n) addMid.push({ text: bMid[j++], changed: true });

    const prefixSeg = a.slice(0, pre).join("");
    const suffixSeg = suf > 0 ? a.slice(a.length - suf).join("") : "";
    return {
      del: mergeSegments([
        { text: prefixSeg, changed: false },
        ...delMid,
        { text: suffixSeg, changed: false },
      ]),
      add: mergeSegments([
        { text: prefixSeg, changed: false },
        ...addMid,
        { text: suffixSeg, changed: false },
      ]),
    };
  }

  const prefixText = a.slice(0, pre).join("");
  const suffixText = suf > 0 ? a.slice(a.length - suf).join("") : "";
  return {
    del: mergeSegments([
      { text: prefixText, changed: false },
      { text: aMid.join(""), changed: true },
      { text: suffixText, changed: false },
    ]),
    add: mergeSegments([
      { text: prefixText, changed: false },
      { text: bMid.join(""), changed: true },
      { text: suffixText, changed: false },
    ]),
  };
}

export function annotateDiffHunk(hunk: DiffPreviewHunk): AnnotatedDiffLine[] {
  const m = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(hunk.header || "");
  let oldCursor = m ? Number(m[1]) : 1;
  let newCursor = m ? Number(m[2]) : 1;

  const out: AnnotatedDiffLine[] = hunk.lines.map((l) => {
    if (l.kind === "ctx") {
      return {
        kind: "ctx",
        text: l.text,
        oldNo: oldCursor++,
        newNo: newCursor++,
        segments: [{ text: l.text, changed: false }],
      };
    }
    if (l.kind === "del") {
      return {
        kind: "del",
        text: l.text,
        oldNo: oldCursor++,
        newNo: null,
        segments: [{ text: l.text, changed: false }],
      };
    }
    return {
      kind: "add",
      text: l.text,
      oldNo: null,
      newNo: newCursor++,
      segments: [{ text: l.text, changed: false }],
    };
  });

  // Pair contiguous del[] + add[] runs for intra-line word-diff marks.
  let idx = 0;
  while (idx < out.length) {
    if (out[idx].kind !== "del") {
      idx++;
      continue;
    }
    const delStart = idx;
    while (idx < out.length && out[idx].kind === "del") idx++;
    const addStart = idx;
    while (idx < out.length && out[idx].kind === "add") idx++;
    const pairs = Math.min(addStart - delStart, idx - addStart);
    for (let k = 0; k < pairs; k++) {
      const paired = diffWordPair(
        out[delStart + k].text,
        out[addStart + k].text,
      );
      out[delStart + k].segments = paired.del;
      out[addStart + k].segments = paired.add;
    }
  }

  return out;
}

// ---------- [gMarkdown] Directive (Safe HTML + Code Copy + Router Links) ----------

@Directive({
  selector: "[gMarkdown]",
})
export class MarkdownDirective {
  private readonly el = inject(ElementRef<HTMLElement>);
  private readonly sanitizer = inject(DomSanitizer);
  private readonly router = inject(Router);
  private readonly doc = inject(DOCUMENT);

  readonly gMarkdown = input<string>("");

  private static readonly cache = new Map<string, string>();

  constructor() {
    effect(() => {
      const src = this.gMarkdown() ?? "";
      let html = MarkdownDirective.cache.get(src);
      if (html === undefined) {
        html = renderMarkdown(src);
        if (MarkdownDirective.cache.size >= 500) {
          MarkdownDirective.cache.clear();
        }
        MarkdownDirective.cache.set(src, html);
      }
      // renderMarkdown escapes all raw HTML and validates every href via safeHref;
      // setting innerHTML directly preserves .code-copy-btn buttons and data-copy-code.
      void (this.sanitizer.bypassSecurityTrustHtml(html) as SafeHtml);
      this.el.nativeElement.innerHTML = html;
    });
  }

  @HostListener("click", ["$event"])
  onClick(ev: MouseEvent): void {
    const target = ev.target as HTMLElement | null;
    const copyBtn = target?.closest<HTMLButtonElement>("[data-copy-code]");
    if (copyBtn) {
      ev.preventDefault();
      ev.stopPropagation();
      const code =
        copyBtn.closest(".code-card")?.querySelector("pre code")?.textContent ??
        "";
      void this.copyText(code);
      copyBtn.textContent = "Copied";
      copyBtn.classList.add("is-copied");
      setTimeout(() => {
        if (copyBtn.isConnected) {
          copyBtn.textContent = "Copy";
          copyBtn.classList.remove("is-copied");
        }
      }, 1600);
      return;
    }

    const anchor = target?.closest<HTMLAnchorElement>("a[href]");
    if (
      anchor &&
      ev.button === 0 &&
      !ev.metaKey &&
      !ev.ctrlKey &&
      !ev.shiftKey &&
      !ev.altKey
    ) {
      const href = anchor.getAttribute("href") || "";
      if (href.startsWith("/next/")) {
        ev.preventDefault();
        void this.router.navigateByUrl(href.slice("/next".length) || "/inbox");
      }
    }
  }

  private async copyText(text: string): Promise<void> {
    try {
      await this.doc.defaultView?.navigator?.clipboard?.writeText(text);
    } catch {
      // Clipboard API unavailable in restricted/non-secure test contexts.
    }
  }
}
