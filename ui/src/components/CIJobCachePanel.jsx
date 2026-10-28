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

import { useState } from "react";
import { Box, Button, Typography } from "@mui/material";
import api from "../api.js";
import { formatBytes } from "./PackageCachePanel.jsx";
import { oldestAge } from "./CIHistoryPanel.jsx";

// What grain CI's job cache occupies, in the System pane's Storage
// section (pkg/ui/cistorage.go, grain/task-276).
//
// It is the third thing under that heading and the second with a button,
// and the button is deliberately not the proxy cache's. Flushing
// packages costs the next run one download; emptying this costs every job
// of the next few runs a whole re-run, which on this deployment is tens
// of sandbox-minutes and, under load, jobs queued behind a memory budget
// waiting for a lane. Nothing is *lost* either way -- a cache entry is a
// promise that identical work was already done, never the only copy of
// anything (ci.InspectableCache) -- so this is an expensive action rather
// than a destructive one, and it asks first for exactly that reason.
//
// The CI pane has a panel over this same directory and its own clear
// button (CIPage, grain/task-304), and this is deliberately not a link
// to it. That one answers "is the cache working" for somebody reading
// runs; this one answers "what is on my volume and what may I clear" for
// somebody reading disk bars, which is a question they are already
// halfway through when they get here. Both press the same DELETE and
// both ask the same thing first.
//
// The hit counter is shown beside the size because it is what that
// question turns on: 200 MB is worth clearing off a full volume if
// nothing is hitting it, and is the cheapest 200 MB on the disk if four
// jobs in five are being answered out of it. The counters run from this
// daemon's start, so a deployment that restarted an hour ago shows a full
// cache and small numbers.

// formatAge renders one of the bounds' own durations in the words
// somebody sizing a cache uses. It takes seconds because that is what the
// API sends: a time.Duration is nanoseconds in JSON and a pane that has
// to know to divide by a billion is a pane that will one day forget
// (ui.CIJobCacheStorage.MaxAgeSeconds).
export function formatAge(seconds) {
  if (!seconds) return "";
  const days = Math.round(seconds / 86400);
  if (days >= 1) return `${days} ${days === 1 ? "day" : "days"}`;
  const hours = Math.round(seconds / 3600);
  if (hours >= 1) return `${hours} ${hours === 1 ? "hour" : "hours"}`;
  const minutes = Math.max(Math.round(seconds / 60), 1);
  return `${minutes} ${minutes === 1 ? "minute" : "minutes"}`;
}

// jobCacheSummary is what it holds, against the ceiling it is held under.
function jobCacheSummary(cache, now = Date.now()) {
  if (!cache) return "";
  const bytes = cache.bytes || 0;
  const entries = cache.entries || 0;
  const parts = [
    cache.maxBytes
      ? `${formatBytes(bytes)} of ${formatBytes(cache.maxBytes)}`
      : `${formatBytes(bytes)}, no ceiling`,
  ];
  if (entries === 0) {
    // An empty cache is a reading, not a gap: a daemon that has just
    // swept, and one whose CI has never run a job, both land here.
    parts.push("no job result is cached");
    return parts.join(" · ");
  }
  parts.push(
    cache.maxEntries
      ? `${entries} of ${cache.maxEntries} job results`
      : `${entries} job ${entries === 1 ? "result" : "results"}`,
  );
  const age = oldestAge(cache.oldestAt, now);
  if (age) parts.push(`the oldest of them ${age} old`);
  return parts.join(" · ");
}

// jobCacheValue is the other bound -- how long an unasked-for entry is
// kept -- and what emptying this would cost, said as the work it has
// saved since this daemon started.
//
// It ends by saying where the ceiling comes from, because this panel is
// where somebody decides to move it: the three bounds were daemon flags
// until grain/task-317, so "200 MiB of 256 MiB" used to be a number an
// operator could read here and only change by editing a systemd unit.
function jobCacheValue(cache) {
  if (!cache) return "";
  const parts = [];
  const age = formatAge(cache.maxAgeSeconds);
  if (age) {
    parts.push(
      `Kept: an entry nothing asks for goes after ${age}, and the oldest go first when the ceiling bites`,
    );
  }
  const hits = cache.hits || 0;
  const misses = cache.misses || 0;
  const looks = hits + misses;
  if (looks === 0) {
    parts.push(
      "nothing has looked in it since this daemon started, so there is no hit rate to read yet",
    );
  } else {
    const rate = Math.round((hits / looks) * 100);
    parts.push(
      `it has answered ${hits} of ${looks} job lookups (${rate}%) since this daemon started, which is what emptying it costs as re-runs`,
    );
  }
  parts.push(
    "the ceiling, the entry count and the age are editable on Settings → CI, where a change binds the next sweep rather than the next restart",
  );
  const line = parts.join(". ");
  return `${line.charAt(0).toUpperCase()}${line.slice(1)}.`;
}

// CIJobCachePanel is those two lines and the button, and a sentence
// instead of all three on a deployment running every job every time.
//
// `cache` is the ciJobCache the pane's own GET /api/sandboxes poll
// carried. Clearing answers with the cache's state afterwards, which the
// panel redraws from rather than waiting for the next poll -- a button
// whose numbers sit still for five seconds reads as one that did nothing
// (PackageCachePanel makes the same argument about its own).
export default function CIJobCachePanel({ cache, showError, onCleared }) {
  const [clearing, setClearing] = useState(false);
  // cleared is the clear's answer and the `cache` it was taken over, so
  // that a new reading from the page replaces it.
  const [cleared, setCleared] = useState(null);
  const shown = cleared && cleared.over === cache ? cleared.after : cache;

  if (!shown) {
    return (
      <Typography variant="caption" color="text.secondary">
        This deployment&apos;s grain CI keeps no job cache -- every job runs,
        every time. The daemon&apos;s log says why it could not open one.
      </Typography>
    );
  }

  const clear = async () => {
    if (
      !window.confirm(
        "Empty the CI job cache? Nothing is lost -- an entry is only a record that identical work was already done -- but every job of the next few runs does itself over, which is sandbox time this deployment then has to find.",
      )
    )
      return;
    setClearing(true);
    try {
      const result = await api("/api/ci/cache", { method: "DELETE" });
      // DELETE answers in the CI pane's own shape (ui.CICacheStats),
      // which is this one under a different name for the oldest entry.
      // Mapped here rather than at either end: the two endpoints are
      // answering different questions about the same directory, and the
      // one field they spell differently is not worth a third type.
      const after = result.cache
        ? { ...result.cache, oldestAt: result.cache.oldest }
        : { ...shown, bytes: 0, entries: 0, oldestAt: null };
      setCleared({ over: cache, after });
      if (onCleared) onCleared(after);
    } catch (err) {
      if (showError) showError(err);
    } finally {
      setClearing(false);
    }
  };

  return (
    <Box sx={{ mt: 1 }}>
      <Typography variant="body2" color="text.secondary">
        {jobCacheSummary(shown)}
      </Typography>
      <Typography variant="caption" color="text.secondary" component="div">
        {jobCacheValue(shown)}
      </Typography>
      <Button
        size="small"
        sx={{ mt: 0.5 }}
        disabled={clearing || !shown.entries}
        onClick={clear}
      >
        Empty the job cache
      </Button>
    </Box>
  );
}
