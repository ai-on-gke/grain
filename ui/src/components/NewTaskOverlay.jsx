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
import GrantChip from "./GrantChip.jsx";
import { localGrantHandlers } from "./capabilityMethods.js";
import { isCapabilityRef } from "../capabilityArgs.js";
import { useEffect, useRef, useState } from "react";
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Alert,
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
  Tooltip,
  Typography,
} from "@mui/material";
import ExpandMoreIcon from "@mui/icons-material/ExpandMore";
import api from "../api.js";
import { useHeldIds } from "../useHeldIds.js";
import fileToAttachment from "../attachments.js";
import {
  branchesInUse,
  capabilityRows,
  capabilityUnavailableHint,
  LEGACY_CAPABILITIES_HINT,
  NO_CAPABILITIES_HINT,
  defaultCapabilitiesFor,
  connectionLabel,
  connectionsServingModels,
  defaultModels,
  deploymentRow,
  repoReachable,
  repoTargetable,
  pickerRepos,
  connectionModels,
  modelOptions,
  targetableRepos,
  lastBaseForRepo,
  suggestsBase,
  teamDefaultLabel,
} from "../state.js";
import AttachmentPicker from "./AttachmentPicker.jsx";
import BranchField from "./BranchField.jsx";
import { CustomFieldInputs, fileableFieldValues } from "./CustomFields.jsx";
import { GlyphLabel } from "./ItemGlyph.jsx";
import ModelSelect from "./ModelSelect.jsx";
import ReadOnlyReposField from "./ReadOnlyReposField.jsx";
import WriteReposField from "./WriteReposField.jsx";
import RepoField, { NO_REPO } from "./RepoField.jsx";
import NoGitConnectionNotice from "./NoGitConnectionNotice.jsx";
import SidePanel from "./SidePanel.jsx";
import TaskPicker from "./TaskPicker.jsx";
import { clearTaskDraft, loadTaskDraft, saveTaskDraft } from "../taskDraft.js";
import { templateLabeler } from "../templateLabels.js";

export default function NewTaskOverlay({
  tasks,
  config,
  templates,
  defaultRepo,
  // prefill is a task somebody is being offered rather than one they
  // are writing: {title, description}, already filled in, which they
  // read, edit and file (the board buttons' own doing -- boards.js).
  // Only the two text fields, deliberately: everything else on this
  // form is a real choice about how the task runs, and a button
  // elsewhere in the app has no business making those quietly. The repo
  // is the exception, and it arrives as defaultRepo like every other
  // pre-filled repo does.
  prefill = null,
  fields,
  onClose,
  onCreated,
  // onOpenSettings opens the deployment's Settings on a tab: the "add a
  // git connection first" notice's link, used in place.
  onOpenSettings,
  showError,
}) {
  // If there is a stored draft and no explicit prefill from a button,
  // seed the form from the draft so leaving the window preserves work.
  //
  // Read once, at mount: undefined is "not read yet", and null is "read,
  // and there was none". With null standing for both, a form opened with
  // no draft re-read storage on every render, so the first autosave of
  // this very form became its "restored" draft mid-session -- and Repos,
  // remounted onto a team whose list lacked that snapshot's repo, showed
  // and filed it instead of the repo picked since (2026-10-07
  // walkthrough).
  const initialDraft = useRef(undefined);
  if (initialDraft.current === undefined) {
    initialDraft.current = !prefill ? loadTaskDraft() : null;
  }
  const draft = initialDraft.current;
  // draftDiscarded hides the restored draft's own leftovers -- the repo
  // the picker remounts on, and the notice above Repos -- once Discard
  // draft has been pressed. initialDraft itself is left alone: emptying
  // it would only make the next render load whatever is saved next.
  const [draftDiscarded, setDraftDiscarded] = useState(false);
  // Names alone where they are unique, more where they are not
  // (templateLabels.js).
  const templateLabel = templateLabeler(templates);
  const liveDraft = draftDiscarded ? null : draft;

  // Which repo a restored draft is filed against when the page this form
  // was opened from names a different one (grain/task-1585). A draft
  // records whether its repo was picked (repoEdited) or was only the page
  // default it happened to be opened with. An unpicked one yields to the
  // page's repo, keeping everything else that was typed, so a draft
  // started on one repo's page and reopened from another's files where
  // the operator now is. A picked one is kept -- it was a decision -- but
  // the form says so (draftRepoNotice, below) rather than quietly filing
  // somewhere other than the page in front of them. A draft saved before
  // repoEdited existed is read as picked: the old behaviour, now with the
  // notice. A form opened with no page repo has nothing to yield to.
  const pageRepo = defaultRepo || "";
  const draftRepoYields =
    !!draft &&
    pageRepo !== "" &&
    draft.repoEdited === false &&
    !draft.noRepo &&
    draft.repo !== pageRepo;
  // repoEdited is Base branch's baseEdited (below), for the repo itself.
  const repoEdited = useRef(
    Boolean(draft) && !draftRepoYields && draft.repoEdited !== false,
  );

  // title and repo are lifted to state, unlike most of this form's other
  // text fields, because the Create button's disabled state (below) has
  // to read their live value on every keystroke rather than only at
  // submit.
  const [title, setTitle] = useState(
    () => draft?.title ?? prefill?.title ?? "",
  );
  const [description, setDescription] = useState(
    () => draft?.description ?? prefill?.description ?? "",
  );
  const [repo, setRepo] = useState(() => {
    if (draft && draft.repo !== undefined && !draftRepoYields)
      return draft.repo;
    return defaultRepo || "";
  });
  // State disables the button visibly; the ref closes the smaller gap before
  // React renders that state, so two submit events can never start two POSTs.
  const [submitting, setSubmitting] = useState(false);
  const submittingRef = useRef(false);
  // noRepo is the explicit "this task has no repo" choice
  // (bwsalmon/agents#614): distinct from repo simply being blank, which
  // used to be the only way to skip it and silently fell back to the
  // deployment default (or failed validation with no warning until
  // submit). Checking it hides RepoField outright, so its <select>/
  // <input> is never in the DOM to be submitted -- payload.repo comes
  // back "" the same way an unset field always has, with noRepo now
  // saying why.
  // changeTarget is the non-git change target pick, offered only where
  // the binary registers the kind (config.changeTargets): a task with no
  // repo too (so noRepo is set with it, hiding everything a branch would
  // need), filed with a change target instead of noRepo, and never
  // auto-merged -- a changelist has no pull request to merge.
  const changeTargets = config?.changeTargets || [];
  const [changeTarget, setChangeTarget] = useState(() =>
    typeof draft?.changeTarget === "string" ? draft.changeTarget : "",
  );
  const [noRepo, setNoRepo] = useState(
    () => Boolean(draft?.noRepo) || Boolean(draft?.changeTarget),
  );
  // base tracks Base branch as state, unlike most text fields on this
  // form, so picking a repo (below) can prefill it from
  // lastBaseForRepo without fighting an uncontrolled <input>'s own
  // defaultValue (bwsalmon/agents#641).
  const [base, setBase] = useState(() => {
    // A base the draft only inherited from its old repo's history goes
    // with that repo when the page's takes over; a typed one stays, as it
    // does when the repo is picked by hand (handleRepoChange).
    if (
      draft &&
      draft.base !== undefined &&
      !(draftRepoYields && !draft.baseEdited)
    )
      return draft.base;
    return lastBaseForRepo(tasks, defaultRepo || "");
  });
  // baseEdited tracks whether the human has typed into Base branch
  // themselves, so handleRepoChange's prefill (below) never clobbers a
  // value they already chose -- only ever a prefill from a previous repo
  // pick, or the field's own initial empty state.
  const baseEdited = useRef(Boolean(draft?.baseEdited));
  // newBranch is "create a new branch" (grain/task-2317): the task is
  // filed onto newBranchName, which grain cuts from Base branch (the
  // repo's default branch when that is blank) before the task runs
  // (ui.CreateTaskRequest.NewBranch). Base branch keeps its own value
  // either way, so ticking this only changes what that field means.
  const [newBranch, setNewBranch] = useState(() => !!draft?.newBranch);
  const [newBranchName, setNewBranchName] = useState(
    () => draft?.newBranchName ?? "",
  );
  // The deployment's own settings, config.deployment -- its config repo,
  // repos, default model and git connection.
  const deployment = deploymentRow(config);

  // Two lists, not one: the target field may only offer what a task can
  // actually be filed against, while the reads and writes pickers below
  // are not gated on the allowlist at all (targetableRepos, state.js).
  //
  // All three are the deployment's: its attached repos and config repo
  // and, where none are attached, what its GitHub credential reaches
  // (GET /api/deployment/reachable-repos -- push for the target and the
  // writes, anything readable for the reads). reachable stays null until
  // that answers, or when the server cannot list it, and nothing is
  // warned about then.
  //
  // Asked only of a deployment that has no repos attached: one that has
  // them is offered those, and the server still refuses anything it
  // cannot reach when the task is filed.
  const [reachable, setReachable] = useState(null);
  const askReach = !!deployment && !(deployment.targetRepos || []).length;
  useEffect(() => {
    let live = true;
    setReachable(null);
    if (!askReach) return undefined;
    Promise.resolve()
      .then(() => api("/api/deployment/reachable-repos"))
      .then((res) => {
        if (live) setReachable(res && res.available ? res.repos || [] : null);
      })
      .catch(() => {
        if (live) setReachable(null);
      });
    return () => {
      live = false;
    };
  }, [askReach]);
  const targetOptions = targetableRepos(config, tasks, reachable);
  const repoOptions = pickerRepos(config, tasks, reachable);
  const writeOptions = pickerRepos(config, tasks, reachable, { push: true });

  // dependsOn is picked tasks ({id, title}), not just ids -- keeping the
  // title lets the chips below the picker read as "task 12 Fix the
  // thing" instead of a bare number nobody can place.
  const [dependsOn, setDependsOn] = useState(() => draft?.dependsOn ?? []);
  // reads is the read-only repos picked below, held here rather than
  // left as a form field for the reason capabilities and dependsOn are:
  // its picker (ReadOnlyReposField) is a search box over knownRepos, not
  // an <input> FormData could read the answer off.
  const [reads, setReads] = useState(() => draft?.reads ?? []);
  // writes is the extra writeable repos picked below ({repo, base}
  // objects, ui.CreateTaskRequest.Writes), held in state for reads' own
  // reason -- and because each row carries a branch box whose value
  // belongs with the repo it sits beside rather than in a form field
  // named after neither of them.
  const [writes, setWrites] = useState(() => draft?.writes ?? []);
  // capabilities starts as whatever a task against this repo would be
  // filed holding: this deployment's own default set plus whatever the
  // picked repo adds to it (defaultCapabilitiesFor, over GET
  // /api/config's defaultCapabilities and repoDefaultCapabilities) --
  // ticked, in the picker below, so it is visible before the task is
  // filed and can be unticked here rather than detached afterwards. The
  // payload always names the resulting list, so what is ticked when
  // Create is clicked is exactly what the task is filed with; the server
  // only falls back to its own defaults for a caller that names no list
  // at all (ui.CreateTaskRequest.Capabilities).
  const [capabilities, setCapabilities] = useState(() => {
    if (
      draft &&
      draft.capabilities !== undefined &&
      !(draftRepoYields && !draft.capabilitiesEdited)
    )
      return draft.capabilities;
    return defaultCapabilitiesFor(config, defaultRepo || "");
  });
  // The rows the picker below is drawn from: what this deployment has
  // configured, plus anything already ticked -- a default it cannot
  // honour is still shown, and still warned about, because a pre-ticked
  // grant needs a row to be unticked from (capabilityRows, state.js).
  //
  // Capability server grants (with their with: arguments) and the agent
  // API's grants (apiGrants.js) are kept in the same list, and edited
  // together by GrantsPicker below the checklist rather than as rows of
  // it; the checklist is only the legacy in-process ids.
  const capabilityIds = capabilities.filter(
    (id) => !isCapabilityRef(id) && !isApiGrant(id),
  );
  const apiIds = capabilities.filter(isApiGrant);
  const capabilityRefs = capabilities.filter(isCapabilityRef);
  const heldCapabilityIds = useHeldIds(capabilityIds);
  const capabilityOptions = withoutApiRows(
    capabilityRows(config?.capabilities, heldCapabilityIds),
  );
  const methodGrants = localGrantHandlers(capabilityRefs, (refs) => {
    capabilitiesEdited.current = true;
    setCapabilities([...capabilityIds, ...apiIds, ...refs]);
  });
  // capabilitiesEdited is Base branch's baseEdited above, for the same
  // reason: picking a different repo re-seeds this picker from that
  // repo's own defaults, and must not do so over a choice somebody has
  // already made. Once the picker has been touched, the ticks are theirs
  // and changing repo leaves them exactly as they are -- an untick that
  // silently came back because the repo changed afterwards is the one
  // failure a re-seed can cause, and it would put a capability on a task
  // that whoever filed it had already said no to.
  const capabilitiesEdited = useRef(Boolean(draft?.capabilitiesEdited));
  // attachments is File objects, not yet read -- AttachmentPicker's own
  // doc comment on why that read is deferred to submit.
  const [attachments, setAttachments] = useState([]);
  // model is the model this task runs on (ui.CreateTaskRequest.Model);
  // "" is its team's default. agentBackendId is the optional connection
  // pin beside it (ui.CreateTaskRequest.AgentBackendID); "" lets dispatch
  // round-robin across every enabled connection of the team that serves
  // the model. Both are lifted to state because the connection picker's
  // choices depend on the live model: a pinned connection must advertise
  // it, or CreateTask refuses the pair (docs/connections-and-models.md).
  // A model implies its framework, so there is no framework choice.
  const [model, setModel] = useState(() => draft?.model ?? "");
  const [agentBackendId, setAgentBackendId] = useState(
    () => draft?.agentBackendId ?? "",
  );
  const connections = config?.agentBackends || [];
  const models = connectionModels(connections);
  const teamDefaultModels = defaultModels(deployment);
  const teamDefaultModel = deployment?.defaultModel || "";
  // The model(s) a pin is checked against: the one chosen here, or else the
  // team's default when it has one.
  const effectiveModels = model ? [model] : teamDefaultModels;
  const effectiveModel = effectiveModels.join(" → ");
  const pinChoices = connectionsServingModels(
    models,
    connections,
    effectiveModels,
  );
  const [networkAccess, setNetworkAccess] = useState(
    () => draft?.networkAccess ?? "",
  );
  // What a run ending on nothing_needed does to this task
  // (model.Task.NothingNeededAction): "close" -- the default, and what
  // the server reads "" as -- or "ack" to leave it for acknowledgement.
  const [nothingNeededAction, setNothingNeededAction] = useState(
    () => draft?.nothingNeededAction ?? "close",
  );
  const [reviewTemplateId, setReviewTemplateId] = useState(
    () => draft?.reviewTemplateId ?? "",
  );
  const [reviewers, setReviewers] = useState(() =>
    Array.isArray(draft?.reviewers)
      ? draft.reviewers.join(", ")
      : (draft?.reviewers ?? ""),
  );
  const [promptExtension, setPromptExtension] = useState(
    () => draft?.promptExtension ?? "",
  );
  const [sandboxCpus, setSandboxCpus] = useState(
    () => draft?.sandboxCpus ?? "",
  );
  const [sandboxMemoryMb, setSandboxMemoryMb] = useState(
    () => draft?.sandboxMemoryMb ?? "",
  );
  const [sandboxDiskGb, setSandboxDiskGb] = useState(
    () => draft?.sandboxDiskGb ?? "",
  );
  // advancedOpen is Advanced options' own expanded state, held here so
  // that a field inside it the browser refuses on submit (a vCPUs of -1,
  // say, kept by the draft) can open the section rather than leave the
  // refusal unseen: collapsed, the field is not focusable, so Create did
  // nothing at all and only the console said why (2026-10-23 intake
  // walkthrough).
  const [advancedOpen, setAdvancedOpen] = useState(false);
  // Only a refusal of an actual submit counts: Create's click marks one,
  // and Enter in a field submits by clicking it too. The browser fires
  // `invalid` at nothing else, but jsdom also fires it while matching a
  // `:invalid` style rule.
  const submitAttempt = useRef(false);
  const markSubmitAttempt = () => {
    submitAttempt.current = true;
    setTimeout(() => {
      submitAttempt.current = false;
    }, 0);
  };
  const refusedField = useRef(null);
  const revealInvalidAdvanced = (evt) => {
    if (advancedOpen || !submitAttempt.current) return;
    refusedField.current = evt.target;
    setAdvancedOpen(true);
  };
  // Once the section has finished opening, not before: focusing a field
  // mid-animation scrolls to where it will not be, and leaves it below
  // the fold. Then the browser's own message, on the field that earned
  // it.
  const reportRefusedField = () => {
    const field = refusedField.current;
    refusedField.current = null;
    field?.focus?.();
    field?.reportValidity?.();
  };
  const [autoMerge, setAutoMerge] = useState(
    () => draft?.autoMerge ?? !!config?.autoMergeByDefault,
  );
  const [approved, setApproved] = useState(
    () => draft?.approved ?? !!config?.approvedByDefault,
  );
  // noPort is model.Task.NoPort: a change that belongs only on the
  // branch it lands on, and is not ported to the repo's paired branches.
  const [noPort, setNoPort] = useState(() => !!draft?.noPort);
  // fixesBaseBranch is ui.CreateTaskRequest.FixesBaseBranch: this task
  // fixes its base branch while that branch is red, so grain never makes
  // it wait on the branch's own repair task (grain/task-3451).
  const [fixesBaseBranch, setFixesBaseBranch] = useState(
    () => !!draft?.fixesBaseBranch,
  );
  // atFront is which end of the backlog this task joins
  // (ui.CreateTaskRequest.AtFront, grain/task-202): the front, ahead of
  // everything already queued, or the end, behind it. Seeded from
  // config.newestFirst, which is where the last task filed from here
  // went -- the server remembers the choice on every filing that states
  // one, so this form opens on it rather than on a fixed default nobody
  // chose. App.jsx re-fetches the config after a task is created, so the
  // next open of this form sees the choice this one just made.
  const [atFront, setAtFront] = useState(
    () => draft?.atFront ?? !!config?.newestFirst,
  );
  // customFields is this deployment's own fields (grain/task-53), held
  // as state rather than read off the form at submit for the reason
  // capabilities above is: an enum is a <Select> and a boolean a
  // checkbox, neither of which FormData reads the way a text input is
  // read. It opens on the values the /new link carried, if it was
  // followed from a task's own "File a task with these fields", and on
  // each field's declared default otherwise.
  // fileableFieldValues rather than the values as they stand: this
  // form files a task, and a filing has nothing already stored to be
  // grandfathered against, so a value the declarations have since
  // stopped offering (a link copied out of a task that still carries
  // one) is dropped here rather than refused by POST /api/tasks.
  const [customFields, setCustomFields] = useState(() => {
    if (fields) return fileableFieldValues(config, fields);
    if (draft?.customFields) return draft.customFields;
    return fileableFieldValues(config, null);
  });
  const setCustomField = (name, value) =>
    setCustomFields((prev) => ({ ...prev, [name]: value }));

  const [resetKey, setResetKey] = useState(0);
  // repoSeed is what Repos starts from when it is remounted for a reason
  // other than a discard: today only a team change carrying the previous
  // team's config repo over to the new team's (see Target team below).
  const [repoSeed, setRepoSeed] = useState(null);
  const discardedRef = useRef(false);

  const isDraftModified = () => {
    if (title.trim() !== "") return true;
    if (description.trim() !== "") return true;
    if (noRepo) return true;
    if (repo !== (defaultRepo || "")) return true;
    if (
      baseEdited.current &&
      base !== lastBaseForRepo(tasks, defaultRepo || "")
    )
      return true;
    if (reads.length > 0) return true;
    if (writes.length > 0) return true;
    if (dependsOn.length > 0) return true;
    if (capabilitiesEdited.current) return true;
    if (model !== "") return true;
    if (agentBackendId !== "") return true;
    if (networkAccess !== "") return true;
    if (nothingNeededAction !== "close") return true;
    if (reviewTemplateId !== "") return true;
    if (promptExtension.trim() !== "") return true;
    if (sandboxCpus !== "" && sandboxCpus !== 0) return true;
    if (sandboxMemoryMb !== "" && sandboxMemoryMb !== 0) return true;
    if (sandboxDiskGb !== "" && sandboxDiskGb !== 0) return true;
    if (autoMerge !== !!config?.autoMergeByDefault) return true;
    if (approved !== !!config?.approvedByDefault) return true;
    if (noPort) return true;
    if (fixesBaseBranch) return true;
    if (newBranch || newBranchName.trim() !== "") return true;
    if (reviewers.trim() !== "") return true;
    if (atFront !== !!config?.newestFirst) return true;
    const defaultFields = fileableFieldValues(config, fields);
    for (const [k, v] of Object.entries(customFields)) {
      if (JSON.stringify(v) !== JSON.stringify(defaultFields[k])) return true;
    }
    return false;
  };

  const hasDraft = isDraftModified();
  // draftRepoNotice is the restored draft's own repo, while it is still
  // the one this task would be filed against and is not the page's.
  const draftRepoNotice =
    liveDraft &&
    pageRepo !== "" &&
    !draftRepoYields &&
    (liveDraft.noRepo || (liveDraft.repo ?? pageRepo) !== pageRepo) &&
    (noRepo || repo !== pageRepo)
      ? liveDraft.noRepo || !liveDraft.repo
        ? "Restored a draft filed with no repo"
        : `Restored a draft for ${liveDraft.repo}`
      : null;

  useEffect(() => {
    if (discardedRef.current) {
      discardedRef.current = false;
      return;
    }
    if (isDraftModified()) {
      saveTaskDraft({
        title,
        description,
        repo,
        repoEdited: repoEdited.current,
        noRepo,
        changeTarget,
        base,
        baseEdited: baseEdited.current,
        newBranch,
        newBranchName,
        reads,
        writes,
        autoMerge,
        capabilities,
        capabilitiesEdited: capabilitiesEdited.current,
        dependsOn,
        model,
        agentBackendId,
        networkAccess,
        nothingNeededAction,
        reviewTemplateId,
        reviewers,
        promptExtension,
        sandboxCpus,
        sandboxMemoryMb,
        sandboxDiskGb,
        approved,
        noPort,
        fixesBaseBranch,
        atFront,
        customFields,
      });
    } else {
      clearTaskDraft();
    }
  }, [
    title,
    description,
    repo,
    noRepo,
    changeTarget,
    base,
    newBranch,
    newBranchName,
    reads,
    writes,
    autoMerge,
    capabilities,
    dependsOn,
    model,
    agentBackendId,
    networkAccess,
    nothingNeededAction,
    reviewTemplateId,
    reviewers,
    promptExtension,
    sandboxCpus,
    sandboxMemoryMb,
    sandboxDiskGb,
    approved,
    noPort,
    fixesBaseBranch,
    atFront,
    customFields,
  ]);

  const handleDiscard = () => {
    discardedRef.current = true;
    clearTaskDraft();
    setDraftDiscarded(true);
    repoEdited.current = false;
    setTitle("");
    setDescription("");
    setRepo(defaultRepo || "");
    setNoRepo(false);
    setChangeTarget("");
    setBase(lastBaseForRepo(tasks, defaultRepo || ""));
    baseEdited.current = false;
    setNewBranch(false);
    setNewBranchName("");
    setDependsOn([]);
    setReads([]);
    setWrites([]);
    setCapabilities(defaultCapabilitiesFor(config, defaultRepo || ""));
    capabilitiesEdited.current = false;
    setCustomFields(fileableFieldValues(config, fields));
    setAttachments([]);
    setModel("");
    setAgentBackendId("");
    setNetworkAccess("");
    setNothingNeededAction("close");
    setReviewTemplateId("");
    setReviewers("");
    setPromptExtension("");
    setSandboxCpus("");
    setSandboxMemoryMb("");
    setSandboxDiskGb("");
    setAutoMerge(!!config?.autoMergeByDefault);
    setApproved(!!config?.approvedByDefault);
    setNoPort(false);
    setFixesBaseBranch(false);
    setAtFront(!!config?.newestFirst);
    setRepoSeed(null);
    setResetKey((k) => k + 1);
  };

  // handleRepoChange prefills Base branch from whatever the newly-picked
  // repo's own last task used (bwsalmon/agents#641), rather than
  // clobbering something the human already typed (baseEdited.current) or
  // a repo with no history to prefill from. That "no history" check is
  // gated on whether the repo has any task suggestsBase counts at all,
  // not on lastBaseForRepo's return value -- that value is "" both when
  // there is no history and when the most recent task deliberately used
  // the default branch, and only the former should leave a
  // manually-typed base alone.
  //
  // "No repo" arrives here as any other pick does, since it is an
  // option in the same list: it names no repo, so it resolves
  // capabilities the way a task with no target does (the deployment's
  // own set alone, what CreateTask gives a task whose Target is nil) and
  // has no repo history to prefill a base from.
  const handleRepoChange = (picked) => {
    const ct = changeTargets.includes(picked) ? picked : "";
    const none = picked === NO_REPO || ct !== "";
    const r = none ? "" : picked;
    setChangeTarget(ct);
    setNoRepo(none);
    setRepo(r);
    // Re-seeded unconditionally when the picker is untouched, unlike
    // Base branch's own "only if the new repo has history" guard: the
    // resolved set for a repo that adds nothing is the deployment's
    // alone, which is a real answer rather than the absence of one, and
    // leaving the previous repo's extra capabilities ticked would file
    // the task with capabilities this repo never asked for.
    if (!capabilitiesEdited.current)
      setCapabilities(defaultCapabilitiesFor(config, r));
    if (none || baseEdited.current) return;
    const hasHistory = (tasks || []).some(
      (t) => t.repo === r && suggestsBase(t),
    );
    if (hasHistory) setBase(lastBaseForRepo(tasks, r));
  };

  const addDependency = (t) => {
    setDependsOn((prev) =>
      prev.some((p) => p.id === t.id) ? prev : [...prev, t],
    );
  };
  const removeDependency = (id) => {
    setDependsOn((prev) => prev.filter((p) => p.id !== id));
  };

  const submit = async (evt) => {
    evt.preventDefault();
    if (submittingRef.current) return;
    submittingRef.current = true;
    setSubmitting(true);
    const form = evt.target;
    const data = new FormData(form);
    // The repo <select> submits NO_REPO's own sentinel value when "No
    // repo" is picked, so read the field rather than only the noRepo
    // state: the two agree, but this is the one that decides what
    // reaches the server, and NO_REPO is not a repo name.
    const pickedRepo = data.get("repo") ?? repo;
    const ct =
      changeTarget || (changeTargets.includes(pickedRepo) ? pickedRepo : "");
    const none = noRepo || pickedRepo === NO_REPO || ct !== "";
    // A task creating its branch is filed onto that branch, and Base
    // branch is what grain cuts it from. A task with no repo has no
    // branch to create, so the box means nothing for one -- and it is
    // not sent for one either: picking a repo prefills it from that
    // repo's last task, and switching to No repo afterwards left that
    // branch on the task as its target (2026-10-13 walkthrough).
    const creating = newBranch && !none;
    const parsedReviewers = reviewers
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    const payload = {
      title: title,
      description: description || "",
      repo: none ? "" : pickedRepo,
      noRepo: none && !ct,
      ...(ct ? { changeTarget: { kind: ct } } : {}),
      ...(parsedReviewers.length > 0 ? { reviewers: parsedReviewers } : {}),
      ...(creating
        ? {
            base: newBranchName.trim(),
            newBranch: true,
            newBranchFrom: base || "",
          }
        : { base: none ? "" : base || "" }),
      autoMerge: autoMerge && !ct,
      // Only said when ticked: unticked is the server's own default
      // (model.Task.NoPort).
      ...(noPort && !ct ? { noPort: true } : {}),
      // Likewise only when ticked, and never for a task with no repo,
      // which has no base branch to fix.
      ...(fixesBaseBranch && !none ? { fixesBaseBranch: true } : {}),
      sandboxCpus: parseInt(sandboxCpus, 10) || 0,
      sandboxMemoryMb: parseInt(sandboxMemoryMb, 10) || 0,
      sandboxDiskGb: parseInt(sandboxDiskGb, 10) || 0,
      // "" is the deployment's default model (model.Task.Model). No framework
      // is sent: the model implies one.
      model,
      // "" is the rotation across every enabled connection of the team
      // that serves the model; naming one pins it, even a disabled one
      // (model.Task.AgentBackendID).
      agentBackendId: pinChoices.some((c) => c.id === agentBackendId)
        ? agentBackendId
        : "",
      // "" is the deployment's own answer here too, not a network: the
      // server reads an empty networkAccess as "whatever this deployment
      // is set to when the task dispatches" (model.Task.NetworkAccess).
      // The field is disabled on a deployment that cannot honour "none",
      // and a disabled field is left out of FormData entirely -- which
      // is the "" that means no override.
      networkAccess: config?.sandboxShapeIgnored ? "" : networkAccess,
      nothingNeededAction,
      // "" is "no override" here too, not "tell this run nothing": with
      // it empty the run gets whatever the deployment and the repo say
      // (model.Task.PromptExtension).
      promptExtension: promptExtension || "",
      // "" is no review at all, which is what most tasks want and what
      // the picker below starts on (model.Task.ReviewTemplateID).
      reviewTemplateId: (templates || []).length === 0 ? "" : reviewTemplateId,
      capabilities,
      // Every field the form rendered, blanks included: the server
      // drops the empty ones (model.NormalizeCustomFieldValues), so
      // what is filed is exactly what was filled in. Left out of the
      // request entirely on a deployment that declares none, which is
      // most of them -- an empty map would be a key on every filing
      // saying nothing.
      ...(Object.keys(customFields).length > 0 ? { customFields } : {}),
      dependsOn: dependsOn.map((t) => (typeof t === "object" && t ? t.id : t)),
      reads,
      writes: ct ? [] : writes,
      approved,
      atFront,
    };
    try {
      // Read inside the try: a file that has gone away since it was
      // picked fails here, and that has to land in showError and release
      // the Create button like any other failed filing.
      payload.attachments = await Promise.all(
        attachments.map((f) => fileToAttachment(f)),
      );
      await api("/api/tasks", {
        method: "POST",
        body: JSON.stringify(payload),
      });
      clearTaskDraft();
      form.reset();
      setTitle("");
      setDescription("");
      setRepo(defaultRepo || "");
      repoEdited.current = false;
      setNoRepo(false);
      setChangeTarget("");
      setBase(lastBaseForRepo(tasks, defaultRepo || ""));
      baseEdited.current = false;
      setNewBranch(false);
      setNewBranchName("");
      setDependsOn([]);
      setReads([]);
      // Emptied with the rest of the repos block: the extra repos were
      // this task's, and the form is being cleared for the next one.
      setWrites([]);
      setCapabilities(defaultCapabilitiesFor(config, defaultRepo || ""));
      capabilitiesEdited.current = false;
      // Back to the declared defaults rather than to what this filing
      // carried: the link's values were for the task just filed, and
      // the form is being emptied for the next one.
      setCustomFields(fileableFieldValues(config, null));
      setAttachments([]);
      setModel("");
      setAgentBackendId("");
      setNetworkAccess("");
      setNothingNeededAction("close");
      setReviewTemplateId("");
      setReviewers("");
      setPromptExtension("");
      setSandboxCpus("");
      setSandboxMemoryMb("");
      setSandboxDiskGb("");
      setAutoMerge(!!config?.autoMergeByDefault);
      setApproved(!!config?.approvedByDefault);
      setNoPort(false);
      setFixesBaseBranch(false);
      setAtFront(!!config?.newestFirst);
      onClose();
      await onCreated();
    } catch (err) {
      submittingRef.current = false;
      setSubmitting(false);
      showError(err);
    }
  };

  return (
    <SidePanel
      title="New task"
      onClose={onClose}
      onSubmit={submit}
      footer={
        <Stack
          direction="row"
          spacing={1}
          justifyContent="space-between"
          alignItems="center"
        >
          {hasDraft ? (
            <Button
              type="button"
              variant="text"
              color="inherit"
              onClick={handleDiscard}
            >
              Discard draft
            </Button>
          ) : (
            <span />
          )}
          <Button
            type="submit"
            variant="contained"
            onClick={markSubmitAttempt}
            disabled={
              submitting ||
              !title.trim() ||
              (!noRepo && !repo.trim()) ||
              (!noRepo && newBranch && !newBranchName.trim())
            }
          >
            Create task
          </Button>
        </Stack>
      }
    >
      <>
        {/* Focused as the form opens, as New session's and New team's
            first fields are: without it the dialog's own paper held
            focus, so a title typed straight after "+ New task" went
            nowhere and the box was a Tab past "Close dialog" (found in
            the 2026-10-15 intake walkthrough). */}
        <TextField
          name="title"
          label="Title"
          required
          InputLabelProps={{ required: false }}
          autoFocus
          autoComplete="off"
          fullWidth
          margin="normal"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
        />
        {/* minRows, not rows: the box grows with whatever is typed into
            it rather than keeping a fixed five lines and scrolling
            inside itself, so a long description is read in the panel's
            own scroll rather than in a second scrollbar within it
            (grain/task-85). The clip that attaches files sits in its
            bottom-right corner, which is what the padding leaves room
            for. */}
        <AttachmentPicker
          files={attachments}
          onChange={setAttachments}
          variant="icon"
        >
          <TextField
            name="description"
            label="Description"
            multiline
            minRows={6}
            fullWidth
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            InputProps={{ sx: { pb: 4 } }}
          />
        </AttachmentPicker>
        {/* This deployment's own fields, above the repo and the rest of
            the mechanics: they are part of what the task *is*, like the
            title and the description over them, rather than part of how
            it runs. Nothing renders here on a deployment that declares
            none, which is most of them. */}
        <CustomFieldInputs
          config={config}
          values={customFields}
          onChange={setCustomField}
        />
        {/* One block for every repo this task may change, not two
            (grain/task-202): the repo the run itself pushes to, then
            any others named right under it, added in the same place
            rather than in a second field somebody has to notice and
            work out they belong in. Which is which is still a real
            distinction -- it is the first one that gets a branch and a
            pull request -- and it is settled by the order they are in
            rather than by asking. */}
        <Box sx={{ mt: 2, mb: 1 }}>
          {!noRepo && (
            <NoGitConnectionNotice
              deployment={deployment}
              onOpenSettings={onOpenSettings}
              sx={{ mb: 1 }}
            />
          )}
          {draftRepoNotice !== null && (
            <Alert
              severity="info"
              sx={{ mb: 1 }}
              data-testid="draft-repo-notice"
            >
              {draftRepoNotice}. Discard it to file in {pageRepo}.
            </Alert>
          )}
          <Box component="label" sx={{ display: "block", mb: 0.5 }}>
            <Typography
              variant="caption"
              color="text.secondary"
              sx={{ display: "block", mb: 0.5 }}
            >
              Repos{" "}
              <span className="hint">
                the first is the one this task&apos;s own work lands in
              </span>
            </Typography>
            {/* Pre-filled from the repo the "+ New task" button was
                opened from -- the repo-centric task list's whole point
                is filing work against the repo you're already looking
                at without retyping it. allowNoRepo puts "no repo at
                all" in this same list (grain/task-85): picking it is
                what sets noRepo, and submit above blanks the repo the
                field then reports. */}
            <RepoField
              key={resetKey}
              name="repo"
              options={targetOptions}
              defaultValue={
                repoSeed !== null
                  ? repoSeed
                  : liveDraft && !draftRepoYields
                    ? liveDraft.changeTarget &&
                      changeTargets.includes(liveDraft.changeTarget)
                      ? liveDraft.changeTarget
                      : liveDraft.noRepo
                        ? NO_REPO
                        : liveDraft.repo || ""
                    : defaultRepo || ""
              }
              allowNoRepo
              changeTargets={changeTargets}
              onChange={(picked) => {
                // Only a pick made here marks the repo as chosen: a team
                // change routes through handleRepoChange too, and that is
                // the form following the team rather than anybody's choice.
                repoEdited.current = true;
                handleRepoChange(picked);
              }}
            />
            {/* Typing is still allowed; this is the warning for a repo
                the team's credential cannot push to, which the server
                will refuse to file (ui.Client.reachRefusal). */}
            {!noRepo &&
              repoReachable(reachable, repo, { push: true }) === false && (
                <Typography
                  variant="caption"
                  color="warning.main"
                  data-testid="repo-unreachable"
                >
                  The deployment&apos;s GitHub credential cannot push to {repo}{" "}
                  -- give it access on GitHub, or choose another in Settings →
                  Git connections.
                </Typography>
              )}
            {/* The server files a task on a repo off the team's list
                rather than refusing it, but parks it awaiting a reply
                (ui.Client.parkOffAllowlist), and the form used to close
                without a word -- the only explanation a comment on a task
                nobody had opened yet. Said here before submit, by the
                rule the pane's own warning uses (repoTargetable). */}
            {!noRepo && repo && !repoTargetable(config, repo) && (
              <Typography
                variant="caption"
                color="warning.main"
                sx={{ display: "block" }}
                data-testid="repo-off-allowlist"
              >
                {repo} is not on this deployment&apos;s target repo list. The
                task will be filed but parked, and runs only once an operator
                adds the repo under Settings and replies on it.
              </Typography>
            )}
          </Box>
          {/* The first repo's own branch, which is the task's: it sits
              between that repo and the ones under it because that is
              where it belongs -- each of those carries its branch on
              the same row, and this one would read as the last of them
              if it came after. */}
          {!noRepo && (
            <FormControlLabel
              control={
                <Checkbox
                  name="newBranch"
                  checked={newBranch}
                  onChange={(e) => setNewBranch(e.target.checked)}
                />
              }
              label="Create a new branch"
              sx={{ display: "flex" }}
            />
          )}
          {!noRepo && newBranch && (
            <TextField
              name="newBranchName"
              label="New branch name"
              required
              fullWidth
              margin="dense"
              value={newBranchName}
              onChange={(e) => setNewBranchName(e.target.value)}
              helperText={
                "grain creates it from the branch below before the task " +
                "runs; one that already exists is used as it stands"
              }
              inputProps={{ "data-testid": "new-branch-name" }}
            />
          )}
          {/* A non-git change target task has no branch and lands nowhere
              else: its change is one changelist. */}
          {!changeTarget && (
            <>
              <BranchField
                name="base"
                label={!noRepo && newBranch ? "Create from" : "Base branch"}
                helperText={
                  !noRepo && newBranch
                    ? "optional -- the repo's default branch if blank"
                    : "optional"
                }
                margin="dense"
                size="medium"
                repo={noRepo ? "" : repo}
                options={branchesInUse(tasks, repo)}
                warnMissing="push it first, or have the task depend on the one that cuts it"
                value={base}
                onChange={(next) => {
                  baseEdited.current = true;
                  setBase(next);
                }}
              />
              <WriteReposField
                options={writeOptions}
                value={writes}
                onChange={setWrites}
              />
            </>
          )}
        </Box>
        <ReadOnlyReposField
          options={repoOptions}
          value={reads}
          onChange={setReads}
          // The repo picked right above, not a saved one: this form is
          // where both halves are chosen, so the read-only picker should
          // stop offering the target the moment it is named -- the same
          // value WriteReposField gets, and "" for a task filed with no
          // repo at all, which has no target for a read to collide with.
          target={noRepo ? "" : repo}
        />
        <TextField
          name="reviewers"
          label="Reviewers"
          placeholder="alice, bob"
          helperText="optional -- comma-separated usernames passed to workspace grants and used when mailing a CL"
          fullWidth
          margin="normal"
          size="small"
          value={reviewers}
          onChange={(e) => setReviewers(e.target.value)}
        />
        {changeTarget ? (
          <Typography
            variant="caption"
            color="text.secondary"
            sx={{ display: "block", mt: 1 }}
            data-testid="change-target-note"
          >
            Its change is a {changeTarget} changelist in this task&apos;s own
            workspace, created for it: nothing is cloned, and there is no pull
            request to merge or branch to port.
          </Typography>
        ) : (
          <>
            <FormControlLabel
              control={
                <Checkbox
                  name="autoMerge"
                  checked={autoMerge}
                  onChange={(e) => setAutoMerge(e.target.checked)}
                />
              }
              label="Auto-merge once checks pass"
              sx={{ display: "flex", mt: 1 }}
            />
            <FormControlLabel
              control={
                <Checkbox
                  name="noPort"
                  checked={noPort}
                  onChange={(e) => setNoPort(e.target.checked)}
                />
              }
              label="Don't port to the repo's paired branches"
              sx={{ display: "flex" }}
            />
          </>
        )}
        {!noRepo && (
          <FormControlLabel
            control={
              <Checkbox
                name="fixesBaseBranch"
                checked={fixesBaseBranch}
                onChange={(e) => setFixesBaseBranch(e.target.checked)}
              />
            }
            label={`This fixes a red ${(newBranch ? newBranchName.trim() : base) || "main"}`}
            title="While the branch a task lands on is red, grain makes the work aimed at it wait on that branch's repair task. Tick this for a task that is itself fixing the branch -- a de-flake, a broken build -- so it never waits: it counts as the branch's repair, and is left open with a comment, not closed, when the branch goes green."
            sx={{ display: "flex" }}
          />
        )}
        <FormControl fullWidth margin="normal" size="small">
          <InputLabel id="new-task-capabilities-label">
            Legacy capabilities
          </InputLabel>
          <Select
            labelId="new-task-capabilities-label"
            label="Legacy capabilities"
            multiple
            value={capabilityIds}
            onChange={(e) => {
              capabilitiesEdited.current = true;
              setCapabilities([
                ...e.target.value,
                ...apiIds,
                ...capabilityRefs,
              ]);
            }}
            renderValue={(selected) => (
              <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.5 }}>
                {selected.map((id) => (
                  <GrantChip
                    key={id}
                    grant={id}
                    config={config}
                    tabIndex={-1}
                  />
                ))}
              </Box>
            )}
          >
            {capabilityOptions.length === 0 && (
              <MenuItem disabled>{NO_CAPABILITIES_HINT}</MenuItem>
            )}
            {capabilityOptions.map((c) => {
              const unavailable = capabilityUnavailableHint(c);
              return (
                <MenuItem
                  key={c.id}
                  value={c.id}
                  title={
                    unavailable
                      ? `${c.description}\n\n${unavailable}`
                      : c.description
                  }
                >
                  <Checkbox
                    checked={capabilityIds.includes(c.id)}
                    size="small"
                  />
                  <ListItemText
                    primary={c.name}
                    secondary={unavailable || null}
                    secondaryTypographyProps={{ color: "warning.main" }}
                  />
                </MenuItem>
              );
            })}
          </Select>
          {defaultCapabilitiesFor(config, noRepo ? "" : repo).length > 0 && (
            <FormHelperText>
              Pre-ticked ones are the defaults for this repo -- this
              deployment&apos;s (Settings &gt; Tasks), plus anything the repo
              itself adds (Repos &gt; Capabilities). Untick any this task should
              not have.
            </FormHelperText>
          )}
          <FormHelperText>{LEGACY_CAPABILITIES_HINT}</FormHelperText>
        </FormControl>
        <GrantsPicker
          config={config}
          grants={capabilities}
          onApiChange={(next) => {
            capabilitiesEdited.current = true;
            setCapabilities([...capabilityIds, ...next, ...capabilityRefs]);
          }}
          onSubmit={methodGrants.onSubmit}
          onRemove={methodGrants.onRemove}
          idPrefix="new-task"
        />
        {/* The model this one task runs on. Its choices are what the
            target team's connections advertise; a model only disabled
            connections serve is still offered, and says so, because
            pinning one of those (Advanced options) is how it runs. Left
            out where the team has no connection at all. */}
        {models.length > 0 && (
          <ModelSelect
            name="model"
            label="Model"
            value={model}
            onChange={(next) => {
              setModel(next);
              const nextModels = next ? [next] : teamDefaultModels;
              const allowed = connectionsServingModels(
                models,
                connections,
                nextModels,
              );
              if (
                agentBackendId &&
                nextModels.length > 0 &&
                !allowed.some((c) => c.id === agentBackendId)
              )
                setAgentBackendId("");
            }}
            options={modelOptions(
              models,
              teamDefaultLabel(deployment || teamDefaultModel, models),
              model,
            )}
            helperText="which model this one task runs on -- leave it on the deployment's default unless it needs a particular one; type to search"
            margin="normal"
          />
        )}
        <fieldset>
          <legend>
            Depends on <span className="hint">optional</span>
          </legend>
          {dependsOn.length > 0 && (
            // One full-width chip per line, the shape DetailOverlay's
            // dependency list uses: a picked task then reads as a row,
            // rather than a bubble sized by however long its title
            // happens to be, wrapped in beside the others.
            <Stack spacing={0.6} sx={{ mb: 1 }}>
              {dependsOn.map((t) => (
                <Tooltip
                  key={t.id}
                  title={`${t.id} ${t.title}`}
                  placement="left"
                >
                  <Chip
                    label={`${t.id} ${t.title}`}
                    onDelete={() => removeDependency(t.id)}
                    deleteIcon={
                      <span title={`Remove dependency on ${t.id}`}>×</span>
                    }
                    sx={{
                      width: "100%",
                      justifyContent: "space-between",
                      "& .MuiChip-label": {
                        flex: 1,
                        minWidth: 0,
                        overflow: "hidden",
                        textOverflow: "ellipsis",
                      },
                    }}
                  />
                </Tooltip>
              ))}
            </Stack>
          )}
          <TaskPicker
            tasks={tasks || []}
            exclude={dependsOn.map((t) => t.id)}
            onPick={addDependency}
            placeholder="Search tasks to depend on…"
          />
        </fieldset>
        <Accordion
          disableGutters
          sx={{ mt: 2 }}
          expanded={advancedOpen}
          onChange={(_, open) => setAdvancedOpen(open)}
          onInvalidCapture={revealInvalidAdvanced}
          slotProps={{ transition: { onEntered: reportRefusedField } }}
        >
          <AccordionSummary
            expandIcon={<ExpandMoreIcon />}
            aria-controls="new-task-advanced-content"
            id="new-task-advanced-header"
          >
            <Typography>Advanced options</Typography>
          </AccordionSummary>
          <AccordionDetails>
            {/* A specific connection for this one task. Unset, the task
                round-robins across every enabled connection of its team
                that serves its model. A pin skips that rotation and must
                advertise the model, so only those are offered; a disabled
                connection is among them, labelled pin-only, because a
                pin is the only way work reaches it. */}
            {connections.length > 0 && (
              <TextField
                select
                name="agentBackendId"
                label="Specific connection"
                value={
                  pinChoices.some((c) => c.id === agentBackendId)
                    ? agentBackendId
                    : ""
                }
                onChange={(e) => setAgentBackendId(e.target.value)}
                InputLabelProps={{ shrink: true }}
                SelectProps={{ displayEmpty: true }}
                helperText={
                  effectiveModel
                    ? `connections that serve ${effectiveModel} -- pin one only if this task needs that account`
                    : "pin this task to one connection -- leave it on any connection unless it needs that account"
                }
                fullWidth
                margin="normal"
                size="small"
              >
                <MenuItem value="">
                  Any enabled connection serving the model
                </MenuItem>
                {pinChoices.map((backend) => (
                  <MenuItem key={backend.id} value={backend.id}>
                    {connectionLabel(backend)}
                  </MenuItem>
                ))}
              </TextField>
            )}
            {/* How much of the internet this one task's sandbox may
                reach (grain/task-269). "No network" is only offered
                where a sandbox is a VM: on a host-directory deployment
                (config.sandboxShapeIgnored -- the same backend question
                the shape fields below ask) the server refuses it, so the
                whole field is disabled rather than offering the one
                choice that guarantees the run fails at sandbox setup,
                exactly as those fields are. */}
            <TextField
              select
              name="networkAccess"
              label="Network access"
              value={networkAccess}
              onChange={(e) => setNetworkAccess(e.target.value)}
              disabled={!!config?.sandboxShapeIgnored}
              // displayEmpty and the shrink with it, for the reason
              // Agent framework above needs both: "" is a real choice
              // (the deployment's own), not a blank field.
              InputLabelProps={{ shrink: true }}
              SelectProps={{ displayEmpty: true }}
              helperText={
                config?.sandboxShapeIgnored
                  ? "unavailable here: this deployment's sandboxes are directories on the daemon's own machine, which have the host's own network"
                  : "a task with no network still reaches this deployment's git proxy, and nothing else"
              }
              fullWidth
              margin="normal"
              size="small"
            >
              <MenuItem value="">
                {`Deployment default${config && config.networkAccess ? ` (${config.networkAccess === "open" ? "egress" : config.networkAccess})` : ""}`}
              </MenuItem>
              <MenuItem value="open">Egress (outbound internet only)</MenuItem>
              <MenuItem value="none">None (this deployment only)</MenuItem>
            </TextField>
            {/* What happens when this task's agent ends its run with
                nothing_needed -- looked, and found no change and no
                report anyone has to act on. Closing is the default, so
                deliberate no-ops stay out of the awaiting-ack inbox. */}
            <TextField
              select
              name="nothingNeededAction"
              label="When the agent finds nothing to do"
              value={nothingNeededAction}
              onChange={(e) => setNothingNeededAction(e.target.value)}
              fullWidth
              margin="normal"
              size="small"
            >
              <MenuItem value="close">Close</MenuItem>
              <MenuItem value="ack">Leave for acknowledgement</MenuItem>
            </TextField>
            {/* The review attached to this task (grain/task-284): once
                its own work is done, a second agent is filed from this
                template onto the same branch to read what it proposed
                and fix what is wrong with it, and the task waits for
                that before it merges. A template, not a box to type
                instructions into, because those instructions are the
                same on every task they are attached to -- write them
                once in Templates and pick them here. */}
            <TextField
              select
              name="reviewTemplateId"
              label="Review"
              value={reviewTemplateId}
              onChange={(e) => setReviewTemplateId(e.target.value)}
              // displayEmpty and the shrink with it, for the reason
              // Agent framework above needs both: "" is a real choice
              // (no review), not a blank field, and the label has to be
              // out of the way of the "No review" it displays.
              InputLabelProps={{ shrink: true }}
              SelectProps={{ displayEmpty: true }}
              helperText={
                (templates || []).length > 0
                  ? "run a second agent over this task's own code before it merges, with instructions from a template"
                  : "no templates yet -- write one in Templates to be able to attach a review here"
              }
              disabled={(templates || []).length === 0}
              fullWidth
              margin="normal"
              size="small"
            >
              {/* The templates figure in front of each name
                  (ItemGlyph.jsx), as on the schedule editor's own
                  template picker and on the Review row this choice
                  becomes in the task pane -- so the thing picked here is
                  recognisably the thing the Templates page lists. "No
                  review" names no template and holds the slot open
                  instead, GlyphLabel's own rule for a row in a glyphed
                  menu that names nothing a figure stands for. */}
              <MenuItem value="">
                <GlyphLabel kind={null}>No review</GlyphLabel>
              </MenuItem>
              {(templates || []).map((t) => (
                <MenuItem key={t.id} value={t.id}>
                  <GlyphLabel kind="templates">{templateLabel(t)}</GlyphLabel>
                </MenuItem>
              ))}
            </TextField>
            {/* The task layer of model/prompt_extension.go's three, and
                the only one that replaces rather than adds: what the
                deployment and the repo would otherwise say is dropped
                for this task. config.promptExtension is shown under it
                so that choice is made while looking at what it would
                replace -- the deployment's half of it, at least; a
                repo's own is on that repository's own page (Repos ->
                <repo> -> Agents), one repo at a time, rather than in the
                config every poll fetches. */}
            <TextField
              name="promptExtension"
              label="Prompt extension override"
              helperText="Replaces this deployment's and the repo's standing instructions for this task alone. Leave empty to use them."
              multiline
              minRows={3}
              autoComplete="off"
              fullWidth
              margin="normal"
              size="small"
              value={promptExtension}
              onChange={(e) => setPromptExtension(e.target.value)}
            />
            {config?.promptExtension && (
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{ display: "block", mb: 2, whiteSpace: "pre-wrap" }}
              >
                Deployment-wide, used when the box above is empty:{" "}
                {config.promptExtension}
              </Typography>
            )}
            {/* grain/task-26: on a deployment whose sandboxes have no
              shape of their own (config.sandboxShapeIgnored, from the
              backend that would be handed one -- ui.Config's own doc
              comment), a shape here is not ignored the way the
              deployment-wide numbers on the Settings pane are. It is
              refused: HostSandboxes.Acquire fails the whole run at
              sandbox setup with "a host-directory sandbox has no shape of
              its own", after the task has been filed and dispatched. So
              the fields are disabled rather than merely annotated -- the
              Settings pane leaves its own editable because a stored value
              is worth setting before switching a deployment over, and
              this one is applied to this task's next dispatch or not at
              all. Disabled fields are left out of FormData entirely,
              which is the 0 submit() already reads as "no override". */}
            <fieldset disabled={!!config?.sandboxShapeIgnored}>
              <legend>
                Sandbox shape override{" "}
                <span className="hint">
                  {config?.sandboxShapeIgnored
                    ? "unavailable on this deployment"
                    : "optional, granule-managed deployments only"}
                </span>
              </legend>
              {/* The sentence itself sits here rather than in the legend
                above, which is 0.72rem uppercase: a short label reads as
                one there and a two-line explanation does not. */}
              {config?.sandboxShapeIgnored && (
                <Alert severity="warning" sx={{ mb: 1, fontSize: "0.8rem" }}>
                  This deployment&apos;s sandboxes are directories on the
                  daemon&apos;s own machine, which have no CPU, memory or disk
                  shape of their own. An override here is refused rather than
                  ignored: the task would be filed, dispatched, and then fail at
                  sandbox setup.
                </Alert>
              )}
              <TextField
                name="sandboxCpus"
                label="vCPUs"
                helperText="blank/0 uses the deployment default"
                type="number"
                inputProps={{ min: 0, step: 1 }}
                autoComplete="off"
                fullWidth
                margin="normal"
                size="small"
                value={sandboxCpus}
                onChange={(e) => setSandboxCpus(e.target.value)}
              />
              <TextField
                name="sandboxMemoryMb"
                label="Memory (MiB)"
                helperText="blank/0 uses the deployment default"
                type="number"
                inputProps={{ min: 0, step: 1 }}
                autoComplete="off"
                fullWidth
                margin="normal"
                size="small"
                value={sandboxMemoryMb}
                onChange={(e) => setSandboxMemoryMb(e.target.value)}
              />
              <TextField
                name="sandboxDiskGb"
                label="Disk (GiB)"
                helperText="blank/0 uses the deployment default"
                type="number"
                inputProps={{ min: 0, step: 1 }}
                autoComplete="off"
                fullWidth
                margin="normal"
                size="small"
                value={sandboxDiskGb}
                onChange={(e) => setSandboxDiskGb(e.target.value)}
              />
            </fieldset>
          </AccordionDetails>
        </Accordion>
        <FormControlLabel
          control={
            <Checkbox
              name="approved"
              checked={approved}
              onChange={(e) => setApproved(e.target.checked)}
            />
          }
          label="Queue immediately (unchecked files it as a proposal, needing approval)"
          sx={{ display: "flex", mt: 1 }}
        />
        {/* Which end of the backlog the task joins, and the choice
            the next one will start from: the server stores it as it
            files this task (ui.CreateTaskRequest.AtFront), so
            somebody filing a run of urgent work picks "Front" once
            rather than on every task -- or drags it there
            afterwards, which is the same OrderKey either way. */}
        <TextField
          select
          label="Add to backlog"
          value={atFront ? "front" : "end"}
          onChange={(e) => setAtFront(e.target.value === "front")}
          helperText="Remembered: the next task you file starts at this end too."
          fullWidth
          margin="normal"
          size="small"
        >
          <MenuItem value="end">
            End -- runs after everything already queued
          </MenuItem>
          <MenuItem value="front">
            Front -- runs next, ahead of everything already queued
          </MenuItem>
        </TextField>
      </>
    </SidePanel>
  );
}
