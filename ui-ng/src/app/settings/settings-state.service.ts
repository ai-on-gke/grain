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
  settingLabel,
} from "./settings-sections";

// failedSaveLabels names what a refused save was carrying -- the label of
// each drafted setting, "Deployment identity" for the deployment record
// and "Deployment memory" for the memory document -- so the section header
// can say "Couldn't save Branch name template" rather than only that
// changes are unsaved.
export function failedSaveLabels(
  settingsKeys: readonly string[],
  deploymentKeys: readonly string[],
  memory: boolean,
): string[] {
  const out = settingsKeys.map((k) => settingLabel(k));
  if (deploymentKeys.length > 0) out.push("Deployment identity");
  if (memory) out.push("Deployment memory");
  return out;
}

// readableSaveError turns a server refusal that opens with the setting's
// JSON key ("taskBranchTemplate branch template must contain {id}") into
// one that opens with its label ("Branch name template: branch template
// must contain {id}"), the name the person sees on the field.
export function readableSaveError(
  message: string,
  keys: readonly string[],
): string {
  for (const k of keys) {
    const label = settingLabel(k);
    if (label === k) continue;
    if (message.startsWith(k + " ") || message.startsWith(k + ":")) {
      const rest = message.slice(k.length).replace(/^:?\s*/, "");
      return `${label}: ${rest}`;
    }
  }
  return message;
}

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
  // available is false when no Git connection could list anything;
  // connections are the ones that were asked, by credential and host.
  available: boolean;
  connections: ReachConnection[];
}

export interface ReachConnection {
  credential: string;
  host: string;
  ok: boolean;
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
  readonly memoryStored = signal<string | null>(null);
  readonly memoryDraft = signal<string | null>(null);
  readonly fieldErrors = signal<Record<string, string>>({});
  readonly discardEpoch = signal(0);

  readonly loading = signal(false);
  readonly planning = signal(false);
  readonly saving = signal(false);
  readonly error = signal<string | null>(null);
  // The labels of what the last Save changes failed to save, for the
  // header's "Couldn't save <field>" and the error banner's Retry; empty
  // once a save succeeds or the drafts are discarded.
  readonly failedSave = signal<string[]>([]);
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

  readonly externalConfigRepo = computed(() => {
    const s = this.settings();
    const c = this.config() as Record<string, unknown> | null;
    return String(
      s["externalConfigRepo"] ?? c?.["externalConfigRepo"] ?? "",
    ).trim();
  });

  readonly hasInternalConfigRepo = computed(() => {
    const s = this.settings();
    const c = this.config() as Record<string, unknown> | null;
    return (
      Boolean(c?.["configRepo"] ?? s["configRepo"]) &&
      !this.externalConfigRepo()
    );
  });

  readonly configRepoOnly = computed(() => {
    const s = this.settings();
    const c = this.config() as Record<string, unknown> | null;
    const locked =
      typeof s["configRepoOnly"] === "boolean"
        ? s["configRepoOnly"]
        : Boolean(c?.["configRepoOnly"]);
    return locked || Boolean(this.externalConfigRepo());
  });

  readonly pendingRestart = computed<string[]>(() => {
    const raw = this.settings()["pendingRestart"];
    return Array.isArray(raw)
      ? raw.filter((x): x is string => typeof x === "string")
      : [];
  });

  readonly hasSettingsDraft = computed(
    () => Object.keys(this.draft()).length > 0,
  );
  readonly hasDeployDraft = computed(
    () => Object.keys(this.deployDraft()).length > 0,
  );
  readonly hasMemoryDraft = computed(
    () =>
      this.memoryDraft() !== null &&
      this.memoryDraft() !== (this.memoryStored() ?? ""),
  );
  readonly hasFieldErrors = computed(
    () => Object.keys(this.fieldErrors()).length > 0,
  );
  readonly isDirty = computed(
    () =>
      this.hasSettingsDraft() ||
      this.hasDeployDraft() ||
      this.hasMemoryDraft() ||
      this.hasFieldErrors(),
  );

  readonly dirtyCount = computed(
    () =>
      Object.keys(this.draft()).length +
      Object.keys(this.deployDraft()).length +
      (this.hasMemoryDraft() ? 1 : 0) +
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

  // Many numeric settings store 0 for "grain's default" and report that
  // default beside them as `<key>Default` (sandboxCpus/sandboxCpusDefault,
  // ciCacheMb/ciCacheMbDefault, ...). Showing the stored 0 reads as "zero
  // vCPUs"; the box shows blank with the default as its placeholder
  // instead, and a cleared box saves 0 again (back to the default).
  defaultedNumber(key: string): string {
    const v = Number(this.val<unknown>(key, 0));
    return Number.isFinite(v) && v !== 0 ? String(v) : "";
  }

  // defaultHint is `<key>Default` as text for a placeholder, or `fallback`
  // when the server does not report one.
  defaultHint(key: string, fallback = ""): string {
    const d = this.settings()[`${key}Default`];
    return d === undefined || d === null || d === "" ? fallback : String(d);
  }

  // setNumber records a number box's text: blank is `blank` (0, the
  // server's "default", unless the setting refuses 0 and wants grain's
  // default spelled out), anything else its numeric value.
  //
  // A setting whose blank is a non-zero default (Max worker agents,
  // Max merges per repo branch) binds `(input)` with deferBlank, so the
  // box is not refilled under the cursor while somebody retypes it, and
  // `(change)` without it, which fills the default in once they leave.
  setNumber(key: string, raw: string, blank = 0, deferBlank = false): void {
    const t = String(raw ?? "").trim();
    if (t === "" && deferBlank) return;
    this.setField(key, t === "" ? blank : Number(t));
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

  // GET and PUT /api/deployment serialise Team's slice fields
  // (modelSettings, defaultModels, ...) with omitempty, so an empty list
  // comes back absent rather than []. Treat undefined/null as [] when
  // staging an array so reverting to empty clears deployDraft instead of
  // comparing "not set" with "none".
  setDeployField(key: string, value: unknown): void {
    const raw = this.deployment()[key];
    const stored =
      Array.isArray(value) && (raw === undefined || raw === null) ? [] : raw;
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

  memoryValue(): string {
    return this.memoryDraft() ?? this.memoryStored() ?? "";
  }

  setMemoryStored(val: string): void {
    this.memoryStored.set(val);
    if (this.memoryDraft() === val) {
      this.memoryDraft.set(null);
    }
  }

  setMemoryDraft(val: string): void {
    const stored = this.memoryStored() ?? "";
    this.memoryDraft.set(val === stored ? null : val);
  }

  discard(): void {
    this.draft.set({});
    this.deployDraft.set({});
    this.memoryDraft.set(null);
    this.fieldErrors.set({});
    this.qaRolloutRaw.set(null);
    this.discardEpoch.update((n) => n + 1);
    this.planPreview.set(null);
    this.error.set(null);
    this.failedSave.set([]);
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
            available?: boolean;
            connections?: Partial<ReachConnection>[];
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
          // A bare list is an older server's: it listed, so it was available.
          available: Array.isArray(res) ? true : res?.available !== false,
          connections: (!Array.isArray(res) ? (res?.connections ?? []) : [])
            .filter((c) => c && typeof c === "object")
            .map((c) => ({
              credential: String(c.credential ?? ""),
              host: String(c.host ?? ""),
              ok: Boolean(c.ok),
            })),
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
      if (this.access.canGlobal("GET /api/settings")) {
        tasks.push(
          this.api
            .get<Record<string, unknown>>("GET /api/settings")
            .then((s) => this.settings.set(s ?? {}))
            .catch(() => undefined),
        );
      }
      if (this.access.canGlobal("GET /api/deployment")) {
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

  // saveFields saves just these drafted settings, for a card with a Save
  // button of its own (Memory's Save memory), leaving every other draft
  // for the section Save bar. It throws what the server refused.
  async saveFields(keys: readonly string[]): Promise<void> {
    const draft = this.draft();
    const payload: Record<string, unknown> = {};
    for (const k of keys) if (k in draft) payload[k] = draft[k];
    if (Object.keys(payload).length === 0) return;
    const saved = await this.api.post<Record<string, unknown>>(
      "POST /api/settings",
      undefined,
      payload,
    );
    if (saved && typeof saved === "object") this.settings.set(saved);
    this.draft.update((prev) => {
      const next = { ...prev };
      for (const k of keys) delete next[k];
      return next;
    });
  }

  async saveChanges(): Promise<void> {
    if (!this.isDirty() || this.hasFieldErrors()) return;
    this.saving.set(true);
    this.error.set(null);
    const settingsKeys = Object.keys(this.draft());
    const deploymentKeys = Object.keys(this.deployDraft());
    const memory = this.hasMemoryDraft();
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
      if (this.hasMemoryDraft()) {
        if (!this.access.can("PUT /api/memory")) {
          throw new Error(this.access.deniedReason("PUT /api/memory"));
        }
        const nextMem = this.memoryDraft() ?? "";
        await this.api.put("PUT /api/memory", undefined, { text: nextMem });
        this.memoryStored.set(nextMem);
        this.memoryDraft.set(null);
      }
      this.fieldErrors.set({});
      this.discardEpoch.update((n) => n + 1);
      this.planPreview.set(null);
      this.refusedChanges.set([]);
      this.failedSave.set([]);
      this.snackbar.show("Settings saved");
    } catch (err) {
      this.recordError(err);
      const msg = this.error();
      if (msg) this.error.set(readableSaveError(msg, settingsKeys));
      // Only what is still drafted failed: a deployment or memory write
      // after a successful settings write leaves the settings saved.
      this.failedSave.set(
        failedSaveLabels(
          Object.keys(this.draft()).length > 0 ? settingsKeys : [],
          Object.keys(this.deployDraft()).length > 0 ? deploymentKeys : [],
          memory && this.hasMemoryDraft(),
        ),
      );
    } finally {
      this.saving.set(false);
    }
  }
}
