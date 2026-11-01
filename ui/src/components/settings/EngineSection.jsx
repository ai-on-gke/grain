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

import { TextField } from "@mui/material";
import SandboxCommitmentNote from "../SandboxCommitmentNote.jsx";
import MergeBatchingNote from "../MergeBatchingNote.jsx";
import MergeGatheringNote from "../MergeGatheringNote.jsx";
import SettingsCard, { settingHints } from "./SettingsCard.jsx";
import { identifierField } from "../../identifierField.js";

// EngineSection is Settings → Engine (docs/design/settings-panel.md
// §3.2). `s` is the page's useSettingsSection().
export default function EngineSection({ s }) {
  const { settings, onTextBlur, onIntBlur, onEnterKey } = s;
  const { unrunnableHint, unrunnableChip } = settingHints(settings);
  return (
    <>
      <SettingsCard title="Polling" anchor="polling">
        <TextField
          key={`poll-${settings.pollInterval || ""}`}
          name="pollInterval"
          data-setting-id="pollInterval"
          label="Poll interval"
          helperText={unrunnableHint("pollInterval", "Go duration, e.g. 10s")}
          defaultValue={settings.pollInterval || ""}
          onBlur={onTextBlur("pollInterval")}
          onKeyDown={onEnterKey}
          autoComplete="off"
          inputProps={identifierField}
          fullWidth
          margin="normal"
          InputProps={{ endAdornment: unrunnableChip("pollInterval") }}
        />
        <TextField
          key={`maxWorkers-${settings.maxWorkers ?? ""}`}
          name="maxWorkers"
          data-setting-id="maxWorkers"
          label="Max worker agents"
          helperText="Maximum number of ordinary tasks dispatched at once (10 by default)"
          type="number"
          inputProps={{ min: 1, step: 1 }}
          defaultValue={String(settings.maxWorkers || 10)}
          onBlur={onIntBlur("maxWorkers", {
            defaultVal: 10,
            emptyVal: 10,
          })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
      </SettingsCard>
      <SettingsCard title="Merge queue" anchor="merge-queue">
        <TextField
          key={`mergesPerRepo-${settings.maxMergesPerRepo ?? ""}`}
          name="maxMergesPerRepo"
          data-setting-id="maxMergesPerRepo"
          label="Max merges per repo branch"
          helperText="maximum ready pull requests merged into each branch of a repository per queue cycle"
          type="number"
          inputProps={{ min: 1, step: 1 }}
          defaultValue={String(settings.maxMergesPerRepo || 1)}
          onBlur={onIntBlur("maxMergesPerRepo", {
            defaultVal: 1,
            emptyVal: 1,
          })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          // Keyed on the size saved too, not only the one chosen: a
          // refused save rolls mergeBatchSize back, and that remount is
          // what takes the refused number out of the box (2026-11-01
          // walkthrough: 21 stayed in it, looking saved).
          key={`mergeBatch-${settings.mergeBatchSizeChosen || ""}-${settings.mergeBatchSize ?? ""}`}
          name="mergeBatchSize"
          data-setting-id="mergeBatchSize"
          label="Merge batch size"
          helperText="how many queued changes the merge queue tests together on a throwaway ref before merging the lot. 1 turns batching off and merges one at a time; a bigger batch saves suites when every change in it is good and costs an extra one, plus the latency of the changes that were fine, when one is not. Blank uses grain's default"
          type="number"
          inputProps={{ min: 1, step: 1 }}
          defaultValue={String(settings.mergeBatchSizeChosen || "")}
          placeholder={
            settings.mergeBatchSize
              ? String(settings.mergeBatchSize)
              : undefined
          }
          onBlur={onIntBlur("mergeBatchSize", {
            emptyVal: 0,
            readFrom: "mergeBatchSizeChosen",
          })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`mergeGatherWindow-${settings.mergeGatherWindow || ""}`}
          name="mergeGatherWindow"
          data-setting-id="mergeGatherWindow"
          label="Merge gather window"
          helperText="how long the queue holds a change that is ready to merge, so that another change whose Grain CI run is still going can be tested with it rather than after it. It only waits while there is such a run in flight on the same branch, and it lets go the moment that run reports. Blank uses grain's default; a negative duration (-1s) turns the wait off and merges a ready change at once."
          defaultValue={settings.mergeGatherWindow || ""}
          placeholder={settings.mergeGatherWindowDefault || undefined}
          onBlur={onTextBlur("mergeGatherWindow")}
          onKeyDown={onEnterKey}
          autoComplete="off"
          inputProps={identifierField}
          fullWidth
          margin="normal"
        />
        <MergeBatchingNote
          batching={settings.mergeBatching}
          limit={settings.mergeBatchSize}
          sx={{ mt: 1 }}
        />
        <MergeGatheringNote
          gathering={settings.mergeGathering}
          sx={{ mt: 1 }}
        />
        {/* Max CI jobs likewise: a team's own max CI jobs is the
          count, and this deployment's sandbox budget is what holds
          the host. */}

        <SandboxCommitmentNote
          commitment={settings.sandboxCommitment}
          sx={{ mt: 1 }}
        />
      </SettingsCard>
    </>
  );
}
