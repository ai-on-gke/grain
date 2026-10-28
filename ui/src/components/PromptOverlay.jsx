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
import api from "../api.js";
import Overlay from "./Overlay.jsx";

// PromptOverlay is the whole prompt a task's agent was actually handed
// (grain/task-91): its title and description plus everything grain adds
// to them on the way out -- which repo and branch to push, the
// conversation so far, the attachments materialized into the sandbox,
// each capability's own section, and the follow-on task etiquette
// (pkg/orchestrator's BuildPrompt and prepareCapabilities). None of that
// is visible anywhere else, so a task that behaved oddly could only be
// explained by guessing at what it was told.
//
// It shows one attempt's prompt, the most recent one that recorded any
// (ui.Client.TaskPrompt): the prompt is assembled per run, out of a task
// that may since have been edited and a conversation that has since
// grown, so there is no single prompt for a task -- only the latest
// answer to "what would/did an agent see".
//
// Fetched once, not polled: unlike AttemptTranscriptOverlay's own
// transcript, a run's prompt is written before its agent's first turn
// and never changes after (Store.SetRunPrompt), so there is nothing for
// a second fetch to pick up.
// promptSize is the one line that says what this prompt costs the run it
// was handed to: its size, the token estimate, and the share of the
// context window grain measures against (model.PromptSize). The window
// is named rather than implied, because it is an assumption grain states
// and not a fact it holds -- a share quoted on its own is a number the
// reader cannot check.
//
// It is here because a prompt is the one part of a dispatch that grows
// without bound: a task's whole conversation is replayed into every
// later attempt, so attempt 10 of a week-old chat looks exactly like
// attempt 1 in this pane until somebody measures it (grain/task-268).
// Nothing for a response that carried no measurement, which is every
// answer from a daemon older than this.
function promptSize({ bytes, tokens, contextTokens }) {
  if (!bytes) return null;
  const size =
    bytes >= 1 << 20
      ? `${(bytes / (1 << 20)).toFixed(1)} MB`
      : bytes >= 1 << 10
        ? `${Math.floor(bytes / (1 << 10))} KB`
        : `${bytes} B`;
  if (!tokens) return size;
  if (!contextTokens) return `${size} · ~${tokens.toLocaleString()} tokens`;
  const share = Math.round((tokens / contextTokens) * 100);
  return (
    `${size} · ~${tokens.toLocaleString()} tokens · ${share}% of a ` +
    `${contextTokens.toLocaleString()}-token window`
  );
}

export default function PromptOverlay({ taskId, onClose }) {
  const [prompt, setPrompt] = useState(null);
  const [attempt, setAttempt] = useState(0);
  const [size, setSize] = useState(null);
  // Reported in the pane itself rather than through App.jsx's own
  // showError banner: an error about the prompt belongs where the prompt
  // was going to be, in the pane the reader just opened, rather than in
  // a banner behind the task page this opens over.
  const [error, setError] = useState(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await api(
          `/api/tasks/${encodeURIComponent(taskId)}/prompt`,
        );
        if (cancelled) return;
        setPrompt(res.prompt || "");
        setAttempt(res.attempt || 0);
        setSize(promptSize(res));
      } catch (err) {
        if (!cancelled) setError(err);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [taskId]);

  return (
    <Overlay onClose={onClose} wide label={`Prompt for ${taskId}`}>
      <h2>
        Prompt for {taskId}
        {attempt > 0 && <span className="hint"> · attempt #{attempt}</span>}
        {size && <span className="hint"> · {size}</span>}
      </h2>
      <pre className="logs-view">
        {error
          ? `Could not load this task's prompt: ${error.message}`
          : prompt === null
            ? "Loading…"
            : prompt ||
              "No prompt recorded yet -- nothing has been dispatched for this task, " +
                "so grain has not built one. A prompt is recorded the moment a run hands it to its agent."}
      </pre>
    </Overlay>
  );
}
