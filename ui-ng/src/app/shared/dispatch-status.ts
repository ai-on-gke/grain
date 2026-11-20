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

import type { AgentPauseEntry, GrainConfig } from "../api/models";

export interface UsagePauseScope {
  connection?: string;
  connectionName?: string;
  model?: string;
  pausedUntil?: string;
  // "weekly" when the pause met the provider's weekly limit rather than
  // its interval window (pkg/usage PauseHorizon).
  pauseHorizon?: string;
  weeklyPausedUntil?: string;
}

export interface UsagePauseInput {
  mode?: string;
  maxWorkers?: number;
  effectiveWorkers?: number;
  warnings?: string[];
  advisories?: string[];
  scopes?: UsagePauseScope[];
}

export interface DispatchDetailItem {
  label: string;
  reason: string;
  resetText: string;
  tone: "error" | "warn";
}

// PauseWindow is one model-quota pause as a viewer can hide it: what is
// paused (id: the connection, model, team and framework, or the usage
// scope) and when that window ends (untilMs; null when the server gave
// no end). Hiding remembers these, so the same pauses stay hidden across
// countdown polls while a new pause, a new window on the same model, or
// an escalation from one model to its whole connection shows again.
export interface PauseWindow {
  id: string;
  untilMs: number | null;
}

export interface DispatchStatusBanner {
  key: string;
  tone: "error" | "warn";
  headline: string;
  subline: string;
  resetText: string;
  shortLabel: string;
  details: DispatchDetailItem[];
  // hideable is whether a viewer may hide this banner until its pauses
  // reset (GU-6): it says nothing but model-quota pauses while other
  // models still dispatch. A reconciler outage, a failed dispatch
  // precondition, a deployment pause, or quotas with no fallback model
  // left is never hideable.
  hideable: boolean;
  // pauses are the model-quota pause windows behind the banner.
  pauses: PauseWindow[];
}

// PAUSE_WINDOW_SLACK_MS is how far one pause's reported end may move
// between polls and still be the same window: a server that derives
// `until` from seconds remaining jitters by seconds, while a fresh quota
// hit moves it by far more.
export const PAUSE_WINDOW_SLACK_MS = 5 * 60_000;

// HIDDEN_NO_END_MS is how long a hidden pause with no known end stays
// hidden before it shows again.
export const HIDDEN_NO_END_MS = 12 * 3600_000;

export interface HiddenPause extends PauseWindow {
  hiddenAtMs: number;
}

// pauseWindowsHidden is whether every pause behind banner is one the
// viewer hid, in the same window. A banner that is not hideable, or has
// no pauses, is never hidden.
export function pauseWindowsHidden(
  banner: DispatchStatusBanner | null | undefined,
  hidden: readonly HiddenPause[],
): boolean {
  if (!banner?.hideable || banner.pauses.length === 0) return false;
  return banner.pauses.every((p) =>
    hidden.some(
      (h) =>
        h.id === p.id &&
        (p.untilMs === null || h.untilMs === null
          ? p.untilMs === h.untilMs
          : Math.abs(p.untilMs - h.untilMs) <= PAUSE_WINDOW_SLACK_MS),
    ),
  );
}

// pruneHiddenPauses drops hidden pauses whose window has ended (or, with
// no known end, that were hidden long enough ago), so the list does not
// grow and an ended window cannot hide its successor.
export function pruneHiddenPauses(
  hidden: readonly HiddenPause[],
  nowMs: number,
): HiddenPause[] {
  return hidden.filter((h) =>
    h.untilMs === null
      ? nowMs - h.hiddenAtMs < HIDDEN_NO_END_MS
      : h.untilMs + PAUSE_WINDOW_SLACK_MS > nowMs,
  );
}

function agentPauseId(p: AgentPauseEntry): string {
  return [
    "agent",
    p.team ?? "",
    p.connection ?? "",
    p.model ?? "",
    p.framework ?? "",
  ].join("|");
}

export function effectiveDisplayTimeZone(configTz?: string | null): string {
  try {
    const stored = globalThis.localStorage?.getItem("grain.timeZone")?.trim();
    if (stored && stored.toLowerCase() !== "local") {
      return resolveDisplayTimeZone(stored);
    }
  } catch {
    // Ignore storage restriction and fall back to config/browser zone.
  }
  return resolveDisplayTimeZone(configTz);
}

export function resolveDisplayTimeZone(preferred?: string | null): string {
  const clean = (preferred ?? "").trim();
  if (clean && clean.toLowerCase() !== "local") {
    try {
      new Intl.DateTimeFormat(undefined, { timeZone: clean });
      return clean;
    } catch {
      // Invalid IANA zone: fall back to runtime zone.
    }
  }
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

export function formatRemainingDuration(diffSec: number): string {
  if (!Number.isFinite(diffSec) || diffSec <= 0) return "soon";
  const totalSec = Math.ceil(diffSec);
  if (totalSec < 60) return `in ${totalSec}s`;
  const totalMin = Math.round(totalSec / 60);
  if (totalMin < 60) return `in ${totalMin}m`;
  const h = Math.floor(totalMin / 60);
  const m = totalMin % 60;
  return m > 0 ? `in ${h}h ${m}m` : `in ${h}h`;
}

export function parseResetMs(
  until?: string,
  secondsRemaining?: number,
  nowMs = Date.now(),
): number | null {
  if (until) {
    const parsed = Date.parse(until);
    if (Number.isFinite(parsed) && parsed > 0) return parsed;
  }
  if (
    secondsRemaining !== undefined &&
    Number.isFinite(secondsRemaining) &&
    secondsRemaining > 0
  ) {
    return nowMs + secondsRemaining * 1000;
  }
  return null;
}

// formatStamp prints an absolute server timestamp (RFC 3339, nanoseconds
// and all) in the display time zone -- Preferences' choice, else the
// deployment's, else the browser's -- as "Nov 14, 2026, 12:02:32 UTC".
// A value that does not parse is returned as it came.
export function formatStamp(at: string, preferredTz?: string | null): string {
  const ms = Date.parse(at);
  if (!at || Number.isNaN(ms)) return at;
  try {
    return new Intl.DateTimeFormat(undefined, {
      timeZone: effectiveDisplayTimeZone(preferredTz),
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      timeZoneName: "short",
    }).format(new Date(ms));
  } catch {
    return at;
  }
}

export function formatLocalReset(
  until?: string,
  secondsRemaining?: number,
  nowMs = Date.now(),
  preferredTz?: string | null,
): string {
  const targetMs = parseResetMs(until, secondsRemaining, nowMs);
  if (targetMs === null) return "";
  const tz = resolveDisplayTimeZone(preferredTz);
  const dt = new Date(targetMs);
  const now = new Date(nowMs);
  let clock = "";
  try {
    const dayFmt = new Intl.DateTimeFormat("en-CA", {
      timeZone: tz,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    });
    const sameDay = dayFmt.format(dt) === dayFmt.format(now);
    const fmt = new Intl.DateTimeFormat(undefined, {
      timeZone: tz,
      ...(sameDay ? {} : { month: "short", day: "numeric" }),
      hour: "numeric",
      minute: "2-digit",
      timeZoneName: "short",
    });
    clock = fmt.format(dt);
  } catch {
    clock = dt.toISOString();
  }
  const rel = formatRemainingDuration((targetMs - nowMs) / 1000);
  return `${clock} (${rel})`;
}

const NOTICE_MONTHS = [
  "Jan",
  "Feb",
  "Mar",
  "Apr",
  "May",
  "Jun",
  "Jul",
  "Aug",
  "Sep",
  "Oct",
  "Nov",
  "Dec",
];

// Formats a UTC clock phrase inside a backend warning string -- "14:05 UTC",
// or the daemon's 12-hour "2:05 PM UTC" -- into the user's local timezone
// alongside a relative reset countdown. A phrase with a date ("Nov 9 03:00
// UTC", a weekly limit's reset days away) is read on that date, in the year
// that puts it nearest now.
export function formatNoticeText(
  text: string,
  nowMs = Date.now(),
  preferredTz?: string | null,
): string {
  const clock =
    /\b(?:(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) (\d{1,2}) )?(\d{1,2}):(\d{2})(?:\s*([AaPp][Mm]))?\s*UTC\b/g;
  return text.replace(clock, (raw, mon, day, hh, mm, ampm?: string) => {
    let hour = Number(hh);
    if (ampm) {
      if (hour < 1 || hour > 12) return raw;
      hour = (hour % 12) + (ampm.toUpperCase() === "PM" ? 12 : 0);
    } else if (hh.length !== 2) {
      return raw;
    }
    const cur = new Date(nowMs);
    let target: number;
    if (mon) {
      const month = NOTICE_MONTHS.indexOf(mon);
      target = Date.UTC(
        cur.getUTCFullYear(),
        month,
        Number(day),
        hour,
        Number(mm),
      );
      // "Jan 2" read in late December is next year's.
      if (target < nowMs - 180 * 86_400_000) {
        target = Date.UTC(
          cur.getUTCFullYear() + 1,
          month,
          Number(day),
          hour,
          Number(mm),
        );
      }
    } else {
      target = Date.UTC(
        cur.getUTCFullYear(),
        cur.getUTCMonth(),
        cur.getUTCDate(),
        hour,
        Number(mm),
        0,
        0,
      );
      if (target < nowMs - 60_000) {
        target += 86_400_000;
      }
    }
    const local = formatLocalReset(
      new Date(target).toISOString(),
      undefined,
      nowMs,
      preferredTz,
    );
    if (!local) return raw;
    const tz = resolveDisplayTimeZone(preferredTz);
    return tz === "UTC" || /\bUTC\b/.test(local) ? local : `${local} [${raw}]`;
  });
}

function pauseEntryLabel(p: AgentPauseEntry): string {
  const conn = p.connectionName || p.connection || p.framework || "deployment";
  if (p.model) return `${conn} · ${p.model}`;
  return conn;
}

function configuredDefaultModels(
  cfg: GrainConfig | null | undefined,
): string[] {
  const rec = cfg as Record<string, unknown> | null | undefined;
  const dep = rec?.["deployment"] as Record<string, unknown> | undefined;
  const raw =
    rec?.["defaultModels"] ?? dep?.["defaultModels"] ?? cfg?.defaultModel;
  const items = Array.isArray(raw)
    ? raw
    : typeof raw === "string"
      ? raw.split(",")
      : [];
  return items
    .map((m) => (typeof m === "string" ? m.trim() : ""))
    .filter(Boolean);
}

export function summarizeDispatchStatus(
  cfg: GrainConfig | null | undefined,
  usage: UsagePauseInput | null | undefined,
  nowMs = Date.now(),
  preferredTz?: string | null,
  opts?: { modelQuotaOnly?: boolean },
): DispatchStatusBanner | null {
  const tzPref = preferredTz ?? effectiveDisplayTimeZone(cfg?.timeZone);
  const details: DispatchDetailItem[] = [];
  let worstTone: "error" | "warn" | null = null;
  let primaryHeadline = "";
  let primarySubline = "";
  let primaryReset = "";
  let shortLabel = "Dispatch paused";
  // nonQuota: something other than a model-quota pause is holding work.
  // starved: quotas leave no fallback model at all. Either makes the
  // banner one a viewer cannot hide.
  let nonQuota = false;
  let starved = false;
  const pauses: PauseWindow[] = [];

  const bump = (tone: "error" | "warn") => {
    if (worstTone !== "error") worstTone = tone;
  };

  if (!opts?.modelQuotaOnly && cfg?.reconcilerDown) {
    bump("error");
    nonQuota = true;
    primaryHeadline =
      "Reconciler unavailable — automated development dispatch is paused";
    primarySubline =
      "Background reconciliation is down until the reconciler recovers.";
    shortLabel = "Reconciler down";
    details.push({
      label: "Reconciler",
      reason: "Reconciler is not running",
      resetText: "",
      tone: "error",
    });
  }

  const blocked = opts?.modelQuotaOnly ? [] : (cfg?.dispatchBlocked ?? []);
  for (const d of blocked) {
    bump("error");
    nonQuota = true;
    const label = d.name || "Dispatch blocked";
    const reason = d.error || String(d.check ?? "") || "Precondition failed";
    const msg = [reason, d.remedy].filter(Boolean).join(" — ");
    const held = d.held ? `held ${d.held}` : "";
    details.push({ label, reason: msg, resetText: held, tone: "error" });
    if (!primaryHeadline) {
      primaryHeadline = `Automated development paused: ${label}`;
      primarySubline = msg;
      primaryReset = held;
      shortLabel = "Dispatch blocked";
    }
  }

  const pipe = opts?.modelQuotaOnly ? null : cfg?.pipelinePause;
  if (pipe?.paused) {
    const tone: "error" | "warn" = pipe.runNowPasses ? "warn" : "error";
    bump(tone);
    nonQuota = true;
    const who = pipe.byLabel || pipe.byId || "";
    const reason =
      pipe.reason || (who ? `Paused by ${who}` : "Pipeline paused");
    const sub = pipe.runNowPasses
      ? `${reason} — scheduled work is held (manual Run now still starts)`
      : `${reason} — all new runs are held`;
    const until = pipe.untilTask ? `until #${pipe.untilTask}` : "";
    details.push({
      label: "Pipeline",
      reason: sub,
      resetText: until,
      tone,
    });
    if (!primaryHeadline) {
      primaryHeadline = pipe.runNowPasses
        ? "Pipeline paused (manual Run now allowed)"
        : "Pipeline paused — development runs are held";
      primarySubline = reason;
      primaryReset = until;
      shortLabel = "Pipeline paused";
    }
  }

  const ap = cfg?.agentPause;
  const rawEntries = ap?.pauses ?? [];
  const entries = rawEntries.filter((p) => {
    if (opts?.modelQuotaOnly && !p.connection?.trim()) return false;
    const ms = parseResetMs(p.until, p.secondsRemaining, nowMs);
    return ms === null || ms > nowMs;
  });
  const apMs = ap ? parseResetMs(ap.until, ap.secondsRemaining, nowMs) : null;
  const apActive =
    !!ap?.paused &&
    (rawEntries.length > 0
      ? entries.length > 0
      : !opts?.modelQuotaOnly && (apMs === null || apMs > nowMs));
  if (ap && apActive) {
    const wholeConnPaused =
      entries.length === 0 || entries.some((p) => !p.model);
    const tone: "error" | "warn" = wholeConnPaused ? "error" : "warn";
    bump(tone);
    let latestMs: number | null = null;
    for (const p of entries) {
      const ms = parseResetMs(p.until, p.secondsRemaining, nowMs);
      if (ms !== null && (latestMs === null || ms > latestMs)) latestMs = ms;
      pauses.push({ id: agentPauseId(p), untilMs: ms });
      details.push({
        label: pauseEntryLabel(p),
        reason:
          p.reason || p.escalated || "Model quota or credential limit reached",
        resetText: formatLocalReset(p.until, p.secondsRemaining, nowMs, tzPref),
        tone: !p.model ? "error" : "warn",
      });
    }
    if (latestMs === null && apMs !== null) latestMs = apMs;
    if (entries.length === 0) {
      pauses.push({ id: "agent|deployment", untilMs: apMs });
    }
    const apReset = formatLocalReset(
      latestMs !== null ? new Date(latestMs).toISOString() : ap.until,
      latestMs === null ? ap.secondsRemaining : undefined,
      nowMs,
      tzPref,
    );
    if (!primaryHeadline) {
      if (entries.length === 1) {
        const p = entries[0];
        primaryHeadline = !p.model
          ? `Model quota exceeded — ${pauseEntryLabel(p)} development paused`
          : `Model quota exceeded on ${pauseEntryLabel(p)} (fallback active)`;
        primarySubline =
          p.reason ||
          (!opts?.modelQuotaOnly ? ap.reason : undefined) ||
          "Usage limit pause active for agent runs";
      } else {
        primaryHeadline = wholeConnPaused
          ? `Model quota exceeded — development paused across ${Math.max(1, entries.length)} targets`
          : `Model quota exceeded on ${entries.length} models (fallback active)`;
        primarySubline =
          (opts?.modelQuotaOnly
            ? entries.find((p) => p.reason)?.reason
            : ap.reason) ||
          entries.find((p) => p.reason)?.reason ||
          "Usage limit pause active for agent runs";
      }
      primaryReset = apReset ? `resets ${apReset}` : "";
      shortLabel = wholeConnPaused ? "Quota paused" : "Model fallback";
    }
  }

  const warnings = (usage?.warnings ?? []).filter(Boolean);
  const pausedScopes = (usage?.scopes ?? []).filter((s) => {
    if (!s.pausedUntil) return false;
    const ms = parseResetMs(s.pausedUntil, undefined, nowMs);
    return ms === null || ms > nowMs;
  });
  if (warnings.length > 0 || pausedScopes.length > 0) {
    const terminalRe = /falls back to nothing|no fallback|all .* paused/i;
    const explicitExhausted = warnings.some((w) => terminalRe.test(w));
    const defaultModels = configuredDefaultModels(cfg);
    const allDefaultsPaused =
      defaultModels.length > 0 &&
      defaultModels.every((m) =>
        pausedScopes.some(
          (s) => !s.model || s.model.toLowerCase() === m.toLowerCase(),
        ),
      );
    const noFallbackLeft =
      explicitExhausted || (warnings.length === 0 && allDefaultsPaused);
    const tone: "error" | "warn" = noFallbackLeft ? "error" : "warn";
    bump(tone);
    if (noFallbackLeft) starved = true;
    // A spent weekly quota is said as one: it lasts days, not hours.
    const weeklySpent =
      pausedScopes.some(
        (s) => s.pauseHorizon === "weekly" || !!s.weeklyPausedUntil,
      ) || warnings.some((w) => /\bweekly limit\b/i.test(w));

    let maxScopeResetMs: number | null = null;
    for (const s of pausedScopes) {
      const ms = parseResetMs(s.pausedUntil, undefined, nowMs);
      if (ms !== null && (maxScopeResetMs === null || ms > maxScopeResetMs)) {
        maxScopeResetMs = ms;
      }
      pauses.push({
        id: [
          "usage",
          s.connection ?? s.connectionName ?? "",
          s.model ?? "",
        ].join("|"),
        untilMs: ms,
      });
      const label = [s.connectionName || s.connection, s.model]
        .filter(Boolean)
        .join(" · ");
      if (!details.some((d) => d.label === label)) {
        details.push({
          label: label || "Connection quota",
          reason:
            s.pauseHorizon === "weekly" || s.weeklyPausedUntil
              ? "Weekly limit reached"
              : "Token budget window limit reached",
          resetText: formatLocalReset(s.pausedUntil, undefined, nowMs, tzPref),
          tone: noFallbackLeft ? "error" : "warn",
        });
      }
    }
    for (const w of warnings) {
      // A warning's countdown changes every poll; its words do not.
      pauses.push({ id: `warning|${w.replace(/\d+/g, "#")}`, untilMs: null });
      const formatted = formatNoticeText(w, nowMs, tzPref);
      if (!details.some((d) => d.reason === formatted)) {
        details.push({
          label: "Usage governor",
          reason: formatted,
          resetText: "",
          tone: terminalRe.test(w) ? "error" : "warn",
        });
      }
    }

    const scopeResetText =
      maxScopeResetMs !== null
        ? `resets ${formatLocalReset(new Date(maxScopeResetMs).toISOString(), undefined, nowMs, tzPref)}`
        : "";
    if (!primaryHeadline || (noFallbackLeft && worstTone === "error")) {
      if (noFallbackLeft) {
        primaryHeadline = weeklySpent
          ? "Weekly model quota exhausted (no fallback models left) — development paused"
          : "Model quotas exceeded (no fallback models left) — development paused";
        shortLabel = "Quota paused";
      } else if (!primaryHeadline) {
        primaryHeadline = `${weeklySpent ? "Weekly model quota" : "Model quota"} reached (${Math.max(warnings.length, pausedScopes.length)} paused · fallback active)`;
        shortLabel = "Quota warning";
      }
      if (!primarySubline && warnings.length > 0) {
        const picked = noFallbackLeft
          ? (warnings.find((w) => terminalRe.test(w)) ?? warnings[0])
          : warnings[0];
        primarySubline = formatNoticeText(picked, nowMs, tzPref);
      }
      if (!primaryReset && scopeResetText) {
        primaryReset = scopeResetText;
      }
    }
  }

  if (!worstTone) return null;

  const key = [
    worstTone,
    primaryHeadline,
    primaryReset,
    String(details.length),
  ].join("|");

  return {
    key,
    tone: worstTone,
    headline: primaryHeadline,
    subline: primarySubline,
    resetText: primaryReset,
    shortLabel,
    details,
    hideable: !nonQuota && !starved && pauses.length > 0,
    pauses,
  };
}
