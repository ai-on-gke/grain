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

import { HttpErrorResponse } from "@angular/common/http";
import { Injectable, computed, inject, signal } from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import type { GrainConfig } from "../api/models";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  type SettingsPlanRow,
  type SettingsSectionId,
  computeSettingsPlan,
  formatSettingValue,
  isConfigRepoSection,
} from "./settings-sections";

export interface ConfigFileChange {
  path: string;
  op: string;
  patch: string;
}

export function extractRefusedChanges(err: unknown): ConfigFileChange[] {
  if (!(err instanceof HttpErrorResponse)) return [];
  const body = err.error;
  if (!body || typeof body !== "object") return [];
  const rec = body as Record<string, unknown>;
  if (rec["code"] !== "config_managed_externally") return [];
  const raw = Array.isArray(rec["change"])
    ? rec["change"]
    : Array.isArray(rec["changes"])
      ? rec["changes"]
      : [];
  return raw
    .filter(
      (c): c is ConfigFileChange =>
        Boolean(c) &&
        typeof c === "object" &&
        typeof (c as ConfigFileChange).path === "string" &&
        typeof (c as ConfigFileChange).op === "string" &&
        typeof (c as ConfigFileChange).patch === "string",
    )
    .map((c) => ({ path: c.path, op: c.op, patch: c.patch }));
}

export function errText(err: unknown): string {
  if (err instanceof HttpErrorResponse) {
    const body = err.error;
    if (typeof body === "string" && body.trim()) return body.trim();
    if (body && typeof body === "object") {
      const msg = (body as Record<string, unknown>)["error"] ?? body["message"];
      if (typeof msg === "string" && msg.trim()) return msg.trim();
    }
    return err.message || `HTTP ${err.status}`;
  }
  if (err instanceof Error) return err.message;
  return String(err ?? "Request failed");
}

export interface ReachableReposState {
  repos: { repo: string; push: boolean }[];
  errors: string[];
  hostMismatches: string[];
  reason: string;
}

const REACHABLE_REPOS_TTL_MS = 60_000;

@Injectable({ providedIn: "root" })
export class SettingsStateService {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);

  readonly config = signal<GrainConfig | null>(null);
  readonly initialized = signal(false);
  readonly settings = signal<Record<string, unknown>>({});
  readonly deployment = signal<Record<string, unknown>>({});
  readonly draft = signal<Record<string, unknown>>({});
  readonly deployDraft = signal<Record<string, unknown>>({});
  readonly fieldErrors = signal<Record<string, string>>({});
  readonly discardEpoch = signal(0);

  readonly loading = signal(false);
  readonly planning = signal(false);
  readonly saving = signal(false);
  readonly error = signal<string | null>(null);
  readonly planPreview = signal<SettingsPlanRow[] | null>(null);
  readonly refusedChanges = signal<ConfigFileChange[]>([]);

  private reachableCache: { at: number; data: ReachableReposState } | null =
    null;
  private reachableInflight: Promise<ReachableReposState> | null = null;
  readonly reachableEpoch = signal(0);
  readonly qaRolloutRaw = signal<{ epoch: number; text: string } | null>(null);

  recordError(err: unknown): void {
    const changes = extractRefusedChanges(err);
    if (changes.length > 0) {
      this.refusedChanges.set(changes);
    }
    this.error.set(errText(err));
  }

  clearError(): void {
    this.refusedChanges.set([]);
    this.error.set("");
  }

  readonly configRepoOnly = computed(() => {
    const s = this.settings();
    const c = this.config() as Record<string, unknown> | null;
    return (
      Boolean(s["configRepoOnly"] ?? c?.["configRepoOnly"]) ||
      Boolean(s["externalConfigRepo"] ?? c?.["externalConfigRepo"]) ||
      Boolean(s["settingsLocked"] ?? c?.["settingsLocked"])
    );
  });

  readonly hasSettingsDraft = computed(
    () => Object.keys(this.draft()).length > 0,
  );
  readonly hasDeployDraft = computed(
    () => Object.keys(this.deployDraft()).length > 0,
  );
  readonly hasFieldErrors = computed(
    () => Object.keys(this.fieldErrors()).length > 0,
  );
  readonly isDirty = computed(
    () =>
      this.hasSettingsDraft() || this.hasDeployDraft() || this.hasFieldErrors(),
  );

  readonly dirtyCount = computed(
    () =>
      Object.keys(this.draft()).length +
      Object.keys(this.deployDraft()).length +
      Object.keys(this.fieldErrors()).length,
  );

  sectionLocked(section: SettingsSectionId): boolean {
    if (this.configRepoOnly() && isConfigRepoSection(section)) {
      return true;
    }
    return false;
  }

  val<T>(key: string, fallback: T): T {
    const d = this.draft();
    if (key in d) return (d[key] as T) ?? fallback;
    const s = this.settings();
    if (key in s && s[key] !== undefined && s[key] !== null) {
      return s[key] as T;
    }
    return fallback;
  }

  deployVal<T>(key: string, fallback: T): T {
    const d = this.deployDraft();
    if (key in d) return (d[key] as T) ?? fallback;
    const dep = this.deployment();
    if (key in dep && dep[key] !== undefined && dep[key] !== null) {
      return dep[key] as T;
    }
    return fallback;
  }

  setField(key: string, value: unknown): void {
    this.planPreview.set(null);
    const stored = this.settings()[key];
    this.draft.update((prev) => {
      const next = { ...prev };
      if (formatSettingValue(stored) === formatSettingValue(value)) {
        delete next[key];
      } else {
        next[key] = value;
      }
      return next;
    });
  }

  setDeployField(key: string, value: unknown): void {
    const stored = this.deployment()[key];
    this.deployDraft.update((prev) => {
      const next = { ...prev };
      if (formatSettingValue(stored) === formatSettingValue(value)) {
        delete next[key];
      } else {
        next[key] = value;
      }
      return next;
    });
  }

  setFieldError(key: string, message: string | null): void {
    this.planPreview.set(null);
    this.fieldErrors.update((prev) => {
      const next = { ...prev };
      if (!message) {
        delete next[key];
      } else {
        next[key] = message;
      }
      return next;
    });
  }

  discard(): void {
    this.draft.set({});
    this.deployDraft.set({});
    this.fieldErrors.set({});
    this.qaRolloutRaw.set(null);
    this.discardEpoch.update((n) => n + 1);
    this.planPreview.set(null);
    this.error.set(null);
  }

  invalidateReachableRepos(): void {
    this.reachableCache = null;
    this.reachableInflight = null;
    this.reachableEpoch.update((n) => n + 1);
  }

  async fetchReachableRepos(force = false): Promise<ReachableReposState> {
    if (
      !force &&
      this.reachableCache &&
      Date.now() - this.reachableCache.at < REACHABLE_REPOS_TTL_MS
    ) {
      return this.reachableCache.data;
    }
    if (!force && this.reachableInflight) {
      return this.reachableInflight;
    }
    const epoch = this.reachableEpoch();
    const req = this.api
      .get<
        | unknown[]
        | {
            repos?: unknown[];
            errors?: string[];
            hostMismatches?: string[];
            reason?: string;
          }
      >("GET /api/deployment/reachable-repos")
      .then((res) => {
        const rawList = Array.isArray(res) ? res : (res?.repos ?? []);
        const parsed: { repo: string; push: boolean }[] = [];
        const seen = new Set<string>();
        for (const item of rawList) {
          let slug = "";
          let push = true;
          if (typeof item === "string") {
            slug = item.trim();
          } else if (item && typeof item === "object" && "repo" in item) {
            const obj = item as { repo?: unknown; push?: unknown };
            slug = typeof obj.repo === "string" ? obj.repo.trim() : "";
            if (typeof obj.push === "boolean") push = obj.push;
          }
          if (slug && !seen.has(slug)) {
            seen.add(slug);
            parsed.push({ repo: slug, push });
          }
        }
        parsed.sort((a, b) => a.repo.localeCompare(b.repo));
        const data: ReachableReposState = {
          repos: parsed,
          errors: !Array.isArray(res) ? (res?.errors ?? []) : [],
          hostMismatches: !Array.isArray(res)
            ? (res?.hostMismatches ?? [])
            : [],
          reason: !Array.isArray(res) ? (res?.reason ?? "") : "",
        };
        if (this.reachableEpoch() === epoch) {
          this.reachableCache = { at: Date.now(), data };
        }
        return data;
      })
      .finally(() => {
        if (this.reachableInflight === req) {
          this.reachableInflight = null;
        }
      });
    this.reachableInflight = req;
    return req;
  }

  async loadCore(background = false): Promise<void> {
    if (!background) {
      this.loading.set(true);
    }
    this.error.set(null);
    try {
      const seedFromConfig = (cfg: GrainConfig | null | undefined) => {
        if (!cfg) return;
        this.config.set(cfg);
        this.access.setConfig(cfg);
        const dep = (cfg as Record<string, unknown>)["deployment"];
        if (
          dep &&
          typeof dep === "object" &&
          !Array.isArray(dep) &&
          Object.keys(this.deployment()).length === 0
        ) {
          this.deployment.set(dep as Record<string, unknown>);
        }
      };

      const hasSeededConfig =
        this.access.configLoaded() && this.access.rawConfig() !== null;
      let configTask: Promise<void> | null = null;
      if (hasSeededConfig) {
        seedFromConfig(this.access.rawConfig());
        configTask = this.api.config().then(seedFromConfig);
      } else {
        const cfg = await this.api.config();
        seedFromConfig(cfg);
      }

      const tasks: Promise<void>[] = [];
      if (configTask) {
        tasks.push(
          configTask.catch((err) => {
            if (!this.config()) throw err;
          }),
        );
      }
      if (this.access.can("GET /api/settings")) {
        tasks.push(
          this.api
            .get<Record<string, unknown>>("GET /api/settings")
            .then((s) => this.settings.set(s ?? {}))
            .catch(() => undefined),
        );
      }
      if (this.access.can("GET /api/deployment")) {
        tasks.push(
          this.api
            .get<Record<string, unknown>>("GET /api/deployment")
            .then((d) => this.deployment.set(d ?? {}))
            .catch(() => undefined),
        );
      }
      await Promise.all(tasks);
      if (this.config() !== null) {
        this.initialized.set(true);
      }
    } catch (err) {
      this.error.set(errText(err));
    } finally {
      if (!background) {
        this.loading.set(false);
      }
    }
  }

  async previewPlan(): Promise<void> {
    if (this.hasFieldErrors() || !this.access.can("POST /api/settings/plan")) {
      return;
    }
    const payload = this.draft();
    if (Object.keys(payload).length === 0) return;
    this.planning.set(true);
    this.error.set(null);
    try {
      const planned = await this.api.post<Record<string, unknown>>(
        "POST /api/settings/plan",
        undefined,
        payload,
      );
      this.planPreview.set(
        computeSettingsPlan(this.settings(), planned, payload),
      );
    } catch (err) {
      this.error.set(errText(err));
    } finally {
      this.planning.set(false);
    }
  }

  async saveChanges(): Promise<void> {
    if (!this.isDirty() || this.hasFieldErrors()) return;
    this.saving.set(true);
    this.error.set(null);
    try {
      const settingsPayload = this.draft();
      if (Object.keys(settingsPayload).length > 0) {
        if (!this.access.can("POST /api/settings")) {
          throw new Error(this.access.deniedReason("POST /api/settings"));
        }
        const saved = await this.api.post<Record<string, unknown>>(
          "POST /api/settings",
          undefined,
          settingsPayload,
        );
        if (saved && typeof saved === "object") {
          this.settings.set(saved);
        }
        this.draft.set({});
        this.qaRolloutRaw.set(null);
      }
      const depPayload = this.deployDraft();
      if (Object.keys(depPayload).length > 0) {
        if (!this.access.can("PUT /api/deployment")) {
          throw new Error(this.access.deniedReason("PUT /api/deployment"));
        }
        const savedDep = await this.api.put<Record<string, unknown>>(
          "PUT /api/deployment",
          undefined,
          depPayload,
        );
        if (savedDep && typeof savedDep === "object") {
          this.deployment.set(savedDep);
        }
        this.deployDraft.set({});
      }
      this.fieldErrors.set({});
      this.discardEpoch.update((n) => n + 1);
      this.planPreview.set(null);
      this.snackbar.show("Settings saved");
    } catch (err) {
      this.recordError(err);
    } finally {
      this.saving.set(false);
    }
  }
}
