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

import type { AgentModel, Comment } from "../api/models";
import {
  extractGrainPlans,
  type GrainPlanBlock,
} from "../director/director-draft";
import type { PillOption } from "../shared/pill-select.component";

export interface DocArtifact {
  id: string;
  commentId: number;
  title: string;
  summary: string;
  markdown: string;
}

export interface HtmlArtifact {
  id: string;
  commentId: number;
  title: string;
  html: string;
}

export interface PlanArtifact {
  id: string;
  commentId: number;
  plan: GrainPlanBlock;
}

export interface ExtractedSessionArtifacts {
  docs: DocArtifact[];
  htmlMocks: HtmlArtifact[];
  plans: PlanArtifact[];
}

const HTML_FENCE_RE = /```(?:grain-html|html)\s*\n([\s\S]*?)```/gi;
const DOC_FENCE_RE = /```(?:grain-doc|markdown|md)\s*\n([\s\S]*?)```/gi;

export function extractSessionArtifacts(
  comments: Comment[],
): ExtractedSessionArtifacts {
  const docs: DocArtifact[] = [];
  const htmlMocks: HtmlArtifact[] = [];
  const plans: PlanArtifact[] = [];

  for (const c of comments) {
    if (c.authorKind === "human" || !c.body) continue;
    const body = c.body;

    // 1. HTML prototypes & mocks
    HTML_FENCE_RE.lastIndex = 0;
    let htmlIdx = 0;
    let m: RegExpExecArray | null;
    while ((m = HTML_FENCE_RE.exec(body)) !== null) {
      const html = m[1].trim();
      if (!html) continue;
      htmlIdx += 1;
      const title =
        extractHtmlTitle(html) || `HTML Mock #${htmlMocks.length + 1}`;
      htmlMocks.push({
        id: `html-${c.id}-${htmlIdx}`,
        commentId: c.id,
        title,
        html,
      });
    }

    // 2. Structured grain-plan DAG blocks
    const extractedPlans = extractGrainPlans(body);
    extractedPlans.forEach((plan, i) => {
      plans.push({
        id: `plan-${c.id}-${i + 1}`,
        commentId: c.id,
        plan,
      });
    });

    // 3. Explicit doc fences or rich heading-structured assistant responses
    DOC_FENCE_RE.lastIndex = 0;
    let docIdx = 0;
    while ((m = DOC_FENCE_RE.exec(body)) !== null) {
      const md = m[1].trim();
      if (!md) continue;
      docIdx += 1;
      docs.push({
        id: `doc-${c.id}-${docIdx}`,
        commentId: c.id,
        title: firstHeading(md) || `Document #${docs.length + 1}`,
        summary: firstSentence(md),
        markdown: md,
      });
    }
    if (docIdx === 0 && isStructuredSpecReply(body)) {
      docs.push({
        id: `doc-${c.id}-body`,
        commentId: c.id,
        title: firstHeading(body) || `Note #${docs.length + 1}`,
        summary: firstSentence(body),
        markdown: body,
      });
    }
  }

  return { docs, htmlMocks, plans };
}

function extractHtmlTitle(html: string): string {
  const tm = /<title>([^<]+)<\/title>/i.exec(html);
  if (tm?.[1]?.trim()) return tm[1].trim();
  const hm = /<h1[^>]*>([^<]+)<\/h1>/i.exec(html);
  if (hm?.[1]?.trim()) return hm[1].trim();
  return "";
}

function firstHeading(md: string): string {
  const m = /^\s{0,3}#{1,3}\s+(.+)$/m.exec(md);
  return m?.[1]?.trim() ?? "";
}

function firstSentence(md: string): string {
  for (const line of md.split("\n")) {
    const clean = line.replace(/^\s{0,3}#+\s*/, "").trim();
    if (!clean || clean.startsWith("```")) continue;
    return clean.length > 110 ? `${clean.slice(0, 107)}...` : clean;
  }
  return "Session artifact note";
}

function isStructuredSpecReply(body: string): boolean {
  if (body.length < 180) return false;
  return /^\s{0,3}#{1,3}\s+\S/m.test(body);
}

// Wraps user/agent HTML inside a strict-CSP standalone document for opaque
// <iframe sandbox="allow-scripts"> rendering (no allow-same-origin, no network).
export function wrapSandboxedHtml(
  html: string,
  theme: "light" | "dark" = "light",
): string {
  const csp =
    "default-src 'none'; base-uri 'none'; form-action 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none';";
  const themeStyle =
    theme === "dark"
      ? "html{color-scheme:dark;background:#111318;color:#e2e8f0;font-family:system-ui,-apple-system,sans-serif;}"
      : "html{color-scheme:light;background:#ffffff;color:#0f172a;font-family:system-ui,-apple-system,sans-serif;}";
  // Strip any attacker-supplied base or http-equiv directives (e.g. refresh or
  // permissive CSP attempts) before injecting our strict CSP at the head start.
  const sanitized = html
    .trim()
    .replace(/<meta\b[^>]*\bhttp-equiv\b[^>]*>/gi, "")
    .replace(/<base\b[^>]*>/gi, "");
  if (/<html[\s>]/i.test(sanitized) || /<!doctype/i.test(sanitized)) {
    const inject = `<meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${csp}"><style>${themeStyle}</style>`;
    if (/<head[\s>]/i.test(sanitized)) {
      return sanitized.replace(/<head([^>]*)>/i, `<head$1>${inject}`);
    }
    return `<!doctype html><html><head>${inject}</head>${sanitized}</html>`;
  }
  return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${csp}"><style>${themeStyle}body{margin:16px;}</style></head><body>${sanitized}</body></html>`;
}

export function buildAgentModelOptions(
  agentModels: AgentModel[],
  configModels: string[],
  defaultModel: string,
  currentModel?: string,
): PillOption[] {
  const opts: PillOption[] = [
    {
      value: "",
      label: defaultModel
        ? `Deployment default (${defaultModel})`
        : "Deployment default",
    },
  ];
  const byId = new Map<string, AgentModel>();
  for (const m of agentModels) {
    if (m.id && !byId.has(m.id)) byId.set(m.id, m);
  }
  const seen = new Set<string>([""]);
  for (const id of configModels) {
    if (!id || seen.has(id)) continue;
    seen.add(id);
    const meta = byId.get(id);
    if (meta) {
      const base = meta.label || meta.family || meta.id;
      const label =
        meta.effort && !base.toLowerCase().includes(meta.effort.toLowerCase())
          ? `${base} · ${meta.effort}`
          : base;
      opts.push({ value: id, label });
    } else {
      opts.push({ value: id, label: id });
    }
  }
  if (currentModel && !seen.has(currentModel)) {
    opts.push({ value: currentModel, label: currentModel });
  }
  return opts;
}

export function topologicalPlanOrder<
  T extends {
    key?: string;
    title: string;
    dependsOn?: string[];
    after?: string[];
  },
>(tasks: T[]): T[] {
  const keyToIdx = new Map<string, number>();
  tasks.forEach((t, idx) => {
    if (t.key) keyToIdx.set(t.key, idx);
    keyToIdx.set(t.title, idx);
  });
  const visited = new Array<number>(tasks.length).fill(0);
  const out: T[] = [];
  const visit = (i: number) => {
    if (visited[i] === 2) return;
    if (visited[i] === 1) {
      throw new Error(
        `Dependency cycle in workstream plan involving "${tasks[i].key || tasks[i].title}"`,
      );
    }
    visited[i] = 1;
    const deps = [...(tasks[i].dependsOn ?? []), ...(tasks[i].after ?? [])];
    for (const d of deps) {
      const target = keyToIdx.get(d);
      if (target !== undefined) visit(target);
    }
    visited[i] = 2;
    out.push(tasks[i]);
  };
  for (let i = 0; i < tasks.length; i++) visit(i);
  return out;
}
