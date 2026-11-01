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

import {
  Checkbox,
  FormControlLabel,
  TextField,
  Typography,
} from "@mui/material";
import SettingsCard from "./SettingsCard.jsx";
import { UpgradeCheckSettings } from "../UpgradePanel.jsx";

// GeneralSection is Settings → General (design/design/settings-panel.design.md
// §3.2). `s` is the page's useSettingsSection().
export default function GeneralSection({ s, config }) {
  const { settings, saveChoice, onTextBlur, onEnterKey } = s;
  const external = Boolean(
    settings ? settings.externalConfigRepo : config?.externalConfigRepo,
  );
  const settingsLocked =
    typeof settings?.settingsLocked === "boolean"
      ? settings.settingsLocked
      : Boolean(config?.settingsLocked || config?.configRepoOnly);
  return (
    <>
      <SettingsCard title="Deployment" anchor="deployment">
        <TextField
          key={`env-${settings.environmentName || ""}`}
          name="environmentName"
          data-setting-id="environmentName"
          label="Environment name"
          helperText="Shown beside the grain mark and in the browser tab, e.g. staging. Leave empty for an unnamed deployment."
          defaultValue={settings.environmentName || ""}
          onBlur={onTextBlur("environmentName")}
          onKeyDown={onEnterKey}
          inputProps={{ maxLength: 32 }}
          autoComplete="off"
          fullWidth
          margin="normal"
        />

        <FormControlLabel
          control={
            <Checkbox
              name="showClosedByDefault"
              data-setting-id="showClosedByDefault"
              checked={!!settings.showClosedByDefault}
              onChange={(evt) =>
                saveChoice({ showClosedByDefault: evt.target.checked })
              }
            />
          }
          label={
            <>
              Show closed tasks by default
              <span className="hint">
                off (default): a task list opens narrowed by &quot;State is none
                of Deferred, Closed&quot;, so closed tasks are out of it until
                that filter is edited. on: the filter leaves closed out, showing
                them from the start.
              </span>
            </>
          }
          sx={{ display: "flex", mt: 1 }}
        />
      </SettingsCard>
      <SettingsCard
        title="Settings lock"
        anchor="settings-lock"
        allowInExternalMode={!external}
      >
        <FormControlLabel
          control={
            <Checkbox
              name="settingsLocked"
              data-setting-id="settingsLocked"
              checked={settingsLocked}
              onChange={(evt) =>
                saveChoice({ settingsLocked: evt.target.checked })
              }
            />
          }
          label={
            <>
              Lock settings in UI (require config repo PRs)
              <span className="hint">
                off (default): settings are editable in the UI and API and
                synced to the config repository automatically. on: settings
                managed in the config repository are read-only here and must be
                changed via config pull requests.
              </span>
            </>
          }
          sx={{ display: "flex", mt: 1 }}
        />
      </SettingsCard>
      <SettingsCard title="Updates" anchor="updates">
        <UpgradeCheckSettings
          key={`${settings.upgradeCheckMode}-${settings.upgradeCheckPausesPipeline}-${settings.autoUpgradeChannel}-${settings.autoUpgradeTime}`}
          settings={settings}
          onSave={saveChoice}
        />
      </SettingsCard>
      <SettingsCard title="About" anchor="about">
        <Typography
          variant="body2"
          color="text.secondary"
          data-testid="settings-version-display"
          sx={{ fontFamily: "monospace" }}
        >
          {versionText(config?.version)}
        </Typography>
      </SettingsCard>
    </>
  );
}

// versionText is the build this daemon runs, as the old Settings
// overlay's Version line wrote it: the release, the commit (marked
// -dirty for a modified tree) and when it was committed.
export function versionText(v) {
  if (!v || (!v.commit && !v.release)) {
    return v?.edition ? `dev (${v.edition}, unstamped)` : "dev (unstamped)";
  }
  const parts = [];
  if (v.release) parts.push(v.release);
  if (v.edition) parts.push(v.edition);
  if (v.commit) parts.push(v.commit.slice(0, 7) + (v.modified ? "-dirty" : ""));
  if (v.committedAt) {
    const d = new Date(v.committedAt);
    if (!Number.isNaN(d.getTime()))
      parts.push(d.toISOString().slice(0, 16).replace("T", " ") + "Z");
  }
  return parts.join(" · ");
}
