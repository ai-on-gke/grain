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

import { Typography } from "@mui/material";
import { formatBytes } from "./PackageCachePanel.jsx";
import { oldestAge } from "./CIHistoryPanel.jsx";
import { formatAge } from "./CIJobCachePanel.jsx";

// What this deployment's collected CI artifacts occupy, in the System
// pane's Storage section (pkg/ui/cistorage.go, grain/task-276).
//
// No button, and that is the point of the entry rather than a gap in it.
// An artifact is the only copy of what a run produced -- the trace, the
// screenshot, the report a job was told to keep because its log could not
// carry it -- so there is nothing here that a re-download or a re-run
// puts back, which is the whole difference between this and the two
// panels above it. The only thing on this deployment that drops them is
// the reclaim behind the disk floor, on a host that is already refusing
// work (pkg/ci's ClearableArtifacts, grain/task-213).
//
// So what the operator asking "what can I safely clear?" is given here is
// a number, the bounds that sweep it without being asked, and the
// directory to go to if they mean to act on it by hand anyway. That is
// more use than a button it would be wrong to press.

// artifactsSummary is what is stored, against the ceiling on the
// directory.
function artifactsSummary(artifacts, now = Date.now()) {
  if (!artifacts) return "";
  const bytes = artifacts.bytes || 0;
  const count = artifacts.artifacts || 0;
  const runs = artifacts.runs || 0;
  const parts = [
    artifacts.maxBytes
      ? `${formatBytes(bytes)} of ${formatBytes(artifacts.maxBytes)}`
      : `${formatBytes(bytes)}, no ceiling`,
  ];
  if (count === 0) {
    parts.push("no run has left anything behind");
    return parts.join(" · ");
  }
  parts.push(
    `${count} ${count === 1 ? "artifact" : "artifacts"} from ${runs} ${runs === 1 ? "run" : "runs"}`,
  );
  const age = oldestAge(artifacts.oldestAt, now);
  if (age) parts.push(`the oldest of them ${age} old`);
  return parts.join(" · ");
}

// artifactsBounds is the rest of what bounds them: per run, per artifact
// and by age. Every number here is the one in force.
function artifactsBounds(artifacts) {
  if (!artifacts) return "";
  const parts = [];
  if (artifacts.maxRunBytes)
    parts.push(`up to ${formatBytes(artifacts.maxRunBytes)} per run`);
  if (artifacts.maxArtifactBytes)
    parts.push(`${formatBytes(artifacts.maxArtifactBytes)} for any one file`);
  const age = formatAge(artifacts.maxAgeSeconds);
  if (age) parts.push(`none older than ${age}`);
  if (parts.length === 0) return "";
  // And where they come from, for the same reason the job cache's line
  // says it: these four were daemon flags until grain/task-317, so an
  // operator reading a ceiling here could only move it by editing a
  // systemd unit and restarting.
  return `Kept: ${parts.join(", ")}. All four are editable on Settings → CI, where a change binds the next sweep rather than the next restart.`;
}

// CIArtifactsPanel is those two lines and the sentence that says why
// there is no button beside them.
export default function CIArtifactsPanel({ artifacts }) {
  if (!artifacts) {
    return (
      <Typography variant="caption" color="text.secondary">
        This deployment keeps no CI artifacts -- a job that says{" "}
        <code>artifacts:</code> has nowhere to put them. The daemon&apos;s log
        says why.
      </Typography>
    );
  }
  return (
    <>
      <Typography variant="body2" color="text.secondary">
        {artifactsSummary(artifacts)}
      </Typography>
      <Typography variant="caption" color="text.secondary" component="div">
        {artifactsBounds(artifacts)} There is no button: an artifact is the only
        copy of what a run produced, so these are swept by the bounds rather
        than cleared by hand
        {artifacts.dir ? ` (${artifacts.dir} on the host)` : ""}.
      </Typography>
    </>
  );
}
