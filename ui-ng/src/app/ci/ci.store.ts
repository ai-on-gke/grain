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
import { GrainApi } from "../api/grain-api.service";
import { GrainEvents } from "../api/grain-events.service";
import { AccessService } from "../services/access.service";
import { errorText } from "../sessions/session-view";
import {
  type ActionsCacheFlush,
  type CICacheResponse,
  type CIFault,
  type CIFaultsResponse,
  type CIRun,
  type CIRunArtifact,
  type CIRunArtifactsResponse,
  type CIRunLogResponse,
  type CIRunRequest,
  type CIRunsResponse,
  type CIWorkflowOption,
  type CIWorkflowsResponse,
  type PackageCacheFlush,
  type QuarantineTestRequest,
  type QuarantineTestResponse,
  isRunActive,
} from "./ci.models";

const POLL_MS = 5000;

@Injectable({ providedIn: "root" })
export class CIStore {
  private readonly api = inject(GrainApi);
  private readonly events = inject(GrainEvents);
  private readonly access = inject(AccessService);
  private readonly zone = inject(NgZone);

  readonly loaded = signal(false);
  readonly enabled = signal(true);
  readonly runs = signal<CIRun[]>([]);
  readonly faults = signal<CIFault[]>([]);
  readonly matched = signal(0);
  readonly stored = signal(0);
  readonly actionsNote = signal("");
  readonly targetRepos = signal<string[]>([]);
  readonly loadError = signal<string | null>(null);
  readonly actionError = signal<string | null>(null);

  readonly selectedId = signal<string | null>(null);
  readonly detail = signal<CIRun | null>(null);
  readonly detailLoading = signal(false);
  readonly artifacts = signal<CIRunArtifact[]>([]);
  readonly artifactsAvailable = signal(false);
  readonly jobLogs = signal<Record<string, CIRunLogResponse>>({});
  readonly jobLogLoading = signal<string | null>(null);

  readonly activeRun = computed(() => {
    const id = this.selectedId();
    if (!id) return null;
    const det = this.detail();
    if (det && det.id === id) return det;
    return this.runs().find((r) => r.id === id) ?? null;
  });

  readonly combinedFaults = computed(() => {
    const map = new Map<string, CIFault>();
    for (const f of this.faults()) {
      const key =
        f.digest ||
        `${f.repo ?? ""}:${f.jobId ?? f.jobName ?? ""}:${f.kind}:${f.summary ?? ""}`;
      map.set(key, f);
    }
    return [...map.values()];
  });

  private eventSub: Subscription | null = null;
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private activeCount = 0;

  start(): void {
    this.activeCount++;
    if (this.activeCount > 1) return;
    void this.loadConfig();
    void this.refresh();
    this.eventSub = this.events.events().subscribe((ev) => {
      if (
        ev.type === "task.ci" ||
        ev.type === "task.state" ||
        ev.type.startsWith("ci.")
      ) {
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

  private async loadConfig(): Promise<void> {
    try {
      const cfg = await this.api.config();
      if (cfg) {
        this.access.setConfig(cfg);
        const repos = Array.isArray(cfg["targetRepos"])
          ? (cfg["targetRepos"] as string[])
          : [];
        this.targetRepos.set(repos);
      }
    } catch {
      // Non-fatal: page still renders CI feed.
    }
  }

  async refresh(forceDetail = false): Promise<void> {
    if (!this.access.can("GET /api/ci/runs")) {
      this.loaded.set(true);
      this.loadError.set(this.access.deniedReason("GET /api/ci/runs"));
      return;
    }
    try {
      const [runsRes, faultsRes] = await Promise.all([
        this.api.getIfChanged<CIRunsResponse>("GET /api/ci/runs"),
        this.access.can("GET /api/ci/faults")
          ? this.api
              .getIfChanged<CIFaultsResponse>("GET /api/ci/faults")
              .catch(() => ({ body: null, changed: false }))
          : Promise.resolve({ body: null, changed: false }),
      ]);

      if (runsRes.body) {
        const body = runsRes.body;
        this.enabled.set(body.enabled !== false);
        this.runs.set(Array.isArray(body.runs) ? body.runs : []);
        this.matched.set(body.matched ?? body.runs?.length ?? 0);
        this.stored.set(body.stored ?? body.runs?.length ?? 0);
        this.actionsNote.set(body.actionsNote ?? "");
        const dedicated = faultsRes.body?.faults;
        const merged = [
          ...(Array.isArray(dedicated) ? dedicated : []),
          ...(Array.isArray(body.faults) ? body.faults : []),
        ];
        this.faults.set(merged);
      } else if (faultsRes.body?.faults) {
        this.faults.set(faultsRes.body.faults);
      }
      this.loadError.set(null);
      this.loaded.set(true);

      const sel = this.selectedId();
      if (sel) {
        const cur = this.detail();
        if (forceDetail || !cur || cur.id !== sel || isRunActive(cur)) {
          await this.loadRunDetail(sel, false);
        }
      }
    } catch (err) {
      this.loaded.set(true);
      this.loadError.set(errorText(err));
    }
  }

  async selectRun(id: string | null): Promise<void> {
    if (this.selectedId() === id && this.detail()?.id === id) return;
    this.selectedId.set(id);
    this.jobLogs.set({});
    if (!id) {
      this.detail.set(null);
      this.artifacts.set([]);
      return;
    }
    await this.loadRunDetail(id, true);
  }

  async loadRunDetail(id: string, showSpinner = true): Promise<void> {
    if (!this.access.can("GET /api/ci/runs/{id}")) return;
    if (showSpinner) this.detailLoading.set(true);
    try {
      const [run, artRes] = await Promise.all([
        this.api.get<CIRun>("GET /api/ci/runs/{id}", { id }),
        this.access.can("GET /api/ci/runs/{id}/artifacts")
          ? this.api
              .get<CIRunArtifactsResponse>("GET /api/ci/runs/{id}/artifacts", {
                id,
              })
              .catch(() => null)
          : Promise.resolve(null),
      ]);
      if (this.selectedId() === id) {
        this.detail.set(run);
        if (artRes) {
          this.artifactsAvailable.set(!!artRes.available);
          this.artifacts.set(
            Array.isArray(artRes.artifacts) ? artRes.artifacts : [],
          );
        } else {
          const fromJobs = (run.jobs ?? []).flatMap((j) => j.artifacts ?? []);
          this.artifactsAvailable.set(fromJobs.length > 0);
          this.artifacts.set(fromJobs);
        }
      }
    } catch (err) {
      if (this.selectedId() === id) {
        this.actionError.set(errorText(err));
      }
    } finally {
      if (showSpinner) this.detailLoading.set(false);
    }
  }

  async fetchJobLog(
    runId: string,
    jobNameOrId: string,
    tail?: number,
  ): Promise<void> {
    if (!this.access.can("GET /api/ci/runs/{id}/log")) return;
    const key = `${runId}:${jobNameOrId}`;
    this.jobLogLoading.set(key);
    try {
      const params = new URLSearchParams({ job: jobNameOrId });
      if (tail && tail > 0) params.set("tail", String(tail));
      const res = await this.api.get<CIRunLogResponse>(
        "GET /api/ci/runs/{id}/log",
        { id: runId },
        params.toString(),
      );
      this.jobLogs.update((prev) => ({ ...prev, [key]: res }));
    } catch (err) {
      this.actionError.set(errorText(err));
    } finally {
      if (this.jobLogLoading() === key) this.jobLogLoading.set(null);
    }
  }

  async fetchWorkflows(repo: string, ref: string): Promise<CIWorkflowOption[]> {
    if (!repo.trim() || !this.access.can("GET /api/ci/workflows", repo)) {
      return [];
    }
    const q = new URLSearchParams({ repo: repo.trim() });
    if (ref.trim()) q.set("ref", ref.trim());
    const res = await this.api.get<CIWorkflowsResponse>(
      "GET /api/ci/workflows",
      undefined,
      q.toString(),
    );
    return Array.isArray(res?.workflows) ? res.workflows : [];
  }

  async triggerRun(req: CIRunRequest): Promise<CIRun> {
    this.actionError.set(null);
    const created = await this.api.post<CIRun>(
      "POST /api/ci/runs",
      undefined,
      req,
    );
    this.api.clearConditionalCache();
    await this.refresh(true);
    if (created?.id) {
      await this.selectRun(created.id);
    }
    return created;
  }

  async cancelRun(id: string, reason?: string): Promise<void> {
    this.actionError.set(null);
    const body = reason?.trim() ? { reason: reason.trim() } : undefined;
    await this.api.delete("DELETE /api/ci/runs/{id}", { id }, body);
    this.api.clearConditionalCache();
    await this.refresh(true);
  }

  async flushCICache(): Promise<CICacheResponse> {
    this.actionError.set(null);
    return this.api.delete<CICacheResponse>("DELETE /api/ci/cache");
  }

  async flushActionsCache(repo?: string): Promise<ActionsCacheFlush> {
    this.actionError.set(null);
    const q = repo?.trim()
      ? new URLSearchParams({ repo: repo.trim() }).toString()
      : undefined;
    return this.api.post<ActionsCacheFlush>(
      "POST /api/actions-cache/flush",
      undefined,
      {},
      q,
    );
  }

  async flushPackageCache(kind?: string): Promise<PackageCacheFlush> {
    this.actionError.set(null);
    const q = kind?.trim()
      ? new URLSearchParams({ kind: kind.trim() }).toString()
      : undefined;
    return this.api.post<PackageCacheFlush>(
      "POST /api/package-cache/flush",
      undefined,
      {},
      q,
    );
  }

  async quarantineTest(
    req: QuarantineTestRequest,
  ): Promise<QuarantineTestResponse> {
    this.actionError.set(null);
    const res = await this.api.post<QuarantineTestResponse>(
      "POST /api/tests/quarantine",
      undefined,
      req,
    );
    this.api.clearConditionalCache();
    await this.refresh(true);
    return res;
  }
}
