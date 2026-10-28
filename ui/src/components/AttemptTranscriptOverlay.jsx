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

import { useAttemptTranscript } from "../useAttemptTranscript.js";
import CapabilityAudit from "./CapabilityAudit.jsx";
import Overlay from "./Overlay.jsx";

// AttemptTranscriptOverlay is what typing on a task attempt in
// DetailOverlay's own Timeline opens: the single scrolling pane over
// that attempt's whole agent transcript -- thinking, text and every tool
// call interleaved, in the order the agent produced them
// (pkg/agent/claude/transcript.go's own doc comment on how it is built)
// -- useful for debugging why an attempt failed, or for watching one
// still in flight without reaching for a shell (bwsalmon/agents#446,
// bwsalmon/agents#467). The fetching, polling and pin-to-bottom
// scrolling are useAttemptTranscript's. Above the transcript, when the
// attempt has any, is what its capability grants recorded doing
// (CapabilityAudit) -- grain's record rather than the agent's account.
export default function AttemptTranscriptOverlay({
  taskId,
  attempt,
  onClose,
  showError,
}) {
  const { transcript, view, onScroll } = useAttemptTranscript(
    taskId,
    attempt,
    showError,
  );

  return (
    <Overlay
      onClose={onClose}
      wide
      label={`Attempt #${attempt.number} transcript`}
    >
      <h2>Attempt #{attempt.number} transcript</h2>
      {/* Which model this attempt was given (ui.Attempt.Model), since a
          task left to the rotation is not always on the same one. */}
      {attempt.model && (
        <p className="attempt-transcript-model">
          Ran on <code>{attempt.model}</code>
          {attempt.agentFramework && <> ({attempt.agentFramework})</>}
        </p>
      )}
      <CapabilityAudit
        taskId={taskId}
        attempt={attempt}
        showError={showError}
      />
      <pre className="logs-view" ref={view} onScroll={onScroll}>
        {transcript === null
          ? "Loading…"
          : transcript ||
            (attempt.finishedAt
              ? "(no transcript recorded)"
              : "Still running -- nothing written yet.")}
      </pre>
    </Overlay>
  );
}
