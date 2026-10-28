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
import { Box, Button, Stack, Typography } from "@mui/material";
import api from "../api.js";

// The proxy cache on the rail's own Caches page: what it is
// holding, and the buttons that throw it away (pkg/pkgcache,
// pkg/ui/packagecache.go, CachesPage.jsx).
//
// "Proxy cache" is the name every reader sees, since grain/task-328
// settled the two names this feature had; the file, the Go package and
// the routes it calls are still the package-cache ones, because those
// are an API and a state repository rather than prose.
//
// It sat under the Settings switch that turns the cache on until
// grain/task-266, on the argument that the two questions are asked
// together. They are not. Settings is a form you fill in and save, and
// what belongs on it is the configuration: whether runs use the cache at
// all, and how large it may grow. This panel is neither -- it is a live
// reading that changes without anybody saving anything, and a button that
// deletes files the moment it is pressed. Neither belongs on a form, and
// an operator who wants to know what is eating the volume should not have
// to open the deployment's configuration to find out.
//
// It then spent a while directly under the host's disk bars on System
// -> Sandbox health, on the argument that the bar for /var/lib/grain/data is
// red and this cache is one of the things filling it. What that never
// answered is the question asked the other way round -- "what has this
// deployment cached, and is the install I am watching a miss?" -- which
// is nobody's reason to open a pane about CPU pressure. So the figures
// and the buttons are a page of their own in the rail now
// (grain/task-321), and the disk bars carry a pointer to it.
//
// Either way the rule holds that took them off the form: "flush" beside
// "apt: 412 packages, 3.1 GiB" is a decision, and "flush" beside
// nothing is a guess. The page polls GET /api/package-cache for them,
// and hands them here; this panel reads what it is given and owns the
// flush, so there is one rendering of the cache's figures rather than
// one per pane that wants them.

// KIND_LABELS is what to call each ecosystem in prose. A kind with no
// entry falls back to its own name, so a UI running against a newer
// daemon that has learned to cache something else still lists it.
const KIND_LABELS = {
  apt: "apt packages",
  pypi: "Python packages",
  docker: "Docker Hub layers",
  npm: "npm packages",
  go: "Go modules",
  gobuild: "Go build artifacts",
  github: "GitHub repositories",
  playwright: "Playwright browsers",
};

function kindLabel(kind) {
  return KIND_LABELS[kind] || kind;
}

// formatBytes renders a cache size the way somebody sizing a disk says
// one: 1024-based and coarse, because the last three digits of a cache
// directory are nobody's business.
export function formatBytes(bytes) {
  if (bytes === null || bytes === undefined || Number.isNaN(bytes)) return "—";
  if (bytes >= 1 << 30) return `${(bytes / (1 << 30)).toFixed(1)} GiB`;
  if (bytes >= 1 << 20) return `${(bytes / (1 << 20)).toFixed(1)} MiB`;
  if (bytes >= 1 << 10) return `${Math.round(bytes / (1 << 10))} KiB`;
  return `${bytes} B`;
}

// cacheSummary is one ecosystem in one line.
function cacheSummary(stats) {
  const count = stats.entries || 0;
  let noun = "package";
  if (stats.kind === "docker") noun = "layer";
  else if (stats.kind === "gobuild") noun = "object";
  else if (stats.kind === "github") noun = "repository";
  else if (stats.kind === "playwright") noun = "browser";
  const pluralNoun = stats.kind === "github" ? "repositories" : `${noun}s`;
  return `${kindLabel(stats.kind)}: ${count} ${count === 1 ? noun : pluralNoun}, ${formatBytes(stats.bytes || 0)}`;
}

// PackageCachePanel lists what the cache holds and offers a flush per
// ecosystem plus one for all of it.
//
// A flush needs no confirmation and gets none: nothing is lost that a
// re-download does not put back, which is the whole difference between
// this and every other button that deletes something in this UI.
//
// `cache` is what the page's own GET /api/package-cache poll carried,
// and is absent on a deployment whose daemon runs no cache --
// which is said rather than hidden, since "the switch is on but there is
// no cache" is exactly the state somebody would otherwise stare at.
//
// A flush answers with the cache's state afterwards, so the panel redraws
// from that rather than waiting for the pane's next poll to come round: a
// button whose numbers do not move for five seconds reads as a button
// that did nothing. That answer stands only until the page hands down a
// newer reading: the next poll is at least as fresh as the flush, and a
// panel that kept drawing the flush's figures would stop moving for good.
export default function PackageCachePanel({ cache, showError, onFlushed }) {
  const [flushing, setFlushing] = useState("");
  // flushed is the flush's answer and the `cache` it was taken over, so
  // that a new reading from the page replaces it.
  const [flushed, setFlushed] = useState(null);
  const shown = flushed && flushed.over === cache ? flushed.after : cache;

  if (!shown) {
    return (
      <Typography variant="caption" color="text.secondary">
        This deployment is not running a proxy cache -- sandboxes download their
        packages, and their Docker Hub images, from the internet. The daemon's
        log says why it could not start one.
      </Typography>
    );
  }

  const flush = async (kind) => {
    setFlushing(kind || "all");
    try {
      const result = await api(
        `/api/package-cache/flush${kind ? `?kind=${encodeURIComponent(kind)}` : ""}`,
        { method: "POST" },
      );
      setFlushed({ over: cache, after: result.cache || [] });
      if (onFlushed) onFlushed(result.cache || []);
    } catch (err) {
      if (showError) showError(err);
    } finally {
      setFlushing("");
    }
  };

  const total = shown.reduce((sum, stats) => sum + (stats.bytes || 0), 0);

  return (
    <Box sx={{ mt: 1 }}>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        Total space consumed: {formatBytes(total)}
      </Typography>
      <Stack spacing={0.5}>
        {shown.map((stats) => (
          <Box
            key={stats.kind}
            sx={{
              display: "flex",
              alignItems: "center",
              justifyContent: "space-between",
              gap: 1,
              flexWrap: "wrap",
            }}
          >
            <Typography variant="caption" color="text.secondary">
              {cacheSummary(stats)}
            </Typography>
            {/* Named for what it flushes: six buttons all called
                "Flush" read as one word six times to a screen reader or
                a keyboard user tabbing down the list. */}
            <Button
              size="small"
              aria-label={`Flush ${kindLabel(stats.kind)}`}
              disabled={flushing !== "" || !stats.entries}
              onClick={() => flush(stats.kind)}
            >
              Flush
            </Button>
          </Box>
        ))}
      </Stack>
      <Button
        size="small"
        sx={{ mt: 0.5 }}
        disabled={flushing !== "" || total === 0}
        onClick={() => flush("")}
      >
        Flush the whole cache
      </Button>
    </Box>
  );
}
