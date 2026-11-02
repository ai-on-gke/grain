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

import { useEffect, useRef, useState } from "react";
import { Box, Button, Paper, Select, Stack, Typography } from "@mui/material";
import api from "../api.js";
import { configuredCapabilities } from "../state.js";
import BulkEditOverlay from "./BulkEditOverlay.jsx";

// BatchActionsBar is DetailOverlay's Actions and CapabilityToggles, but
// aimed at every selected task at once instead of the one the overlay
// has open -- the same handful of idempotent endpoints (approve,
// submit, retry merge, defer, undefer, close, reopen, capabilities), most of which
// already no-op on a task they do not apply to (Client.Approve on an
// already-approved task, Client.SetCapability detaching a capability
// that is not attached, ...), so firing one at every selection member is
// safe even when the selection mixes states.
//
// Defer is the one that refuses rather than no-ops -- a task that has
// already run cannot be put aside (Client.Defer) -- and it is offered
// here anyway, because putting a stack of proposals aside in one go is
// the thing deferring is most often for. A mixed selection defers what
// it can and says how many it could not: App's actBatch collects
// failures rather than aborting the rest, and leaves the selection
// standing so whoever ran it can see what is left.
//
// The bar takes the selected tasks themselves rather than a count of
// them, because one of its buttons is about what they currently hold:
// "Bulk edit" opens a form pre-filled with the fields the selection
// already agrees on (BulkEditOverlay). The count on the left is
// selection.length, so the two can never disagree.
//
// A selection outlives the view it was made in: ticking rows under
// Queued and then opening Running leaves them selected, and the bar's
// buttons still act on them. That is deliberate -- it is how a stack of
// proposals and a stack of queued tasks get closed in one go -- but a
// bar that only said "7 selected" over a list showing none of them
// ticked offered to close seven tasks nobody could see. shownIds is the
// set of ids the list under the bar is drawing (TaskList's
// onShownChange); given it, the bar says how many of the selection are
// not among them, and so does Close's confirmation. Left out -- a board,
// whose cards are drawn elsewhere -- it says nothing extra.
export default function BatchActionsBar({
  selection,
  shownIds,
  tasks,
  config,
  templates,
  onRun,
  onClear,
}) {
  const [capabilityId, setCapabilityId] = useState("");
  const [editing, setEditing] = useState(false);
  const count = selection.length;
  const hidden = shownIds
    ? selection.filter((t) => !shownIds.has(t.id)).length
    : 0;
  const barRef = useRef(null);
  useBarHeight(barRef, count);

  if (count === 0) return null;
  const run = (mutate) =>
    onRun((id) => api(`/api/tasks/${id}${mutate}`, { method: "POST" }));

  return (
    <Paper
      ref={barRef}
      elevation={4}
      square
      sx={{
        position: "fixed",
        left: 0,
        right: 0,
        bottom: 0,
        zIndex: 4,
        // Clear of the home indicator on a phone: index.html asks for
        // viewport-fit=cover, so a bar pinned to the bottom edge is
        // partly under it without this. Zero everywhere else.
        pb: "env(safe-area-inset-bottom, 0px)",
      }}
    >
      <Stack
        direction="row"
        alignItems="center"
        spacing={1.2}
        flexWrap="wrap"
        sx={{ px: { xs: 1.5, sm: 3 }, py: 1.3 }}
      >
        <Typography fontWeight={500}>{count} selected</Typography>
        {hidden > 0 && (
          <Typography variant="body2" color="warning.main">
            {hidden === count
              ? "none of them shown here"
              : `${hidden} not shown here`}
          </Typography>
        )}
        <>
          <Button
            variant="outlined"
            size="small"
            onClick={() =>
              onRun((id) => api(`/api/tasks/${id}/approve`, { method: "POST" }))
            }
          >
            Approve
          </Button>
          {/* Run now for each of them (ui.Client.RunNow: approving and
              undeferring where needed), which is how a paused deployment
              -- manual mode -- is told exactly what to start. Requests
              start oldest first as capacity allows; a running or closed
              task refuses and is counted. */}
          <Button
            variant="outlined"
            size="small"
            title={
              config?.pipelinePause?.paused
                ? "Start the selected tasks now. The deployment is paused, and these are the only tasks that will start."
                : "Start the selected tasks now, ahead of the queue, approving any that need it."
            }
            onClick={() => run("/run-now")}
          >
            Run selected now
          </Button>
          <Button
            variant="outlined"
            size="small"
            onClick={() => run("/submit")}
          >
            Submit
          </Button>
          {/* Retry merge for each of them (ui.Client.RetryMerge): puts a
              pull request the merge queue gave up on back on the queue.
              A task with no pull request, or a closed one, refuses and is
              counted; one the queue is still driving is left as it is. */}
          <Button
            variant="outlined"
            size="small"
            title="Put the selected tasks' pull requests back on the merge queue for another try. Tasks without a pull request, and closed tasks, are skipped and counted."
            onClick={() => run("/retry-merge")}
          >
            Retry merge
          </Button>
          <Button
            variant="outlined"
            size="small"
            title="Put the selected tasks aside. They stop waiting to run and drop out of the lists until you ask for them."
            onClick={() => run("/defer")}
          >
            Defer
          </Button>
          <Button
            variant="outlined"
            size="small"
            onClick={() => run("/undefer")}
          >
            Undefer
          </Button>
          <Button
            variant="outlined"
            size="small"
            color="error"
            onClick={() => {
              const unseen =
                hidden > 0
                  ? ` ${hidden} of them ${hidden === 1 ? "is" : "are"} not shown in this view.`
                  : "";
              if (!confirm(`Close ${count} task(s)?${unseen}`)) return;
              run("/close");
            }}
          >
            Close
          </Button>
          <Button
            variant="outlined"
            size="small"
            onClick={() => run("/reopen")}
          >
            Reopen
          </Button>
          {/* The one button here that opens something rather than firing
            an endpoint: the fields (repo, base, auto-merge, framework,
            review, prompt extension, and whatever this deployment
            declares) have values to pick, so they need a form -- see
            BulkEditOverlay. */}
          <Button
            variant="outlined"
            size="small"
            title="Set repo, base branch, auto-merge, framework, review, prompt extension or this deployment's own fields on every selected task at once."
            onClick={() => setEditing(true)}
          >
            Bulk edit…
          </Button>

          <Select
            native
            size="small"
            value={capabilityId}
            onChange={(e) => setCapabilityId(e.target.value)}
            displayEmpty
            // Its only visible label is the "Capability…" placeholder
            // option, which stops naming it once one is picked.
            inputProps={{ "aria-label": "Capability to attach or detach" }}
          >
            <option value="">Capability…</option>
            {/* Only capabilities this deployment has configured -- plus any
              the selection already holds, since this is the only bulk
              way to detach one and hiding it would leave every selected
              task stuck with it (configuredCapabilities, state.js). */}
            {configuredCapabilities(
              config?.capabilities,
              selection.flatMap((t) => t.capabilities || []),
            ).map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </Select>
          <Button
            variant="outlined"
            size="small"
            disabled={!capabilityId}
            onClick={() =>
              onRun((id) =>
                api(`/api/tasks/${id}/capabilities`, {
                  method: "POST",
                  body: JSON.stringify({ id: capabilityId, attach: true }),
                }),
              )
            }
          >
            Attach
          </Button>
          <Button
            variant="outlined"
            size="small"
            disabled={!capabilityId}
            onClick={() =>
              onRun((id) =>
                api(`/api/tasks/${id}/capabilities`, {
                  method: "POST",
                  body: JSON.stringify({ id: capabilityId, attach: false }),
                }),
              )
            }
          >
            Detach
          </Button>
        </>

        <Box sx={{ flex: 1 }} />
        <Button variant="outlined" size="small" onClick={onClear}>
          Clear selection
        </Button>
      </Stack>
      {editing && (
        <BulkEditOverlay
          selection={selection}
          tasks={tasks}
          config={config}
          templates={templates}
          onClose={() => setEditing(false)}
          onRun={onRun}
        />
      )}
    </Paper>
  );
}

// BAR_HEIGHT_PROPERTY is how the rest of the page finds out how much of
// its bottom edge this bar is standing on.
//
// The bar is fixed to that edge, so it takes no space in the layout and
// the list goes on running underneath it: scrolled all the way down with
// a selection up, the last row of the task list and the last card of a
// board column were behind the bar rather than above it -- unreadable
// and unclickable, and worse on a phone, where the buttons wrap onto
// three rows and swallow the last row and a half. The scroll had already
// reached its end, so there was nothing the reader could do about it.
//
// Its height cannot be written down here, since it is however tall the
// buttons wrap to at this width, so the bar measures itself and
// publishes the answer as a custom property on the document root.
// style.css adds it to the bottom padding of the two things that scroll
// under it -- `main` for the flat list, `.board-columns` for the board
// -- so the space the bar occupies is space the content ends before,
// and both go back to their own padding the moment the property is gone.
const BAR_HEIGHT_PROPERTY = "--batch-bar-height";

// useBarHeight keeps that property equal to the bar's own height for as
// long as there is a bar, and removes it as soon as there is not -- an
// empty selection renders nothing, and a stale reservation would leave
// the list ending short of a bar nobody can see.
//
// Re-measured on resize, since a narrower window wraps the buttons onto
// another row and makes the bar taller. ResizeObserver is checked for
// rather than assumed: jsdom has none, and a component test mounting
// this bar should not fail over a measurement no headless DOM can make
// anyway.
function useBarHeight(ref, count) {
  useEffect(() => {
    const root = document.documentElement;
    const bar = ref.current;
    if (!bar) {
      root.style.removeProperty(BAR_HEIGHT_PROPERTY);
      return undefined;
    }
    const measure = () =>
      root.style.setProperty(BAR_HEIGHT_PROPERTY, `${bar.offsetHeight}px`);
    measure();
    const observer =
      typeof ResizeObserver === "undefined"
        ? null
        : new ResizeObserver(measure);
    observer?.observe(bar);
    return () => {
      observer?.disconnect();
      root.style.removeProperty(BAR_HEIGHT_PROPERTY);
    };
  }, [ref, count]);
}
