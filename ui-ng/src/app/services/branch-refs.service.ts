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

import { Injectable, inject } from "@angular/core";
import { GrainApi, splitRepo } from "../api/grain-api.service";
import { AccessService } from "./access.service";

export const BRANCH_REFS_TTL_MS = 30_000;
export const BRANCH_REFS_REFRESH_COOLDOWN_MS = 15_000;

interface CacheEntry {
  at: number;
  refreshedAt?: number;
  branches: ReadonlySet<string> | null;
}

type BranchPayload =
  | string[]
  | {
      branches?: (string | { name?: string })[];
      items?: (string | { name?: string })[];
    };

function normalizeBranchSet(raw: unknown): ReadonlySet<string> | null {
  if (!raw || typeof raw !== "object") {
    if (!Array.isArray(raw)) return null;
    const set = extractNames(raw);
    return set.size > 0 ? set : null;
  }
  if (Array.isArray(raw)) {
    const set = extractNames(raw);
    return set.size > 0 ? set : null;
  }
  const obj = raw as Exclude<BranchPayload, string[]>;
  const list = obj.branches ?? obj.items;
  if (!Array.isArray(list)) return null;
  const set = extractNames(list);
  return set.size > 0 ? set : null;
}

function extractNames(
  list: (string | { name?: string })[],
): ReadonlySet<string> {
  const out = new Set<string>();
  for (const item of list) {
    if (typeof item === "string" && item.trim()) {
      out.add(item.trim());
    } else if (
      item &&
      typeof item === "object" &&
      typeof item.name === "string" &&
      item.name.trim()
    ) {
      out.add(item.name.trim());
    }
  }
  return out;
}

// BranchRefsService caches remote repository branch listings from github-branches
// for a short TTL so UI surfaces can skip doomed diff-preview/compare requests
// for deleted branches without stampeding the API.
// A null answer means "unknown / cannot check" so active diffs still try.
@Injectable({ providedIn: "root" })
export class BranchRefsService {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);

  private readonly cache = new Map<string, CacheEntry>();
  private readonly inFlight = new Map<
    string,
    Promise<ReadonlySet<string> | null>
  >();

  async branchesFor(
    repo: string | undefined | null,
    force = false,
  ): Promise<ReadonlySet<string> | null> {
    const clean = (repo ?? "").trim();
    if (!clean || clean.split("/").filter(Boolean).length !== 2) {
      return null;
    }
    const now = Date.now();
    const cached = this.cache.get(clean);
    if (!force && cached && now - cached.at < BRANCH_REFS_TTL_MS) {
      return cached.branches;
    }
    if (!force) {
      const existing = this.inFlight.get(clean);
      if (existing) return existing;
    }

    const task = this.fetchBranches(clean, force).finally(() => {
      if (this.inFlight.get(clean) === task) {
        this.inFlight.delete(clean);
      }
    });
    this.inFlight.set(clean, task);
    return task;
  }

  async hasBranch(
    repo: string | undefined | null,
    branch: string | undefined | null,
    force = false,
  ): Promise<boolean | null> {
    const clean = (repo ?? "").trim();
    const br = (branch ?? "").trim();
    if (!br) return false;
    let set = await this.branchesFor(clean, force);
    if (set === null) return null;
    if (set.has(br)) return true;
    if (!force) {
      const cached = this.cache.get(clean);
      const lastRefresh = cached?.refreshedAt ?? 0;
      if (Date.now() - lastRefresh >= BRANCH_REFS_REFRESH_COOLDOWN_MS) {
        set = await this.branchesFor(clean, true);
        if (set === null) return null;
        return set.has(br);
      }
    }
    return false;
  }

  invalidate(repo?: string): void {
    if (!repo) {
      this.cache.clear();
      return;
    }
    this.cache.delete(repo.trim());
  }

  private async fetchBranches(
    repo: string,
    force = false,
  ): Promise<ReadonlySet<string> | null> {
    if (typeof this.api.get !== "function") return null;
    const cfg = this.access.rawConfig();
    if (
      cfg &&
      cfg.gitCredentialsConfigured === false &&
      repo !== cfg.configRepo
    ) {
      return null;
    }
    const { owner, name } = splitRepo(repo);
    if (!owner || !name) return null;

    let set: ReadonlySet<string> | null = null;
    if (
      this.access.can("GET /api/repos/{owner}/{name}/github-branches", repo)
    ) {
      try {
        const res = await this.api.get<BranchPayload>(
          "GET /api/repos/{owner}/{name}/github-branches",
          { owner, name },
          force ? "refresh=1" : undefined,
        );
        set = normalizeBranchSet(res);
      } catch {
        set = null;
      }
    }

    const now = Date.now();
    if (set !== null) {
      const prev = this.cache.get(repo);
      this.cache.set(repo, {
        at: now,
        refreshedAt: force ? now : (prev?.refreshedAt ?? 0),
        branches: set,
      });
    } else if (force) {
      const prev = this.cache.get(repo);
      if (prev) this.cache.set(repo, { ...prev, refreshedAt: now });
    }
    return set;
  }
}
