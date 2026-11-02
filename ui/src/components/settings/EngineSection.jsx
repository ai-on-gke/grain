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
import SettingsCard, { settingHints } from "./SettingsCard.jsx";
import { identifierField } from "../../identifierField.js";
import { ConfigManagedNotice } from "../ConfigRepoLink.jsx";

// EngineSection is Settings → Engine (docs/design/settings-panel.md
// §3.2). `s` is the page's useSettingsSection().
export default function EngineSection({ s }) {
  const { settings, onTextBlur, onIntBlur, onEnterKey, readOnly } = s;
  const { unrunnableHint, unrunnableChip } = settingHints(settings);
  // Max worker agents is a bootstrap setting and stays editable while
  // settings are locked; the poll interval and the merge queue are
  // config-managed and the API refuses them then
  // (settings-mutability.spec.md D-1).
  const locked = Boolean(s.configLocked);
  return (
    <>
      {locked && !readOnly && (
        <ConfigManagedNotice
          what="The poll interval and the merge queue settings"
          paths={["tables/grain_config.json"]}
        />
      )}
      <SettingsCard title="Polling" anchor="polling">
        <TextField
          key={`poll-${settings.pollInterval || ""}`}
          name="pollInterval"
          disabled={locked}
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
          disabled={locked}
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
