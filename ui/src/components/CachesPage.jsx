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

import { useCallback, useEffect, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  LinearProgress,
  Stack,
  Typography,
} from "@mui/material";
import api from "../api.js";
import ItemGlyph from "./ItemGlyph.jsx";
import { ListHeader } from "./ListPrimitives.jsx";
import PackageCachePanel, { formatBytes } from "./PackageCachePanel.jsx";

// CachesPage combines the Proxy cache (pkg/pkgcache, grain/task-321)
// and the GitHub Actions cache (pkg/actionscache, grain/task-357) on a
// single page in the rail: what this deployment has downloaded on its
// sandboxes' behalf, what CI jobs have left behind for each other, and
// the controls that flush either or both.

// REFRESH_MS: the cadence the proxy cache, actions cache, and sandbox panes use.
const REFRESH_MS = 5000;

// repoLabel is a repository as the Actions cache names one.
function repoLabel(repo) {
  return repo || "(unnamed repository)";
}

// actionsCacheSummary is what the Actions cache holds, against the ceiling it
// is held under.
function actionsCacheSummary(state) {
  if (!state) return "";
  const entries = state.entries || 0;
  const bytes = state.bytes || 0;
  const size = state.maxBytes
    ? `${formatBytes(bytes)} of ${formatBytes(state.maxBytes)}`
    : `${formatBytes(bytes)}, no ceiling`;
  if (entries === 0) return `empty (${size})`;
  return `${entries.toLocaleString()} ${entries === 1 ? "entry" : "entries"}, ${size}`;
}

// fullness is how much of the bound is used, 0-100, for the bar under
// the Actions cache summary -- null where there is no bound to be a fraction of.
function fullness(state) {
  if (!state || !state.maxBytes) return null;
  return Math.min(100, Math.round(((state.bytes || 0) / state.maxBytes) * 100));
}

export default function CachesPage({ showError, standalone = false }) {
  const [packageCache, setPackageCache] = useState(null);
  const [actionsCache, setActionsCache] = useState(null);
  const [flushingActions, setFlushingActions] = useState("");
  // Why the last read failed, or "" once one succeeds. Without it a pane
  // whose first read failed spun "Loading caches" for as long as it was
  // open, and one whose reads started failing later went on showing the
  // last figures as if they were current.
  const [loadError, setLoadError] = useState("");
  // Whether the reads are failing right now, so a run of failures is
  // reported once rather than in a new snackbar every REFRESH_MS.
  const failing = useRef(false);

  const refresh = useCallback(async () => {
    try {
      const [pkg, act] = await Promise.all([
        api("/api/package-cache"),
        api("/api/actions-cache"),
      ]);
      setPackageCache(pkg);
      setActionsCache(act);
      setLoadError("");
      failing.current = false;
    } catch (err) {
      setLoadError(err?.message || String(err));
      if (!failing.current) showError(err);
      failing.current = true;
    }
  }, [showError]);

  useEffect(() => {
    let timer = null;
    let stopped = false;
    const tick = async () => {
      await refresh();
      if (stopped) return;
      timer = setTimeout(tick, REFRESH_MS);
    };
    tick();
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
    };
  }, [refresh]);

  const flushActions = async (repo) => {
    setFlushingActions(repo || "all");
    try {
      const result = await api(
        `/api/actions-cache/flush${repo ? `?repo=${encodeURIComponent(repo)}` : ""}`,
        { method: "POST" },
      );
      if (result.cache) setActionsCache(result.cache);
    } catch (err) {
      showError(err);
    } finally {
      setFlushingActions("");
    }
  };

  const loading = packageCache === null && actionsCache === null;
  const repos = actionsCache?.repos || [];
  const actionsUsed = fullness(actionsCache);

  const refreshButton = (
    <Button
      size="small"
      variant="outlined"
      sx={{ ml: "auto" }}
      onClick={refresh}
    >
      Refresh
    </Button>
  );
  const Root = standalone ? "main" : "section";

  return (
    <Root
      className={standalone ? "caches-panel standalone-page" : "caches-panel"}
    >
      {standalone ? (
        <ListHeader
          title="Caches"
          icon={<ItemGlyph kind="cache" size={20} />}
          action={refreshButton}
        />
      ) : (
        <Box
          sx={{
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            mb: 1,
          }}
        >
          <Typography variant="h6" component="h1">
            Caches
          </Typography>
          {refreshButton}
        </Box>
      )}
      {loading && !loadError && (
        <Box sx={{ display: "flex", justifyContent: "center", py: 3 }}>
          <CircularProgress size={28} aria-label="Loading caches" />
        </Box>
      )}
      {loading && loadError && (
        <Alert severity="error" sx={{ mb: 2 }}>
          The caches could not be read ({loadError}). This pane tries again
          every few seconds.
        </Alert>
      )}
      {!loading && loadError && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          The latest read failed ({loadError}), so the figures below are from
          the last one that succeeded. This pane tries again every few seconds.
        </Alert>
      )}

      <Box sx={{ maxWidth: "44rem" }}>
        {/* Proxy Cache Section */}
        <Box sx={{ mb: 4 }}>
          <Typography variant="h6" component="h2" sx={{ mt: 0, mb: 1 }}>
            Proxy cache
          </Typography>
          {packageCache !== null && !packageCache.enabled && (
            <Alert severity="info" sx={{ mb: 2 }}>
              This deployment is not running a proxy cache -- sandboxes download
              their packages, and their Docker Hub images, from the internet.
              The switch is on Settings &rarr; Caches; if it is already on, the
              daemon&apos;s log says why the cache could not start.
            </Alert>
          )}
          {packageCache !== null && packageCache.enabled && (
            <>
              <Typography variant="body2" color="text.secondary">
                What sandboxes have downloaded through this deployment rather
                than from the internet: apt and pip packages, npm packages, Go
                modules and Docker Hub layers, kept so the next run that wants
                one does not fetch it again -- and so a sandbox with no network
                of its own can install packages at all. Flushing frees the space
                and costs the next run that wants a package one download;
                nothing is lost that a re-download does not put back. Whether
                sandboxes use it at all, and how large it may grow, are on
                Settings &rarr; Caches.
              </Typography>
              <PackageCachePanel
                cache={packageCache.cache || []}
                showError={showError}
                onFlushed={(cache) =>
                  setPackageCache((prev) => ({ ...prev, cache }))
                }
              />
            </>
          )}
        </Box>

        {/* Actions Cache Section */}
        <Box sx={{ mb: 2 }}>
          <Typography variant="h6" component="h2" sx={{ mt: 0, mb: 1 }}>
            Actions cache
          </Typography>
          {actionsCache !== null && !actionsCache.enabled && (
            <Alert severity="info" sx={{ mb: 2 }}>
              This deployment keeps no Actions cache -- every CI job runs its
              own cache server inside its sandbox, and everything a{" "}
              <code>cache-from: type=gha</code> build exports dies with that
              sandbox. That is slow CI, not broken CI. The daemon&apos;s log
              says why it could not open one.
            </Alert>
          )}
          {actionsCache !== null && actionsCache.enabled && (
            <>
              <Typography variant="body2" color="text.secondary">
                What this deployment&apos;s CI jobs have left behind for each
                other: what a workflow writes with <code>actions/cache</code> or
                exports from a build with <code>cache-from: type=gha</code>,
                kept on the controller rather than in the sandbox that is
                destroyed when the job ends. Flushing frees the space and costs
                the next job that wanted an entry a cold build; nothing is lost
                that a rebuild does not put back, and a job reading an entry
                right now goes on reading it. Whether jobs use the cache at all,
                and how large it may grow, are on Settings &rarr; Caches.
              </Typography>
              <Alert severity="info" sx={{ mt: 2 }}>
                Entries belong to a repository, and every branch of that
                repository shares them: anything one branch&apos;s CI writes,
                another branch&apos;s CI can read back. A deployment that runs
                unreviewed branches on shared machines has granted that much
                already -- a branch&apos;s own workflow file is a file the
                branch can rewrite -- and one that has not should turn the cache
                off on Settings &rarr; Caches rather than reason about cache
                keys.
              </Alert>
              {!actionsCache.usedByJobs && (
                <Alert severity="warning" sx={{ mt: 2 }}>
                  No CI job is being handed this cache: the switch is off on
                  Settings &rarr; Caches. What is stored below stays where it is
                  until somebody flushes it, and every job builds from cold
                  meanwhile.
                </Alert>
              )}
              <Typography variant="body2" sx={{ mt: 2 }}>
                {actionsCacheSummary(actionsCache)}
              </Typography>
              {actionsUsed !== null && (
                <LinearProgress
                  variant="determinate"
                  value={actionsUsed}
                  aria-label="How full the Actions cache is"
                  sx={{ mt: 0.5, mb: 1, height: 6, borderRadius: 3 }}
                />
              )}
              <Stack spacing={0.5} sx={{ mt: 1 }}>
                {repos.length === 0 && (
                  <Typography variant="caption" color="text.secondary">
                    Nothing cached yet. The first CI job that exports a build
                    cache, or writes one with <code>actions/cache</code>, fills
                    this in.
                  </Typography>
                )}
                {repos.map((repo) => (
                  <Box
                    key={repo.repo}
                    sx={{
                      display: "flex",
                      alignItems: "center",
                      justifyContent: "space-between",
                      gap: 1,
                      flexWrap: "wrap",
                    }}
                  >
                    <Typography variant="caption" color="text.secondary">
                      {repoLabel(repo.repo)}: {formatBytes(repo.bytes || 0)}
                    </Typography>
                    <Button
                      size="small"
                      aria-label={`Flush the Actions cache of ${repoLabel(repo.repo)}`}
                      disabled={flushingActions !== ""}
                      onClick={() => flushActions(repo.repo)}
                    >
                      Flush
                    </Button>
                  </Box>
                ))}
              </Stack>
              <Button
                size="small"
                sx={{ mt: 0.5 }}
                disabled={
                  flushingActions !== "" || !(actionsCache.entries || 0)
                }
                onClick={() => flushActions("")}
              >
                Flush the whole cache
              </Button>
            </>
          )}
        </Box>
      </Box>
    </Root>
  );
}
