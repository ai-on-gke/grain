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
  type OnDestroy,
  ViewEncapsulation,
  computed,
  inject,
  signal,
  viewChild,
} from "@angular/core";
import { ActivatedRoute, Router, RouterLink } from "@angular/router";
import type { Subscription } from "rxjs";
import { GrainApi } from "../api/grain-api.service";
import type {
  DiffPreviewFile,
  DiffPreviewLine,
  QAFlowsResponse,
} from "../api/models";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  ConfirmDialogComponent,
  type ConfirmRequest,
} from "../shared/confirm-dialog.component";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import {
  type FlowDeclarationV2,
  type FlowDeclarationV4,
  MAX_PROMPT_BUDGET_BYTES,
  type QADryRunResponse,
  type QAFlowVersionDetail,
  type QAFlowVersionItem,
  type QAFlowVersionsResponse,
  buildClientWorkflowStagesView,
  computeActivePromptBytes,
  isLockedFloorRule,
  utf8ByteCount,
} from "./qa-flows.models";
import { QaV3StudioComponent } from "./qa-v3-studio.component";

@Component({
  selector: "g-qa-flows-panel",
  imports: [
    QaV3StudioComponent,
    PillSelectComponent,
    ConfirmDialogComponent,
    RouterLink,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  styleUrls: ["./qa-flows-panel.css", "./qa-v3-studio.css"],
  encapsulation: ViewEncapsulation.None,
  template: `
    <div
      class="qa-flows-panel qa-flows-layout"
      [class.has-mobile-inspector]="mobileInspectorOpen()"
      data-testid="qa-flows-panel"
    >
      <!-- TOP SUB-HEADER TOOLBAR -->
      <header
        class="qa-studio-toolbar"
        role="toolbar"
        aria-label="Workflow studio toolbar"
        data-testid="qa-studio-toolbar"
      >
        <div class="qa-toolbar-top-row">
          <div class="qa-toolbar-identity">
            <button
              type="button"
              class="btn-text qa-mobile-back"
              data-testid="qa-mobile-back"
              [hidden]="!mobileInspectorOpen()"
              (click)="mobileInspectorOpen.set(false)"
            >
              ← Flows
            </button>
            <g-pill-select
              testid="qa-flow-switcher"
              ariaLabel="Flow package version"
              [options]="flowSwitcherOptions()"
              [value]="selectedHash() || ''"
              (valueChange)="selectVersion($event, true)"
            />
            @if (selectedDetail(); as detail) {
              <h2 class="qa-toolbar-title" data-testid="qa-flow-insp-title">
                {{ detail.label || detail.key }}
                <span class="s-mono">&#64;{{ detail.version }}</span>
              </h2>
              <div class="qa-toolbar-badges">
                @if (qaEnabled() && activeFlowId() === detail.key) {
                  <span class="badge active-badge"
                    >Active ({{ activeRolloutPct() }}%)</span
                  >
                } @else {
                  <span class="badge active-badge">Available</span>
                }
                @if (detail.builtIn) {
                  <span class="badge builtin-badge">Built-in</span>
                } @else {
                  <span class="badge custom-badge">Custom</span>
                }
                <span
                  class="badge severity-pill"
                  data-testid="qa-schema-pill"
                  >{{ schemaShortBadge(detail) }}</span
                >
                @if (detail.retiredAt) {
                  <span class="badge retired-badge">Retired</span>
                }
                @if (detail.sourceKind === "adopt" && detail.sourceRepo) {
                  <span class="s-mono badge severity-pill"
                    >{{ detail.sourceRepo }}&#64;{{
                      (detail.sourceCommit || "").slice(0, 7)
                    }}</span
                  >
                }
              </div>
            }
          </div>

          <div class="qa-toolbar-right">
            <div
              class="qa-prompt-budget byte-counter"
              [class.error]="skillByteOverflow() || promptBudgetOverflow()"
              data-testid="qa-prompt-budget"
            >
              <span>Prompt budget:</span>
              <strong class="s-num">{{ activePromptBytes() }} B</strong>
              <span>/ 8.0 kB ({{ maxPromptBudgetBytes }} B)</span>
              <span class="qa-budget-track" aria-hidden="true">
                <span
                  class="qa-budget-fill"
                  [style.width.%]="promptBudgetPct()"
                ></span>
              </span>
            </div>

            <div class="detail-actions">
              <button
                type="button"
                class="btn-outlined"
                data-testid="qa-v3-open-probe"
                (click)="openProbe()"
              >
                What-if probe
              </button>
              <button
                type="button"
                class="btn-tonal"
                data-testid="qa-dry-run-btn"
                (click)="isEditing() ? dryRunDraft() : dryRun()"
              >
                Dry run
              </button>
              @if (canAdmin()) {
                <button
                  type="button"
                  class="btn-outlined"
                  data-testid="qa-v3-open-adopt"
                  (click)="openAdopt()"
                >
                  Adopt from commit
                </button>
                <button
                  type="button"
                  class="btn-tonal"
                  data-testid="qa-fork-flow-btn"
                  (click)="forkFlow()"
                >
                  Fork flow
                </button>
                <button
                  type="button"
                  class="btn-tonal"
                  data-testid="qa-new-version-btn"
                  (click)="newVersion()"
                >
                  New version
                </button>
              }
              @if (selectedDetail(); as detail) {
                <a
                  class="btn-tonal"
                  [routerLink]="['/telemetry']"
                  [queryParams]="{
                    tab: 'quality',
                    createExp: '1',
                    flow: detail.hash,
                  }"
                  >Test in experiment</a
                >
                @if (canAdmin() && !detail.builtIn && !detail.retiredAt) {
                  <button
                    type="button"
                    class="btn-text btn-retire"
                    (click)="retireFlow()"
                  >
                    Retire
                  </button>
                }
              }
              <a
                class="btn-text"
                routerLink="/settings/tasks"
                data-testid="qa-configure-rollout-link"
                >Settings → Tasks</a
              >
            </div>
          </div>
        </div>

        <!-- FLOW PACKAGE QUICK-SELECT STRIP -->
        @if (!isEditing()) {
          <div
            class="qa-flows-list"
            role="navigation"
            aria-label="Quality flow versions"
            data-testid="quality-flows-list"
          >
            @for (group of groupedVersions(); track group.key) {
              @for (v of group.versions; track v.hash) {
                <button
                  type="button"
                  class="flow-item"
                  [attr.data-testid]="
                    v.hash === group.latestActiveHash
                      ? 'qa-flow-row-' + group.key
                      : null
                  "
                  [class.selected]="v.hash === selectedHash()"
                  [attr.aria-current]="
                    v.hash === selectedHash() ? 'true' : null
                  "
                  (click)="selectVersion(v.hash, true)"
                >
                  <span class="s-mono"
                    >{{ group.key }}&#64;{{ v.version }}</span
                  >
                  @if (
                    qaEnabled() &&
                    activeFlowId() === v.key &&
                    v.hash === group.latestActiveHash
                  ) {
                    <span class="badge active-badge"
                      >Active ({{ activeRolloutPct() }}%)</span
                    >
                  } @else if (v.hash === group.latestActiveHash) {
                    <span class="badge active-badge">Available</span>
                  }
                  @if (v.builtIn) {
                    <span class="badge builtin-badge">Built-in</span>
                  } @else {
                    <span class="badge custom-badge">Custom</span>
                  }
                </button>
              }
            }
          </div>
        }

        <!-- IN-PLACE DRAFT EDITING DIRTY BAR -->
        @if (isEditing()) {
          <div class="editor-header" data-testid="qa-draft-dirty-bar">
            <div class="qa-draft-meta-fields">
              <strong>{{ isFork() ? "Fork flow" : "New version" }}</strong>
              <label for="qa-flow-key-input">
                Key
                <input
                  id="qa-flow-key-input"
                  type="text"
                  class="s-mono"
                  [value]="draftDeclaration().key"
                  (input)="updateDraft('key', $event)"
                  [disabled]="!isFork()"
                />
              </label>
              <label for="qa-flow-lbl-input">
                Label
                <input
                  id="qa-flow-lbl-input"
                  type="text"
                  [value]="draftDeclaration().label"
                  (input)="updateDraft('label', $event)"
                />
              </label>
              <label for="qa-flow-sum-input">
                Summary
                <input
                  id="qa-flow-sum-input"
                  type="text"
                  [value]="draftDeclaration().summary || ''"
                  (input)="updateDraft('summary', $event)"
                />
              </label>
              <label for="qa-flow-skill-textarea">
                Coding skill ({{ draftSkillBytes() }} /
                {{ draftDeclaration().skill.maxBytes }} B)
                <input
                  id="qa-flow-skill-textarea"
                  type="text"
                  class="s-mono"
                  [value]="draftDeclaration().skill.text"
                  (input)="updateDraft('skill.text', $event)"
                />
              </label>
            </div>
            <div class="editor-actions">
              <button
                type="button"
                class="btn-text"
                data-testid="qa-cancel-edit-btn"
                (click)="cancelEdit()"
              >
                Cancel
              </button>
              <button
                type="button"
                class="btn-tonal"
                data-testid="qa-v3-verify-dry-run"
                (click)="dryRunDraft()"
              >
                Verify &amp; dry-run
              </button>
              <button
                type="button"
                class="btn-primary"
                data-testid="qa-publish-version-btn"
                [disabled]="skillByteOverflow() || promptBudgetOverflow()"
                (click)="confirmPublish()"
              >
                Publish version
              </button>
            </div>
          </div>
        }
      </header>

      <!-- 30/70 8-STAGE STUDIO WORKBENCH -->
      <div class="qa-flows-detail">
        @if (selectedDetail() || isEditing()) {
          <g-qa-v3-studio
            [embedded]="true"
            [detail]="selectedDetail()"
            [editing]="isEditing()"
            [decl]="
              draftDeclaration().declarationV3 ??
              selectedDetail()?.declarationV3
            "
            [declV4]="
              draftDeclaration().declarationV4 ??
              selectedDetail()?.declarationV4
            "
            [showParentDiff]="showParentDiff()"
            [diffFiles]="diffFiles()"
            [externalDryRun]="dryRunResult()"
            (adopted)="loadVersions()"
            (toggleParentDiffReq)="toggleParentDiff()"
          />
        } @else {
          <div class="empty-detail">Select a flow version from the list.</div>
        }
      </div>
    </div>

    @if (confirmRequest()) {
      <g-confirm-dialog
        [request]="confirmRequest()!"
        (done)="onConfirmDone($event)"
      />
    }
  `,
})
export class QaFlowsPanelComponent implements OnDestroy {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);
  private readonly route = inject(ActivatedRoute);
  private readonly router = inject(Router, { optional: true });
  private readonly querySub?: Subscription;

  readonly studio = viewChild(QaV3StudioComponent);

  readonly maxPromptBudgetBytes = MAX_PROMPT_BUDGET_BYTES;

  readonly versions = signal<QAFlowVersionItem[]>([]);
  readonly selectedHash = signal<string | null>(null);
  readonly selectedDetail = signal<QAFlowVersionDetail | null>(null);
  readonly qaEnabled = signal(false);
  readonly activeFlowId = signal("");
  readonly activeRolloutPct = signal(0);
  readonly mobileInspectorOpen = signal(false);

  readonly isEditing = signal(false);
  readonly isFork = signal(false);
  readonly showParentDiff = signal(false);
  readonly jumpSection = signal("basics");

  readonly dryRunResult = signal<QADryRunResponse | null>(null);
  readonly confirmRequest = signal<ConfirmRequest | null>(null);

  readonly draftDeclaration = signal<FlowDeclarationV2>({
    schema: "grain.dev/qa-flow/v2",
    key: "",
    label: "",
    skill: { mode: "replace", text: "", maxBytes: 1800 },
    stopNudge: true,
    stagingIsolation: true,
    checks: {},
    verifier: {
      enabled: true,
      tiers: ["T1", "T2", "T3"],
      maxRepairRounds: 1,
      maxSeconds: 60,
      maxTokens: 50000,
      checklist: "escape-classes-v1",
    },
  });

  readonly draftChecks = signal<
    Record<
      string,
      { enabled: boolean; severity: string; params?: Record<string, unknown> }
    >
  >({});
  readonly draftCheckParams = signal<Record<string, Record<string, unknown>>>(
    {},
  );

  readonly verifierTiers = ["T1", "T2", "T3"];

  readonly builtInCheckKeys = [
    "golden:test-integrity-guard",
    "golden:strict-config-decoder",
    "golden:sibling-export-hazard",
    "golden:ui-budget-and-spec-floor",
    "golden:exported-surface-delta",
    "golden:test-hygiene",
    "golden:mutation-hardening",
    "golden:boundary-fuzz",
  ];

  readonly canAdmin = computed(() =>
    this.access.can("POST /api/qa/flow-versions"),
  );

  readonly draftSkillBytes = computed(() =>
    this.byteCount(this.draftDeclaration().skill?.text || ""),
  );

  readonly skillByteOverflow = computed(
    () =>
      this.isEditing() &&
      this.draftSkillBytes() >
        (this.draftDeclaration().skill?.maxBytes || MAX_PROMPT_BUDGET_BYTES),
  );

  readonly activePromptBytes = computed(() => {
    const st = this.studio();
    if (st) {
      const studioBytes = st.activePromptBytes();
      if (this.isEditing()) {
        return Math.max(studioBytes, this.draftSkillBytes());
      }
      return studioBytes;
    }
    const det = this.selectedDetail();
    const stages = buildClientWorkflowStagesView(det);
    return computeActivePromptBytes(
      stages,
      this.isEditing()
        ? this.draftSkillBytes()
        : det
          ? this.readonlySkillBytes(det)
          : 0,
    );
  });

  readonly promptBudgetPct = computed(() =>
    Math.min(
      100,
      Math.round((this.activePromptBytes() / MAX_PROMPT_BUDGET_BYTES) * 100),
    ),
  );

  readonly promptBudgetOverflow = computed(
    () => this.activePromptBytes() > MAX_PROMPT_BUDGET_BYTES,
  );

  readonly flowSwitcherOptions = computed<PillOption[]>(() => {
    const opts: PillOption[] = [];
    for (const group of this.groupedVersions()) {
      for (const v of group.versions) {
        opts.push({
          value: v.hash,
          label: `${v.key}@${v.version}`,
        });
      }
    }
    return opts;
  });

  readonly groupedVersions = computed(() => {
    const list = this.versions();
    const groups = new Map<string, QAFlowVersionItem[]>();
    for (const v of list) {
      if (!groups.has(v.key)) groups.set(v.key, []);
      groups.get(v.key)!.push(v);
    }
    return Array.from(groups.entries())
      .map(([key, vers]) => {
        const sorted = [...vers].sort((a, b) => b.version - a.version);
        const latestActive = sorted.find((v) => !v.retiredAt);
        return {
          key,
          versions: sorted,
          latestActiveHash: latestActive?.hash,
        };
      })
      .sort((a, b) => a.key.localeCompare(b.key));
  });

  readonly diffFiles = computed(() => {
    const detail = this.selectedDetail();
    let oldT = detail ? this.readonlySkillText(detail) : "";
    let newT =
      this.studio()?.codingSkillText() ||
      this.draftDeclaration().skill?.text ||
      "";
    if (!this.isEditing() && detail) {
      const parentRef =
        detail.parentHash ||
        detail.declarationV4?.extends ||
        detail.declaration?.extends ||
        detail.lineage?.[0]?.hash ||
        "";
      const parentVer = parentRef
        ? this.versions().find(
            (v) =>
              v.hash === parentRef ||
              `${v.key}@${v.version}` === parentRef ||
              v.key === parentRef,
          )
        : undefined;
      oldT = parentVer ? this.readonlySkillText(parentVer) : "";
      newT = this.readonlySkillText(detail);
    }
    if (oldT === newT) return [];

    const lines: DiffPreviewLine[] = [];
    const oldLines = oldT ? oldT.split("\n") : [];
    const newLines = newT ? newT.split("\n") : [];

    for (const l of oldLines) {
      lines.push({ kind: "del", text: l });
    }
    for (const l of newLines) {
      lines.push({ kind: "add", text: l });
    }

    return [
      {
        path: "skill.text",
        status: "modified",
        additions: newLines.length,
        deletions: oldLines.length,
        hunks: [
          {
            header: "@@ -1 +1 @@",
            lines,
          },
        ],
      } as DiffPreviewFile,
    ];
  });

  constructor() {
    void this.loadVersions();

    const qpObs = (
      this.route as unknown as {
        queryParamMap?: {
          subscribe?: (
            fn: (m: { get: (k: string) => string | null }) => void,
          ) => Subscription;
        };
      }
    ).queryParamMap;
    if (qpObs && typeof qpObs.subscribe === "function") {
      this.querySub = qpObs.subscribe((m) => {
        const flowParam = m.get("flow") || m.get("from");
        if (this.versions().length === 0) return;
        if (flowParam) {
          const h = this.resolveVersionParam(flowParam);
          if (h && h !== this.selectedHash()) {
            void this.doSelectVersion(h);
          }
        } else if (this.activeFlowId()) {
          const active = this.versions().find(
            (v) => v.key === this.activeFlowId() && !v.retiredAt,
          );
          if (active && active.hash !== this.selectedHash()) {
            void this.doSelectVersion(active.hash);
          }
        }
      });
    }
  }

  ngOnDestroy(): void {
    this.querySub?.unsubscribe();
  }

  private resolveVersionParam(raw: string): string {
    if (raw.includes("@")) {
      const [key, ver] = raw.split("@");
      const v = this.versions().find(
        (item) => item.key === key && item.version === Number(ver),
      );
      if (v) return v.hash;
    }
    const byKey = this.versions().find((item) => item.key === raw);
    if (byKey) return byKey.hash;
    return raw;
  }

  schemaShortBadge(detail: QAFlowVersionDetail): string {
    if (
      this.isEditing() &&
      this.draftDeclaration().schema === "grain.dev/qa-flow/v4"
    ) {
      return "v4";
    }
    const sv =
      detail.schemaVer ||
      detail.declarationV4?.schema ||
      detail.declarationV3?.schema ||
      detail.declaration?.schema ||
      "";
    if (sv.endsWith("/v4") || detail.key.includes("v4")) return "v4";
    if (sv.endsWith("/v3") || detail.key.includes("v3")) return "v3";
    return "v2";
  }

  byteCount(text: string): number {
    return utf8ByteCount(text);
  }

  readonlySkillText(detail: QAFlowVersionDetail): string {
    return (
      detail.declaration?.skill?.text ||
      detail.sourceYaml ||
      detail.summary ||
      ""
    );
  }

  readonlySkillBytes(detail: QAFlowVersionDetail): number {
    if (detail.declaration?.skill?.text) {
      return this.byteCount(detail.declaration.skill.text);
    }
    return detail.resolved?.skillBytes ?? this.byteCount(detail.summary || "");
  }

  isFloorCheck(checkId: string): boolean {
    return isLockedFloorRule(checkId);
  }

  toggleParentDiff(): void {
    this.showParentDiff.update((v) => !v);
  }

  openProbe(): void {
    this.studio()?.showProbe.set(true);
  }

  openAdopt(): void {
    this.studio()?.showAdopt.set(true);
  }

  async loadVersions(): Promise<void> {
    if (this.access.can("GET /api/settings")) {
      try {
        const settings = (await this.api.get("GET /api/settings")) as {
          qaEnabled?: boolean;
          qaFlowId?: string;
          qaRolloutPct?: number;
        } | null;
        if (settings) {
          this.qaEnabled.set(Boolean(settings.qaEnabled));
          this.activeFlowId.set(settings.qaFlowId ?? "");
          this.activeRolloutPct.set(settings.qaRolloutPct ?? 0);
        }
      } catch {
        // Optional settings read when operator holds narrower scope.
      }
    }

    let loaded: QAFlowVersionItem[] = [];
    try {
      const res = (await this.api.get<QAFlowVersionsResponse>(
        "GET /api/qa/flow-versions",
        undefined,
        "limit=50",
      )) as QAFlowVersionsResponse | null;
      loaded = Array.isArray(res?.versions) ? res.versions : [];
    } catch {
      loaded = [];
    }

    if (
      loaded.length === 0 ||
      loaded.some((v) => v.key === "golden-v1-minimal" && v.builtIn)
    ) {
      let fallback: QAFlowsResponse | null = null;
      try {
        fallback = (await this.api.get<QAFlowsResponse>(
          "GET /api/qa/flows",
        )) as QAFlowsResponse | null;
      } catch {
        fallback = null;
      }
      for (const f of fallback?.flows ?? []) {
        const existingIdx = loaded.findIndex((x) => x.key === f.id);
        if (existingIdx >= 0) {
          if (
            f.label &&
            loaded[existingIdx].label === loaded[existingIdx].key
          ) {
            loaded[existingIdx] = {
              ...loaded[existingIdx],
              label: f.label,
            };
          }
          continue;
        }
        loaded.push({
          hash: f.id,
          key: f.id,
          version: 1,
          label: f.label,
          summary: f.summary,
          qaEngineRev: 1,
          builtIn: true,
          resolved: f,
        });
      }
    }

    this.versions.set(loaded);
    if (!this.selectedHash() && loaded.length > 0) {
      const qm = this.route.snapshot?.queryParamMap;
      const fromParam = qm?.get("from") || qm?.get("flow");
      if (fromParam) {
        const resolved = this.resolveVersionParam(fromParam);
        if (resolved) {
          await this.doSelectVersion(resolved);
          return;
        }
      }
      const activeMatch = this.activeFlowId()
        ? loaded.find((v) => v.key === this.activeFlowId() && !v.retiredAt)
        : undefined;
      const firstHash = activeMatch?.hash ?? loaded[0].hash;
      await this.doSelectVersion(firstHash);
    }
  }

  async selectVersion(hash: string, fromUserClick = false): Promise<void> {
    if (this.isEditing()) {
      this.confirmRequest.set({
        title: "Unsaved changes",
        message: "You have unsaved changes. Discard them?",
        accept: "Discard",
        destructive: true,
        run: async () => {
          await this.doSelectVersion(hash, fromUserClick);
        },
      });
    } else {
      await this.doSelectVersion(hash, fromUserClick);
    }
  }

  async doSelectVersion(hash: string, fromUserClick = false): Promise<void> {
    this.selectedHash.set(hash);
    this.isEditing.set(false);
    this.dryRunResult.set(null);
    if (fromUserClick) {
      this.mobileInspectorOpen.set(true);
    }
    const local = this.versions().find((x) => x.hash === hash);
    if (fromUserClick && local && this.router) {
      const qpObs = (this.route as unknown as { queryParamMap?: unknown })
        .queryParamMap;
      if (qpObs) {
        void this.router.navigate([], {
          relativeTo: this.route,
          queryParams: { flow: local.key },
          queryParamsHandling: "merge",
        });
      }
    }
    try {
      const detail = (await this.api.get<QAFlowVersionDetail>(
        "GET /api/qa/flow-versions/{hash}",
        { hash },
      )) as QAFlowVersionDetail | null;
      if (detail && detail.hash) {
        this.selectedDetail.set({
          ...local,
          ...detail,
          label:
            local?.label && detail.label === detail.key
              ? local.label
              : detail.label,
          resolved:
            detail.resolved ??
            (detail as unknown as { spec?: QAFlowVersionDetail["resolved"] })
              .spec ??
            local?.resolved,
        });
        return;
      }
    } catch {
      // Fall back to cached list item below.
    }
    if (local) {
      this.selectedDetail.set(local);
    }
  }

  newVersion(): void {
    this.isEditing.set(true);
    this.isFork.set(false);
    this.initDraft();
  }

  forkFlow(): void {
    this.isEditing.set(true);
    this.isFork.set(true);
    this.initDraft();
  }

  initDraft(): void {
    const detail = this.selectedDetail();
    if (!detail) return;
    const base: FlowDeclarationV2 = detail.declaration ?? {
      schema: "grain.dev/qa-flow/v2",
      key: detail.key,
      extends: detail.hash,
      label: detail.label,
      summary: detail.summary,
      skill: {
        mode: "replace",
        text: detail.summary || "",
        maxBytes: 1800,
      },
      stopNudge: detail.resolved?.stopNudge || false,
      stagingIsolation: detail.resolved?.stagingIsolation || false,
      checks: {},
      verifier: {
        enabled: detail.resolved?.enableS4 || false,
        tiers: ["T1", "T2", "T3"],
        maxRepairRounds: detail.resolved?.maxS4RepairRounds || 1,
        maxSeconds: 60,
        maxTokens: 50000,
        checklist: "escape-classes-v1",
      },
    };

    const draft = structuredClone(base);
    if (
      detail.schemaVer === "grain.dev/qa-flow/v4" ||
      detail.declarationV4 ||
      detail.key.includes("v4")
    ) {
      draft.schema = "grain.dev/qa-flow/v4";
      draft.declarationV4 = detail.declarationV4;
    } else if (
      detail.schemaVer === "grain.dev/qa-flow/v3" ||
      detail.declarationV3 ||
      detail.key.startsWith("golden-v3")
    ) {
      draft.schema = "grain.dev/qa-flow/v3";
      draft.declarationV3 = detail.declarationV3;
    }
    if (this.isFork()) {
      draft.key = detail.key + "-fork";
    } else {
      draft.key = detail.key;
    }
    draft.extends = detail.hash;

    const checks: Record<string, { enabled: boolean; severity: string }> = {};
    const params: Record<string, Record<string, unknown>> = {};
    for (const c of this.builtInCheckKeys) {
      if (this.isFloorCheck(c)) {
        checks[c] = { enabled: true, severity: "blocking" };
      } else {
        checks[c] = {
          enabled: draft.checks?.[c]?.enabled ?? false,
          severity: draft.checks?.[c]?.severity || "blocking",
        };
        params[c] = { ...(draft.checks?.[c]?.params || {}) };
      }
    }
    this.draftChecks.set(checks);
    this.draftCheckParams.set(params);
    this.draftDeclaration.set(draft);
  }

  cancelEdit(): void {
    this.confirmRequest.set({
      title: "Unsaved changes",
      message: "You have unsaved changes. Discard them?",
      accept: "Discard",
      destructive: true,
      run: async () => {
        this.isEditing.set(false);
      },
    });
  }

  updateDraft(field: string, event: Event): void {
    const val = (event.target as HTMLInputElement | HTMLTextAreaElement).value;
    this.draftDeclaration.update((prev) => {
      const next: FlowDeclarationV2 = {
        ...prev,
        skill: { ...prev.skill },
        verifier: { ...prev.verifier, tiers: [...prev.verifier.tiers] },
      };
      if (field === "key") next.key = val;
      if (field === "extends") next.extends = val;
      if (field === "label") next.label = val;
      if (field === "summary") next.summary = val;
      if (field === "skill.text") next.skill.text = val;
      if (field === "verifier.maxSeconds") {
        next.verifier.maxSeconds = Number(val);
      }
      if (field === "verifier.maxTokens") {
        next.verifier.maxTokens = Number(val);
      }
      return next;
    });
  }

  updateCheckParam(check: string, param: string, event: Event): void {
    const val = Number((event.target as HTMLInputElement).value);
    this.draftCheckParams.update((prev) => ({
      ...prev,
      [check]: { ...(prev[check] || {}), [param]: val },
    }));
  }

  toggleCheckEnabled(check: string): void {
    this.draftChecks.update((prev) => {
      const cur = prev[check] || { enabled: false, severity: "blocking" };
      return {
        ...prev,
        [check]: { ...cur, enabled: !cur.enabled },
      };
    });
  }

  toggleVerifierEnabled(): void {
    this.draftDeclaration.update((prev) => ({
      ...prev,
      verifier: { ...prev.verifier, enabled: !prev.verifier.enabled },
    }));
  }

  updateSkillMode(mode: string): void {
    this.draftDeclaration.update((prev) => ({
      ...prev,
      skill: {
        ...prev.skill,
        mode: mode as "replace" | "inherit" | "append",
      },
    }));
  }

  updateCheckSeverity(check: string, sev: string): void {
    this.draftChecks.update((prev) => {
      const cur = prev[check] || { enabled: false, severity: "blocking" };
      return {
        ...prev,
        [check]: { ...cur, severity: sev },
      };
    });
  }

  updateVerifierChecklist(val: string): void {
    this.draftDeclaration.update((prev) => ({
      ...prev,
      verifier: { ...prev.verifier, checklist: val },
    }));
  }

  toggleTier(tier: string): void {
    this.draftDeclaration.update((prev) => {
      const tiers = prev.verifier.tiers.includes(tier)
        ? prev.verifier.tiers.filter((t) => t !== tier)
        : [...prev.verifier.tiers, tier];
      return {
        ...prev,
        verifier: { ...prev.verifier, tiers },
      };
    });
  }

  scrollToSection(section: string): void {
    this.jumpSection.set(section);
    const el = document.getElementById("section-" + section);
    if (el) el.scrollIntoView({ behavior: "smooth" });
  }

  prepareDraft(): FlowDeclarationV2 & Partial<FlowDeclarationV4> {
    const draft = structuredClone(
      this.draftDeclaration(),
    ) as FlowDeclarationV2 & Partial<FlowDeclarationV4>;
    delete draft.declarationV3;
    delete draft.declarationV4;

    const checks = this.draftChecks();
    const params = this.draftCheckParams();
    draft.checks = {};
    for (const c of this.builtInCheckKeys) {
      if (this.isFloorCheck(c)) continue;
      if (checks[c]?.enabled) {
        draft.checks[c] = {
          enabled: true,
          severity: (checks[c].severity || "blocking") as
            "blocking" | "warn" | "shadow",
        };
        if (params[c] && Object.keys(params[c]).length > 0) {
          draft.checks[c].params = { ...params[c] };
        }
      }
    }

    const st = this.studio();
    if (
      st &&
      (draft.schema === "grain.dev/qa-flow/v4" ||
        draft.schema === "grain.dev/qa-flow/v3" ||
        st.skillSteps().length > 0 ||
        st.customChecks().length > 0)
    ) {
      const v4 = st.exportDeclarationV4({
        key: draft.key,
        extends: draft.extends,
        label: draft.label,
        summary: draft.summary,
      });
      draft.schema = "grain.dev/qa-flow/v4";
      if (v4.skill) {
        draft.skill = v4.skill;
      }
      draft.stages = v4.stages;
      draft.decompose = v4.decompose;
      draft.context = v4.context;
      draft.readabilityPacks = v4.readabilityPacks;
      draft.skillSteps = v4.skillSteps;
      draft.prContract = v4.prContract;
      if (v4.customChecks?.length) {
        draft.customChecks = v4.customChecks;
      } else {
        delete draft.customChecks;
        delete draft["custom-checks"];
      }
    }

    return draft;
  }

  async dryRunDraft(): Promise<void> {
    try {
      const decl = this.prepareDraft();
      const res = await this.api.post<QADryRunResponse>(
        "POST /api/qa/flow-versions",
        undefined,
        { declaration: decl },
        "dryRun=1&limit=20",
      );
      this.dryRunResult.set(res);
      this.snackbar.show("Dry run complete");
    } catch {
      this.snackbar.show("Dry run failed");
    }
  }

  async dryRun(): Promise<void> {
    const detail = this.selectedDetail();
    if (!detail) return;
    try {
      const body = detail.declarationV4
        ? { declaration: detail.declarationV4 }
        : detail.declaration
          ? { declaration: detail.declaration }
          : { yaml: detail.sourceYaml || "" };
      const res = await this.api.post<QADryRunResponse>(
        "POST /api/qa/flow-versions",
        undefined,
        body,
        "dryRun=1&limit=20",
      );
      this.dryRunResult.set(res);
      this.snackbar.show("Dry run complete");
    } catch {
      this.snackbar.show("Dry run failed");
    }
  }

  confirmPublish(): void {
    const decl = this.prepareDraft();
    const activeBlocking =
      Object.values(decl.checks || {}).filter((c) => c.severity === "blocking")
        .length + 2;
    const skillBytes = this.byteCount(decl.skill.text);
    this.confirmRequest.set({
      title: "Publish version",
      message: `Publish ${decl.key} (extends ${decl.extends || "none"})?\nSkill size: ${skillBytes} bytes.\nActive blocking checks: ${activeBlocking}.`,
      accept: "Publish",
      destructive: false,
      run: async () => {
        try {
          await this.api.post("POST /api/qa/flow-versions", undefined, {
            declaration: decl,
          });
          this.snackbar.show("Version published");
          this.isEditing.set(false);
          await this.loadVersions();
        } catch {
          this.snackbar.show("Publish failed");
        }
      },
    });
  }

  retireFlow(): void {
    const detail = this.selectedDetail();
    if (!detail) return;
    this.confirmRequest.set({
      title: "Retire flow",
      message: `Retire version ${detail.key}@${detail.version} (${detail.hash})?`,
      accept: "Retire",
      destructive: true,
      run: async () => {
        try {
          await this.api.post("POST /api/qa/flow-versions", undefined, {
            action: "retire",
            hash: detail.hash,
          });
          this.snackbar.show("Flow retired");
          await this.loadVersions();
        } catch {
          this.snackbar.show("Retire failed");
        }
      },
    });
  }

  onConfirmDone(accepted: boolean): void {
    const req = this.confirmRequest();
    this.confirmRequest.set(null);
    if (accepted && req) {
      void req.run();
    }
  }
}
