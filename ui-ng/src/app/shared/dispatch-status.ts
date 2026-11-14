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

export interface DispatchStatusBanner {
  key: string;
  tone: "error" | "warn";
  headline: string;
  subline: string;
  resetText: string;
  shortLabel: string;
  details: DispatchDetailItem[];
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

// Formats a UTC "HH:MM UTC" phrase inside a backend warning string into the
// user's local timezone alongside a relative reset countdown.
export function formatNoticeText(
  text: string,
  nowMs = Date.now(),
  preferredTz?: string | null,
): string {
  return text.replace(/\b(\d{2}):(\d{2})\s*UTC\b/g, (raw, hh, mm) => {
    const cur = new Date(nowMs);
    let target = Date.UTC(
      cur.getUTCFullYear(),
      cur.getUTCMonth(),
      cur.getUTCDate(),
      Number(hh),
      Number(mm),
      0,
      0,
    );
    if (target < nowMs - 60_000) {
      target += 86_400_000;
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

  const bump = (tone: "error" | "warn") => {
    if (worstTone !== "error") worstTone = tone;
  };

  if (!opts?.modelQuotaOnly && cfg?.reconcilerDown) {
    bump("error");
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
    const ms = parseResetMs(p.until, p.secondsRemaining, nowMs);
    return ms === null || ms > nowMs;
  });
  const apMs = ap ? parseResetMs(ap.until, ap.secondsRemaining, nowMs) : null;
  const apActive =
    !!ap?.paused &&
    (rawEntries.length > 0
      ? entries.length > 0
      : apMs === null || apMs > nowMs);
  if (ap && apActive) {
    const wholeConnPaused =
      entries.length === 0 || entries.some((p) => !p.model);
    const tone: "error" | "warn" = wholeConnPaused ? "error" : "warn";
    bump(tone);
    let latestMs: number | null = null;
    for (const p of entries) {
      const ms = parseResetMs(p.until, p.secondsRemaining, nowMs);
      if (ms !== null && (latestMs === null || ms > latestMs)) latestMs = ms;
      details.push({
        label: pauseEntryLabel(p),
        reason:
          p.reason || p.escalated || "Model quota or credential limit reached",
        resetText: formatLocalReset(p.until, p.secondsRemaining, nowMs, tzPref),
        tone: !p.model ? "error" : "warn",
      });
    }
    if (apMs !== null && (latestMs === null || apMs > latestMs))
      latestMs = apMs;
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
          p.reason || ap.reason || "Usage limit pause active for agent runs";
      } else {
        primaryHeadline = wholeConnPaused
          ? `Model quota exceeded — development paused across ${Math.max(1, entries.length)} targets`
          : `Model quota exceeded on ${entries.length} models (fallback active)`;
        primarySubline = ap.reason || "Usage limit pause active for agent runs";
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

    let maxScopeResetMs: number | null = null;
    for (const s of pausedScopes) {
      const ms = parseResetMs(s.pausedUntil, undefined, nowMs);
      if (ms !== null && (maxScopeResetMs === null || ms > maxScopeResetMs)) {
        maxScopeResetMs = ms;
      }
      const label = [s.connectionName || s.connection, s.model]
        .filter(Boolean)
        .join(" · ");
      if (!details.some((d) => d.label === label)) {
        details.push({
          label: label || "Connection quota",
          reason: "Token budget window limit reached",
          resetText: formatLocalReset(s.pausedUntil, undefined, nowMs, tzPref),
          tone: noFallbackLeft ? "error" : "warn",
        });
      }
    }
    for (const w of warnings) {
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
        primaryHeadline =
          "Model quotas exceeded (no fallback models left) — development paused";
        shortLabel = "Quota paused";
      } else if (!primaryHeadline) {
        primaryHeadline = `Model quota reached (${Math.max(warnings.length, pausedScopes.length)} paused · fallback active)`;
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
  };
}
