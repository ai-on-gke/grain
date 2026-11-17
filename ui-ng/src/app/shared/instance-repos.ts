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

export interface InstanceReposSummary {
  /** True when the deployment configures an explicit targetRepos allowlist. */
  readonly hasTargetAllowlist: boolean;
  /**
   * Repositories configured on this Grain instance (matching classic
   * `ui/src/state.js` `repoRows`), deduplicated case-insensitively while
   * preserving the first canonical spelling. Never includes full GitHub-account
   * `reachable-repos` listings.
   */
  readonly repos: string[];
}

export function hasExplicitTargetAllowlist(
  cfg: Record<string, unknown> | null | undefined,
): boolean {
  if (!cfg) return false;
  const top = cfg["targetRepos"];
  if (
    Array.isArray(top) &&
    top.some((r) => typeof r === "string" && r.trim())
  ) {
    return true;
  }
  const dep = cfg["deployment"] as Record<string, unknown> | undefined;
  const depTargets = dep?.["targetRepos"];
  return (
    Array.isArray(depTargets) &&
    depTargets.some((r) => typeof r === "string" && r.trim())
  );
}

export function configuredInstanceRepos(
  cfg: Record<string, unknown> | null | undefined,
): InstanceReposSummary {
  if (!cfg) return { hasTargetAllowlist: false, repos: [] };

  const seen = new Map<string, string>();
  const add = (raw: unknown): void => {
    if (typeof raw !== "string") return;
    const trimmed = raw.trim();
    if (!trimmed) return;
    const key = trimmed.toLowerCase();
    if (!seen.has(key)) seen.set(key, trimmed);
  };
  const addList = (raw: unknown): void => {
    if (!Array.isArray(raw)) return;
    for (const item of raw) {
      if (typeof item === "string") {
        add(item);
      } else if (item && typeof item === "object") {
        add((item as { repo?: unknown }).repo);
      }
    }
  };

  const dep = cfg["deployment"] as Record<string, unknown> | undefined;
  addList(cfg["targetRepos"]);
  addList(dep?.["targetRepos"]);
  addList(dep?.["repos"]);
  add(cfg["defaultTarget"]);

  addList(cfg["reposWithWorkflows"]);
  addList(cfg["reposWithPromptExtension"]);
  addList(cfg["reposWithSetupCommand"]);
  addList(cfg["reposWithCICapabilities"]);
  addList(cfg["reposWithReleaseSettings"]);

  const capMap = cfg["repoDefaultCapabilities"];
  if (capMap && typeof capMap === "object" && !Array.isArray(capMap)) {
    for (const [repo, ids] of Object.entries(capMap)) {
      if (Array.isArray(ids) && ids.length > 0) add(repo);
    }
  }
  for (const mapKey of ["repoDefaultAdmissions", "repoDefaultHostings"]) {
    const m = cfg[mapKey];
    if (m && typeof m === "object" && !Array.isArray(m)) {
      for (const [repo, val] of Object.entries(m)) {
        if (typeof val === "string" && val.trim()) add(repo);
      }
    }
  }

  return {
    hasTargetAllowlist: hasExplicitTargetAllowlist(cfg),
    repos: [...seen.values()].sort((a, b) => a.localeCompare(b)),
  };
}
