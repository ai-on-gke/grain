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
import SettingsCard from "./SettingsCard.jsx";
import { identifierField } from "../../identifierField.js";

// CISection is Settings → CI (docs/design/settings-panel.md
// §3.2). `s` is the page's useSettingsSection().
export default function CISection({ s }) {
  const { settings, onTextBlur, onIntBlur, onEnterKey } = s;
  return (
    <>
      <SettingsCard title="CI queue" anchor="ci-queue">
        <TextField
          key={`ciJobsPerRun-${settings.ciJobsPerRun || ""}`}
          name="ciJobsPerRun"
          data-setting-id="ciJobsPerRun"
          label="CI jobs per run"
          helperText="ceiling on how many sandboxes one Grain CI run may hold at once. Blank or 0 sets no per-run limit, so all ready jobs in a run proceed to the deployment's sandbox budget."
          type="number"
          inputProps={{ min: 0, step: 1 }}
          defaultValue={
            settings.ciJobsPerRun ? String(settings.ciJobsPerRun) : ""
          }
          onBlur={onIntBlur("ciJobsPerRun", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`ciAgeBoost-${settings.ciAgeBoost || ""}`}
          name="ciAgeBoost"
          data-setting-id="ciAgeBoost"
          label="CI queue age boost"
          helperText="how long a waiting Grain CI job queues to gain one step of rank -- the floor that stops higher-priority work starving the rest: a job ranked N steps below the work ahead of it overtakes that work after N boosts. Blank uses grain's default."
          defaultValue={settings.ciAgeBoost || ""}
          placeholder={settings.ciAgeBoostDefault || undefined}
          onBlur={onTextBlur("ciAgeBoost")}
          onKeyDown={onEnterKey}
          autoComplete="off"
          inputProps={identifierField}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`ciReserveAfter-${settings.ciReserveAfter || ""}`}
          name="ciReserveAfter"
          data-setting-id="ciReserveAfter"
          label="CI capacity reserved after"
          helperText="how long the sandbox budget must have been refusing a job before capacity is held for it rather than given to smaller jobs that fit. That trade is the point: a little capacity is wasted while the hole fills, and without it an expensive job never runs when capacity only ever frees in slivers. Blank uses grain's default; a negative duration (-1s) turns the reservation off."
          defaultValue={settings.ciReserveAfter || ""}
          placeholder={settings.ciReserveAfterDefault || undefined}
          onBlur={onTextBlur("ciReserveAfter")}
          onKeyDown={onEnterKey}
          autoComplete="off"
          inputProps={identifierField}
          fullWidth
          margin="normal"
        />
      </SettingsCard>
      <SettingsCard title="CI history" anchor="ci-history">
        <TextField
          key={`ciHistory-${settings.ciRunHistory || ""}`}
          name="ciRunHistory"
          data-setting-id="ciRunHistory"
          label="CI runs kept"
          helperText="how many finished Grain CI runs keep their record -- the ref, commit and every job's and step's verdict, without the logs. Blank uses grain's default; -1 keeps every run."
          type="number"
          inputProps={{ step: 1 }}
          defaultValue={
            settings.ciRunHistory ? String(settings.ciRunHistory) : ""
          }
          placeholder={
            settings.ciRunHistoryDefault
              ? String(settings.ciRunHistoryDefault)
              : undefined
          }
          onBlur={onIntBlur("ciRunHistory", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`ciStepMb-${settings.ciStepOutputMb || ""}`}
          name="ciStepOutputMb"
          data-setting-id="ciStepOutputMb"
          label="CI step output kept (MiB)"
          helperText="ceiling on the step output kept across the whole history: whole runs' logs are swept, oldest first, until what is left fits, and the runs themselves stay. This is the one that decides whether a failure is still readable days later. Blank uses grain's default; -1 keeps every byte."
          type="number"
          inputProps={{ step: 1 }}
          defaultValue={
            settings.ciStepOutputMb ? String(settings.ciStepOutputMb) : ""
          }
          placeholder={
            settings.ciStepOutputMbDefault
              ? String(settings.ciStepOutputMbDefault)
              : undefined
          }
          onBlur={onIntBlur("ciStepOutputMb", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`ciStepDays-${settings.ciStepOutputDays || ""}`}
          name="ciStepOutputDays"
          data-setting-id="ciStepOutputDays"
          label="CI step output age (days)"
          helperText="how long a run's step output may live whatever room there is under the ceiling. Blank uses grain's default; -1 bounds output by size alone."
          type="number"
          inputProps={{ step: 1 }}
          defaultValue={
            settings.ciStepOutputDays ? String(settings.ciStepOutputDays) : ""
          }
          placeholder={
            settings.ciStepOutputDaysDefault
              ? String(settings.ciStepOutputDaysDefault)
              : undefined
          }
          onBlur={onIntBlur("ciStepOutputDays", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`ciRunKb-${settings.ciRunOutputKb || ""}`}
          name="ciRunOutputKb"
          data-setting-id="ciRunOutputKb"
          label="CI output per run (KiB)"
          helperText="most step output any one run may store, spent on the steps a reader opens first. A run that would print more than the whole ceiling above has its output declined rather than sweeping every older run's away. Blank uses grain's default."
          type="number"
          inputProps={{ min: 0, step: 1 }}
          defaultValue={
            settings.ciRunOutputKb ? String(settings.ciRunOutputKb) : ""
          }
          placeholder={
            settings.ciRunOutputKbDefault
              ? String(settings.ciRunOutputKbDefault)
              : undefined
          }
          onBlur={onIntBlur("ciRunOutputKb", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        {/* What CI keeps on this deployment's own disk, as against
          what it keeps in the database above (grain/task-317).
          Beside the history for the reason the history is beside the
          job limit: the three groups together are "how much CI, for
          how long, and at what cost in disk". These were daemon
          flags until the System pane started reporting what each of
          them was holding -- an operator reading "200 MiB of 256
          MiB" there should not have to edit a systemd unit to move
          the ceiling. Blank means grain's own default, shown as the
          placeholder; unlike the history's fields there is no -1
          here, because a directory on the volume this deployment
          dispatches out of has no unbounded setting. A change binds
          the next sweep, five minutes out at most -- the CI pane's
          own Clear is for getting the disk back now. */}
      </SettingsCard>
      <SettingsCard title="CI job cache" anchor="ci-job-cache">
        <TextField
          key={`ciCacheMb-${settings.ciCacheMb || ""}`}
          name="ciCacheMb"
          data-setting-id="ciCacheMb"
          label="CI job cache size (MiB)"
          helperText="ceiling on the whole job cache directory: the oldest entries are swept until it fits. Nothing here can change an answer -- a cache key is a promise the work was identical -- so this trades disk against how often a job runs twice. Blank uses grain's default."
          type="number"
          inputProps={{ min: 0, step: 1 }}
          defaultValue={settings.ciCacheMb ? String(settings.ciCacheMb) : ""}
          placeholder={
            settings.ciCacheMbDefault
              ? String(settings.ciCacheMbDefault)
              : undefined
          }
          onBlur={onIntBlur("ciCacheMb", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`ciCacheEntries-${settings.ciCacheEntries || ""}`}
          name="ciCacheEntries"
          data-setting-id="ciCacheEntries"
          label="CI job cache entries"
          helperText="ceiling on how many entries it holds, alongside the size: the entries are one flat directory and a hit is one open in it, so small results would reach six figures long before they reached the size ceiling. Blank uses grain's default."
          type="number"
          inputProps={{ min: 0, step: 1 }}
          defaultValue={
            settings.ciCacheEntries ? String(settings.ciCacheEntries) : ""
          }
          placeholder={
            settings.ciCacheEntriesDefault
              ? String(settings.ciCacheEntriesDefault)
              : undefined
          }
          onBlur={onIntBlur("ciCacheEntries", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`ciCacheDays-${settings.ciCacheDays || ""}`}
          name="ciCacheDays"
          data-setting-id="ciCacheDays"
          label="CI job cache age (days)"
          helperText="how long an unasked-for result is kept. An entry does not go stale -- it stops being one anybody's branch will ask for again. Blank uses grain's default."
          type="number"
          inputProps={{ min: 0, step: 1 }}
          defaultValue={
            settings.ciCacheDays ? String(settings.ciCacheDays) : ""
          }
          placeholder={
            settings.ciCacheDaysDefault
              ? String(settings.ciCacheDaysDefault)
              : undefined
          }
          onBlur={onIntBlur("ciCacheDays", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
      </SettingsCard>
      <SettingsCard title="CI artifacts" anchor="ci-artifacts">
        <TextField
          key={`ciArtifactMb-${settings.ciArtifactMb || ""}`}
          name="ciArtifactMb"
          data-setting-id="ciArtifactMb"
          label="CI artifacts kept (MiB)"
          helperText="ceiling on the whole artifact directory -- what failing jobs declared worth keeping, a trace or a core file or a log too big for a step's output. The oldest runs' artifacts are swept whole until it fits, and dropping one costs the re-run that would produce it again, never a verdict. Blank uses grain's default."
          type="number"
          inputProps={{ min: 0, step: 1 }}
          defaultValue={
            settings.ciArtifactMb ? String(settings.ciArtifactMb) : ""
          }
          placeholder={
            settings.ciArtifactMbDefault
              ? String(settings.ciArtifactMbDefault)
              : undefined
          }
          onBlur={onIntBlur("ciArtifactMb", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`ciArtifactDays-${settings.ciArtifactDays || ""}`}
          name="ciArtifactDays"
          data-setting-id="ciArtifactDays"
          label="CI artifacts age (days)"
          helperText="how long a run's artifacts are kept. Shorter than the job cache's age on purpose: a cache entry a month old still saves a re-run, while an artifact is evidence about a failure somebody is investigating now. Blank uses grain's default."
          type="number"
          inputProps={{ min: 0, step: 1 }}
          defaultValue={
            settings.ciArtifactDays ? String(settings.ciArtifactDays) : ""
          }
          placeholder={
            settings.ciArtifactDaysDefault
              ? String(settings.ciArtifactDaysDefault)
              : undefined
          }
          onBlur={onIntBlur("ciArtifactDays", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`ciArtifactRunMb-${settings.ciArtifactRunMb || ""}`}
          name="ciArtifactRunMb"
          data-setting-id="ciArtifactRunMb"
          label="CI artifacts per run (MiB)"
          helperText="ceiling on what one run's artifacts may come to: one push of a workflow whose dozen jobs all fail and all keep a trace is the shape this bounds. Blank uses grain's default."
          type="number"
          inputProps={{ min: 0, step: 1 }}
          defaultValue={
            settings.ciArtifactRunMb ? String(settings.ciArtifactRunMb) : ""
          }
          placeholder={
            settings.ciArtifactRunMbDefault
              ? String(settings.ciArtifactRunMbDefault)
              : undefined
          }
          onBlur={onIntBlur("ciArtifactRunMb", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`ciArtifactFileMb-${settings.ciArtifactFileMb || ""}`}
          name="ciArtifactFileMb"
          data-setting-id="ciArtifactFileMb"
          label="Largest CI artifact (MiB)"
          helperText="largest single artifact kept, and the number a job's sandbox is told before it sends anything -- one past it is refused there, so it is never copied out, and the run records how big it was. Blank uses grain's default."
          type="number"
          inputProps={{ min: 0, step: 1 }}
          defaultValue={
            settings.ciArtifactFileMb ? String(settings.ciArtifactFileMb) : ""
          }
          placeholder={
            settings.ciArtifactFileMbDefault
              ? String(settings.ciArtifactFileMbDefault)
              : undefined
          }
          onBlur={onIntBlur("ciArtifactFileMb", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
      </SettingsCard>
    </>
  );
}
