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
  HttpClient,
  HttpErrorResponse,
  HttpHeaders,
} from "@angular/common/http";
import { Injectable, OnDestroy, inject } from "@angular/core";
import { type Observable, firstValueFrom } from "rxjs";
import { ROUTE_SCOPES, type RouteKey } from "./generated-routes";
import type {
  DiffPreview,
  GrainConfig,
  LocalScriptApproval,
  LogoutResponse,
  Notification,
  Task,
  TaskDetail,
  TaskSummariesResponse,
  WhoamiResponse,
} from "./models";

// Path parameters a route key names, e.g. {id} or {owner}.
export type RouteParams = Record<string, string | number>;

// routePath fills a route key's {params} in, URL-encoding each, so a call
// is always written against a "METHOD /path" the server's route table
// actually has (generated-routes.ts) -- a typo is a compile error.
export function routePath(key: RouteKey, params: RouteParams = {}): string {
  const path = key.slice(key.indexOf(" ") + 1);
  return path.replace(/\{([^}]+)\}/g, (_, name: string) => {
    if (name.endsWith("...")) {
      const bare = name.slice(0, -3);
      const v = params[name] ?? params[bare] ?? "";
      return String(v)
        .split("/")
        .filter(Boolean)
        .map((seg) => encodeURIComponent(seg))
        .join("/");
    }
    const v = params[name];
    if (v === undefined) throw new Error(`${key}: missing {${name}}`);
    const s = String(v);
    return encodeURIComponent(s);
  });
}

export function routeScope(key: RouteKey): string | null {
  return ROUTE_SCOPES[key];
}

// splitRepo turns "owner/name" (or "host/owner/name") into the {owner}
// and {name} the /api/repos/... routes take, as ui/src/repoApi.js does.
export function splitRepo(repo: string): { owner: string; name: string } {
  const parts = repo.trim().split("/");
  if (parts.length >= 3) {
    return { owner: `${parts[0]}/${parts[1]}`, name: parts.slice(2).join("/") };
  }
  return { owner: parts[0] || "", name: parts[1] || "" };
}

const JSON_HEADERS = new HttpHeaders({ "Content-Type": "application/json" });

export const TASK_SUMMARIES_BATCH_SIZE = 50;

// Splits unique non-empty task IDs into batches of at most `size`.
export function chunkTaskIds(
  ids: readonly string[],
  size = TASK_SUMMARIES_BATCH_SIZE,
): string[][] {
  const seen = new Set<string>();
  const unique: string[] = [];
  for (const raw of ids) {
    const id = raw.trim();
    if (!id || seen.has(id)) continue;
    seen.add(id);
    unique.push(id);
  }
  const chunks: string[][] = [];
  for (let i = 0; i < unique.length; i += size) {
    chunks.push(unique.slice(i, i + size));
  }
  return chunks;
}

export function taskSummariesQuery(
  ids: readonly string[],
  commentsLimit = 1,
  attemptsLimit = 1,
): string {
  return `ids=${ids.map((id) => encodeURIComponent(id)).join(",")}&comments_limit=${commentsLimit}&attempts_limit=${attemptsLimit}`;
}

export interface TaskSummaryBatchResult {
  tasks: Record<string, TaskDetail>;
  failedIds: string[];
}

// Fetches task summaries in batches of up to 50, preserving partial chunk
// successes and reporting IDs from failed chunks or omitted entries.
export async function loadTaskSummaryBatches(
  ids: readonly string[],
  commentsLimit: number,
  attemptsLimit: number,
  fetchChunk: (
    query: string,
  ) => Promise<Record<string, TaskDetail> | undefined>,
): Promise<TaskSummaryBatchResult> {
  const chunks = chunkTaskIds(ids);
  if (chunks.length === 0) return { tasks: {}, failedIds: [] };
  const tasks: Record<string, TaskDetail> = {};
  const failedIds: string[] = [];
  const results = await Promise.allSettled(
    chunks.map((chunk) =>
      fetchChunk(taskSummariesQuery(chunk, commentsLimit, attemptsLimit)),
    ),
  );
  for (let i = 0; i < chunks.length; i++) {
    const chunk = chunks[i];
    const res = results[i];
    if (res.status !== "fulfilled") {
      failedIds.push(...chunk);
      continue;
    }
    const map = res.value ?? {};
    for (const id of chunk) {
      const detail = map[id];
      if (detail) {
        tasks[id] = detail;
      } else {
        failedIds.push(id);
      }
    }
  }
  return { tasks, failedIds };
}

const VERIFIER_UNAVAILABLE_CODE = "verifier_unavailable";
const DEFAULT_RETRY_AFTER_SEC = 2;
const MAX_RETRY_AFTER_SEC = 10;
const RETRY_JITTER_RATIO = 0.3;
const MAX_VERIFIER_RETRIES = 3;
const MAX_VERIFIER_RETRY_MS = 20_000;

// READ_TIMEOUT_MS bounds each idempotent read whose answer is expected
// promptly so a daemon (or proxy) that accepts connections without answering
// aborts the hung socket and surfaces a timeout instead of wedging polls.
export const READ_TIMEOUT_MS = 8_000;
const SLOW_READ_TIMEOUT_MS = 30_000;

export interface ReadTimeoutError extends Error {
  readonly timedOut: true;
  readonly timeoutMs: number;
  readonly sentAt: number;
}

export interface ApiActivityObserver {
  onOk(url: string): void;
  onError(url: string, err: unknown): void;
}

function makeTimeoutError(timeoutMs: number, sentAt: number): ReadTimeoutError {
  const secs = Math.max(1, Math.round(timeoutMs / 1000));
  const err = new Error(
    `The server sent no answer within ${secs} seconds.`,
  ) as ReadTimeoutError;
  return Object.assign(err, {
    timedOut: true as const,
    timeoutMs,
    sentAt,
  });
}

function readTimeoutForUrl(url: string): number {
  const [path, qs = ""] = url.split("?");
  if (path === "/api/restart-watch") {
    return new URLSearchParams(qs).get("quick") === "1" ? READ_TIMEOUT_MS : 0;
  }
  if (path.includes("reachable-repos")) {
    return SLOW_READ_TIMEOUT_MS;
  }
  return READ_TIMEOUT_MS;
}

function routeMethod(key: RouteKey): string {
  const space = key.indexOf(" ");
  return space > 0 ? key.slice(0, space).toUpperCase() : "GET";
}

function isIdempotentReadMethod(method: string): boolean {
  return method === "GET" || method === "HEAD";
}

export function isVerifierUnavailable(err: unknown): boolean {
  if (!err || typeof err !== "object") return false;
  const e = err as { status?: unknown; error?: unknown };
  if (e.status !== 503) return false;
  let body = e.error;
  if (typeof body === "string") {
    const trimmed = body.trim();
    if (!trimmed.startsWith("{")) return false;
    try {
      body = JSON.parse(trimmed);
    } catch {
      return false;
    }
  }
  return (
    Boolean(body) &&
    typeof body === "object" &&
    (body as { code?: unknown }).code === VERIFIER_UNAVAILABLE_CODE
  );
}

function parseRetryAfterSeconds(err: unknown, nowMs: number): number {
  const raw = (
    err as { headers?: { get?(name: string): string | null } }
  )?.headers?.get?.("Retry-After");
  if (raw) {
    const trimmed = raw.trim();
    if (/^\d+(?:\.\d+)?$/.test(trimmed)) {
      const n = Number(trimmed);
      if (Number.isFinite(n) && n >= 0) {
        return Math.min(n, MAX_RETRY_AFTER_SEC);
      }
    } else {
      const at = Date.parse(trimmed);
      if (Number.isFinite(at)) {
        return Math.min(Math.max(0, (at - nowMs) / 1000), MAX_RETRY_AFTER_SEC);
      }
    }
  }
  return DEFAULT_RETRY_AFTER_SEC;
}

function computeVerifierBackoffMs(err: unknown, nowMs: number): number {
  const baseMs = parseRetryAfterSeconds(err, nowMs) * 1000;
  const jitter = 1 + (Math.random() * 2 - 1) * RETRY_JITTER_RATIO;
  return Math.max(0, Math.round(baseMs * jitter));
}

// Fresh is one conditional read: the body (the cached copy when the
// server answered 304 Not Modified) and whether it changed since the
// last read of the same URL.
export interface Fresh<T> {
  body: T;
  changed: boolean;
}

// GrainApi is the typed REST client: same origin, the daemon's own
// session cookie, and nothing of its own for CSRF -- the browser's
// Sec-Fetch-Site: same-origin is what pkg/ui/csrf.go checks, exactly as
// for the React UI.
@Injectable({ providedIn: "root" })
export class GrainApi implements OnDestroy {
  private readonly http = inject(HttpClient);
  // The last ETag and body per URL, for getIfChanged. Unlike the classic
  // UI's per-path ETag map, a 304 hands back the cached body, so any
  // number of readers can poll the same URL safely.
  private readonly etags = new Map<string, { etag: string; body: unknown }>();
  private readonly inflightGet = new Map<string, Promise<unknown>>();
  private readonly inflightCond = new Map<string, Promise<Fresh<unknown>>>();
  private readonly urlEpochs = new Map<string, number>();
  private condEpoch = 0;
  private configCache: { exp: number; body: GrainConfig } | null = null;
  // Shared in-flight backoff so concurrent reads hitting the same transient
  // verifier_unavailable blip wait on one timer instead of storming.
  private recovery: {
    promise: Promise<void>;
    wakeAt: number;
    timer: ReturnType<typeof setTimeout>;
  } | null = null;
  private readonly observers = new Set<ApiActivityObserver>();
  private readonly activeReads = new Set<{
    sentAt: number;
    deadline: number;
    abort: () => void;
    dispose: () => void;
  }>();
  private readSweepTimer: ReturnType<typeof setTimeout> | null = null;
  private destroyed = false;

  ngOnDestroy(): void {
    this.destroyed = true;
    if (this.recovery) {
      clearTimeout(this.recovery.timer);
      this.recovery = null;
    }
    if (this.readSweepTimer) {
      clearTimeout(this.readSweepTimer);
      this.readSweepTimer = null;
    }
    for (const r of [...this.activeReads]) {
      r.dispose();
    }
    this.observers.clear();
  }

  observeActivity(observer: ApiActivityObserver): () => void {
    this.observers.add(observer);
    this.scheduleReadSweep();
    return () => {
      this.observers.delete(observer);
      if (this.observers.size === 0 && this.readSweepTimer) {
        clearTimeout(this.readSweepTimer);
        this.readSweepTimer = null;
      }
    };
  }

  // Aborts reads still waiting from before a recovery so half-open sockets
  // never hold inflight entries or HTTP/1.1 connection slots once the server
  // is answering new requests again.
  abortStaleReads(olderThanMs = 2_000, now = Date.now()): void {
    for (const r of [...this.activeReads]) {
      if (now - r.sentAt >= olderThanMs) {
        r.abort();
      }
    }
    this.scheduleReadSweep();
  }

  private scheduleReadSweep(): void {
    if (this.readSweepTimer) {
      clearTimeout(this.readSweepTimer);
      this.readSweepTimer = null;
    }
    if (
      this.destroyed ||
      this.observers.size === 0 ||
      this.activeReads.size === 0
    ) {
      return;
    }
    let nextDeadline = Infinity;
    for (const r of this.activeReads) {
      if (r.deadline < nextDeadline) nextDeadline = r.deadline;
    }
    if (!Number.isFinite(nextDeadline)) return;
    const delay = Math.max(0, nextDeadline - Date.now());
    this.readSweepTimer = setTimeout(() => {
      this.readSweepTimer = null;
      const now = Date.now();
      for (const r of [...this.activeReads]) {
        if (now >= r.deadline) {
          r.abort();
        }
      }
      this.scheduleReadSweep();
    }, delay);
  }

  private notifyOk(url: string): void {
    if (this.destroyed) return;
    for (const obs of this.observers) obs.onOk(url);
  }

  private notifyError(url: string, err: unknown): void {
    if (this.destroyed) return;
    for (const obs of this.observers) obs.onError(url, err);
  }

  private trackOutcome<T>(url: string, p: Promise<T>): Promise<T> {
    return p.then(
      (res) => {
        this.notifyOk(url);
        return res;
      },
      (err) => {
        this.notifyError(url, err);
        throw err;
      },
    );
  }

  private readBounded<T>(
    obs$: Observable<T>,
    timeoutMs: number,
    alwaysBound = false,
  ): Promise<T> {
    if (timeoutMs <= 0 || (!alwaysBound && this.observers.size === 0)) {
      return firstValueFrom(obs$);
    }
    const sentAt = Date.now();
    const deadline = sentAt + timeoutMs;
    return new Promise<T>((resolve, reject) => {
      let settled = false;
      let sub: { unsubscribe(): void } | null = null;
      const cleanup = () => {
        this.activeReads.delete(entry);
        if (this.activeReads.size === 0 && this.readSweepTimer) {
          clearTimeout(this.readSweepTimer);
          this.readSweepTimer = null;
        }
      };
      const entry = {
        sentAt,
        deadline,
        abort: () => {
          if (settled) return;
          settled = true;
          cleanup();
          sub?.unsubscribe();
          reject(makeTimeoutError(timeoutMs, sentAt));
        },
        dispose: () => {
          if (settled) return;
          settled = true;
          cleanup();
          sub?.unsubscribe();
        },
      };
      this.activeReads.add(entry);
      this.scheduleReadSweep();
      sub = obs$.subscribe({
        next: (val) => {
          if (settled) return;
          settled = true;
          cleanup();
          sub?.unsubscribe();
          this.scheduleReadSweep();
          resolve(val);
        },
        error: (err) => {
          if (settled) return;
          settled = true;
          cleanup();
          this.scheduleReadSweep();
          reject(err);
        },
        complete: () => {
          if (settled) return;
          settled = true;
          cleanup();
          this.scheduleReadSweep();
          reject(new Error("Empty response stream"));
        },
      });
    });
  }

  get<T>(key: RouteKey, params?: RouteParams, query?: string): Promise<T> {
    const url = routePath(key, params) + (query ? `?${query}` : "");
    const pending = this.inflightGet.get(url);
    if (pending) return pending as Promise<T>;
    const method = routeMethod(key);
    const timeoutMs = readTimeoutForUrl(url);
    const req = this.trackOutcome(
      url,
      this.retryIdempotentRead(method, () =>
        this.readBounded(
          method === "HEAD"
            ? this.http.head<T>(url, { headers: JSON_HEADERS })
            : this.http.get<T>(url, { headers: JSON_HEADERS }),
          timeoutMs,
        ),
      ),
    ).finally(() => {
      if (this.inflightGet.get(url) === req) {
        this.inflightGet.delete(url);
      }
    });
    this.inflightGet.set(url, req);
    return req;
  }

  head(key: RouteKey, params?: RouteParams, query?: string): Promise<void> {
    const url = routePath(key, params) + (query ? `?${query}` : "");
    const keyMethod = routeMethod(key);
    const method = isIdempotentReadMethod(keyMethod) ? "HEAD" : keyMethod;
    return this.trackOutcome(
      url,
      this.retryIdempotentRead(method, () =>
        this.readBounded(
          this.http.head<void>(url, { headers: JSON_HEADERS }),
          readTimeoutForUrl(url),
        ),
      ),
    );
  }

  getText(
    key: RouteKey,
    params?: RouteParams,
    query?: string,
  ): Promise<string> {
    const url = routePath(key, params) + (query ? `?${query}` : "");
    return this.trackOutcome(
      url,
      this.retryIdempotentRead(routeMethod(key), () =>
        this.readBounded(
          this.http.get(url, { responseType: "text" }),
          readTimeoutForUrl(url),
        ),
      ),
    );
  }

  // getIfChanged reads a URL with If-None-Match, the way the server's
  // polled lists expect (pkg/ui tasks.go answers 304 when nothing moved),
  // so a page can poll every few seconds for the price of a header.
  getIfChanged<T>(
    key: RouteKey,
    params?: RouteParams,
    query?: string,
  ): Promise<Fresh<T>> {
    const url = routePath(key, params) + (query ? `?${query}` : "");
    const pending = this.inflightCond.get(url);
    if (pending) return pending as Promise<Fresh<T>>;
    const epoch = this.condEpoch;
    const urlEpoch = this.urlEpochs.get(url) ?? 0;
    const req = this.trackOutcome(
      url,
      this.fetchIfChanged<T>(url, epoch, urlEpoch),
    ).finally(() => {
      if (this.inflightCond.get(url) === req) {
        this.inflightCond.delete(url);
      }
    });
    this.inflightCond.set(url, req);
    return req;
  }

  private fetchIfChanged<T>(
    url: string,
    epoch: number,
    urlEpoch: number,
  ): Promise<Fresh<T>> {
    const timeoutMs = readTimeoutForUrl(url);
    return this.retryIdempotentRead("GET", () => {
      const cached = this.etags.get(url);
      let headers = JSON_HEADERS;
      if (cached) headers = headers.set("If-None-Match", cached.etag);
      return this.readBounded(
        this.http.get<T>(url, { headers, observe: "response" }),
        timeoutMs,
      ).then(
        (res) => {
          const body = res.body as T;
          const etag = res.headers.get("ETag");
          if (
            this.condEpoch === epoch &&
            (this.urlEpochs.get(url) ?? 0) === urlEpoch
          ) {
            if (etag) this.etags.set(url, { etag, body });
            else this.etags.delete(url);
          }
          return { body, changed: true };
        },
        (err) => {
          if ((err as HttpErrorResponse)?.status === 304) {
            if (cached) return { body: cached.body as T, changed: false };
            // A 304 to a request that sent no tag means a cache in between
            // answered for us; nothing here can stand in for the body.
            throw new Error(`${url}: 304 Not Modified with no cached copy`);
          }
          throw err;
        },
      );
    });
  }

  private retryIdempotentRead<T>(
    method: string,
    attempt: () => Promise<T>,
  ): Promise<T> {
    if (!isIdempotentReadMethod(method)) return attempt();
    const startedAt = Date.now();
    const run = (retries: number): Promise<T> =>
      attempt().then(undefined, (err) => {
        if (retries >= MAX_VERIFIER_RETRIES || !isVerifierUnavailable(err)) {
          throw err;
        }
        return this.waitForVerifierRecovery(err, startedAt).then(
          (shouldRetry) => {
            if (!shouldRetry) throw err;
            return run(retries + 1);
          },
        );
      });
    return run(0);
  }

  private waitForVerifierRecovery(
    err: unknown,
    startedAt: number,
  ): Promise<boolean> {
    if (this.destroyed) return Promise.resolve(false);
    const now = Date.now();
    const elapsed = now - startedAt;
    if (elapsed >= MAX_VERIFIER_RETRY_MS) {
      return Promise.resolve(false);
    }
    if (this.recovery) {
      if (this.recovery.wakeAt - startedAt > MAX_VERIFIER_RETRY_MS) {
        return Promise.resolve(false);
      }
      return this.recovery.promise.then(() => !this.destroyed);
    }
    const delayMs = computeVerifierBackoffMs(err, now);
    if (elapsed + delayMs > MAX_VERIFIER_RETRY_MS) {
      return Promise.resolve(false);
    }
    const wakeAt = now + delayMs;
    let resolveRecovery!: () => void;
    const promise = new Promise<void>((resolve) => {
      resolveRecovery = resolve;
    });
    const timer = setTimeout(() => {
      // Clear before resolving so retries that immediately hit 503 again
      // start a fresh recovery wait rather than awaiting this settled one.
      if (this.recovery?.promise === promise) {
        this.recovery = null;
      }
      resolveRecovery();
    }, delayMs);
    this.recovery = { promise, wakeAt, timer };
    return promise.then(() => !this.destroyed);
  }

  // clearConditionalCache forgets cached ETag bodies (all, or matching prefix)
  // for sign-in, role changes, or mutating requests where a URL's payload shifts.
  // The bodies getIfChanged returns are shared between readers: treat them as read-only.
  clearConditionalCache(prefix?: string): void {
    if (!prefix) {
      this.condEpoch++;
      this.urlEpochs.clear();
      this.etags.clear();
      this.inflightCond.clear();
      this.inflightGet.clear();
      this.configCache = null;
      return;
    }
    const matches = (k: string) =>
      k === prefix || k.startsWith(`${prefix}?`) || k.startsWith(`${prefix}/`);
    if (matches("/api/config")) this.configCache = null;
    this.urlEpochs.set(prefix, (this.urlEpochs.get(prefix) ?? 0) + 1);
    for (const map of [this.etags, this.inflightCond, this.inflightGet]) {
      for (const k of map.keys()) {
        if (matches(k)) {
          this.urlEpochs.set(k, (this.urlEpochs.get(k) ?? 0) + 1);
          map.delete(k);
        }
      }
    }
  }

  private async send<T>(
    method: "patch" | "put" | "post" | "delete",
    key: RouteKey,
    params?: RouteParams,
    body?: unknown,
    query?: string,
  ): Promise<T> {
    const url = routePath(key, params) + (query ? `?${query}` : "");
    const res = await this.trackOutcome(
      url,
      firstValueFrom(
        this.http.request<T>(method, url, {
          headers: JSON_HEADERS,
          body:
            method === "delete"
              ? body === undefined
                ? undefined
                : body
              : (body ?? null),
        }),
      ),
    );
    this.invalidateAfterMutation(url);
    return res;
  }

  private invalidateAfterMutation(url: string): void {
    const path = url.split("?")[0] ?? url;
    if (
      path.startsWith("/api/") &&
      /^(settings|deployment|pause|pipeline-pause|repos|auth|agent-keys|github-tokens|usage\/settings)/.test(
        path.slice(5),
      )
    ) {
      this.clearConditionalCache("/api/config");
    }
    if (path === "/api/tasks" || path.startsWith("/api/tasks/")) {
      this.clearConditionalCache("/api/tasks/summaries");
    }
    if (path.startsWith("/api/tasks/")) {
      const taskId = path.slice(11).split("/")[0];
      if (taskId) this.clearConditionalCache(`/api/tasks/${taskId}`);
    }
  }

  patch<T = unknown>(
    key: RouteKey,
    params?: RouteParams,
    body?: unknown,
    query?: string,
  ): Promise<T> {
    return this.send<T>("patch", key, params, body, query);
  }

  put<T = unknown>(
    key: RouteKey,
    params?: RouteParams,
    body?: unknown,
    query?: string,
  ): Promise<T> {
    return this.send<T>("put", key, params, body, query);
  }

  post<T = unknown>(
    key: RouteKey,
    params?: RouteParams,
    body?: unknown,
    query?: string,
  ): Promise<T> {
    return this.send<T>("post", key, params, body, query);
  }

  delete<T = unknown>(
    key: RouteKey,
    params?: RouteParams,
    body?: unknown,
    query?: string,
  ): Promise<T> {
    return this.send<T>("delete", key, params, body, query);
  }

  config(): Promise<GrainConfig> {
    if (this.configCache && Date.now() < this.configCache.exp) {
      return Promise.resolve(this.configCache.body);
    }
    const epoch = this.condEpoch + (this.urlEpochs.get("/api/config") ?? 0);
    return this.get<GrainConfig>("GET /api/config").then((body) => {
      if (this.condEpoch + (this.urlEpochs.get("/api/config") ?? 0) === epoch) {
        this.configCache = { exp: Date.now() + 2_000, body };
      }
      return body;
    });
  }

  // probeConfig asks GET /api/config directly (bypassing the 2s cache and any
  // older in-flight read) to confirm whether the daemon is answering and to
  // detect recovery without recursive activity notifications.
  probeConfig(timeoutMs = 5_000): Promise<GrainConfig> {
    const epoch = this.condEpoch + (this.urlEpochs.get("/api/config") ?? 0);
    return this.readBounded(
      this.http.get<GrainConfig>("/api/config", { headers: JSON_HEADERS }),
      timeoutMs,
      true,
    ).then((body) => {
      if (this.condEpoch + (this.urlEpochs.get("/api/config") ?? 0) === epoch) {
        this.configCache = { exp: Date.now() + 2_000, body };
      }
      return body;
    });
  }

  tasks(): Promise<Task[]> {
    return this.get<Task[]>("GET /api/tasks");
  }

  task(id: string): Promise<TaskDetail> {
    return this.get<TaskDetail>("GET /api/tasks/{id}", { id });
  }

  taskSummary(id: string): Promise<TaskDetail> {
    return this.getIfChanged<TaskDetail>(
      "GET /api/tasks/{id}",
      { id },
      "comments_limit=1&attempts_limit=1",
    ).then((r) => r.body);
  }

  async taskSummaries(
    ids: readonly string[],
    commentsLimit = 1,
    attemptsLimit = 1,
  ): Promise<Record<string, TaskDetail>> {
    const chunks = chunkTaskIds(ids);
    if (chunks.length === 0) return {};
    const responses = await Promise.all(
      chunks.map((chunk) =>
        this.getIfChanged<TaskSummariesResponse>(
          "GET /api/tasks/summaries",
          undefined,
          taskSummariesQuery(chunk, commentsLimit, attemptsLimit),
        ),
      ),
    );
    const out: Record<string, TaskDetail> = {};
    for (const r of responses) {
      if (r.body?.tasks) Object.assign(out, r.body.tasks);
    }
    return out;
  }

  openNotifications(): Promise<Notification[]> {
    return this.get<
      | Notification[]
      | { notifications?: Notification[]; items?: Notification[] }
    >("GET /api/notifications", undefined, "open=1&fields=list").then((r) =>
      Array.isArray(r) ? r : (r?.notifications ?? r?.items ?? []),
    );
  }

  diffPreview(id: string, max = 6): Promise<DiffPreview> {
    return this.get<DiffPreview>(
      "GET /api/tasks/{id}/diff-preview",
      { id },
      `max=${max}`,
    );
  }

  localScriptApprovals(): Promise<LocalScriptApproval[]> {
    return this.get<
      LocalScriptApproval[] | { approvals?: LocalScriptApproval[] }
    >("GET /api/local-script-approvals").then((r) =>
      Array.isArray(r) ? r : (r?.approvals ?? []),
    );
  }

  whoami(): Promise<WhoamiResponse> {
    return this.get<WhoamiResponse>("GET /api/whoami");
  }

  logout(): Promise<LogoutResponse> {
    return this.post<LogoutResponse>("POST /api/auth/logout");
  }
}
