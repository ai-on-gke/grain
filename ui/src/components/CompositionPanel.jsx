import { useEffect, useState } from "react";
import { Box, Chip, Divider, Stack, Typography } from "@mui/material";
import api from "../api.js";
import GrantChip from "./GrantChip.jsx";

// CompositionPanel shows what a schedule or a suite will actually file,
// before it files it: every task of the next firing (or of one pass of
// the run about to be started), each field's value, and which
// declaration decided it.
//
// A firing combines two or three declarations -- a schedule, the
// template it fires, the suite run that owns the batch -- and which one
// wins differs by field: content from the template, placement from a
// bound template or otherwise from whatever fired it, approval and
// auto-merge from the suite when a suite is running it (pkg/model's own
// compose.go states the whole rule). No form on this page can show that,
// because it is not a property of the form: it is a property of the
// combination. So this panel asks the server for the combination itself
// (GET /api/schedules/{id}/composition, GET /api/suites/{id}/composition),
// which is resolved by the same code the firing runs.
//
// path is the composition endpoint to read, already carrying whatever
// query the caller wants (a suite run's chosen repo and branch). Null
// means "nothing to compose yet" -- a run form with no suite picked --
// and renders nothing at all rather than an empty frame.
//
// body, given, makes this a preview of a *draft* instead: the form's own
// CreateScheduleRequest/CreateSuiteRequest, POSTed to the draft endpoint
// (POST /api/schedules/composition, POST /api/suites/composition), which
// composes it and writes nothing. That is what lets the panel follow the
// edits rather than the saved row -- a brand new schedule has no id to
// ask about, and an edited one's stored fields are exactly what somebody
// is in the middle of changing. Recomposed whenever the body changes,
// debounced (DRAFT_DEBOUNCE_MS) so a typed title costs one request and
// not one per keystroke.

// DRAFT_DEBOUNCE_MS is how long the form has to stand still before the
// draft it describes is composed again. Long enough that typing a title
// is one request rather than one per letter, short enough that it still
// reads as the panel keeping up with the form.
const DRAFT_DEBOUNCE_MS = 300;

// LABELS is the field vocabulary the API returns (ui.ComposedField.Name)
// as a human reads it.
const LABELS = {
  title: "Title",
  body: "Description",
  repo: "Repo",
  base: "Branch",
  reads: "Reads",
  writes: "Also writes",
  capabilities: "Capabilities",
  customFields: "Fields",
  network: "Network",
  nothingNeeded: "Nothing needed",
  model: "Model",
  autoMerge: "Auto-merge",
  approval: "Approval",
};

// EMPTY is what an empty value *means*, per field -- the whole reason
// the API sends the field even when it has no value. "No branch" is the
// target repo's default branch; no network answer is the deployment's
// own; and a suite composed before a repo has been chosen has no repo
// yet rather than none.
const EMPTY = {
  base: "the repo's default branch",
  network: "no override",
  nothingNeeded: "close",
  model: "the deployment's default model",
  repo: "the repo this run targets",
  body: "none",
  reads: "none",
  writes: "none",
  capabilities: "none",
  customFields: "none",
};

// SOURCES is the layer that answered, as a phrase to put beside the
// value (model.CompositionLayer).
const SOURCES = {
  template: "from the template",
  schedule: "from the schedule",
  suite: "from the suite",
  plan: "from the plan",
  candidate: "from the candidate",
  repo: "from the repo",
  deployment: "the deployment decides",
  team: "the team decides",
  filing: "chosen here",
};

// VALUES reads a field's stored value the way the form that set it
// words it, where the two differ: a network of "open" is what the
// network picker calls egress, and "ack" is its "leave for
// acknowledgement".
const VALUES = {
  network: {
    open: "egress (outbound internet only)",
    none: "none (this deployment only)",
  },
  nothingNeeded: { ack: "leave for acknowledgement" },
};

// passes says how many times the tasks below are filed. A suite run
// repeats whole passes, and every pass composes identically, so the list
// is one pass and this line is the rest of the answer.
function passes(composition) {
  if (!composition || composition.kind !== "suite") return null;
  if (composition.mode === "count") {
    return composition.count === 1
      ? "one pass"
      : `${composition.count} passes of these tasks`;
  }
  if (composition.mode === "until_clean") {
    return `repeated until a pass files no pull request and proposes nothing, up to ${composition.maxPasses} passes`;
  }
  return null;
}

function fieldValue(field) {
  if (field.name === "model") {
    if (field.source === "team") {
      if (field.values && field.values.length > 0) {
        return `the deployment's default model (${field.values[0]})`;
      }
      return "the deployment's default model";
    }
    if (field.source === "deployment") {
      if (field.values && field.values.length > 0) {
        return `the deployment's default model (${field.values[0]})`;
      }
      return "the deployment's default model";
    }
  }
  // A grant is a chip like it is everywhere else (GrantChip): a stored
  // server grant is a long <ref>=<json> string.
  if (field.name === "capabilities" && field.values?.length > 0) {
    return (
      <Box
        component="span"
        sx={{ display: "inline-flex", flexWrap: "wrap", gap: 0.5 }}
      >
        {field.values.map((v) => (
          <GrantChip key={v} grant={v} component="span" />
        ))}
      </Box>
    );
  }
  if (field.values && field.values.length > 0) {
    const words = VALUES[field.name] || {};
    return field.values.map((v) => words[v] || v).join(", ");
  }
  // A suite's tasks take the branch of the run that files them -- a run
  // always names one -- so an unset branch the suite answers for is that
  // branch, not the repo's default.
  if (field.name === "base" && field.source === "suite") {
    return "the branch this run targets";
  }
  // One task filed straight from a template with no repo chosen goes
  // to the deployment's default target repo (Client.CreateTask); there
  // is no run for it to take one from.
  if (field.name === "repo" && field.source === "filing") {
    return "the deployment's default repo";
  }
  return EMPTY[field.name] || "none";
}

// fieldGroups splits a field whose values came from more than one layer
// (e.g. a schedule's own capabilities plus the target repo's grain-ci
// grant) into one { values, source } entry per layer, in order. When a
// single layer decided the whole field, it returns one group so the row
// renders with a single value string and a single source chip.
function fieldGroups(field) {
  const values = field.values || [];
  const sources = field.sources || [];
  if (values.length <= 1 || sources.length !== values.length) {
    return [{ values, source: field.source }];
  }
  const groups = [];
  for (let i = 0; i < values.length; i++) {
    const source = sources[i] || field.source;
    const last = groups[groups.length - 1];
    if (last && last.source === source) {
      last.values.push(values[i]);
    } else {
      groups.push({ values: [values[i]], source });
    }
  }
  return groups;
}

function ComposedTaskCard({ task, index, total }) {
  return (
    <Box sx={{ mt: 1.5 }}>
      {/* Headed by the template each task comes from rather than by its
          title: the title is a composed field like any other, listed
          below with the layer that decided it, and heading the card with
          it would print it twice. A schedule firing no template says so
          instead -- its content is its own. */}
      <Typography variant="subtitle2" component="h4" sx={{ m: 0 }}>
        {total > 1 ? `${index + 1}. ` : ""}
        {task.templateName || "This schedule's own content"}
      </Typography>
      <Box component="dl" sx={{ m: 0, mt: 0.5 }}>
        {(task.fields || []).map((field) => {
          const groups = fieldGroups(field);
          return (
            <Stack
              key={field.name}
              direction="row"
              spacing={1}
              alignItems="baseline"
              sx={{ py: 0.25 }}
            >
              <Typography
                component="dt"
                variant="caption"
                color="text.secondary"
                sx={{ minWidth: 110 }}
              >
                {LABELS[field.name] || field.name}
              </Typography>
              {groups.length === 1 ? (
                <>
                  <Typography
                    component="dd"
                    variant="body2"
                    sx={{ m: 0, flex: 1, wordBreak: "break-word" }}
                  >
                    {fieldValue({ ...field, source: groups[0].source })}
                  </Typography>
                  <Chip
                    size="small"
                    variant="outlined"
                    label={SOURCES[groups[0].source] || groups[0].source}
                  />
                </>
              ) : (
                <Stack component="dd" spacing={0.5} sx={{ m: 0, flex: 1 }}>
                  {groups.map((group) => (
                    <Stack
                      key={`${group.source}-${group.values.join(",")}`}
                      direction="row"
                      spacing={1}
                      alignItems="baseline"
                    >
                      <Typography
                        variant="body2"
                        sx={{ m: 0, flex: 1, wordBreak: "break-word" }}
                      >
                        {fieldValue({
                          ...field,
                          values: group.values,
                          source: group.source,
                        })}
                      </Typography>
                      <Chip
                        size="small"
                        variant="outlined"
                        label={SOURCES[group.source] || group.source}
                      />
                    </Stack>
                  ))}
                </Stack>
              )}
            </Stack>
          );
        })}
      </Box>
    </Box>
  );
}

export default function CompositionPanel({
  path,
  body,
  heading = "What this files",
  note,
}) {
  const [composition, setComposition] = useState(null);
  const [error, setError] = useState(null);
  // The draft itself is what this effect depends on, not the object
  // holding it: callers build the request body fresh on every render, so
  // its identity changes even when nothing anybody typed did, and
  // depending on the identity would recompose on every keystroke
  // anywhere in the overlay.
  const draft = body ? JSON.stringify(body) : null;

  useEffect(() => {
    if (!path) return undefined;
    let cancelled = false;
    setError(null);
    setComposition(null);
    const compose = () => {
      // Promise.resolve around the call, not because api can return
      // anything else -- it cannot -- but because this is the one fetch
      // on the page nobody asked for, and a panel that is only ever
      // supplementary must not be able to take the overlay it sits in
      // down with it.
      Promise.resolve(
        draft === null ? api(path) : api(path, { method: "POST", body: draft }),
      )
        .then((got) => {
          if (!cancelled) setComposition(got);
        })
        .catch((err) => {
          // Shown here rather than raised to the page's own error
          // banner: this is a preview nobody asked for by pressing
          // anything, and a template deleted out from under a suite --
          // or a half-filled form the server cannot resolve yet -- must
          // not look like the edit somebody is in the middle of having
          // failed.
          if (!cancelled) {
            setComposition(null);
            setError(err.message);
          }
        });
    };
    // A saved row is asked about once and immediately; a draft waits for
    // the form to stand still first.
    if (draft === null) {
      compose();
      return () => {
        cancelled = true;
      };
    }
    const timer = setTimeout(compose, DRAFT_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [path, draft]);

  if (!path) return null;
  const cadence = passes(composition);
  // Defensive about the shape as well as about the wait: this renders
  // whatever the endpoint said, whether or not it looks like a
  // composition.
  const tasks = (composition && composition.tasks) || [];
  // Nothing to say yet -- the first answer has not arrived, or it held
  // no tasks -- is nothing on the screen, not an empty frame that
  // appears and then fills in.
  if (!error && tasks.length === 0) return null;
  return (
    <Box id="composition-preview" sx={{ mt: 3 }}>
      <Divider sx={{ mb: 1.5 }} />
      <Typography variant="subtitle1" component="h3" sx={{ m: 0 }}>
        {heading}
      </Typography>
      {note ? (
        <Typography variant="caption" color="text.secondary">
          {note}
        </Typography>
      ) : null}
      {error ? (
        <Typography variant="body2" color="error" sx={{ mt: 1 }}>
          {error}
        </Typography>
      ) : null}
      {tasks.length > 0 ? (
        <>
          {cadence ? (
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ display: "block" }}
            >
              {cadence}
            </Typography>
          ) : null}
          {tasks.map((task, i) => (
            <ComposedTaskCard
              key={`${task.templateId || "inline"}-${i}`}
              task={task}
              index={i}
              total={tasks.length}
            />
          ))}
        </>
      ) : null}
    </Box>
  );
}
