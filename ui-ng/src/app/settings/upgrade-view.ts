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

// Pure helpers behind Settings -> Upgrade (settings-upgrade.component.ts):
// the same readings of GET /api/upgrade, /api/upgrade/targets and
// /api/upgrade/release-notes the classic ui/src/components/UpgradePanel.jsx
// and UpgradeReleaseNotes.jsx make, kept free of Angular so they are unit
// tested on their own.

// UpgradeTarget is one entry of GET /api/upgrade/targets' `targets`
// (pkg/upgrade/targets.go's Target). `branch` and `version` are what an
// older daemon's bare array carried, still read so it keeps working.
export interface UpgradeTarget {
  name: string;
  kind: string;
  allowed: boolean;
  version?: string;
}

// DiscardedRC is pkg/upgrade.DiscardedRC: the release candidate this
// deployment runs failed qualification.
export interface DiscardedRC {
  running: string;
  latest?: string;
  replacementRc?: string;
}

// RetractedRelease is pkg/upgrade.RetractedRelease: the promoted release
// this deployment runs was retracted.
export interface RetractedRelease {
  running: string;
  latest?: string;
  newer?: string;
}

export interface UpgradeTargets {
  targets: UpgradeTarget[];
  allowedBranches: string[];
  versionsError: string;
  default: string;
  discardedRc: DiscardedRC | null;
  retractedRelease: RetractedRelease | null;
}

// TARGET_GROUPS is the heading each kind of target is listed under, as in
// the classic BranchPicker.
export const TARGET_GROUPS: Record<string, string> = {
  channel: "Standard branches",
  version: "Released versions",
  allowed: "Allowed on this deployment",
};

// CHANNEL_NOTES is what each moving name means, beside it in the list.
export const CHANNEL_NOTES: Record<string, string> = {
  latest: "the last promoted release",
  candidate: "the current release candidate (alias: rc)",
  main: "the head of main",
};

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}

function obj(v: unknown): Record<string, unknown> | null {
  return v && typeof v === "object" && !Array.isArray(v)
    ? (v as Record<string, unknown>)
    : null;
}

export function readDiscardedRC(v: unknown): DiscardedRC | null {
  const o = obj(v);
  if (!o || !str(o["running"])) return null;
  return {
    running: str(o["running"]),
    latest: str(o["latest"]) || undefined,
    replacementRc: str(o["replacementRc"]) || undefined,
  };
}

export function readRetractedRelease(v: unknown): RetractedRelease | null {
  const o = obj(v);
  if (!o || !str(o["running"])) return null;
  return {
    running: str(o["running"]),
    latest: str(o["latest"]) || undefined,
    newer: str(o["newer"]) || undefined,
  };
}

// readTargets reads GET /api/upgrade/targets: the object current daemons
// answer, or the bare array of {branch, version} older ones did.
export function readTargets(raw: unknown): UpgradeTargets {
  const o = obj(raw);
  const list: unknown[] = Array.isArray(raw)
    ? raw
    : Array.isArray(o?.["targets"])
      ? (o["targets"] as unknown[])
      : [];
  const targets: UpgradeTarget[] = [];
  for (const item of list) {
    const t = obj(item);
    if (!t) continue;
    const name = str(t["name"]) || str(t["branch"]);
    if (!name) continue;
    targets.push({
      name,
      kind: str(t["kind"]) || "channel",
      // An older daemon sent no `allowed`; it refused nothing up front.
      allowed: t["allowed"] !== false,
      version: str(t["version"]) || str(t["ref"]) || undefined,
    });
  }
  const allowed = Array.isArray(o?.["allowedBranches"])
    ? (o["allowedBranches"] as unknown[]).map(String)
    : [];
  return {
    targets,
    allowedBranches: allowed,
    versionsError: str(o?.["versionsError"]),
    default: str(o?.["default"]),
    discardedRc: readDiscardedRC(o?.["discardedRc"]),
    retractedRelease: readRetractedRelease(o?.["retractedRelease"]),
  };
}

export interface TargetGroup {
  label: string;
  options: UpgradeTarget[];
}

// filterTargets is the classic BranchPicker's filterOptions: the whole
// list when the box is empty or already holds a listed name (the point of
// opening it then is to see the other choices), otherwise the names that
// contain what was typed -- grouped by kind in the order kinds first
// appear.
export function filterTargets(
  targets: readonly UpgradeTarget[],
  query: string,
): TargetGroup[] {
  const q = query.trim().toLowerCase();
  const all = q === "" || targets.some((t) => t.name.toLowerCase() === q);
  const groups: TargetGroup[] = [];
  for (const t of targets) {
    if (!all && !t.name.toLowerCase().includes(q)) continue;
    const label = TARGET_GROUPS[t.kind] ?? "";
    let g = groups.find((x) => x.label === label);
    if (!g) {
      g = { label, options: [] };
      groups.push(g);
    }
    g.options.push(t);
  }
  return groups;
}

// pickerHelp is the classic BranchPicker's helper text.
export function pickerHelp(
  t: UpgradeTargets | null,
  listError: string,
): string {
  let help =
    "latest is the last promoted release, main is the head of main, and candidate (or rc) is the current release candidate.";
  if (listError) {
    help += ` The list of branches could not be loaded (${listError}); type a branch instead.`;
  } else if (t) {
    const anyBranch = t.allowedBranches.includes("**");
    if (!anyBranch && t.allowedBranches.length > 0) {
      help += ` Pick one from the list or type any branch; this deployment only upgrades to ${t.allowedBranches.join(", ")} (-upgrade-allowed-branches).`;
    } else {
      help += " Pick one from the list or type any branch.";
    }
  }
  if (t?.versionsError) {
    help += ` Released versions could not be listed: ${t.versionsError}`;
  }
  return help;
}

export function nextRCVersion(running: string): string {
  const m = /^(.+-rc\.)(\d+)(.*)$/.exec(running || "");
  if (!m) return "the next candidate";
  return `${m[1]}${Number(m[2]) + 1}${m[3]}`;
}

// tagForBranch is pkg/upgrade's TagForBranch: each '/' written as "..".
export function tagForBranch(branch: string): string {
  return branch.replaceAll("/", "..");
}

function imageTagOf(ref: string): string {
  const at = ref.indexOf("@");
  const base = at >= 0 ? ref.slice(0, at) : ref;
  const slash = base.lastIndexOf("/");
  const colon = base.lastIndexOf(":");
  return colon > slash ? base.slice(colon + 1) : "";
}

function hasTagOrDigest(ref: string): boolean {
  return ref.includes("@") || imageTagOf(ref) !== "";
}

// resolvedImage is the reference an upgrade onto branch would pull: the
// operator's image as typed when it names a build, otherwise branch's tag
// in the chosen repository, or in the one this deployment pulls from
// (the classic pane's resolvedImage).
export function resolvedImage(
  image: string,
  repo: string,
  branch: string,
): string {
  const chosen = (image || "").trim();
  if (chosen && hasTagOrDigest(chosen)) return chosen;
  const from = chosen || repo || "";
  const target = (branch || "").trim();
  if (!from || !target) return from;
  return `${from}:${tagForBranch(target)}`;
}

export interface ReleaseNote {
  version: string;
  title: string;
  markdown: string;
  breaking: boolean;
}

export interface ReleaseNotesPreview {
  from: string;
  version: string;
  message: string;
  notInThisBuild: boolean;
  notes: ReleaseNote[];
}

// hasBreakingChange reports whether a version's notes call out a breaking
// change the way scripts/release-notes.sh and hand-written notes do: a
// "Breaking changes" heading, a bold "Breaking" lead-in, a
// "BREAKING CHANGE:" trailer, or "(breaking)" after an item.
export function hasBreakingChange(markdown: string): boolean {
  return (
    /^\s{0,3}#{1,6}\s*breaking\b/im.test(markdown) ||
    /(\*\*|__)\s*breaking\b/i.test(markdown) ||
    /\bBREAKING[ -]CHANGES?\b/.test(markdown) ||
    /\(breaking\)/i.test(markdown)
  );
}

// readReleaseNotes reads GET /api/upgrade/release-notes
// (pkg/upgrade/releasenotes.go's preview). A bare string is shown as one
// note.
export function readReleaseNotes(raw: unknown): ReleaseNotesPreview {
  if (typeof raw === "string") {
    return {
      from: "",
      version: "",
      message: "",
      notInThisBuild: false,
      notes: raw.trim()
        ? [
            {
              version: "",
              title: "",
              markdown: raw,
              breaking: hasBreakingChange(raw),
            },
          ]
        : [],
    };
  }
  const o = obj(raw) ?? {};
  const notes: ReleaseNote[] = [];
  if (Array.isArray(o["notes"])) {
    for (const item of o["notes"] as unknown[]) {
      const n = obj(item);
      if (!n) continue;
      const version = str(n["version"]);
      const md =
        str(n["markdown"]) ||
        str(n["body"]) ||
        str(n["text"]) ||
        (version ? `# ${version}` : "");
      if (!md) continue;
      notes.push({
        version,
        title: str(n["title"]),
        markdown: md,
        breaking: hasBreakingChange(md),
      });
    }
  } else if (str(o["notes"]).trim()) {
    const md = str(o["notes"]);
    notes.push({
      version: "",
      title: "",
      markdown: md,
      breaking: hasBreakingChange(md),
    });
  }
  return {
    from: str(o["from"]),
    version: str(o["version"]),
    message: str(o["message"]),
    notInThisBuild: o["notInThisBuild"] === true,
    notes,
  };
}

// capitalize starts the server's message as a sentence -- unless it
// starts with a version, whose "v" is part of its name.
export function capitalize(text: string): string {
  if (!text || /^v\d/.test(text)) return text;
  return text[0].toUpperCase() + text.slice(1);
}

export const UPGRADE_CHECK_LABELS: Record<string, string> = {
  off: "Don't",
  approve: "File a proposal to approve",
  auto: "File it and queue it (default)",
};

export const UPGRADE_CHECK_HELP: Record<string, string> = {
  off: "An upgrade lands and grain says nothing further about it.",
  approve:
    "A task is filed at the head of the backlog after each upgrade, and runs once somebody approves it.",
  auto: "A task is filed at the head of the backlog after each upgrade and is the next thing dispatched. It reads this host's own source checkout and this deployment's config repository, and reports what it found in a comment.",
};

// OFFLINE_MIGRATION_COMMANDS move a SQLite store into PostgreSQL with the
// daemon stopped (docs/database.md), as the classic DatabaseBackend lists.
export const OFFLINE_MIGRATION_COMMANDS = [
  "sudo systemctl stop grain-daemon.service",
  "sudo ./scripts/setup.sh --db-driver postgres",
  "sudo grain db check -data-dir /var/lib/grain/data",
].join("\n");

export interface UpgradeNotice {
  message: string;
  command: string;
  docsUrl: string;
}

export function readNotices(v: unknown): UpgradeNotice[] {
  if (!Array.isArray(v)) return [];
  const out: UpgradeNotice[] = [];
  for (const item of v) {
    const n = obj(item);
    if (!n || !str(n["message"])) continue;
    out.push({
      message: str(n["message"]),
      command: str(n["command"]),
      docsUrl: safeHttpUrl(n["docsUrl"]),
    });
  }
  return out;
}

// safeHttpUrl keeps only an absolute http(s) link.
export function safeHttpUrl(raw: unknown): string {
  if (typeof raw !== "string" || !raw) return "";
  try {
    const u = new URL(raw);
    return u.protocol === "https:" || u.protocol === "http:" ? u.href : "";
  } catch {
    return "";
  }
}
