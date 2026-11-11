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

import { useEffect, useState } from "react";
import { Button, Chip, Stack, TextField } from "@mui/material";
import api from "../../api.js";
import { useTimeZone } from "../../TimeZoneContext.jsx";
import { formatDateTime } from "../../time.js";
import Markdown from "../Markdown.jsx";

// A durable task's context (model.TaskContext,
// design/design/durable-tasks.design.md#DT-15): the compaction cursor its
// prompts start from, how stale its handover is, and the current brief
// that stands in for its description. Read from GET
// /api/tasks/{id}/context, which only a durable task's page asks for --
// an ordinary task's prompt is its description and its whole thread, and
// none of this applies to it.

// isDurable is Task.IsDurable for the browser.
export function isDurable(t) {
  return t?.admission === "durable";
}

// useTaskContext is t's context view, or null for a task that is not
// durable or whose context has not loaded yet. It is read again whenever
// the task's conversation, runs or state move, since each of those can
// age the handover or answer a compaction request; setContext takes the
// view a POST answered with, so a change shows without waiting for that.
export function useTaskContext(t) {
  const [context, setContext] = useState(null);
  const durable = isDurable(t);
  const id = t?.id;
  const comments = (t?.comments?.length || 0) + (t?.olderComments || 0);
  const attempts = (t?.attempts?.length || 0) + (t?.olderAttempts || 0);
  const state = t?.state;
  useEffect(() => {
    if (!durable || !id) {
      setContext(null);
      return undefined;
    }
    let live = true;
    api(`/api/tasks/${id}/context`)
      .then((view) => live && setContext(view))
      // The page still works without it: the description shows as it
      // does for any task, and the section says nothing it cannot back.
      .catch(() => live && setContext(null));
    return () => {
      live = false;
    };
  }, [durable, id, comments, attempts, state]);
  // A view for some other task (the pane swapped to the next one before
  // its read came back) is not this task's.
  return [context?.task_id === id ? context : null, setContext];
}

function plural(n, word) {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

// BriefSection is where a task's description goes. On a durable task
// with a current brief, the brief takes its place -- it is what the
// task's prompts carry -- with who set it and why, the original
// description a click away, and every earlier brief. It is also where a
// person edits the brief: a reason is required, as it is on the API.
export function BriefSection({ t, context, setContext, act, description }) {
  const zone = useTimeZone();
  const [draft, setDraft] = useState(null);
  if (!context) return description;
  const brief = context.current_brief || "";
  const edits = context.brief_edits || [];
  const closed = t.state === "closed";

  const save = (next) =>
    act(async () => {
      const view = await api(`/api/tasks/${t.id}/brief`, {
        method: "POST",
        body: JSON.stringify({ brief: next.brief, reason: next.reason }),
      });
      setContext(view);
    }, t.id).then((ok) => ok && setDraft(null));

  return (
    <div data-testid="brief-section">
      {draft ? (
        <Stack spacing={1} sx={{ my: 2 }} data-testid="brief-editor">
          <TextField
            label="Current brief"
            multiline
            minRows={4}
            fullWidth
            value={draft.brief}
            onChange={(e) => setDraft({ ...draft, brief: e.target.value })}
            helperText="Replaces the description in this task's prompts. Leave it empty to go back to the original."
          />
          <TextField
            label="Why it changed"
            size="small"
            fullWidth
            required
            value={draft.reason}
            onChange={(e) => setDraft({ ...draft, reason: e.target.value })}
          />
          <Stack direction="row" spacing={1}>
            <Button
              size="small"
              variant="contained"
              disabled={!draft.reason.trim()}
              onClick={() => save(draft)}
            >
              Save brief
            </Button>
            <Button size="small" onClick={() => setDraft(null)}>
              Cancel
            </Button>
          </Stack>
        </Stack>
      ) : brief ? (
        <>
          <div className="brief-header timeline-meta" data-testid="brief-meta">
            <span>
              <strong>Current brief</strong>
              {context.brief_updated_by && ` · by ${context.brief_updated_by}`}
              {context.brief_updated_at &&
                ` · ${formatDateTime(context.brief_updated_at, zone)}`}
              {edits.length > 0 && edits[edits.length - 1].reason && (
                <> · {edits[edits.length - 1].reason}</>
              )}
            </span>
          </div>
          <Markdown className="description brief">{brief}</Markdown>
        </>
      ) : (
        description
      )}
      {!draft && (
        <div className="brief-extras">
          {!closed && (
            <Button
              size="small"
              onClick={() => setDraft({ brief, reason: "" })}
              data-testid="brief-edit"
            >
              {brief ? "Edit brief" : "Write a brief"}
            </Button>
          )}
          {brief && (
            <details data-testid="brief-original">
              <summary>Original description</summary>
              <p className="hint">
                What the task was filed with. Prompts carry the brief above
                instead.
              </p>
              {description}
            </details>
          )}
          {edits.length > 0 && (
            <details data-testid="brief-history">
              <summary>Brief history ({plural(edits.length, "edit")})</summary>
              <ol className="brief-history">
                {[...edits].reverse().map((e) => (
                  <li key={e.id}>
                    <div className="timeline-meta">
                      {formatDateTime(e.at, zone)} · {e.by}
                      {e.reason ? ` · ${e.reason}` : ""}
                    </div>
                    {e.brief ? (
                      <Markdown className="description brief">
                        {e.brief}
                      </Markdown>
                    ) : (
                      <p className="hint">
                        Cleared: prompts went back to the original description.
                      </p>
                    )}
                  </li>
                ))}
              </ol>
            </details>
          )}
        </div>
      )}
    </div>
  );
}

// handoverAgeText is the handover's age in words.
export function handoverAgeText(context) {
  const age = context.handover_age || {};
  if (!age.exists) return "No handover yet.";
  return `${plural(age.runs || 0, "run")} and ${plural(age.comments || 0, "comment")} since it was written or confirmed.`;
}

// ContextSection is the side panel's account of what this durable
// task's next prompt starts from: the handover (its version, its age,
// and whether it is stale), how much of the thread is folded into it,
// and the Compact action. Compact without a handover asks the next run
// to compact before it does anything else; with one, the task is
// compacted to it at once, under the person's name.
export function ContextSection({ t, context, setContext, act }) {
  const zone = useTimeZone();
  const [handover, setHandover] = useState(null);
  if (!context) return null;
  const closed = t.state === "closed";

  const compact = (body) =>
    act(async () => {
      const view = await api(`/api/tasks/${t.id}/compact`, {
        method: "POST",
        body: JSON.stringify(body ? { handover: body } : {}),
      });
      setContext(view);
    }, t.id).then((ok) => ok && setHandover(null));

  return (
    <fieldset data-testid="context-section" style={{ minWidth: 0 }}>
      <legend>Context</legend>
      <p style={{ margin: "0 0 4px" }} data-testid="context-handover">
        <strong>Handover</strong>
        {context.handover_version ? ` v${context.handover_version}` : ""}
        {context.handover_stale && (
          <>
            {" "}
            <Chip
              component="span"
              size="small"
              color="warning"
              label="stale"
              data-testid="handover-stale"
              sx={{ height: 20, verticalAlign: "text-bottom" }}
            />
          </>
        )}
        . {handoverAgeText(context)}
      </p>
      {context.handover_stale && (
        <p className="hint">The next run is asked to replace or confirm it.</p>
      )}
      <p className="hint" data-testid="context-compaction">
        {context.compacted_through
          ? `Compacted ${formatDateTime(context.compacted_at, zone)}${
              context.compacted_by ? ` by ${context.compacted_by}` : ""
            }: ${plural(context.summarised || 0, "comment")} summarised, collapsed in the timeline and left out of prompts.`
          : "Never compacted: prompts carry the whole thread, cut to size."}
      </p>
      {context.compact_requested_at && (
        <p className="hint" data-testid="context-requested">
          Compaction requested{" "}
          {formatDateTime(context.compact_requested_at, zone)}
          {context.compact_requested_by
            ? ` by ${context.compact_requested_by}`
            : ""}
          : the next run compacts before anything else.
        </p>
      )}
      {handover !== null && (
        <Stack spacing={1} sx={{ mt: 1 }} data-testid="compact-editor">
          <TextField
            label="Handover"
            multiline
            minRows={4}
            fullWidth
            value={handover}
            onChange={(e) => setHandover(e.target.value)}
            helperText="Folds every comment so far into this summary now."
          />
        </Stack>
      )}
      {!closed && (
        <Stack
          direction="row"
          spacing={1}
          sx={{ mt: 1, flexWrap: "wrap", rowGap: 1 }}
        >
          {handover !== null ? (
            <>
              <Button
                size="small"
                variant="contained"
                disabled={!handover.trim()}
                onClick={() => compact(handover)}
              >
                Compact now
              </Button>
              <Button size="small" onClick={() => setHandover(null)}>
                Cancel
              </Button>
            </>
          ) : (
            <>
              <Button
                size="small"
                onClick={() => compact("")}
                disabled={Boolean(context.compact_requested_at)}
                data-testid="compact"
              >
                Compact
              </Button>
              <Button size="small" onClick={() => setHandover("")}>
                Write handover…
              </Button>
            </>
          )}
        </Stack>
      )}
    </fieldset>
  );
}
