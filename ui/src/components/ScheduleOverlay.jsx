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

import { isApiGrant, withoutApiRows } from "../apiGrants.js";
import GrantsPicker from "./GrantsPicker.jsx";
import { localGrantHandlers } from "./capabilityMethods.js";
import { isCapabilityRef } from "../capabilityArgs.js";
import { useState } from "react";
import {
  Box,
  Button,
  Checkbox,
  Chip,
  FormControl,
  FormControlLabel,
  FormHelperText,
  InputLabel,
  ListItemText,
  MenuItem,
  Select,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";
import { useHeldIds } from "../useHeldIds.js";
import CompositionPanel from "./CompositionPanel.jsx";
import { CustomFieldInputs, initialFieldValues } from "./CustomFields.jsx";
import { useTimeZone } from "../TimeZoneContext.jsx";
import ModelPicker from "./ModelPicker.jsx";
import {
  browserTimeZone,
  formatDateTime,
  timeZoneOptions,
  zoneAbbreviation,
} from "../time.js";
import { describeSkip, describeSuppression } from "../scheduleSuppression.js";
import ItemGlyph, { GlyphLabel } from "./ItemGlyph.jsx";
import Overlay, { DISCARD_PROMPT } from "./Overlay.jsx";
import { StaleEditNotice, isStaleEdit, withReload } from "./StaleEdit.jsx";
import { DeclaredFields, DeclaredNotice } from "./Declared.jsx";
import ReadOnlyReposField from "./ReadOnlyReposField.jsx";
import BranchField from "./BranchField.jsx";
import RepoField from "./RepoField.jsx";
import WriteReposField from "./WriteReposField.jsx";
import LastWorkflowRun from "./LastWorkflowRun.jsx";
import { useUnsavedEdits } from "../unsavedEdits.js";
import { templateLabeler } from "../templateLabels.js";
import {
  configuredCapabilities,
  LEGACY_CAPABILITIES_HINT,
  NO_CAPABILITIES_HINT,
  defaultRepoChoice,
  targetableRepos,
} from "../state.js";

const WEEKDAYS = [
  "sunday",
  "monday",
  "tuesday",
  "wednesday",
  "thursday",
  "friday",
  "saturday",
];

// The two cadences counted from the last firing rather than read off a
// clock, so they take no time of day. Minutes have a floor
// (model.MinEveryNMinutes) so nobody schedules a sandbox every minute.
const INTERVAL_KINDS = ["everyNHours", "everyNMinutes"];
const MIN_EVERY_N_MINUTES = 10;

function capitalize(s) {
  return s[0].toUpperCase() + s.slice(1);
}

// RecurrenceFields is the "every N hours (or minutes), or at a fixed
// time daily/weekly/monthly" picker bwsalmon/agents#464 asks for. Kind and Weekday
// are lifted into React state (rather than left as plain form fields
// FormData could read) because Kind decides which of the other inputs
// even render, and MUI's Select needs a controlled value either way --
// ScheduleOverlay's submit handler reads them off the same state rather
// than off the form.
function RecurrenceFields({
  defaultValue,
  kind,
  setKind,
  weekday,
  setWeekday,
  overlap,
  setOverlap,
  timeZone,
  setTimeZone,
}) {
  // The time typed here is read on the schedule's explicit time zone,
  // defaulting to UTC when unset. Named for the zone itself with
  // its current abbreviation beside it ("America/Los_Angeles, PDT"),
  // unless the abbreviation is the zone's own name again: UTC's is
  // "UTC", and the default label read "Time (UTC, UTC)".
  const effectiveZone = timeZone || "UTC";
  const abbreviation = zoneAbbreviation(effectiveZone);
  const timeLabel = `Time (${effectiveZone}${abbreviation && abbreviation !== effectiveZone ? `, ${abbreviation}` : ""})`;
  const browserZone = browserTimeZone();
  return (
    <Box sx={{ mt: 1 }}>
      <FormControl fullWidth margin="normal" size="small">
        <InputLabel id="recurrence-kind-label">Repeat</InputLabel>
        <Select
          labelId="recurrence-kind-label"
          label="Repeat"
          value={kind}
          onChange={(e) => setKind(e.target.value)}
        >
          <MenuItem value="everyNHours">Every N hours</MenuItem>
          <MenuItem value="everyNMinutes">Every N minutes</MenuItem>
          <MenuItem value="daily">Daily, at a time</MenuItem>
          <MenuItem value="weekly">Weekly, on a day and time</MenuItem>
          <MenuItem value="monthly">Monthly, on a day and time</MenuItem>
        </Select>
      </FormControl>
      {kind === "everyNHours" && (
        <TextField
          name="everyNHours"
          label="Every N hours"
          type="number"
          defaultValue={defaultValue?.everyNHours || 24}
          inputProps={{ min: 1, max: 8784 }}
          required
          fullWidth
          margin="dense"
        />
      )}
      {kind === "everyNMinutes" && (
        <TextField
          name="everyNMinutes"
          label="Every N minutes"
          type="number"
          defaultValue={defaultValue?.everyNMinutes || 30}
          inputProps={{ min: MIN_EVERY_N_MINUTES, max: 527040 }}
          required
          fullWidth
          margin="dense"
          helperText={`At least ${MIN_EVERY_N_MINUTES}; counted from the last firing`}
        />
      )}
      {!INTERVAL_KINDS.includes(kind) && (
        <TextField
          name="timeOfDay"
          label={timeLabel}
          type="time"
          defaultValue={defaultValue?.timeOfDay || "09:00"}
          required
          fullWidth
          margin="dense"
          InputLabelProps={{ shrink: true }}
        />
      )}
      {kind === "weekly" && (
        <FormControl fullWidth margin="dense" size="small">
          <InputLabel id="recurrence-weekday-label">Day of week</InputLabel>
          <Select
            labelId="recurrence-weekday-label"
            label="Day of week"
            value={weekday}
            onChange={(e) => setWeekday(e.target.value)}
          >
            {WEEKDAYS.map((d) => (
              <MenuItem key={d} value={d}>
                {capitalize(d)}
              </MenuItem>
            ))}
          </Select>
        </FormControl>
      )}
      {kind === "monthly" && (
        <TextField
          name="dayOfMonth"
          label="Day of month"
          type="number"
          defaultValue={defaultValue?.dayOfMonth || 1}
          inputProps={{ min: 1, max: 31 }}
          required
          fullWidth
          margin="dense"
          helperText="1-31; a shorter month fires on its last day"
        />
      )}
      {setTimeZone && (
        <FormControl fullWidth margin="dense" size="small">
          <InputLabel id="schedule-timezone-label" shrink>
            Time zone
          </InputLabel>
          <Select
            labelId="schedule-timezone-label"
            label="Time zone"
            name="timeZone"
            value={timeZone || ""}
            onChange={(e) => setTimeZone(e.target.value)}
            displayEmpty
            notched
          >
            <MenuItem value="">UTC (default)</MenuItem>
            {timeZoneOptions(timeZone).map((z) => (
              <MenuItem key={z} value={z}>
                {z}
                {z === browserZone ? " (this browser)" : ""}
              </MenuItem>
            ))}
          </Select>
          <FormHelperText>
            The timezone this schedule fires in. Defaults to UTC if left unset.
          </FormHelperText>
        </FormControl>
      )}
      {/* What to do when the cadence comes round again and the last
          firing is still going (model.OverlapPolicy). It sits with the
          cadence because it is the other half of the same question --
          how often this runs -- and it is a Select rather than a
          checkbox so both answers are spelled out: "skip" is a real
          choice a human is making, not the absence of one.
          Offered only to a caller that has somewhere to put the answer. */}
      {setOverlap && (
        <FormControl fullWidth margin="dense" size="small">
          <InputLabel id="schedule-overlap-label">
            If the last run has not finished
          </InputLabel>
          <Select
            labelId="schedule-overlap-label"
            label="If the last run has not finished"
            value={overlap}
            onChange={(e) => setOverlap(e.target.value)}
          >
            <MenuItem value="skip">Skip this run</MenuItem>
            <MenuItem value="allow">Start it anyway</MenuItem>
          </Select>
          <FormHelperText>
            {overlap === "allow"
              ? "Firings can pile up: this schedule can have several unfinished at once."
              : "The cadence waits: the skipped run happens as soon as the last one finishes."}
          </FormHelperText>
        </FormControl>
      )}
    </Box>
  );
}

// ScheduleOverlay is the schedules page's own sub page (bwsalmon/
// agents#547): the "+" on SchedulesList opens this blank, and clicking
// an existing row opens the same overlay pre-filled with that schedule's
// fields -- TemplateOverlay's own split (bwsalmon/agents#545) applied to
// schedules, so a schedule stops being the one list on its page with a
// full form and its own row of buttons crowding the row itself. Pause/
// Resume and Delete move in here too, alongside the editable fields, the
// same place DetailOverlay keeps a task's own Close/Reopen next to its
// editable fields rather than on TaskList's row.
//
// dependsOn and "approved" have no place here, unlike NewTaskOverlay --
// CreateScheduleRequest's own doc comment explains why: a one-shot
// dependency link makes no sense against a task this schedule refiles
// indefinitely, and a firing always lands already approved by design.
//
// templateId (bwsalmon/agents#516) is its own bit of state, not a plain
// form field, because it decides whether the title/description/reads/
// writes/capabilities/fields inputs below even render: picking a
// template hands that
// content over entirely (ui.CreateScheduleRequest's own doc comment on
// why a template and per-field overrides do not mix), so this form hides
// them rather than showing fields the request would silently ignore.
// Repo and base branch normally are not among them -- a template
// usually carries no target of its own -- so those two render whether a
// template is selected or not. The exception is a template bound to a
// repo (grain/task-285): that binding is what its firings target, over
// anything this form could say, so the two fields give way to a line
// naming where the schedule will actually fire.
//
// "Fires" (fires/suiteId) is the same idea one step out: a schedule can
// run a whole suite on its cadence instead of filing one task, in
// which case the suite decides everything a template would have and
// more, so every content field here gives way to a single suite picker.
// It is offered on a new schedule only -- what a schedule fires is fixed
// when it is created (ui.UpdateScheduleRequest's own doc comment on why),
// though which suite a suite-backed one runs stays editable.
function ScheduleOverlay({
  schedule,
  repoOptions,
  tasks = [],
  templates = [],
  suites = [],
  config,
  onClose,
  onOpenTask,
  onSaved,
  showError,
  onReload,
}) {
  const isNew = !schedule;
  // A repository's own schedule (Declared.jsx): shown, never saved,
  // paused or deleted from here -- pausing is an edit to it as far as the
  // server is concerned (UpdateSchedule). Run now is not, and stays.
  const declaredIn = schedule?.declaredIn || null;
  // The deployment's own clock, for the one instant this pane prints:
  // when grain stops waiting for the review holding a suppressed
  // schedule. RecurrenceFields reads it again for the time field it
  // labels, which is its own concern.
  const zone = useTimeZone();
  const [capabilities, setCapabilities] = useState(
    schedule?.capabilities || [],
  );
  // What this deployment has configured, plus whatever this schedule
  // already names -- configuredCapabilities (state.js) has why an
  // unconfigured one is left off and a chosen one is not.
  // Capability server grants, with their with: arguments, and the agent
  // API's grants (apiGrants.js) share the list and are edited together
  // by GrantsPicker rather than the legacy checklist.
  const capabilityIds = capabilities.filter(
    (id) => !isCapabilityRef(id) && !isApiGrant(id),
  );
  const apiIds = capabilities.filter(isApiGrant);
  const capabilityRefs = capabilities.filter(isCapabilityRef);
  const heldCapabilityIds = useHeldIds(capabilityIds);
  const capabilityOptions = withoutApiRows(
    configuredCapabilities(config?.capabilities, heldCapabilityIds),
  );
  const methodGrants = localGrantHandlers(capabilityRefs, (refs) =>
    setCapabilities([...capabilityIds, ...apiIds, ...refs]),
  );
  // reads is state for the reason capabilities is: its picker
  // (ReadOnlyReposField) is a search box, not a form field submit could
  // read the answer off.
  const [reads, setReads] = useState(schedule?.reads || []);
  const targetOptions = targetableRepos(config, tasks);
  const [timeZone, setTimeZone] = useState(schedule?.timeZone || "");
  // writes is the extra writeable repos every task this schedule files
  // carries ({repo, base} objects, ui.Schedule.Writes) -- state for
  // reads' own reason, with a branch box per row that belongs beside the
  // repo it names.
  const [writes, setWrites] = useState(schedule?.writes || []);
  // What every task this schedule files says about this deployment's
  // own fields (grain/task-105) -- state for the same reason reads is,
  // and seeded from the schedule's saved values falling back to each
  // declaration's default, exactly as the new-task form seeds its own.
  const [customFields, setCustomFields] = useState(() =>
    initialFieldValues(config, schedule?.customFields),
  );
  const [kind, setKind] = useState(schedule?.recurrence?.kind || "everyNHours");
  const [weekday, setWeekday] = useState(
    schedule?.recurrence?.weekday || "monday",
  );
  // "skip" for a new schedule, and for one stored before the API had
  // this field to send -- the same default the API itself applies
  // (model.OverlapPolicy's zero value).
  const [overlap, setOverlap] = useState(schedule?.overlap || "skip");
  // repo tracks the target as it is picked, for the branch box under it
  // and for the preview at the foot of the form: the suggestions the
  // branch box offers are that repo's own (BranchField.jsx), and the
  // preview is composed against whatever is in these boxes now.
  //
  // A required RepoField omits its blank option, so its native select
  // visibly starts on the first known repo. Start this state on the same
  // answer: submit and the draft preview read state, and leaving it empty
  // made Add schedule send no repo until somebody reselected the option
  // already showing in the field.
  // It is the deployment's default target when the field offers it
  // (defaultRepoChoice), and the field's own first option otherwise.
  const initialRepo =
    schedule?.repo || (isNew ? defaultRepoChoice(config, targetOptions) : "");
  const [repo, setRepo] = useState(initialRepo);
  // The rest of what a firing carries, tracked as it is typed for the
  // same reason -- the preview recomposes from it, and submit reads its
  // payload off the same state rather than off the form, so what is
  // shown below is the body that gets filed. The inputs stay
  // uncontrolled (defaultValue plus onChange): these only follow the
  // boxes, they do not drive them.
  const [base, setBase] = useState(schedule?.base || "");
  const [title, setTitle] = useState(schedule?.title || "");
  const [description, setDescription] = useState(schedule?.description || "");
  const [autoMerge, setAutoMerge] = useState(!!schedule?.autoMerge);
  const [networkAccess, setNetworkAccess] = useState(
    schedule?.networkAccess || "",
  );
  // "" on the wire is close (model.Schedule.NothingNeededAction).
  const [nothingNeededAction, setNothingNeededAction] = useState(
    schedule?.nothingNeededAction || "close",
  );
  const [templateId, setTemplateId] = useState(schedule?.templateId || "");
  // The model every firing asks for (ui.CreateScheduleRequest.Model):
  // this schedule's own choice, which beats its template's. Not content,
  // so it is sent whether or not a template is attached -- and never for
  // a suite, which the API refuses.
  const [chosenModel, setChosenModel] = useState(schedule?.model || "");
  const [fires, setFires] = useState(schedule?.suiteId ? "suite" : "task");
  const [suiteId, setSuiteId] = useState(schedule?.suiteId || "");
  const firesSuite = fires === "suite";
  // One of grain's own standing jobs (ui.Schedule.System): the row grain
  // seeds for itself, whose content is a copy of a template that lives
  // in grain's code and is re-resolved every time it fires. Three
  // things follow, and this form draws all three: what it fires is not
  // in `templates` and cannot be repointed, it targets no repo at all
  // (so the required repo field below would refuse to submit a cadence
  // edit that is otherwise perfectly allowed), and the title and
  // description this row carries are a cache rather than the prompt --
  // which is worth saying out loud, because the alternative is somebody
  // editing them, here or in the state repository's dump, and believing
  // they have changed what the job is told.
  const isSystem = !!schedule?.system;
  // The selected template, when it is one bound to a repo of its own --
  // undefined for no template, an unbound one, or a suite schedule. Its
  // binding replaces this form's own repo and base fields.
  const boundTemplate = firesSuite
    ? undefined
    : templates.find((t) => t.id === templateId && t.repo);
  // Names alone where they are unique, more where they are not
  // (templateLabels.js).
  const templateLabel = templateLabeler(templates);
  // Whether this form is choosing a placement at all: a bound template
  // names the repo and branch itself, and one of grain's own schedules
  // targets nothing, so neither renders the two boxes and neither sends
  // them (the API fills a bound template's in from the binding).
  const placed = !isSystem && !boundTemplate;

  // scheduleRequest is the CreateScheduleRequest this form describes as
  // it stands -- what submit sends, and what the preview at the foot of
  // the form is composed from, built in one place so the two cannot
  // drift apart. The cadence is the caller's to add: it is the one part
  // of a schedule a composition has nothing to say about.
  //
  // The content fields are omitted rather than sent-and-ignored when a
  // template or a suite decides them, and the API now holds us to it: a
  // title, description, auto-merge, reads, writes, capabilities or
  // custom fields sent alongside a templateId or a suiteId is a 400
  // naming the fields (ui.refuseIgnoredScheduleContent), because the
  // template or the suite decides all of them. Resending a value
  // identical to the one in force is still fine, which is what keeps
  // this form's whole-schedule save working.
  //
  // templateId is always sent for a task schedule, even "" --
  // CreateScheduleRequest reads "" as "no template";
  // UpdateScheduleRequest reads a given-at-all templateId (its own doc
  // comment) the same way, "" meaning detach rather than leave alone,
  // which is exactly what re-submitting this form with "None" selected
  // should do. A suite schedule sends suiteId in its place and nothing
  // else: the suite decides the content, the passes, the approval and
  // the auto-merge of everything a firing runs.
  const scheduleRequest = () => {
    const req = {
      ...(firesSuite ? { suiteId } : { templateId }),
      repo: placed ? repo : "",
      base: placed ? base : "",
    };
    // Only when it changed, for the reason TemplateOverlay sends it so:
    // absent leaves the saved model alone (ui.UpdateScheduleRequest.Model).
    if (!firesSuite && chosenModel !== (schedule?.model || "")) {
      req.model = chosenModel;
    }
    if (!firesSuite && templateId === "") {
      req.title = title;
      req.description = description;
      req.autoMerge = autoMerge;
      req.networkAccess = networkAccess;
      req.nothingNeededAction = nothingNeededAction;
      req.reads = reads;
      req.writes = writes;
      req.capabilities = capabilities;
      req.customFields = customFields;
    }
    return req;
  };

  // Whether leaving now would lose an edit: the form's own fields (the
  // cadence's number and time boxes are read off it, not state) plus
  // everything submit takes from state instead.
  const { formRef, isDirty } = useUnsavedEdits({
    request: scheduleRequest(),
    kind,
    weekday,
    overlap,
    timeZone,
    fires,
  });

  const submit = async (evt) => {
    evt.preventDefault();
    const form = evt.target;
    const data = new FormData(form);

    const recurrence = { kind };
    if (kind === "everyNHours") {
      recurrence.everyNHours = Number(data.get("everyNHours"));
    } else if (kind === "everyNMinutes") {
      recurrence.everyNMinutes = Number(data.get("everyNMinutes"));
    } else {
      recurrence.timeOfDay = data.get("timeOfDay");
      if (kind === "weekly") recurrence.weekday = weekday;
      if (kind === "monthly")
        recurrence.dayOfMonth = Number(data.get("dayOfMonth"));
    }

    // Caught here rather than left to the API, which would read a blank
    // suiteId as "this schedule files a task" and complain about a
    // missing title instead of about the picker actually left empty.
    if (firesSuite && suiteId === "") {
      showError(new Error("choose a suite for this schedule to run"));
      return;
    }

    // The body the panel at the foot of this form has been previewing
    // all along (scheduleRequest), plus the cadence, which is the one
    // part of a schedule a composition has nothing to say about.
    const payload = {
      ...scheduleRequest(),
      recurrence,
      overlap,
      ...(timeZone
        ? { timeZone }
        : !isNew && schedule?.timeZone
          ? { timeZone: "" }
          : {}),
    };

    try {
      if (isNew) {
        await api("/api/schedules", {
          method: "POST",
          body: JSON.stringify(payload),
        });
      } else {
        // The revision this pane was opened on, so a save from a copy
        // somebody has since saved over is refused rather than putting
        // their change back (StaleEdit.jsx).
        await api(`/api/schedules/${schedule.id}`, {
          method: "PATCH",
          body: JSON.stringify({ ...payload, revision: openedRevision }),
        });
      }
      await onSaved();
      onClose();
    } catch (err) {
      if (isStaleEdit(err)) setStale(err);
      else showError(err);
    }
  };

  // Pause, Resume and Run now each close the pane once they are done,
  // and send nothing of the form -- so an edit still in it would go with
  // the pane, past the question Escape and "← Back" ask (found in the
  // 2026-10-07 exploratory pass). Asked first, then, in the same words.
  // The refusal of a save made from a copy somebody else has since
  // saved over, shown in the pane until it is reloaded (StaleEdit.jsx).
  const [stale, setStale] = useState(null);
  // The revision this pane was opened on, held rather than read off the
  // prop at save time: the page polls its list, so the prop moves on to
  // somebody else's save while this form still holds what it was seeded
  // with -- exactly the copy the check is about.
  const [openedRevision] = useState(schedule?.revision);

  const toggleEnabled = async () => {
    if (isDirty() && !confirm(DISCARD_PROMPT)) return;
    try {
      await api(`/api/schedules/${schedule.id}`, {
        method: "PATCH",
        body: JSON.stringify({ enabled: !schedule.enabled }),
      });
      await onSaved();
      onClose();
    } catch (err) {
      showError(err);
    }
  };

  // Run now fires this schedule this instant, on top of its cadence
  // (grain/task-227) -- the same firing the cadence makes, leaving
  // nextRunAt exactly where it is, so the occurrence already scheduled
  // still happens when it was going to (ui.Client.RunScheduleNow).
  //
  // It fires the schedule as *saved*, not as this form currently stands,
  // the same way Pause above sends nothing but `enabled` and leaves an
  // unsaved edit unsaved -- which is why the confirm says what it is
  // about to run rather than only asking whether to.
  //
  // Confirmed because it is not a preview: a firing dispatches an agent
  // against a real repo, and a suite-backed schedule dispatches a whole
  // run of them. The refusal a schedule set to skip gives while its last
  // firing is still open comes back as a plain error message
  // (scheduleBusyError, a 409), which showError puts in front of
  // whoever pressed it.
  const runNow = async () => {
    // A schedule a workflow's own cron declares fires neither of the
    // other two: grain starts the workflow's CI run and files the task
    // that reviews it once that run has finished, so what is promised
    // here is the run and not a task id (model.ScheduleFiring.WorkflowRun).
    const what = schedule.suiteId
      ? `a run of the suite "${schedule.suiteName || schedule.suiteId}"`
      : schedule.templateId?.startsWith("system:workflow-review:")
        ? `a CI run of the workflow it was declared in, to be reviewed when it finishes`
        : schedule.templateId
          ? // Titled by the template, not by this schedule: a declared
            // schedule's own name ("Nightly Repo Maintenance Sweep")
            // is not the title of the task its template files
            // ("Declared Nightly Maintenance"), and the confirm named
            // the one that was never going to appear.
            `a task from the template "${schedule.templateName || schedule.templateId}"`
          : `the task "${schedule.title}"`;
    // One question rather than two: the run's own, with the edit it
    // would throw away named in it.
    if (
      !confirm(
        `Run "${schedule.title}" now? It starts ${what} straight away; ` +
          `the schedule's own next run is unchanged.` +
          (isDirty()
            ? ` Your unsaved changes here are discarded, not run.`
            : ""),
      )
    )
      return;
    try {
      await api(`/api/schedules/${schedule.id}/run`, { method: "POST" });
      await onSaved();
      onClose();
    } catch (err) {
      showError(err);
    }
  };

  const remove = async () => {
    if (
      !confirm(
        `Delete the schedule "${schedule.title}"? ${
          schedule.suiteId
            ? "Suite runs it already started are"
            : "Tasks it already filed are"
        } not affected.`,
      )
    )
      return;
    try {
      await api(`/api/schedules/${schedule.id}`, { method: "DELETE" });
      await onSaved();
      onClose();
    } catch (err) {
      showError(err);
    }
  };

  return (
    <Overlay
      onClose={onClose}
      pane
      label={isNew ? "New schedule" : declaredIn ? "Schedule" : "Edit schedule"}
      backLabel="Schedules"
      isDirty={declaredIn ? null : isDirty}
    >
      {/* The hourglass heads the pane the way it heads the page this
          one opened over (ListPrimitives' own `icon`, ItemGlyph.jsx) --
          decoration ahead of the heading, so "Edit schedule" is still
          the whole of its accessible name. */}
      <Stack direction="row" alignItems="center" spacing={1} sx={{ mb: 1 }}>
        <ItemGlyph kind="schedules" size={20} />
        <Typography variant="h6" component="h2" sx={{ m: 0 }}>
          {isNew ? "New schedule" : declaredIn ? "Schedule" : "Edit schedule"}
        </Typography>
      </Stack>
      <DeclaredNotice
        declaredIn={declaredIn}
        noun="schedule"
        also="Run now still fires it once."
      />
      {/* Said here as well as on the row, because this is the pane
          somebody opens *after* noticing the row -- and because both
          things it points at live here: the overlap setting that decides
          to skip, and the "Run now" button, which is refused for the
          same reason while this holds (ui.scheduleBusyError, a 409).
          Nothing here is stored, so it goes as soon as the task it names
          is closed (ui.Schedule.Suppressed). */}
      {schedule?.suppressed && (
        <Typography
          variant="body2"
          className="schedule-overdue"
          sx={{ mt: 1, mb: 1 }}
        >
          This schedule has come due and is not firing:{" "}
          {describeSuppression(schedule.suppressed)}
          {schedule.suppressed.taskId && (
            <>
              {" ("}
              <a
                href={`/tasks/${schedule.suppressed.taskId}`}
                onClick={(e) => {
                  if (
                    e.button !== 0 ||
                    e.metaKey ||
                    e.ctrlKey ||
                    e.shiftKey ||
                    e.altKey
                  ) {
                    return;
                  }
                  if (isDirty() && !window.confirm(DISCARD_PROMPT)) {
                    e.preventDefault();
                    return;
                  }
                  if (onOpenTask) {
                    e.preventDefault();
                    onClose?.();
                    onOpenTask(schedule.suppressed.taskId);
                  }
                }}
              >
                task {schedule.suppressed.taskId}
              </a>
              {")"}
            </>
          )}
          . It is set to skip a firing that would overlap the last one, so the
          next run below has not moved: it fires as soon as that finishes.
          {schedule.suppressed.until && (
            <>
              {" "}
              If nothing closes it first, grain stops waiting and fires anyway
              at {formatDateTime(schedule.suppressed.until, zone)}.
            </>
          )}
        </Typography>
      )}
      {/* Every firing that came due and filed nothing, and what became
          of it (ui.Schedule.Skips) -- the history Suppressed above is
          not, since that goes the moment its task closes. */}
      {schedule?.skips?.length > 0 && (
        <div className="schedule-skips">
          <Typography variant="subtitle2" sx={{ mt: 1 }}>
            Skipped firings
          </Typography>
          <ul>
            {schedule.skips.map((k) => (
              <li key={`${k.occurrence}`}>
                <Typography variant="body2" component="span">
                  {describeSkip(k, (t) => formatDateTime(t, zone))}
                </Typography>
              </li>
            ))}
          </ul>
        </div>
      )}
      {/* A schedule that runs a workflow files a review only for a run
          that did not pass (ui.Schedule.LastWorkflowRun), so a green
          night is seen here or nowhere. */}
      {schedule?.lastWorkflowRun && (
        <Typography variant="body2" sx={{ mt: 1, mb: 1 }}>
          <LastWorkflowRun run={schedule.lastWorkflowRun} zone={zone} />.{" "}
          {schedule.workflowReview === "always"
            ? "Every run of this schedule is reviewed."
            : "A run is reviewed only when it does not pass."}
        </Typography>
      )}
      <StaleEditNotice error={stale} onReload={onReload} />
      <form className="pane-form" onSubmit={submit} ref={formRef}>
        <DeclaredFields declaredIn={declaredIn}>
          {isNew && (
            <FormControl fullWidth margin="normal" size="small">
              <InputLabel id="schedule-fires-label">Fires</InputLabel>
              <Select
                labelId="schedule-fires-label"
                label="Fires"
                value={fires}
                onChange={(e) => setFires(e.target.value)}
              >
                {/* Both options carry their kind's own figure
                  (ItemGlyph.jsx): this choice decides which of the two
                  a schedule runs, and until now the two options differed
                  by one word -- then by one word and one figure, while a
                  task had none to draw (grain/task-372). */}
                <MenuItem value="task">
                  <GlyphLabel kind="tasks">A task</GlyphLabel>
                </MenuItem>
                <MenuItem value="suite">
                  <GlyphLabel kind="suites">A suite</GlyphLabel>
                </MenuItem>
              </Select>
            </FormControl>
          )}
          {isSystem ? (
            <Typography
              variant="body2"
              color="text.secondary"
              sx={{ mt: 2, mb: 1 }}
            >
              Fires {schedule.templateName || schedule.templateId} -- one of
              grain&apos;s own jobs. What it does and what it says ship with
              grain and change when grain is upgraded, so the title and
              description stored on this schedule are a copy of them rather than
              what actually runs -- editing that copy, here or in the config
              repository, changes neither. You can change how often it runs,
              what it does when a firing overlaps the last one, and whether it
              runs at all.
            </Typography>
          ) : firesSuite ? (
            <FormControl fullWidth margin="normal" size="small">
              <InputLabel id="schedule-suite-label">Suite</InputLabel>
              <Select
                labelId="schedule-suite-label"
                label="Suite"
                value={suiteId}
                onChange={(e) => setSuiteId(e.target.value)}
              >
                {suites.map((s) => (
                  <MenuItem key={s.id} value={s.id}>
                    <GlyphLabel kind="suites">{s.name}</GlyphLabel>
                  </MenuItem>
                ))}
              </Select>
            </FormControl>
          ) : (
            <FormControl fullWidth margin="normal" size="small">
              <InputLabel id="schedule-template-label">Template</InputLabel>
              <Select
                labelId="schedule-template-label"
                label="Template"
                value={templateId}
                onChange={(e) => setTemplateId(e.target.value)}
              >
                {/* "None" holds the glyph slot open rather than carrying
                  one: it is the absence of a template, and starting its
                  label a figure's width left of every other row's would
                  read as a fifth kind. */}
                <MenuItem value="">
                  <GlyphLabel kind={null}>
                    None -- fill in the fields below
                  </GlyphLabel>
                </MenuItem>
                {templates.map((t) => (
                  <MenuItem key={t.id} value={t.id}>
                    <GlyphLabel kind="templates">{templateLabel(t)}</GlyphLabel>
                  </MenuItem>
                ))}
              </Select>
            </FormControl>
          )}
          {isSystem ? null : boundTemplate ? (
            <Typography
              variant="body2"
              color="text.secondary"
              sx={{ mt: 2, mb: 1 }}
            >
              Fires against {boundTemplate.repo}
              {boundTemplate.base ? ` on ${boundTemplate.base}` : ""} -- the
              selected template is bound to that repo, so this schedule does not
              choose one.
            </Typography>
          ) : (
            // Every repo a firing may change, in one block
            // (grain/task-202): the repo it is filed against first, then
            // any others under it. Those others are the schedule's own
            // only when it has no template -- with one selected they come
            // from the template, along with everything else below -- so
            // they are rendered here on that condition rather than beside
            // the fields the template supplies.
            <Box sx={{ mt: 2, mb: 1 }}>
              <Box component="label" sx={{ display: "block" }}>
                <Typography
                  variant="caption"
                  color="text.secondary"
                  sx={{ display: "block", mb: 0.5 }}
                >
                  Repos{" "}
                  <span className="hint">
                    the first is the one each firing&apos;s own work lands in
                  </span>
                </Typography>
                <RepoField
                  name="repo"
                  options={targetOptions}
                  defaultValue={initialRepo}
                  required
                  onChange={setRepo}
                />
              </Box>
              <BranchField
                name="base"
                label="Base branch"
                defaultValue={schedule?.base || ""}
                helperText={
                  firesSuite
                    ? "required: a suite run stacks its tasks against one branch"
                    : "optional"
                }
                required={firesSuite}
                margin="dense"
                size="medium"
                repo={repo}
                warnMissing="each firing fails until it is cut"
                onChange={setBase}
              />
              {!firesSuite && templateId === "" && (
                <WriteReposField
                  options={repoOptions}
                  value={writes}
                  onChange={setWrites}
                />
              )}
            </Box>
          )}
          {!firesSuite && (
            <ModelPicker
              config={config}
              value={chosenModel}
              onChange={setChosenModel}
              emptyLabel={
                templateId !== "" ? "The template's model" : undefined
              }
              helperText="the model every task this schedule files asks for"
            />
          )}
          {firesSuite ? (
            <Typography
              variant="body2"
              color="text.secondary"
              sx={{ mt: 1, mb: 1 }}
            >
              Every firing starts one run of the selected suite against this
              repo and branch -- the suite decides which tasks run, how many
              passes, and whether they need approval.
            </Typography>
          ) : isSystem ? null : templateId !== "" ? (
            <Typography
              variant="body2"
              color="text.secondary"
              sx={{ mt: 1, mb: 1 }}
            >
              Title, description, reads, other writeable repos, capabilities,
              fields and auto-merge all come from the selected template, and
              stay in sync with it.
            </Typography>
          ) : (
            <>
              <TextField
                name="title"
                label="Title"
                defaultValue={schedule?.title}
                onChange={(e) => setTitle(e.target.value)}
                required
                InputLabelProps={{ required: false }}
                autoComplete="off"
                fullWidth
                margin="normal"
              />
              <TextField
                name="description"
                label="Description"
                defaultValue={schedule?.description}
                onChange={(e) => setDescription(e.target.value)}
                multiline
                rows={4}
                fullWidth
                margin="normal"
              />
              <ReadOnlyReposField
                options={repoOptions}
                value={reads}
                onChange={setReads}
                // The saved target, not what the repo field says as it is
                // typed -- WriteReposField's own reason above: this only
                // decides what the picker offers, and re-rendering the
                // whole form on every keystroke to hide one row of a
                // dropdown is not worth it.
                target={schedule?.repo || ""}
              />
              <FormControlLabel
                control={
                  <Checkbox
                    name="autoMerge"
                    defaultChecked={schedule?.autoMerge}
                    onChange={(e) => setAutoMerge(e.target.checked)}
                  />
                }
                label="Auto-merge once checks pass"
                sx={{ display: "flex", mt: 1 }}
              />
              <TextField
                select
                name="networkAccess"
                label="Network access"
                value={networkAccess}
                onChange={(e) => setNetworkAccess(e.target.value)}
                InputLabelProps={{ shrink: true }}
                SelectProps={{ displayEmpty: true }}
                helperText="fixes the network of every task this schedule files. A task with no network still reaches this deployment's git proxy."
                fullWidth
                margin="normal"
                size="small"
              >
                <MenuItem value="">Deployment default</MenuItem>
                <MenuItem value="open">
                  Egress (outbound internet only)
                </MenuItem>
                <MenuItem value="none">None (this deployment only)</MenuItem>
              </TextField>
              <TextField
                select
                name="nothingNeededAction"
                label="When the agent finds nothing to do"
                value={nothingNeededAction}
                onChange={(e) => setNothingNeededAction(e.target.value)}
                helperText="applies to every task this schedule files"
                fullWidth
                margin="normal"
                size="small"
              >
                <MenuItem value="close">Close</MenuItem>
                <MenuItem value="ack">Leave for acknowledgement</MenuItem>
              </TextField>
              <FormControl fullWidth margin="normal" size="small">
                <InputLabel id="schedule-capabilities-label">
                  Legacy capabilities
                </InputLabel>
                <Select
                  labelId="schedule-capabilities-label"
                  label="Legacy capabilities"
                  multiple
                  value={capabilityIds}
                  onChange={(e) =>
                    setCapabilities([
                      ...e.target.value,
                      ...apiIds,
                      ...capabilityRefs,
                    ])
                  }
                  renderValue={(selected) => (
                    <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.5 }}>
                      {selected.map((id) => {
                        const c = (config?.capabilities || []).find(
                          (cap) => cap.id === id,
                        );
                        return (
                          <Chip key={id} size="small" label={c ? c.name : id} />
                        );
                      })}
                    </Box>
                  )}
                >
                  {capabilityOptions.length === 0 && (
                    <MenuItem disabled>{NO_CAPABILITIES_HINT}</MenuItem>
                  )}
                  {capabilityOptions.map((c) => (
                    <MenuItem key={c.id} value={c.id} title={c.description}>
                      <Checkbox
                        checked={capabilityIds.includes(c.id)}
                        size="small"
                      />
                      <ListItemText primary={c.name} />
                    </MenuItem>
                  ))}
                </Select>
                <FormHelperText>{LEGACY_CAPABILITIES_HINT}</FormHelperText>
              </FormControl>
              <GrantsPicker
                config={config}
                grants={capabilities}
                onApiChange={(next) =>
                  setCapabilities([
                    ...capabilityIds,
                    ...next,
                    ...capabilityRefs,
                  ])
                }
                onSubmit={methodGrants.onSubmit}
                onRemove={methodGrants.onRemove}
                idPrefix="schedule"
                policyRepo={repo}
              />
              {/* Renders nothing on a deployment that declares no fields,
                so this form is unchanged for everybody who has never
                declared one. */}
              <CustomFieldInputs
                config={config}
                values={customFields}
                onChange={(name, value) =>
                  setCustomFields((prev) => ({ ...prev, [name]: value }))
                }
              />
            </>
          )}
          <RecurrenceFields
            defaultValue={schedule?.recurrence}
            kind={kind}
            setKind={setKind}
            weekday={weekday}
            setWeekday={setWeekday}
            overlap={overlap}
            setOverlap={setOverlap}
            timeZone={timeZone}
            setTimeZone={setTimeZone}
          />
        </DeclaredFields>
        {/* Wraps rather than running off a 320px phone, where the two
            clusters together are wider than the pane: Cancel and Save
            keep to the right on whichever line they land. */}
        <Stack
          direction="row"
          justifyContent={isNew ? "flex-end" : "space-between"}
          alignItems="center"
          sx={{ mt: 2, flexWrap: "wrap", gap: 1 }}
        >
          {!isNew && (
            <Stack direction="row" spacing={1}>
              <Button color="error" onClick={remove} disabled={!!declaredIn}>
                Delete
              </Button>
              <Button onClick={toggleEnabled} disabled={!!declaredIn}>
                {schedule.enabled ? "Pause" : "Resume"}
              </Button>
              {/* Offered on a paused schedule too: pausing stops the
                  cadence, and this is a human asking for one run, which
                  is not the cadence. */}
              <Button onClick={runNow}>Run now</Button>
            </Stack>
          )}
          <Stack direction="row" spacing={1} sx={{ ml: "auto" }}>
            <Button onClick={onClose}>{declaredIn ? "Close" : "Cancel"}</Button>
            <Button type="submit" variant="contained" disabled={!!declaredIn}>
              {isNew ? "Add schedule" : "Save"}
            </Button>
          </Stack>
        </Stack>
      </form>
      {/* Composed from the form rather than from the row: the body above
          is POSTed to the draft endpoint, which resolves the template or
          suite it names out of the store and composes it without writing
          anything, so this follows the edits and a schedule that does
          not exist yet has a preview too. The template is resolved on
          every call, so what is shown is today's template and not a
          snapshot of one.

          One of grain's own schedules is the exception: its content is a
          copy of a template that lives in grain's code rather than one
          this form could describe, so it keeps asking about the saved
          row. */}
      {isSystem ? (
        <CompositionPanel
          path={`/api/schedules/${schedule.id}/composition`}
          heading="What this schedule files"
          note="grain's own job -- its content comes from this build"
        />
      ) : (
        <CompositionPanel
          path="/api/schedules/composition"
          body={scheduleRequest()}
          heading="What this schedule files"
          note={isNew ? "as filled in above" : "as edited above, not as saved"}
        />
      )}
    </Overlay>
  );
}

// Reloadable (StaleEdit.jsx), so a save refused as stale can start over
// from the saved copy.
export default withReload(ScheduleOverlay);
