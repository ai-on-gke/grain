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

import { Injectable, NgZone, computed, inject, signal } from "@angular/core";
import type { Subscription } from "rxjs";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import type { QAFlowSpec, QAFlowsResponse, TaskDetail } from "../api/models";
import { AccessService } from "../services/access.service";
import { TaskFeedStore } from "../services/task-feed.service";
import { errorText } from "../sessions/session-helpers";
import { configuredInstanceRepos } from "../shared/instance-repos";
import {
  type Composition,
  type CreateScheduleRequest,
  type CreateSuiteRequest,
  type CreateTemplateRequest,
  type DeclarationAcceptanceView,
  type DeclaredTrigger,
  type RepoDefaultsView,
  type RepoWorkflow,
  type RepoWorkflowsResponse,
  type Schedule,
  type ScheduleRun,
  type Suite,
  type SuiteRun,
  type Template,
  type UpdateScheduleRequest,
  type UpdateSuiteRequest,
  type UpdateTemplateRequest,
} from "./processes.models";

const POLL_MS = 10000;

function unwrapList<T>(raw: unknown, key?: string): T[] {
  if (Array.isArray(raw)) return raw as T[];
  if (raw && typeof raw === "object") {
    const obj = raw as Record<string, unknown>;
    if (key && Array.isArray(obj[key])) return obj[key] as T[];
    if (Array.isArray(obj["items"])) return obj["items"] as T[];
    if (Array.isArray(obj["repos"])) return obj["repos"] as T[];
  }
  return [];
}

@Injectable({ providedIn: "root" })
export class ProcessesStore {
  private readonly api = inject(GrainApi);
  private readonly events = inject(GrainEvents);
  private readonly access = inject(AccessService);
  private readonly taskFeed = inject(TaskFeedStore);
  private readonly zone = inject(NgZone);

  readonly loaded = signal(false);
  readonly loadError = signal<string | null>(null);
  readonly actionError = signal<string | null>(null);

  readonly schedules = signal<Schedule[]>([]);
  readonly templates = signal<Template[]>([]);
  readonly suites = signal<Suite[]>([]);
  readonly suiteRuns = signal<SuiteRun[]>([]);
  readonly acceptances = signal<DeclarationAcceptanceView[]>([]);
  readonly qaFlows = signal<QAFlowSpec[]>([]);
  readonly qaFlowsLoaded = signal(false);
  readonly qaRollout = signal<{
    enabled: boolean;
    flowId: string;
    rolloutPct: number;
    shadowOnControl: boolean;
  } | null>(null);
  readonly configuredRepos = signal<string[]>([]);
  readonly hasTargetAllowlist = signal(false);
  readonly approvedByDefault = signal(false);

  readonly repoDefaults = signal<Record<string, RepoDefaultsView>>({});
  readonly repoWorkflows = signal<Record<string, RepoWorkflowsResponse>>({});
  readonly repoLoading = signal<string | null>(null);

  readonly knownRepos = computed(() => {
    const seen = new Map<string, string>();
    const add = (repo: string | undefined): void => {
      if (!repo) return;
      const trimmed = repo.trim();
      if (!trimmed) return;
      const key = trimmed.toLowerCase();
      if (!seen.has(key)) seen.set(key, trimmed);
    };

    for (const r of this.configuredRepos()) add(r);
    for (const s of this.schedules()) add(s.repo);
    for (const t of this.templates()) {
      add(t.repo);
      add(t.declaredIn?.repo);
    }
    for (const st of this.suites()) {
      add(st.declaredIn?.repo);
    }
    for (const sr of this.suiteRuns()) add(sr.repo);
    for (const acc of this.acceptances()) add(acc.repo);
    // Match classic ui/src/state.js repoRows: only derive repo rows from task
    // history when the deployment has no explicit targetRepos allowlist.
    if (!this.hasTargetAllowlist()) {
      for (const t of this.taskFeed.tasks()) {
        if (!t.consoleSession && !t.interactive) add(t.repo);
      }
    }
    return [...seen.values()].sort((a, b) => a.localeCompare(b));
  });

  // declaringRepos is every repository whose declarations are read for
  // triggers, as the classic page reads them (ui/src/processes.js
  // declaringRepos): the deployment's configured repositories and each one
  // a loaded schedule, template or suite was declared in. A repository can
  // declare nothing but a trigger, which is why the configured ones count.
  readonly declaringRepos = computed(() => {
    const found = new Set(this.configuredRepos().filter(Boolean));
    for (const list of [this.schedules(), this.templates(), this.suites()]) {
      for (const it of list) {
        if (it.declaredIn?.repo) found.add(it.declaredIn.repo);
      }
    }
    return [...found].sort((a, b) => a.localeCompare(b));
  });

  // triggers is each .grain/triggers file a declaring repository's synced
  // declarations name (RepoDeclarationsStatus.triggers), enriched with
  // parsed PathTrigger metadata when triggerDetails is present.
  readonly triggers = computed(() => {
    const out: DeclaredTrigger[] = [];
    const defaults = this.repoDefaults();
    for (const repo of this.declaringRepos()) {
      const status = defaults[repo]?.declarationsStatus;
      const byPath = new Map(
        (status?.triggerDetails ?? []).map((d) => [d.path, d]),
      );
      const paths =
        status?.triggers ?? (status?.triggerDetails ?? []).map((d) => d.path);
      for (const path of paths) {
        const detail = byPath.get(path);
        out.push(detail ? { repo, path, detail } : { repo, path });
      }
    }
    return out;
  });

  readonly awaitingAcceptancesCount = computed(
    () => this.acceptances().filter((a) => a.awaiting).length,
  );

  private eventSub: Subscription | null = null;
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private configTask: Promise<void> | null = null;
  private activeCount = 0;

  start(): void {
    this.actionError.set(null);
    this.activeCount++;
    if (this.activeCount > 1) return;
    this.configTask = this.loadConfigAndRepos().finally(() => {
      this.configTask = null;
    });
    void this.refresh();
    this.eventSub = this.events.events().subscribe((ev) => {
      if (ev.type === "task.state") {
        void this.refresh();
      }
    });
    this.zone.runOutsideAngular(() => {
      this.pollTimer = setInterval(() => {
        this.zone.run(() => void this.refresh());
      }, POLL_MS);
    });
  }

  stop(): void {
    this.activeCount = Math.max(0, this.activeCount - 1);
    if (this.activeCount > 0) return;
    this.declStatusAsked.clear();
    this.eventSub?.unsubscribe();
    this.eventSub = null;
    if (this.pollTimer) clearInterval(this.pollTimer);
    this.pollTimer = null;
  }

  canReadGlobalSettings(): boolean {
    return (
      this.access.can("GET /api/qa/flows") &&
      !this.access.repoLimits()["settings:read"]?.length
    );
  }

  private async refreshQaRollout(): Promise<void> {
    if (
      !this.canReadGlobalSettings() ||
      !this.access.can("GET /api/settings")
    ) {
      return;
    }
    try {
      const s =
        await this.api.get<Record<string, unknown>>("GET /api/settings");
      if (this.activeCount > 0 && s) {
        this.qaRollout.set({
          enabled: !!s["qaEnabled"],
          flowId:
            typeof s["qaFlowId"] === "string" && s["qaFlowId"]
              ? s["qaFlowId"]
              : "golden-v1-minimal",
          rolloutPct:
            typeof s["qaRolloutPct"] === "number" ? s["qaRolloutPct"] : 0,
          shadowOnControl: s["qaShadowOnControl"] !== false,
        });
      }
    } catch {
      // Non-fatal.
    }
  }

  private ensureConfig(): Promise<void> {
    if (this.configTask) return this.configTask;
    if (this.access.configLoaded() || typeof this.api.config !== "function") {
      return Promise.resolve();
    }
    return (this.configTask = this.loadConfigAndRepos().finally(() => {
      this.configTask = null;
    }));
  }

  private async loadConfigAndRepos(): Promise<void> {
    try {
      const cfg = await this.api.config();
      if (this.activeCount === 0) return;
      if (cfg) {
        this.access.setConfig(cfg);
        this.approvedByDefault.set(!!cfg["approvedByDefault"]);
        const summary = configuredInstanceRepos(cfg);
        this.hasTargetAllowlist.set(summary.hasTargetAllowlist);
        this.configuredRepos.set(summary.repos);
        if (this.loaded()) void this.loadDeclarationStatuses();
      }
      if (this.canReadGlobalSettings()) {
        try {
          const flowsRes =
            await this.api.get<QAFlowsResponse>("GET /api/qa/flows");
          if (this.activeCount > 0 && Array.isArray(flowsRes?.flows)) {
            this.qaFlows.set(flowsRes.flows);
          }
        } catch {
          // Non-fatal on older servers.
        }
      }
      if (this.activeCount > 0) {
        this.qaFlowsLoaded.set(true);
      }
      await this.refreshQaRollout();
    } catch {
      if (this.activeCount > 0) {
        this.qaFlowsLoaded.set(true);
      }
    }
  }

  async refresh(): Promise<void> {
    if (this.activeCount === 0) return;
    if (!this.access.configLoaded()) {
      await this.ensureConfig();
      if (this.activeCount === 0) return;
    }
    try {
      const [schedRes, tmplRes, suiteRes, runsRes, accRes] = await Promise.all([
        this.access.can("GET /api/schedules")
          ? this.api.getIfChanged<unknown>("GET /api/schedules")
          : Promise.resolve({ body: [], changed: false }),
        this.access.can("GET /api/templates")
          ? this.api.getIfChanged<unknown>("GET /api/templates")
          : Promise.resolve({ body: [], changed: false }),
        this.access.can("GET /api/suites")
          ? this.api.getIfChanged<unknown>("GET /api/suites")
          : Promise.resolve({ body: [], changed: false }),
        this.access.can("GET /api/suite-runs")
          ? this.api.getIfChanged<unknown>("GET /api/suite-runs")
          : Promise.resolve({ body: [], changed: false }),
        this.access.can("GET /api/declarations")
          ? this.api
              .getIfChanged<unknown>("GET /api/declarations")
              .catch(() => ({ body: [], changed: false }))
          : Promise.resolve({ body: [], changed: false }),
      ]);
      if (this.activeCount === 0) return;

      if (schedRes.body !== null) {
        this.schedules.set(unwrapList<Schedule>(schedRes.body, "schedules"));
      }
      if (tmplRes.body !== null) {
        this.templates.set(unwrapList<Template>(tmplRes.body, "templates"));
      }
      if (suiteRes.body !== null) {
        this.suites.set(unwrapList<Suite>(suiteRes.body, "suites"));
      }
      if (runsRes.body !== null) {
        this.suiteRuns.set(unwrapList<SuiteRun>(runsRes.body, "runs"));
      }
      if (accRes.body !== null) {
        this.acceptances.set(
          unwrapList<DeclarationAcceptanceView>(accRes.body, "declarations"),
        );
      }
      void this.refreshQaRollout();

      this.loadError.set(null);
      this.loaded.set(true);
      void this.loadDeclarationStatuses();
    } catch (err) {
      if (this.activeCount === 0) return;
      this.loaded.set(true);
      this.loadError.set(errorText(err));
    }
  }

  // --- Schedules ---

  fetchScheduleComposition(id: string): Promise<Composition> {
    return this.api.get<Composition>("GET /api/schedules/{id}/composition", {
      id,
    });
  }

  previewDraftSchedule(req: CreateScheduleRequest): Promise<Composition> {
    return this.api.post<Composition>(
      "POST /api/schedules/composition",
      undefined,
      req,
    );
  }

  async createSchedule(req: CreateScheduleRequest): Promise<Schedule> {
    this.actionError.set(null);
    const created = await this.api.post<Schedule>(
      "POST /api/schedules",
      undefined,
      req,
    );
    this.api.clearConditionalCache();
    await this.refresh();
    return created;
  }

  async updateSchedule(
    id: string,
    req: UpdateScheduleRequest,
  ): Promise<Schedule> {
    this.actionError.set(null);
    const updated = await this.api.patch<Schedule>(
      "PATCH /api/schedules/{id}",
      { id },
      req,
    );
    this.api.clearConditionalCache();
    await this.refresh();
    return updated;
  }

  async deleteSchedule(id: string): Promise<void> {
    this.actionError.set(null);
    await this.api.delete("DELETE /api/schedules/{id}", { id });
    this.api.clearConditionalCache();
    await this.refresh();
  }

  async runScheduleNow(id: string): Promise<ScheduleRun> {
    this.actionError.set(null);
    const res = await this.api.post<ScheduleRun>(
      "POST /api/schedules/{id}/run",
      { id },
      {},
    );
    this.api.clearConditionalCache();
    await this.refresh();
    void this.taskFeed.refresh();
    return res;
  }

  // --- Templates ---

  fetchTemplateComposition(
    id: string,
    repo: string,
    base: string,
    approved: boolean,
    fields: Record<string, string>,
  ): Promise<Composition> {
    const params = new URLSearchParams();
    params.set("repo", repo);
    params.set("base", base);
    params.set("approved", approved ? "true" : "false");
    for (const [k, v] of Object.entries(fields)) {
      params.set(`field.${k}`, v);
    }
    return this.api.get<Composition>(
      "GET /api/templates/{id}/composition",
      { id },
      params.toString(),
    );
  }

  async createTemplate(req: CreateTemplateRequest): Promise<Template> {
    this.actionError.set(null);
    const created = await this.api.post<Template>(
      "POST /api/templates",
      undefined,
      req,
    );
    this.api.clearConditionalCache();
    await this.refresh();
    return created;
  }

  async updateTemplate(
    id: string,
    req: UpdateTemplateRequest,
  ): Promise<Template> {
    this.actionError.set(null);
    const updated = await this.api.patch<Template>(
      "PATCH /api/templates/{id}",
      { id },
      req,
    );
    this.api.clearConditionalCache();
    await this.refresh();
    return updated;
  }

  async deleteTemplate(id: string): Promise<void> {
    this.actionError.set(null);
    await this.api.delete("DELETE /api/templates/{id}", { id });
    this.api.clearConditionalCache();
    await this.refresh();
  }

  canLaunchTemplate(t: Template, repo?: string): boolean {
    const targetRepo = t.declaredIn?.repo || t.repo || repo || "";
    if (t.declaredIn?.repo) {
      return (
        this.access.can("POST /api/tasks/from-template", t.declaredIn.repo) ||
        this.access.can("POST /api/tasks", t.declaredIn.repo)
      );
    }
    return this.access.can("POST /api/tasks", targetRepo || undefined);
  }

  async launchTaskFromTemplate(
    t: Template,
    opts: {
      repo: string;
      base: string;
      approved: boolean;
      fields: Record<string, string>;
    },
  ): Promise<TaskDetail> {
    this.actionError.set(null);
    let created: TaskDetail;
    if (
      t.declaredIn?.repo &&
      this.access.can("POST /api/tasks/from-template", t.declaredIn.repo)
    ) {
      created = await this.api.post<TaskDetail>(
        "POST /api/tasks/from-template",
        undefined,
        {
          repo: t.declaredIn.repo,
          template: t.id,
          fields: opts.fields,
          approved: opts.approved,
        },
      );
    } else {
      created = await this.api.post<TaskDetail>("POST /api/tasks", undefined, {
        templateId: t.id,
        fields: opts.fields,
        repo: t.repo ? "" : t.system ? "" : opts.repo.trim(),
        base: t.repo ? "" : t.system ? "" : opts.base.trim(),
        approved: t.interactive ? true : opts.approved,
      });
    }
    void this.taskFeed.refresh();
    return created;
  }

  // --- Suites & Suite Runs ---

  fetchSuiteComposition(
    id: string,
    repo = "",
    base = "",
  ): Promise<Composition> {
    const q = new URLSearchParams();
    if (repo.trim()) q.set("repo", repo.trim());
    if (base.trim()) q.set("base", base.trim());
    return this.api.get<Composition>(
      "GET /api/suites/{id}/composition",
      { id },
      q.toString() || undefined,
    );
  }

  previewDraftSuite(
    req: CreateSuiteRequest,
    repo = "",
    base = "",
  ): Promise<Composition> {
    const q = new URLSearchParams();
    if (repo.trim()) q.set("repo", repo.trim());
    if (base.trim()) q.set("base", base.trim());
    return this.api.post<Composition>(
      "POST /api/suites/composition",
      undefined,
      req,
      q.toString() || undefined,
    );
  }

  async createSuite(req: CreateSuiteRequest): Promise<Suite> {
    this.actionError.set(null);
    const created = await this.api.post<Suite>(
      "POST /api/suites",
      undefined,
      req,
    );
    this.api.clearConditionalCache();
    await this.refresh();
    return created;
  }

  async updateSuite(id: string, req: UpdateSuiteRequest): Promise<Suite> {
    this.actionError.set(null);
    const updated = await this.api.patch<Suite>(
      "PATCH /api/suites/{id}",
      { id },
      req,
    );
    this.api.clearConditionalCache();
    await this.refresh();
    return updated;
  }

  async deleteSuite(id: string): Promise<void> {
    this.actionError.set(null);
    await this.api.delete("DELETE /api/suites/{id}", { id });
    this.api.clearConditionalCache();
    await this.refresh();
  }

  async fetchSuiteRunDetail(id: number): Promise<SuiteRun> {
    const detail = await this.api.get<SuiteRun>("GET /api/suite-runs/{id}", {
      id,
    });
    if (this.activeCount > 0) {
      this.suiteRuns.update((list) =>
        list.map((r) => (r.id === id ? detail : r)),
      );
    }
    return detail;
  }

  async startSuiteRun(
    suiteId: string,
    repo: string,
    base: string,
  ): Promise<SuiteRun> {
    this.actionError.set(null);
    const run = await this.api.post<SuiteRun>(
      "POST /api/suite-runs",
      undefined,
      {
        suiteId,
        repo: repo.trim(),
        base: base.trim(),
      },
    );
    this.api.clearConditionalCache();
    await this.refresh();
    void this.taskFeed.refresh();
    return run;
  }

  async cancelSuiteRunTasks(run: SuiteRun): Promise<number> {
    this.actionError.set(null);
    const byId = new Map(this.taskFeed.tasks().map((t) => [t.id, t]));
    const openTasks = (run.tasks ?? []).filter((rt) => {
      const st = (byId.get(rt.taskId)?.state || rt.state || "").toLowerCase();
      return Boolean(rt.taskId) && st !== "closed" && st !== "merged";
    });
    for (const rt of openTasks) {
      await this.api.post(
        "POST /api/tasks/{id}/close",
        { id: rt.taskId },
        { reason: `Cancelled from suite run #${run.id}` },
      );
    }
    this.api.clearConditionalCache();
    await this.refresh();
    void this.taskFeed.refresh();
    return openTasks.length;
  }

  // --- Repo Workflows & .grain/ Declarations ---

  private readonly declStatusAsked = new Set<string>();

  // loadDeclarationStatuses reads, once per visit, the declarations of
  // each declaring repository not read yet, for its triggers. Selecting a
  // repository on the declarations tab reads it again (loadRepoInspect).
  private async loadDeclarationStatuses(): Promise<void> {
    const repos = this.declaringRepos().filter(
      (r) =>
        !this.declStatusAsked.has(r) &&
        this.access.can("GET /api/repos/{owner}/{name}/declarations", r),
    );
    await Promise.all(
      repos.map(async (repo) => {
        const { owner, name } = splitRepo(repo);
        if (!owner || !name) return;
        this.declStatusAsked.add(repo);
        const decl = await this.api
          .get<RepoDefaultsView>("GET /api/repos/{owner}/{name}/declarations", {
            owner,
            name,
          })
          .catch(() => null);
        if (decl && this.activeCount > 0) {
          this.repoDefaults.update((m) => ({ ...m, [repo]: decl }));
        }
      }),
    );
  }

  async loadRepoInspect(repo: string): Promise<void> {
    const clean = repo.trim();
    if (!clean) return;
    const { owner, name } = splitRepo(clean);
    if (!owner || !name) return;
    this.repoLoading.set(clean);
    try {
      if (!this.access.configLoaded()) {
        await this.ensureConfig();
      }
      const [decl, wf] = await Promise.all([
        this.access.can("GET /api/repos/{owner}/{name}/declarations", clean)
          ? this.api
              .get<RepoDefaultsView>(
                "GET /api/repos/{owner}/{name}/declarations",
                { owner, name },
              )
              .catch(() => null)
          : Promise.resolve(null),
        this.access.can("GET /api/repos/{owner}/{name}/workflows", clean)
          ? this.api
              .get<RepoWorkflowsResponse>(
                "GET /api/repos/{owner}/{name}/workflows",
                { owner, name },
              )
              .catch(() => null)
          : Promise.resolve(null),
      ]);
      if (this.activeCount === 0) return;
      if (decl) {
        this.repoDefaults.update((m) => ({ ...m, [clean]: decl }));
      }
      if (wf) {
        this.repoWorkflows.update((m) => ({ ...m, [clean]: wf }));
      }
    } finally {
      if (this.activeCount > 0 && this.repoLoading() === clean) {
        this.repoLoading.set(null);
      }
    }
  }

  async setRepoDeclarationsEnabled(
    repo: string,
    declarations: boolean,
  ): Promise<RepoDefaultsView> {
    this.actionError.set(null);
    const { owner, name } = splitRepo(repo);
    const updated = await this.api.put<RepoDefaultsView>(
      "PUT /api/repos/{owner}/{name}/declarations",
      { owner, name },
      { declarations },
    );
    if (this.activeCount > 0) {
      this.repoDefaults.update((m) => ({ ...m, [repo]: updated }));
    }
    this.api.clearConditionalCache();
    await this.refresh();
    return updated;
  }

  async saveRepoWorkflows(
    repo: string,
    workflows: RepoWorkflow[],
  ): Promise<RepoWorkflowsResponse> {
    this.actionError.set(null);
    const { owner, name } = splitRepo(repo);
    const updated = await this.api.put<RepoWorkflowsResponse>(
      "PUT /api/repos/{owner}/{name}/workflows",
      { owner, name },
      { workflows },
    );
    if (this.activeCount > 0) {
      this.repoWorkflows.update((m) => ({ ...m, [repo]: updated }));
    }
    return updated;
  }

  async acceptDeclaration(
    id: string,
    seen: string[],
  ): Promise<DeclarationAcceptanceView> {
    this.actionError.set(null);
    const accepted = await this.api.post<DeclarationAcceptanceView>(
      "POST /api/declarations/accept",
      undefined,
      { id, seen },
    );
    this.api.clearConditionalCache();
    await this.refresh();
    return accepted;
  }
}
