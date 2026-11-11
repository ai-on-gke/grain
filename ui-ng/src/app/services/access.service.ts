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

import { Injectable, computed, signal } from "@angular/core";
import type { RouteKey } from "../api/generated-routes";
import { routeScope } from "../api/grain-api.service";
import type { GrainConfig } from "../api/models";

// AccessService answers "may the signed-in person make this call?" the
// way the server will when they try: the role GET /api/config reports
// (pkg/ui/access_roles.go's AccessRoleInfo) against the scope the route
// table says the call needs (generated-routes.ts, read off RoleScopeOf).
// It is ui/src/access.js's canAccess, keyed by route rather than by a
// scope string a page has to know. It only decides what is offered;
// the server still checks every call.
@Injectable({ providedIn: "root" })
export class AccessService {
  private readonly config = signal<GrainConfig | null>(null);

  readonly roleLabel = computed(() => this.config()?.access?.role ?? "");
  // Signed-in person for "owned by someone else" checks (pkg/ui ConfigResponse).
  readonly viewer = computed(
    () => this.config()?.viewerOwner || this.config()?.actor || "",
  );
  readonly limited = computed(() => {
    const a = this.config()?.access;
    return !!a && !a.full;
  });

  setConfig(config: GrainConfig | null): void {
    this.config.set(config);
  }

  canScope(scope: string | null, repo?: string | string[]): boolean {
    const access = this.config()?.access;
    if (!access || access.full) return true;
    if (scope === "signed-in") return true;
    if (scope === null) return false;
    if (!access.scopes?.includes(scope)) return false;
    const repos = (Array.isArray(repo) ? repo : repo ? [repo] : []).filter(
      Boolean,
    );
    const limit = access.repoLimits?.[scope];
    if (!limit || limit.length === 0 || repos.length === 0) return true;
    const lower = limit.map((r) => r.toLowerCase());
    return repos.every((r) => lower.includes(r.toLowerCase()));
  }

  can(route: RouteKey, repo?: string | string[]): boolean {
    return this.canScope(routeScope(route), repo);
  }

  // deniedReason is the sentence a disabled control explains itself
  // with, "" when the call is allowed.
  deniedReason(route: RouteKey, repo?: string | string[]): string {
    if (this.can(route, repo)) return "";
    const scope = routeScope(route);
    const access = this.config()?.access;
    const roles = access?.roles ?? [];
    const who = `Your ${roles.length > 1 ? "roles" : "role"} ${access?.role || "none"}`;
    if (scope === null) {
      return `${who} cannot do this: it needs a role holding every scope (grain/operator).`;
    }
    const where =
      repo && access?.scopes?.includes(scope) ? " on this repository" : "";
    return `${who} does not hold ${scope}${where}, which this needs. Ask an admin for a role that includes it.`;
  }
}
