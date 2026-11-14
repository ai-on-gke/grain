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
  AfterViewInit,
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  NgZone,
  OnDestroy,
  ViewEncapsulation,
  computed,
  inject,
  viewChild,
} from "@angular/core";
import { Router } from "@angular/router";
import { HelpService, type HelpTab } from "../services/help.service";
import { KeyboardService } from "../services/keyboard.service";

interface ViewGuide {
  title: string;
  summary: string;
  steps: string[];
}

const VIEW_GUIDES: Record<string, ViewGuide> = {
  inbox: {
    title: "Inbox",
    summary:
      "Decisions stopped until a person weighs in: agent questions, task proposals, access or host-script approvals, held merges, and deployment alerts.",
    steps: [
      "Click a row (or step with j and k) to inspect its question, rationale, CI gates, and branch diff preview.",
      "Selection pins the active row and holds queue order so incoming updates never swap what you are about to answer.",
      "Drag the vertical divider (or use Left/Right arrows, 30 / 70, 42 / 58, or Hide list) to tune queue vs inspector space.",
      "Tick rows (or press x and *) to batch-approve or defer with repository and owner scope checks, plus 5-second Undo.",
    ],
  },
  workstreams: {
    title: "Workstreams",
    summary:
      "Multi-task goals and every child task filed for them, organized as navigable trees and dependency graphs.",
    steps: [
      "People scope highlights user-initiated goals plus any system streams currently waiting on a person.",
      "Switch between List and Cards to scan progress, active agents, and pending decisions across streams.",
      "Open a workstream to inspect its hierarchy tree, tidy dependency graph, and embedded task detail side by side.",
      "Approve all proposals in a stream or pause/resume queued work with scoped confirmation.",
    ],
  },
  tasks: {
    title: "Tasks",
    summary:
      "Every task in this deployment: Backlog in exact dispatch order first, Needs approval beside it, then Running, Asked, Merging, Merged, Deferred, and Closed.",
    steps: [
      "Approved tasks enter Backlog at their priority band (Urgent 9 down to Lowest 1) and dispatch top-to-bottom.",
      "Drag backlog handles (or pick up with Space and arrow keys) to reorder; crossing a band updates priority automatically.",
      "Click any row to open the split task inspector in place, or open the full task page for deep history.",
      "Next up in the top bar always shows the head of the runnable queue (or why queued items wait on dependencies).",
    ],
  },
  task: {
    title: "Task detail",
    summary:
      "One task in full: conversation timeline, agent attempts and live transcript, branch changes and CI gates, workstream tree, and metadata.",
    steps: [
      "Header controls adapt to task state: Approve/Defer proposals, Reply to questions, Stop running turns, or Submit changes.",
      "Runs lists each attempt with model, hostd/sandbox execution target, prompt token estimate, and live transcript.",
      "Workstream shows the parent chain, child tasks, and upstream/downstream dependencies.",
    ],
  },
  agents: {
    title: "Agents (Live)",
    summary:
      "Real-time operational wall over the shared task feed: one card per running agent with its latest status sentence, CPU meter, and 30-minute sparkline.",
    steps: [
      "Quiet runs (no activity update for 10+ minutes) sort first with an amber outline and CPU-informed diagnosis.",
      "Filter by Working, Setting up, or Quiet, group by Repository, Model, or Host work, or switch between Cards and List.",
      "Press f for full-screen Wall view on a team monitor, or use Contact running to broadcast a notice to active agents.",
    ],
  },
  sessions: {
    title: "Sessions",
    summary:
      "Standing interactive conversations with the Director and specialized console agents.",
    steps: [
      "Select Director or another session on the left; each message starts a standard agent run with that session's grants.",
      "Toggle capabilities inline above the thread; changes take effect from the next turn.",
      "When context grows crowded, Start a fresh chat carries the latest handover note forward cleanly.",
    ],
  },
  ci: {
    title: "CI",
    summary:
      "Grain CI and external checks in one split workspace, separating shared infrastructure faults (Errored) from isolated branch code failures (Failed).",
    steps: [
      "Shared infrastructure faults surface in the top banner with affected run and branch counts plus assigned repair tasks.",
      "Filter by conclusion, repository, branch, or provider (Grain CI vs GitHub Actions) and inspect step logs in the recessed log viewer.",
      "Trigger manual workflows, re-run failed jobs, flush caches, or quarantine flaky tests with a follow-up repair task.",
    ],
  },
  processes: {
    title: "Processes",
    summary:
      "Standing automation: recurring schedules, event triggers, parameterized templates, multi-pass suites, and repository .grain/ declarations.",
    steps: [
      "Schedules & Triggers lists recurring cadences and GitHub/git-ref triggers with Run now, Pause/Resume, and live composition previews.",
      "Templates browses deployment, repo-declared, and built-in templates and launches tasks with parameter validation.",
      "Suites & Runs manages multi-template suites and inspects pass-by-pass child tasks; Repo Declarations manages .grain/ sync and capability acceptances.",
    ],
  },
  telemetry: {
    title: "Telemetry",
    summary:
      "Delivery throughput, cycle-time quantiles, CI and merge-queue health, token concurrency budgets, and non-ranked team collaboration stats.",
    steps: [
      "Switch between 24h, 7d, and 30d windows and scope to a repository or contributor from the toolbar.",
      "Inspect throughput, p50/p90 lifecycle stage timings, run outcome shares, and top tool/CI wait causes.",
      "Configure per-connection token budgets and advisory/auto worker scaling when holding settings:write.",
      "Review team collaboration and shipped directions alphabetically (A–Z), never leaderboard-ranked.",
    ],
  },
  context: {
    title: "Context & Standing Memory",
    summary:
      "Deployment-wide and per-repository standing agent instructions, 200-character indexed memory packs, and the live /api/fs/memory/ namespace.",
    steps: [
      "Switch between This deployment and any repository scope to edit standing instructions injected into every prompt.",
      "Create and maintain Memory Packs with a crisp <= 200 character index summary fetched on demand by runs.",
      "Browse and grep memory:// and config:// files directly with syntax-highlighted or Markdown preview.",
    ],
  },
  fleet: {
    title: "Fleet & Host Operations",
    summary:
      "Host CPU/vCPU/memory/disk capacity, live and persistent sandboxes, grain-agentd host daemons, dispatch pauses, caches, and daemon logs.",
    steps: [
      "Monitor live task sandboxes, run a throwaway diagnostic probe, or start/restart/rebuild/stop persistent sandboxes.",
      "Inspect connected grain-agentd hosts and rotate, rename, or revoke host tokens.",
      "Pause or resume pipeline dispatch, clear quota pauses, flush package/Actions caches, or tail redacted daemon logs.",
    ],
  },
  settings: {
    title: "Settings",
    summary:
      "Deployment configuration across Models & Compute, Repositories & Code, Work & Agents, Access & Security, and Instance & Preferences.",
    steps: [
      "Filter all 20 sections from the left Material 3 drawer or jump directly via comma (,).",
      "Preview normalized server changes before saving via Plan / Preview changes.",
      "When managed in StateRepo, config-locked sections display a direct link to their committed file in Config repo while secrets stay editable.",
    ],
  },
  repos: {
    title: "Repository settings",
    summary:
      "Per-repository Git connections, CI mode and workflows, merge gates, branches, issue import, and release qualification.",
    steps: [
      "Switch tabs across Overview, CI & Workflows, Merge Queue, Agents & Grants, Branches, and Releases.",
      "Cut release candidates, approve qualification checks, promote candidates, and approve or reject pending trunk releases.",
    ],
  },
  default: {
    title: "Grain",
    summary:
      "Exception-first orchestration for autonomous coding agents: humans shape goals and resolve holds while routine verified work runs and lands.",
    steps: [
      "Press c (or click the blue + button at the top of the rail) from any screen to file a task or start a workstream.",
      "Use 1–9 to jump across your visible rail destinations, / to filter the current view, and 0 for Account & roles.",
    ],
  },
};

export const GLOSSARY_ITEMS: readonly { term: string; definition: string }[] = [
  {
    term: "Workstreams",
    definition:
      "Connected trees of tasks rooted at an originating goal, Director plan, design doc, or recurring suite, showing progress and dependencies together.",
  },
  {
    term: "Proposals",
    definition:
      "Tasks suggested by an agent, session, or unapproved draft (state: proposed). Proposals never dispatch until a person approves them.",
  },
  {
    term: "Merge Gates",
    definition:
      "Required checks a pull request must satisfy before landing — such as CI passing, code/security review tasks, and human submit approval.",
  },
  {
    term: "Capabilities",
    definition:
      "Explicit permissions granted to an agent run or session (for example repository writes, API scopes, or priority-dispatch).",
  },
  {
    term: "Sandboxes",
    definition:
      "Isolated execution environments (Granule microVMs, containers, Kubernetes pods, or local host sandboxes) where task attempts build and test code.",
  },
  {
    term: "Hostd",
    definition:
      "Token landing daemon (grain-agentd) connecting a developer's own machine and CLI credentials back to a Grain controller for sandboxed agent turns.",
  },
  {
    term: "Director",
    definition:
      "The primary standing session agent that helps break down goals, inspect deployment state, and propose structured tasks or workstreams.",
  },
  {
    term: "Priority & Next up",
    definition:
      "Tasks carry priority 1–9 (default 5, Normal). Grain dispatches higher numbers first, then manual backlog order, skipping tasks blocked on open dependencies.",
  },
];

const SHORTCUT_ROWS: readonly { keys: string; label: string }[] = [
  { keys: "c", label: "New task or workstream" },
  { keys: "/", label: "Focus view filter" },
  { keys: "1 – 9", label: "Open rail destinations in visible order" },
  { keys: ",", label: "Open Settings" },
  { keys: "0", label: "Open Account & access popover" },
  { keys: "t", label: "Switch light / dark theme" },
  { keys: "?", label: "Open Help, Glossary & Shortcuts" },
  { keys: "j / k", label: "Next / previous item in Inbox" },
  { keys: "Enter", label: "Open full task" },
  { keys: "a / e", label: "Primary action / focus reply in Inbox" },
  { keys: "Shift + A", label: "Approve selected" },
  { keys: "x / *", label: "Select item / select all visible in Inbox" },
  { keys: "u", label: "Undo last batch or Inbox action" },
  { keys: "f", label: "Toggle full-screen Wall view on Agents" },
  {
    keys: "Esc",
    label: "Close dialog/popover, cancel drag, or leave Wall view",
  },
];

export function guideKeyForUrl(url: string): string {
  const clean = (url || "").split("?")[0].replace(/^\/+/, "");
  const seg = clean.split("/")[0] || "inbox";
  if (seg in VIEW_GUIDES) return seg;
  return "default";
}

@Component({
  selector: "g-help-dialog",
  encapsulation: ViewEncapsulation.None,
  styleUrls: ["./shell-overlays.css", "../shared/surface-controls.css"],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="scrim" aria-hidden="true" (click)="help.close()"></div>
    <div
      #dialogEl
      class="shell-modal"
      role="dialog"
      aria-modal="true"
      aria-labelledby="help-dialog-title"
      data-testid="help-dialog"
    >
      <div class="shell-modal-head">
        <div class="shell-modal-title-group">
          <span class="status-pill neutral">Help</span>
          <h2 class="shell-modal-title" id="help-dialog-title">
            {{ heading() }}
          </h2>
        </div>
        <button
          #closeBtn
          type="button"
          class="icon-btn"
          aria-label="Close help"
          data-testid="help-close"
          (click)="help.close()"
        >
          <svg width="20" height="20" viewBox="0 0 24 24" aria-hidden="true">
            <path
              d="M19 6.41 17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"
              fill="currentColor"
            />
          </svg>
        </button>
      </div>

      <div class="shell-modal-body" tabindex="0">
        <div
          class="segmented help-tabs"
          role="tablist"
          aria-label="Help sections"
        >
          <button
            type="button"
            role="tab"
            id="help-tab-btn-about"
            aria-controls="help-tabpanel"
            [attr.aria-selected]="help.tab() === 'about'"
            [attr.tabindex]="help.tab() === 'about' ? 0 : -1"
            data-testid="help-tab-about"
            (click)="setTab('about')"
            (keydown)="onTabListKey($event)"
          >
            About this view
          </button>
          <button
            type="button"
            role="tab"
            id="help-tab-btn-glossary"
            aria-controls="help-tabpanel"
            [attr.aria-selected]="help.tab() === 'glossary'"
            [attr.tabindex]="help.tab() === 'glossary' ? 0 : -1"
            data-testid="help-tab-glossary"
            (click)="setTab('glossary')"
            (keydown)="onTabListKey($event)"
          >
            Glossary
          </button>
          <button
            type="button"
            role="tab"
            id="help-tab-btn-shortcuts"
            aria-controls="help-tabpanel"
            [attr.aria-selected]="help.tab() === 'shortcuts'"
            [attr.tabindex]="help.tab() === 'shortcuts' ? 0 : -1"
            data-testid="help-tab-shortcuts"
            (click)="setTab('shortcuts')"
            (keydown)="onTabListKey($event)"
          >
            Keyboard shortcuts
          </button>
        </div>

        <div
          id="help-tabpanel"
          role="tabpanel"
          class="help-tabpanel"
          [attr.aria-labelledby]="'help-tab-btn-' + help.tab()"
        >
          @switch (help.tab()) {
            @case ("about") {
              <p class="help-summary" data-testid="help-about-summary">
                {{ guide().summary }}
              </p>
              <ol class="help-steps">
                @for (step of guide().steps; track step) {
                  <li>{{ step }}</li>
                }
              </ol>
              <p class="muted" style="margin: 0; font-size: 12px">
                Hover or focus any button or badge for a concise tooltip
                explanation.
              </p>
            }
            @case ("glossary") {
              <dl class="help-glossary" data-testid="help-glossary-list">
                @for (item of glossary; track item.term) {
                  <dt>{{ item.term }}</dt>
                  <dd>{{ item.definition }}</dd>
                }
              </dl>
            }
            @case ("shortcuts") {
              <div class="shortcut-switch-bar">
                <div>
                  <div style="font-weight: 500; font-size: 13px">
                    Single-key shortcuts
                  </div>
                  <div class="muted" style="font-size: 12px">
                    Active when focus is not inside a text field. Turn off for
                    speech input or screen-reader navigation.
                  </div>
                </div>
                <button
                  type="button"
                  class="g-switch"
                  role="switch"
                  data-testid="help-shortcuts-switch"
                  [attr.aria-checked]="keyboard.enabled()"
                  aria-label="Enable single-key shortcuts"
                  (click)="keyboard.setEnabled(!keyboard.enabled())"
                ></button>
              </div>
              <div class="shortcut-list" data-testid="help-shortcuts-list">
                @for (row of shortcuts; track row.keys) {
                  <div class="shortcut-row">
                    <span>{{ row.label }}</span>
                    <kbd class="kbd-key">{{ row.keys }}</kbd>
                  </div>
                }
              </div>
            }
          }
        </div>
      </div>

      <div class="shell-modal-foot">
        <span style="flex: 1"></span>
        <button
          type="button"
          class="btn-primary"
          data-testid="help-done"
          (click)="help.close()"
        >
          Done
        </button>
      </div>
    </div>
  `,
})
export class HelpDialogComponent implements AfterViewInit, OnDestroy {
  protected readonly help = inject(HelpService);
  protected readonly keyboard = inject(KeyboardService);
  private readonly router = inject(Router);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);

  private readonly dialogEl = viewChild<ElementRef<HTMLElement>>("dialogEl");
  private readonly closeBtn =
    viewChild<ElementRef<HTMLButtonElement>>("closeBtn");

  protected readonly glossary = GLOSSARY_ITEMS;
  protected readonly shortcuts = SHORTCUT_ROWS;

  private readonly onCaptureKeydown = (ev: KeyboardEvent): void => {
    if (ev.key === "Escape") {
      this.zone.run(() => this.onEscape(ev));
    } else if (ev.key === "Tab") {
      this.zone.run(() => this.onTab(ev));
    }
  };

  protected readonly guide = computed<ViewGuide>(() => {
    const key = guideKeyForUrl(this.router.url);
    return VIEW_GUIDES[key] ?? VIEW_GUIDES["default"];
  });

  protected readonly heading = computed(() => {
    const t = this.help.tab();
    if (t === "glossary") return "Glossary";
    if (t === "shortcuts") return "Keyboard shortcuts";
    return `About ${this.guide().title}`;
  });

  ngAfterViewInit(): void {
    this.zone.runOutsideAngular(() => {
      this.doc.addEventListener("keydown", this.onCaptureKeydown, true);
    });
    this.closeBtn()?.nativeElement.focus();
  }

  ngOnDestroy(): void {
    this.doc.removeEventListener("keydown", this.onCaptureKeydown, true);
  }

  protected setTab(tab: HelpTab): void {
    this.help.tab.set(tab);
  }

  protected onTabListKey(ev: KeyboardEvent): void {
    const order: HelpTab[] = ["about", "glossary", "shortcuts"];
    const idx = order.indexOf(this.help.tab());
    let next = -1;
    if (ev.key === "ArrowRight") next = (idx + 1) % order.length;
    else if (ev.key === "ArrowLeft")
      next = (idx - 1 + order.length) % order.length;
    else if (ev.key === "Home") next = 0;
    else if (ev.key === "End") next = order.length - 1;
    if (next < 0) return;
    ev.preventDefault();
    const target = order[next];
    this.help.tab.set(target);
    queueMicrotask(() => {
      this.dialogEl()
        ?.nativeElement.querySelector<HTMLButtonElement>(
          `#help-tab-btn-${target}`,
        )
        ?.focus();
    });
  }

  protected onEscape(ev: Event): void {
    if (ev.defaultPrevented) return;
    ev.preventDefault();
    ev.stopPropagation();
    this.help.close();
  }

  protected onTab(ev: Event): void {
    const ke = ev as KeyboardEvent;
    const root = this.dialogEl()?.nativeElement;
    if (!root) return;
    const focusables = Array.from(
      root.querySelectorAll<HTMLElement>(
        'button:not([disabled]), a[href], input:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ),
    );
    if (!focusables.length) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    const active = this.doc.activeElement;
    if (ke.shiftKey && (active === first || !root.contains(active))) {
      ke.preventDefault();
      last.focus();
    } else if (!ke.shiftKey && (active === last || !root.contains(active))) {
      ke.preventDefault();
      first.focus();
    }
  }
}
