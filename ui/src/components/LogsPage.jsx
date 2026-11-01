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
import { PageHeaderActions, PageHeaderPageActions } from "./PageHeader.jsx";
import {
  Alert,
  Button,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";

// REFRESH_MS is how often this panel re-fetches the selected source's log
// lines while the System overlay is open -- the same order of magnitude
// as App.jsx's own POLL_INTERVAL_MS, so a log tailed here moves about as
// often as the task list does.
const REFRESH_MS = 5000;
const LINES_TO_FETCH = 500;

// PINNED_SLACK_PX mirrors AttemptTranscriptOverlay's own constant: how
// far from the bottom still counts as "at the bottom" when deciding
// whether to keep following the tail, given a browser's own rounding of
// fractional line heights.
const PINNED_SLACK_PX = 24;

// DEFAULT_SOURCE is the source this pane opens on when the daemon offers
// it. GET /api/logs lists its sources alphabetically, so the first of
// them is "config-sync" (pkg/cli/daemon.go's own ui.Config.Logs) --
// the rollout loop, which is nobody's first question. What anyone
// opening this pane wants is the daemon's own journal; the picker is
// still one click away for the rest.
const DEFAULT_SOURCE = "daemon";

// LogsPage is the Logs panel of SystemOverlay.jsx -- it used to be a full
// nav entry of its own (bwsalmon/agents#457), then a tab inside Settings
// alongside Sandbox health and the reboot control (bwsalmon/agents#623),
// before settling on a sidebar entry of its own, apart from
// Settings (bwsalmon/agents#640). GET /api/logs' own "enabled" flag says
// whether this deployment has any log sources configured at all; when it
// doesn't (`grain demo`'s throwaway UI, or any UI not colocated with a
// real daemon), this shows a note instead of a pane that could only ever
// 404, the same convention SecretsPanel/UpgradePanel already use for
// their own optional pieces.
export default function LogsPage({ showError }) {
  const [sources, setSources] = useState(null);
  const [source, setSource] = useState(null);
  const [lines, setLines] = useState([]);
  const [query, setQuery] = useState("");
  // Why GET /api/logs could not be read. Without it the pane stayed blank
  // under its tab once the snackbar saying so had gone.
  const [sourcesError, setSourcesError] = useState("");
  const view = useRef(null);
  // Whether the pane was scrolled to the bottom when the next lot of log
  // lines arrived. It starts true so the first render lands at the end,
  // and goes false the moment the reader scrolls up (below).
  const pinned = useRef(true);
  // Whether the reader was following the tail when they started typing a
  // search. Narrowing the text shrinks it under the scroll position, and
  // the scroll that shrinking causes would otherwise decide `pinned` for
  // them; clearing the search puts back what they were doing before it.
  const pinnedBeforeSearch = useRef(true);
  // The source on screen now. A fetch that was in flight when the reader
  // picked another source must not land its lines under the new name.
  const shownSource = useRef(source);
  shownSource.current = source;

  useEffect(() => {
    (async () => {
      try {
        const res = await api("/api/logs");
        setSources(res);
        if (res.enabled && res.sources.length > 0) {
          setSource(
            res.sources.includes(DEFAULT_SOURCE)
              ? DEFAULT_SOURCE
              : res.sources[0],
          );
        }
      } catch (err) {
        setSourcesError(err?.message || String(err));
        showError(err);
      }
    })();
  }, [showError]);

  const refresh = useCallback(async () => {
    if (!source) return;
    try {
      const res = await api(
        `/api/logs/${encodeURIComponent(source)}?lines=${LINES_TO_FETCH}`,
      );
      if (shownSource.current !== source) return;
      setLines(res.lines || []);
    } catch (err) {
      if (shownSource.current !== source) return;
      showError(err);
    }
  }, [source, showError]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  useEffect(() => {
    if (!source) return;
    const interval = setInterval(refresh, REFRESH_MS);
    return () => clearInterval(interval);
  }, [source, refresh]);

  // A newly picked source is read from its end too, whatever the reader
  // had scrolled the previous one to.
  useEffect(() => {
    pinned.current = true;
    setLines([]);
  }, [source]);

  // Open at the end, not at the top, exactly as AttemptTranscriptOverlay
  // does for a transcript (grain/task-22). GET /api/logs/{source} returns
  // the tail of a journal oldest-first, and what anyone reading a log
  // wants is its last few lines -- what the daemon has just done, or the
  // error it died on -- not the 500th line back. Scrolling to the end by
  // hand every time was the complaint; the top is still one flick away.
  //
  // The same effect keeps following the tail as the 5s poll above brings
  // in newer lines, but only while the reader has left the pane at the
  // bottom: once they scroll up to read something, the next poll must
  // not yank them back down.
  //
  // A search changes what the pane shows without changing `lines`, so it
  // is followed too: a reader on the tail who searches and then clears
  // the box was left at the oldest line -- the shrunken text had pulled
  // the scroll to the top -- and only thrown back to the newest one by
  // the next poll, up to five seconds later. A reader who had scrolled up
  // before searching is left where the browser puts them instead
  // (pinnedBeforeSearch above).
  useEffect(() => {
    const el = view.current;
    if (!el || !pinned.current) return;
    el.scrollTop = el.scrollHeight;
  }, [lines, query]);

  const onScroll = useCallback(() => {
    const el = view.current;
    if (!el) return;
    pinned.current =
      el.scrollHeight - el.scrollTop - el.clientHeight <= PINNED_SLACK_PX;
  }, []);

  if (!sources) {
    return sourcesError ? (
      <Alert severity="error">
        The logs could not be listed ({sourcesError}). Reopen this tab to try
        again.
      </Alert>
    ) : null;
  }

  const needle = query.trim().toLowerCase();
  const shownLines = needle
    ? lines.filter((line) => line.toLowerCase().includes(needle))
    : lines;

  return (
    <section className="logs-panel">
      <Typography variant="subtitle2" sx={{ mb: 1 }}>
        Logs
      </Typography>
      {!sources.enabled && (
        <Alert severity="info" sx={{ mb: 2 }}>
          Not available: this deployment has no log sources configured
          (bwsalmon/agents#444).
        </Alert>
      )}
      {sources.enabled && (
        <>
          {/* The source and the search are the page's filters, in its
              header rail (PageHeader.jsx, grain/task-4915). */}
          <PageHeaderActions>
            <div className="logs-toolbar">
              {/* A width the slot can take back, not a minimum: in a
                  narrow rail (an 820px tablet) a 220px floor ran the
                  select past the slot's clipped edge, its arrow cut
                  off, rather than leaving it whole beside "more". */}
              <FormControl
                size="small"
                sx={{ width: 220, maxWidth: "100%", flex: "0 1 auto" }}
              >
                <InputLabel id="logs-source-label">Source</InputLabel>
                <Select
                  labelId="logs-source-label"
                  label="Source"
                  value={source || ""}
                  onChange={(e) => setSource(e.target.value)}
                >
                  {sources.sources.map((s) => (
                    <MenuItem key={s} value={s}>
                      {s}
                    </MenuItem>
                  ))}
                </Select>
              </FormControl>
              <TextField
                label="Search logs"
                size="small"
                sx={{ flex: "1 1 12rem" }}
                value={query}
                onChange={(event) => {
                  const next = event.target.value;
                  if (!query.trim() && next.trim()) {
                    pinnedBeforeSearch.current = pinned.current;
                  } else if (query.trim() && !next.trim()) {
                    pinned.current = pinnedBeforeSearch.current;
                  }
                  setQuery(next);
                }}
              />
              <PageHeaderPageActions>
                <Button size="small" variant="outlined" onClick={refresh}>
                  Refresh
                </Button>
              </PageHeaderPageActions>
            </div>
          </PageHeaderActions>
          {/* Focusable and named so the keyboard can scroll it: a log
              line is as long as it was written, and without a tab stop
              the overflow past the right edge was out of reach of
              anyone not holding a pointer (axe, 2026-10-26). */}
          <pre
            className="logs-view"
            ref={view}
            onScroll={onScroll}
            tabIndex={0}
            role="region"
            aria-label="Log lines"
          >
            {shownLines.length > 0
              ? shownLines.join("\n")
              : lines.length > 0
                ? "(no matching log lines)"
                : "(no log lines)"}
          </pre>
        </>
      )}
    </section>
  );
}
