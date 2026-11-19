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

import {
  ChangeDetectionStrategy,
  Component,
  ViewEncapsulation,
  computed,
  input,
} from "@angular/core";
import type { CommittedContextRef } from "../api/models";

const SAFE_REV_RE = /^[A-Za-z0-9._/-]+$/;
const SAFE_HOST_RE = /^[A-Za-z0-9.-]+(?::\d{1,5})?$/;

export interface RenderedContextFile {
  key: string;
  path: string;
  kind: string;
  title: string;
  sourceUrl: string;
}

export interface RenderedContextItem {
  key: string;
  isRead: boolean;
  badgeLabel: string;
  displayRef: string;
  rev: string;
  shortRev: string;
  href: string;
  hasManifest: boolean;
  manifestTitle: string;
  manifestDescription: string;
  files: RenderedContextFile[];
}

export function parseRepoWebBase(
  repo: string | undefined,
  githubHost?: string,
): string {
  if (!repo || typeof repo !== "string") return "";
  const parts = repo.split("/").filter(Boolean);
  if (parts.length === 2) {
    const defaultHost = (githubHost || "github.com").trim();
    if (!defaultHost || !SAFE_HOST_RE.test(defaultHost)) return "";
    return `https://${defaultHost}/${encodeURIComponent(parts[0])}/${encodeURIComponent(parts[1])}`;
  }
  if (
    parts.length === 3 &&
    parts[0].includes(".") &&
    SAFE_HOST_RE.test(parts[0])
  ) {
    return `https://${parts[0]}/${encodeURIComponent(parts[1])}/${encodeURIComponent(parts[2])}`;
  }
  return "";
}

function encodeRepoPath(relPath: string | undefined): string {
  const segs = String(relPath || "")
    .split("/")
    .filter(Boolean);
  if (segs.includes("..")) return "";
  return segs.map((seg) => encodeURIComponent(seg)).join("/");
}

export function contextTreeHref(
  item: CommittedContextRef,
  githubHost?: string,
): string {
  if (
    !item.repo ||
    !item.rev ||
    !SAFE_REV_RE.test(item.rev) ||
    item.rev.split("/").includes("..")
  ) {
    return "";
  }
  const base = parseRepoWebBase(item.repo, githubHost);
  if (!base) return "";
  const encodedRev = encodeURIComponent(item.rev);
  const rawPath = String(item.path || "").trim();
  const encodedPath = encodeRepoPath(rawPath);
  if (rawPath && !encodedPath) return "";
  return encodedPath
    ? `${base}/tree/${encodedRev}/${encodedPath}`
    : `${base}/tree/${encodedRev}`;
}

export function safeSourceUrl(raw: string | undefined): string {
  if (!raw || typeof raw !== "string") return "";
  try {
    const parsed = new URL(raw);
    return parsed.protocol === "http:" || parsed.protocol === "https:"
      ? parsed.href
      : "";
  } catch {
    return "";
  }
}

@Component({
  selector: "g-task-committed-context",
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./task-committed-context.component.css",
  template: `
    @if (rendered().length > 0) {
      <section
        id="committed-context"
        class="g-pane tcc-pane"
        [class.tcc-highlight]="highlighted()"
        tabindex="-1"
        aria-labelledby="committed-context-heading"
        data-testid="committed-context"
      >
        <div class="g-pane-head">
          <h2
            id="committed-context-heading"
            [attr.aria-level]="headingLevel()"
            class="g-pane-title"
          >
            Committed context
          </h2>
          <span
            class="tab-count tabular"
            [attr.aria-label]="
              rendered().length === 1
                ? '1 reference'
                : rendered().length + ' references'
            "
            >{{ rendered().length }}</span
          >
        </div>
        <ul class="tcc-list">
          @for (item of rendered(); track item.key) {
            <li class="tcc-item" data-testid="committed-context-item">
              <div class="tcc-item-head">
                <span
                  class="tcc-badge"
                  [class.is-read]="item.isRead"
                  [class.is-target]="!item.isRead"
                  >{{ item.badgeLabel }}</span
                >
                @if (item.shortRev) {
                  @if (item.href) {
                    <a
                      class="tcc-sha"
                      [href]="item.href"
                      target="_blank"
                      rel="noopener noreferrer"
                      [attr.title]="'Pinned commit ' + item.rev"
                      >&#64;{{ item.shortRev }}</a
                    >
                  } @else {
                    <code
                      class="tcc-sha"
                      [attr.title]="'Pinned commit ' + item.rev"
                      >&#64;{{ item.shortRev }}</code
                    >
                  }
                }
              </div>
              <div class="tcc-path">
                <code>{{ item.displayRef }}</code>
              </div>
              @if (item.hasManifest) {
                <div class="tcc-manifest">
                  @if (item.manifestTitle) {
                    <div class="tcc-manifest-title">
                      {{ item.manifestTitle }}
                    </div>
                  }
                  @if (item.manifestDescription) {
                    <p class="tcc-manifest-desc">
                      {{ item.manifestDescription }}
                    </p>
                  }
                  @if (item.files.length > 0) {
                    <ul class="tcc-files">
                      @for (f of item.files; track f.key) {
                        <li class="tcc-file">
                          <code class="tcc-file-path">{{ f.path }}</code>
                          @if (f.kind) {
                            <span class="tcc-file-kind">{{ f.kind }}</span>
                          }
                          @if (f.title) {
                            <span class="tcc-file-title">{{ f.title }}</span>
                          }
                          @if (f.sourceUrl) {
                            <a
                              class="tcc-file-source"
                              [href]="f.sourceUrl"
                              target="_blank"
                              rel="noopener noreferrer"
                              >Source<span class="sr-only">
                                for {{ f.path }}</span
                              ></a
                            >
                          }
                        </li>
                      }
                    </ul>
                  }
                </div>
              }
            </li>
          }
        </ul>
      </section>
    }
  `,
})
export class TaskCommittedContextComponent {
  readonly items = input<readonly CommittedContextRef[]>([]);
  readonly githubHost = input<string | undefined>(undefined);
  readonly headingLevel = input<number | null>(null);
  readonly highlighted = input(false);

  protected readonly rendered = computed<RenderedContextItem[]>(() => {
    const list = this.items();
    if (!Array.isArray(list) || list.length === 0) return [];
    const host = this.githubHost();
    return list.map((item, idx) => {
      const isRead = item.placement === "read";
      const displayRef =
        isRead && item.repo ? `${item.repo}:${item.path}` : item.path;
      const rev = item.rev ?? "";
      const shortRev = rev ? rev.slice(0, 10) : "";
      const href = contextTreeHref(item, host);
      const manifest = item.manifest ?? null;
      const manifestTitle = manifest?.title?.trim() ?? "";
      const manifestDescription = manifest?.description?.trim() ?? "";
      const rawFiles = manifest?.files;
      const files: RenderedContextFile[] = (
        Array.isArray(rawFiles) ? rawFiles : []
      ).map((f, fIdx) => ({
        key: `${f.path}:${fIdx}`,
        path: f.path,
        kind: f.kind?.trim() ?? "",
        title: f.title?.trim() ?? "",
        sourceUrl: safeSourceUrl(f.source_url),
      }));
      return {
        key: `${item.placement || "target"}:${item.repo || ""}:${item.path}:${rev}:${idx}`,
        isRead,
        badgeLabel: isRead ? "Read-only repo" : "Target repo",
        displayRef,
        rev,
        shortRev,
        href,
        hasManifest:
          Boolean(manifestTitle) ||
          Boolean(manifestDescription) ||
          files.length > 0,
        manifestTitle,
        manifestDescription,
        files,
      };
    });
  });
}
