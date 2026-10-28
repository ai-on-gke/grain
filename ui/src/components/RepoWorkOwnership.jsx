import { useCallback, useEffect, useState } from "react";
import {
  Box,
  Button,
  Chip,
  FormControlLabel,
  Stack,
  Switch,
  Typography,
} from "@mui/material";
import api from "../api.js";
import { repoApiRoot } from "../repoApi.js";

// RepoWorkOwnership is the pane for what this deployment says about one
// repository's standing work (model.WorkOwnership, grain/task-486): the
// repository default, and the exception to it on each work item -- where
// a schedule's id is an item and the release is one more item.
//
// It is a reading with buttons rather than a column of checkboxes, and
// that is the whole design. The setting is three-valued on purpose:
// "nobody has said" follows whatever the rule says and moves with it,
// "off" is a decision that survives the default being turned back on,
// and "on" is the decision that survives it being turned off. A
// checkbox has two positions and would have to spend one of them on the
// first state, which is the state very nearly every repository is in --
// so an unticked box would be a claim ("somebody decided no") where the
// truth is silence, and the one difference the type exists to record
// would be the one the pane could not draw.
//
// So every row says both halves, and neither is inferred from the other
// by a reader: what was said, and what that resolves to. The rows are
// this repository's real items -- the release, and every schedule it
// declares or fires into here -- so that nobody has to know an id to
// act, plus any item an override names that is neither, because an
// override outlives whatever it named and a decision nobody can see is
// one nobody can undo.
//
// readOnly draws the buttons disabled for a reader the server would
// refuse every save here (RepoPage's writeRefusal, which says why);
// describedBy is the id of that sentence, each disabled control's
// accessible description.
//
// refreshKey moves when the page saved this repository's release switch
// elsewhere (RepoReleaseWorkflowSettings), which is the release item's
// own override, so the row is re-read rather than left saying what was
// true before; onSaved is the same news going the other way.
export default function RepoWorkOwnership({
  repo,
  readOnly = false,
  describedBy,
  refreshKey = 0,
  onSaved,
  showError,
}) {
  const root = repoApiRoot(repo);
  const [work, setWork] = useState(null);

  const load = useCallback(async () => {
    try {
      setWork(await api(`${root}/work-ownership`));
    } catch (err) {
      showError(err);
    }
  }, [root, showError]);

  useEffect(() => {
    setWork(null);
    load();
  }, [load]);

  useEffect(() => {
    if (refreshKey) load();
  }, [refreshKey, load]);

  const save = async (body) => {
    try {
      setWork(
        await api(`${root}/work-ownership`, {
          method: "PUT",
          body: JSON.stringify(body),
        }),
      );
      onSaved?.();
    } catch (err) {
      showError(err);
    }
  };

  const items = work?.items || [];

  return (
    <Box>
      <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
        Work ownership
      </Typography>
      {work === null ? (
        <Typography variant="body2" color="text.secondary">
          Loading work ownership…
        </Typography>
      ) : (
        <Stack spacing={1.5}>
          {/*
            The switch is the repository default, drawn as the one control
            it is (grain/task-3332): off is "this deployment runs no
            automated work for this repository", and every piece of
            standing work below follows it unless its own row says
            otherwise. The three-state buttons under it are still how
            "nobody has said" is reached.
          */}
          <FormControlLabel
            control={
              <Switch
                checked={work.defaultRuns}
                disabled={readOnly}
                onChange={(e) =>
                  save({ default: e.target.checked ? "on" : "off" })
                }
                inputProps={{
                  role: "switch",
                  "aria-label": "Standing work on this deployment",
                  "aria-describedby": describedBy,
                }}
              />
            }
            label={
              <Typography variant="body1" sx={{ fontWeight: 500 }}>
                Standing work on this deployment:{" "}
                {work.defaultRuns ? "on" : "off"}
              </Typography>
            }
          />
          <Box component="div">
            <Typography variant="body2" color="text.secondary">
              Covers: this repository&apos;s schedules (declared, set up here,
              and workflow crons), its release, the base-branch check,
              post-merge jobs, automatic ports, and the triage and repair tasks
              grain files on its own.
            </Typography>
            <Typography variant="body2" color="text.secondary">
              Does not cover: tasks people file by hand against this repository,
              their pull requests&apos; CI, the merge queue for them, and the
              repair of those pull requests — those run either way.
            </Typography>
          </Box>
          <Typography variant="body2" color="text.secondary">
            Whether <em>this</em> deployment is the one running this
            repository&apos;s standing work, as against another deployment
            watching the same repository. The repository declares that the work
            exists; this says who does it. Saying nothing is its own answer, and
            a different one from saying no: an item that says nothing follows
            the default and moves with it, and one that says off stays off when
            the default is turned back on — and one that says on runs while the
            default is off.
          </Typography>
          <Stack
            direction="row"
            spacing={1}
            alignItems="center"
            flexWrap="wrap"
            useFlexGap
          >
            <Typography variant="body2">Repository default:</Typography>
            <SaidChip said={work.default} />
            <ResolvedChip
              runs={work.defaultRuns}
              label={
                work.defaultRuns
                  ? "Runs this repository's work"
                  : "Does not run this repository's work"
              }
            />
            <SetButtons
              label="the repository default"
              said={work.default}
              disabled={readOnly}
              describedBy={describedBy}
              onSet={(state) => save({ default: state })}
            />
          </Stack>
          {items.length === 0 ? (
            <Typography variant="body2" color="text.secondary">
              No work items: this repository declares no schedules here, none
              fires into it, and nothing has been said about one.
            </Typography>
          ) : (
            <Stack spacing={1}>
              {items.map((item) => (
                <WorkItemRow
                  key={item.item}
                  item={item}
                  disabled={readOnly}
                  describedBy={describedBy}
                  onSet={(state) => save({ item: item.item, state })}
                />
              ))}
            </Stack>
          )}
          <Typography variant="caption" color="text.secondary">
            Grain resolves this before every piece of standing work it starts
            here. A skipped schedule says so on its own status and moves on to
            its next occurrence, so turning the work back on does not release a
            backlog of catch-up firings. The deployment&apos;s own configuration
            repository is checked whatever this says.
          </Typography>
        </Stack>
      )}
    </Box>
  );
}

// WorkItemRow is one item: what it is, what was said about it, what that
// resolves to, and the three buttons that change it.
function WorkItemRow({ item, disabled, describedBy, onSet }) {
  const label = item.title || item.item;
  return (
    <Stack
      direction="row"
      spacing={1}
      alignItems="center"
      flexWrap="wrap"
      useFlexGap
    >
      <ResolvedChip runs={item.runs} label={item.runs ? "Runs" : "Not run"} />
      <Typography variant="body2" sx={{ fontWeight: 500 }}>
        {label}
      </Typography>
      <SaidChip said={item.override} />
      {/* The id as well as the name whenever they differ: it is what the
          CLI and the API take, and what an override that outlives its
          schedule is left keyed on. */}
      {item.title && item.title !== item.item && (
        <Typography variant="caption" color="text.secondary">
          <code>{item.item}</code>
        </Typography>
      )}
      {item.paused && (
        <Chip size="small" variant="outlined" label="Paused on its own row" />
      )}
      {item.declaration && (
        <Typography variant="caption" color="text.secondary">
          declared in {item.declaration.path}
        </Typography>
      )}
      {item.kind === "unknown" && (
        <Typography variant="caption" color="text.secondary">
          nothing here has this id — a decision left behind by whatever it named
        </Typography>
      )}
      <SetButtons
        label={label}
        said={item.override}
        disabled={disabled}
        describedBy={describedBy}
        onSet={onSet}
      />
    </Stack>
  );
}

// SaidChip is what was actually said, in three colours and three
// wordings -- never as a tick, and never left blank for the empty case,
// which is the one a reader is most likely to fill in for themselves.
function SaidChip({ said }) {
  if (said === "on") {
    return <Chip size="small" color="success" label="On, set here" />;
  }
  if (said === "off") {
    return <Chip size="small" color="error" label="Off, set here" />;
  }
  return <Chip size="small" variant="outlined" label="Nobody has said" />;
}

// ResolvedChip is what that comes to once the override and the default
// have been resolved against each other -- the server's own answer
// (model.RunsWorkItem), never worked out here.
function ResolvedChip({ runs, label }) {
  return (
    <Chip
      size="small"
      variant="outlined"
      color={runs ? "success" : "default"}
      label={label}
    />
  );
}

// SetButtons is the three states as three buttons, with the one already
// chosen drawn as chosen and still clickable -- pressing it writes the
// same value again, which is a no-op rather than a trap, where
// disabling it would leave a row whose current state was only legible
// from the chips.
//
// Named for the thing they act on (`Nightly sweep: off`) rather than
// labelled "On"/"Off" alone, so that a page with a dozen rows on it has
// a dozen distinguishable controls for anything reading it by name -- a
// screen reader, or a test.
function SetButtons({ label, said, disabled = false, describedBy, onSet }) {
  const states = [
    ["on", "On"],
    ["off", "Off"],
    ["", "Say nothing"],
  ];
  return (
    <Stack direction="row" spacing={0.5}>
      {states.map(([state, text]) => (
        <Button
          key={state || "unset"}
          size="small"
          variant={said === state ? "contained" : "outlined"}
          aria-label={`${label}: ${state || "say nothing"}`}
          // The chosen one is drawn filled, which a screen reader cannot
          // see: aria-pressed says the same thing to it.
          aria-pressed={said === state}
          disabled={disabled}
          aria-describedby={describedBy}
          onClick={() => onSet(state)}
        >
          {text}
        </Button>
      ))}
    </Stack>
  );
}
