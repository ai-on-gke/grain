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
import type { TaskDetail } from "../api/models";
import { AccessService } from "../services/access.service";
import { TaskFeedStore } from "../services/task-feed.service";
import { errorText } from "../sessions/session-view";
import {
  type Composition,
  type CreateScheduleRequest,
  type CreateSuiteRequest,
  type CreateTemplateRequest,
  type DeclarationAcceptanceView,
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
  isOpenTaskState,
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
  readonly reachableRepos = signal<string[]>([]);
  readonly approvedByDefault = signal(false);

  readonly repoDefaults = signal<Record<string, RepoDefaultsView>>({});
  readonly repoWorkflows = signal<Record<string, RepoWorkflowsResponse>>({});
  readonly repoLoading = signal<string | null>(null);

  readonly knownRepos = computed(() => {
    const set = new Set<string>(this.reachableRepos());
    for (const s of this.schedules()) if (s.repo) set.add(s.repo);
    for (const t of this.templates()) {
      if (t.repo) set.add(t.repo);
      if (t.declaredIn?.repo) set.add(t.declaredIn.repo);
    }
    for (const st of this.suites()) {
      if (st.declaredIn?.repo) set.add(st.declaredIn.repo);
    }
    for (const sr of this.suiteRuns()) if (sr.repo) set.add(sr.repo);
    for (const acc of this.acceptances()) if (acc.repo) set.add(acc.repo);
    for (const t of this.taskFeed.tasks()) if (t.repo) set.add(t.repo);
    return [...set].filter(Boolean).sort();
  });

  readonly awaitingAcceptancesCount = computed(
    () => this.acceptances().filter((a) => a.awaiting).length,
  );

  private eventSub: Subscription | null = null;
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private activeCount = 0;

  start(): void {
    this.activeCount++;
    if (this.activeCount > 1) return;
    void this.loadConfigAndRepos();
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
    this.eventSub?.unsubscribe();
    this.eventSub = null;
    if (this.pollTimer) clearInterval(this.pollTimer);
    this.pollTimer = null;
  }

  private async loadConfigAndRepos(): Promise<void> {
    try {
      const cfg = await this.api.config();
      if (cfg) {
        this.access.setConfig(cfg);
        this.approvedByDefault.set(!!cfg["approvedByDefault"]);
        const targetRepos = Array.isArray(cfg["targetRepos"])
          ? (cfg["targetRepos"] as string[])
          : [];
        if (targetRepos.length) {
          this.reachableRepos.update((prev) =>
            [...new Set([...prev, ...targetRepos])].sort(),
          );
        }
      }
    } catch {
      // Non-fatal.
    }

    if (this.access.can("GET /api/deployment/reachable-repos")) {
      try {
        const raw = await this.api.get<unknown>(
          "GET /api/deployment/reachable-repos",
        );
        const list = unwrapList<string | { repo?: string; name?: string }>(
          raw,
          "repos",
        )
          .map((item) =>
            typeof item === "string" ? item : item.repo || item.name || "",
          )
          .filter(Boolean);
        if (list.length) {
          this.reachableRepos.update((prev) =>
            [...new Set([...prev, ...list])].sort(),
          );
        }
      } catch {
        // Optional discovery endpoint.
      }
    }
  }

  async refresh(): Promise<void> {
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

      this.loadError.set(null);
      this.loaded.set(true);
    } catch (err) {
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
    this.suiteRuns.update((list) =>
      list.map((r) => (r.id === id ? detail : r)),
    );
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
    const openTasks = (run.tasks ?? []).filter((rt) =>
      isOpenTaskState(byId.get(rt.taskId)?.state || rt.state),
    );
    for (const rt of openTasks) {
      const st = (byId.get(rt.taskId)?.state || rt.state || "").toLowerCase();
      if (st === "running") {
        await this.api.post("POST /api/tasks/{id}/stop", { id: rt.taskId }, {});
      } else {
        await this.api.post(
          "POST /api/tasks/{id}/close",
          { id: rt.taskId },
          { reason: `Cancelled from suite run #${run.id}` },
        );
      }
    }
    this.api.clearConditionalCache();
    await this.refresh();
    void this.taskFeed.refresh();
    return openTasks.length;
  }

  // --- Repo Workflows & .grain/ Declarations ---

  async loadRepoInspect(repo: string): Promise<void> {
    const clean = repo.trim();
    if (!clean) return;
    const { owner, name } = splitRepo(clean);
    if (!owner || !name) return;
    this.repoLoading.set(clean);
    try {
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
      if (decl) {
        this.repoDefaults.update((m) => ({ ...m, [clean]: decl }));
      }
      if (wf) {
        this.repoWorkflows.update((m) => ({ ...m, [clean]: wf }));
      }
    } finally {
      if (this.repoLoading() === clean) this.repoLoading.set(null);
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
    this.repoDefaults.update((m) => ({ ...m, [repo]: updated }));
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
    this.repoWorkflows.update((m) => ({ ...m, [repo]: updated }));
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
