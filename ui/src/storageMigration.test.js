import { beforeEach, describe, expect, it } from "vitest";
import {
  STORAGE_VERSION_KEY,
  mergePinnedLists,
  migrateStorage,
  pinnedCandidates,
} from "./storageMigration.js";
import { loadPinnedItems } from "./pinnedNav.js";
import { readStoredTimeZone } from "./PreferencesContext.jsx";

const set = (key, value) =>
  localStorage.setItem(
    key,
    typeof value === "string" ? value : JSON.stringify(value),
  );
const pinned = () => JSON.parse(localStorage.getItem("grain.pinned"));
const keys = () => Object.keys({ ...localStorage }).sort();

// docs/design/settings-panel.md §6.3: this browser's per-user keys become
// deployment-wide ones, and nothing anyone had pinned or chosen is lost.
describe("migrateStorage", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("keeps the remembered name's pins and preferences", () => {
    set("grain.username", "alice");
    set("grain.pinnedNav.alice.default", [
      { kind: "view", id: "inbox" },
      { kind: "board", id: "b1", icon: "boards", color: "#ef4444" },
    ]);
    set("grain.userTimeZone", "Europe/London");
    set("grain.themeMode", "dark");
    set("grain.timelineOrder", "oldest");

    expect(migrateStorage()).toBe(true);

    expect(loadPinnedItems()).toEqual([
      { kind: "view", id: "inbox" },
      { kind: "board", id: "b1", icon: "boards", color: "#ef4444" },
    ]);
    expect(readStoredTimeZone()).toBe("Europe/London");
    // Already deployment-wide: untouched.
    expect(localStorage.getItem("grain.themeMode")).toBe("dark");
    expect(localStorage.getItem("grain.timelineOrder")).toBe("oldest");
  });

  it("removes every old key and records the version", () => {
    set("grain.username", "alice");
    set("grain.pinnedNav.alice.default", [{ kind: "view", id: "ci" }]);
    set("grain.pinnedNav..default", [{ kind: "view", id: "repos" }]);
    set("grain.pinnedNav.alice", [{ kind: "view", id: "tasks" }]);
    set("grain.pinnedNav", [{ kind: "view", id: "boards" }]);
    set("grain.pinnedNav.bob.infra", [{ kind: "view", id: "sessions" }]);
    set("grain.userTimeZone", "UTC");
    set("grain.themeMode", "light");

    migrateStorage();

    expect(keys()).toEqual([
      "grain.pinned",
      "grain.storageVersion",
      "grain.themeMode",
      "grain.timeZone",
    ]);
    expect(localStorage.getItem(STORAGE_VERSION_KEY)).toBe("1");
  });

  it("merges two names' lists: the remembered one first, no duplicates, order kept", () => {
    set("grain.username", "alice");
    set("grain.pinnedNav.alice.default", [
      { kind: "board", id: "b1" },
      { kind: "view", id: "ci" },
    ]);
    set("grain.pinnedNav.bob.default", [
      { kind: "view", id: "ci", color: "#22c55e" },
      { kind: "session", id: "s1" },
      { kind: "session", id: "s2" },
    ]);

    migrateStorage();

    expect(pinned()).toEqual([
      { kind: "board", id: "b1" },
      { kind: "view", id: "ci" },
      { kind: "session", id: "s1" },
      { kind: "session", id: "s2" },
    ]);
  });

  // Each of §6.3 step 1's candidates, when it is the best one stored,
  // leads grain.pinned, and every lower one is merged in behind it.
  it.each([
    ["the remembered name's", 0],
    ["the nameless", 1],
    ["another name's", 2],
    ["the remembered name's pre-team", 3],
    ["the pre-team", 4],
    ["another team's", 5],
  ])("puts %s list first when it is the best one stored", (_, from) => {
    const tiers = [
      "grain.pinnedNav.alice.default",
      "grain.pinnedNav..default",
      "grain.pinnedNav.bob.default",
      "grain.pinnedNav.alice",
      "grain.pinnedNav",
      "grain.pinnedNav.alice.infra",
    ];
    set("grain.username", "alice");
    tiers
      .slice(from)
      .forEach((key, i) => set(key, [{ kind: "board", id: `b${from + i}` }]));

    expect(migrateStorage()).toBe(true);
    expect(pinned()).toEqual(
      tiers.slice(from).map((_, i) => ({ kind: "board", id: `b${from + i}` })),
    );
    expect(keys()).toEqual(["grain.pinned", "grain.storageVersion"]);
  });

  it("maps the views Processes replaced (§5.6) while moving the list", () => {
    set("grain.pinnedNav..default", [
      { kind: "view", id: "schedules", color: "#3b82f6" },
      { kind: "view", id: "templates" },
    ]);
    migrateStorage();
    expect(pinned()).toEqual([
      { kind: "view", id: "processes", color: "#3b82f6" },
    ]);
  });

  it("maps the views that became a System tab or a Settings section", () => {
    set("grain.username", "alice");
    set("grain.pinnedNav.alice.default", [
      { kind: "view", id: "sandboxes", color: "#22c55e" },
    ]);
    set("grain.pinnedNav.bob.default", [
      { kind: "view", id: "caches" },
      { kind: "view", id: "memory" },
    ]);
    migrateStorage();
    expect(pinned()).toEqual([
      { kind: "view", id: "system", tab: "sandboxes", color: "#22c55e" },
      { kind: "view", id: "system", tab: "caches" },
      { kind: "view", id: "settings", section: "memory" },
    ]);
  });

  it("keeps an emptied list empty rather than restoring the defaults", () => {
    set("grain.username", "alice");
    set("grain.pinnedNav.alice.default", []);
    migrateStorage();
    expect(loadPinnedItems()).toEqual([]);
  });

  it("writes no list when there was none, so the defaults show", () => {
    set("grain.username", "alice");
    migrateStorage();
    expect(localStorage.getItem("grain.pinned")).toBeNull();
    expect(keys()).toEqual(["grain.storageVersion"]);
  });

  it("drops a list that does not parse and still finishes", () => {
    set("grain.pinnedNav..default", "invalid JSON {");
    set("grain.pinnedNav.bob.default", [{ kind: "view", id: "ci" }]);
    expect(migrateStorage()).toBe(true);
    expect(pinned()).toEqual([{ kind: "view", id: "ci" }]);
    expect(localStorage.getItem("grain.pinnedNav..default")).toBeNull();
  });

  it("does not overwrite a time zone already under the new key", () => {
    set("grain.userTimeZone", "Asia/Tokyo");
    set("grain.timeZone", "UTC");
    migrateStorage();
    expect(readStoredTimeZone()).toBe("UTC");
    expect(localStorage.getItem("grain.userTimeZone")).toBeNull();
  });

  it("is idempotent", () => {
    set("grain.username", "alice");
    set("grain.pinnedNav.alice.default", [{ kind: "view", id: "ci" }]);
    set("grain.userTimeZone", "UTC");
    migrateStorage();
    const first = { ...localStorage };
    // Even with the version key gone, a second run changes nothing.
    localStorage.removeItem(STORAGE_VERSION_KEY);
    migrateStorage();
    expect({ ...localStorage }).toEqual(first);
  });

  it("leaves an already-migrated store alone", () => {
    set(STORAGE_VERSION_KEY, "1");
    set("grain.pinned", [{ kind: "view", id: "ci" }]);
    set("grain.username", "alice");
    set("grain.pinnedNav.alice.default", [{ kind: "board", id: "b1" }]);
    expect(migrateStorage()).toBe(true);
    expect(pinned()).toEqual([{ kind: "view", id: "ci" }]);
    expect(
      localStorage.getItem("grain.pinnedNav.alice.default"),
    ).not.toBeNull();
  });

  it("treats unavailable storage as no error", () => {
    expect(
      migrateStorage(() => {
        throw new Error("SecurityError");
      }),
    ).toBe(false);
    expect(migrateStorage(() => null)).toBe(false);
  });

  it("leaves the version unwritten when a step throws, so the next load retries", () => {
    set("grain.username", "alice");
    set("grain.pinnedNav.alice.default", [{ kind: "view", id: "ci" }]);
    const real = localStorage;
    let failing = true;
    const flaky = {
      get length() {
        return real.length;
      },
      key: (i) => real.key(i),
      getItem: (k) => real.getItem(k),
      setItem: (k, v) => {
        if (failing) throw new Error("QuotaExceededError");
        real.setItem(k, v);
      },
      removeItem: (k) => real.removeItem(k),
    };
    expect(migrateStorage(() => flaky)).toBe(false);
    expect(real.getItem(STORAGE_VERSION_KEY)).toBeNull();
    expect(real.getItem("grain.pinnedNav.alice.default")).not.toBeNull();

    failing = false;
    expect(migrateStorage(() => flaky)).toBe(true);
    expect(pinned()).toEqual([{ kind: "view", id: "ci" }]);
  });
});

describe("pinnedCandidates", () => {
  it("orders the old lists as §6.3 step 1 does", () => {
    const stored = [
      "grain.themeMode",
      "grain.pinnedNav.zed.infra",
      "grain.pinnedNav",
      "grain.pinnedNav.carol.default",
      "grain.pinnedNav.alice",
      "grain.pinnedNav.bob.default",
      "grain.pinnedNav..default",
      "grain.pinnedNav.alice.default",
    ];
    expect(pinnedCandidates(stored, "alice")).toEqual([
      "grain.pinnedNav.alice.default",
      "grain.pinnedNav..default",
      "grain.pinnedNav.bob.default",
      "grain.pinnedNav.carol.default",
      "grain.pinnedNav.alice",
      "grain.pinnedNav",
      "grain.pinnedNav.zed.infra",
    ]);
  });

  it("with no remembered name, takes the nameless list first", () => {
    expect(
      pinnedCandidates(
        ["grain.pinnedNav.alice.default", "grain.pinnedNav..default"],
        "",
      ),
    ).toEqual(["grain.pinnedNav..default", "grain.pinnedNav.alice.default"]);
  });

  it("puts a list already moved ahead of every old one", () => {
    expect(
      pinnedCandidates(["grain.pinnedNav..default", "grain.pinned"], ""),
    ).toEqual(["grain.pinned", "grain.pinnedNav..default"]);
  });
});

describe("mergePinnedLists", () => {
  it("keeps two System tabs or Settings sections apart", () => {
    expect(
      mergePinnedLists([
        [{ kind: "view", id: "system", tab: "sandboxes" }],
        [
          { kind: "view", id: "system", tab: "caches" },
          { kind: "view", id: "system", tab: "sandboxes" },
          { kind: "view", id: "settings", section: "memory" },
        ],
      ]),
    ).toEqual([
      { kind: "view", id: "system", tab: "sandboxes" },
      { kind: "view", id: "system", tab: "caches" },
      { kind: "view", id: "settings", section: "memory" },
    ]);
  });

  it("appends only what is not already there, by kind and id", () => {
    expect(
      mergePinnedLists([
        [{ kind: "view", id: "ci" }],
        [
          { kind: "board", id: "ci" },
          { kind: "view", id: "ci" },
        ],
      ]),
    ).toEqual([
      { kind: "view", id: "ci" },
      { kind: "board", id: "ci" },
    ]);
  });
});
