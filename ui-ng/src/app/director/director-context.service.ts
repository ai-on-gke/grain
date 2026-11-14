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

import { computed, Injectable, signal } from "@angular/core";

export type DirectorSelectionKind =
  "task" | "workstream" | "inbox" | "ci" | "repo" | "session";

export interface DirectorSelection {
  kind: DirectorSelectionKind;
  id: string;
  title?: string;
  repo?: string;
  state?: string;
  summary?: string;
}

// Tracks the global Director drawer open/close state and the live route + item
// selection context so the operator can open Director from anywhere without
// losing their place in the workspace.
@Injectable({ providedIn: "root" })
export class DirectorContextService {
  readonly isOpen = signal(false);
  readonly routeUrl = signal("");
  readonly selection = signal<DirectorSelection | null>(null);
  readonly includeContext = signal(true);
  private openerEl: HTMLElement | null = null;

  readonly activeContext = computed<DirectorSelection | null>(() => {
    const sel = this.selection();
    if (sel) return sel;
    return parseRouteContext(this.routeUrl());
  });

  readonly contextLine = computed<string>(() => {
    const ctx = this.activeContext();
    if (!ctx) return "";
    const parts: string[] = [];
    const label = contextKindLabel(ctx.kind, ctx.id);
    if (ctx.title) {
      parts.push(`${label}: ${ctx.title}`);
    } else {
      parts.push(label);
    }
    const meta = [ctx.state, ctx.repo, ctx.summary].filter(Boolean);
    if (meta.length > 0) {
      parts.push(`(${meta.join(" · ")})`);
    }
    return parts.join(" ");
  });

  open(opener?: HTMLElement | null): void {
    if (opener) this.openerEl = opener;
    this.isOpen.set(true);
  }

  close(restoreFocus = true): void {
    if (!this.isOpen()) return;
    this.isOpen.set(false);
    if (restoreFocus && this.openerEl?.isConnected) {
      queueMicrotask(() => this.openerEl?.focus());
    }
  }

  toggle(opener?: HTMLElement | null): void {
    if (this.isOpen()) {
      this.close(true);
    } else {
      this.open(opener);
    }
  }

  setRoute(url: string): void {
    this.routeUrl.set(url);
  }

  setSelection(sel: DirectorSelection | null): void {
    this.selection.set(sel);
  }

  clearSelection(kind?: DirectorSelectionKind): void {
    const cur = this.selection();
    if (!cur) return;
    if (!kind || cur.kind === kind) {
      this.selection.set(null);
    }
  }

  formatMessageWithContext(body: string): string {
    const trimmed = body.trim();
    const line = this.contextLine();
    if (!trimmed || !this.includeContext() || !line) {
      return trimmed;
    }
    return `> [Context: ${line}]\n\n${trimmed}`;
  }
}

export function contextKindLabel(
  kind: DirectorSelectionKind,
  id: string,
): string {
  switch (kind) {
    case "task":
      return `Task #${id}`;
    case "workstream":
      return `Workstream ${id}`;
    case "inbox":
      return `Inbox #${id}`;
    case "ci":
      return `CI Run #${id}`;
    case "repo":
      return `Repo ${id}`;
    case "session":
      return `Session ${id}`;
  }
}

function safeDecode(s: string): string {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
}

export function parseRouteContext(url: string): DirectorSelection | null {
  const qIdx = url.indexOf("?");
  const beforeQuery = qIdx >= 0 ? url.slice(0, qIdx) : url;
  const afterQuery = qIdx >= 0 ? url.slice(qIdx + 1) : "";
  const mWsSlash = /^\/workstreams\/(.+)$/.exec(beforeQuery);
  if (mWsSlash) {
    return { kind: "workstream", id: safeDecode(mWsSlash[1]) };
  }
  const mWsHash = /^\/workstreams#(.+)$/.exec(beforeQuery);
  if (mWsHash) {
    return { kind: "workstream", id: safeDecode(mWsHash[1]) };
  }
  if (beforeQuery === "/workstreams") {
    const hIdx = afterQuery.indexOf("#");
    if (hIdx >= 0 && hIdx + 1 < afterQuery.length) {
      return {
        kind: "workstream",
        id: safeDecode(afterQuery.slice(hIdx + 1)),
      };
    }
  }
  const clean = beforeQuery.split("#")[0];
  const mTask = /^\/task\/([^/]+)/.exec(clean);
  if (mTask) {
    return { kind: "task", id: safeDecode(mTask[1]) };
  }
  const mRepo = /^\/repos\/([^/]+\/[^/]+)/.exec(clean);
  if (mRepo) {
    return { kind: "repo", id: safeDecode(mRepo[1]) };
  }
  return null;
}
