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

import { Fragment, useEffect, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  Link,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import api from "../api.js";
import { buildPath } from "../paths.js";
import { lastRestartReason, stagingLink } from "../staging.js";

const PERSISTENT_SANDBOXES_DOC =
  "https://github.com/ai-on-gke/grain/blob/main/docs/persistent-sandboxes.md";

// persistentStateColor is the chip colour for a persistent sandbox's state
// (orchestrator.PersistentStarting and friends).
export function persistentStateColor(state) {
  switch (state) {
    case "running":
      return "success";
    case "starting":
      return "info";
    case "restarting":
      return "warning";
    default:
      return "default";
  }
}

// persistentUptime is how long the current command has been up, in the
// largest unit worth reading, or "" when it is not running.
export function persistentUptime(sandbox, now = Date.now()) {
  if (sandbox.state !== "running" || !sandbox.startedAt) return "";
  const s = Math.max(0, Math.floor((now - new Date(sandbox.startedAt)) / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400)
    return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}

// ReachedAt is where one persistent sandbox is reached: its sub-path or
// host-port link (stagingLink), the host port beside a sub-path, and the
// proxy warning when the link will probably not load from here.
function ReachedAt({ sandbox, config }) {
  const link = stagingLink(sandbox, config);
  if (!link.href) return "—";
  return (
    <>
      <Link
        href={link.href}
        target="_blank"
        rel="noopener noreferrer"
        title={link.warning || link.href}
      >
        {link.label}
      </Link>
      {sandbox.uiSubpath && sandbox.hostPort ? (
        <Typography variant="caption" component="div">
          host port {sandbox.hostPort}
        </Typography>
      ) : null}
      {link.warning ? (
        <Typography
          variant="caption"
          color="warning.main"
          component="div"
          title={link.warning}
          data-testid="persistent-sandbox-proxy-warning"
          sx={{ whiteSpace: "normal", maxWidth: "16rem" }}
        >
          Probably not reachable through this proxy
        </Typography>
      ) : null}
    </>
  );
}

// persistentCommitLine is the commit a sandbox is running and its refresh
// policy (model.PersistentSandbox.Refresh), as one caption: "running
// 1a2b3c4d5e6f · after_green". "" when there is neither to say.
export function persistentCommitLine(sandbox) {
  const parts = [];
  if (sandbox.commit) parts.push(`running ${sandbox.commit.slice(0, 12)}`);
  if (sandbox.refresh && sandbox.refresh !== "manual")
    parts.push(sandbox.refresh);
  return parts.join(" · ");
}

// persistentRefreshLine is why the sandbox last moved itself to another
// commit -- "refreshed 2h ago: candidate moved; CI green at 1a2b3c4d" --
// or "" when it never has.
export function persistentRefreshLine(sandbox, now = Date.now()) {
  const r = sandbox.lastRefresh;
  if (!r || !r.reason) return "";
  const s = Math.max(0, Math.floor((now - new Date(r.at)) / 1000));
  let ago = `${s}s`;
  if (s >= 86400) ago = `${Math.floor(s / 86400)}d`;
  else if (s >= 3600) ago = `${Math.floor(s / 3600)}h`;
  else if (s >= 60) ago = `${Math.floor(s / 60)}m`;
  return `refreshed ${ago} ago: ${r.reason}`;
}

// PersistentSandboxesPanel is the persistent sandboxes -- staging
// environments run from a repository checkout (model.PersistentSandbox)
// -- the daemon keeps running: their state, where they are reached, and
// the operator's Restart, Stop/Start and Rebuild controls, with the tail
// of one's output on demand. The list is the page's own GET
// /api/sandboxes (persistentSandboxes), so it refreshes with the page;
// onChange asks the page to re-read after a control. On a deployment
// defining none, it explains what they are and points to the director
// chat and tables/persistent_sandbox.json.
//
// Where each is reached is staging.js's stagingLink, the same link the
// rail's Staging entries open: a sub-path root-relative, a host port on
// the host this page came from -- with a warning, and no pretence it
// will load, when this page came through a proxy that will not carry
// it. config is GET /api/config's, which says whether it did; the panel
// reads it itself when it is not handed one and has a host port to link.
export default function PersistentSandboxesPanel({
  sandboxes,
  onChange,
  onOpenSession,
  config: givenConfig,
}) {
  const list = Array.isArray(sandboxes) ? sandboxes : [];
  const [error, setError] = useState("");
  const [fetchedConfig, setFetchedConfig] = useState(null);
  const config = givenConfig || fetchedConfig;
  const needsConfig =
    !givenConfig && list.some((s) => s.hostPort && !s.uiSubpath);
  useEffect(() => {
    if (!needsConfig || fetchedConfig) return undefined;
    let live = true;
    api("/api/config")
      .then((cfg) => {
        if (live) setFetchedConfig(cfg || {});
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [needsConfig, fetchedConfig]);
  const [logs, setLogs] = useState({ name: "", text: "" });

  useEffect(() => {
    if (!logs.name) return undefined;
    let live = true;
    const fetchLogs = async () => {
      try {
        const res = await api(
          `/api/persistent-sandboxes/${encodeURIComponent(logs.name)}/logs`,
        );
        if (live)
          setLogs((l) => (l.name === logs.name ? { ...l, text: res.logs } : l));
      } catch (err) {
        if (live)
          setLogs((l) =>
            l.name === logs.name ? { ...l, text: `(${err.message})` } : l,
          );
      }
    };
    fetchLogs();
    const t = setInterval(fetchLogs, 3000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [logs.name]);

  const act = async (name, action) => {
    try {
      await api(
        `/api/persistent-sandboxes/${encodeURIComponent(name)}/${action}`,
        { method: "POST" },
      );
      setError("");
    } catch (err) {
      setError(err.message);
    }
    if (onChange) onChange();
  };

  const directorHref = buildPath({ view: "session", sessionId: "director" });

  return (
    <Box
      id="persistent-sandboxes"
      sx={{ mb: "0.75rem" }}
      data-testid="persistent-sandboxes"
    >
      <Typography variant="body2" color="text.secondary" sx={{ mb: "0.25rem" }}>
        Persistent sandboxes
      </Typography>
      {error && (
        <Alert severity="error" sx={{ mb: 1 }}>
          {error}
        </Alert>
      )}
      {list.length === 0 ? (
        <>
          <Typography variant="body2" color="text.secondary">
            Persistent sandboxes keep a long-lived service running — a staging
            environment of one of your repos at a branch, reachable on a host
            port or under <code>/staging/&lt;name&gt;/</code>. To add one, ask
            the{" "}
            <Link
              href={directorHref}
              onClick={(evt) => {
                if (
                  !onOpenSession ||
                  evt.button !== 0 ||
                  evt.metaKey ||
                  evt.ctrlKey ||
                  evt.shiftKey ||
                  evt.altKey
                )
                  return;
                evt.preventDefault();
                onOpenSession("director");
              }}
            >
              director
            </Link>{" "}
            to set one up — e.g.{" "}
            <em>
              &apos;set up a staging environment for bwsalmon/grain on
              main&apos;
            </em>
            .
          </Typography>
          <Typography variant="caption" color="text.secondary" component="div">
            Or define it in the config repo&apos;s{" "}
            <code>tables/persistent_sandbox.json</code> (
            <Link
              href={PERSISTENT_SANDBOXES_DOC}
              target="_blank"
              rel="noopener noreferrer"
            >
              docs/persistent-sandboxes.md
            </Link>
            ).
          </Typography>
        </>
      ) : (
        <>
          <TableContainer>
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell>Name</TableCell>
                  <TableCell>Repository</TableCell>
                  <TableCell>Command</TableCell>
                  <TableCell>Status</TableCell>
                  <TableCell>Uptime</TableCell>
                  <TableCell>Restarts</TableCell>
                  <TableCell>Reached at</TableCell>
                  <TableCell />
                </TableRow>
              </TableHead>
              <TableBody>
                {list.map((s) => (
                  <Fragment key={s.name}>
                    <TableRow
                      sx={
                        s.packageCache
                          ? { "& > td": { borderBottom: 0 } }
                          : undefined
                      }
                    >
                      <TableCell>
                        {s.name}
                        <Typography
                          variant="caption"
                          color="text.secondary"
                          sx={{ display: "block", fontFamily: "monospace" }}
                        >
                          {s.sandbox}
                        </Typography>
                      </TableCell>
                      <TableCell
                        sx={{ fontFamily: "monospace", fontSize: "0.8rem" }}
                      >
                        {s.repo}
                        {s.ref ? `@${s.ref}` : ""}
                        {persistentCommitLine(s) && (
                          <Typography
                            variant="caption"
                            color="text.secondary"
                            component="div"
                            data-testid="persistent-commit"
                            sx={{ fontFamily: "monospace" }}
                          >
                            {persistentCommitLine(s)}
                          </Typography>
                        )}
                        {persistentRefreshLine(s) && (
                          <Typography
                            variant="caption"
                            color="text.secondary"
                            component="div"
                            data-testid="persistent-refresh"
                            sx={{
                              fontFamily: "inherit",
                              overflowWrap: "anywhere",
                            }}
                          >
                            {persistentRefreshLine(s)}
                          </Typography>
                        )}
                      </TableCell>
                      <TableCell
                        sx={{ fontFamily: "monospace", fontSize: "0.8rem" }}
                      >
                        {s.command}
                      </TableCell>
                      <TableCell>
                        <Chip
                          size="small"
                          color={persistentStateColor(s.state)}
                          label={s.state}
                        />
                        {s.lastError && (
                          <Typography
                            variant="caption"
                            color="error"
                            component="div"
                            sx={{ mt: 0.5, overflowWrap: "anywhere" }}
                          >
                            {s.lastError}
                          </Typography>
                        )}
                        {s.restarts > 0 && lastRestartReason(s) && (
                          <Typography
                            variant="caption"
                            color="text.secondary"
                            component="div"
                            data-testid="persistent-sandbox-restart-reason"
                            sx={{ mt: 0.5, overflowWrap: "anywhere" }}
                          >
                            Last restart: {lastRestartReason(s)}
                          </Typography>
                        )}
                      </TableCell>
                      <TableCell sx={{ whiteSpace: "nowrap" }}>
                        {persistentUptime(s) || "—"}
                      </TableCell>
                      <TableCell>{s.restarts}</TableCell>
                      <TableCell sx={{ whiteSpace: "nowrap" }}>
                        <ReachedAt sandbox={s} config={config} />
                      </TableCell>
                      <TableCell sx={{ whiteSpace: "nowrap" }}>
                        {s.state === "stopped" ? (
                          <Button
                            size="small"
                            onClick={() => act(s.name, "start")}
                          >
                            Start
                          </Button>
                        ) : (
                          <>
                            <Button
                              size="small"
                              onClick={() => act(s.name, "restart")}
                            >
                              Restart
                            </Button>
                            <Button
                              size="small"
                              onClick={() => act(s.name, "stop")}
                            >
                              Stop
                            </Button>
                            <Button
                              size="small"
                              onClick={() => act(s.name, "rebuild")}
                            >
                              Rebuild
                            </Button>
                          </>
                        )}
                        <Button
                          size="small"
                          onClick={() =>
                            setLogs((l) =>
                              l.name === s.name
                                ? { name: "", text: "" }
                                : { name: s.name, text: "" },
                            )
                          }
                        >
                          {logs.name === s.name ? "Hide logs" : "Logs"}
                        </Button>
                        <Link
                          href={`/api/persistent-sandboxes/${encodeURIComponent(s.name)}/logs?format=text`}
                          target="_blank"
                          rel="noopener"
                          variant="body2"
                          title={`${s.name}'s output, in a tab of its own`}
                          sx={{ ml: 0.5 }}
                        >
                          Raw
                        </Link>
                      </TableCell>
                    </TableRow>
                    {/* Whether this sandbox fetches through the deployment's
                    package cache (or why not), across the whole row: it is
                    a sentence, and no one column is wide enough for it. */}
                    {s.packageCache && (
                      <TableRow>
                        <TableCell colSpan={8} sx={{ pt: 0 }}>
                          <Typography
                            variant="caption"
                            color="text.secondary"
                            component="div"
                            data-testid="persistent-package-cache"
                            sx={{
                              overflowWrap: "anywhere",
                              // The table scrolls sideways on a phone; the
                              // sentence stays where it can be read.
                              position: "sticky",
                              left: 16,
                              maxWidth: {
                                xs: "calc(100vw - 64px)",
                                md: "none",
                              },
                            }}
                          >
                            Package cache: {s.packageCache}
                          </Typography>
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
          {logs.name && (
            <Box
              component="pre"
              data-testid="persistent-sandbox-logs"
              sx={{
                mt: 1,
                p: 1,
                maxHeight: "20rem",
                overflow: "auto",
                fontSize: "0.75rem",
                bgcolor: "action.hover",
                whiteSpace: "pre-wrap",
              }}
            >
              {logs.text || "(no output yet)"}
            </Box>
          )}
        </>
      )}
    </Box>
  );
}
