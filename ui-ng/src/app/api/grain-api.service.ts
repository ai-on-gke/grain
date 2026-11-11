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
import { Injectable, inject } from "@angular/core";
import { firstValueFrom } from "rxjs";
import { ROUTE_SCOPES, type RouteKey } from "./generated-routes";
import type {
  DiffPreview,
  GrainConfig,
  LocalScriptApproval,
  Notification,
  Task,
  TaskDetail,
} from "./models";

// Path parameters a route key names, e.g. {id} or {owner}.
export type RouteParams = Record<string, string | number>;

// routePath fills a route key's {params} in, URL-encoding each, so a call
// is always written against a "METHOD /path" the server's route table
// actually has (generated-routes.ts) -- a typo is a compile error.
export function routePath(key: RouteKey, params: RouteParams = {}): string {
  const path = key.slice(key.indexOf(" ") + 1);
  return path.replace(/\{([^}]+)\}/g, (_, name: string) => {
    const v = params[name];
    if (v === undefined) throw new Error(`${key}: missing {${name}}`);
    return encodeURIComponent(String(v));
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
export class GrainApi {
  private readonly http = inject(HttpClient);
  // The last ETag and body per URL, for getIfChanged. Unlike the classic
  // UI's per-path ETag map, a 304 hands back the cached body, so any
  // number of readers can poll the same URL safely.
  private readonly etags = new Map<string, { etag: string; body: unknown }>();

  get<T>(key: RouteKey, params?: RouteParams, query?: string): Promise<T> {
    const url = routePath(key, params) + (query ? `?${query}` : "");
    return firstValueFrom(this.http.get<T>(url, { headers: JSON_HEADERS }));
  }

  // getIfChanged reads a URL with If-None-Match, the way the server's
  // polled lists expect (pkg/ui tasks.go answers 304 when nothing moved),
  // so a page can poll every few seconds for the price of a header.
  async getIfChanged<T>(
    key: RouteKey,
    params?: RouteParams,
    query?: string,
  ): Promise<Fresh<T>> {
    const url = routePath(key, params) + (query ? `?${query}` : "");
    const cached = this.etags.get(url);
    let headers = JSON_HEADERS;
    if (cached) headers = headers.set("If-None-Match", cached.etag);
    try {
      const res = await firstValueFrom(
        this.http.get<T>(url, { headers, observe: "response" }),
      );
      const body = res.body as T;
      const etag = res.headers.get("ETag");
      if (etag) this.etags.set(url, { etag, body });
      else this.etags.delete(url);
      return { body, changed: true };
    } catch (err) {
      if (cached && (err as HttpErrorResponse)?.status === 304) {
        return { body: cached.body as T, changed: false };
      }
      throw err;
    }
  }

  patch<T = unknown>(
    key: RouteKey,
    params?: RouteParams,
    body?: unknown,
  ): Promise<T> {
    return firstValueFrom(
      this.http.patch<T>(routePath(key, params), body ?? null, {
        headers: JSON_HEADERS,
      }),
    );
  }

  put<T = unknown>(
    key: RouteKey,
    params?: RouteParams,
    body?: unknown,
  ): Promise<T> {
    return firstValueFrom(
      this.http.put<T>(routePath(key, params), body ?? null, {
        headers: JSON_HEADERS,
      }),
    );
  }

  post<T = unknown>(
    key: RouteKey,
    params?: RouteParams,
    body?: unknown,
  ): Promise<T> {
    return firstValueFrom(
      this.http.post<T>(routePath(key, params), body ?? null, {
        headers: JSON_HEADERS,
      }),
    );
  }

  config(): Promise<GrainConfig> {
    return this.get<GrainConfig>("GET /api/config");
  }

  tasks(): Promise<Task[]> {
    return this.get<Task[]>("GET /api/tasks");
  }

  task(id: string): Promise<TaskDetail> {
    return this.get<TaskDetail>("GET /api/tasks/{id}", { id });
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
}
