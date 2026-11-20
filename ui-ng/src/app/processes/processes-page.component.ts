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

import { QaFlowsPanelComponent } from "./qa-flows-panel.component";

import { DOCUMENT } from "@angular/common";
import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  HostListener,
  Injector,
  NgZone,
  type OnDestroy,
  type OnInit,
  ViewEncapsulation,
  afterNextRender,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
  viewChild,
} from "@angular/core";
import { Router, RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import type { CustomFieldDeclaration } from "../api/models";
import { CreateTaskService } from "../create/create-task.service";
import { NO_REPO } from "../create/new-task-form";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { TaskFeedStore } from "../services/task-feed.service";
import { ViewFilter } from "../services/view-filter.service";
import { errorText } from "../sessions/session-helpers";
import { formatStamp } from "../shared/dispatch-status";
import { ActorChipComponent } from "../shared/actor-chip.component";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import { DialogTrapDirective } from "../shared/dialog-trap.directive";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import { SplitController } from "../shared/split-controller";
import { StatusIconComponent } from "../shared/status-icon.component";
import { TipDirective } from "../shared/tip.directive";
import { GrantsPickerComponent } from "../shared/grants-picker.component";
import { ModelPickerComponent } from "../shared/model-picker.component";
import { type ModelBackendLike, modelGroups } from "../shared/model-catalog";
import { CompositionViewComponent } from "./composition-view.component";
import {
  ADMISSION_OPTIONS,
  HOSTING_OPTIONS,
  NETWORK_ACCESS_OPTIONS,
  customFieldValueList,
  initialCustomFields,
  matchesTemplateQuery,
  sameCustomFields,
  sameList,
  sameWrites,
  templateDisambiguatedLabels,
  type Composition,
  type CreateScheduleRequest,
  type CreateSuiteRequest,
  type CreateTemplateRequest,
  type DeclarationAcceptanceView,
  type DeclaredTrigger,
  type DerivedRuns,
  type PathTriggerView,
  type ProcessSort,
  type ProcessTab,
  type Recurrence,
  type RepoWorkflow,
  type Schedule,
  type Suite,
  type SuiteRun,
  type Template,
  type TemplateField,
  type UpdateScheduleRequest,
  type UpdateSuiteRequest,
  type UpdateTemplateRequest,
  type WriteTarget,
  PROCESS_SORTS,
  buildSuiteRunPasses,
  derivedRuns,
  describeSuiteMode,
  formatCadence,
  formatScheduleNextRun,
  formatScheduleSuppression,
  isEventTrigger,
  movedOrder,
  parseProcessSort,
  scheduleFiringTarget,
  scheduleKindOf,
  skipFolded,
  sortProcessRows,
  scheduleStatusBadge,
  suiteRunBadge,
  templateSourceBadge,
} from "./processes.models";
import { ProcessesStore } from "./processes.store";
import { MarkdownDirective } from "../shared/code-highlight";
import { taskPriority } from "../tasks/batch-edit-dialog.component";

const DEFAULT_QA_FLOW_REFS = [
  "standard-workflow@1",
  "fast-baseline@1",
  "gates-only@1",
] as const;

const TABS: readonly { id: ProcessTab; label: string; tip: string }[] = [
  {
    id: "schedules",
    label: "Schedules & Triggers",
    tip: "Recurring schedules and GitHub/git-ref event triggers.",
  },
  {
    id: "templates",
    label: "Templates",
    tip: "Parameterized task templates across this deployment and repositories.",
  },
  {
    id: "suites",
    label: "Suites & Runs",
    tip: "Multi-template suites and live/completed suite runs.",
  },
  {
    id: "declarations",
    label: "Repo Workflows & Declarations",
    tip: "Repository .grain/ declarations, workflows, and capability acceptances.",
  },
  {
    id: "quality",
    label: "Software Workflow & QA",
    tip: "Pre-PR deterministic and fresh-context quality flow packages and rules.",
  },
];

const WEEKDAYS = [
  "monday",
  "tuesday",
  "wednesday",
  "thursday",
  "friday",
  "saturday",
  "sunday",
] as const;

const CADENCE_KINDS: readonly { id: string; label: string }[] = [
  { id: "everyNMinutes", label: "Every N min" },
  { id: "everyNHours", label: "Every N hr" },
  { id: "daily", label: "Daily" },
  { id: "weekly", label: "Weekly" },
  { id: "monthly", label: "Monthly" },
  { id: "github_workflow_run", label: "GitHub workflow" },
  { id: "git_ref", label: "Git ref push" },
];

type SchedFilter = "all" | "active" | "paused" | "triggers" | "suppressed";
type TmplFilter = "all" | "deployment" | "declared" | "system";
type SuitesPaneView = "suites" | "runs";
type OrderedList = "schedules" | "templates" | "suites";

// The custom order is browser state, kept under the classic Processes
// page's own key (ui/src/listOrder.js, list "processes") with its row
// keys ("schedule:<id>", "template:<id>", "suite:<id>"), so a row moved
// on either page sits in the same place on the other.
const ORDER_KEY = "grain.list-order.processes";

function loadOrder(): string[] {
  try {
    const raw = globalThis.localStorage?.getItem(ORDER_KEY);
    const ids: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(ids)
      ? ids.filter((x): x is string => typeof x === "string")
      : [];
  } catch {
    return [];
  }
}

function saveOrder(ids: readonly string[]): void {
  try {
    if (ids.length === 0) globalThis.localStorage?.removeItem(ORDER_KEY);
    else globalThis.localStorage?.setItem(ORDER_KEY, JSON.stringify(ids));
  } catch {
    // Storage denied: the order lasts as long as the page does.
  }
}

// A row of the Schedules & Triggers list: a schedule, or a trigger a
// repository declares in .grain/triggers.
interface SchedTabRow {
  key: string;
  schedule?: Schedule;
  trigger?: DeclaredTrigger;
}

function triggerName(path: string): string {
  return path.replace(/^\.grain\/triggers\//, "");
}

function templateFileStem(path: string): string {
  const base = path.split("/").pop() ?? path;
  return base.replace(/\.ya?ml$/i, "");
}

function synthesizeTriggerYaml(
  repo: string,
  filePath: string,
  detail: PathTriggerView | undefined,
  templates: readonly Template[],
): string {
  const lines = [`# ${repo}:${filePath}`];
  if (!detail) {
    lines.push("# Synced path trigger declaration.");
    return lines.join("\n");
  }
  if (detail.on) lines.push(`on: ${detail.on}`);
  if (detail.branches?.length) {
    lines.push(
      `branches: [${detail.branches.map((b) => JSON.stringify(b)).join(", ")}]`,
    );
  }
  if (detail.paths?.length) {
    lines.push(
      `paths: [${detail.paths.map((p) => JSON.stringify(p)).join(", ")}]`,
    );
  }
  if (detail.ignore?.length) {
    lines.push(
      `ignore: [${detail.ignore.map((p) => JSON.stringify(p)).join(", ")}]`,
    );
  }
  if (detail.template) {
    const declTmpl = templates.find(
      (t) => t.id === detail.template && t.declaredIn?.repo === repo,
    );
    const tmplRef = declTmpl?.declaredIn?.path
      ? templateFileStem(declTmpl.declaredIn.path)
      : detail.template;
    lines.push(`template: ${tmplRef}`);
  }
  if (detail.per) lines.push(`per: ${detail.per}`);
  lines.push(`gate: ${detail.gate ? "true" : "false"}`);
  if (detail.human) lines.push(`human: ${detail.human}`);
  if (detail.auto_approve) lines.push("auto-approve: true");
  if (detail.sensitive) lines.push("sensitive: true");
  if (detail.approvers?.length) {
    lines.push(
      `approvers: [${detail.approvers.map((a) => JSON.stringify(a)).join(", ")}]`,
    );
  }
  if (detail.fixes) lines.push("fixes: true");
  return lines.join("\n");
}

function synthesizeDeclaredTemplateYaml(
  repo: string,
  filePath: string,
  t: Template,
): string {
  const lines = [`# ${repo}:${filePath}`];
  if (t.name) lines.push(`name: ${t.name}`);
  if (t.title) lines.push(`title: ${t.title}`);
  if (t.fields?.length) {
    lines.push("fields:");
    for (const f of t.fields) {
      lines.push(`  - name: ${f.name}`);
      if (f.label) lines.push(`    label: ${JSON.stringify(f.label)}`);
      if (f.required) lines.push("    required: true");
      if (f.default) lines.push(`    default: ${JSON.stringify(f.default)}`);
    }
  }
  if (t.description) {
    lines.push("prompt: |");
    for (const pl of t.description.split(/\r?\n/)) {
      lines.push(`  ${pl}`);
    }
  }
  return lines.join("\n");
}

const UPPER_QA_TOKENS = new Set(["qa", "pr", "ci", "llm", "ui", "api", "id"]);

function humanizeQaFlowKey(key: string): string {
  return key
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((word) => {
      const lower = word.toLowerCase();
      if (/^v\d+[a-z0-9]*$/i.test(word) || /^s\d+$/i.test(word)) {
        return word.toUpperCase();
      }
      if (UPPER_QA_TOKENS.has(lower)) {
        return lower.toUpperCase();
      }
      return word.charAt(0).toUpperCase() + word.slice(1);
    })
    .join(" ");
}

function formatQaFlowLabel(key: string, rawLabel?: string): string {
  const trimmed = (rawLabel ?? "").trim();
  if (!trimmed || trimmed === key) {
    return humanizeQaFlowKey(key);
  }
  const parenIdx = trimmed.indexOf(" (");
  const head = (parenIdx >= 0 ? trimmed.slice(0, parenIdx) : trimmed).trim();
  if (
    head === key ||
    head === head.toLowerCase() ||
    head.toLowerCase().replace(/[\s_]+/g, "-") === key.toLowerCase()
  ) {
    return humanizeQaFlowKey(head);
  }
  return trimmed;
}

function formatQaFlowHint(ref: string): string {
  const atIdx = ref.indexOf("@");
  if (atIdx < 0) return ref;
  const key = ref.slice(0, atIdx);
  const ver = ref.slice(atIdx + 1).replace(/^v/i, "") || "1";
  return `${key}@v${ver}`;
}

function isBenchmarkQaFlow(key: string, rawLabel?: string): boolean {
  const k = key.toLowerCase();
  const l = (rawLabel ?? "").toLowerCase();
  return (
    k === "deterministic-s3-only" ||
    k.endsWith("-skill-only") ||
    k.endsWith("-checks-only") ||
    k.endsWith("-seed") ||
    k.endsWith("-benchmark") ||
    k.includes("-ablation") ||
    k.startsWith("bench-") ||
    l.includes("ablation") ||
    l.includes("benchmark")
  );
}

interface ParsedTriggerYaml {
  on: string;
  paths: string[];
  gate: string;
  template: string;
}

function unquoteYamlScalar(raw: string): string {
  const s = raw.replace(/\s+#.*$/, "").trim();
  if (
    (s.startsWith('"') && s.endsWith('"')) ||
    (s.startsWith("'") && s.endsWith("'"))
  ) {
    return s.slice(1, -1).trim();
  }
  return s;
}

function parseInlineYamlList(raw: string): string[] {
  const inner = raw.trim().slice(1, -1).trim();
  if (!inner) return [];
  return inner
    .split(",")
    .map((part) => unquoteYamlScalar(part))
    .filter(Boolean);
}

function parseTriggerYaml(raw: string): ParsedTriggerYaml {
  const out: ParsedTriggerYaml = { on: "", paths: [], gate: "", template: "" };
  const lines = raw.split(/\r?\n/);
  let listKey: "paths" | "on" | null = null;
  for (const line of lines) {
    if (!line.trim() || line.trim().startsWith("#")) continue;
    const listMatch = /^\s*-\s+(.*)$/.exec(line);
    if (listMatch && listKey) {
      const val = unquoteYamlScalar(listMatch[1]);
      if (val) {
        if (listKey === "paths") out.paths.push(val);
        else if (listKey === "on") {
          out.on = out.on ? `${out.on}, ${val}` : val;
        }
      }
      continue;
    }
    listKey = null;
    if (!/^\s/.test(line)) {
      const kv = /^["']?([a-zA-Z0-9_-]+)["']?\s*:\s*(.*)$/.exec(line);
      if (!kv) continue;
      const key = kv[1].toLowerCase();
      const rest = kv[2].trim();
      if (key === "paths") {
        if (!rest || rest.startsWith("#")) {
          listKey = "paths";
        } else if (rest.startsWith("[") && rest.endsWith("]")) {
          out.paths = parseInlineYamlList(rest);
        } else {
          const val = unquoteYamlScalar(rest);
          if (val) out.paths = [val];
        }
      } else if (key === "on") {
        if (!rest || rest.startsWith("#")) {
          listKey = "on";
        } else if (rest.startsWith("[") && rest.endsWith("]")) {
          out.on = parseInlineYamlList(rest).join(", ");
        } else {
          out.on = unquoteYamlScalar(rest);
        }
      } else if (key === "gate") {
        out.gate = unquoteYamlScalar(rest);
      } else if (key === "template") {
        out.template = unquoteYamlScalar(rest);
      }
    }
  }
  return out;
}

const NO_RUNS: DerivedRuns = { lastRunAt: null, nextRunAt: null };

@Component({
  selector: "g-processes-page",
  imports: [
    RouterLink,
    TipDirective,
    StatusIconComponent,
    ActorChipComponent,
    ConfirmDialogComponent,
    CompositionViewComponent,
    DialogTrapDirective,
    PillSelectComponent,
    GrantsPickerComponent,
    ModelPickerComponent,
    MarkdownDirective,
    QaFlowsPanelComponent,
  ],
  encapsulation: ViewEncapsulation.None,
  styleUrls: [
    "../shared/work-surface.css",
    "../shared/surface-controls.css",
    "../inbox/inbox-queue.css",
    "../ci/ci-list.css",
    "./processes-list.css",
    "./processes-detail.css",
    "./processes-dialogs.css",
    "../shared/conversation.css",
    "../shared/code-highlight.css",
    "../shared/detail-panes.css",
    "../shared/mermaid.css",
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <section
      class="view-panel is-active"
      aria-labelledby="processes-headline"
      [attr.inert]="modalOpen() ? '' : null"
    >
      <div class="stage-header">
        <div class="view-headline-row">
          <h1
            class="view-headline"
            id="processes-headline"
            data-testid="processes-headline"
          >
            Processes
          </h1>
          <div class="headline-actions">
            @switch (activeTab()) {
              @case ("templates") {
                @if (
                  store.templates().length > 0 && canOpenLaunchTemplateDialog()
                ) {
                  <button
                    type="button"
                    class="btn-tonal"
                    data-testid="proc-launch-template-btn"
                    (click)="openLaunchTemplateDialog(selectedTemplate())"
                  >
                    Launch from template
                  </button>
                }
              }
              @case ("suites") {
                @if (
                  store.suites().length > 0 &&
                  access.can("POST /api/suite-runs")
                ) {
                  <button
                    type="button"
                    class="btn-tonal"
                    data-testid="proc-run-suite-btn"
                    (click)="openRunSuiteDialog(selectedSuite())"
                  >
                    Run suite
                  </button>
                }
              }
            }
            @if (activeTab() === "templates") {
              <button
                type="button"
                class="btn-primary"
                data-testid="proc-new-template-btn"
                [disabled]="!access.can('POST /api/templates')"
                [gTip]="
                  access.can('POST /api/templates')
                    ? 'Create a new task template.'
                    : access.deniedReason('POST /api/templates')
                "
                (click)="openTemplateDialog(null)"
              >
                + New template
              </button>
            } @else if (activeTab() === "suites") {
              <button
                type="button"
                class="btn-primary"
                data-testid="proc-new-suite-btn"
                [disabled]="!access.can('POST /api/suites')"
                [gTip]="
                  access.can('POST /api/suites')
                    ? 'Create a new multi-template suite.'
                    : access.deniedReason('POST /api/suites')
                "
                (click)="suitesView.set('suites'); openSuiteDialog(null)"
              >
                + New suite
              </button>
            } @else if (canCreateAny()) {
              <div class="pill-dropdown">
                <button
                  type="button"
                  class="btn-primary"
                  data-testid="proc-new-menu-btn"
                  aria-haspopup="menu"
                  [attr.aria-expanded]="openPicker() === 'newMenu'"
                  (click)="togglePicker('newMenu', $event)"
                >
                  New ▾
                </button>
                @if (openPicker() === "newMenu") {
                  <ul
                    class="pop-menu align-right proc-new-menu"
                    role="menu"
                    aria-label="New"
                    aria-describedby="proc-new-menu-hint"
                    data-testid="proc-new-menu"
                    tabindex="-1"
                    (keydown)="onNewMenuKeydown($event)"
                  >
                    @if (access.can("POST /api/schedules")) {
                      <li role="none">
                        <button
                          type="button"
                          role="menuitem"
                          data-testid="proc-new-schedule"
                          (click)="newProcess('schedule')"
                        >
                          Schedule
                        </button>
                      </li>
                    }
                    @if (access.can("POST /api/templates")) {
                      <li role="none">
                        <button
                          type="button"
                          role="menuitem"
                          data-testid="proc-new-template"
                          (click)="newProcess('template')"
                        >
                          Template
                        </button>
                      </li>
                    }
                    @if (access.can("POST /api/suites")) {
                      <li role="none">
                        <button
                          type="button"
                          role="menuitem"
                          data-testid="proc-new-suite"
                          (click)="newProcess('suite')"
                        >
                          Suite
                        </button>
                      </li>
                    }
                    <li
                      role="none"
                      id="proc-new-menu-hint"
                      class="proc-new-menu-hint"
                      data-testid="proc-new-menu-hint"
                    >
                      Workflow schedules and triggers are declared in a
                      repository's <code>.grain/</code>, not created here.
                    </li>
                  </ul>
                }
              </div>
            }
          </div>
        </div>
        <p class="view-subtitle">
          Standing schedules, event triggers, reusable templates, multi-pass
          suites, repository <code>.grain/</code> declarations, and pre-PR
          quality checks.
        </p>

        <nav class="tab-strip" aria-label="Process sections">
          @for (t of tabs; track t.id) {
            <a
              class="tab-link"
              [routerLink]="['/processes', t.id]"
              queryParamsHandling="merge"
              [attr.aria-current]="activeTab() === t.id ? 'page' : null"
              [attr.data-testid]="'proc-tab-' + t.id"
              [gTip]="t.tip"
              (click)="onTabClick($event, t.id)"
            >
              {{ t.label }}
              <span
                class="tab-count tabular"
                [class.warn]="
                  t.id === 'declarations' &&
                  store.awaitingAcceptancesCount() > 0
                "
                >{{ tabCount(t.id) }}</span
              >
            </a>
          }
        </nav>
      </div>

      @if (store.loadError(); as err) {
        <p
          class="banner banner-error"
          role="alert"
          data-testid="proc-load-error"
        >
          {{ err }}
        </p>
      }
      @if (store.actionError(); as err) {
        <p
          class="banner banner-error"
          role="alert"
          data-testid="proc-action-error"
        >
          {{ err }}
          <button
            type="button"
            class="btn-text"
            (click)="store.actionError.set(null)"
          >
            Dismiss
          </button>
        </p>
      }

      @if (activeTab() === "quality") {
        <g-qa-flows-panel class="view-panel-fill"></g-qa-flows-panel>
      } @else {
        <div
          class="inbox-split proc-split"
          #split
          [class.is-collapsed]="sp.collapsed()"
          [class.is-dragging]="sp.dragging()"
          [class.has-mobile-detail]="mobileDetailOpen()"
          [style.--queue-w.%]="sp.ratioExact()"
        >
          <!-- LEFT LIST PANE -->
          <section
            class="queue-pane"
            aria-label="Process items"
            data-testid="proc-list-pane"
          >
            <p class="sr-only" aria-live="polite" data-testid="proc-move-note">
              {{ moveNote() }}
            </p>
            @switch (activeTab()) {
              @case ("schedules") {
                <div
                  class="proc-subbar"
                  role="toolbar"
                  aria-label="Schedule filter"
                >
                  @for (sf of schedFilters; track sf.id) {
                    <button
                      type="button"
                      class="filter-chip"
                      [attr.aria-pressed]="schedFilter() === sf.id"
                      [attr.data-testid]="'sched-filter-' + sf.id"
                      (click)="schedFilter.set(sf.id)"
                    >
                      {{ sf.label }}
                    </button>
                  }
                  <g-pill-select
                    class="proc-sort"
                    prefix="Sort"
                    [options]="sortOptions()"
                    [value]="activeSort()"
                    ariaLabel="Sort"
                    testid="proc-sort"
                    (valueChange)="setSort($event)"
                  />
                </div>
                @if (store.loaded() && scheduleRows().length === 0) {
                  <div class="empty-state" data-testid="schedules-empty">
                    <p>No schedules or event triggers match this view.</p>
                  </div>
                } @else {
                  <ul class="proc-list">
                    @for (r of scheduleRows(); track r.key) {
                      @if (r.trigger; as tr) {
                        <li
                          class="proc-li"
                          [class.is-drop-target]="dropKey() === r.key"
                          [attr.draggable]="
                            activeSort() === 'custom' ? 'true' : null
                          "
                          (dragstart)="onDragStart($event, r.key)"
                          (dragover)="onDragOver($event, r.key)"
                          (dragleave)="onDragLeave(r.key)"
                          (drop)="onDrop($event, 'schedules', r.key)"
                          (dragend)="onDragEnd()"
                        >
                          <button
                            type="button"
                            class="proc-row"
                            [class.is-selected]="selectedTriggerKey() === r.key"
                            [attr.aria-current]="
                              selectedTriggerKey() === r.key ? 'true' : null
                            "
                            [attr.data-testid]="'trigger-row-' + r.key"
                            [attr.aria-keyshortcuts]="
                              activeSort() === 'custom'
                                ? 'Alt+ArrowUp Alt+ArrowDown'
                                : null
                            "
                            (click)="selectTrigger(tr, true)"
                            (keydown)="onRowKey($event, 'schedules', r.key)"
                            (keydown.space)="
                              selectTrigger(tr, true); $event.preventDefault()
                            "
                          >
                            <div class="proc-row-top">
                              @if (activeSort() === "custom") {
                                <span
                                  class="proc-drag-grip"
                                  aria-hidden="true"
                                  data-testid="proc-drag-grip"
                                  [gTip]="'Drag or press Alt+Up/Down to reorder'"
                                >
                                  <svg
                                    width="14"
                                    height="14"
                                    viewBox="0 0 16 16"
                                    fill="currentColor"
                                    aria-hidden="true"
                                  >
                                    <circle cx="5.5" cy="3.5" r="1.25" />
                                    <circle cx="10.5" cy="3.5" r="1.25" />
                                    <circle cx="5.5" cy="8" r="1.25" />
                                    <circle cx="10.5" cy="8" r="1.25" />
                                    <circle cx="5.5" cy="12.5" r="1.25" />
                                    <circle cx="10.5" cy="12.5" r="1.25" />
                                  </svg>
                                </span>
                              }
                              <span class="status-pill reply">
                                <g-status-icon tone="reply" kind="reply" />
                                Declared
                              </span>
                              <span class="proc-row-title">{{
                                triggerName(tr.path)
                              }}</span>
                              <span class="ci-provider-tag">Trigger</span>
                              <span class="ci-provider-tag">.grain</span>
                            </div>
                            <div class="proc-row-sub">
                              <span class="tabular">{{ tr.repo }}</span>
                              <span class="tabular">· {{ tr.path }}</span>
                              <span>· next: on change</span>
                            </div>
                          </button>
                        </li>
                      }
                      @if (r.schedule; as s) {
                        @let badge = schedBadge(s);
                        <li
                          class="proc-li"
                          [class.is-drop-target]="
                            dropKey() === 'schedule:' + s.id
                          "
                          [attr.draggable]="
                            activeSort() === 'custom' ? 'true' : null
                          "
                          (dragstart)="onDragStart($event, 'schedule:' + s.id)"
                          (dragover)="onDragOver($event, 'schedule:' + s.id)"
                          (dragleave)="onDragLeave('schedule:' + s.id)"
                          (drop)="
                            onDrop($event, 'schedules', 'schedule:' + s.id)
                          "
                          (dragend)="onDragEnd()"
                        >
                          <button
                            type="button"
                            class="proc-row"
                            [class.is-selected]="selectedScheduleId() === s.id"
                            [attr.aria-current]="
                              selectedScheduleId() === s.id ? 'true' : null
                            "
                            [attr.data-testid]="'sched-row-' + s.id"
                            [attr.aria-keyshortcuts]="
                              activeSort() === 'custom'
                                ? 'Alt+ArrowUp Alt+ArrowDown'
                                : null
                            "
                            (click)="selectSchedule(s.id, true)"
                            (keydown)="
                              onRowKey($event, 'schedules', 'schedule:' + s.id)
                            "
                            (keydown.space)="
                              selectSchedule(s.id, true);
                              $event.preventDefault()
                            "
                          >
                            <div class="proc-row-top">
                              @if (activeSort() === "custom") {
                                <span
                                  class="proc-drag-grip"
                                  aria-hidden="true"
                                  data-testid="proc-drag-grip"
                                  [gTip]="'Drag or press Alt+Up/Down to reorder'"
                                >
                                  <svg
                                    width="14"
                                    height="14"
                                    viewBox="0 0 16 16"
                                    fill="currentColor"
                                    aria-hidden="true"
                                  >
                                    <circle cx="5.5" cy="3.5" r="1.25" />
                                    <circle cx="10.5" cy="3.5" r="1.25" />
                                    <circle cx="5.5" cy="8" r="1.25" />
                                    <circle cx="10.5" cy="8" r="1.25" />
                                    <circle cx="5.5" cy="12.5" r="1.25" />
                                    <circle cx="10.5" cy="12.5" r="1.25" />
                                  </svg>
                                </span>
                              }
                              <span [class]="'status-pill ' + badge.tone">
                                <g-status-icon
                                  [tone]="badge.tone"
                                  [kind]="badge.iconKind"
                                />
                                {{ badge.label }}
                              </span>
                              <span class="proc-row-title">{{
                                s.title || s.templateName || s.suiteName || s.id
                              }}</span>
                              @if (isTrigger(s)) {
                                <span class="ci-provider-tag">Trigger</span>
                              }
                              @if (s.declaredIn) {
                                <span class="ci-provider-tag">.grain</span>
                              }
                            </div>
                            <div class="proc-row-sub">
                              <span>{{
                                cadenceOf(s.recurrence, s.timeZone)
                              }}</span>
                              @if (schedTargetOf(s).repo; as repo) {
                                <span class="tabular">· {{ repo }}</span>
                              }
                              @if (schedNextRunOf(s); as nr) {
                                <span class="tabular"
                                  >· next: {{ stamp(nr) }}</span
                                >
                              }
                              <span>· overlap: {{ s.overlap || "skip" }}</span>
                            </div>
                          </button>
                        </li>
                      }
                    }
                  </ul>
                }
              }

              @case ("templates") {
                <div
                  class="proc-subbar"
                  role="toolbar"
                  aria-label="Template filter"
                >
                  @for (tf of tmplFilters; track tf.id) {
                    <button
                      type="button"
                      class="filter-chip"
                      [attr.aria-pressed]="tmplFilter() === tf.id"
                      [attr.data-testid]="'tmpl-filter-' + tf.id"
                      (click)="tmplFilter.set(tf.id)"
                    >
                      {{ tf.label }}
                    </button>
                  }
                  <g-pill-select
                    class="proc-sort"
                    prefix="Sort"
                    [options]="sortOptions()"
                    [value]="activeSort()"
                    ariaLabel="Sort"
                    testid="proc-sort"
                    (valueChange)="setSort($event)"
                  />
                </div>
                @if (store.loaded() && filteredTemplates().length === 0) {
                  <div class="empty-state" data-testid="templates-empty">
                    <p>No task templates match this filter.</p>
                  </div>
                } @else {
                  <ul class="proc-list">
                    @for (t of sortedTemplates(); track t.id) {
                      @let badge = tmplBadge(t);
                      @let runs = templateRuns().get(t.id) ?? noRuns;
                      <li
                        class="proc-li"
                        [class.is-drop-target]="
                          dropKey() === 'template:' + t.id
                        "
                        [attr.draggable]="
                          activeSort() === 'custom' ? 'true' : null
                        "
                        (dragstart)="onDragStart($event, 'template:' + t.id)"
                        (dragover)="onDragOver($event, 'template:' + t.id)"
                        (dragleave)="onDragLeave('template:' + t.id)"
                        (drop)="onDrop($event, 'templates', 'template:' + t.id)"
                        (dragend)="onDragEnd()"
                      >
                        <button
                          type="button"
                          class="proc-row"
                          [class.is-selected]="selectedTemplateId() === t.id"
                          [attr.aria-current]="
                            selectedTemplateId() === t.id ? 'true' : null
                          "
                          [attr.data-testid]="'tmpl-row-' + t.id"
                          [attr.aria-keyshortcuts]="
                            activeSort() === 'custom'
                              ? 'Alt+ArrowUp Alt+ArrowDown'
                              : null
                          "
                          (click)="selectTemplate(t.id, true)"
                          (keydown)="
                            onRowKey($event, 'templates', 'template:' + t.id)
                          "
                          (keydown.space)="
                            selectTemplate(t.id, true); $event.preventDefault()
                          "
                        >
                          <div class="proc-row-top">
                            @if (activeSort() === "custom") {
                              <span
                                class="proc-drag-grip"
                                aria-hidden="true"
                                data-testid="proc-drag-grip"
                                [gTip]="'Drag or press Alt+Up/Down to reorder'"
                              >
                                <svg
                                  width="14"
                                  height="14"
                                  viewBox="0 0 16 16"
                                  fill="currentColor"
                                  aria-hidden="true"
                                >
                                  <circle cx="5.5" cy="3.5" r="1.25" />
                                  <circle cx="10.5" cy="3.5" r="1.25" />
                                  <circle cx="5.5" cy="8" r="1.25" />
                                  <circle cx="10.5" cy="8" r="1.25" />
                                  <circle cx="5.5" cy="12.5" r="1.25" />
                                  <circle cx="10.5" cy="12.5" r="1.25" />
                                </svg>
                              </span>
                            }
                            <span [class]="'status-pill ' + badge.tone">
                              <g-status-icon
                                [tone]="badge.tone"
                                [kind]="badge.iconKind"
                              />
                              {{ badge.label }}
                            </span>
                            <span class="proc-row-title">{{
                              t.name || t.title || t.id
                            }}</span>
                            @if ((t.fields ?? []).length > 0) {
                              <span class="ci-provider-tag tabular">{{
                                countOf((t.fields ?? []).length, "param")
                              }}</span>
                            }
                          </div>
                          <div class="proc-row-sub">
                            <span>{{ t.title }}</span>
                            @if (t.declaredIn) {
                              <span class="tabular"
                                >· {{ t.declaredIn.repo }} ({{
                                  t.declaredIn.path
                                }})</span
                              >
                            } @else if (t.repo) {
                              <span class="tabular"
                                >· bound to {{ t.repo }}</span
                              >
                            }
                            @if (runs.lastRunAt; as lr) {
                              <span
                                class="tabular"
                                [attr.data-testid]="'tmpl-last-' + t.id"
                                >· last: {{ stamp(lr) }}</span
                              >
                            }
                            <span
                              class="tabular"
                              [attr.data-testid]="'tmpl-next-' + t.id"
                              >·
                              {{
                                runs.nextRunAt
                                  ? "next: " + stamp(runs.nextRunAt)
                                  : "on demand"
                              }}</span
                            >
                          </div>
                        </button>
                      </li>
                    }
                  </ul>
                }
              }

              @case ("suites") {
                <div
                  class="proc-subbar"
                  role="toolbar"
                  aria-label="Suites view"
                >
                  <div
                    class="segmented"
                    role="group"
                    aria-label="Suites or runs"
                  >
                    <button
                      type="button"
                      data-testid="suites-subview-suites"
                      [attr.aria-pressed]="suitesView() === 'suites'"
                      (click)="suitesView.set('suites')"
                    >
                      Suites ({{ filteredSuites().length }})
                    </button>
                    <button
                      type="button"
                      data-testid="suites-subview-runs"
                      [attr.aria-pressed]="suitesView() === 'runs'"
                      (click)="suitesView.set('runs')"
                    >
                      Suite runs ({{ filteredSuiteRuns().length }})
                    </button>
                  </div>
                  @if (suitesView() === "suites") {
                    <g-pill-select
                      class="proc-sort"
                      prefix="Sort"
                      [options]="sortOptions()"
                      [value]="activeSort()"
                      ariaLabel="Sort"
                      testid="proc-sort"
                      (valueChange)="setSort($event)"
                    />
                  }
                </div>

                @if (suitesView() === "suites") {
                  @if (store.loaded() && filteredSuites().length === 0) {
                    <div class="empty-state" data-testid="suites-empty">
                      <p>No suites configured yet.</p>
                    </div>
                  } @else {
                    <ul class="proc-list">
                      @for (st of sortedSuites(); track st.id) {
                        @let runs = suiteRunsById().get(st.id) ?? noRuns;
                        <li
                          class="proc-li"
                          [class.is-drop-target]="
                            dropKey() === 'suite:' + st.id
                          "
                          [attr.draggable]="
                            activeSort() === 'custom' ? 'true' : null
                          "
                          (dragstart)="onDragStart($event, 'suite:' + st.id)"
                          (dragover)="onDragOver($event, 'suite:' + st.id)"
                          (dragleave)="onDragLeave('suite:' + st.id)"
                          (drop)="onDrop($event, 'suites', 'suite:' + st.id)"
                          (dragend)="onDragEnd()"
                        >
                          <button
                            type="button"
                            class="proc-row"
                            [class.is-selected]="
                              selectedSuiteRunId() === null &&
                              selectedSuiteId() === st.id
                            "
                            [attr.aria-current]="
                              selectedSuiteRunId() === null &&
                              selectedSuiteId() === st.id
                                ? 'true'
                                : null
                            "
                            [attr.data-testid]="'suite-row-' + st.id"
                            [attr.aria-keyshortcuts]="
                              activeSort() === 'custom'
                                ? 'Alt+ArrowUp Alt+ArrowDown'
                                : null
                            "
                            (click)="selectSuite(st.id, true)"
                            (keydown)="
                              onRowKey($event, 'suites', 'suite:' + st.id)
                            "
                            (keydown.space)="
                              selectSuite(st.id, true); $event.preventDefault()
                            "
                          >
                            <div class="proc-row-top">
                              @if (activeSort() === "custom") {
                                <span
                                  class="proc-drag-grip"
                                  aria-hidden="true"
                                  data-testid="proc-drag-grip"
                                  [gTip]="'Drag or press Alt+Up/Down to reorder'"
                                >
                                  <svg
                                    width="14"
                                    height="14"
                                    viewBox="0 0 16 16"
                                    fill="currentColor"
                                    aria-hidden="true"
                                  >
                                    <circle cx="5.5" cy="3.5" r="1.25" />
                                    <circle cx="10.5" cy="3.5" r="1.25" />
                                    <circle cx="5.5" cy="8" r="1.25" />
                                    <circle cx="10.5" cy="8" r="1.25" />
                                    <circle cx="5.5" cy="12.5" r="1.25" />
                                    <circle cx="10.5" cy="12.5" r="1.25" />
                                  </svg>
                                </span>
                              }
                              <span class="status-pill proposal">
                                <g-status-icon
                                  tone="proposal"
                                  kind="proposal"
                                />
                                {{
                                  suiteModeOf(st.mode, st.count, st.maxPasses)
                                }}
                              </span>
                              <span class="proc-row-title">{{ st.name }}</span>
                              @if (st.declaredIn) {
                                <span class="ci-provider-tag">.grain</span>
                              }
                            </div>
                            <div class="proc-row-sub">
                              <span class="tabular">{{
                                countOf(st.items.length, "template")
                              }}</span>
                              @if (st.autoMerge) {
                                <span>· auto-merge</span>
                              }
                              @if (st.requireApproval) {
                                <span>· requires approval</span>
                              }
                              @if (runs.lastRunAt; as lr) {
                                <span
                                  class="tabular"
                                  [attr.data-testid]="'suite-last-' + st.id"
                                  >· last: {{ stamp(lr) }}</span
                                >
                              }
                              <span
                                class="tabular"
                                [attr.data-testid]="'suite-next-' + st.id"
                                >·
                                {{
                                  runs.nextRunAt
                                    ? "next: " + stamp(runs.nextRunAt)
                                    : "on demand"
                                }}</span
                              >
                            </div>
                          </button>
                        </li>
                      }
                    </ul>
                  }
                } @else {
                  @if (store.loaded() && filteredSuiteRuns().length === 0) {
                    <div class="empty-state" data-testid="suite-runs-empty">
                      <p>No suite runs recorded yet.</p>
                    </div>
                  } @else {
                    <ul class="proc-list">
                      @for (sr of filteredSuiteRuns(); track sr.id) {
                        @let rBadge = runBadgeOf(sr.status);
                        <li class="proc-li">
                          <button
                            type="button"
                            class="proc-row"
                            [class.is-selected]="selectedSuiteRunId() === sr.id"
                            [attr.aria-current]="
                              selectedSuiteRunId() === sr.id ? 'true' : null
                            "
                            [attr.data-testid]="'suite-run-row-' + sr.id"
                            (click)="selectSuiteRun(sr.id, true)"
                            (keydown.space)="
                              selectSuiteRun(sr.id, true);
                              $event.preventDefault()
                            "
                          >
                            <div class="proc-row-top">
                              <span [class]="'status-pill ' + rBadge.tone">
                                <g-status-icon
                                  [tone]="rBadge.tone"
                                  [kind]="rBadge.iconKind"
                                />
                                {{ rBadge.label }}
                              </span>
                              <span class="proc-row-title"
                                >{{ sr.suiteName || sr.suiteId }} #{{
                                  sr.id
                                }}</span
                              >
                              @if (sr.pass > 0) {
                                <span class="ci-provider-tag tabular"
                                  >Pass {{ sr.pass }}</span
                                >
                              }
                            </div>
                            <div class="proc-row-sub">
                              <span class="tabular"
                                >{{ sr.repo }} &#64;
                                {{ sr.base || "default branch" }}</span
                              >
                              <span class="tabular"
                                >·
                                {{
                                  countOf(
                                    sr.taskCount ?? sr.tasks?.length ?? 0,
                                    "task"
                                  )
                                }}</span
                              >
                            </div>
                          </button>
                        </li>
                      }
                    </ul>
                  }
                }
              }

              @case ("declarations") {
                @if (
                  store.knownRepos().length === 0 &&
                  store.acceptances().length === 0
                ) {
                  <div class="empty-state" data-testid="declarations-empty">
                    <p>
                      No repositories or .grain/ declarations discovered yet.
                    </p>
                  </div>
                } @else {
                  <ul class="proc-list">
                    @for (repo of filteredDeclRepos(); track repo) {
                      @let pendingCount = repoPendingAcceptances(repo);
                      <li class="proc-li">
                        <button
                          type="button"
                          class="proc-row"
                          [class.is-selected]="selectedDeclRepo() === repo"
                          [attr.aria-current]="
                            selectedDeclRepo() === repo ? 'true' : null
                          "
                          [attr.data-testid]="'decl-repo-row-' + repo"
                          (click)="selectDeclRepo(repo, true)"
                          (keydown.space)="
                            selectDeclRepo(repo, true); $event.preventDefault()
                          "
                        >
                          <div class="proc-row-top">
                            <span
                              [class]="
                                pendingCount > 0
                                  ? 'status-pill blast'
                                  : 'status-pill healthy'
                              "
                            >
                              <g-status-icon
                                [tone]="pendingCount > 0 ? 'blast' : 'healthy'"
                                [kind]="pendingCount > 0 ? 'blast' : 'healthy'"
                              />
                              {{
                                pendingCount > 0
                                  ? pendingCount + " awaiting acceptance"
                                  : "Synced"
                              }}
                            </span>
                            <span class="proc-row-title tabular">{{
                              repo
                            }}</span>
                          </div>
                          <div class="proc-row-sub">
                            @if (store.repoDefaults()[repo]; as def) {
                              <span>
                                .grain declarations:
                                {{ def.declarations ? "enabled" : "disabled" }}
                              </span>
                              @if (def.declarationsStatus?.error) {
                                <span style="color: var(--md-sys-color-error)"
                                  >· sync error</span
                                >
                              }
                            } @else {
                              <span
                                >Inspect .grain/ declarations & workflows</span
                              >
                            }
                          </div>
                        </button>
                      </li>
                    }
                  </ul>
                }
              }
            }
          </section>

          <!-- RESIZER -->
          <div
            class="inbox-resizer"
            role="separator"
            tabindex="0"
            aria-orientation="vertical"
            aria-label="Resize process list and inspector"
            [attr.aria-valuenow]="sp.ratioPct()"
            [attr.aria-valuemin]="sp.minPct()"
            [attr.aria-valuemax]="sp.maxPct()"
            (pointerdown)="sp.onResizeStart($event)"
            (pointermove)="sp.onResizeMove($event)"
            (pointerup)="sp.onResizeEnd($event)"
            (pointercancel)="sp.onResizeCancel()"
            (dblclick)="sp.reset()"
            (keydown)="sp.onKey($event)"
          >
            <span class="inbox-resizer-pill" aria-hidden="true"></span>
          </div>

          <!-- RIGHT DETAIL PANE -->
          <section
            class="proc-inspector"
            aria-label="Process detail inspector"
            data-testid="proc-inspector"
          >
            @switch (activeTab()) {
              @case ("schedules") {
                @if (selectedSchedule(); as s) {
                  @let badge = schedBadge(s);
                  <div class="proc-insp-head">
                    <div class="proc-insp-top">
                      <div class="headline-cluster">
                        <button
                          type="button"
                          class="btn-text proc-mobile-back"
                          (click)="closeMobileDetail()"
                        >
                          ← Back
                        </button>
                        <span [class]="'status-pill ' + badge.tone">
                          <g-status-icon
                            [tone]="badge.tone"
                            [kind]="badge.iconKind"
                          />
                          {{ badge.label }}
                        </span>
                        <h2
                          class="proc-insp-title"
                          data-testid="sched-detail-title"
                        >
                          {{ s.title || s.templateName || s.suiteName || s.id }}
                        </h2>
                      </div>

                      <div class="headline-actions">
                        <button
                          type="button"
                          class="btn-primary"
                          data-testid="sched-run-now-btn"
                          [disabled]="
                            !access.can('POST /api/schedules/{id}/run', s.repo)
                          "
                          [gTip]="
                            access.deniedReason(
                              'POST /api/schedules/{id}/run',
                              s.repo
                            ) ||
                            (s.type === 'workflow'
                              ? 'Starts a CI run of the declared workflow straight away, to be reviewed when it finishes.'
                              : 'Fire this schedule immediately.')
                          "
                          (click)="runScheduleNow(s)"
                        >
                          Run now
                        </button>
                        <button
                          type="button"
                          class="btn-tonal"
                          data-testid="sched-toggle-enabled-btn"
                          [disabled]="
                            !!s.declaredIn ||
                            !access.can('PATCH /api/schedules/{id}', s.repo)
                          "
                          [gTip]="
                            s.declaredIn
                              ? 'Declared in ' +
                                s.declaredIn.path +
                                ' — edit in repository.'
                              : ''
                          "
                          (click)="toggleScheduleEnabled(s)"
                        >
                          {{ s.enabled ? "Pause" : "Resume" }}
                        </button>
                        @if (!s.declaredIn) {
                          <button
                            type="button"
                            class="btn-tonal"
                            data-testid="sched-edit-btn"
                            [disabled]="
                              !access.can('PATCH /api/schedules/{id}', s.repo)
                            "
                            (click)="openScheduleDialog(s)"
                          >
                            Edit
                          </button>
                        }
                        @if (!s.declaredIn && !s.system) {
                          <button
                            type="button"
                            class="btn-text danger"
                            data-testid="sched-delete-btn"
                            [disabled]="
                              !access.can('DELETE /api/schedules/{id}', s.repo)
                            "
                            (click)="confirmDeleteSchedule(s)"
                          >
                            Delete
                          </button>
                        }
                      </div>
                    </div>

                    @if (s.qaFlowRef) {
                      <div class="proc-meta-row">
                        <span
                          class="ci-provider-tag"
                          data-testid="process-qa-flow-badge"
                          >Workflow: {{ s.qaFlowRef }}</span
                        >
                      </div>
                    }
                    @if (s.declaredIn) {
                      <p class="banner banner-info" style="margin: 6px 0 0">
                        Declared in <code>{{ s.declaredIn.repo }}</code> at
                        <code>{{ s.declaredIn.path }}</code
                        >. Update that file in git to modify this schedule.
                      </p>
                    }
                    @if (s.suppressed; as sup) {
                      <div
                        class="banner banner-error"
                        style="margin: 6px 0 0"
                        data-testid="sched-suppressed-banner"
                      >
                        <!-- The server's Detail sentence follows the reason
                             when it sent one (without repeating "skipped: " or
                             a reason already leading Detail); without one the
                             reason stands alone. -->
                        {{ formatSuppression(sup) }}
                        @if (sup.taskId) {
                          —
                          <a [routerLink]="['/task', sup.taskId]"
                            >Open task #{{ sup.taskId }}</a
                          >
                        }
                      </div>
                    }
                  </div>

                  <div class="g-panes">
                    <section
                      class="g-pane"
                      aria-labelledby="sched-prompt-h"
                      data-testid="sched-prompt-pane"
                    >
                      <h3 id="sched-prompt-h" class="g-pane-title">Prompt</h3>
                      @if (s.description) {
                        <div
                          class="msg-body g-pane-prompt"
                          data-testid="sched-prompt"
                          [gMarkdown]="s.description"
                        ></div>
                      } @else {
                        <p class="g-pane-empty">
                          No prompt of its own{{
                            s.templateName || s.suiteName
                              ? " — it files from " +
                                (s.templateName || s.suiteName)
                              : ""
                          }}.
                        </p>
                      }
                    </section>
                    <section
                      class="g-pane"
                      aria-labelledby="sched-config-h"
                      data-testid="sched-config-pane"
                    >
                      <h3 id="sched-config-h" class="g-pane-title">
                        Configuration
                      </h3>
                      <dl class="g-attrs">
                        <dt>Source</dt>
                        <dd>
                          <g-actor-chip
                            [label]="
                              s.type === 'workflow'
                                ? 'workflow'
                                : s.source || 'deployment'
                            "
                            [kind]="
                              s.system && s.type !== 'workflow'
                                ? 'grain'
                                : 'service'
                            "
                          />
                        </dd>
                        <dt>Cadence</dt>
                        <dd>{{ cadenceOf(s.recurrence, s.timeZone) }}</dd>
                        @if (s.templateName || s.templateId) {
                          <dt>Template</dt>
                          <dd>{{ s.templateName || s.templateId }}</dd>
                        }
                        @if (s.suiteName || s.suiteId) {
                          <dt>Suite</dt>
                          <dd>{{ s.suiteName || s.suiteId }}</dd>
                        }
                        @let target = schedTargetOf(s);
                        <dt>Repository</dt>
                        <dd class="tabular" data-testid="sched-repo">
                          {{ target.repo || "None" }}
                        </dd>
                        @if (target.base) {
                          <dt>Base</dt>
                          <dd class="tabular" data-testid="sched-base">
                            {{ target.base }}
                          </dd>
                        }
                        @if (schedModelOf(s); as model) {
                          <dt>Model</dt>
                          <dd data-testid="sched-model-val">{{ model }}</dd>
                        }
                        @if (!s.suiteId && s.type !== "workflow") {
                          <dt>Admission</dt>
                          <dd data-testid="sched-admission-val">
                            {{ schedAdmissionOf(s) }}
                          </dd>
                          <dt>Hosting</dt>
                          <dd data-testid="sched-hosting-val">
                            {{ schedHostingOf(s) }}
                          </dd>
                          <dt>Keep running</dt>
                          <dd data-testid="sched-keep-running-val">
                            {{ schedKeepRunningOf(s) ? "on" : "off" }}
                          </dd>
                        }
                        @if (s.networkAccess) {
                          <dt>Network</dt>
                          <dd data-testid="sched-network-val">
                            {{ networkLabelOf(s.networkAccess) }}
                          </dd>
                        }
                        @if ((s.reads ?? []).length > 0) {
                          <dt>Reads</dt>
                          <dd class="tabular" data-testid="sched-reads-val">
                            {{ (s.reads ?? []).join(", ") }}
                          </dd>
                        }
                        @if ((s.writes ?? []).length > 0) {
                          <dt>Writes</dt>
                          <dd class="tabular" data-testid="sched-writes-val">
                            {{ formatWrites(s.writes) }}
                          </dd>
                        }
                        <dt>Overlap</dt>
                        <dd>{{ s.overlap || "skip" }}</dd>
                        @if (s.overlapGroup) {
                          <dt>Overlap group</dt>
                          <dd data-testid="sched-overlap-group">
                            {{ s.overlapGroup }}
                          </dd>
                        }
                        <dt>Auto-merge</dt>
                        <dd>{{ s.autoMerge ? "on" : "off" }}</dd>
                        <!-- An event trigger's nextRunAt is a placeholder a year
                           out (model.Schedule); schedNextRunOf labels it. -->
                        @if (schedNextRunOf(s); as nr) {
                          <dt>Next run</dt>
                          <dd class="tabular" data-testid="sched-next-run">
                            {{ stamp(nr) }}
                          </dd>
                        }
                        @if (s.lastRunAt) {
                          <dt>Last run</dt>
                          <dd class="tabular" data-testid="sched-last-run">
                            {{ stamp(s.lastRunAt) }}
                          </dd>
                        }
                      </dl>
                    </section>
                  </div>

                  <g-composition-view [composition]="scheduleComposition()" />

                  <div class="proc-card">
                    <h3 class="section-title">Recent fired tasks & skips</h3>
                    @if (s.lastWorkflowRun; as lwr) {
                      <div class="proc-kv-item">
                        <span>
                          Workflow run
                          @if (lwr.runId) {
                            <a [routerLink]="['/ci', 'runs', lwr.runId]">{{
                              lwr.runId
                            }}</a>
                          }
                          ({{ lwr.conclusion || "running" }})
                        </span>
                        @if (lwr.reviewTaskId) {
                          <a [routerLink]="['/task', lwr.reviewTaskId]"
                            >Review #{{ lwr.reviewTaskId }}</a
                          >
                        }
                      </div>
                    }
                    @if (scheduledTasksFor(s).length > 0) {
                      <ul class="proc-kv-list">
                        @for (t of scheduledTasksFor(s); track t.id) {
                          <li class="proc-kv-item">
                            <a [routerLink]="['/task', t.id]"
                              >#{{ t.id }} · {{ t.title }}</a
                            >
                            <span class="ci-provider-tag">{{ t.state }}</span>
                          </li>
                        }
                      </ul>
                    } @else if (
                      (s.skips ?? []).length === 0 && !s.lastWorkflowRun
                    ) {
                      <p class="muted" style="margin: 0">
                        No recent fired tasks in the active feed.
                      </p>
                    }
                    @for (sk of (s.skips ?? []).slice(0, 5); track sk.at) {
                      <div
                        class="proc-kv-item proc-skip-row"
                        data-testid="sched-skip"
                      >
                        <span class="muted">{{ sk.summary }}</span>
                        <span class="proc-skip-refs">
                          @if (sk.taskId) {
                            <a [routerLink]="['/task', sk.taskId]"
                              >#{{ sk.taskId }}</a
                            >
                          }
                          @if (sk.outcome === "fired") {
                            <span data-testid="sched-skip-outcome">
                              → fired
                              @if (sk.firedTaskId) {
                                <a [routerLink]="['/task', sk.firedTaskId]"
                                  >#{{ sk.firedTaskId }}</a
                                >
                              }
                              @if (skipFolded(sk); as folded) {
                                · {{ folded }}
                              }
                            </span>
                          } @else if (sk.outcome === "dropped") {
                            <span data-testid="sched-skip-outcome"
                              >→ dropped</span
                            >
                          } @else if (!sk.outcome || sk.outcome === "held") {
                            <span data-testid="sched-skip-outcome">→ held</span>
                          } @else {
                            <span data-testid="sched-skip-outcome"
                              >→ {{ sk.outcome }}</span
                            >
                          }
                        </span>
                      </div>
                    }
                  </div>
                } @else {
                  @if (selectedTrigger(); as tr) {
                    @let parsed = parsedSelectedTrigger();
                    <div
                      class="trigger-inspector"
                      data-testid="trigger-inspector"
                    >
                      <div class="proc-insp-head">
                        <div class="proc-insp-top">
                          <div class="headline-cluster">
                            <button
                              type="button"
                              class="btn-text proc-mobile-back"
                              (click)="closeMobileDetail()"
                            >
                              ← Back
                            </button>
                            <span class="status-pill reply">
                              <g-status-icon tone="reply" kind="reply" />
                              Declared
                            </span>
                            <h2
                              class="proc-insp-title"
                              data-testid="trigger-detail-title"
                            >
                              {{ triggerName(tr.path) }}
                            </h2>
                          </div>

                          <div class="headline-actions">
                            <a
                              class="btn-tonal"
                              [routerLink]="repoAdvancedRoute(tr.repo)"
                              data-testid="trigger-open-repo-link"
                              >Open repository settings</a
                            >
                          </div>
                        </div>

                        <p
                          class="banner banner-info"
                          style="margin: 6px 0 0"
                          data-testid="trigger-declared-banner"
                        >
                          Declared in <code>{{ tr.repo }}</code> at
                          <code>{{ tr.path }}</code
                          >.
                        </p>
                      </div>

                      <div class="g-panes">
                        <section
                          class="g-pane"
                          aria-labelledby="trigger-config-h"
                          data-testid="trigger-config-pane"
                        >
                          <h3 id="trigger-config-h" class="g-pane-title">
                            Configuration
                          </h3>
                          <dl class="g-attrs">
                            <dt>Repository</dt>
                            <dd class="tabular">{{ tr.repo }}</dd>
                            <dt>File path</dt>
                            <dd class="tabular">
                              <code>{{ tr.path }}</code>
                            </dd>
                            <dt>On</dt>
                            <dd class="tabular">{{ parsed.on || "—" }}</dd>
                            <dt>Paths</dt>
                            <dd class="tabular">
                              {{
                                parsed.paths.length
                                  ? parsed.paths.join(", ")
                                  : "—"
                              }}
                            </dd>
                            <dt>Gate</dt>
                            <dd class="tabular">{{ parsed.gate || "—" }}</dd>
                            <dt>Template</dt>
                            <dd>
                              @if (matchedTriggerTemplate(); as mt) {
                                <button
                                  type="button"
                                  class="btn-text tabular"
                                  data-testid="trigger-template-link"
                                  (click)="openTriggerTemplate(mt.id)"
                                >
                                  {{ mt.name || mt.title || parsed.template }}
                                </button>
                              } @else {
                                <span class="tabular">{{
                                  parsed.template || "—"
                                }}</span>
                              }
                            </dd>
                          </dl>
                        </section>

                        <section
                          class="g-pane"
                          aria-labelledby="trigger-yaml-h"
                          data-testid="trigger-yaml-pane"
                        >
                          <h3 id="trigger-yaml-h" class="g-pane-title">
                            Declared trigger YAML
                          </h3>
                          <pre
                            class="comp-body-well s-mono"
                            data-testid="trigger-yaml-preview"
                            [textContent]="triggerYamlDisplay()"
                          ></pre>
                        </section>
                      </div>
                    </div>
                  } @else {
                    <div class="empty-state">
                      <p>
                        Select a schedule or event trigger to inspect its
                        cadence and composition.
                      </p>
                    </div>
                  }
                }
              }

              @case ("templates") {
                @if (selectedTemplate(); as t) {
                  @let badge = tmplBadge(t);
                  <div class="proc-insp-head">
                    <div class="proc-insp-top">
                      <div class="headline-cluster">
                        <button
                          type="button"
                          class="btn-text proc-mobile-back"
                          (click)="closeMobileDetail()"
                        >
                          ← Back
                        </button>
                        <span [class]="'status-pill ' + badge.tone">
                          <g-status-icon
                            [tone]="badge.tone"
                            [kind]="badge.iconKind"
                          />
                          {{ badge.label }}
                        </span>
                        <h2
                          class="proc-insp-title"
                          data-testid="tmpl-detail-title"
                        >
                          {{ t.name || t.title }}
                        </h2>
                      </div>

                      <div class="headline-actions">
                        <button
                          type="button"
                          class="btn-primary"
                          data-testid="tmpl-launch-btn"
                          [disabled]="!store.canLaunchTemplate(t)"
                          (click)="openLaunchTemplateDialog(t)"
                        >
                          Launch task
                        </button>
                        @if (!t.declaredIn) {
                          <button
                            type="button"
                            class="btn-tonal"
                            data-testid="tmpl-new-task-btn"
                            [disabled]="!store.canLaunchTemplate(t)"
                            [gTip]="
                              store.canLaunchTemplate(t)
                                ? 'Open the New task sheet pre-filled with this template.'
                                : access.deniedReason(
                                    'POST /api/tasks',
                                    t.repo || undefined
                                  )
                            "
                            (click)="openNewTaskFromTemplate(t, $event)"
                          >
                            New task from this template
                          </button>
                        }
                        @if (!t.declaredIn && !t.system) {
                          <button
                            type="button"
                            class="btn-tonal"
                            data-testid="tmpl-edit-btn"
                            [disabled]="
                              !access.can('PATCH /api/templates/{id}')
                            "
                            (click)="openTemplateDialog(t)"
                          >
                            Edit
                          </button>
                          <button
                            type="button"
                            class="btn-text danger"
                            data-testid="tmpl-delete-btn"
                            [disabled]="
                              !access.can('DELETE /api/templates/{id}')
                            "
                            (click)="confirmDeleteTemplate(t)"
                          >
                            Delete
                          </button>
                        }
                      </div>
                    </div>

                    @if (t.qaFlowRef) {
                      <div class="proc-meta-row">
                        <span
                          class="ci-provider-tag"
                          data-testid="process-qa-flow-badge"
                          >Workflow: {{ t.qaFlowRef }}</span
                        >
                      </div>
                    }
                    @if (t.declaredIn) {
                      <p
                        class="banner banner-info"
                        style="margin: 6px 0 0"
                        data-testid="tmpl-declared-banner"
                      >
                        Repo-declared read-only template from
                        <code>{{ t.declaredIn.repo }}</code> (<code>{{
                          t.declaredIn.path
                        }}</code
                        >). Launching uses
                        <code>POST /api/tasks/from-template</code>.
                      </p>
                    }
                  </div>

                  <div class="g-panes">
                    <section
                      class="g-pane"
                      aria-labelledby="tmpl-prompt-h"
                      data-testid="tmpl-prompt-pane"
                    >
                      <h3 id="tmpl-prompt-h" class="g-pane-title">Prompt</h3>
                      @if (t.description) {
                        <div
                          class="msg-body g-pane-prompt"
                          data-testid="tmpl-prompt-preview"
                          [gMarkdown]="t.description"
                        ></div>
                      } @else {
                        <p
                          class="g-pane-empty"
                          data-testid="tmpl-prompt-preview"
                        >
                          Empty prompt body.
                        </p>
                      }
                    </section>
                    <section
                      class="g-pane"
                      aria-labelledby="tmpl-config-h"
                      data-testid="tmpl-config-pane"
                    >
                      <h3 id="tmpl-config-h" class="g-pane-title">
                        Configuration
                      </h3>
                      <dl class="g-attrs">
                        <dt>Source</dt>
                        <dd>
                          <g-actor-chip
                            [label]="
                              t.declaredIn?.repo ||
                              (t.system ? 'grain' : 'deployment')
                            "
                            [kind]="t.system ? 'grain' : 'service'"
                          />
                        </dd>
                        <dt>Task title</dt>
                        <dd>{{ t.title }}</dd>
                        <dt>Repository</dt>
                        <dd class="tabular">
                          {{ t.repo || "Chosen at launch" }}
                        </dd>
                        @if (t.base) {
                          <dt>Base</dt>
                          <dd class="tabular">{{ t.base }}</dd>
                        }
                        @if (t.model) {
                          <dt>Model</dt>
                          <dd>{{ t.model }}</dd>
                        }
                        @if (t.priority) {
                          <dt>Priority</dt>
                          <dd class="tabular">{{ t.priority }}</dd>
                        }
                        @if (t.interactive) {
                          <dt>Interactive</dt>
                          <dd>on</dd>
                        }
                        <dt>Auto-merge</dt>
                        <dd>{{ t.autoMerge ? "on" : "off" }}</dd>
                      </dl>
                    </section>
                  </div>

                  @if ((t.fields ?? []).length > 0) {
                    <div class="proc-card" data-testid="tmpl-fields-schema">
                      <h3 class="section-title">
                        Parameter schema ({{ (t.fields ?? []).length }})
                      </h3>
                      <ul class="proc-kv-list">
                        @for (f of t.fields ?? []; track f.name) {
                          <li class="proc-kv-item">
                            <div>
                              <code class="tabular"
                                >{{ "{{" + f.name }}{{ "}}" }}</code
                              >
                              @if (f.label) {
                                <span> · {{ f.label }}</span>
                              }
                              @if (f.required) {
                                <span class="ci-provider-tag">required</span>
                              }
                            </div>
                            <span class="muted">
                              @if ((f.options ?? []).length > 0) {
                                options: {{ (f.options ?? []).join(", ") }}
                              } @else if (f.default) {
                                default: "{{ f.default }}"
                              } @else {
                                string
                              }
                            </span>
                          </li>
                        }
                      </ul>
                    </div>
                  }

                  <g-composition-view [composition]="templateComposition()" />
                } @else {
                  <div class="empty-state">
                    <p>
                      Select a template to inspect its parameter schema and
                      prompt.
                    </p>
                  </div>
                }
              }

              @case ("suites") {
                @if (selectedSuiteRun(); as sr) {
                  @let rBadge = runBadgeOf(sr.status);
                  <div class="proc-insp-head" data-testid="suite-run-inspector">
                    <div class="proc-insp-top">
                      <div class="headline-cluster">
                        <button
                          type="button"
                          class="btn-text proc-mobile-back"
                          (click)="closeMobileDetail()"
                        >
                          ← Back
                        </button>
                        <span [class]="'status-pill ' + rBadge.tone">
                          <g-status-icon
                            [tone]="rBadge.tone"
                            [kind]="rBadge.iconKind"
                          />
                          {{ rBadge.label }}
                        </span>
                        <h2 class="proc-insp-title">
                          {{ sr.suiteName || sr.suiteId }} · Run #{{ sr.id }}
                        </h2>
                      </div>

                      <div class="headline-actions">
                        <button
                          type="button"
                          class="btn-tonal"
                          (click)="selectSuite(sr.suiteId, true)"
                        >
                          Inspect suite definition
                        </button>
                        @if (sr.status === "active" && hasOpenSuiteTasks(sr)) {
                          <button
                            type="button"
                            class="btn-tonal danger"
                            data-testid="suite-run-cancel-btn"
                            [disabled]="
                              !access.can('POST /api/tasks/{id}/close', sr.repo)
                            "
                            (click)="confirmCancelSuiteRun(sr)"
                          >
                            Cancel suite run
                          </button>
                        }
                      </div>
                    </div>

                    <div class="proc-meta-row">
                      <span class="tabular"
                        >Target: <strong>{{ sr.repo }}</strong> &#64;
                        {{ sr.base || "default branch" }}</span
                      >
                      <span
                        >{{ suiteModeOf(sr.mode, sr.count, sr.maxPasses) }}
                        @if (sr.pass > 0) {
                          ·
                          {{
                            sr.status === "active" ? "currently on" : "ended on"
                          }}
                          pass {{ sr.pass }}
                        } @else {
                          ·
                          {{
                            sr.status === "active"
                              ? "no pass filed yet"
                              : "ended without filing a pass"
                          }}
                        }
                      </span>
                      @if (sr.scheduleId) {
                        <span>Started by schedule {{ sr.scheduleId }}</span>
                      }
                    </div>
                    @if (sr.error) {
                      <p class="banner banner-error" style="margin: 6px 0 0">
                        {{ sr.error }}
                      </p>
                    }
                  </div>

                  @for (p of passesForRun(sr); track p.pass) {
                    <div
                      class="proc-card"
                      [attr.data-testid]="'suite-pass-' + p.pass"
                    >
                      <h3 class="section-title">
                        Pass {{ p.pass }}
                        <span class="muted tabular"
                          >({{ countOf(p.items.length, "task") }})</span
                        >
                      </h3>
                      <ul class="proc-kv-list">
                        @for (item of p.items; track item.entry.taskId) {
                          <li class="proc-kv-item">
                            <div>
                              <a
                                [routerLink]="['/task', item.entry.taskId]"
                                [attr.data-testid]="
                                  'suite-task-link-' + item.entry.taskId
                                "
                                >#{{ item.entry.taskId }} · {{ item.title }}</a
                              >
                              @if (item.entry.openedPullRequest) {
                                <span class="ci-provider-tag">PR opened</span>
                              }
                              @for (ch of item.spawned; track ch.id) {
                                <div class="muted" style="margin-top: 4px">
                                  ↳ Spawned
                                  <a [routerLink]="['/task', ch.id]"
                                    >#{{ ch.id }} {{ ch.title }}</a
                                  >
                                  ({{ ch.state }})
                                </div>
                              }
                            </div>
                            <span class="ci-provider-tag">{{
                              item.liveState
                            }}</span>
                          </li>
                        }
                      </ul>
                    </div>
                  }
                } @else if (selectedSuite()) {
                  @let st = selectedSuite()!;
                  <div class="proc-insp-head" data-testid="suite-inspector">
                    <div class="proc-insp-top">
                      <div class="headline-cluster">
                        <button
                          type="button"
                          class="btn-text proc-mobile-back"
                          (click)="closeMobileDetail()"
                        >
                          ← Back
                        </button>
                        <span class="status-pill proposal">
                          <g-status-icon tone="proposal" kind="proposal" />
                          {{ suiteModeOf(st.mode, st.count, st.maxPasses) }}
                        </span>
                        <h2
                          class="proc-insp-title"
                          data-testid="suite-detail-title"
                        >
                          {{ st.name }}
                        </h2>
                      </div>

                      <div class="headline-actions">
                        <button
                          type="button"
                          class="btn-primary"
                          data-testid="suite-open-run-dialog-btn"
                          [disabled]="!access.can('POST /api/suite-runs')"
                          (click)="openRunSuiteDialog(st)"
                        >
                          Run suite
                        </button>
                        @if (!st.declaredIn) {
                          <button
                            type="button"
                            class="btn-tonal"
                            data-testid="suite-edit-btn"
                            (click)="openSuiteDialog(st)"
                          >
                            Edit
                          </button>
                          <button
                            type="button"
                            class="btn-text danger"
                            data-testid="suite-delete-btn"
                            [disabled]="!access.can('DELETE /api/suites/{id}')"
                            (click)="confirmDeleteSuite(st)"
                          >
                            Delete
                          </button>
                        }
                      </div>
                    </div>

                    <div class="proc-meta-row">
                      <g-actor-chip
                        [label]="st.declaredIn?.repo || 'deployment'"
                        kind="service"
                      />
                      <span
                        >Auto-merge:
                        <strong>{{ st.autoMerge ? "on" : "off" }}</strong></span
                      >
                      <span
                        >Approval:
                        <strong>{{
                          st.requireApproval
                            ? "each pass waits for approval"
                            : "auto-approved"
                        }}</strong></span
                      >
                      @if (st.qaFlowRef) {
                        <span
                          class="ci-provider-tag"
                          data-testid="process-qa-flow-badge"
                          >Workflow: {{ st.qaFlowRef }}</span
                        >
                      }
                    </div>
                    @if (st.declaredIn) {
                      <p class="banner banner-info" style="margin: 6px 0 0">
                        Declared in
                        <code>{{ st.declaredIn.repo }}</code> (<code>{{
                          st.declaredIn.path
                        }}</code
                        >).
                      </p>
                    }
                  </div>

                  <div class="proc-card">
                    <h3 class="section-title">
                      Member templates ({{ st.items.length }})
                    </h3>
                    <ul class="proc-kv-list">
                      @for (it of st.items; track $index) {
                        <li class="proc-kv-item">
                          <span
                            ><strong>{{ $index + 1 }}.</strong>
                            {{ it.templateName || it.templateId }}</span
                          >
                          <code class="tabular muted">{{ it.templateId }}</code>
                        </li>
                      }
                    </ul>
                  </div>

                  <g-composition-view [composition]="suiteComposition()" />

                  @if (runsOfSuite(st.id).length > 0) {
                    <div class="proc-card">
                      <h3 class="section-title">Recent runs of this suite</h3>
                      <ul class="proc-kv-list">
                        @for (r of runsOfSuite(st.id); track r.id) {
                          <li class="proc-kv-item">
                            <button
                              type="button"
                              class="btn-text"
                              (click)="selectSuiteRun(r.id, true)"
                            >
                              Run #{{ r.id }} · {{ r.repo }} &#64;
                              {{ r.base || "default branch"
                              }}{{ r.pass > 0 ? " (pass " + r.pass + ")" : "" }}
                            </button>
                            <span class="ci-provider-tag">{{ r.status }}</span>
                          </li>
                        }
                      </ul>
                    </div>
                  }
                } @else {
                  <div class="empty-state">
                    <p>Select a suite or suite run on the left.</p>
                  </div>
                }
              }

              @case ("declarations") {
                @if (selectedDeclRepo(); as repo) {
                  @let def = store.repoDefaults()[repo];
                  @let status = def?.declarationsStatus;
                  @let wfResp = store.repoWorkflows()[repo];
                  <div
                    class="proc-insp-head decl-inspector"
                    data-testid="decl-inspector"
                  >
                    <div class="proc-insp-top">
                      <div class="headline-cluster">
                        <button
                          type="button"
                          class="btn-text proc-mobile-back"
                          (click)="closeMobileDetail()"
                        >
                          ← Back
                        </button>
                        <h2 class="proc-insp-title tabular">{{ repo }}</h2>
                      </div>

                      <div class="headline-actions">
                        @if (def) {
                          <div class="switch-row">
                            <button
                              type="button"
                              role="switch"
                              class="g-switch"
                              attr.aria-label="Read {{
                                repo
                              }}'s own declarations"
                              data-testid="decl-toggle-switch"
                              [disabled]="
                                !access.can(
                                  'PUT /api/repos/{owner}/{name}/declarations',
                                  repo
                                )
                              "
                              [attr.aria-checked]="def.declarations"
                              (click)="
                                toggleRepoDeclarations(repo, !def.declarations)
                              "
                            ></button>
                            <span
                              >Read its own
                              <code>.grain/</code> declarations</span
                            >
                          </div>
                        }
                        <a
                          class="btn-tonal"
                          [routerLink]="repoAdvancedRoute(repo)"
                          data-testid="decl-open-repo-link"
                          >Open repository settings</a
                        >
                      </div>
                    </div>

                    <div class="proc-meta-row">
                      @if (status?.commit) {
                        <span class="tabular"
                          >Synced commit:
                          <strong>{{
                            status!.commit!.slice(0, 12)
                          }}</strong></span
                        >
                      }
                      @if (status?.checkedAt) {
                        <span class="tabular"
                          >Checked: {{ stamp(status!.checkedAt!) }}</span
                        >
                      }
                    </div>

                    @if (status?.error) {
                      <p
                        class="banner banner-error"
                        style="margin: 6px 0 0; white-space: pre-line"
                        data-testid="decl-sync-error"
                      >
                        Sync / validation error: {{ status!.error }}
                      </p>
                    }
                    @for (w of status?.warnings ?? []; track w) {
                      <p class="banner banner-info" style="margin: 6px 0 0">
                        Warning: {{ w }}
                      </p>
                    }
                  </div>

                  @if (acceptancesForRepo(repo).length > 0) {
                    <div class="proc-card" data-testid="decl-acceptances-card">
                      <h3 class="section-title">
                        Declaration capability acceptances
                      </h3>
                      @for (acc of acceptancesForRepo(repo); track acc.id) {
                        <div class="proc-kv-item" style="flex-wrap: wrap">
                          <div>
                            <strong>{{ acc.id }}</strong>
                            <span class="muted"> ({{ acc.kind }})</span>
                            <div class="muted" style="font-size: 12px">
                              Requested:
                              <code>{{ acc.requested.join(", ") }}</code>
                              @if (acc.needsScopes.length > 0) {
                                · Requires scopes:
                                {{ acc.needsScopes.join(", ") }}
                              }
                            </div>
                            @if (acc.acceptedBy) {
                              <div style="margin-top: 4px">
                                Accepted by
                                <g-actor-chip
                                  [label]="acc.acceptedBy"
                                  kind="human"
                                />
                              </div>
                            }
                          </div>
                          @if (acc.awaiting) {
                            <button
                              type="button"
                              class="btn-primary"
                              [attr.data-testid]="'decl-accept-btn-' + acc.id"
                              [disabled]="!canAcceptDeclaration(acc)"
                              [gTip]="acceptDeniedReason(acc)"
                              (click)="acceptRepoDeclaration(acc)"
                            >
                              Accept declaration
                            </button>
                          } @else {
                            <span class="status-pill healthy">
                              <g-status-icon tone="healthy" kind="healthy" />
                              Accepted
                            </span>
                          }
                        </div>
                      }
                    </div>
                  }

                  <div class="proc-card" data-testid="decl-discovered-files">
                    <h3 class="section-title">
                      Discovered <code>.grain/</code> files
                    </h3>
                    <ul class="proc-kv-list">
                      @for (item of discoveredFiles(status); track item.path) {
                        <li
                          class="proc-kv-item"
                          style="flex-direction: column; align-items: stretch"
                        >
                          <div class="proc-insp-top">
                            <div class="headline-cluster">
                              @if (item.kind === "trigger") {
                                <button
                                  type="button"
                                  class="btn-text tabular"
                                  [attr.data-testid]="
                                    'decl-trigger-link-' + item.path
                                  "
                                  (click)="toggleInlineYaml(repo, item.path)"
                                >
                                  <code>{{ item.path }}</code>
                                </button>
                              } @else {
                                <code class="tabular">{{ item.path }}</code>
                              }
                              <span class="ci-provider-tag">{{
                                item.kind
                              }}</span>
                            </div>
                            <button
                              type="button"
                              class="btn-text"
                              data-testid="decl-inline-yaml-toggle"
                              [attr.data-file-path]="item.path"
                              [attr.aria-expanded]="
                                isInlineYamlOpen(repo, item.path)
                              "
                              (click)="toggleInlineYaml(repo, item.path)"
                            >
                              {{
                                isInlineYamlOpen(repo, item.path)
                                  ? "Hide YAML"
                                  : "View YAML"
                              }}
                            </button>
                          </div>
                          @if (isInlineYamlOpen(repo, item.path)) {
                            <pre
                              class="comp-body-well s-mono"
                              data-testid="decl-inline-yaml"
                              [textContent]="inlineYamlDisplay()"
                            ></pre>
                          }
                        </li>
                      }
                      @if (discoveredFiles(status).length === 0) {
                        <li class="muted">
                          No <code>.grain/</code> declaration files synced on
                          the default branch yet.
                        </li>
                      }
                    </ul>
                  </div>

                  <div class="proc-card" data-testid="decl-workflows-card">
                    <div class="proc-insp-top">
                      <h3 class="section-title" style="margin: 0">
                        Deployment-declared workflows ({{
                          (wfResp?.workflows ?? []).length
                        }})
                      </h3>
                      @if (
                        access.can(
                          "PUT /api/repos/{owner}/{name}/workflows",
                          repo
                        )
                      ) {
                        <button
                          type="button"
                          class="btn-tonal"
                          data-testid="decl-add-workflow-btn"
                          (click)="openWorkflowEditor(repo, null)"
                        >
                          Add workflow YAML
                        </button>
                      }
                    </div>
                    @if ((wfResp?.workflows ?? []).length === 0) {
                      <p class="muted" style="margin: 0">
                        No controller-side workflow overrides stored for
                        {{ repo }}.
                      </p>
                    } @else {
                      <ul class="proc-kv-list">
                        @for (wf of wfResp!.workflows; track wf.path) {
                          <li
                            class="proc-kv-item"
                            style="flex-direction: column; align-items: stretch"
                          >
                            <div class="proc-insp-top">
                              <div>
                                <strong>{{ wf.name || wf.path }}</strong>
                                <span class="muted tabular">
                                  · {{ wf.path }}</span
                                >
                              </div>
                              <div class="headline-cluster">
                                <button
                                  type="button"
                                  class="btn-text"
                                  data-testid="decl-inline-yaml-toggle"
                                  [attr.data-file-path]="wf.path"
                                  [attr.aria-expanded]="
                                    isInlineYamlOpen(repo, wf.path)
                                  "
                                  (click)="
                                    toggleInlineYaml(repo, wf.path, wf.text)
                                  "
                                >
                                  {{
                                    isInlineYamlOpen(repo, wf.path)
                                      ? "Hide YAML"
                                      : "View YAML"
                                  }}
                                </button>
                                <button
                                  type="button"
                                  class="btn-text"
                                  [attr.data-testid]="
                                    'decl-inspect-wf-' + wf.path
                                  "
                                  (click)="openWorkflowEditor(repo, wf)"
                                >
                                  {{
                                    access.can(
                                      "PUT /api/repos/{owner}/{name}/workflows",
                                      repo
                                    )
                                      ? "Inspect / Edit"
                                      : "Inspect"
                                  }}
                                </button>
                              </div>
                            </div>
                            @if (isInlineYamlOpen(repo, wf.path)) {
                              <pre
                                class="comp-body-well s-mono"
                                data-testid="decl-inline-yaml"
                                [textContent]="inlineYamlDisplay()"
                              ></pre>
                            }
                          </li>
                        }
                      </ul>
                    }
                  </div>
                } @else {
                  <div class="empty-state">
                    <p>
                      Select a repository to inspect its <code>.grain/</code>
                      declarations, sync errors, and workflow overrides.
                    </p>
                  </div>
                }
              }
            }
          </section>
        </div>
      }
    </section>

    <!-- NEW / EDIT SCHEDULE DIALOG -->
    @if (schedDialogOpen()) {
      <div
        class="scrim"
        aria-hidden="true"
        (click)="dismissScheduleDialog()"
      ></div>
      <div
        class="g-dialog is-drawer"
        role="dialog"
        aria-modal="true"
        aria-labelledby="sched-dialog-title"
        data-testid="schedule-dialog"
        gDialogTrap
        (gDialogClose)="dismissScheduleDialog()"
      >
        <h2 id="sched-dialog-title">
          {{ editingSchedule() ? "Edit schedule" : "New schedule or trigger" }}
        </h2>

        @if (editingSchedule()?.system) {
          <p
            class="banner banner-info"
            style="margin: 8px 0 0"
            data-testid="sched-system-banner"
          >
            Built-in Grain schedule — target and prompt are managed by Grain;
            model, cadence and overlap policy can be customized.
          </p>
        } @else {
          <div class="field">
            <span>Firing target</span>
            <div
              class="segmented"
              role="group"
              aria-label="Schedule target kind"
            >
              <button
                type="button"
                data-testid="sched-target-prompt"
                [disabled]="!!editingSchedule()?.suiteId"
                [attr.aria-pressed]="schedTargetKind() === 'prompt'"
                (click)="setSchedTargetKind('prompt')"
              >
                Inline prompt
              </button>
              <button
                type="button"
                data-testid="sched-target-template"
                [disabled]="!!editingSchedule()?.suiteId"
                [attr.aria-pressed]="schedTargetKind() === 'template'"
                (click)="setSchedTargetKind('template')"
              >
                Template
              </button>
              <button
                type="button"
                data-testid="sched-target-suite"
                [disabled]="
                  editingSchedule() !== null && !editingSchedule()?.suiteId
                "
                [attr.aria-pressed]="schedTargetKind() === 'suite'"
                (click)="setSchedTargetKind('suite')"
              >
                Suite
              </button>
            </div>
          </div>

          @if (schedTargetKind() === "template") {
            <div class="field">
              <span>Template</span>
              <div class="pill-dropdown">
                <button
                  type="button"
                  class="pill-trigger"
                  data-testid="sched-template-picker"
                  aria-haspopup="listbox"
                  [attr.aria-expanded]="openPicker() === 'schedTmpl'"
                  (click)="togglePicker('schedTmpl', $event)"
                >
                  <span>{{
                    templateNameById(sDraftTemplateId()) || "Choose template…"
                  }}</span>
                  <span aria-hidden="true">▾</span>
                </button>
                @if (openPicker() === "schedTmpl") {
                  <ul class="pop-menu" role="listbox" aria-label="Templates">
                    @for (t of schedulableTemplates(); track t.id) {
                      <li role="none">
                        <button
                          type="button"
                          role="option"
                          [attr.aria-selected]="sDraftTemplateId() === t.id"
                          (click)="
                            sDraftTemplateId.set(t.id); openPicker.set(null)
                          "
                        >
                          {{ t.name || t.title }}
                        </button>
                      </li>
                    }
                  </ul>
                }
              </div>
            </div>
          } @else if (schedTargetKind() === "suite") {
            <div class="field">
              <span>Suite</span>
              <div class="pill-dropdown">
                <button
                  type="button"
                  class="pill-trigger"
                  data-testid="sched-suite-picker"
                  aria-haspopup="listbox"
                  [attr.aria-expanded]="openPicker() === 'schedSuite'"
                  (click)="togglePicker('schedSuite', $event)"
                >
                  <span>{{
                    suiteNameById(sDraftSuiteId()) || "Choose suite…"
                  }}</span>
                  <span aria-hidden="true">▾</span>
                </button>
                @if (openPicker() === "schedSuite") {
                  <ul class="pop-menu" role="listbox" aria-label="Suites">
                    @for (st of store.suites(); track st.id) {
                      <li role="none">
                        <button
                          type="button"
                          role="option"
                          [attr.aria-selected]="sDraftSuiteId() === st.id"
                          (click)="
                            sDraftSuiteId.set(st.id); openPicker.set(null)
                          "
                        >
                          {{ st.name }}
                        </button>
                      </li>
                    }
                  </ul>
                }
              </div>
            </div>
          } @else {
            <label class="field">
              <span>Task title</span>
              <input
                type="text"
                data-testid="sched-title-input"
                placeholder="Nightly dependency audit"
                [value]="sDraftTitle()"
                (input)="sDraftTitle.set($any($event.target).value)"
              />
            </label>
            <label class="field">
              <span>Task prompt</span>
              <textarea
                rows="8"
                data-testid="sched-desc-input"
                placeholder="Check outdated packages and open a PR if needed."
                [value]="sDraftDesc()"
                (input)="sDraftDesc.set($any($event.target).value)"
              ></textarea>
            </label>
          }

          @if (schedBoundTemplate(); as bound) {
            <div
              class="proc-field-grid proc-bound-target"
              data-testid="sched-bound-target"
            >
              <label class="field">
                <span>Repository (owner/name)</span>
                <input
                  type="text"
                  class="proc-readonly-input tabular"
                  readonly
                  aria-describedby="sched-bound-target-note"
                  data-testid="sched-repo-input"
                  [value]="bound.repo"
                />
              </label>
              <label class="field">
                <span>Base branch</span>
                <input
                  type="text"
                  class="proc-readonly-input tabular"
                  readonly
                  aria-describedby="sched-bound-target-note"
                  data-testid="sched-base-input"
                  [value]="bound.base || 'Default branch'"
                />
              </label>
            </div>
            <p
              id="sched-bound-target-note"
              class="muted proc-field-help proc-bound-target-note"
              data-testid="sched-bound-target-note"
            >
              Set by template
              <strong>{{ bound.name || bound.title || bound.id }}</strong> —
              repository and base branch come from its binding.
            </p>
          } @else {
            <div class="proc-field-grid">
              <div class="field">
                <span>Repository (owner/name)</span>
                @if (store.knownRepos().length > 0) {
                  <g-pill-select
                    [options]="schedRepoOptions()"
                    [value]="
                      store.knownRepos().includes(sDraftRepo())
                        ? sDraftRepo()
                        : ''
                    "
                    ariaLabel="Schedule repository"
                    align="left"
                    testid="sched-repo-select"
                    (valueChange)="$event && sDraftRepo.set($event)"
                  />
                }
                <input
                  type="text"
                  aria-label="Repository (owner/name)"
                  data-testid="sched-repo-input"
                  placeholder="acme/widgets"
                  [value]="sDraftRepo()"
                  (input)="sDraftRepo.set($any($event.target).value)"
                />
              </div>
              <label class="field">
                <span>Base branch</span>
                <input
                  type="text"
                  data-testid="sched-base-input"
                  placeholder="Default branch"
                  [value]="sDraftBase()"
                  (input)="sDraftBase.set($any($event.target).value)"
                />
              </label>
            </div>
          }

          @if (schedTargetKind() === "prompt") {
            <div class="switch-row" style="margin-top: 14px">
              <button
                type="button"
                role="switch"
                class="g-switch"
                aria-label="Keep running"
                data-testid="sched-keep-running"
                [attr.aria-checked]="sDraftKeepRunning()"
                (click)="sDraftKeepRunning.set(!sDraftKeepRunning())"
              ></button>
              <span
                >Keep running
                <span class="muted"
                  >— restart every task this schedule files when its run exits,
                  instead of completing or parking it</span
                ></span
              >
            </div>
            <div class="proc-field-grid">
              <div class="field">
                <span>Admission</span>
                <g-pill-select
                  [options]="admissionOptions"
                  [value]="sDraftAdmission()"
                  ariaLabel="Admission"
                  align="left"
                  testid="sched-admission"
                  (valueChange)="sDraftAdmission.set($event)"
                />
                <p
                  class="muted proc-field-help"
                  data-testid="sched-admission-help"
                >
                  {{ admissionHelp(sDraftAdmission()) }} Applies to every task
                  this schedule files.
                </p>
              </div>
              <div class="field">
                <span>Hosting</span>
                <g-pill-select
                  [options]="hostingOptions"
                  [value]="sDraftHosting()"
                  ariaLabel="Hosting"
                  align="left"
                  testid="sched-hosting"
                  (valueChange)="sDraftHosting.set($event)"
                />
                <p
                  class="muted proc-field-help"
                  data-testid="sched-hosting-help"
                >
                  {{ hostingHelp(sDraftHosting()) }} Applies to every task this
                  schedule files.
                </p>
              </div>
            </div>
            <div class="field">
              <span>Grants</span>
              <g-grants-picker
                testid="sched-grants"
                [offered]="offeredCapabilities()"
                [held]="sDraftCaps()"
                [repo]="sDraftRepo().trim() || undefined"
                [policyVerdicts]="true"
                (heldChange)="sDraftCaps.set($event)"
              />
            </div>
            <div class="field">
              <span>Network access</span>
              <g-pill-select
                [options]="networkAccessOptions"
                [value]="sDraftNetwork()"
                ariaLabel="Network access"
                align="left"
                testid="sched-network"
                (valueChange)="sDraftNetwork.set($event)"
              />
              <p class="muted proc-field-help" data-testid="sched-network-help">
                Fixes the network of every task this schedule files. A task with
                no network still reaches this deployment's git proxy.
              </p>
            </div>
            <div class="field">
              <span>Reads (extra read-only repositories)</span>
              @if (sDraftReads().length > 0) {
                <ul
                  class="proc-chip-list"
                  aria-label="Read-only repositories"
                  data-testid="sched-reads-chips"
                >
                  @for (r of sDraftReads(); track r) {
                    <li class="proc-chip">
                      <code class="proc-chip-label tabular">{{ r }}</code>
                      <button
                        type="button"
                        class="proc-chip-remove"
                        [attr.aria-label]="'Remove read repository ' + r"
                        [attr.data-testid]="'sched-remove-read-' + r"
                        (click)="removeSchedRead(r)"
                      >
                        ×
                      </button>
                    </li>
                  }
                </ul>
              }
              <div class="proc-inline-row">
                @if (schedReadRepoOptions().length > 1) {
                  <g-pill-select
                    [options]="schedReadRepoOptions()"
                    value=""
                    placeholder="Add read-only repository…"
                    ariaLabel="Add read-only repository"
                    align="left"
                    testid="sched-reads-select"
                    (valueChange)="addSchedRead($event)"
                  />
                }
                <input
                  type="text"
                  aria-label="Read-only repository (owner/name)"
                  data-testid="sched-reads-input"
                  placeholder="owner/name"
                  [value]="sDraftReadInput()"
                  (input)="sDraftReadInput.set($any($event.target).value)"
                  (keydown.enter)="
                    $event.preventDefault(); addSchedReadFromInput()
                  "
                />
                <button
                  type="button"
                  class="btn-tonal"
                  data-testid="sched-reads-add"
                  [disabled]="!sDraftReadInput().trim()"
                  (click)="addSchedReadFromInput()"
                >
                  Add
                </button>
              </div>
            </div>
            <div class="field">
              <span>Writes (additional write targets)</span>
              @if (sDraftWrites().length > 0) {
                <ul
                  class="proc-chip-list"
                  aria-label="Additional write targets"
                  data-testid="sched-writes-chips"
                >
                  @for (
                    w of sDraftWrites();
                    track w.repo + ":" + (w.base || "")
                  ) {
                    <li class="proc-chip">
                      <code class="proc-chip-label tabular"
                        >{{ w.repo }}{{ w.base ? " @ " + w.base : "" }}</code
                      >
                      <button
                        type="button"
                        class="proc-chip-remove"
                        [attr.aria-label]="
                          'Remove write target ' +
                          w.repo +
                          (w.base ? ' @ ' + w.base : '')
                        "
                        [attr.data-testid]="'sched-remove-write-' + $index"
                        (click)="removeSchedWrite($index)"
                      >
                        ×
                      </button>
                    </li>
                  }
                </ul>
              }
              <div class="proc-inline-row">
                @if (schedWriteRepoOptions().length > 1) {
                  <g-pill-select
                    [options]="schedWriteRepoOptions()"
                    [value]="schedSelectedWriteRepo()"
                    placeholder="Select repo…"
                    ariaLabel="Additional write repository"
                    align="left"
                    testid="sched-writes-repo-select"
                    (valueChange)="$event && sDraftWriteRepo.set($event)"
                  />
                }
                <input
                  type="text"
                  aria-label="Additional write repository (owner/name)"
                  data-testid="sched-writes-repo-input"
                  placeholder="owner/name"
                  [value]="sDraftWriteRepo()"
                  (input)="sDraftWriteRepo.set($any($event.target).value)"
                  (keydown.enter)="$event.preventDefault(); addSchedWrite()"
                />
                <input
                  type="text"
                  aria-label="Additional write base branch"
                  data-testid="sched-writes-base-input"
                  placeholder="Default branch"
                  [value]="sDraftWriteBase()"
                  (input)="sDraftWriteBase.set($any($event.target).value)"
                  (keydown.enter)="$event.preventDefault(); addSchedWrite()"
                />
                <button
                  type="button"
                  class="btn-tonal"
                  data-testid="sched-writes-add"
                  [disabled]="!sDraftWriteRepo().trim()"
                  (click)="addSchedWrite()"
                >
                  Add
                </button>
              </div>
            </div>
            @if (declaredCustomFields().length > 0) {
              <div
                class="proc-field-grid"
                style="margin-top: 4px"
                data-testid="sched-custom-fields"
              >
                @for (cf of declaredCustomFields(); track cf.name) {
                  <div class="field">
                    <span>{{ cf.label || cf.name }}</span>
                    @if (cf.multiple) {
                      @if (schedCustomFieldList(cf.name).length > 0) {
                        <ul
                          class="proc-chip-list"
                          [attr.aria-label]="cf.label || cf.name"
                          [attr.data-testid]="'sched-cf-chips-' + cf.name"
                        >
                          @for (v of schedCustomFieldList(cf.name); track v) {
                            <li class="proc-chip">
                              <span class="proc-chip-label">{{ v }}</span>
                              <button
                                type="button"
                                class="proc-chip-remove"
                                [attr.aria-label]="
                                  'Remove ' + (cf.label || cf.name) + ' ' + v
                                "
                                (click)="removeSchedCustomFieldItem(cf.name, v)"
                              >
                                ×
                              </button>
                            </li>
                          }
                        </ul>
                      }
                      @if (
                        cf.type === "enum" || (cf.options ?? []).length > 0
                      ) {
                        <g-pill-select
                          [options]="customMultiEnumOptions(cf)"
                          value=""
                          placeholder="Add value…"
                          [ariaLabel]="cf.label || cf.name"
                          align="left"
                          [testid]="'sched-cf-' + cf.name"
                          (valueChange)="
                            addSchedCustomFieldItem(cf.name, $event)
                          "
                        />
                      } @else {
                        <div class="proc-inline-row">
                          <input
                            [type]="
                              cf.type === 'number'
                                ? 'number'
                                : cf.type === 'date'
                                  ? 'date'
                                  : 'text'
                            "
                            [attr.aria-label]="cf.label || cf.name"
                            [attr.data-testid]="'sched-cf-input-' + cf.name"
                            [value]="sDraftCustomInput()[cf.name] || ''"
                            (input)="
                              setSchedCustomInput(
                                cf.name,
                                $any($event.target).value
                              )
                            "
                            (keydown.enter)="
                              $event.preventDefault();
                              commitSchedCustomInput(cf.name)
                            "
                          />
                          <button
                            type="button"
                            class="btn-tonal"
                            [attr.data-testid]="'sched-cf-add-' + cf.name"
                            [disabled]="
                              !(sDraftCustomInput()[cf.name] || '').trim()
                            "
                            (click)="commitSchedCustomInput(cf.name)"
                          >
                            Add
                          </button>
                        </div>
                      }
                    } @else if (
                      cf.type === "boolean" || $any(cf.type) === "bool"
                    ) {
                      <g-pill-select
                        [options]="customBooleanOptions"
                        [value]="schedCustomFieldScalar(cf.name)"
                        [ariaLabel]="cf.label || cf.name"
                        align="left"
                        [testid]="'sched-cf-' + cf.name"
                        (valueChange)="setSchedCustomField(cf.name, $event)"
                      />
                    } @else if (
                      cf.type === "enum" || (cf.options ?? []).length > 0
                    ) {
                      <g-pill-select
                        [options]="customEnumOptions(cf)"
                        [value]="schedCustomFieldScalar(cf.name)"
                        [ariaLabel]="cf.label || cf.name"
                        align="left"
                        [testid]="'sched-cf-' + cf.name"
                        (valueChange)="setSchedCustomField(cf.name, $event)"
                      />
                    } @else {
                      <input
                        [type]="
                          cf.type === 'number'
                            ? 'number'
                            : cf.type === 'date'
                              ? 'date'
                              : 'text'
                        "
                        [attr.aria-label]="cf.label || cf.name"
                        [attr.data-testid]="'sched-cf-' + cf.name"
                        [value]="schedCustomFieldScalar(cf.name)"
                        (input)="
                          setSchedCustomField(
                            cf.name,
                            $any($event.target).value
                          )
                        "
                      />
                    }
                    @if (cf.description) {
                      <span class="muted proc-field-help">{{
                        cf.description
                      }}</span>
                    }
                  </div>
                }
              </div>
            }
          } @else if (schedTargetKind() === "template") {
            <p
              class="muted proc-field-help proc-template-note"
              data-testid="sched-template-note"
            >
              Admission, hosting, grants, reads, writes, network, custom fields
              and keep running come from the selected template, and stay in sync
              with it.
            </p>
          }
        }

        @if (schedTargetKind() !== "suite") {
          <div class="field">
            <span>Model</span>
            <div class="proc-model-row">
              <g-model-picker
                [groups]="modelGroups()"
                [value]="sDraftModel()"
                [placeholder]="schedModelPlaceholder()"
                ariaLabel="Model"
                testid="sched-model"
                (valueChange)="sDraftModel.set($event)"
              />
              @if (sDraftModel()) {
                <button
                  type="button"
                  class="btn-text"
                  data-testid="sched-model-clear"
                  (click)="sDraftModel.set('')"
                >
                  Use default
                </button>
              }
            </div>
            <p class="muted proc-field-help">
              The model every task this schedule files asks for.
            </p>
          </div>
        }

        <div class="field">
          <span>Cadence / Event trigger</span>
          <div class="filter-strip" style="margin-top: 4px">
            @for (ck of cadenceKinds; track ck.id) {
              <button
                type="button"
                class="filter-chip"
                [attr.aria-pressed]="sDraftKind() === ck.id"
                [attr.data-testid]="'sched-kind-' + ck.id"
                (click)="sDraftKind.set(ck.id)"
              >
                {{ ck.label }}
              </button>
            }
          </div>
        </div>

        <div class="proc-field-grid">
          @if (sDraftKind() === "everyNMinutes") {
            <label class="field">
              <span>Every N minutes</span>
              <input
                type="number"
                min="10"
                [value]="sDraftEveryMin()"
                (input)="sDraftEveryMin.set(+$any($event.target).value)"
              />
            </label>
          } @else if (sDraftKind() === "everyNHours") {
            <label class="field">
              <span>Every N hours</span>
              <input
                type="number"
                min="1"
                [value]="sDraftEveryHr()"
                (input)="sDraftEveryHr.set(+$any($event.target).value || 1)"
              />
            </label>
          } @else if (
            sDraftKind() === "daily" ||
            sDraftKind() === "weekly" ||
            sDraftKind() === "monthly"
          ) {
            <label class="field">
              <span>Time of day (HH:MM)</span>
              <input
                type="text"
                placeholder="09:00"
                [value]="sDraftTime()"
                (input)="sDraftTime.set($any($event.target).value)"
              />
            </label>
            <label class="field">
              <span>Time zone (IANA)</span>
              <input
                type="text"
                placeholder="UTC"
                [value]="sDraftTz()"
                (input)="sDraftTz.set($any($event.target).value)"
              />
            </label>
            @if (sDraftKind() === "weekly") {
              <div class="field">
                <span>Weekday</span>
                <div class="pill-dropdown">
                  <button
                    type="button"
                    class="pill-trigger"
                    aria-haspopup="listbox"
                    [attr.aria-expanded]="openPicker() === 'weekday'"
                    (click)="togglePicker('weekday', $event)"
                  >
                    <span>{{ sDraftWeekday() }}</span>
                    <span aria-hidden="true">▾</span>
                  </button>
                  @if (openPicker() === "weekday") {
                    <ul class="pop-menu" role="listbox" aria-label="Weekday">
                      @for (wd of weekdays; track wd) {
                        <li role="none">
                          <button
                            type="button"
                            role="option"
                            [attr.aria-selected]="sDraftWeekday() === wd"
                            (click)="
                              sDraftWeekday.set(wd); openPicker.set(null)
                            "
                          >
                            {{ wd }}
                          </button>
                        </li>
                      }
                    </ul>
                  }
                </div>
              </div>
            }
            @if (sDraftKind() === "monthly") {
              <label class="field">
                <span>Day of month (1–28)</span>
                <input
                  type="number"
                  min="1"
                  max="28"
                  [value]="sDraftDayOfMonth()"
                  (input)="
                    sDraftDayOfMonth.set(+$any($event.target).value || 1)
                  "
                />
              </label>
            }
          } @else if (sDraftKind() === "github_workflow_run") {
            <label class="field">
              <span>Workflow name or file</span>
              <input
                type="text"
                data-testid="sched-trigger-workflow-input"
                placeholder="CI"
                [value]="sDraftTriggerWorkflow()"
                (input)="sDraftTriggerWorkflow.set($any($event.target).value)"
              />
            </label>
            <label class="field">
              <span>Conclusion filter</span>
              <input
                type="text"
                data-testid="sched-trigger-conclusion-input"
                placeholder="failure"
                [value]="sDraftTriggerConclusion()"
                (input)="sDraftTriggerConclusion.set($any($event.target).value)"
              />
            </label>
            <label class="field">
              <span>Branch filter (optional)</span>
              <input
                type="text"
                data-testid="sched-trigger-branch-input"
                placeholder="main"
                [value]="sDraftTriggerBranch()"
                (input)="sDraftTriggerBranch.set($any($event.target).value)"
              />
            </label>
          } @else if (sDraftKind() === "git_ref") {
            <label class="field">
              <span>Watched git repository</span>
              <input
                type="text"
                data-testid="sched-git-ref-repo-input"
                [placeholder]="schedEffectiveRepo() || 'acme/widgets'"
                [value]="sDraftGitRefRepo()"
                (input)="sDraftGitRefRepo.set($any($event.target).value)"
              />
            </label>
            <label class="field">
              <span>Watched git ref / branch</span>
              <input
                type="text"
                data-testid="sched-git-ref-input"
                placeholder="refs/heads/main"
                [value]="sDraftGitRef()"
                (input)="sDraftGitRef.set($any($event.target).value)"
              />
            </label>
          }
        </div>

        <div class="field">
          <span>Overlap policy</span>
          <div class="segmented" role="group" aria-label="Overlap policy">
            <button
              type="button"
              [attr.aria-pressed]="sDraftOverlap() === 'skip'"
              (click)="sDraftOverlap.set('skip')"
            >
              Skip while open
            </button>
            <button
              type="button"
              [attr.aria-pressed]="sDraftOverlap() === 'allow'"
              (click)="sDraftOverlap.set('allow')"
            >
              Allow overlap
            </button>
          </div>
        </div>

        @if (!editingSchedule()?.system && !editingSchedule()?.declaredIn) {
          <div class="field">
            <span>Software workflow</span>
            <g-pill-select
              [options]="qaFlowOptions()"
              [value]="sDraftQaFlowRef()"
              ariaLabel="Software workflow"
              align="left"
              testid="process-qa-flow-select"
              (valueChange)="sDraftQaFlowRef.set($event)"
            />
          </div>
          <div style="margin-top: 12px">
            <button
              type="button"
              class="btn-tonal"
              data-testid="sched-preview-composition-btn"
              (click)="previewScheduleDraft()"
            >
              Preview composition
            </button>
          </div>
          @if (draftSchedComposition(); as dc) {
            <div style="margin-top: 10px">
              <g-composition-view
                [composition]="dc"
                heading="Draft composition preview"
              />
            </div>
          }
        }

        <!-- Beside the actions, so a refused save is in view where the
             Save was pressed, on a phone as on a desktop. -->
        @if (dialogError(); as derr) {
          <p
            class="banner banner-error dialog-error"
            role="alert"
            data-testid="proc-dialog-error"
          >
            {{ derr }}
          </p>
        }
        <div class="dialog-actions">
          <button
            type="button"
            class="btn-text"
            (click)="dismissScheduleDialog()"
          >
            Cancel
          </button>
          <button
            type="button"
            class="btn-primary"
            data-testid="sched-save-btn"
            [disabled]="submitting() || !canSaveSchedule()"
            (click)="saveScheduleDialog()"
          >
            {{ submitting() ? "Saving…" : "Save schedule" }}
          </button>
        </div>
      </div>
    }

    <!-- LAUNCH TASK FROM TEMPLATE DIALOG -->
    @if (launchDialogOpen()) {
      <div class="scrim" aria-hidden="true" (click)="closeLaunchDialog()"></div>
      <div
        class="g-dialog is-drawer"
        role="dialog"
        aria-modal="true"
        aria-labelledby="launch-tmpl-title"
        data-testid="launch-template-dialog"
        gDialogTrap
        (gDialogClose)="closeLaunchDialog()"
      >
        <h2 id="launch-tmpl-title">Launch task from template</h2>

        <div class="field">
          <span>Template</span>
          <div class="pill-dropdown">
            <button
              type="button"
              class="pill-trigger"
              data-testid="launch-template-picker"
              aria-haspopup="listbox"
              [attr.aria-expanded]="openPicker() === 'launchTmpl'"
              (click)="togglePicker('launchTmpl', $event)"
            >
              <span>{{ launchTemplateLabel() }}</span>
              <span aria-hidden="true">▾</span>
            </button>
            @if (openPicker() === "launchTmpl") {
              <ul class="pop-menu" role="listbox" aria-label="Template">
                @for (t of store.templates(); track t.id) {
                  <li role="none">
                    <button
                      type="button"
                      role="option"
                      [attr.aria-selected]="
                        launchSelectedTemplate()?.id === t.id
                      "
                      (click)="pickLaunchTemplate(t.id)"
                    >
                      {{ t.name || t.title }}
                    </button>
                  </li>
                }
              </ul>
            }
          </div>
        </div>

        @if (launchSelectedTemplate(); as lt) {
          @for (f of lt.fields ?? []; track f.name) {
            @if ((f.options ?? []).length > 0) {
              <div class="field">
                <span>{{ f.label || f.name }}</span>
                <div class="filter-strip" style="margin-top: 4px">
                  @for (opt of f.options ?? []; track opt) {
                    <button
                      type="button"
                      class="filter-chip"
                      [attr.aria-pressed]="launchFields()[f.name] === opt"
                      (click)="setLaunchField(f.name, opt)"
                    >
                      {{ opt }}
                    </button>
                  }
                </div>
              </div>
            } @else {
              <label class="field">
                <span
                  >{{ f.label || f.name
                  }}{{ f.required ? " (required)" : "" }}</span
                >
                <input
                  type="text"
                  [attr.data-testid]="'launch-field-' + f.name"
                  [value]="launchFields()[f.name] || ''"
                  (input)="setLaunchField(f.name, $any($event.target).value)"
                />
              </label>
            }
          }

          @if (lt.repo) {
            <p class="muted proc-field-help" style="margin-top: 12px">
              Files against <code>{{ lt.repo }}</code
              >{{ lt.base ? " @ " + lt.base : "" }} — this template is bound to
              it.
            </p>
          } @else if (lt.system) {
            <p class="muted proc-field-help" style="margin-top: 12px">
              Files with no repo — this built-in template works on this
              deployment itself, not on a repository.
            </p>
          } @else if (!lt.declaredIn) {
            <div class="proc-field-grid">
              <label class="field">
                <span>Target repository (owner/name)</span>
                <input
                  type="text"
                  data-testid="launch-repo-input"
                  placeholder="acme/widgets"
                  [value]="launchRepo()"
                  (input)="launchRepo.set($any($event.target).value)"
                />
              </label>
              <label class="field">
                <span>Base branch</span>
                <input
                  type="text"
                  data-testid="launch-base-input"
                  placeholder="main"
                  [value]="launchBase()"
                  (input)="launchBase.set($any($event.target).value)"
                />
              </label>
            </div>
          }

          @if (lt.interactive) {
            <p
              class="muted proc-field-help"
              style="margin-top: 14px"
              data-testid="launch-interactive-note"
            >
              This template files a live chat: the agent can talk with you while
              it works, and the chat opens as soon as the task is filed.
            </p>
          } @else {
            <div class="switch-row" style="margin-top: 14px">
              <button
                type="button"
                role="switch"
                class="g-switch"
                aria-label="Approved (queue immediately without proposal hold)"
                [attr.aria-checked]="launchApproved()"
                (click)="launchApproved.set(!launchApproved())"
              ></button>
              <span>Approved (queue immediately without proposal hold)</span>
            </div>
          }
        }

        <!-- Beside the actions, so a refused save is in view where the
             Save was pressed, on a phone as on a desktop. -->
        @if (dialogError(); as derr) {
          <p
            class="banner banner-error dialog-error"
            role="alert"
            data-testid="proc-dialog-error"
          >
            {{ derr }}
          </p>
        }
        <div class="dialog-actions">
          <button type="button" class="btn-text" (click)="closeLaunchDialog()">
            Cancel
          </button>
          <button
            type="button"
            class="btn-primary"
            data-testid="launch-template-submit"
            [disabled]="submitting() || !canSubmitLaunchTemplate()"
            (click)="submitLaunchTemplate()"
          >
            {{ submitting() ? "Launching…" : "Create task" }}
          </button>
        </div>
      </div>
    }

    <!-- CREATE / EDIT SUITE DIALOG -->
    @if (suiteDialogOpen()) {
      <div
        class="scrim"
        aria-hidden="true"
        (click)="dismissSuiteDialog()"
      ></div>
      <div
        class="g-dialog is-drawer"
        role="dialog"
        aria-modal="true"
        aria-labelledby="suite-dialog-title"
        data-testid="suite-dialog"
        [attr.inert]="tmplDialogOpen() ? '' : null"
        gDialogTrap
        (gDialogClose)="dismissSuiteDialog()"
      >
        <h2 id="suite-dialog-title">
          {{ editingSuite() ? "Edit suite" : "New suite" }}
        </h2>
        <label class="field">
          <span>Suite name</span>
          <input
            type="text"
            data-testid="suite-name-input"
            placeholder="Release hardening sweep"
            [value]="stDraftName()"
            (input)="stDraftName.set($any($event.target).value)"
          />
        </label>

        <div class="field">
          <span>Stopping mode</span>
          <div class="segmented" role="group" aria-label="Suite mode">
            <button
              type="button"
              data-testid="suite-mode-count"
              [attr.aria-pressed]="stDraftMode() === 'count'"
              (click)="stDraftMode.set('count')"
            >
              Fixed pass count
            </button>
            <button
              type="button"
              data-testid="suite-mode-until-clean"
              [attr.aria-pressed]="stDraftMode() === 'until_clean'"
              (click)="stDraftMode.set('until_clean')"
            >
              Until clean
            </button>
          </div>
        </div>

        @if (stDraftMode() === "count") {
          <label class="field">
            <span>Number of passes</span>
            <input
              type="number"
              min="1"
              [attr.max]="maxSuitePasses"
              step="1"
              data-testid="suite-count-input"
              [value]="stDraftCount()"
              (input)="stDraftCount.set(+$any($event.target).value)"
            />
          </label>
        } @else {
          <label class="field">
            <span>Maximum passes</span>
            <input
              type="number"
              min="1"
              [attr.max]="maxSuitePasses"
              step="1"
              data-testid="suite-max-passes-input"
              [value]="stDraftMaxPasses()"
              (input)="stDraftMaxPasses.set(+$any($event.target).value)"
            />
          </label>
        }

        <div class="field">
          <div class="suite-templates-head">
            <span>Templates</span>
            <button
              type="button"
              class="btn-text suite-new-template-btn"
              data-testid="suite-new-template-btn"
              [disabled]="!access.can('POST /api/templates')"
              [gTip]="
                access.can('POST /api/templates')
                  ? 'Create a new template and add it to this suite.'
                  : access.deniedReason('POST /api/templates')
              "
              (click)="openInlineSuiteTemplateDialog()"
            >
              + New template
            </button>
          </div>
          @if (stDraftTemplateIds().length > 0) {
            <ul
              class="proc-chip-list"
              aria-label="Selected suite templates"
              data-testid="suite-selected-templates"
            >
              @for (tid of stDraftTemplateIds(); track tid) {
                <li
                  class="proc-chip"
                  [attr.data-testid]="'suite-selected-tmpl-' + tid"
                >
                  <span class="proc-chip-label"
                    >{{ $index + 1 }}. {{ suiteTemplateLabel(tid) }}</span
                  >
                  <button
                    type="button"
                    class="proc-chip-remove"
                    [attr.aria-label]="
                      'Remove template ' + suiteTemplateLabel(tid)
                    "
                    [attr.data-testid]="'suite-remove-tmpl-' + tid"
                    (click)="removeSuiteTemplateId(tid)"
                  >
                    ×
                  </button>
                </li>
              }
            </ul>
          }
          <div class="suite-tmpl-picker">
            <div class="suite-tmpl-combo-wrap">
              <input
                type="text"
                role="combobox"
                aria-autocomplete="list"
                aria-label="Templates"
                aria-controls="suite-templates-listbox"
                data-testid="suite-templates-combobox"
                [attr.aria-expanded]="suiteTmplOpen()"
                [attr.aria-activedescendant]="
                  suiteTmplOpen() && suiteActiveOptionId()
                    ? suiteActiveOptionId()
                    : null
                "
                [placeholder]="
                  stDraftTemplateIds().length > 0
                    ? stDraftTemplateIds().length +
                      ' selected — search templates…'
                    : 'Search templates…'
                "
                [value]="suiteTmplQuery()"
                (focus)="openSuiteTmplDropdown()"
                (click)="openSuiteTmplDropdown()"
                (input)="onSuiteTmplQueryInput($any($event.target).value)"
                (keydown)="onSuiteTmplKeydown($event)"
              />
              <button
                type="button"
                class="suite-tmpl-caret"
                tabindex="-1"
                aria-hidden="true"
                (mousedown)="$event.preventDefault()"
                (click)="toggleSuiteTmplDropdown($event)"
              >
                ▾
              </button>
            </div>
            @if (suiteTmplOpen()) {
              <ul
                id="suite-templates-listbox"
                class="suite-tmpl-listbox"
                role="listbox"
                aria-label="Templates"
                aria-multiselectable="true"
                data-testid="suite-templates-listbox"
              >
                @if (filteredSuiteTemplates().length === 0) {
                  <li
                    class="suite-tmpl-empty"
                    role="presentation"
                    data-testid="suite-templates-empty"
                  >
                    No templates match "{{ suiteTmplQuery() }}".
                  </li>
                } @else {
                  @for (
                    opt of filteredSuiteTemplates();
                    track opt.template.id
                  ) {
                    @let picked =
                      stDraftTemplateIds().includes(opt.template.id);
                    <li
                      role="option"
                      tabindex="-1"
                      class="suite-tmpl-opt"
                      [id]="'suite-tmpl-opt-' + opt.template.id"
                      [class.is-selected]="picked"
                      [class.is-active]="suiteTmplActiveIndex() === $index"
                      [attr.aria-selected]="picked"
                      [attr.data-testid]="'suite-tmpl-opt-' + opt.template.id"
                      (mousedown)="$event.preventDefault()"
                      (mouseenter)="suiteTmplActiveIndex.set($index)"
                      (click)="toggleSuiteTemplateId(opt.template.id)"
                      (keydown.enter)="toggleSuiteTemplateId(opt.template.id)"
                      (keydown.space)="
                        $event.preventDefault();
                        toggleSuiteTemplateId(opt.template.id)
                      "
                    >
                      <span class="suite-tmpl-check" aria-hidden="true">{{
                        picked ? "✓" : ""
                      }}</span>
                      <span class="suite-tmpl-opt-copy">
                        <span class="suite-tmpl-opt-label">{{
                          opt.label
                        }}</span>
                        @if (opt.sub) {
                          <span class="suite-tmpl-opt-sub">{{ opt.sub }}</span>
                        }
                      </span>
                    </li>
                  }
                }
              </ul>
            }
          </div>
        </div>

        @if (!editingSuite()?.declaredIn) {
          <div class="field">
            <span>Software workflow</span>
            <g-pill-select
              [options]="qaFlowOptions()"
              [value]="stDraftQaFlowRef()"
              ariaLabel="Software workflow"
              align="left"
              testid="process-qa-flow-select"
              (valueChange)="stDraftQaFlowRef.set($event)"
            />
          </div>
        }

        <div class="proc-field-grid" style="margin-top: 14px">
          <div class="switch-row">
            <button
              type="button"
              role="switch"
              class="g-switch"
              aria-label="Require approval before each pass"
              data-testid="suite-require-approval-switch"
              [attr.aria-checked]="stDraftRequireApproval()"
              (click)="stDraftRequireApproval.set(!stDraftRequireApproval())"
            ></button>
            <span>Require approval before each pass</span>
          </div>
          <div class="switch-row">
            <button
              type="button"
              role="switch"
              class="g-switch"
              aria-label="Auto-merge passing pull requests"
              data-testid="suite-auto-merge-switch"
              [attr.aria-checked]="stDraftAutoMerge()"
              (click)="stDraftAutoMerge.set(!stDraftAutoMerge())"
            ></button>
            <span>Auto-merge passing pull requests</span>
          </div>
        </div>

        <div style="margin-top: 12px">
          <button
            type="button"
            class="btn-tonal"
            data-testid="suite-preview-composition-btn"
            (click)="previewSuiteDraft()"
          >
            Preview composition
          </button>
        </div>
        @if (draftSuiteComposition(); as dsc) {
          <div style="margin-top: 10px">
            <g-composition-view
              [composition]="dsc"
              heading="Draft suite composition"
            />
          </div>
        }

        <!-- Beside the actions, so a refused save is in view where the
             Save was pressed, on a phone as on a desktop. -->
        @if (!tmplDialogOpen() && dialogError(); as derr) {
          <p
            class="banner banner-error dialog-error"
            role="alert"
            data-testid="proc-dialog-error"
          >
            {{ derr }}
          </p>
        }
        <div class="dialog-actions">
          <button type="button" class="btn-text" (click)="dismissSuiteDialog()">
            Cancel
          </button>
          <button
            type="button"
            class="btn-primary"
            data-testid="suite-save-btn"
            [disabled]="submitting() || !canSaveSuite()"
            [gTip]="canSaveSuite() ? '' : suiteSaveDeniedReason()"
            (click)="saveSuiteDialog()"
          >
            {{ submitting() ? "Saving…" : "Save suite" }}
          </button>
        </div>
      </div>
    }

    <!-- CREATE / EDIT TEMPLATE DIALOG (after Suite so + New template stacks over it) -->
    @if (tmplDialogOpen()) {
      <div
        class="scrim"
        [class.is-stacked]="suiteDialogOpen()"
        aria-hidden="true"
        (click)="dismissTemplateDialog()"
      ></div>
      <div
        class="g-dialog is-drawer"
        [class.is-stacked]="suiteDialogOpen()"
        role="dialog"
        aria-modal="true"
        aria-labelledby="tmpl-dialog-title"
        data-testid="template-dialog"
        gDialogTrap
        (gDialogClose)="dismissTemplateDialog()"
      >
        <h2 id="tmpl-dialog-title">
          {{ editingTemplate() ? "Edit template" : "New task template" }}
        </h2>
        <div class="proc-field-grid">
          <label class="field">
            <span>Template name</span>
            <input
              type="text"
              data-testid="tmpl-name-input"
              placeholder="Upgrade Go service"
              [value]="tDraftName()"
              (input)="tDraftName.set($any($event.target).value)"
            />
          </label>
          <label class="field">
            <span>Filed task title (supports {{ "{{param}}" }})</span>
            <input
              type="text"
              data-testid="tmpl-title-input"
              placeholder="chore: upgrade {{ '{{pkg}}' }}"
              [value]="tDraftTitle()"
              (input)="tDraftTitle.set($any($event.target).value)"
            />
          </label>
        </div>
        <label class="field">
          <span>Prompt body</span>
          <textarea
            rows="8"
            data-testid="tmpl-desc-input"
            placeholder="Upgrade {{ '{{pkg}}' }} and run tests."
            [value]="tDraftDesc()"
            (input)="tDraftDesc.set($any($event.target).value)"
          ></textarea>
        </label>
        <div class="proc-field-grid">
          <label class="field">
            <span>Optional bound repository</span>
            <input
              type="text"
              data-testid="tmpl-repo-input"
              placeholder="Leave blank for any repo"
              [value]="tDraftRepo()"
              (input)="tDraftRepo.set($any($event.target).value)"
            />
          </label>
          <label class="field">
            <span>Parameters (comma-separated names)</span>
            <input
              type="text"
              data-testid="tmpl-params-input"
              placeholder="pkg, target_version"
              [value]="tDraftParamsCsv()"
              (input)="onTemplateParamsCsvInput($any($event.target).value)"
            />
          </label>
        </div>

        @if (tDraftFields().length > 0) {
          <div class="field" data-testid="tmpl-draft-fields">
            <span>Parameter metadata</span>
            <div class="tmpl-field-editor-list">
              @for (f of tDraftFields(); track f.name) {
                <div class="tmpl-field-editor-row">
                  <code class="tabular">{{ "{{" + f.name + "}}" }}</code>
                  <input
                    type="text"
                    placeholder="Label (optional)"
                    [attr.aria-label]="'Label for ' + f.name"
                    [attr.data-testid]="'tmpl-field-label-' + f.name"
                    [value]="f.label || ''"
                    (input)="
                      updateTemplateFieldMeta(
                        f.name,
                        'label',
                        $any($event.target).value
                      )
                    "
                  />
                  <input
                    type="text"
                    placeholder="Default value"
                    [attr.aria-label]="'Default value for ' + f.name"
                    [attr.data-testid]="'tmpl-field-default-' + f.name"
                    [value]="f.default || ''"
                    (input)="
                      updateTemplateFieldMeta(
                        f.name,
                        'default',
                        $any($event.target).value
                      )
                    "
                  />
                  <div class="switch-row">
                    <button
                      type="button"
                      role="switch"
                      class="g-switch"
                      [attr.aria-label]="'Required parameter ' + f.name"
                      [attr.data-testid]="'tmpl-field-required-' + f.name"
                      [attr.aria-checked]="!!f.required"
                      (click)="toggleTemplateFieldRequired(f.name)"
                    ></button>
                    <span>Required</span>
                  </div>
                </div>
              }
            </div>
          </div>
        }

        @if (!editingTemplate()?.declaredIn && !editingTemplate()?.system) {
          <div class="field">
            <span>Software workflow</span>
            <g-pill-select
              [options]="qaFlowOptions()"
              [value]="tDraftQaFlowRef()"
              ariaLabel="Software workflow"
              align="left"
              testid="process-qa-flow-select"
              (valueChange)="tDraftQaFlowRef.set($event)"
            />
          </div>
        }

        <div class="switch-row" style="margin-top: 14px">
          <button
            type="button"
            role="switch"
            class="g-switch"
            aria-label="Auto-merge passing pull requests"
            [attr.aria-checked]="tDraftAutoMerge()"
            (click)="tDraftAutoMerge.set(!tDraftAutoMerge())"
          ></button>
          <span>Auto-merge passing pull requests</span>
        </div>
        <div class="switch-row" style="margin-top: 14px">
          <button
            type="button"
            role="switch"
            class="g-switch"
            aria-label="Keep running"
            data-testid="tmpl-keep-running"
            [attr.aria-checked]="tDraftKeepRunning()"
            (click)="tDraftKeepRunning.set(!tDraftKeepRunning())"
          ></button>
          <span
            >Keep running
            <span class="muted"
              >— restart every task this template files when its run exits,
              instead of completing or parking it</span
            ></span
          >
        </div>
        <div class="switch-row" style="margin-top: 14px">
          <button
            type="button"
            role="switch"
            class="g-switch"
            aria-label="Interactive"
            data-testid="tmpl-interactive"
            [attr.aria-checked]="tDraftInteractive()"
            (click)="tDraftInteractive.set(!tDraftInteractive())"
          ></button>
          <span
            >Interactive
            <span class="muted"
              >— file a live chat: the agent can talk with you while it
              works</span
            ></span
          >
        </div>
        <label class="field">
          <span>Task priority</span>
          <input
            type="text"
            inputmode="numeric"
            data-testid="tmpl-priority-input"
            placeholder="5"
            [value]="tDraftPriority()"
            [attr.aria-invalid]="tDraftPriorityInvalid() ? 'true' : null"
            (input)="tDraftPriority.set($any($event.target).value)"
            (blur)="normalizeTemplatePriority()"
          />
          @if (tDraftPriorityInvalid()) {
            <span class="proc-field-error" data-testid="tmpl-priority-error"
              >Use a whole number, 1 or more.</span
            >
          } @else {
            <span class="muted proc-field-help"
              >Blank uses the default, 5; higher runs first.</span
            >
          }
        </label>
        <div class="proc-field-grid">
          <div class="field">
            <span>Admission</span>
            <g-pill-select
              [options]="admissionOptions"
              [value]="tDraftAdmission()"
              ariaLabel="Admission"
              align="left"
              testid="tmpl-admission"
              (valueChange)="tDraftAdmission.set($event)"
            />
            <p class="muted proc-field-help" data-testid="tmpl-admission-help">
              {{ admissionHelp(tDraftAdmission()) }} Applies to every task this
              template files.
            </p>
          </div>
          <div class="field">
            <span>Hosting</span>
            <g-pill-select
              [options]="hostingOptions"
              [value]="tDraftHosting()"
              ariaLabel="Hosting"
              align="left"
              testid="tmpl-hosting"
              (valueChange)="tDraftHosting.set($event)"
            />
            <p class="muted proc-field-help" data-testid="tmpl-hosting-help">
              {{ hostingHelp(tDraftHosting()) }} Applies to every task this
              template files.
            </p>
          </div>
        </div>
        <div class="field">
          <span>Grants</span>
          <g-grants-picker
            testid="tmpl-grants"
            [offered]="offeredCapabilities()"
            [held]="tDraftCaps()"
            [repo]="tDraftRepo().trim() || undefined"
            [policyVerdicts]="true"
            (heldChange)="tDraftCaps.set($event)"
          />
        </div>
        <div class="field">
          <span>Model</span>
          <div class="proc-model-row">
            <g-model-picker
              [groups]="modelGroups()"
              [value]="tDraftModel()"
              [placeholder]="'Deployment default model'"
              ariaLabel="Model"
              testid="tmpl-model"
              (valueChange)="tDraftModel.set($event)"
            />
            @if (tDraftModel()) {
              <button
                type="button"
                class="btn-text"
                data-testid="tmpl-model-clear"
                (click)="tDraftModel.set('')"
              >
                Use default
              </button>
            }
          </div>
          <p class="muted proc-field-help">
            The model every task this template files asks for.
          </p>
        </div>
        <!-- Beside the actions, so a refused save is in view where the
             Save was pressed, on a phone as on a desktop. -->
        @if (dialogError(); as derr) {
          <p
            class="banner banner-error dialog-error"
            role="alert"
            data-testid="proc-dialog-error"
          >
            {{ derr }}
          </p>
        }
        <div class="dialog-actions">
          @if (editingTemplate(); as et) {
            <button
              type="button"
              class="btn-tonal dialog-action-start"
              data-testid="tmpl-dialog-new-task-btn"
              [disabled]="!store.canLaunchTemplate(et)"
              [gTip]="
                store.canLaunchTemplate(et)
                  ? 'Open the New task sheet pre-filled with this template.'
                  : access.deniedReason('POST /api/tasks', et.repo || undefined)
              "
              aria-label="New task from this template"
              (click)="openNewTaskFromTemplate(et, $event, true)"
            >
              New task<span class="dialog-action-long">
                from this template</span
              >
            </button>
          }
          <button
            type="button"
            class="btn-text"
            (click)="dismissTemplateDialog()"
          >
            Cancel
          </button>
          <button
            type="button"
            class="btn-primary"
            data-testid="tmpl-save-btn"
            [disabled]="submitting() || !canSaveTemplate()"
            (click)="saveTemplateDialog()"
          >
            {{ submitting() ? "Saving…" : "Save template" }}
          </button>
        </div>
      </div>
    }

    <!-- RUN SUITE DIALOG -->
    @if (runSuiteDialogOpen()) {
      <div
        class="scrim"
        aria-hidden="true"
        (click)="closeRunSuiteDialog()"
      ></div>
      <div
        class="g-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="run-suite-title"
        data-testid="run-suite-dialog"
        gDialogTrap
        (gDialogClose)="closeRunSuiteDialog()"
      >
        <h2 id="run-suite-title">Run suite</h2>
        <div class="field">
          <span>Suite</span>
          <div class="pill-dropdown">
            <button
              type="button"
              class="pill-trigger"
              data-testid="run-suite-picker"
              aria-haspopup="listbox"
              [attr.aria-expanded]="openPicker() === 'runSuite'"
              (click)="togglePicker('runSuite', $event)"
            >
              <span>{{ suiteNameById(runSuiteId()) || "Choose suite…" }}</span>
              <span aria-hidden="true">▾</span>
            </button>
            @if (openPicker() === "runSuite") {
              <ul class="pop-menu" role="listbox" aria-label="Suite">
                @for (st of store.suites(); track st.id) {
                  <li role="none">
                    <button
                      type="button"
                      role="option"
                      [attr.aria-selected]="runSuiteId() === st.id"
                      (click)="runSuiteId.set(st.id); openPicker.set(null)"
                    >
                      {{ st.name }}
                    </button>
                  </li>
                }
              </ul>
            }
          </div>
        </div>
        <label class="field">
          <span>Repository (owner/name)</span>
          <input
            type="text"
            data-testid="run-suite-repo-input"
            placeholder="acme/widgets"
            [value]="runSuiteRepo()"
            (input)="runSuiteRepo.set($any($event.target).value)"
          />
        </label>
        <label class="field">
          <span>Base branch</span>
          <input
            type="text"
            data-testid="run-suite-base-input"
            placeholder="main"
            [value]="runSuiteBase()"
            (input)="runSuiteBase.set($any($event.target).value)"
          />
        </label>
        <!-- Beside the actions, so a refused save is in view where the
             Save was pressed, on a phone as on a desktop. -->
        @if (dialogError(); as derr) {
          <p
            class="banner banner-error dialog-error"
            role="alert"
            data-testid="proc-dialog-error"
          >
            {{ derr }}
          </p>
        }
        <div class="dialog-actions">
          <button
            type="button"
            class="btn-text"
            (click)="closeRunSuiteDialog()"
          >
            Cancel
          </button>
          <button
            type="button"
            class="btn-primary"
            data-testid="run-suite-submit"
            [disabled]="submitting() || !canSubmitRunSuite()"
            (click)="submitRunSuite()"
          >
            {{ submitting() ? "Starting…" : "Start suite run" }}
          </button>
        </div>
      </div>
    }

    <!-- REPO WORKFLOW YAML EDITOR DIALOG -->
    @if (wfEditorOpen()) {
      <div
        class="scrim"
        aria-hidden="true"
        (click)="wfEditorOpen.set(false)"
      ></div>
      <div
        class="g-dialog is-drawer"
        role="dialog"
        aria-modal="true"
        aria-labelledby="wf-editor-title"
        data-testid="repo-workflow-dialog"
        gDialogTrap
        (gDialogClose)="wfEditorOpen.set(false)"
      >
        <h2 id="wf-editor-title">Workflow YAML for {{ wfEditorRepo() }}</h2>
        <label class="field">
          <span>Workflow file path</span>
          <input
            type="text"
            data-testid="wf-path-input"
            placeholder=".grain/workflows/ci.yml"
            [readOnly]="!canEditWorkflow()"
            [value]="wfEditorPath()"
            (input)="wfEditorPath.set($any($event.target).value)"
          />
        </label>
        <label class="field">
          <span>YAML definition</span>
          <textarea
            class="proc-yaml-editor"
            data-testid="wf-yaml-input"
            [readOnly]="!canEditWorkflow()"
            [value]="wfEditorText()"
            (input)="wfEditorText.set($any($event.target).value)"
          ></textarea>
        </label>
        <!-- Beside the actions, so a refused save is in view where the
             Save was pressed, on a phone as on a desktop. -->
        @if (dialogError(); as derr) {
          <p
            class="banner banner-error dialog-error"
            role="alert"
            data-testid="proc-dialog-error"
          >
            {{ derr }}
          </p>
        }
        <div class="dialog-actions">
          @if (wfEditorOriginalPath() && canEditWorkflow()) {
            <button
              type="button"
              class="btn-tonal danger"
              style="margin-right: auto"
              data-testid="wf-delete-btn"
              [disabled]="submitting()"
              (click)="deleteWorkflowOverride()"
            >
              Delete override
            </button>
          }
          <button
            type="button"
            class="btn-text"
            (click)="wfEditorOpen.set(false)"
          >
            {{ canEditWorkflow() ? "Cancel" : "Close" }}
          </button>
          @if (canEditWorkflow()) {
            <button
              type="button"
              class="btn-primary"
              data-testid="wf-save-btn"
              [disabled]="
                submitting() || !wfEditorPath().trim() || !wfEditorText().trim()
              "
              (click)="saveWorkflowEditor()"
            >
              {{ submitting() ? "Saving…" : "Save workflow" }}
            </button>
          }
        </div>
      </div>
    }

    @if (confirming(); as req) {
      <g-confirm-dialog [request]="req" (done)="onConfirmDone($event)" />
    }
  `,
})
export class ProcessesPageComponent implements OnInit, OnDestroy {
  readonly tab = input<string | undefined>(undefined);
  readonly flow = input<string | undefined>(undefined);
  readonly itemId = input<string | undefined>(undefined);
  readonly runId = input<string | undefined>(undefined);
  readonly kind = input<string | undefined>(undefined);
  readonly q = input<string | undefined>(undefined);
  readonly sort = input<string | undefined>(undefined);
  readonly new = input<string | undefined>(undefined);
  readonly edit = input<string | undefined>(undefined);
  readonly launch = input<string | undefined>(undefined);
  readonly launchTemplate = input<string | undefined>(undefined);
  readonly run = input<string | undefined>(undefined);
  readonly runSuite = input<string | undefined>(undefined);
  // ?repo=owner/name on the declarations tab opens that repository's
  // .grain/ declarations: Settings → Repositories links each row here.
  readonly repo = input<string | undefined>(undefined);

  protected readonly store = inject(ProcessesStore);
  protected readonly access = inject(AccessService);
  protected readonly filter = inject(ViewFilter);
  private readonly createTask = inject(CreateTaskService);
  private readonly api = inject(GrainApi);
  private readonly taskFeed = inject(TaskFeedStore);
  private readonly router = inject(Router);
  private readonly snackbar = inject(SnackbarService);
  private readonly doc = inject(DOCUMENT);
  private readonly zone = inject(NgZone);
  private readonly injector = inject(Injector);
  private readonly hostEl = inject<ElementRef<HTMLElement>>(ElementRef);

  private readonly onCaptureKeydown = (ev: KeyboardEvent): void => {
    if (ev.key === "Escape" && (this.openPicker() || this.suiteTmplOpen())) {
      ev.preventDefault();
      ev.stopPropagation();
      ev.stopImmediatePropagation();
      // Focus inside the New menu would be lost with it: hand it back to
      // the New button, as a menu button does.
      const fromNewMenu =
        this.openPicker() === "newMenu" &&
        !!(ev.target as Element | null)?.closest?.(
          '[data-testid="proc-new-menu"]',
        );
      this.zone.run(() => {
        if (this.suiteTmplOpen()) this.suiteTmplOpen.set(false);
        else this.openPicker.set(null);
      });
      if (fromNewMenu) {
        this.hostEl.nativeElement
          .querySelector<HTMLElement>('[data-testid="proc-new-menu-btn"]')
          ?.focus();
      }
    }
  };

  protected readonly tabs = TABS;
  protected readonly weekdays = WEEKDAYS;
  protected readonly cadenceKinds = CADENCE_KINDS;

  protected readonly schedFilters: { id: SchedFilter; label: string }[] = [
    { id: "all", label: "All" },
    { id: "active", label: "Active" },
    { id: "paused", label: "Paused" },
    { id: "triggers", label: "Event triggers" },
    { id: "suppressed", label: "Suppressed" },
  ];

  protected readonly tmplFilters: { id: TmplFilter; label: string }[] = [
    { id: "all", label: "All" },
    { id: "deployment", label: "Deployment" },
    { id: "declared", label: "Repo-declared" },
    { id: "system", label: "Built-in" },
  ];

  protected readonly schedFilter = signal<SchedFilter>("all");
  protected readonly tmplFilter = signal<TmplFilter>("all");
  protected readonly suitesView = signal<SuitesPaneView>("suites");

  protected readonly selectedScheduleId = signal<string | null>(null);
  protected readonly selectedTrigger = signal<DeclaredTrigger | null>(null);
  protected readonly liveSelectedTrigger = computed<DeclaredTrigger | null>(
    () => {
      const sel = this.selectedTrigger();
      if (!sel) return null;
      return (
        this.store
          .triggers()
          .find((t) => t.repo === sel.repo && t.path === sel.path) ?? sel
      );
    },
  );
  protected readonly selectedTriggerKey = computed(() => {
    const tr = this.selectedTrigger();
    return tr ? `trigger:${tr.repo}:${tr.path}` : null;
  });
  protected readonly triggerYamlText = signal("");
  protected readonly triggerYamlLoading = signal(false);
  protected readonly triggerYamlDisplay = computed(() => {
    const tr = this.liveSelectedTrigger();
    if (!tr) return "No YAML content available.";
    if (tr.detail) {
      return synthesizeTriggerYaml(
        tr.repo,
        tr.path,
        tr.detail,
        this.store.templates(),
      );
    }
    if (this.triggerYamlLoading() || this.store.repoLoading() === tr.repo) {
      return "Loading YAML…";
    }
    const raw = this.triggerYamlText();
    if (raw) return raw;
    return synthesizeTriggerYaml(
      tr.repo,
      tr.path,
      undefined,
      this.store.templates(),
    );
  });
  protected readonly parsedSelectedTrigger = computed<ParsedTriggerYaml>(() => {
    const tr = this.liveSelectedTrigger();
    if (tr?.detail) {
      return {
        on: tr.detail.on || "",
        paths: tr.detail.paths ?? [],
        gate: tr.detail.gate ? "true" : "false",
        template: tr.detail.template || "",
      };
    }
    const raw = this.triggerYamlText();
    if (raw) return parseTriggerYaml(raw);
    return { on: "", paths: [], gate: "", template: "" };
  });
  protected readonly matchedTriggerTemplate = computed<Template | null>(() => {
    const tr = this.liveSelectedTrigger();
    const ref = this.parsedSelectedTrigger().template.trim();
    if (!tr || !ref) return null;
    const list = this.store.templates();
    return (
      list.find((t) => t.id === ref) ??
      list.find((t) => t.id === `repo:${tr.repo}:${ref}`) ??
      list.find(
        (t) =>
          t.declaredIn?.repo === tr.repo &&
          templateFileStem(t.declaredIn.path) === ref,
      ) ??
      list.find(
        (t) =>
          (!t.declaredIn || t.declaredIn.repo === tr.repo) && t.name === ref,
      ) ??
      null
    );
  });
  protected readonly selectedTemplateId = signal<string | null>(null);
  protected readonly selectedSuiteId = signal<string | null>(null);
  protected readonly selectedSuiteRunId = signal<number | null>(null);
  protected readonly selectedDeclRepo = signal<string | null>(null);
  protected readonly inlineYamlKey = signal<string | null>(null);
  private inlineYamlSeq = 0;
  protected readonly inlineYamlText = signal<string>("");
  protected readonly inlineYamlLoading = signal(false);
  protected readonly inlineYamlDisplay = computed(() =>
    this.inlineYamlLoading() ? "Loading YAML…" : this.inlineYamlText(),
  );
  protected readonly mobileDetailOpen = signal(false);
  private handledRepoLinkKey = "";

  protected readonly scheduleComposition = signal<Composition | null>(null);
  protected readonly templateComposition = signal<Composition | null>(null);
  protected readonly suiteComposition = signal<Composition | null>(null);

  protected readonly openPicker = signal<string | null>(null);
  protected readonly submitting = signal(false);
  protected readonly dialogError = signal<string | null>(null);
  protected readonly confirming = signal<ConfirmRequest | null>(null);

  // Schedule dialog state
  protected readonly schedDialogOpen = signal(false);
  protected readonly editingSchedule = signal<Schedule | null>(null);
  protected readonly schedTargetKind = signal<"prompt" | "template" | "suite">(
    "prompt",
  );
  protected readonly sDraftTitle = signal("");
  protected readonly sDraftDesc = signal("");
  protected readonly sDraftTemplateId = signal("");
  protected readonly sDraftSuiteId = signal("");
  protected readonly sDraftRepo = signal("");
  protected readonly schedRepoOptions = computed<PillOption[]>(() => [
    { value: "", label: "Choose configured repository…" },
    ...this.store.knownRepos().map((r) => ({ value: r, label: r })),
  ]);
  protected readonly schedBoundTemplate = computed<Template | null>(() => {
    if (this.editingSchedule()?.system) return null;
    if (this.schedTargetKind() !== "template") return null;
    const id = this.sDraftTemplateId();
    if (!id) return null;
    const tmpl = this.store.templates().find((t) => t.id === id);
    return tmpl?.repo?.trim() ? tmpl : null;
  });
  protected readonly schedEffectiveRepo = computed(
    () => this.schedBoundTemplate()?.repo?.trim() || this.sDraftRepo().trim(),
  );
  protected readonly sDraftBase = signal("main");
  protected readonly sDraftKind = signal("daily");
  protected readonly sDraftEveryMin = signal(15);
  protected readonly sDraftEveryHr = signal(6);
  protected readonly sDraftTime = signal("09:00");
  protected readonly sDraftWeekday = signal("monday");
  protected readonly sDraftDayOfMonth = signal(1);
  protected readonly sDraftTz = signal("UTC");
  protected readonly sDraftTriggerWorkflow = signal("");
  protected readonly sDraftTriggerConclusion = signal("failure");
  protected readonly sDraftTriggerBranch = signal("");
  protected readonly sDraftGitRefRepo = signal("");
  protected readonly sDraftGitRef = signal("refs/heads/main");
  protected readonly sDraftOverlap = signal("skip");
  // What every firing of an inline-prompt schedule carries beside its
  // prompt (model.Schedule): a template-backed schedule takes these from
  // its template, so only the model is the schedule's own there.
  protected readonly sDraftModel = signal("");
  protected readonly sDraftKeepRunning = signal(false);
  protected readonly sDraftAdmission = signal("standard");
  protected readonly sDraftHosting = signal("unattended");
  protected readonly sDraftCaps = signal<string[]>([]);
  protected readonly sDraftNetwork = signal("");
  protected readonly sDraftReads = signal<string[]>([]);
  protected readonly sDraftReadInput = signal("");
  protected readonly sDraftWrites = signal<WriteTarget[]>([]);
  protected readonly sDraftWriteRepo = signal("");
  protected readonly sDraftWriteBase = signal("");
  protected readonly sDraftCustomFields = signal<
    Record<string, string | string[]>
  >({});
  protected readonly sDraftCustomInput = signal<Record<string, string>>({});
  protected readonly sDraftQaFlowRef = signal("");
  protected readonly draftSchedComposition = signal<Composition | null>(null);

  protected readonly declaredCustomFields = computed<CustomFieldDeclaration[]>(
    () => this.access.rawConfig()?.customFields ?? [],
  );
  protected readonly schedReadRepoOptions = computed<PillOption[]>(() => {
    const seen = new Set<string>();
    const main = this.sDraftRepo().trim().toLowerCase();
    for (const r of this.sDraftReads()) seen.add(r.toLowerCase());
    if (main) seen.add(main);
    const candidates: string[] = [];
    for (const r of [
      ...this.store.knownRepos(),
      ...this.store.reachableRepos(),
    ]) {
      const key = r.trim().toLowerCase();
      if (!key || seen.has(key)) continue;
      seen.add(key);
      candidates.push(r.trim());
    }
    candidates.sort((a, b) => a.localeCompare(b));
    return [
      { value: "", label: "Add read-only repository…" },
      ...candidates.map((r) => ({ value: r, label: r })),
    ];
  });
  protected readonly schedWriteRepoOptions = computed<PillOption[]>(() => {
    const main = this.sDraftRepo().trim().toLowerCase();
    const candidates = this.store
      .knownRepos()
      .filter((r) => r.trim().toLowerCase() !== main);
    return [
      { value: "", label: "Select repo…" },
      ...candidates.map((r) => ({ value: r, label: r })),
    ];
  });
  protected readonly schedSelectedWriteRepo = computed(() => {
    const cur = this.sDraftWriteRepo();
    return this.schedWriteRepoOptions().some((o) => o.value === cur) ? cur : "";
  });
  protected readonly networkAccessOptions: PillOption[] =
    NETWORK_ACCESS_OPTIONS.map((o) => ({ value: o.value, label: o.label }));
  protected readonly customBooleanOptions: PillOption[] = [
    { value: "", label: "Not set" },
    { value: "true", label: "Yes" },
    { value: "false", label: "No" },
  ];

  // Launch template dialog state
  protected readonly launchDialogOpen = signal(false);
  protected readonly launchTemplateId = signal("");
  protected readonly launchRepo = signal("");
  protected readonly launchBase = signal("");
  protected readonly launchApproved = signal(true);
  protected readonly launchFields = signal<Record<string, string>>({});

  // Create/Edit template dialog state
  protected readonly tmplDialogOpen = signal(false);
  protected readonly editingTemplate = signal<Template | null>(null);
  protected readonly tDraftName = signal("");
  protected readonly tDraftTitle = signal("");
  protected readonly tDraftDesc = signal("");
  protected readonly tDraftRepo = signal("");
  protected readonly tDraftParamsCsv = signal("");
  protected readonly tDraftFields = signal<TemplateField[]>([]);
  protected readonly tDraftAutoMerge = signal(false);
  protected readonly tDraftModel = signal("");
  protected readonly tDraftKeepRunning = signal(false);
  // tDraftPriority is the Task priority field as typed: blank leaves the
  // template's tasks on the deployment's default (5).
  protected readonly tDraftPriority = signal("");
  protected readonly tDraftInteractive = signal(false);
  protected readonly tDraftPriorityInvalid = computed(
    () =>
      this.tDraftPriority().trim() !== "" &&
      Number.isNaN(taskPriority(this.tDraftPriority())),
  );
  protected readonly tDraftAdmission = signal("standard");
  protected readonly tDraftHosting = signal("unattended");
  protected readonly tDraftCaps = signal<string[]>([]);

  // The vocabularies the schedule and template forms share: the
  // deployment's model catalog (one group per connection), the
  // capabilities it offers, and the admission and hosting settings.
  protected readonly modelGroups = computed(() =>
    modelGroups(
      (this.access.rawConfig()?.agentBackends ?? []) as ModelBackendLike[],
    ),
  );
  protected readonly offeredCapabilities = computed(
    () => this.access.rawConfig()?.capabilities ?? [],
  );
  protected readonly admissionOptions: PillOption[] = ADMISSION_OPTIONS.map(
    (o) => ({ value: o.value, label: o.label }),
  );
  protected readonly hostingOptions: PillOption[] = HOSTING_OPTIONS.map(
    (o) => ({ value: o.value, label: o.label }),
  );
  protected readonly schedModelPlaceholder = computed(() =>
    this.schedTargetKind() === "template" && !this.editingSchedule()?.system
      ? "The template's model"
      : "Deployment default model",
  );
  protected admissionHelp(v: string): string {
    return ADMISSION_OPTIONS.find((o) => o.value === v)?.help ?? "";
  }
  protected hostingHelp(v: string): string {
    return HOSTING_OPTIONS.find((o) => o.value === v)?.help ?? "";
  }
  protected readonly tDraftQaFlowRef = signal("");

  // Create/Edit suite dialog state
  protected readonly suiteDialogOpen = signal(false);
  protected readonly editingSuite = signal<Suite | null>(null);
  protected readonly stDraftName = signal("");
  protected readonly stDraftMode = signal("count");
  protected readonly stDraftCount = signal(1);
  protected readonly stDraftMaxPasses = signal(3);
  // model.MaxSuitePasses: the most passes a suite may run.
  protected readonly maxSuitePasses = 100;
  protected readonly stDraftTemplateIds = signal<string[]>([]);
  protected readonly stDraftRequireApproval = signal(false);
  protected readonly stDraftAutoMerge = signal(true);
  protected readonly stDraftQaFlowRef = signal("");
  protected readonly draftSuiteComposition = signal<Composition | null>(null);
  protected readonly suiteTmplQuery = signal("");
  protected readonly suiteTmplOpen = signal(false);
  protected readonly suiteTmplActiveIndex = signal(0);
  protected readonly creatingTemplateForSuite = signal(false);

  protected readonly suiteTemplateLabels = computed(() =>
    templateDisambiguatedLabels(this.store.templates()),
  );
  protected readonly filteredSuiteTemplates = computed(() => {
    const labels = this.suiteTemplateLabels();
    const q = this.suiteTmplQuery();
    return this.schedulableTemplates()
      .map((t) => {
        const label = labels.get(t.id) ?? (t.name || t.title || t.id);
        const repo = t.repo || t.declaredIn?.repo || "";
        const titlePart = t.title && t.title !== label ? t.title : "";
        const sub =
          titlePart && repo ? `${titlePart} — ${repo}` : titlePart || repo;
        return { template: t, label, sub };
      })
      .filter((item) => matchesTemplateQuery(item.template, item.label, q));
  });
  protected readonly suiteActiveOptionId = computed(() => {
    const list = this.filteredSuiteTemplates();
    const idx = this.suiteTmplActiveIndex();
    const item = list[idx];
    return item ? `suite-tmpl-opt-${item.template.id}` : "";
  });

  private requestedQaFlowVersions = false;
  protected readonly qaFlowVersionsLoaded = signal(false);
  protected readonly loadedQaFlowVersions = signal<
    {
      ref: string;
      key: string;
      version: number;
      label: string;
      hint?: string;
      benchmark: boolean;
    }[]
  >([]);
  protected readonly qaFlowOptions = computed<PillOption[]>(() => {
    const opts: PillOption[] = [{ value: "", label: "Deployment default" }];
    const seen = new Set<string>([""]);
    const addOption = (
      raw: string | undefined,
      explicitLabel?: string,
      explicitHint?: string,
    ): void => {
      const ref = (raw ?? "").trim();
      if (!ref || seen.has(ref)) return;
      seen.add(ref);
      const atIdx = ref.indexOf("@");
      const key = atIdx >= 0 ? ref.slice(0, atIdx) : ref;
      opts.push({
        value: ref,
        label: formatQaFlowLabel(key, explicitLabel),
        hint: explicitHint ?? formatQaFlowHint(ref),
      });
    };
    for (const v of this.loadedQaFlowVersions()) {
      addOption(v.ref, v.label, v.hint);
    }
    if (!this.qaFlowVersionsLoaded()) {
      for (const ref of DEFAULT_QA_FLOW_REFS) addOption(ref);
    }
    addOption(this.sDraftQaFlowRef());
    addOption(this.tDraftQaFlowRef());
    addOption(this.stDraftQaFlowRef());
    return opts;
  });

  // Run suite dialog state
  protected readonly runSuiteDialogOpen = signal(false);
  protected readonly runSuiteId = signal("");
  protected readonly runSuiteRepo = signal("");
  protected readonly runSuiteBase = signal("main");

  // Repo workflow editor dialog
  protected readonly wfEditorOpen = signal(false);
  protected readonly wfEditorRepo = signal("");
  protected readonly wfEditorOriginalPath = signal("");
  protected readonly wfEditorPath = signal(".grain/workflows/ci.yml");
  protected readonly wfEditorText = signal("");

  private readonly splitEl = viewChild<ElementRef<HTMLElement>>("split");

  protected readonly sp = new SplitController({
    ratioKey: "grain-ng.processes.split-ratio",
    collapsedKey: "grain-ng.processes.split-collapsed",
    defaultRatio: 0.35,
    element: () => this.splitEl()?.nativeElement,
  });

  protected readonly activeTab = computed<ProcessTab>(() => {
    if (this.runId()) return "suites";
    const raw = (this.tab() || "").toLowerCase();
    if (
      raw === "templates" ||
      raw === "suites" ||
      raw === "declarations" ||
      raw === "quality" ||
      raw === "schedules"
    ) {
      return raw;
    }
    const k = (this.kind() || "").split(",")[0]?.trim().toLowerCase() || "";
    if (k === "template" || k === "templates") return "templates";
    if (k === "suite" || k === "suites") return "suites";
    if (k === "declaration" || k === "declarations") return "declarations";
    return "schedules";
  });

  protected readonly modalOpen = computed(
    () =>
      this.schedDialogOpen() ||
      this.launchDialogOpen() ||
      this.tmplDialogOpen() ||
      this.suiteDialogOpen() ||
      this.runSuiteDialogOpen() ||
      this.wfEditorOpen() ||
      !!this.confirming(),
  );

  protected readonly filteredSchedules = computed(() => {
    const f = this.schedFilter();
    const q = this.filter.query().trim().toLowerCase();
    return this.store.schedules().filter((s) => {
      if (f === "active" && !s.enabled) return false;
      if (f === "paused" && s.enabled) return false;
      if (f === "triggers" && !isEventTrigger(s)) return false;
      if (f === "suppressed" && !s.suppressed) return false;
      if (q) {
        const target = this.schedTargetOf(s);
        const hay = [
          s.id,
          s.title,
          target.repo,
          target.base,
          s.templateName ?? "",
          s.suiteName ?? "",
          formatCadence(s.recurrence, s.timeZone),
        ]
          .join(" ")
          .toLowerCase();
        if (!hay.includes(q)) return false;
      }
      return true;
    });
  });

  protected readonly filteredTemplates = computed(() => {
    const f = this.tmplFilter();
    const q = this.filter.query().trim().toLowerCase();
    return this.store.templates().filter((t) => {
      if (f === "deployment" && (t.declaredIn || t.system)) return false;
      if (f === "declared" && !t.declaredIn) return false;
      if (f === "system" && !t.system) return false;
      if (q) {
        const hay = [
          t.id,
          t.name,
          t.title,
          t.description,
          t.declaredIn?.repo ?? "",
          t.repo ?? "",
        ]
          .join(" ")
          .toLowerCase();
        if (!hay.includes(q)) return false;
      }
      return true;
    });
  });

  protected readonly schedulableTemplates = computed(() =>
    this.store.templates().filter((t) => !t.system),
  );

  protected readonly filteredSuites = computed(() => {
    const q = this.filter.query().trim().toLowerCase();
    return this.store.suites().filter((s) => {
      if (!q) return true;
      return `${s.id} ${s.name} ${s.declaredIn?.repo ?? ""}`
        .toLowerCase()
        .includes(q);
    });
  });

  protected readonly filteredSuiteRuns = computed(() => {
    const q = this.filter.query().trim().toLowerCase();
    return this.store.suiteRuns().filter((r) => {
      if (!q) return true;
      return `${r.id} ${r.suiteName} ${r.repo} ${r.base} ${r.status}`
        .toLowerCase()
        .includes(q);
    });
  });

  protected readonly filteredDeclRepos = computed(() => {
    const q = this.filter.query().trim().toLowerCase();
    return this.store
      .knownRepos()
      .filter((r) => !q || r.toLowerCase().includes(q));
  });

  // ?sort= names the order; with none the list is in its custom order,
  // which is name order until somebody moves a row.
  // Suites have no kinds, so a Kind sort carried onto their tab reads as
  // the custom order it falls back to.
  protected readonly activeSort = computed<ProcessSort>(() => {
    const sort = parseProcessSort(this.sort());
    return sort === "kind" && this.activeTab() === "suites" ? "custom" : sort;
  });
  protected readonly customOrder = signal<string[]>(loadOrder());
  protected readonly noRuns = NO_RUNS;
  protected readonly moveNote = signal("");
  protected readonly dropKey = signal<string | null>(null);
  private dragKey: string | null = null;

  protected readonly sortOptions = computed<PillOption[]>(() =>
    PROCESS_SORTS.filter(
      (o) => o.id !== "kind" || this.activeTab() !== "suites",
    ).map((o) => ({ value: o.id, label: o.label })),
  );

  protected readonly templateRuns = computed(() => {
    const scheds = this.store.schedules();
    return new Map(
      this.store
        .templates()
        .map((t) => [t.id, derivedRuns(scheds, "templateId", t.id)] as const),
    );
  });

  protected readonly suiteRunsById = computed(() => {
    const scheds = this.store.schedules();
    const runs = this.store.suiteRuns();
    return new Map(
      this.store
        .suites()
        .map(
          (st) => [st.id, derivedRuns(scheds, "suiteId", st.id, runs)] as const,
        ),
    );
  });

  // A declared trigger is listed beside the schedules, as the classic
  // page lists it: under All and Event triggers, never paused or
  // suppressed, and with nothing to run until its files change.
  protected readonly filteredTriggers = computed(() => {
    const f = this.schedFilter();
    if (f !== "all" && f !== "triggers") return [];
    const q = this.filter.query().trim().toLowerCase();
    return this.store
      .triggers()
      .filter((t) => !q || `${t.repo} ${t.path}`.toLowerCase().includes(q));
  });

  protected readonly scheduleRows = computed(() =>
    sortProcessRows<SchedTabRow>(
      [
        ...this.filteredSchedules().map((s) => ({
          key: `schedule:${s.id}`,
          name: s.title || s.templateName || s.suiteName || s.id,
          kind: scheduleKindOf(s),
          lastRunAt: s.lastRunAt || null,
          nextRunAt:
            s.enabled && !s.suppressed && !isEventTrigger(s)
              ? s.nextRunAt || null
              : null,
          item: { key: `schedule:${s.id}`, schedule: s },
        })),
        ...this.filteredTriggers().map((t) => {
          const key = `trigger:${t.repo}:${t.path}`;
          return {
            key,
            name: triggerName(t.path),
            kind: "trigger",
            lastRunAt: null,
            nextRunAt: null,
            item: { key, trigger: t },
          };
        }),
      ],
      this.activeSort(),
      this.customOrder(),
    ).map((r) => r.item),
  );

  protected readonly sortedSchedules = computed(() =>
    this.scheduleRows().flatMap((r) => (r.schedule ? [r.schedule] : [])),
  );

  protected readonly sortedTemplates = computed(() => {
    const runs = this.templateRuns();
    return sortProcessRows(
      this.filteredTemplates().map((t) => ({
        key: `template:${t.id}`,
        name: t.name || t.title || t.id,
        kind: templateSourceBadge(t).label,
        ...(runs.get(t.id) ?? NO_RUNS),
        item: t,
      })),
      this.activeSort(),
      this.customOrder(),
    ).map((r) => r.item);
  });

  protected readonly sortedSuites = computed(() => {
    const runs = this.suiteRunsById();
    return sortProcessRows(
      this.filteredSuites().map((st) => ({
        key: `suite:${st.id}`,
        name: st.name || st.id,
        kind: "suite",
        ...(runs.get(st.id) ?? NO_RUNS),
        item: st,
      })),
      this.activeSort(),
      this.customOrder(),
    ).map((r) => r.item);
  });

  protected readonly canCreateAny = computed(
    () =>
      this.access.can("POST /api/schedules") ||
      this.access.can("POST /api/templates") ||
      this.access.can("POST /api/suites"),
  );

  protected readonly selectedSchedule = computed(() => {
    const id = this.selectedScheduleId();
    return this.store.schedules().find((s) => s.id === id) ?? null;
  });

  protected readonly selectedTemplate = computed(() => {
    const id = this.selectedTemplateId();
    return this.store.templates().find((t) => t.id === id) ?? null;
  });

  protected readonly selectedSuite = computed(() => {
    const id = this.selectedSuiteId();
    return this.store.suites().find((s) => s.id === id) ?? null;
  });

  protected readonly selectedSuiteRun = computed(() => {
    const id = this.selectedSuiteRunId();
    if (id === null) return null;
    return this.store.suiteRuns().find((r) => r.id === id) ?? null;
  });

  protected readonly launchSelectedTemplate = computed<Template | null>(() => {
    const id = this.launchTemplateId();
    const list = this.store.templates();
    const found = list.find((t) => t.id === id);
    if (found) return found;
    return list.length > 0 ? list[0] : null;
  });

  protected readonly launchTemplateLabel = computed(() => {
    const t = this.launchSelectedTemplate();
    return t ? t.name || t.title || t.id : "Select template";
  });

  private writingQ = 0;
  private handledDeepLinkKey = "";
  private handledItemLinkKey = "";
  private handledItemTab: ProcessTab | null = null;
  private missingItemError: string | null = null;

  constructor() {
    afterNextRender(() => this.sp.attach());

    const nav =
      this.router.getCurrentNavigation() ??
      this.router.lastSuccessfulNavigation;
    const navState = nav?.extras.state;
    const handoffTmplId =
      typeof navState?.["selectTemplateId"] === "string"
        ? navState["selectTemplateId"].trim()
        : "";
    if (handoffTmplId) {
      this.tmplFilter.set("all");
      this.selectedTemplateId.set(handoffTmplId);
      this.mobileDetailOpen.set(true);
    }
    let handledHandoffTmpl = !handoffTmplId;
    effect(() => {
      const loaded = this.store.loaded();
      untracked(() => {
        if (!handledHandoffTmpl && loaded) {
          handledHandoffTmpl = true;
          this.selectTemplate(handoffTmplId, true);
        }
      });
    });

    // ?q= (a shared link, a reload, Back and Forward) fills the "Filter
    // this view" box, and what is typed there goes back into ?q=, the way
    // the Tasks page keeps it. While one of this page's own ?q= writes is
    // in flight, the ?q= it passes through is an echo of what was typed,
    // possibly already overtaken by the next keystroke, so it must not
    // refill the box.
    effect(() => {
      const q = this.q() ?? "";
      untracked(() => {
        if (this.writingQ === 0 && q !== this.filter.query()) {
          this.filter.query.set(q);
        }
      });
    });
    effect(() => {
      const typed = this.filter.query();
      untracked(() => {
        if (typed === (this.q() ?? "")) return;
        this.writingQ++;
        void this.router
          .navigate([], {
            queryParams: { q: typed || null },
            queryParamsHandling: "merge",
            replaceUrl: true,
          })
          .finally(() => this.writingQ--);
      });
    });

    // Select addressed item (:itemId or :runId) once store data loads.
    effect(() => {
      const loaded = this.store.loaded();
      const tab = this.activeTab();
      const item = this.itemId();
      const run = this.runId();
      const key = this.itemLinkKey(tab, item, run);
      untracked(() => {
        if (!loaded) return;
        if (!item && !run) {
          const prevTab = this.handledItemTab;
          this.handledItemLinkKey = "";
          this.handledItemTab = null;
          if (prevTab) {
            this.mobileDetailOpen.set(false);
            this.clearMissingItemError();
            if (prevTab === "schedules") {
              this.selectedScheduleId.set(null);
              this.selectedTrigger.set(null);
              this.scheduleComposition.set(null);
            } else if (prevTab === "templates") {
              this.selectedTemplateId.set(null);
              this.templateComposition.set(null);
            } else if (prevTab === "suites") {
              this.selectedSuiteRunId.set(null);
              this.selectedSuiteId.set(null);
              this.suiteComposition.set(null);
            }
          }
          return;
        }
        if (key === this.handledItemLinkKey) return;
        this.handledItemLinkKey = key;
        this.handledItemTab = tab;
        if (run) {
          this.suitesView.set("runs");
          const parsed = Number(run);
          if (
            Number.isFinite(parsed) &&
            parsed > 0 &&
            this.store.suiteRuns().some((r) => r.id === parsed)
          ) {
            this.selectSuiteRun(parsed, true, false);
          } else {
            this.setMissingItemError(`No suite run "${run}".`);
          }
          return;
        }
        if (!item) return;
        if (tab === "schedules") {
          if (this.store.schedules().some((s) => s.id === item)) {
            this.selectSchedule(item, true, false);
          } else {
            this.setMissingItemError(`No schedule "${item}".`);
          }
        } else if (tab === "templates") {
          if (this.store.templates().some((t) => t.id === item)) {
            this.selectTemplate(item, true, false);
          } else {
            this.setMissingItemError(`No template "${item}".`);
          }
        } else if (tab === "suites") {
          this.suitesView.set("suites");
          if (this.store.suites().some((s) => s.id === item)) {
            this.selectSuite(item, true, false);
          } else {
            this.setMissingItemError(`No suite "${item}".`);
          }
        }
      });
    });

    // Auto-select top item per tab on desktop when data loads or returns to a
    // bare tab route.
    effect(() => {
      const tab = this.activeTab();
      const loaded = this.store.loaded();
      const item = this.itemId();
      const run = this.runId();
      const scheds = this.sortedSchedules();
      const trigs = this.filteredTriggers();
      const tmpls = this.sortedTemplates();
      const suites = this.sortedSuites();
      const repos = this.filteredDeclRepos();
      untracked(() => {
        if (!loaded || item || run) return;
        const isDesktop = (this.doc.defaultView?.innerWidth ?? 1280) >= 600;
        if (!isDesktop) return;
        if (
          tab === "schedules" &&
          !this.selectedScheduleId() &&
          !this.selectedTrigger()
        ) {
          if (scheds[0]) {
            this.selectSchedule(scheds[0].id, false);
          } else if (trigs[0]) {
            this.selectTrigger(trigs[0], false);
          }
        } else if (
          tab === "templates" &&
          !this.selectedTemplateId() &&
          tmpls[0]
        ) {
          this.selectTemplate(tmpls[0].id, false);
        } else if (
          tab === "suites" &&
          !this.selectedSuiteId() &&
          this.selectedSuiteRunId() === null &&
          suites[0]
        ) {
          this.selectSuite(suites[0].id, false);
        } else if (
          tab === "declarations" &&
          !this.selectedDeclRepo() &&
          repos[0]
        ) {
          this.selectDeclRepo(repos[0], false);
        }
      });
    });

    // Open the repository ?repo= names on the declarations tab, on any
    // width: the link was followed for that repository, so on a phone it
    // opens its inspector rather than the list.
    effect(() => {
      const tab = this.activeTab();
      const repo = (this.repo() ?? "").trim();
      const loaded = this.store.loaded();
      untracked(() => {
        if (tab !== "declarations" || !repo || !loaded) return;
        const key = `${tab}:${repo}`;
        if (key === this.handledRepoLinkKey) return;
        this.handledRepoLinkKey = key;
        const known = this.store
          .knownRepos()
          .find((r) => r.toLowerCase() === repo.toLowerCase());
        this.selectDeclRepo(known ?? repo, true);
      });
    });

    // Handle deep-link query params from shell New menu:
    // ?new=1 | ?new=schedule, ?edit=1, ?launch=1 | ?launchTemplate=1, ?run=1 | ?runSuite=1
    effect(() => {
      const loaded = this.store.loaded();
      const newParam = this.new();
      const editParam = this.edit();
      const launchParam = this.launch() || this.launchTemplate();
      const runParam = this.run() || this.runSuite();
      const hasAction = Boolean(
        newParam || editParam || launchParam || runParam,
      );
      const key = `${loaded}:${newParam ?? ""}:${editParam ?? ""}:${launchParam ?? ""}:${runParam ?? ""}:${this.activeTab()}:${this.itemId() ?? ""}`;
      untracked(() => {
        if (!hasAction) {
          this.handledDeepLinkKey = "";
          return;
        }
        if (!loaded || key === this.handledDeepLinkKey) return;
        this.handledDeepLinkKey = key;
        if (newParam === "1" || newParam === "schedule") {
          if (this.access.can("POST /api/schedules")) {
            this.openScheduleDialog(null);
          }
        } else if (newParam === "template") {
          if (this.access.can("POST /api/templates")) {
            this.openTemplateDialog(null);
          }
        } else if (newParam === "suite") {
          if (this.access.can("POST /api/suites")) {
            this.suitesView.set("suites");
            this.openSuiteDialog(null);
          }
        } else if (editParam === "1") {
          const tab = this.activeTab();
          const id = this.itemId();
          if (tab === "suites") {
            const st =
              (id
                ? this.store.suites().find((s) => s.id === id)
                : this.selectedSuite()) ?? null;
            if (st && !st.declaredIn) {
              this.suitesView.set("suites");
              this.openSuiteDialog(st);
            }
          } else if (tab === "templates") {
            const t =
              (id
                ? this.store.templates().find((x) => x.id === id)
                : this.selectedTemplate()) ?? null;
            if (
              t &&
              !t.declaredIn &&
              !t.system &&
              this.access.can("PATCH /api/templates/{id}")
            ) {
              this.openTemplateDialog(t);
            }
          } else if (tab === "schedules") {
            const s =
              (id
                ? this.store.schedules().find((x) => x.id === id)
                : this.selectedSchedule()) ?? null;
            if (
              s &&
              !s.declaredIn &&
              this.access.can("PATCH /api/schedules/{id}")
            ) {
              this.openScheduleDialog(s);
            }
          }
        } else if (launchParam === "1") {
          if (this.canOpenLaunchTemplateDialog()) {
            this.openLaunchTemplateDialog(
              this.selectedTemplate() ?? this.store.templates()[0] ?? null,
            );
          }
        } else if (runParam === "1") {
          if (this.access.can("POST /api/suite-runs")) {
            this.openRunSuiteDialog(
              this.selectedSuite() ?? this.store.suites()[0] ?? null,
            );
          }
        }
        void this.router.navigate([], {
          queryParams: {
            new: null,
            edit: null,
            launch: null,
            launchTemplate: null,
            run: null,
            runSuite: null,
          },
          queryParamsHandling: "merge",
          replaceUrl: true,
        });
      });
    });

    // Load active QA flow versions once the store is loaded and the caller
    // holds global qa:read (GET /api/qa/flow-versions).
    effect(() => {
      const loaded = this.store.loaded();
      const canReadFlows =
        this.access.can("GET /api/qa/flow-versions") &&
        this.access.canGlobalScope("qa:read");
      untracked(() => {
        if (!loaded || !canReadFlows || this.requestedQaFlowVersions) return;
        this.requestedQaFlowVersions = true;
        void this.loadQaFlowOptions();
      });
    });

    // Keep the active section tab visible inside horizontal .tab-strip scroll
    // containers on phone viewports (e.g. /processes/quality at 390x844).
    effect(() => {
      this.activeTab();
      afterNextRender(
        () => {
          const active = this.hostEl.nativeElement.querySelector<HTMLElement>(
            '.tab-strip .tab-link[aria-current="page"]',
          );
          if (active && typeof active.scrollIntoView === "function") {
            active.scrollIntoView({ block: "nearest", inline: "nearest" });
          }
        },
        { injector: this.injector },
      );
    });
  }

  ngOnInit(): void {
    this.zone.runOutsideAngular(() => {
      this.doc.addEventListener("keydown", this.onCaptureKeydown, true);
    });
    this.store.start();
  }

  private async loadQaFlowOptions(): Promise<void> {
    try {
      const res = (await this.api.get<{
        versions?: {
          hash?: string;
          key?: string;
          version?: number;
          label?: string;
          builtIn?: boolean;
          retiredAt?: string | null;
          retired?: boolean;
        }[];
      }>("GET /api/qa/flow-versions", undefined, "limit=500")) as {
        versions?: {
          hash?: string;
          key?: string;
          version?: number;
          label?: string;
          builtIn?: boolean;
          retiredAt?: string | null;
          retired?: boolean;
        }[];
      } | null;
      const versions = res?.versions ?? [];
      const builtInKeys = new Set<string>();
      for (const v of versions) {
        if (v.builtIn === true && v.key) {
          builtInKeys.add(v.key);
        }
      }
      const items: {
        ref: string;
        key: string;
        version: number;
        label: string;
        hint?: string;
        benchmark: boolean;
      }[] = [];
      for (const v of versions) {
        if (v.retiredAt || v.retired || !v.key) continue;
        const ver = v.version ?? 1;
        const hash = (v.hash ?? "").trim();
        const isBuiltInV1Override =
          !v.builtIn && ver === 1 && builtInKeys.has(v.key) && hash !== "";
        const ref = isBuiltInV1Override ? hash : `${v.key}@${ver}`;
        const hint = isBuiltInV1Override ? `${v.key}@v1 (override)` : undefined;
        items.push({
          ref,
          key: v.key,
          version: ver,
          label: formatQaFlowLabel(v.key, v.label),
          hint,
          benchmark: isBenchmarkQaFlow(v.key, v.label),
        });
      }
      this.loadedQaFlowVersions.set(items);
      if (items.length > 0) {
        this.qaFlowVersionsLoaded.set(true);
      }
    } catch {
      // Built-in fallback options remain available.
    }
  }

  ngOnDestroy(): void {
    this.doc.removeEventListener("keydown", this.onCaptureKeydown, true);
    this.sp.detach();
    this.store.stop();
  }

  @HostListener("document:click", ["$event"])
  protected onDocClick(ev: Event): void {
    const t = ev.target as HTMLElement | null;
    if (this.openPicker() && !t?.closest(".pill-dropdown")) {
      this.openPicker.set(null);
    }
    if (this.suiteTmplOpen() && !t?.closest(".suite-tmpl-picker")) {
      this.suiteTmplOpen.set(false);
    }
  }

  protected togglePicker(name: string, ev: Event): void {
    ev.stopPropagation();
    this.openPicker.set(this.openPicker() === name ? null : name);
    // Opened from the keyboard (Enter or Space make a click with no
    // pointer, detail 0), the header New menu takes focus on its first
    // item, as a menu button does: before, focus stayed on New and the
    // arrow keys did nothing (docs/test-runs/2026-11-20-recurring-composed-ui.md,
    // bug 4).
    if (
      name === "newMenu" &&
      this.openPicker() === name &&
      (ev as MouseEvent).detail === 0
    ) {
      setTimeout(() => this.newMenuItems()[0]?.focus(), 0);
    }
  }

  private newMenuItems(): HTMLElement[] {
    return Array.from(
      this.hostEl.nativeElement.querySelectorAll<HTMLElement>(
        '[data-testid="proc-new-menu"] [role="menuitem"]:not([disabled])',
      ),
    );
  }

  // onNewMenuKeydown moves through the header New menu with the arrow
  // keys, Home and End. Escape is onCaptureKeydown's, which closes the
  // menu before this sees it and gives focus back to New.
  protected onNewMenuKeydown(ev: KeyboardEvent): void {
    const items = this.newMenuItems();
    if (!items.length) return;
    const at = items.indexOf(document.activeElement as HTMLElement);
    let next = -1;
    if (ev.key === "ArrowDown") next = (at + 1) % items.length;
    else if (ev.key === "ArrowUp")
      next = (at - 1 + items.length) % items.length;
    else if (ev.key === "Home") next = 0;
    else if (ev.key === "End") next = items.length - 1;
    if (next < 0) return;
    ev.preventDefault();
    items[next].focus();
  }

  // newProcess is the header New menu: it narrows the page to the kind
  // asked for and opens that kind's form over it. It goes through ?new=,
  // the shell New menu's own deep link, because leaving an item's URL
  // (or the bare /processes) makes a fresh page that would drop a form
  // opened on this one.
  protected newProcess(kind: "schedule" | "template" | "suite"): void {
    this.openPicker.set(null);
    const tab: ProcessTab =
      kind === "schedule"
        ? "schedules"
        : kind === "template"
          ? "templates"
          : "suites";
    void this.router.navigate(["/processes", tab], {
      queryParams: { new: kind },
      queryParamsHandling: "merge",
    });
  }

  protected setSort(raw: string): void {
    const sort: ProcessSort = parseProcessSort(raw);
    if (sort === this.activeSort()) return;
    void this.router.navigate([], {
      queryParams: { sort: sort === "custom" ? null : sort },
      queryParamsHandling: "merge",
    });
  }

  // countOf is "1 task", "2 tasks": a count with its noun.
  protected countOf(n: number, noun: string): string {
    return `${n} ${noun}${n === 1 ? "" : "s"}`;
  }

  protected stamp(at: string): string {
    return formatStamp(at);
  }

  private visibleKeys(list: OrderedList): string[] {
    switch (list) {
      case "schedules":
        return this.scheduleRows().map((r) => r.key);
      case "templates":
        return this.sortedTemplates().map((t) => `template:${t.id}`);
      case "suites":
        return this.sortedSuites().map((st) => `suite:${st.id}`);
    }
  }

  private moveRow(list: OrderedList, key: string, delta: number): boolean {
    const visible = this.visibleKeys(list);
    const next = movedOrder(this.customOrder(), visible, key, delta);
    if (!next) return false;
    this.customOrder.set(next);
    saveOrder(next);
    const at = next.indexOf(key) + 1;
    this.moveNote.set(`Moved to position ${at} of ${visible.length}.`);
    return true;
  }

  // Alt+ArrowUp / Alt+ArrowDown move the focused row one place in the
  // custom order, keeping focus on it.
  protected onRowKey(ev: KeyboardEvent, list: OrderedList, key: string): void {
    if (!ev.altKey || (ev.key !== "ArrowUp" && ev.key !== "ArrowDown")) return;
    if (this.activeSort() !== "custom") return;
    ev.preventDefault();
    const btn = ev.currentTarget as HTMLElement | null;
    if (this.moveRow(list, key, ev.key === "ArrowUp" ? -1 : 1)) {
      afterNextRender(() => btn?.focus(), { injector: this.injector });
    }
  }

  protected onDragStart(ev: DragEvent, key: string): void {
    if (this.activeSort() !== "custom") return;
    this.dragKey = key;
    ev.dataTransfer?.setData("text/plain", key);
    if (ev.dataTransfer) ev.dataTransfer.effectAllowed = "move";
  }

  protected onDragOver(ev: DragEvent, key: string): void {
    if (!this.dragKey || this.dragKey === key) return;
    ev.preventDefault();
    if (ev.dataTransfer) ev.dataTransfer.dropEffect = "move";
    this.dropKey.set(key);
  }

  protected onDragLeave(key: string): void {
    if (this.dropKey() === key) this.dropKey.set(null);
  }

  protected onDrop(ev: DragEvent, list: OrderedList, key: string): void {
    const from = this.dragKey;
    this.onDragEnd();
    if (!from || from === key) return;
    ev.preventDefault();
    const visible = this.visibleKeys(list);
    const delta = visible.indexOf(key) - visible.indexOf(from);
    this.moveRow(list, from, delta);
  }

  protected onDragEnd(): void {
    this.dragKey = null;
    this.dropKey.set(null);
  }

  protected tabCount(tab: ProcessTab): number {
    switch (tab) {
      case "schedules":
        return this.store.schedules().length + this.store.triggers().length;
      case "templates":
        return this.store.templates().length;
      case "suites":
        return this.store.suites().length;
      case "declarations":
        return (
          this.store.awaitingAcceptancesCount() ||
          this.store.knownRepos().length
        );
      case "quality": {
        if (this.qaFlowVersionsLoaded()) {
          const primaryKeys = new Set(
            this.loadedQaFlowVersions()
              .filter((v) => !v.benchmark)
              .map((v) => v.key),
          );
          for (const f of this.store.qaFlows()) {
            if (f.id && !isBenchmarkQaFlow(f.id, f.label)) {
              primaryKeys.add(f.id);
            }
          }
          if (primaryKeys.size > 0) return primaryKeys.size;
        }
        return this.store
          .qaFlows()
          .filter((f) => f.id && !isBenchmarkQaFlow(f.id, f.label)).length;
      }
    }
  }

  protected schedBadge = scheduleStatusBadge;
  protected triggerName = triggerName;
  protected schedNextRunOf = formatScheduleNextRun;
  protected tmplBadge = templateSourceBadge;
  protected cadenceOf = formatCadence;
  protected isTrigger = isEventTrigger;
  protected formatSuppression = formatScheduleSuppression;
  protected skipFolded = skipFolded;
  protected suiteModeOf = describeSuiteMode;
  protected runBadgeOf = suiteRunBadge;

  protected schedTargetOf(s: Schedule): { repo: string; base: string } {
    return scheduleFiringTarget(
      s,
      this.store.templates(),
      this.selectedScheduleId() === s.id ? this.scheduleComposition() : null,
    );
  }

  protected schedModelOf(s: Schedule): string {
    if (s.model) return s.model;
    if (!s.templateId) return "";
    return (
      this.store.templates().find((t) => t.id === s.templateId)?.model || ""
    );
  }

  protected schedAdmissionOf(s: Schedule): string {
    const tmpl = s.templateId
      ? this.store.templates().find((t) => t.id === s.templateId)
      : undefined;
    const raw = s.admission || tmpl?.admission || "standard";
    return ADMISSION_OPTIONS.find((o) => o.value === raw)?.label ?? raw;
  }

  protected schedHostingOf(s: Schedule): string {
    const tmpl = s.templateId
      ? this.store.templates().find((t) => t.id === s.templateId)
      : undefined;
    const raw = s.hosting || tmpl?.hosting || "unattended";
    return HOSTING_OPTIONS.find((o) => o.value === raw)?.label ?? raw;
  }

  protected schedKeepRunningOf(s: Schedule): boolean {
    if (s.keepRunning !== undefined) return !!s.keepRunning;
    if (!s.templateId) return false;
    return !!this.store.templates().find((t) => t.id === s.templateId)
      ?.keepRunning;
  }

  protected networkLabelOf(raw: string | undefined): string {
    if (!raw) return "";
    return NETWORK_ACCESS_OPTIONS.find((o) => o.value === raw)?.label ?? raw;
  }

  protected formatWrites(writes: readonly WriteTarget[] | undefined): string {
    return (writes ?? [])
      .map((w) => (w.base ? `${w.repo} @ ${w.base}` : w.repo))
      .join(", ");
  }

  protected repoAdvancedRoute(repo: string): string[] {
    const parts = repo.split("/").filter(Boolean);
    return ["/repos", ...parts, "advanced"];
  }

  protected templateNameById(id: string): string {
    const t = this.store.templates().find((x) => x.id === id);
    return t ? t.name || t.title : "";
  }

  protected suiteNameById(id: string): string {
    return this.store.suites().find((x) => x.id === id)?.name ?? "";
  }

  private itemLinkKey(
    tab: ProcessTab,
    itemId?: string,
    runId?: string,
  ): string {
    return `${this.store.loaded()}:${tab}:${itemId ?? ""}:${runId ?? ""}`;
  }

  private preserveQueryExtras() {
    return this.writingQ > 0
      ? {
          queryParams: { q: this.filter.query() || null },
          queryParamsHandling: "merge" as const,
        }
      : { queryParamsHandling: "preserve" as const };
  }

  private setMissingItemError(msg: string): void {
    this.missingItemError = msg;
    this.store.actionError.set(msg);
  }

  private clearMissingItemError(): void {
    if (
      this.missingItemError &&
      this.store.actionError() === this.missingItemError
    ) {
      this.store.actionError.set(null);
    }
    this.missingItemError = null;
  }

  // Push a history entry for an explicit row or item selection so reload,
  // copy/paste, and Back/Forward land on that item while keeping ?q=, ?sort=,
  // and ?kind=. Auto-selection on arrival passes fromClick: false and leaves
  // the URL alone so Back out of Processes or across ?sort= stays predictable.
  private syncItemUrl(
    tab: "schedules" | "templates" | "suites",
    itemId?: string,
    runId?: number,
  ): void {
    if (runId !== undefined) {
      const runStr = String(runId);
      const targetKey = this.itemLinkKey("suites", undefined, runStr);
      if (this.runId() === runStr && this.handledItemLinkKey === targetKey) {
        return;
      }
      this.handledItemLinkKey = targetKey;
      this.handledItemTab = "suites";
      void this.router.navigate(
        ["/processes", "suites", "runs", runStr],
        this.preserveQueryExtras(),
      );
      return;
    }
    if (itemId !== undefined) {
      const targetKey = this.itemLinkKey(tab, itemId);
      if (
        this.tab() === tab &&
        this.itemId() === itemId &&
        !this.runId() &&
        this.handledItemLinkKey === targetKey
      ) {
        return;
      }
      this.handledItemLinkKey = targetKey;
      this.handledItemTab = tab;
      void this.router.navigate(
        ["/processes", tab, itemId],
        this.preserveQueryExtras(),
      );
    }
  }

  protected closeMobileDetail(): void {
    this.mobileDetailOpen.set(false);
    if (this.itemId() || this.runId()) {
      void this.router.navigate(
        ["/processes", this.activeTab()],
        this.preserveQueryExtras(),
      );
    }
  }

  protected selectSchedule(
    id: string,
    fromClick: boolean,
    syncUrl = fromClick,
  ): void {
    this.selectedTrigger.set(null);
    this.selectedScheduleId.set(id);
    if (fromClick) {
      this.mobileDetailOpen.set(true);
      this.clearMissingItemError();
    }
    if (syncUrl) this.syncItemUrl("schedules", id);
    this.scheduleComposition.set(null);
    if (this.access.can("GET /api/schedules/{id}/composition")) {
      void this.store
        .fetchScheduleComposition(id)
        .then((c) => {
          if (this.selectedScheduleId() === id) this.scheduleComposition.set(c);
        })
        .catch(() => undefined);
    }
  }

  protected selectTrigger(tr: DeclaredTrigger, fromClick: boolean): void {
    this.selectedScheduleId.set(null);
    this.scheduleComposition.set(null);
    this.triggerYamlText.set("");
    this.triggerYamlLoading.set(false);
    this.selectedTrigger.set(tr);
    if (fromClick) {
      this.mobileDetailOpen.set(true);
      this.clearMissingItemError();
      if (this.itemId()) {
        this.handledItemLinkKey = "";
        this.handledItemTab = null;
        void this.router.navigate(
          ["/processes", "schedules"],
          this.preserveQueryExtras(),
        );
      }
    }
    if (!tr.detail && !this.store.repoDefaults()[tr.repo]?.declarationsStatus) {
      void this.store.loadRepoInspect(tr.repo);
    }
  }

  protected openTriggerTemplate(id: string): void {
    this.tmplFilter.set("all");
    this.selectTemplate(id, true, false);
    this.handledItemLinkKey = this.itemLinkKey("templates", id);
    this.handledItemTab = "templates";
    void this.router.navigate(["/processes", "templates", id], {
      state: { selectTemplateId: id },
    });
  }

  protected selectTemplate(
    id: string,
    fromClick: boolean,
    syncUrl = fromClick,
  ): void {
    this.selectedTemplateId.set(id);
    if (fromClick) {
      this.mobileDetailOpen.set(true);
      this.clearMissingItemError();
    }
    if (syncUrl) this.syncItemUrl("templates", id);
    this.templateComposition.set(null);
    const tmpl = this.store.templates().find((t) => t.id === id);
    if (tmpl && this.access.can("GET /api/templates/{id}/composition")) {
      const defaults: Record<string, string> = {};
      // A preview is the firing's own answer: send what a filing would,
      // and a {{name}} placeholder only for a required field with nothing
      // to fall back on (the server refuses a missing required value).
      for (const f of tmpl.fields ?? []) {
        if (f.default || f.options?.length) {
          defaults[f.name] = f.default || f.options![0];
        } else if (f.required) {
          defaults[f.name] = `{{${f.name}}}`;
        }
      }
      void this.store
        .fetchTemplateComposition(
          id,
          tmpl.repo || "",
          tmpl.base || "",
          true,
          defaults,
        )
        .then((c) => {
          if (this.selectedTemplateId() === id) this.templateComposition.set(c);
        })
        .catch(() => undefined);
    }
  }

  protected selectSuite(
    id: string,
    fromClick: boolean,
    syncUrl = fromClick,
  ): void {
    this.selectedSuiteRunId.set(null);
    this.selectedSuiteId.set(id);
    if (fromClick) {
      this.suitesView.set("suites");
      this.mobileDetailOpen.set(true);
      this.clearMissingItemError();
    }
    if (syncUrl) this.syncItemUrl("suites", id);
    this.suiteComposition.set(null);
    if (this.access.can("GET /api/suites/{id}/composition")) {
      void this.store
        .fetchSuiteComposition(id)
        .then((c) => {
          if (this.selectedSuiteId() === id) this.suiteComposition.set(c);
        })
        .catch(() => undefined);
    }
  }

  protected selectSuiteRun(
    id: number,
    fromClick: boolean,
    syncUrl = fromClick,
  ): void {
    this.selectedSuiteRunId.set(id);
    if (fromClick) {
      this.suitesView.set("runs");
      this.mobileDetailOpen.set(true);
      this.clearMissingItemError();
    }
    if (syncUrl) this.syncItemUrl("suites", undefined, id);
    if (this.access.can("GET /api/suite-runs/{id}")) {
      void this.store.fetchSuiteRunDetail(id).catch(() => undefined);
    }
  }

  protected selectDeclRepo(repo: string, fromClick: boolean): void {
    this.selectedDeclRepo.set(repo);
    this.inlineYamlSeq++;
    this.inlineYamlKey.set(null);
    this.inlineYamlText.set("");
    this.inlineYamlLoading.set(false);
    if (fromClick) this.mobileDetailOpen.set(true);
    void this.store.loadRepoInspect(repo);
  }

  protected discoveredFiles(
    status:
      | {
          templates?: string[];
          suites?: string[];
          schedules?: string[];
          triggers?: string[];
          mergeRules?: string;
        }
      | null
      | undefined,
  ): { path: string; kind: string }[] {
    if (!status) return [];
    const out: { path: string; kind: string }[] = [];
    for (const p of status.templates ?? []) {
      out.push({ path: p, kind: "template" });
    }
    for (const p of status.suites ?? []) {
      out.push({ path: p, kind: "suite" });
    }
    for (const p of status.schedules ?? []) {
      out.push({ path: p, kind: "schedule" });
    }
    for (const p of status.triggers ?? []) {
      out.push({ path: p, kind: "trigger" });
    }
    if (status.mergeRules) {
      out.push({ path: status.mergeRules, kind: "merge-rules" });
    }
    return out;
  }

  protected isInlineYamlOpen(repo: string, path: string): boolean {
    return this.inlineYamlKey() === `${repo}:${path}`;
  }

  protected async toggleInlineYaml(
    repo: string,
    filePath: string,
    rawText?: string,
  ): Promise<void> {
    const key = `${repo}:${filePath}`;
    const seq = ++this.inlineYamlSeq;
    if (this.inlineYamlKey() === key) {
      this.inlineYamlKey.set(null);
      this.inlineYamlText.set("");
      this.inlineYamlLoading.set(false);
      return;
    }
    this.inlineYamlKey.set(key);
    if (rawText !== undefined && rawText.length > 0) {
      this.inlineYamlText.set(rawText);
      this.inlineYamlLoading.set(false);
      return;
    }
    const hasDecl = Boolean(
      this.store.repoDefaults()[repo]?.declarationsStatus,
    );
    const hasWfs = Boolean(this.store.repoWorkflows()[repo]);
    if (!hasDecl || !hasWfs) {
      this.inlineYamlText.set("");
      this.inlineYamlLoading.set(true);
      await this.store.loadRepoInspect(repo);
      if (this.inlineYamlSeq !== seq || this.inlineYamlKey() !== key) {
        return;
      }
    }
    this.inlineYamlText.set(this.resolveDeclaredFileYaml(repo, filePath));
    this.inlineYamlLoading.set(false);
  }

  private resolveDeclaredFileYaml(repo: string, filePath: string): string {
    const wfs = this.store.repoWorkflows();
    const wf = wfs[repo]?.workflows.find((w) => w.path === filePath);
    if (wf?.text) return wf.text;

    const status = this.store.repoDefaults()[repo]?.declarationsStatus;
    const trig = status?.triggerDetails?.find((d) => d.path === filePath);
    if (trig || (status?.triggers ?? []).includes(filePath)) {
      return synthesizeTriggerYaml(
        repo,
        filePath,
        trig,
        this.store.templates(),
      );
    }

    const tmpl = this.store
      .templates()
      .find(
        (t) => t.declaredIn?.repo === repo && t.declaredIn.path === filePath,
      );
    if (tmpl) {
      return synthesizeDeclaredTemplateYaml(repo, filePath, tmpl);
    }

    const suite = this.store
      .suites()
      .find(
        (s) => s.declaredIn?.repo === repo && s.declaredIn.path === filePath,
      );
    if (suite) {
      const lines = [
        `# ${repo}:${filePath}`,
        `name: ${suite.name}`,
        `mode: ${suite.mode}`,
      ];
      if (suite.maxPasses) lines.push(`maxPasses: ${suite.maxPasses}`);
      if (suite.items.length) {
        lines.push("items:");
        for (const it of suite.items) {
          lines.push(`  - template: ${it.templateName || it.templateId}`);
        }
      }
      return lines.join("\n");
    }

    const sched = this.store
      .schedules()
      .find(
        (s) => s.declaredIn?.repo === repo && s.declaredIn.path === filePath,
      );
    if (sched) {
      const lines = [
        `# ${repo}:${filePath}`,
        `title: ${sched.title}`,
        `cadence: ${formatCadence(sched.recurrence, sched.timeZone)}`,
      ];
      if (sched.overlap) lines.push(`overlap: ${sched.overlap}`);
      return lines.join("\n");
    }

    return `# ${repo}:${filePath}\n# Synced repository declaration.`;
  }

  protected scheduledTasksFor(s: Schedule) {
    return this.taskFeed
      .tasks()
      .filter(
        (t) =>
          t.scheduled &&
          (!s.repo || t.repo === s.repo) &&
          (t.title === s.title ||
            (s.templateName && t.title === s.templateName)),
      )
      .slice(0, 6);
  }

  protected runsOfSuite(suiteId: string): SuiteRun[] {
    return this.store.suiteRuns().filter((r) => r.suiteId === suiteId);
  }

  protected passesForRun(run: SuiteRun) {
    return buildSuiteRunPasses(run, this.taskFeed.tasks());
  }

  protected hasOpenSuiteTasks(run: SuiteRun): boolean {
    const byId = new Map(this.taskFeed.tasks().map((t) => [t.id, t]));
    return (run.tasks ?? []).some((rt) => {
      const st = (byId.get(rt.taskId)?.state || rt.state || "").toLowerCase();
      return Boolean(rt.taskId) && st !== "closed" && st !== "merged";
    });
  }

  protected repoPendingAcceptances(repo: string): number {
    return this.store.acceptances().filter((a) => a.awaiting && a.repo === repo)
      .length;
  }

  protected acceptancesForRepo(repo: string): DeclarationAcceptanceView[] {
    return this.store.acceptances().filter((a) => !a.repo || a.repo === repo);
  }

  protected canAcceptDeclaration(acc: DeclarationAcceptanceView): boolean {
    if (!this.access.can("POST /api/declarations/accept")) return false;
    return (acc.needsScopes ?? []).every((sc) =>
      this.access.canGlobalScope(sc),
    );
  }

  protected acceptDeniedReason(acc: DeclarationAcceptanceView): string {
    if (!this.access.can("POST /api/declarations/accept")) {
      return this.access.deniedReason("POST /api/declarations/accept");
    }
    const missing = (acc.needsScopes ?? []).filter(
      (sc) => !this.access.canGlobalScope(sc),
    );
    if (missing.length === 0) return "";
    return `Accepting this declaration requires global scope(s): ${missing.join(", ")}.`;
  }

  protected canOpenLaunchTemplateDialog(): boolean {
    if (this.access.can("POST /api/tasks")) return true;
    return (
      this.store.templates().some((t) => !!t.declaredIn) &&
      this.access.can("POST /api/tasks/from-template")
    );
  }

  protected canSubmitLaunchTemplate(): boolean {
    const lt = this.launchSelectedTemplate();
    if (!lt) return false;
    if (lt.declaredIn?.repo) {
      return (
        this.access.can("POST /api/tasks/from-template", lt.declaredIn.repo) ||
        this.access.can("POST /api/tasks", lt.declaredIn.repo)
      );
    }
    if (lt.system) {
      return this.access.can("POST /api/tasks");
    }
    const targetRepo = (lt.repo || this.launchRepo()).trim();
    if (!targetRepo) return false;
    return this.access.can("POST /api/tasks", targetRepo);
  }

  protected readonly canEditWorkflow = computed(
    () =>
      !!this.wfEditorRepo() &&
      this.access.can(
        "PUT /api/repos/{owner}/{name}/workflows",
        this.wfEditorRepo(),
      ),
  );

  // --- Schedule actions & dialog ---

  protected closeScheduleDialog(): void {
    this.openPicker.set(null);
    this.dialogError.set(null);
    this.schedDialogOpen.set(false);
  }

  // The dialogs' unsaved-edit guard, as the classic panes have it
  // (useUnsavedEdits): each dialog's draft is snapshotted when it opens,
  // and Cancel, Escape, the scrim, Back and the rail ask before a draft
  // that differs is dropped -- once, however many of them fire for one
  // gesture. Save closes through close*Dialog / .set(false) and does not ask.
  private schedBaseline = "";
  private tmplBaseline = "";
  private suiteBaseline = "";

  private schedDraftSnapshot(): string {
    return JSON.stringify([
      this.schedTargetKind(),
      this.sDraftTitle(),
      this.sDraftDesc(),
      this.sDraftKeepRunning(),
      this.sDraftAdmission(),
      this.sDraftHosting(),
      this.sDraftCaps(),
      this.sDraftNetwork(),
      this.sDraftReads(),
      this.sDraftWrites(),
      this.sDraftCustomFields(),
      this.buildScheduleCreatePayload(),
    ]);
  }

  private tmplDraftSnapshot(): string {
    return JSON.stringify([
      this.tDraftName(),
      this.tDraftTitle(),
      this.tDraftDesc(),
      this.tDraftRepo(),
      this.tDraftFields(),
      this.tDraftParamsCsv(),
      this.tDraftAutoMerge(),
      this.tDraftModel(),
      this.tDraftKeepRunning(),
      this.tDraftPriority(),
      this.tDraftInteractive(),
      this.tDraftAdmission(),
      this.tDraftHosting(),
      this.tDraftCaps(),
      this.tDraftQaFlowRef(),
    ]);
  }

  private suiteDraftSnapshot(): string {
    return JSON.stringify([
      this.stDraftMode(),
      this.stDraftCount(),
      this.stDraftMaxPasses(),
      this.buildSuiteCreatePayload(),
      this.stDraftQaFlowRef(),
    ]);
  }

  // unsavedDialogs names the open dialogs whose draft differs from what
  // they opened with, topmost last.
  private unsavedDialogs(): string[] {
    const out: string[] = [];
    if (
      this.schedDialogOpen() &&
      this.schedDraftSnapshot() !== this.schedBaseline
    ) {
      out.push("schedule");
    }
    if (
      this.suiteDialogOpen() &&
      this.suiteDraftSnapshot() !== this.suiteBaseline
    ) {
      out.push("suite");
    }
    if (
      this.tmplDialogOpen() &&
      this.tmplDraftSnapshot() !== this.tmplBaseline
    ) {
      out.push("template");
    }
    return out;
  }

  private askDiscard(what: string): Promise<boolean> {
    if (this.discardDraftResolve) return Promise.resolve(false);
    return new Promise<boolean>((resolve) => {
      this.discardDraftResolve = resolve;
      this.confirming.set({
        title: "Discard unsaved changes?",
        message: `You have unsaved changes to this ${what}. Leave and discard them?`,
        accept: "Discard",
        destructive: true,
        run: async () => undefined,
      });
    });
  }

  protected async dismissScheduleDialog(): Promise<void> {
    if (
      this.schedDraftSnapshot() !== this.schedBaseline &&
      !(await this.askDiscard("schedule"))
    ) {
      return;
    }
    this.closeScheduleDialog();
  }

  protected async dismissTemplateDialog(): Promise<void> {
    if (
      this.tmplDraftSnapshot() !== this.tmplBaseline &&
      !(await this.askDiscard("template"))
    ) {
      return;
    }
    this.dialogError.set(null);
    this.creatingTemplateForSuite.set(false);
    this.tmplDialogOpen.set(false);
  }

  protected async dismissSuiteDialog(): Promise<void> {
    if (
      this.suiteDraftSnapshot() !== this.suiteBaseline &&
      !(await this.askDiscard("suite"))
    ) {
      return;
    }
    this.suiteTmplOpen.set(false);
    this.dialogError.set(null);
    this.suiteDialogOpen.set(false);
  }

  protected closeLaunchDialog(): void {
    this.openPicker.set(null);
    this.dialogError.set(null);
    this.launchDialogOpen.set(false);
  }

  protected closeRunSuiteDialog(): void {
    this.openPicker.set(null);
    this.dialogError.set(null);
    this.runSuiteDialogOpen.set(false);
  }

  protected runScheduleNow(s: Schedule): void {
    if (s.type === "workflow") {
      this.confirming.set({
        title: "Run workflow schedule now?",
        message:
          `Run "${s.title || s.id}" now? It starts a CI run of the workflow it was declared in straight away, ` +
          `to be reviewed when it finishes; the schedule's own next run is unchanged.`,
        accept: "Run now",
        destructive: false,
        run: () => this.executeRunScheduleNow(s),
      });
      return;
    }
    void this.executeRunScheduleNow(s);
  }

  private async executeRunScheduleNow(s: Schedule): Promise<void> {
    try {
      const res = await this.store.runScheduleNow(s.id);
      if (res.taskId) {
        this.snackbar.show(`Triggered schedule — filed task #${res.taskId}.`, {
          label: "Open task",
          run: () => void this.router.navigate(["/task", res.taskId]),
        });
      } else if (res.suiteRunId) {
        this.snackbar.show(
          `Triggered schedule — started suite run #${res.suiteRunId}.`,
        );
      } else if (res.workflowRun && !res.taskId && !res.suiteRunId) {
        this.snackbar.show(
          "No concluded GitHub workflow run matched this trigger yet.",
        );
      } else {
        this.snackbar.show("Schedule triggered.");
      }
    } catch (err) {
      this.store.actionError.set(errorText(err));
    }
  }

  protected async toggleScheduleEnabled(s: Schedule): Promise<void> {
    try {
      const updated = await this.store.updateSchedule(s.id, {
        enabled: !s.enabled,
        revision: s.revision,
      });
      this.snackbar.show(
        updated.enabled ? "Schedule resumed." : "Schedule paused.",
      );
    } catch (err) {
      this.store.actionError.set(errorText(err));
    }
  }

  protected confirmDeleteSchedule(s: Schedule): void {
    this.confirming.set({
      title: "Delete schedule?",
      message: `Remove schedule "${s.title || s.id}" permanently?`,
      accept: "Delete schedule",
      destructive: true,
      run: async () => {
        await this.store.deleteSchedule(s.id);
        if (this.selectedScheduleId() === s.id) {
          this.selectedScheduleId.set(null);
          this.scheduleComposition.set(null);
        }
        if (this.activeTab() === "schedules" && this.itemId() === s.id) {
          void this.router.navigate(["/processes", "schedules"], {
            ...this.preserveQueryExtras(),
            replaceUrl: true,
          });
        }
        this.snackbar.show("Schedule deleted.");
      },
    });
  }

  protected setSchedTargetKind(k: "prompt" | "template" | "suite"): void {
    const editing = this.editingSchedule();
    if (editing) {
      if (editing.system) return;
      if (editing.suiteId && k !== "suite") return;
      if (!editing.suiteId && k === "suite") return;
    }
    this.schedTargetKind.set(k);
    if (k === "template" && !this.sDraftTemplateId()) {
      this.sDraftTemplateId.set(this.schedulableTemplates()[0]?.id ?? "");
    }
    if (k === "suite" && !this.sDraftSuiteId()) {
      this.sDraftSuiteId.set(this.store.suites()[0]?.id ?? "");
    }
  }

  protected openScheduleDialog(existing: Schedule | null): void {
    this.openPicker.set(null);
    this.dialogError.set(null);
    this.editingSchedule.set(existing);
    this.draftSchedComposition.set(null);
    if (existing) {
      this.schedTargetKind.set(
        existing.suiteId
          ? "suite"
          : existing.templateId
            ? "template"
            : "prompt",
      );
      this.sDraftTitle.set(existing.title || "");
      this.sDraftDesc.set(existing.description || "");
      this.sDraftTemplateId.set(existing.templateId || "");
      this.sDraftSuiteId.set(existing.suiteId || "");
      const target = scheduleFiringTarget(existing, this.store.templates());
      this.sDraftRepo.set(target.repo || existing.repo || "");
      this.sDraftBase.set(target.base);
      this.sDraftKind.set(existing.recurrence?.kind || "daily");
      this.sDraftEveryMin.set(existing.recurrence?.everyNMinutes || 15);
      this.sDraftEveryHr.set(existing.recurrence?.everyNHours || 6);
      this.sDraftTime.set(existing.recurrence?.timeOfDay || "09:00");
      this.sDraftWeekday.set(existing.recurrence?.weekday || "monday");
      this.sDraftDayOfMonth.set(existing.recurrence?.dayOfMonth || 1);
      this.sDraftTz.set(
        existing.timeZone || existing.recurrence?.timeZone || "UTC",
      );
      this.sDraftTriggerWorkflow.set(
        existing.recurrence?.githubWorkflowRun?.workflow ?? "",
      );
      this.sDraftTriggerConclusion.set(
        (existing.recurrence?.githubWorkflowRun?.conclusions ?? []).join(", "),
      );
      this.sDraftTriggerBranch.set(
        (existing.recurrence?.githubWorkflowRun?.branches ?? []).join(", "),
      );
      this.sDraftGitRefRepo.set(existing.recurrence?.gitRef?.repo ?? "");
      this.sDraftGitRef.set(
        existing.recurrence?.gitRef?.ref ?? "refs/heads/main",
      );
      this.sDraftOverlap.set(existing.overlap || "skip");
      this.sDraftModel.set(existing.model || "");
      this.sDraftKeepRunning.set(!!existing.keepRunning);
      this.sDraftAdmission.set(existing.admission || "standard");
      this.sDraftHosting.set(existing.hosting || "unattended");
      this.sDraftCaps.set([...(existing.capabilities ?? [])]);
      this.sDraftNetwork.set(existing.networkAccess || "");
      this.sDraftReads.set([...(existing.reads ?? [])]);
      this.sDraftWrites.set(
        (existing.writes ?? []).map((w) => ({
          repo: w.repo,
          ...(w.base ? { base: w.base } : {}),
        })),
      );
      this.sDraftCustomFields.set(
        initialCustomFields(this.declaredCustomFields(), existing.customFields),
      );
      this.sDraftQaFlowRef.set(existing.qaFlowRef || "");
    } else {
      this.schedTargetKind.set("prompt");
      this.sDraftTitle.set("");
      this.sDraftDesc.set("");
      this.sDraftTemplateId.set("");
      this.sDraftSuiteId.set("");
      this.sDraftRepo.set(this.store.knownRepos()[0] || "");
      this.sDraftBase.set("main");
      this.sDraftKind.set("daily");
      this.sDraftEveryMin.set(15);
      this.sDraftEveryHr.set(6);
      this.sDraftTime.set("09:00");
      this.sDraftWeekday.set("monday");
      this.sDraftDayOfMonth.set(1);
      this.sDraftTz.set("UTC");
      this.sDraftTriggerWorkflow.set("");
      this.sDraftTriggerConclusion.set("failure");
      this.sDraftTriggerBranch.set("");
      this.sDraftGitRefRepo.set("");
      this.sDraftGitRef.set("refs/heads/main");
      this.sDraftOverlap.set("skip");
      this.sDraftModel.set("");
      this.sDraftKeepRunning.set(false);
      this.sDraftAdmission.set("standard");
      this.sDraftHosting.set("unattended");
      this.sDraftCaps.set([]);
      this.sDraftNetwork.set("");
      this.sDraftReads.set([]);
      this.sDraftWrites.set([]);
      this.sDraftCustomFields.set(
        initialCustomFields(this.declaredCustomFields(), undefined),
      );
      this.sDraftQaFlowRef.set("");
    }
    this.sDraftReadInput.set("");
    this.sDraftWriteRepo.set("");
    this.sDraftWriteBase.set("");
    this.sDraftCustomInput.set({});
    void this.store.ensureReachableRepos();
    this.schedDialogOpen.set(true);
    this.schedBaseline = this.schedDraftSnapshot();
  }

  protected addSchedRead(repo: string): void {
    const r = repo.trim();
    if (!r) return;
    if (r.toLowerCase() === this.sDraftRepo().trim().toLowerCase()) return;
    this.sDraftReads.update((list) =>
      list.some((x) => x.toLowerCase() === r.toLowerCase())
        ? list
        : [...list, r],
    );
  }

  protected addSchedReadFromInput(): void {
    const r = this.sDraftReadInput().trim();
    if (!r) return;
    this.addSchedRead(r);
    this.sDraftReadInput.set("");
  }

  protected removeSchedRead(repo: string): void {
    this.sDraftReads.update((list) => list.filter((x) => x !== repo));
  }

  protected addSchedWrite(): void {
    const repo = this.sDraftWriteRepo().trim();
    if (!repo) return;
    const base = this.sDraftWriteBase().trim();
    this.sDraftWrites.update((list) => {
      const idx = list.findIndex(
        (w) =>
          w.repo.toLowerCase() === repo.toLowerCase() &&
          (w.base ?? "").trim() === base,
      );
      const item: WriteTarget = base ? { repo, base } : { repo };
      if (idx >= 0) {
        const copy = [...list];
        copy[idx] = item;
        return copy;
      }
      return [...list, item];
    });
    this.sDraftWriteRepo.set("");
    this.sDraftWriteBase.set("");
  }

  protected removeSchedWrite(idx: number): void {
    this.sDraftWrites.update((list) => list.filter((_, i) => i !== idx));
  }

  protected schedCustomFieldScalar(name: string): string {
    const v = this.sDraftCustomFields()[name];
    if (Array.isArray(v)) return v[0] ?? "";
    return v ?? "";
  }

  protected schedCustomFieldList(name: string): string[] {
    return customFieldValueList(this.sDraftCustomFields()[name]);
  }

  protected setSchedCustomField(name: string, val: string): void {
    this.sDraftCustomFields.update((m) => ({ ...m, [name]: val }));
  }

  protected setSchedCustomInput(name: string, val: string): void {
    this.sDraftCustomInput.update((m) => ({ ...m, [name]: val }));
  }

  protected addSchedCustomFieldItem(name: string, val: string): void {
    const v = val.trim();
    if (!v) return;
    this.sDraftCustomFields.update((m) => {
      const cur = customFieldValueList(m[name]);
      return cur.includes(v) ? m : { ...m, [name]: [...cur, v] };
    });
  }

  protected commitSchedCustomInput(name: string): void {
    const raw = (this.sDraftCustomInput()[name] || "").trim();
    if (!raw) return;
    this.addSchedCustomFieldItem(name, raw);
    this.sDraftCustomInput.update((m) => ({ ...m, [name]: "" }));
  }

  protected removeSchedCustomFieldItem(name: string, val: string): void {
    this.sDraftCustomFields.update((m) => ({
      ...m,
      [name]: customFieldValueList(m[name]).filter((x) => x !== val),
    }));
  }

  protected customEnumOptions(cf: CustomFieldDeclaration): PillOption[] {
    const opts: PillOption[] = [{ value: "", label: "Not set" }];
    const seen = new Set<string>([""]);
    for (const o of cf.options ?? []) {
      if (!seen.has(o)) {
        seen.add(o);
        opts.push({ value: o, label: o });
      }
    }
    const cur = this.schedCustomFieldScalar(cf.name);
    if (cur && !seen.has(cur)) {
      opts.push({ value: cur, label: `${cur} (saved)` });
    }
    return opts;
  }

  protected customMultiEnumOptions(cf: CustomFieldDeclaration): PillOption[] {
    const chosen = new Set(this.schedCustomFieldList(cf.name));
    return [
      { value: "", label: "Add value…" },
      ...(cf.options ?? [])
        .filter((o) => !chosen.has(o))
        .map((o) => ({ value: o, label: o })),
    ];
  }

  protected canSaveSchedule(): boolean {
    const editing = this.editingSchedule();
    const route = editing ? "PATCH /api/schedules/{id}" : "POST /api/schedules";
    if (editing?.system) {
      return this.access.can(route, editing.repo || undefined);
    }
    const tk = this.schedTargetKind();
    const bound = this.schedBoundTemplate();
    const targetRepo =
      bound?.repo?.trim() || this.sDraftRepo().trim() || editing?.repo || "";
    if (!this.access.can(route, targetRepo || undefined)) return false;
    if (tk === "suite") {
      return Boolean(
        this.sDraftSuiteId() &&
        this.sDraftRepo().trim() &&
        this.sDraftBase().trim(),
      );
    }
    if (tk === "template") {
      return Boolean(this.sDraftTemplateId() && targetRepo);
    }
    return Boolean(this.sDraftTitle().trim() && this.sDraftRepo().trim());
  }

  private buildRecurrenceDraft(): Recurrence {
    const kind = this.sDraftKind();
    const tz = this.sDraftTz().trim() || undefined;
    switch (kind) {
      case "everyNMinutes":
        return { kind, everyNMinutes: this.sDraftEveryMin(), timeZone: tz };
      case "everyNHours":
        return { kind, everyNHours: this.sDraftEveryHr(), timeZone: tz };
      case "daily":
        return { kind, timeOfDay: this.sDraftTime().trim(), timeZone: tz };
      case "weekly":
        return {
          kind,
          timeOfDay: this.sDraftTime().trim(),
          weekday: this.sDraftWeekday(),
          timeZone: tz,
        };
      case "monthly":
        return {
          kind,
          timeOfDay: this.sDraftTime().trim(),
          dayOfMonth: this.sDraftDayOfMonth(),
          timeZone: tz,
        };
      case "github_workflow_run": {
        const concls = this.sDraftTriggerConclusion()
          .split(",")
          .map((c) => c.trim())
          .filter(Boolean);
        const branches = this.sDraftTriggerBranch()
          .split(",")
          .map((b) => b.trim())
          .filter(Boolean);
        return {
          kind,
          githubWorkflowRun: {
            workflow: this.sDraftTriggerWorkflow().trim() || undefined,
            conclusions: concls.length ? concls : undefined,
            branches: branches.length ? branches : undefined,
          },
        };
      }
      case "git_ref":
        return {
          kind,
          gitRef: {
            repo: this.sDraftGitRefRepo().trim() || this.schedEffectiveRepo(),
            ref: this.sDraftGitRef().trim(),
          },
        };
      default:
        return { kind };
    }
  }

  private buildScheduleCreatePayload(): CreateScheduleRequest {
    const tk = this.schedTargetKind();
    const bound = this.schedBoundTemplate();
    const baseReq: CreateScheduleRequest = {
      repo: bound ? "" : this.sDraftRepo().trim(),
      base: bound ? undefined : this.sDraftBase().trim() || undefined,
      recurrence: this.buildRecurrenceDraft(),
      timeZone: this.sDraftTz().trim() || undefined,
      overlap: this.sDraftOverlap(),
    };
    // A suite decides the model of everything it runs; the API refuses
    // one beside a suiteId. "" is the default, the same as saying nothing.
    if (tk !== "suite" && this.sDraftModel()) {
      baseReq.model = this.sDraftModel();
    }
    if (tk === "template") {
      baseReq.templateId = this.sDraftTemplateId();
    } else if (tk === "suite") {
      baseReq.suiteId = this.sDraftSuiteId();
    } else {
      baseReq.title = this.sDraftTitle().trim();
      baseReq.description = this.sDraftDesc().trim();
      baseReq.keepRunning = this.sDraftKeepRunning();
      // Only when it differs from the default: raising admission is
      // gated for a limited role, so a form that says nothing must not ask.
      if (this.sDraftAdmission() !== "standard") {
        baseReq.admission = this.sDraftAdmission();
      }
      if (this.sDraftHosting() !== "unattended") {
        baseReq.hosting = this.sDraftHosting();
      }
      if (this.sDraftNetwork()) {
        baseReq.networkAccess = this.sDraftNetwork();
      }
      baseReq.capabilities = [...this.sDraftCaps()];
      baseReq.reads = [...this.sDraftReads()];
      baseReq.writes = this.sDraftWrites().map((w) => ({
        repo: w.repo,
        ...(w.base ? { base: w.base } : {}),
      }));
      const draftCf = this.sDraftCustomFields();
      if (Object.keys(draftCf).length > 0) {
        baseReq.customFields = { ...draftCf };
      }
    }
    const qaFlowRef = this.sDraftQaFlowRef().trim();
    if (qaFlowRef) {
      baseReq.qaFlowRef = qaFlowRef;
    }
    return baseReq;
  }

  /**
   * buildSchedulePreviewPayload is the create body as the draft
   * composition endpoint must see it. That endpoint has no saved row to
   * fall back on, so the model, admission and hosting in force are always
   * sent -- for a template-backed schedule, the saved schedule's own
   * (a declared schedule may raise its template's admission), and its own
   * capabilities, which its firings grant beside the template's
   * (ScheduleOverlay.jsx previewRequest).
   */
  private buildSchedulePreviewPayload(): CreateScheduleRequest {
    const req = this.buildScheduleCreatePayload();
    const tk = this.schedTargetKind();
    if (tk === "suite") return req;
    const editing = this.editingSchedule();
    const sameTemplate =
      tk === "template" &&
      !!editing?.templateId &&
      editing.templateId === this.sDraftTemplateId();
    req.model = this.sDraftModel();
    if (tk === "prompt") {
      req.admission = this.sDraftAdmission();
      req.hosting = this.sDraftHosting();
    } else {
      req.admission = sameTemplate ? editing?.admission || "" : "";
      req.hosting = sameTemplate ? editing?.hosting || "" : "";
      if (sameTemplate && (editing?.capabilities ?? []).length > 0) {
        req.capabilities = [...(editing?.capabilities ?? [])];
      }
    }
    return req;
  }

  private buildScheduleUpdatePayload(editing: Schedule): UpdateScheduleRequest {
    if (editing.system) {
      // One of grain's own schedules: its model is the deployment's to
      // choose, like its cadence (ui.UpdateScheduleRequest.Model).
      return {
        recurrence: this.buildRecurrenceDraft(),
        timeZone: this.sDraftTz().trim() || undefined,
        overlap: this.sDraftOverlap(),
        ...(this.sDraftModel() !== (editing.model || "")
          ? { model: this.sDraftModel() }
          : {}),
        revision: editing.revision,
      };
    }
    const tk = this.schedTargetKind();
    const bound = this.schedBoundTemplate();
    const patchReq: UpdateScheduleRequest = {
      ...(bound
        ? {}
        : {
            repo: this.sDraftRepo().trim(),
            base: this.sDraftBase().trim(),
          }),
      recurrence: this.buildRecurrenceDraft(),
      timeZone: this.sDraftTz().trim() || undefined,
      overlap: this.sDraftOverlap(),
      revision: editing.revision,
    };
    const qaFlowRef = this.sDraftQaFlowRef().trim();
    if (qaFlowRef || editing.qaFlowRef) {
      patchReq.qaFlowRef = qaFlowRef;
    }
    // Each setting below only when it changed, as the classic form sends
    // them: absent leaves the saved value alone (ui.UpdateScheduleRequest),
    // and resending an unchanged admission would ask a limited role's
    // grant gate for a raise nobody made.
    if (tk !== "suite" && this.sDraftModel() !== (editing.model || "")) {
      patchReq.model = this.sDraftModel();
    }
    if (tk === "template") {
      patchReq.templateId = this.sDraftTemplateId();
    } else if (tk === "suite") {
      patchReq.suiteId = this.sDraftSuiteId();
    } else {
      patchReq.title = this.sDraftTitle().trim();
      patchReq.description = this.sDraftDesc().trim();
      if (editing.templateId) {
        patchReq.templateId = "";
      }
      if (this.sDraftKeepRunning() !== !!editing.keepRunning) {
        patchReq.keepRunning = this.sDraftKeepRunning();
      }
      if (this.sDraftAdmission() !== (editing.admission || "standard")) {
        patchReq.admission = this.sDraftAdmission();
      }
      if (this.sDraftHosting() !== (editing.hosting || "unattended")) {
        patchReq.hosting = this.sDraftHosting();
      }
      if (!sameList(this.sDraftCaps(), editing.capabilities)) {
        patchReq.capabilities = [...this.sDraftCaps()];
      }
      if (this.sDraftNetwork() !== (editing.networkAccess || "")) {
        patchReq.networkAccess = this.sDraftNetwork();
      }
      if (!sameList(this.sDraftReads(), editing.reads)) {
        patchReq.reads = [...this.sDraftReads()];
      }
      if (!sameWrites(this.sDraftWrites(), editing.writes)) {
        patchReq.writes = this.sDraftWrites().map((w) => ({
          repo: w.repo,
          ...(w.base ? { base: w.base } : {}),
        }));
      }
      const draftCf = this.sDraftCustomFields();
      const savedCf = initialCustomFields(
        this.declaredCustomFields(),
        editing.customFields,
      );
      if (!sameCustomFields(draftCf, savedCf)) {
        patchReq.customFields = { ...draftCf };
      }
    }
    return patchReq;
  }

  protected async previewScheduleDraft(): Promise<void> {
    this.dialogError.set(null);
    try {
      const comp = await this.store.previewDraftSchedule(
        this.buildSchedulePreviewPayload(),
      );
      this.draftSchedComposition.set(comp);
    } catch (err) {
      this.dialogError.set(errorText(err));
    }
  }

  protected async saveScheduleDialog(): Promise<void> {
    if (this.submitting() || !this.canSaveSchedule()) return;
    this.dialogError.set(null);
    this.submitting.set(true);
    try {
      const editing = this.editingSchedule();
      if (editing) {
        const saved = await this.store.updateSchedule(
          editing.id,
          this.buildScheduleUpdatePayload(editing),
        );
        this.selectSchedule(saved.id, false);
        this.snackbar.show("Schedule updated.");
      } else {
        const created = await this.store.createSchedule(
          this.buildScheduleCreatePayload(),
        );
        this.selectSchedule(created.id, true);
        this.snackbar.show("Schedule created.");
      }
      this.closeScheduleDialog();
    } catch (err) {
      this.dialogError.set(errorText(err));
    } finally {
      this.submitting.set(false);
    }
  }

  // --- Template actions & dialogs ---

  protected async openNewTaskFromTemplate(
    t: Template,
    ev?: Event,
    fromDialog = false,
  ): Promise<void> {
    const opener = (ev?.currentTarget as HTMLElement | null) ?? null;
    if (fromDialog) {
      if (
        this.tmplDraftSnapshot() !== this.tmplBaseline &&
        !(await this.askDiscard("template"))
      ) {
        return;
      }
      this.dialogError.set(null);
      this.creatingTemplateForSuite.set(false);
      this.tmplDialogOpen.set(false);
    }
    if ((t.fields ?? []).length > 0) {
      this.openLaunchTemplateDialog(t);
      return;
    }
    const repo = t.declaredIn?.repo || t.repo || (t.system ? NO_REPO : "");
    this.createTask.open(
      {
        templateId: t.id,
        templatePriority: t.priority || 5,
        ...(repo ? { repo } : {}),
      },
      opener,
    );
  }

  protected openLaunchTemplateDialog(t: Template | null): void {
    this.openPicker.set(null);
    this.dialogError.set(null);
    const chosen = t ?? this.store.templates()[0] ?? null;
    this.launchTemplateId.set(chosen?.id ?? "");
    this.initLaunchFields(chosen);
    this.launchRepo.set(chosen?.repo || this.store.knownRepos()[0] || "");
    this.launchBase.set(chosen?.base || "main");
    this.launchApproved.set(this.store.approvedByDefault());
    this.launchDialogOpen.set(true);
  }

  protected pickLaunchTemplate(id: string): void {
    this.launchTemplateId.set(id);
    this.openPicker.set(null);
    const chosen = this.store.templates().find((t) => t.id === id) ?? null;
    this.initLaunchFields(chosen);
  }

  private initLaunchFields(t: Template | null): void {
    const init: Record<string, string> = {};
    for (const f of t?.fields ?? []) {
      init[f.name] = f.default || "";
    }
    this.launchFields.set(init);
  }

  protected setLaunchField(name: string, val: string): void {
    this.launchFields.update((m) => ({ ...m, [name]: val }));
  }

  protected async submitLaunchTemplate(): Promise<void> {
    const tmpl = this.launchSelectedTemplate();
    if (!tmpl || this.submitting() || !this.canSubmitLaunchTemplate()) return;
    this.dialogError.set(null);
    this.submitting.set(true);
    try {
      const task = await this.store.launchTaskFromTemplate(tmpl, {
        repo: this.launchRepo(),
        base: this.launchBase(),
        approved: this.launchApproved(),
        fields: this.launchFields(),
      });
      this.closeLaunchDialog();
      if (task?.id) {
        this.snackbar.show(`Created task #${task.id} from template.`);
        await this.router.navigate(["/task", task.id]);
      } else {
        this.snackbar.show("Created task from template.");
      }
    } catch (err) {
      this.dialogError.set(errorText(err));
    } finally {
      this.submitting.set(false);
    }
  }

  protected openTemplateDialog(existing: Template | null): void {
    this.dialogError.set(null);
    this.editingTemplate.set(existing);
    if (existing) {
      this.tDraftName.set(existing.name);
      this.tDraftTitle.set(existing.title);
      this.tDraftDesc.set(existing.description);
      this.tDraftRepo.set(existing.repo || "");
      const clonedFields = structuredClone(existing.fields ?? []);
      this.tDraftFields.set(clonedFields);
      this.tDraftParamsCsv.set(clonedFields.map((f) => f.name).join(", "));
      this.tDraftAutoMerge.set(existing.autoMerge);
      this.tDraftModel.set(existing.model || "");
      this.tDraftKeepRunning.set(!!existing.keepRunning);
      this.tDraftPriority.set(
        existing.priority ? String(existing.priority) : "",
      );
      this.tDraftInteractive.set(!!existing.interactive);
      this.tDraftAdmission.set(existing.admission || "standard");
      this.tDraftHosting.set(existing.hosting || "unattended");
      this.tDraftCaps.set([...(existing.capabilities ?? [])]);
      this.tDraftQaFlowRef.set(existing.qaFlowRef || "");
    } else {
      this.tDraftName.set("");
      this.tDraftTitle.set("");
      this.tDraftDesc.set("");
      this.tDraftRepo.set("");
      this.tDraftFields.set([]);
      this.tDraftParamsCsv.set("");
      this.tDraftAutoMerge.set(false);
      this.tDraftModel.set("");
      this.tDraftKeepRunning.set(false);
      this.tDraftPriority.set("");
      this.tDraftInteractive.set(false);
      this.tDraftAdmission.set("standard");
      this.tDraftHosting.set("unattended");
      this.tDraftCaps.set([]);
      this.tDraftQaFlowRef.set("");
    }
    this.tmplDialogOpen.set(true);
    this.tmplBaseline = this.tmplDraftSnapshot();
  }

  protected onTemplateParamsCsvInput(csv: string): void {
    this.tDraftParamsCsv.set(csv);
    const prevByName = new Map<string, TemplateField>();
    for (const f of this.editingTemplate()?.fields ?? []) {
      prevByName.set(f.name, f);
    }
    for (const f of this.tDraftFields()) {
      prevByName.set(f.name, f);
    }
    const seen = new Set<string>();
    const next: TemplateField[] = [];
    for (const raw of csv.split(",")) {
      const name = raw.trim();
      if (!name || seen.has(name)) continue;
      seen.add(name);
      const existing = prevByName.get(name);
      next.push(
        existing ? structuredClone(existing) : { name, required: true },
      );
    }
    this.tDraftFields.set(next);
  }

  protected updateTemplateFieldMeta(
    name: string,
    key: "label" | "default",
    val: string,
  ): void {
    this.tDraftFields.update((list) =>
      list.map((f) =>
        f.name === name ? { ...f, [key]: val || undefined } : f,
      ),
    );
  }

  protected toggleTemplateFieldRequired(name: string): void {
    this.tDraftFields.update((list) =>
      list.map((f) => (f.name === name ? { ...f, required: !f.required } : f)),
    );
  }

  // draftTemplatePriority is the typed Task priority as a number, 0 when
  // blank (the deployment's default).
  private draftTemplatePriority(): number {
    const n = taskPriority(this.tDraftPriority());
    return Number.isNaN(n) ? 0 : n;
  }

  // normalizeTemplatePriority shows the number a typed priority is ("1e3"
  // reads back as 1000) once the field is left.
  protected normalizeTemplatePriority(): void {
    const n = taskPriority(this.tDraftPriority());
    if (!Number.isNaN(n) && String(n) !== this.tDraftPriority()) {
      this.tDraftPriority.set(String(n));
    }
  }

  protected canSaveTemplate(): boolean {
    if (!this.tDraftName().trim() || !this.tDraftTitle().trim()) return false;
    if (this.tDraftPriorityInvalid()) return false;
    const editing = this.editingTemplate();
    const route = editing ? "PATCH /api/templates/{id}" : "POST /api/templates";
    const targetRepo = this.tDraftRepo().trim() || editing?.repo || undefined;
    return this.access.can(route, targetRepo);
  }

  /**
   * buildTemplateUpdatePayload is the PATCH for an edited template: its
   * text, binding and fields as before, and each setting below only when
   * it changed (TemplateOverlay.jsx): absent leaves the saved value alone.
   */
  private buildTemplateUpdatePayload(
    editing: Template,
    fields: TemplateField[],
  ): UpdateTemplateRequest {
    const req: UpdateTemplateRequest = {
      name: this.tDraftName().trim(),
      title: this.tDraftTitle().trim(),
      description: this.tDraftDesc(),
      repo: this.tDraftRepo().trim(),
      fields,
      autoMerge: this.tDraftAutoMerge(),
      revision: editing.revision,
    };
    if (this.tDraftModel() !== (editing.model || "")) {
      req.model = this.tDraftModel();
    }
    if (this.tDraftKeepRunning() !== !!editing.keepRunning) {
      req.keepRunning = this.tDraftKeepRunning();
    }
    // 0 clears the template's own priority (UpdateTemplateRequest).
    const priority = this.draftTemplatePriority();
    if (priority !== (editing.priority || 0)) {
      req.priority = priority;
    }
    if (this.tDraftInteractive() !== !!editing.interactive) {
      req.interactive = this.tDraftInteractive();
    }
    if (this.tDraftAdmission() !== (editing.admission || "standard")) {
      req.admission = this.tDraftAdmission();
    }
    if (this.tDraftHosting() !== (editing.hosting || "unattended")) {
      req.hosting = this.tDraftHosting();
    }
    if (!sameList(this.tDraftCaps(), editing.capabilities)) {
      req.capabilities = [...this.tDraftCaps()];
    }
    return req;
  }

  protected async saveTemplateDialog(): Promise<void> {
    if (this.submitting() || !this.canSaveTemplate()) return;
    this.dialogError.set(null);
    this.submitting.set(true);
    const fields = structuredClone(this.tDraftFields());
    try {
      const editing = this.editingTemplate();
      const qaFlowRef = this.tDraftQaFlowRef().trim();
      if (editing) {
        const patchReq = this.buildTemplateUpdatePayload(editing, fields);
        if (qaFlowRef || editing.qaFlowRef) {
          patchReq.qaFlowRef = qaFlowRef;
        }
        const updated = await this.store.updateTemplate(editing.id, patchReq);
        this.selectTemplate(updated.id, false);
        const selSuite = this.selectedSuiteId();
        if (selSuite) this.selectSuite(selSuite, false);
        const selSched = this.selectedScheduleId();
        if (selSched) this.selectSchedule(selSched, false);
        this.snackbar.show("Template updated.");
      } else {
        const createReq: CreateTemplateRequest = {
          name: this.tDraftName().trim(),
          title: this.tDraftTitle().trim(),
          description: this.tDraftDesc(),
          repo: this.tDraftRepo().trim(),
          fields: fields.length ? fields : undefined,
          autoMerge: this.tDraftAutoMerge(),
          keepRunning: this.tDraftKeepRunning(),
          ...(this.draftTemplatePriority()
            ? { priority: this.draftTemplatePriority() }
            : {}),
          ...(this.tDraftInteractive() ? { interactive: true } : {}),
          ...(this.tDraftModel() ? { model: this.tDraftModel() } : {}),
          ...(this.tDraftAdmission() !== "standard"
            ? { admission: this.tDraftAdmission() }
            : {}),
          ...(this.tDraftHosting() !== "unattended"
            ? { hosting: this.tDraftHosting() }
            : {}),
          capabilities: [...this.tDraftCaps()],
          reads: [],
          writes: [],
          customFields: {},
        };
        if (qaFlowRef) {
          createReq.qaFlowRef = qaFlowRef;
        }
        const created = await this.store.createTemplate(createReq);
        if (this.creatingTemplateForSuite() && this.suiteDialogOpen()) {
          this.stDraftTemplateIds.update((ids) =>
            ids.includes(created.id) ? ids : [...ids, created.id],
          );
          this.creatingTemplateForSuite.set(false);
          this.snackbar.show("Template created and added to suite.");
        } else {
          this.selectTemplate(created.id, true);
          this.snackbar.show("Template created.");
        }
      }
      this.tmplDialogOpen.set(false);
    } catch (err) {
      this.dialogError.set(errorText(err));
    } finally {
      this.submitting.set(false);
    }
  }

  protected confirmDeleteTemplate(t: Template): void {
    this.confirming.set({
      title: "Delete template?",
      message: `Delete template "${t.name || t.id}"?`,
      accept: "Delete template",
      destructive: true,
      run: async () => {
        await this.store.deleteTemplate(t.id);
        if (this.selectedTemplateId() === t.id) {
          this.selectedTemplateId.set(null);
          this.templateComposition.set(null);
        }
        if (this.activeTab() === "templates" && this.itemId() === t.id) {
          void this.router.navigate(["/processes", "templates"], {
            ...this.preserveQueryExtras(),
            replaceUrl: true,
          });
        }
        this.snackbar.show("Template deleted.");
      },
    });
  }

  // --- Suite & Suite Run actions ---

  protected openSuiteDialog(existing: Suite | null): void {
    this.dialogError.set(null);
    this.editingSuite.set(existing);
    this.draftSuiteComposition.set(null);
    this.suiteTmplQuery.set("");
    this.suiteTmplOpen.set(false);
    this.suiteTmplActiveIndex.set(0);
    this.creatingTemplateForSuite.set(false);
    if (existing) {
      this.stDraftName.set(existing.name);
      this.stDraftMode.set(existing.mode || "count");
      this.stDraftCount.set(existing.count || 1);
      this.stDraftMaxPasses.set(existing.maxPasses || 3);
      this.stDraftTemplateIds.set(
        (existing.items ?? []).map((i) => i.templateId),
      );
      this.stDraftRequireApproval.set(!!existing.requireApproval);
      this.stDraftAutoMerge.set(!!existing.autoMerge);
      this.stDraftQaFlowRef.set(existing.qaFlowRef || "");
    } else {
      this.stDraftName.set("");
      this.stDraftMode.set("count");
      this.stDraftCount.set(1);
      this.stDraftMaxPasses.set(3);
      const firstId = this.schedulableTemplates()[0]?.id;
      this.stDraftTemplateIds.set(firstId ? [firstId] : []);
      this.stDraftRequireApproval.set(false);
      this.stDraftAutoMerge.set(true);
      this.stDraftQaFlowRef.set("");
    }
    this.suiteDialogOpen.set(true);
    this.suiteBaseline = this.suiteDraftSnapshot();
  }

  protected suiteTemplateLabel(id: string): string {
    const label = this.suiteTemplateLabels().get(id);
    if (label) return label;
    const item = this.editingSuite()?.items?.find((x) => x.templateId === id);
    return item?.templateName || id;
  }

  protected openSuiteTmplDropdown(): void {
    this.suiteTmplOpen.set(true);
    if (this.suiteTmplActiveIndex() >= this.filteredSuiteTemplates().length) {
      this.suiteTmplActiveIndex.set(0);
    }
  }

  protected toggleSuiteTmplDropdown(ev: Event): void {
    ev.stopPropagation();
    this.suiteTmplOpen.update((v) => !v);
  }

  protected onSuiteTmplQueryInput(val: string): void {
    this.suiteTmplQuery.set(val);
    this.suiteTmplOpen.set(true);
    this.suiteTmplActiveIndex.set(0);
  }

  protected onSuiteTmplKeydown(ev: KeyboardEvent): void {
    const opts = this.filteredSuiteTemplates();
    if (ev.key === "ArrowDown") {
      ev.preventDefault();
      if (!this.suiteTmplOpen()) {
        this.suiteTmplOpen.set(true);
        this.suiteTmplActiveIndex.set(0);
      } else if (opts.length > 0) {
        this.suiteTmplActiveIndex.update((i) => (i + 1) % opts.length);
      }
    } else if (ev.key === "ArrowUp") {
      ev.preventDefault();
      if (!this.suiteTmplOpen()) {
        this.suiteTmplOpen.set(true);
        this.suiteTmplActiveIndex.set(Math.max(0, opts.length - 1));
      } else if (opts.length > 0) {
        this.suiteTmplActiveIndex.update(
          (i) => (i - 1 + opts.length) % opts.length,
        );
      }
    } else if (ev.key === "Escape" && this.suiteTmplOpen()) {
      ev.preventDefault();
      ev.stopPropagation();
      this.suiteTmplOpen.set(false);
    } else if (ev.key === "Enter") {
      ev.preventDefault();
      if (this.suiteTmplOpen() && opts[this.suiteTmplActiveIndex()]) {
        this.toggleSuiteTemplateId(
          opts[this.suiteTmplActiveIndex()].template.id,
        );
      }
    }
  }

  protected removeSuiteTemplateId(id: string): void {
    this.stDraftTemplateIds.update((prev) => prev.filter((x) => x !== id));
  }

  protected openInlineSuiteTemplateDialog(): void {
    if (!this.access.can("POST /api/templates")) return;
    this.suiteTmplOpen.set(false);
    this.openPicker.set(null);
    this.creatingTemplateForSuite.set(true);
    this.openTemplateDialog(null);
  }

  protected suiteSaveDeniedReason(): string {
    const editing = this.editingSuite();
    const route = editing ? "PATCH /api/suites/{id}" : "POST /api/suites";
    const boundRepos = this.stDraftTemplateIds()
      .map((id) => this.store.templates().find((t) => t.id === id))
      .map((t) => t?.repo || t?.declaredIn?.repo || "")
      .filter(Boolean);
    if (
      !this.access.can(route, boundRepos.length > 0 ? boundRepos : undefined)
    ) {
      return this.access.deniedReason(
        route,
        boundRepos.length > 0 ? boundRepos : undefined,
      );
    }
    return "";
  }

  protected toggleSuiteTemplateId(id: string): void {
    this.stDraftTemplateIds.update((prev) =>
      prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id],
    );
  }

  protected canSaveSuite(): boolean {
    if (!this.stDraftName().trim() || this.stDraftTemplateIds().length === 0) {
      return false;
    }
    const editing = this.editingSuite();
    const route = editing ? "PATCH /api/suites/{id}" : "POST /api/suites";
    const boundRepos = this.stDraftTemplateIds()
      .map((id) => this.store.templates().find((t) => t.id === id))
      .map((t) => t?.repo || t?.declaredIn?.repo || "")
      .filter(Boolean);
    return this.access.can(
      route,
      boundRepos.length > 0 ? boundRepos : undefined,
    );
  }

  // suitePassesProblem says what is wrong with the pass count the dialog
  // holds -- the one its stopping mode reads -- before it is sent: the
  // field keeps what was typed rather than swapping in a default, so a 0,
  // an empty field or a fraction is refused here, in the server's own
  // range (model.MaxSuitePasses), instead of being saved as 1 or 3.
  protected suitePassesProblem(): string | null {
    const untilClean = this.stDraftMode() === "until_clean";
    const n = untilClean ? this.stDraftMaxPasses() : this.stDraftCount();
    if (Number.isInteger(n) && n >= 1 && n <= this.maxSuitePasses) {
      return null;
    }
    const what = untilClean ? "Maximum passes" : "Number of passes";
    return `${what} must be a whole number from 1 to ${this.maxSuitePasses}.`;
  }

  private buildSuiteCreatePayload(): CreateSuiteRequest {
    // suitePassesProblem checks the mode's own number; the other one is
    // sent too but read by nobody, so a stale 0 or 2.5 left in it goes as
    // its default rather than failing the request's decoding.
    const untilClean = this.stDraftMode() === "until_clean";
    const whole = (n: number, dflt: number) =>
      Number.isInteger(n) && n >= 1 ? n : dflt;
    const req: CreateSuiteRequest = {
      name: this.stDraftName().trim(),
      templateIds: this.stDraftTemplateIds(),
      mode: this.stDraftMode(),
      count: untilClean ? whole(this.stDraftCount(), 1) : this.stDraftCount(),
      maxPasses: untilClean
        ? this.stDraftMaxPasses()
        : whole(this.stDraftMaxPasses(), 3),
      requireApproval: this.stDraftRequireApproval(),
      autoMerge: this.stDraftAutoMerge(),
    };
    const qaFlowRef = this.stDraftQaFlowRef().trim();
    if (qaFlowRef) {
      req.qaFlowRef = qaFlowRef;
    }
    return req;
  }

  protected async previewSuiteDraft(): Promise<void> {
    this.dialogError.set(this.suitePassesProblem());
    if (this.dialogError()) return;
    try {
      const comp = await this.store.previewDraftSuite(
        this.buildSuiteCreatePayload(),
      );
      this.draftSuiteComposition.set(comp);
    } catch (err) {
      this.dialogError.set(errorText(err));
    }
  }

  protected async saveSuiteDialog(): Promise<void> {
    if (this.submitting() || !this.canSaveSuite()) return;
    this.dialogError.set(this.suitePassesProblem());
    if (this.dialogError()) return;
    this.submitting.set(true);
    try {
      const editing = this.editingSuite();
      const payload = this.buildSuiteCreatePayload();
      const qaFlowRef = this.stDraftQaFlowRef().trim();
      if (editing) {
        const patchReq: UpdateSuiteRequest = {
          ...payload,
          revision: editing.revision,
        };
        if (qaFlowRef || editing.qaFlowRef) {
          patchReq.qaFlowRef = qaFlowRef;
        }
        const updated = await this.store.updateSuite(editing.id, patchReq);
        this.selectSuite(updated.id, false);
        const selSched = this.selectedScheduleId();
        if (selSched) this.selectSchedule(selSched, false);
        this.snackbar.show("Suite updated.");
      } else {
        const created = await this.store.createSuite(payload);
        this.selectSuite(created.id, true);
        this.snackbar.show("Suite created.");
      }
      this.suiteDialogOpen.set(false);
    } catch (err) {
      this.dialogError.set(errorText(err));
    } finally {
      this.submitting.set(false);
    }
  }

  protected confirmDeleteSuite(st: Suite): void {
    this.confirming.set({
      title: "Delete suite?",
      message: `Delete suite "${st.name}"? Past suite runs keep their snapshots.`,
      accept: "Delete suite",
      destructive: true,
      run: async () => {
        await this.store.deleteSuite(st.id);
        if (this.selectedSuiteId() === st.id) {
          this.selectedSuiteId.set(null);
          this.suiteComposition.set(null);
        }
        if (this.activeTab() === "suites" && this.itemId() === st.id) {
          void this.router.navigate(["/processes", "suites"], {
            ...this.preserveQueryExtras(),
            replaceUrl: true,
          });
        }
        this.snackbar.show("Suite deleted.");
      },
    });
  }

  protected openRunSuiteDialog(st: Suite | null): void {
    this.openPicker.set(null);
    this.dialogError.set(null);
    const chosen = st ?? this.store.suites()[0] ?? null;
    this.runSuiteId.set(chosen?.id ?? "");
    this.runSuiteRepo.set(
      chosen?.declaredIn?.repo || this.store.knownRepos()[0] || "",
    );
    this.runSuiteBase.set("main");
    this.runSuiteDialogOpen.set(true);
  }

  protected canSubmitRunSuite(): boolean {
    const repo = this.runSuiteRepo().trim();
    if (!this.runSuiteId() || !repo || !this.runSuiteBase().trim()) {
      return false;
    }
    return this.access.can("POST /api/suite-runs", repo);
  }

  protected async submitRunSuite(): Promise<void> {
    if (this.submitting() || !this.canSubmitRunSuite()) return;
    this.dialogError.set(null);
    this.submitting.set(true);
    try {
      const run = await this.store.startSuiteRun(
        this.runSuiteId(),
        this.runSuiteRepo(),
        this.runSuiteBase(),
      );
      this.closeRunSuiteDialog();
      this.suitesView.set("runs");
      this.selectSuiteRun(run.id, true);
      this.snackbar.show(`Started suite run #${run.id}.`);
    } catch (err) {
      this.dialogError.set(errorText(err));
    } finally {
      this.submitting.set(false);
    }
  }

  protected confirmCancelSuiteRun(sr: SuiteRun): void {
    this.confirming.set({
      title: "Cancel active suite run?",
      message: `Stop and close all open pass tasks in suite run #${sr.id} (${sr.repo})?`,
      accept: "Stop suite tasks",
      destructive: true,
      run: async () => {
        const stopped = await this.store.cancelSuiteRunTasks(sr);
        this.snackbar.show(
          `Closed ${stopped} active suite ${stopped === 1 ? "task" : "tasks"}.`,
        );
      },
    });
  }

  // --- Repo Declarations & Workflows actions ---

  protected async toggleRepoDeclarations(
    repo: string,
    enabled: boolean,
  ): Promise<void> {
    try {
      await this.store.setRepoDeclarationsEnabled(repo, enabled);
      this.snackbar.show(
        enabled
          ? `Enabled .grain/ declarations for ${repo}.`
          : `Disabled .grain/ declarations for ${repo}.`,
      );
    } catch (err) {
      this.store.actionError.set(errorText(err));
    }
  }

  protected async acceptRepoDeclaration(
    acc: DeclarationAcceptanceView,
  ): Promise<void> {
    if (!this.canAcceptDeclaration(acc)) return;
    try {
      await this.store.acceptDeclaration(acc.id, acc.requested ?? []);
      this.snackbar.show(`Accepted declaration ${acc.id}.`);
    } catch (err) {
      this.store.actionError.set(errorText(err));
    }
  }

  protected openWorkflowEditor(repo: string, wf: RepoWorkflow | null): void {
    this.dialogError.set(null);
    this.wfEditorRepo.set(repo);
    this.wfEditorOriginalPath.set(wf?.path ?? "");
    this.wfEditorPath.set(wf?.path ?? ".grain/workflows/ci.yml");
    this.wfEditorText.set(
      wf?.text ??
        "name: CI\non:\n  - push\n  - pull_request\njobs:\n  test:\n    steps:\n      - name: Run tests\n        run: go test ./...\n",
    );
    this.wfEditorOpen.set(true);
  }

  protected async saveWorkflowEditor(): Promise<void> {
    if (this.submitting() || !this.canEditWorkflow()) return;
    this.dialogError.set(null);
    this.submitting.set(true);
    const repo = this.wfEditorRepo();
    const existing = this.store.repoWorkflows()[repo]?.workflows ?? [];
    const orig = this.wfEditorOriginalPath();
    const updatedList = existing.filter(
      (w) => w.path !== orig && w.path !== this.wfEditorPath().trim(),
    );
    updatedList.push({
      path: this.wfEditorPath().trim(),
      text: this.wfEditorText(),
    });
    try {
      await this.store.saveRepoWorkflows(repo, updatedList);
      this.wfEditorOpen.set(false);
      this.snackbar.show(`Saved workflow ${this.wfEditorPath().trim()}.`);
    } catch (err) {
      this.dialogError.set(errorText(err));
    } finally {
      this.submitting.set(false);
    }
  }

  protected async deleteWorkflowOverride(): Promise<void> {
    const orig = this.wfEditorOriginalPath();
    if (!orig || this.submitting() || !this.canEditWorkflow()) return;
    this.dialogError.set(null);
    this.submitting.set(true);
    const repo = this.wfEditorRepo();
    const existing = this.store.repoWorkflows()[repo]?.workflows ?? [];
    const remaining = existing.filter((w) => w.path !== orig);
    try {
      await this.store.saveRepoWorkflows(repo, remaining);
      this.wfEditorOpen.set(false);
      this.snackbar.show(`Removed workflow override ${orig}.`);
    } catch (err) {
      this.dialogError.set(errorText(err));
    } finally {
      this.submitting.set(false);
    }
  }

  private discardDraftResolve: ((ok: boolean) => void) | null = null;
  private bypassUnsavedDraftGuard = false;

  private hasUnsavedQaDraft(): boolean {
    return Boolean(this.doc.querySelector('[data-qa-unsaved-draft="true"]'));
  }

  @HostListener("window:beforeunload", ["$event"])
  protected onBeforeUnload(ev: BeforeUnloadEvent): void {
    if (this.unsavedDialogs().length > 0) ev.preventDefault();
  }

  canDeactivate(): boolean | Promise<boolean> {
    if (this.bypassUnsavedDraftGuard) return true;
    const dialogs = this.unsavedDialogs();
    if (dialogs.length > 0) {
      return this.askDiscard(dialogs[dialogs.length - 1]).then((ok) => {
        if (ok) {
          this.closeScheduleDialog();
          this.creatingTemplateForSuite.set(false);
          this.suiteTmplOpen.set(false);
          this.tmplDialogOpen.set(false);
          this.suiteDialogOpen.set(false);
        }
        return ok;
      });
    }
    if (!this.hasUnsavedQaDraft()) return true;
    return this.promptDiscardQaDraft();
  }

  private promptDiscardQaDraft(): Promise<boolean> {
    if (this.discardDraftResolve) return Promise.resolve(false);
    return new Promise<boolean>((resolve) => {
      this.discardDraftResolve = resolve;
      this.confirming.set({
        title: "Discard unpublished workflow draft?",
        message:
          "You have an unpublished workflow draft in Software Workflow & QA. Discard your unsaved changes?",
        accept: "Discard draft",
        destructive: true,
        run: async () => undefined,
      });
    });
  }

  protected onTabClick(ev: MouseEvent, tabId: ProcessTab): void {
    if (
      ev.button !== 0 ||
      ev.metaKey ||
      ev.ctrlKey ||
      ev.shiftKey ||
      ev.altKey
    ) {
      return;
    }
    if (tabId === this.activeTab() || !this.hasUnsavedQaDraft()) {
      return;
    }
    ev.preventDefault();
    void this.promptDiscardQaDraft().then((ok) => {
      if (!ok) return;
      this.bypassUnsavedDraftGuard = true;
      void this.router
        .navigate(["/processes", tabId], { queryParamsHandling: "merge" })
        .finally(() => {
          this.bypassUnsavedDraftGuard = false;
        });
    });
  }

  protected async onConfirmDone(accepted: boolean): Promise<void> {
    const req = this.confirming();
    const resolveDraft = this.discardDraftResolve;
    this.discardDraftResolve = null;
    this.confirming.set(null);
    if (resolveDraft) {
      resolveDraft(accepted);
    }
    if (!accepted || !req) return;
    try {
      await req.run();
    } catch (err) {
      this.store.actionError.set(errorText(err));
    }
  }
}
