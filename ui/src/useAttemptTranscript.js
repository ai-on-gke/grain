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
import api from "./api.js";

// REFRESH_MS mirrors LogsOverlay's own polling interval (bwsalmon/
// agents#444). For a finished attempt this is just re-fetching the same
// Store.RunTranscript row RunDispatch wrote once, after framework.Run
// returned (pkg/orchestrator/run.go) -- but while an attempt is still
// running, the backend serves whatever its framework has mirrored to a
// live transcript file so far (ui.Config.LiveTranscripts,
// bwsalmon/agents#467), so each poll here genuinely picks up new content
// rather than re-reading an empty row until the attempt finishes. Leaving
// this open on a long-running attempt is what "checking up on a
// long-running agent" (bwsalmon/agents#446) means in practice.
const REFRESH_MS = 5000;

// How far from the bottom still counts as "at the bottom" when deciding
// whether to keep following the tail: fractional line heights and a
// browser's own rounding leave scrollTop a pixel or two short of the
// arithmetic bottom even when the pane is scrolled all the way down.
const PINNED_SLACK_PX = 24;

// useAttemptTranscript fetches one attempt's agent transcript, polls it
// while the attempt is still running, and keeps the pane it is shown in
// pinned to the bottom for as long as the reader leaves it there. It is
// shared by AttemptTranscriptOverlay (the full-screen view a timeline
// attempt opens) and the detail pane's LiveAttemptTranscript, which were
// once two copies of this same logic.
//
// It returns the transcript (null until the first fetch lands), the ref
// to put on the scrolling element, and that element's onScroll handler.
export function useAttemptTranscript(taskId, attempt, showError) {
  const [transcript, setTranscript] = useState(null);
  const view = useRef(null);
  // Whether the pane was scrolled to the bottom when the next lot of
  // transcript arrived. It starts true so the first render lands at the
  // end, and goes false the moment the reader scrolls up (below).
  const pinned = useRef(true);

  const refresh = useCallback(async () => {
    try {
      const res = await api(
        `/api/tasks/${encodeURIComponent(taskId)}/attempts/${attempt.number}/transcript`,
      );
      setTranscript(res?.transcript || "");
    } catch (err) {
      if (showError) showError(err);
    }
  }, [taskId, attempt.number, showError]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  useEffect(() => {
    if (attempt.finishedAt) return;
    const interval = setInterval(refresh, REFRESH_MS);
    return () => clearInterval(interval);
  }, [attempt.finishedAt, refresh]);

  // Open at the end, not at the top (grain/task-22). A transcript is
  // written oldest-first and can run to thousands of lines, and what
  // anyone opening one wants to see is the last thing the agent did --
  // why the attempt failed, or where a still-running one has got to --
  // not the prompt it started from an hour ago. Scrolling to the end by
  // hand every time was the complaint; the top is still one flick away.
  //
  // The same effect keeps following the tail as the 5s poll above
  // brings in more of a running attempt's transcript, but only while
  // the reader has left the pane at the bottom: once they scroll up to
  // read something, the next poll must not yank them back down.
  useEffect(() => {
    const el = view.current;
    if (!el || transcript === null || !pinned.current) return;
    el.scrollTop = el.scrollHeight;
  }, [transcript]);

  const onScroll = useCallback(() => {
    const el = view.current;
    if (!el) return;
    pinned.current =
      el.scrollHeight - el.scrollTop - el.clientHeight <= PINNED_SLACK_PX;
  }, []);

  return { transcript, view, onScroll };
}
