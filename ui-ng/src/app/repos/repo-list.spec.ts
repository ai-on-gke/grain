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

import { describe, expect, it } from "vitest";
import type { GrainConfig, Task } from "../api/models";
import {
  REPO_ORDER_KEY,
  discoveryLine,
  firstAddWarning,
  loadRepoOrder,
  moveRow,
  repoActivity,
  repoHostOf,
  repoRows,
  saveRepoOrder,
  sortRows,
  stateCounts,
} from "./repo-list";

const task = (over: Partial<Task>): Task =>
  ({ id: "1", title: "t", state: "queued", ...over }) as Task;

describe("repoRows", () => {
  it("unions config repo, targets and configured repos, folding spellings", () => {
    const config = {
      configRepo: "grain/config",
      targetRepos: ["Acme/Widgets"],
      reposWithSetupCommand: ["acme/tools"],
    } as GrainConfig;
    const rows = repoRows(config, [
      task({ repo: "acme/widgets", state: "running" }),
      task({ repo: "acme/widgets", state: "queued", blocked: true }),
      task({ repo: "acme/elsewhere" }),
      task({ repo: "acme/widgets", interactive: true }),
    ]);
    expect(rows.map((r) => r.repo)).toEqual([
      "acme/tools",
      "Acme/Widgets",
      "grain/config",
    ]);
    const w = rows.find((r) => r.repo === "Acme/Widgets")!;
    expect(w).toMatchObject({ total: 2, blocked: 1, configured: true });
    expect(w.counts).toEqual({ running: 1, queued: 1 });
    expect(rows.find((r) => r.repo === "acme/tools")!.defaults).toBe(true);
    expect(rows.find((r) => r.repo === "grain/config")!.configRepo).toBe(true);
  });

  it("lets a task raise a row only on an unrestricted deployment", () => {
    const rows = repoRows({} as GrainConfig, [task({ repo: "acme/a" })]);
    expect(rows.map((r) => r.repo)).toEqual(["acme/a"]);
  });
});

describe("stateCounts and repoActivity", () => {
  it("orders chips in task-list order and reads activity", () => {
    expect(
      stateCounts({ failed: 1, proposed: 2, running: 1 }).map((c) => c.state),
    ).toEqual(["proposed", "running", "failed"]);
    const base = {
      repo: "a",
      blocked: 0,
      configured: true,
      defaults: false,
      configRepo: false,
    };
    expect(
      repoActivity({ ...base, total: 1, counts: { running: 1 } }).key,
    ).toBe("running");
    expect(repoActivity({ ...base, total: 2, counts: { queued: 2 } }).key).toBe(
      "open",
    );
    expect(
      repoActivity({ ...base, total: 1, counts: { deferred: 1 } }).key,
    ).toBe("idle");
    expect(repoActivity({ ...base, total: 0, counts: {} }).key).toBe("idle");
  });
});

describe("sorting and the custom order", () => {
  const rows = repoRows(
    { targetRepos: ["a/one", "a/two", "a/three"] } as GrainConfig,
    [
      task({ repo: "a/three" }),
      task({ repo: "a/three" }),
      task({ repo: "a/two", state: "running" }),
    ],
  );

  it("puts rows the stored order never placed after it, by name", () => {
    expect(sortRows(rows, "custom", ["a/two"]).map((r) => r.repo)).toEqual([
      "a/two",
      "a/one",
      "a/three",
    ]);
    expect(sortRows(rows, "tasks", []).map((r) => r.repo)[0]).toBe("a/three");
    expect(sortRows(rows, "active", []).map((r) => r.repo)[0]).toBe("a/two");
  });

  it("moves a row and round-trips through the classic storage key", () => {
    const ids = moveRow(sortRows(rows, "custom", []), "a/two", 0);
    expect(ids).toEqual(["a/two", "a/one", "a/three"]);
    const store = new Map<string, string>();
    const storage = {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => store.set(k, v),
      removeItem: (k: string) => store.delete(k),
    } as unknown as Storage;
    saveRepoOrder(storage, ids);
    expect(JSON.parse(store.get(REPO_ORDER_KEY)!)).toEqual(ids);
    expect(loadRepoOrder(storage)).toEqual(ids);
    store.set(REPO_ORDER_KEY, "{not json");
    expect(loadRepoOrder(storage)).toEqual([]);
  });
});

describe("add repo helpers", () => {
  it("reads the host a typed repo names", () => {
    expect(repoHostOf("acme/widgets")).toBe("");
    expect(repoHostOf("git.example.com/team/repo")).toBe("git.example.com");
    expect(repoHostOf("https://Git.Example.com/team/repo.git")).toBe(
      "git.example.com",
    );
    expect(repoHostOf("git@git.example.com:team/repo.git")).toBe(
      "git.example.com",
    );
  });

  it("warns only for the first add, naming what falls off", () => {
    const rows = repoRows({} as GrainConfig, [task({ repo: "acme/old" })]);
    expect(firstAddWarning(["x/y"], rows, "acme/new")).toBeNull();
    const w = firstAddWarning([], rows, "acme/new")!;
    expect(w).toContain("narrows it to that one repo");
    expect(w).toContain("Off the allowlist as of this click: acme/old.");
  });

  it("words discovery the way the classic picker does", () => {
    expect(discoveryLine({ loading: true }).kind).toBe("loading");
    expect(
      discoveryLine({
        available: true,
        repos: [{ repo: "a/b" }, { repo: "a/c" }],
        connections: [{ credential: "gh", host: "github.com", ok: true }],
      }).text,
    ).toBe("2 repos discovered from 1 Git connection");
    const none = discoveryLine({ available: false, reason: "no connection" });
    expect(none).toMatchObject({ kind: "none", reason: "no connection" });
  });
});
