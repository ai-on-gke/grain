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
  OnInit,
  ViewEncapsulation,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
  untracked,
} from "@angular/core";
import { Router, RouterLink } from "@angular/router";
import { DomSanitizer, type SafeHtml } from "@angular/platform-browser";
import { GrainApi } from "../api/grain-api.service";
import type {
  Comment,
  MemoryDocument,
  MemoryDocumentsResponse,
  TreeNode,
  WorkTree,
} from "../api/models";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { MarkdownDirective } from "../shared/code-highlight";
import {
  extractSessionArtifacts,
  wrapSandboxedHtml,
} from "./session-artifacts";
import { errorText } from "./session-view";
import { SessionsStore } from "./sessions.store";

export type CanvasTab = "docs" | "html" | "plan" | "memory";

@Component({
  selector: "g-session-canvas",
  imports: [RouterLink, MarkdownDirective],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./session-canvas.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <aside
      class="session-canvas"
      aria-label="Session artifacts and workspace canvas"
      data-testid="session-canvas"
    >
      <header class="canvas-head">
        <div class="canvas-tabs" role="tablist" aria-label="Artifact types">
          <button
            type="button"
            role="tab"
            class="canvas-tab"
            data-testid="canvas-tab-docs"
            [class.is-active]="activeTab() === 'docs'"
            [attr.aria-selected]="activeTab() === 'docs'"
            (click)="activeTab.set('docs')"
          >
            Docs ({{ artifacts().docs.length }})
          </button>
          <button
            type="button"
            role="tab"
            class="canvas-tab"
            data-testid="canvas-tab-html"
            [class.is-active]="activeTab() === 'html'"
            [attr.aria-selected]="activeTab() === 'html'"
            (click)="activeTab.set('html')"
          >
            HTML Mock ({{ artifacts().htmlMocks.length }})
          </button>
          <button
            type="button"
            role="tab"
            class="canvas-tab"
            data-testid="canvas-tab-plan"
            [class.is-active]="activeTab() === 'plan'"
            [attr.aria-selected]="activeTab() === 'plan'"
            (click)="activeTab.set('plan')"
          >
            Workstream ({{ planBadgeCount() }})
          </button>
          @if (canReadMemory()) {
            <button
              type="button"
              role="tab"
              class="canvas-tab"
              data-testid="canvas-tab-memory"
              [class.is-active]="activeTab() === 'memory'"
              [attr.aria-selected]="activeTab() === 'memory'"
              (click)="openMemoryTab()"
            >
              Memory ({{ memoryDocs().length }})
            </button>
          }
        </div>
        <button
          type="button"
          class="icon-btn"
          aria-label="Close workspace canvas"
          data-testid="canvas-close"
          (click)="closed.emit()"
        >
          ✕
        </button>
      </header>

      <div class="canvas-body">
        @switch (activeTab()) {
          @case ("docs") {
            @if (artifacts().docs.length === 0) {
              <p class="canvas-empty">
                Markdown proposals, specs, or <code>grain-doc</code> fences
                shared in this conversation appear here for reading and pinning
                to Memory.
              </p>
            } @else {
              @if (artifacts().docs.length > 1) {
                <div class="canvas-selector">
                  @for (d of artifacts().docs; track d.id) {
                    <button
                      type="button"
                      class="canvas-chip"
                      [class.is-active]="activeDocId() === d.id"
                      (click)="selectedDocId.set(d.id)"
                    >
                      {{ d.title }}
                    </button>
                  }
                </div>
              }
              @if (selectedDoc(); as doc) {
                <div class="canvas-card">
                  <div class="canvas-toolbar">
                    <strong>{{ doc.title }}</strong>
                    @if (canWriteMemory()) {
                      <button
                        type="button"
                        class="btn-outlined"
                        data-testid="canvas-pin-memory-toggle"
                        (click)="togglePinForm(doc)"
                      >
                        {{ pinOpen() ? "Cancel" : "Pin to Memory" }}
                      </button>
                    }
                  </div>
                  @if (pinOpen()) {
                    <form
                      class="canvas-pin-form"
                      (submit)="pinDoc($event, doc)"
                    >
                      <input
                        type="text"
                        data-testid="pin-memory-name"
                        placeholder="Document slug (e.g. rollout-spec)"
                        [value]="pinName()"
                        (input)="pinName.set($any($event.target).value)"
                      />
                      <input
                        type="text"
                        data-testid="pin-memory-summary"
                        placeholder="One-line summary carried in future prompts"
                        [value]="pinSummary()"
                        (input)="pinSummary.set($any($event.target).value)"
                      />
                      <div class="canvas-toolbar">
                        <span class="muted" style="font-size: 11px">{{
                          repo() || "Deployment memory"
                        }}</span>
                        <button
                          type="submit"
                          class="btn-primary"
                          data-testid="pin-memory-save"
                          [disabled]="
                            pinBusy() ||
                            !pinName().trim() ||
                            !pinSummary().trim()
                          "
                        >
                          {{ pinBusy() ? "Saving..." : "Save to Memory" }}
                        </button>
                      </div>
                    </form>
                  }
                  <div class="msg-body" [gMarkdown]="doc.markdown"></div>
                </div>
              }
            }
          }

          @case ("html") {
            @if (artifacts().htmlMocks.length === 0) {
              <p class="canvas-empty">
                Fenced <code>html</code> or <code>grain-html</code> blocks in
                this conversation render here inside a network-isolated
                sandboxed preview.
              </p>
            } @else {
              @if (artifacts().htmlMocks.length > 1) {
                <div class="canvas-selector">
                  @for (h of artifacts().htmlMocks; track h.id) {
                    <button
                      type="button"
                      class="canvas-chip"
                      [class.is-active]="activeHtmlId() === h.id"
                      (click)="selectedHtmlId.set(h.id)"
                    >
                      {{ h.title }}
                    </button>
                  }
                </div>
              }
              @if (selectedHtml(); as mock) {
                <div class="canvas-toolbar">
                  <div class="canvas-btn-group">
                    <button
                      type="button"
                      class="canvas-chip"
                      data-testid="html-mode-preview"
                      [class.is-active]="htmlView() === 'preview'"
                      (click)="htmlView.set('preview')"
                    >
                      Preview
                    </button>
                    <button
                      type="button"
                      class="canvas-chip"
                      data-testid="html-mode-source"
                      [class.is-active]="htmlView() === 'source'"
                      (click)="htmlView.set('source')"
                    >
                      Source
                    </button>
                  </div>
                  <div class="canvas-btn-group">
                    <button
                      type="button"
                      class="canvas-chip"
                      data-testid="html-theme-toggle"
                      (click)="
                        htmlTheme.set(
                          htmlTheme() === 'light' ? 'dark' : 'light'
                        )
                      "
                    >
                      Theme: {{ htmlTheme() }}
                    </button>
                  </div>
                </div>
                @if (htmlView() === "preview") {
                  <iframe
                    class="canvas-iframe"
                    data-testid="canvas-html-iframe"
                    sandbox="allow-scripts"
                    [attr.title]="mock.title"
                    [srcdoc]="safeSrcdoc()"
                  ></iframe>
                } @else {
                  <pre class="canvas-source" data-testid="canvas-html-source">{{
                    mock.html
                  }}</pre>
                }
              }
            }
          }

          @case ("plan") {
            @if (
              artifacts().plans.length === 0 && proposedTreeNodes().length === 0
            ) {
              <p class="canvas-empty">
                Ask the session to break a feature into a
                <code>grain-plan</code>
                block or propose child tasks to review the DAG and launch a
                Workstream directly from here.
              </p>
            } @else {
              @for (p of artifacts().plans; track p.id) {
                <div class="canvas-card" data-testid="canvas-plan-card">
                  <div class="canvas-toolbar">
                    <strong>{{ p.plan.title || "Proposed Workstream" }}</strong>
                    @if (p.plan.repo) {
                      <span class="status-pill neutral">{{ p.plan.repo }}</span>
                    }
                  </div>
                  @if (p.plan.summary) {
                    <p class="muted" style="margin: 0; font-size: 12.5px">
                      {{ p.plan.summary }}
                    </p>
                  }
                  @for (t of p.plan.tasks; track t.title) {
                    <div class="plan-node" data-testid="plan-node">
                      <span>{{ t.title }}</span>
                      @if (t.dependsOn?.length) {
                        <span class="muted mono" style="font-size: 11px"
                          >after {{ t.dependsOn?.join(", ") }}</span
                        >
                      }
                    </div>
                  }
                  @if (canLaunchTasks()) {
                    <div class="canvas-toolbar">
                      <span class="muted" style="font-size: 11.5px"
                        >{{ p.plan.tasks.length }} task{{
                          p.plan.tasks.length === 1 ? "" : "s"
                        }}</span
                      >
                      <button
                        type="button"
                        class="btn-primary"
                        data-testid="canvas-launch-plan"
                        [disabled]="sessions.isBusy('launch-plan')"
                        (click)="launchPlan(p.plan)"
                      >
                        {{
                          sessions.isBusy("launch-plan")
                            ? "Launching..."
                            : "Approve & Launch Workstream"
                        }}
                      </button>
                    </div>
                  }
                </div>
              }

              @if (proposedTreeNodes().length > 0) {
                <div class="canvas-card" data-testid="canvas-tree-proposals">
                  <div class="canvas-toolbar">
                    <strong>Pending task proposals</strong>
                    @if (canApproveProposals()) {
                      <button
                        type="button"
                        class="btn-primary"
                        data-testid="canvas-approve-proposals"
                        [disabled]="treeBusy()"
                        (click)="approveTreeProposals()"
                      >
                        {{
                          treeBusy()
                            ? "Approving..."
                            : "Approve " + proposedTreeNodes().length
                        }}
                      </button>
                    }
                  </div>
                  @for (n of proposedTreeNodes(); track n.id) {
                    <div class="plan-node">
                      <a [routerLink]="['/task', n.id]"
                        >#{{ n.id }} · {{ n.title }}</a
                      >
                      <span class="status-pill neutral">{{ n.status }}</span>
                    </div>
                  }
                </div>
              }
            }
          }

          @case ("memory") {
            @if (memoryError(); as err) {
              <p class="banner banner-error" role="alert">{{ err }}</p>
            }
            @if (memoryDocs().length === 0) {
              <p class="canvas-empty">No Memory documents saved yet.</p>
            } @else {
              <div class="canvas-selector">
                @for (m of memoryDocs(); track m.name + (m.repo || "")) {
                  <button
                    type="button"
                    class="canvas-chip"
                    [class.is-active]="activeMemName() === m.name"
                    (click)="selectMemoryDoc(m)"
                  >
                    {{ m.repo ? m.repo + " / " : "" }}{{ m.name }}
                  </button>
                }
              </div>
              @if (activeMemDoc(); as mdoc) {
                <div class="canvas-card" data-testid="canvas-memory-editor">
                  <div class="canvas-toolbar">
                    <strong>{{ mdoc.name }}</strong>
                    <span class="muted" style="font-size: 11px">{{
                      mdoc.repo || "deployment"
                    }}</span>
                  </div>
                  @if (canWriteMemory()) {
                    <form
                      class="canvas-pin-form"
                      (submit)="saveMemoryDoc($event)"
                    >
                      <input
                        type="text"
                        aria-label="Memory summary"
                        [value]="memSummary()"
                        (input)="memSummary.set($any($event.target).value)"
                      />
                      <textarea
                        rows="8"
                        aria-label="Memory document text"
                        [value]="memText()"
                        (input)="memText.set($any($event.target).value)"
                      ></textarea>
                      <div class="canvas-toolbar">
                        <span class="muted" style="font-size: 11px"
                          >{{ memText().length }} chars</span
                        >
                        <button
                          type="submit"
                          class="btn-primary"
                          [disabled]="pinBusy()"
                        >
                          {{ pinBusy() ? "Saving..." : "Save Memory doc" }}
                        </button>
                      </div>
                    </form>
                  } @else {
                    <div class="msg-body" [gMarkdown]="memText()"></div>
                  }
                </div>
              }
            }
          }
        }
      </div>
    </aside>
  `,
})
export class SessionCanvasComponent implements OnInit {
  protected readonly sessions = inject(SessionsStore);
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);
  private readonly router = inject(Router);
  private readonly sanitizer = inject(DomSanitizer);

  readonly sessionTaskId = input<string | undefined>(undefined);
  readonly repo = input<string | undefined>(undefined);
  readonly comments = input<Comment[]>([]);
  readonly requestedTab = input<CanvasTab | null>(null);
  readonly requestedArtifactId = input<string | null>(null);

  readonly closed = output<void>();

  protected readonly activeTab = signal<CanvasTab>("docs");
  protected readonly selectedDocId = signal<string | null>(null);
  protected readonly selectedHtmlId = signal<string | null>(null);
  protected readonly htmlView = signal<"preview" | "source">("preview");
  protected readonly htmlTheme = signal<"light" | "dark">("light");

  protected readonly pinOpen = signal(false);
  protected readonly pinName = signal("");
  protected readonly pinSummary = signal("");
  protected readonly pinBusy = signal(false);

  protected readonly treeNodes = signal<TreeNode[]>([]);
  protected readonly treeBusy = signal(false);

  protected readonly memoryDocs = signal<MemoryDocument[]>([]);
  protected readonly memoryError = signal<string | null>(null);
  protected readonly activeMemDoc = signal<MemoryDocument | null>(null);
  protected readonly activeMemName = signal<string>("");
  protected readonly memSummary = signal("");
  protected readonly memText = signal("");

  protected readonly artifacts = computed(() =>
    extractSessionArtifacts(this.comments()),
  );

  protected readonly proposedTreeNodes = computed(() => {
    const out: TreeNode[] = [];
    const walk = (nodes: TreeNode[]) => {
      for (const n of nodes) {
        if (!n.root && n.status === "proposed") out.push(n);
        if (n.children?.length) walk(n.children);
      }
    };
    walk(this.treeNodes());
    return out;
  });

  protected readonly planBadgeCount = computed(
    () => this.artifacts().plans.length + this.proposedTreeNodes().length,
  );

  protected readonly selectedDoc = computed(() => {
    const list = this.artifacts().docs;
    const picked = this.selectedDocId();
    return (
      (picked ? list.find((d) => d.id === picked) : undefined) ??
      list[list.length - 1] ??
      null
    );
  });

  protected readonly selectedHtml = computed(() => {
    const list = this.artifacts().htmlMocks;
    const picked = this.selectedHtmlId();
    return (
      (picked ? list.find((h) => h.id === picked) : undefined) ??
      list[list.length - 1] ??
      null
    );
  });

  protected readonly activeDocId = computed(() => this.selectedDoc()?.id ?? "");
  protected readonly activeHtmlId = computed(
    () => this.selectedHtml()?.id ?? "",
  );

  protected readonly wrappedSrcdoc = computed(() => {
    const mock = this.selectedHtml();
    return mock ? wrapSandboxedHtml(mock.html, this.htmlTheme()) : "";
  });

  // Safe: rendered only inside <iframe sandbox="allow-scripts"> (opaque null origin,
  // no allow-same-origin, strict CSP connect-src 'none').
  protected readonly safeSrcdoc = computed<SafeHtml>(() =>
    this.sanitizer.bypassSecurityTrustHtml(this.wrappedSrcdoc()),
  );

  protected readonly canReadMemory = computed(() =>
    this.access.can("GET /api/memory/documents", this.repo() || undefined),
  );
  protected readonly canWriteMemory = computed(() =>
    this.access.can("PUT /api/memory/documents", this.repo() || undefined),
  );
  protected readonly canLaunchTasks = computed(() =>
    this.access.can("POST /api/tasks", this.repo() || undefined),
  );
  protected readonly canApproveProposals = computed(() =>
    this.access.can("POST /api/tasks/{id}/approve", this.repo() || undefined),
  );

  constructor() {
    effect(() => {
      const tab = this.requestedTab();
      const aid = this.requestedArtifactId();
      untracked(() => {
        if (tab) this.activeTab.set(tab);
        if (aid) {
          if (tab === "html") this.selectedHtmlId.set(aid);
          if (tab === "docs") this.selectedDocId.set(aid);
        }
      });
    });

    effect(() => {
      const tid = this.sessionTaskId();
      untracked(() => {
        if (tid) {
          void this.loadTree(tid);
        } else {
          this.treeNodes.set([]);
        }
      });
    });
  }

  ngOnInit(): void {
    if (this.canReadMemory()) {
      void this.loadMemoryDocs();
    }
  }

  private async loadTree(id: string): Promise<void> {
    if (typeof this.api.get !== "function") return;
    try {
      const res = await this.api.get<WorkTree>("GET /api/tasks/{id}/tree", {
        id,
      });
      if (this.sessionTaskId() === id) {
        this.treeNodes.set(res?.nodes ?? []);
      }
    } catch {
      // Optional tree read.
    }
  }

  protected openMemoryTab(): void {
    this.activeTab.set("memory");
    if (this.canReadMemory()) {
      void this.loadMemoryDocs();
    }
  }

  private async loadMemoryDocs(): Promise<void> {
    if (typeof this.api.get !== "function") return;
    try {
      const res = await this.api.get<MemoryDocumentsResponse>(
        "GET /api/memory/documents",
      );
      this.memoryDocs.set(res?.documents ?? []);
      this.memoryError.set(null);
    } catch (err) {
      this.memoryError.set(errorText(err));
    }
  }

  protected async selectMemoryDoc(doc: MemoryDocument): Promise<void> {
    this.activeMemName.set(doc.name);
    this.activeMemDoc.set(doc);
    this.memSummary.set(doc.summary);
    this.memText.set(doc.text ?? "");
    if (typeof this.api.get !== "function") return;
    try {
      const full = await this.api.get<MemoryDocument>(
        "GET /api/memory/documents/{name}",
        { name: doc.name },
        doc.repo ? `repo=${encodeURIComponent(doc.repo)}` : undefined,
      );
      if (full && this.activeMemName() === doc.name) {
        this.activeMemDoc.set(full);
        this.memSummary.set(full.summary || doc.summary);
        this.memText.set(full.text ?? "");
      }
    } catch {
      // Keep cached index metadata on read failure.
    }
  }

  protected togglePinForm(doc: { title: string; summary: string }): void {
    const next = !this.pinOpen();
    this.pinOpen.set(next);
    if (next) {
      const slug = doc.title
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-|-$/g, "")
        .slice(0, 48);
      this.pinName.set(slug || "session-note");
      this.pinSummary.set(doc.summary || doc.title);
    }
  }

  protected async pinDoc(ev: Event, doc: { markdown: string }): Promise<void> {
    ev.preventDefault();
    if (this.pinBusy()) return;
    this.pinBusy.set(true);
    try {
      await this.api.put("PUT /api/memory/documents", undefined, {
        ...(this.repo() ? { repo: this.repo() } : {}),
        name: this.pinName().trim(),
        summary: this.pinSummary().trim(),
        text: doc.markdown,
      });
      this.pinOpen.set(false);
      this.snackbar.show(`Pinned "${this.pinName().trim()}" to Memory`);
      void this.loadMemoryDocs();
    } catch (err) {
      this.snackbar.show(`Could not pin Memory document: ${errorText(err)}`);
    } finally {
      this.pinBusy.set(false);
    }
  }

  protected async saveMemoryDoc(ev: Event): Promise<void> {
    ev.preventDefault();
    const doc = this.activeMemDoc();
    if (!doc || this.pinBusy()) return;
    this.pinBusy.set(true);
    try {
      await this.api.put("PUT /api/memory/documents", undefined, {
        ...(doc.repo ? { repo: doc.repo } : {}),
        name: doc.name,
        summary: this.memSummary().trim() || doc.summary,
        text: this.memText(),
      });
      this.snackbar.show(`Saved Memory "${doc.name}"`);
      void this.loadMemoryDocs();
    } catch (err) {
      this.snackbar.show(`Save failed: ${errorText(err)}`);
    } finally {
      this.pinBusy.set(false);
    }
  }

  protected async launchPlan(
    plan: Parameters<SessionsStore["launchWorkstreamPlan"]>[0],
  ): Promise<void> {
    const origin = await this.sessions.launchWorkstreamPlan(plan);
    if (origin) {
      this.snackbar.show(`Launched workstream (${plan.tasks.length} tasks)`);
      await this.router.navigate(["/workstreams", origin]);
    }
  }

  protected async approveTreeProposals(): Promise<void> {
    const nodes = this.proposedTreeNodes();
    if (!nodes.length || this.treeBusy()) return;
    this.treeBusy.set(true);
    try {
      for (const n of nodes) {
        await this.api.post("POST /api/tasks/{id}/approve", { id: n.id });
      }
      this.snackbar.show(
        `Approved ${nodes.length} proposed task${nodes.length === 1 ? "" : "s"}`,
      );
      const tid = this.sessionTaskId();
      if (tid) void this.loadTree(tid);
    } catch (err) {
      this.snackbar.show(`Approval failed: ${errorText(err)}`);
    } finally {
      this.treeBusy.set(false);
    }
  }
}
