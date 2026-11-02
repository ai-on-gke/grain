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
import { checkGrants, localGrantHandlers } from "./capabilityMethods.js";
import { isCapabilityRef } from "../capabilityArgs.js";
import { Fragment, useEffect, useRef, useState } from "react";
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
import WarningAmberIcon from "@mui/icons-material/WarningAmber";
import api from "../api.js";
import { wholeNumber } from "../wholeNumber.js";
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
import {
  CustomFieldInputs,
  fileableFieldValues,
  hiddenLinkValues,
  unfiledLinkValues,
} from "./CustomFields.jsx";
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
import {
  ADVANCED_ORDER,
  NEW_TASK_FORM_FIELDS,
  pickerAllows,
  pickerDisplay,
  resolveNewTaskForm,
  sortByDisplay,
} from "../newTaskForm.js";

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
  // templateId and boardId pick settings/new-task-form.yml's template
  // and board layers (newTaskForm.js): the template the form opens on
  // (a template page's "New task from this template", which the Template
  // picker below can still change), and the board whose "+ New task"
  // opened the form.
  templateId = "",
  boardId = "",
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
  // The form as settings/new-task-form.yml has it for the page this was
  // opened from: what every field starts on (resolveNewTaskForm). With
  // no configuration it is the form's own built-in defaults.
  const openingForm = resolveNewTaskForm(config, {
    repo: defaultRepo || "",
    templateId,
    boardId,
  });
  const formRepoDefault =
    typeof openingForm.fields.repo.default === "string"
      ? openingForm.fields.repo.default.trim()
      : "";
  const formNoRepo =
    !draft && !defaultRepo && formRepoDefault.toLowerCase() === "none";
  const openingRepo = defaultRepo || (formNoRepo ? "" : formRepoDefault) || "";
  const openingDefault = (id) => openingForm.fields[id].default;
  // formEdited is every field somebody has changed in this form -- or
  // that a restored draft holds -- which a change of repo or template
  // leaves alone when it re-applies the configured defaults (below), the
  // way baseEdited and capabilitiesEdited do for their own fields.
  const formEdited = useRef(new Set(draft ? Object.keys(draft) : []));
  const edit = (id, set) => (value) => {
    formEdited.current.add(id);
    set(value);
  };
  // draftDiscarded hides the restored draft's own leftovers -- the repo
  // the picker remounts on, and the notice above Repos -- once Discard
  // draft has been pressed. initialDraft itself is left alone: emptying
  // it would only make the next render load whatever is saved next.
  const [draftDiscarded, setDraftDiscarded] = useState(false);
  // Names alone where they are unique, more where they are not
  // (templateLabels.js).
  const templateLabel = templateLabeler(templates);
  // fromTemplate is the template this task is filed from: the Template
  // picker's own value, starting on the one the form was opened on. It
  // picks the templates: layer every field resolves through, and is
  // filed as templateId, where the server fills whatever the form left
  // blank from the template (ui.Client.fromTemplate) after applying the
  // layer's hidden defaults -- so a field this form states, a hidden
  // default included, beats the template's own value for it.
  const [fromTemplate, setFromTemplate] = useState(templateId);
  // The templates the picker offers: those with no input fields of
  // their own, which this form has nowhere to fill in ("Use…" on the
  // Templates page does), plus whichever one is picked now.
  const formTemplates = (templates || []).filter(
    (t) => !(t.fields || []).length || t.id === fromTemplate,
  );
  const pickedTemplate =
    (templates || []).find((t) => t.id === fromTemplate) || null;
  // The picker is drawn where a template can matter to this form: it
  // was opened from one, or settings/new-task-form.yml gives some
  // template a layer of its own. Everywhere else it would be one more
  // field nobody asked for.
  const templatePickerShown =
    formTemplates.length > 0 &&
    (templateId !== "" ||
      Object.keys(config?.newTaskForm?.templates || {}).length > 0);
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
    return openingRepo;
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
    () => Boolean(draft?.noRepo) || Boolean(draft?.changeTarget) || formNoRepo,
  );
  // The form for the repo picked now: the fields' placements, labels and
  // defaults, and the capability picker's configuration.
  const taskForm = resolveNewTaskForm(config, {
    repo: noRepo ? "" : repo,
    templateId: fromTemplate,
    boardId,
  });
  const F = taskForm.fields;
  // What a /new link asked for that this form leaves out: the values
  // the declarations would refuse, and -- where settings/new-task-form.yml
  // hides the custom fields -- every value it carried for them, since a
  // hidden field files its default. Drawn beside the custom fields when
  // they are shown, and at the top of the form otherwise, so neither a
  // collapsed "Advanced options" nor a hidden field swallows it.
  const customFieldsHidden = F.customFields.visibility === "hidden";
  const linkNotice = [
    ...unfiledLinkValues(config, fields),
    ...(customFieldsHidden ? hiddenLinkValues(config, fields) : []),
  ];
  // The repo's own block holds its branch, its base and the other repos
  // it writes, as long as they sit where it does and right after it.
  const inRepoGroup = (id) =>
    ["newBranch", "base", "writes"].includes(id) &&
    F.repo.visibility !== "hidden" &&
    F[id].visibility === F.repo.visibility &&
    F[id].order > F.repo.order &&
    F[id].order < F.repo.order + 20;
  // capabilityDefaultsFor is what the picker starts on for a repo:
  // settings/new-task-form.yml's capabilities.defaults where a layer
  // names them, and otherwise the catalogue's own (defaultCapabilitiesFor).
  const capabilityDefaultsFor = (r) =>
    resolveNewTaskForm(config, { repo: r, templateId: fromTemplate, boardId })
      .capabilities.defaults ?? defaultCapabilitiesFor(config, r);
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
    if (openingForm.fields.base.defaultSet)
      return String(openingDefault("base"));
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
  const [newBranch, setNewBranch] = useState(
    () => draft?.newBranch ?? !!openingDefault("newBranch"),
  );
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
  // settings/new-task-form.yml's repo.offered is the target list where a
  // layer names one (the repo already picked kept in it), and
  // allow_change_targets: false leaves the non-git change targets out.
  const formTargetOptions = Array.isArray(F.repo.offered)
    ? [...new Set([...F.repo.offered, ...(repo && !noRepo ? [repo] : [])])]
    : targetOptions;
  const formChangeTargets =
    F.repo.allowChangeTargets === false ? [] : changeTargets;

  // dependsOn is picked tasks ({id, title}), not just ids -- keeping the
  // title lets the chips below the picker read as "task 12 Fix the
  // thing" instead of a bare number nobody can place.
  const [dependsOn, setDependsOn] = useState(
    () =>
      draft?.dependsOn ?? asDependencies(openingDefault("dependsOn"), tasks),
  );
  // reads is the read-only repos picked below, held here rather than
  // left as a form field for the reason capabilities and dependsOn are:
  // its picker (ReadOnlyReposField) is a search box over knownRepos, not
  // an <input> FormData could read the answer off.
  const [reads, setReads] = useState(
    () => draft?.reads ?? asList(openingDefault("reads")),
  );
  // writes is the extra writeable repos picked below ({repo, base}
  // objects, ui.CreateTaskRequest.Writes), held in state for reads' own
  // reason -- and because each row carries a branch box whose value
  // belongs with the repo it sits beside rather than in a form field
  // named after neither of them.
  const [writes, setWrites] = useState(
    () => draft?.writes ?? asWrites(openingDefault("writes")),
  );
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
    return capabilityDefaultsFor(openingRepo);
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
  // settings/new-task-form.yml's capabilities.offered and .hidden narrow
  // what is offered (one already ticked is kept, to be unticked), and
  // .display relabels and orders it.
  const formCapabilities = taskForm.capabilities;
  const capabilityOptions = sortByDisplay(
    formCapabilities,
    withoutApiRows(capabilityRows(config?.capabilities, heldCapabilityIds))
      .filter(
        (c) =>
          pickerAllows(formCapabilities, c.id) || capabilityIds.includes(c.id),
      )
      .map((c) => {
        const d = pickerDisplay(formCapabilities, c.id);
        return d
          ? {
              ...c,
              name: d.label || c.name,
              description: d.description || c.description,
            }
          : c;
      }),
    (c) => c.id,
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
  // catalogueRefusals is, per grant held above, why
  // settings/capabilities.yml refuses it to the task this form would
  // file (POST /api/capability-grants/check with a subject): a default
  // restricted to other repos, say, on a task with No repo. Each is
  // marked where it is ticked, before Create rather than as the 400
  // filing would answer. catalogueSubject is that task's repo -- "" for
  // none -- and null while Repos holds nothing the server could judge
  // (blank, or an Other… name still being typed).
  const policyTarget = noRepo
    ? ""
    : (repo || "").trim() || config?.defaultTarget || "";
  const catalogueSubject = noRepo
    ? ""
    : /^[^/\s]+\/[^/\s]+$/.test(policyTarget)
      ? policyTarget
      : null;
  const [catalogueRefusals, setCatalogueRefusals] = useState({});
  const grantsKey = capabilities.join("\n");
  useEffect(() => {
    const none = (prev) => (Object.keys(prev).length ? {} : prev);
    if (catalogueSubject === null || grantsKey === "") {
      setCatalogueRefusals(none);
      return undefined;
    }
    let current = true;
    Promise.resolve()
      .then(() =>
        checkGrants(
          grantsKey.split("\n").map((grant) => ({ grant })),
          { repo: catalogueSubject },
        ),
      )
      .then((res) => {
        if (!current || !Array.isArray(res)) return;
        const out = {};
        res.forEach((r) => {
          if (r && r.catalogueRefusal) out[r.grant] = r.catalogueRefusal;
        });
        setCatalogueRefusals(out);
      })
      .catch(() => {
        if (current) setCatalogueRefusals(none);
      });
    return () => {
      current = false;
    };
  }, [grantsKey, catalogueSubject]);
  const refusedGrants = capabilities.filter((g) => catalogueRefusals[g]);
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
  const [model, setModel] = useState(
    () => draft?.model ?? String(openingDefault("model") ?? ""),
  );
  const [agentBackendId, setAgentBackendId] = useState(
    () => draft?.agentBackendId ?? String(openingDefault("agentBackendId")),
  );
  const connections = config?.agentBackends || [];
  const models = connectionModels(connections);
  const teamDefaultModels = defaultModels(deployment);
  const teamDefaultModel = deployment?.defaultModel || "";
  // The model(s) a pin is checked against: the one chosen here, or else the
  // team's default when it has one.
  const effectiveModels = model ? [model] : teamDefaultModels;
  const effectiveModel = effectiveModels.join(" → ");
  // settings/new-task-form.yml's model.offered and networkAccess.offered
  // narrow those pickers where a layer names them, the current choice
  // kept so it can still be shown.
  const formModels = Array.isArray(F.model.offered)
    ? models.filter((m) => F.model.offered.includes(m.id) || m.id === model)
    : models;
  const networkOffered = (v) =>
    !Array.isArray(F.networkAccess.offered) ||
    F.networkAccess.offered.includes(v) ||
    networkAccess === v;
  const pinChoices = connectionsServingModels(
    models,
    connections,
    effectiveModels,
  );
  const [networkAccess, setNetworkAccess] = useState(
    () => draft?.networkAccess ?? String(openingDefault("networkAccess")),
  );
  // What a run ending on nothing_needed does to this task
  // (model.Task.NothingNeededAction): "close" -- the default, and what
  // the server reads "" as -- or "ack" to leave it for acknowledgement.
  const [nothingNeededAction, setNothingNeededAction] = useState(
    () =>
      draft?.nothingNeededAction ??
      String(openingDefault("nothingNeededAction")),
  );
  const [reviewTemplateId, setReviewTemplateId] = useState(
    () => draft?.reviewTemplateId ?? String(openingDefault("reviewTemplateId")),
  );
  const [reviewers, setReviewers] = useState(() =>
    Array.isArray(draft?.reviewers)
      ? draft.reviewers.join(", ")
      : (draft?.reviewers ?? ""),
  );
  const [promptExtension, setPromptExtension] = useState(
    () => draft?.promptExtension ?? String(openingDefault("promptExtension")),
  );
  const openingShape = sandboxShapeDefault(openingForm.fields.sandboxShape);
  const [sandboxCpus, setSandboxCpus] = useState(
    () => draft?.sandboxCpus ?? openingShape.cpus,
  );
  const [sandboxMemoryMb, setSandboxMemoryMb] = useState(
    () => draft?.sandboxMemoryMb ?? openingShape.memoryMb,
  );
  const [sandboxDiskGb, setSandboxDiskGb] = useState(
    () => draft?.sandboxDiskGb ?? openingShape.diskGb,
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
    () => draft?.autoMerge ?? !!openingDefault("autoMerge"),
  );
  const [approved, setApproved] = useState(
    () => draft?.approved ?? !!openingDefault("approved"),
  );
  // noPort is model.Task.NoPort: a change that belongs only on the
  // branch it lands on, and is not ported to the repo's paired branches.
  const [noPort, setNoPort] = useState(
    () => draft?.noPort ?? !!openingDefault("noPort"),
  );
  const [keepRunning, setKeepRunning] = useState(() => !!draft?.keepRunning);
  // fixesBaseBranch is ui.CreateTaskRequest.FixesBaseBranch: this task
  // fixes its base branch while that branch is red, so grain never makes
  // it wait on the branch's own repair task (grain/task-3451).
  const [fixesBaseBranch, setFixesBaseBranch] = useState(
    () => draft?.fixesBaseBranch ?? !!openingDefault("fixesBaseBranch"),
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
    () => draft?.atFront ?? !!openingDefault("atFront"),
  );
  const [priority, setPriority] = useState(() =>
    draft?.priority !== undefined ? String(draft.priority) : "5",
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
    if (repo !== openingRepo) return true;
    if (
      baseEdited.current &&
      base !== lastBaseForRepo(tasks, defaultRepo || "")
    )
      return true;
    if (
      JSON.stringify(reads) !== JSON.stringify(asList(openingDefault("reads")))
    )
      return true;
    if (
      JSON.stringify(writes) !==
      JSON.stringify(asWrites(openingDefault("writes")))
    )
      return true;
    if (dependsOn.length > asList(openingDefault("dependsOn")).length)
      return true;
    if (capabilitiesEdited.current) return true;
    if (model !== String(openingDefault("model") ?? "")) return true;
    if (agentBackendId !== String(openingDefault("agentBackendId")))
      return true;
    if (networkAccess !== String(openingDefault("networkAccess"))) return true;
    if (nothingNeededAction !== String(openingDefault("nothingNeededAction")))
      return true;
    if (reviewTemplateId !== String(openingDefault("reviewTemplateId")))
      return true;
    if (
      promptExtension.trim() !==
      String(openingDefault("promptExtension")).trim()
    )
      return true;
    const shapeDiffers = (v, d) => String(v || 0) !== String(d || 0);
    if (shapeDiffers(sandboxCpus, openingShape.cpus)) return true;
    if (shapeDiffers(sandboxMemoryMb, openingShape.memoryMb)) return true;
    if (shapeDiffers(sandboxDiskGb, openingShape.diskGb)) return true;
    if (autoMerge !== !!openingDefault("autoMerge")) return true;
    if (approved !== !!openingDefault("approved")) return true;
    if (noPort !== !!openingDefault("noPort")) return true;
    if (keepRunning) return true;
    if (fixesBaseBranch !== !!openingDefault("fixesBaseBranch")) return true;
    if (newBranch !== !!openingDefault("newBranch")) return true;
    if (newBranchName.trim() !== "") return true;
    if (reviewers.trim() !== "") return true;
    if (atFront !== !!openingDefault("atFront")) return true;
    if (String(priority).trim() !== "5") return true;
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
        keepRunning,
        fixesBaseBranch,
        atFront,
        priority,
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
    keepRunning,
    fixesBaseBranch,
    atFront,
    priority,
    customFields,
  ]);

  // Switching repo or template re-applies the defaults
  // settings/new-task-form.yml gives the fields nobody has touched in
  // this form (formEdited), as Base branch and the capability picker
  // already follow the repo. Only on a configured form, and only for
  // defaults a layer chose: the built-in ones are where the form opened.
  const formDefaultsKey = taskForm.configured
    ? JSON.stringify([
        NEW_TASK_FORM_FIELDS.map((id) =>
          F[id].defaultSet ? [id, F[id].default, F[id].defaultPreset] : null,
        ),
        taskForm.capabilities.defaults,
      ])
    : "";
  useEffect(() => {
    if (!formDefaultsKey) return;
    const setters = {
      description: (v) => setDescription(String(v ?? "")),
      newBranch: (v) => setNewBranch(!!v),
      reads: (v) => setReads(asList(v)),
      writes: (v) => setWrites(asWrites(v)),
      autoMerge: (v) => setAutoMerge(!!v),
      noPort: (v) => setNoPort(!!v),
      fixesBaseBranch: (v) => setFixesBaseBranch(!!v),
      model: (v) => setModel(String(v ?? "")),
      dependsOn: (v) => setDependsOn(asDependencies(v, tasks)),
      agentBackendId: (v) => setAgentBackendId(String(v ?? "")),
      networkAccess: (v) => setNetworkAccess(String(v ?? "")),
      nothingNeededAction: (v) => setNothingNeededAction(String(v ?? "")),
      reviewTemplateId: (v) => setReviewTemplateId(String(v ?? "")),
      promptExtension: (v) => setPromptExtension(String(v ?? "")),
      approved: (v) => setApproved(!!v),
      atFront: (v) => setAtFront(!!v),
    };
    for (const [id, set] of Object.entries(setters)) {
      if (F[id].defaultSet && !formEdited.current.has(id)) set(F[id].default);
    }
    if (F.base.defaultSet && !baseEdited.current)
      setBase(String(F.base.default ?? ""));
    if (F.sandboxShape.defaultSet && !formEdited.current.has("sandboxShape")) {
      const shape = sandboxShapeDefault(F.sandboxShape);
      setSandboxCpus(shape.cpus);
      setSandboxMemoryMb(shape.memoryMb);
      setSandboxDiskGb(shape.diskGb);
    }
    if (taskForm.capabilities.defaults && !capabilitiesEdited.current)
      setCapabilities(capabilityDefaultsFor(noRepo ? "" : repo));
    // Keyed on the resolved defaults alone: they are what changes when
    // the repo or the template does.
  }, [formDefaultsKey]);

  // hiddenPayload is what the fields the resolved form hides file as:
  // their defaults, whatever the form's state holds (the server applies
  // the same, ui.applyHiddenFormDefaults).
  const hiddenPayload = () => {
    const out = {};
    const hidden = (id) => F[id].visibility === "hidden" && F[id].defaultSet;
    const d = (id) => F[id].default;
    if (hidden("description")) out.description = String(d("description") ?? "");
    if (hidden("base")) out.base = String(d("base") ?? "");
    for (const id of [
      "autoMerge",
      "noPort",
      "fixesBaseBranch",
      "approved",
      "atFront",
    ])
      if (hidden(id)) out[id] = !!d(id);
    for (const id of [
      "model",
      "agentBackendId",
      "networkAccess",
      "nothingNeededAction",
      "reviewTemplateId",
      "promptExtension",
    ])
      if (hidden(id)) out[id] = String(d(id) ?? "");
    if (hidden("reads")) out.reads = asList(d("reads"));
    if (hidden("writes")) out.writes = asWrites(d("writes"));
    if (hidden("dependsOn")) out.dependsOn = asList(d("dependsOn"));
    if (hidden("grants")) out.capabilities = asList(d("grants"));
    if (F.sandboxShape.visibility === "hidden" && F.sandboxShape.defaultSet) {
      const shape = sandboxShapeDefault(F.sandboxShape);
      out.sandboxCpus = wholeNumber(shape.cpus) || 0;
      out.sandboxMemoryMb = wholeNumber(shape.memoryMb) || 0;
      out.sandboxDiskGb = wholeNumber(shape.diskGb) || 0;
    }
    return out;
  };

  const handleDiscard = () => {
    discardedRef.current = true;
    clearTaskDraft();
    setDraftDiscarded(true);
    repoEdited.current = false;
    setTitle("");
    setDescription("");
    setRepo(openingRepo);
    setNoRepo(false);
    setChangeTarget("");
    setBase(lastBaseForRepo(tasks, defaultRepo || ""));
    baseEdited.current = false;
    setNewBranch(!!openingDefault("newBranch"));
    setNewBranchName("");
    setDependsOn(asDependencies(openingDefault("dependsOn"), tasks));
    setReads(asList(openingDefault("reads")));
    setWrites(asWrites(openingDefault("writes")));
    setCapabilities(capabilityDefaultsFor(openingRepo));
    capabilitiesEdited.current = false;
    formEdited.current.clear();
    setFromTemplate(templateId);
    setCustomFields(fileableFieldValues(config, fields));
    setAttachments([]);
    setModel(String(openingDefault("model") ?? ""));
    setAgentBackendId(String(openingDefault("agentBackendId")));
    setNetworkAccess(String(openingDefault("networkAccess")));
    setNothingNeededAction(String(openingDefault("nothingNeededAction")));
    setReviewTemplateId(String(openingDefault("reviewTemplateId")));
    setReviewers("");
    setPromptExtension(String(openingDefault("promptExtension")));
    setSandboxCpus(openingShape.cpus);
    setSandboxMemoryMb(openingShape.memoryMb);
    setSandboxDiskGb(openingShape.diskGb);
    setAutoMerge(!!openingDefault("autoMerge"));
    setApproved(!!openingDefault("approved"));
    setNoPort(!!openingDefault("noPort"));
    setKeepRunning(false);
    setFixesBaseBranch(!!openingDefault("fixesBaseBranch"));
    setAtFront(!!openingDefault("atFront"));
    setPriority("5");
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
    if (!capabilitiesEdited.current) setCapabilities(capabilityDefaultsFor(r));
    if (none || baseEdited.current) return;
    const hasHistory = (tasks || []).some(
      (t) => t.repo === r && suggestsBase(t),
    );
    if (hasHistory) setBase(lastBaseForRepo(tasks, r));
  };

  const addDependency = (t) => {
    formEdited.current.add("dependsOn");
    setDependsOn((prev) =>
      prev.some((p) => p.id === t.id) ? prev : [...prev, t],
    );
  };
  const removeDependency = (id) => {
    formEdited.current.add("dependsOn");
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
      ...(keepRunning ? { keepRunning: true } : {}),
      // Likewise only when ticked, and never for a task with no repo,
      // which has no base branch to fix.
      ...(fixesBaseBranch && !none ? { fixesBaseBranch: true } : {}),
      sandboxCpus: wholeNumber(sandboxCpus) || 0,
      sandboxMemoryMb: wholeNumber(sandboxMemoryMb) || 0,
      sandboxDiskGb: wholeNumber(sandboxDiskGb) || 0,
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
      // Named only once somebody has changed the picker. Until then
      // what it shows is this form's copy of the defaults, and the
      // server's own are what the task gets: it applies them for the
      // target it files the task against and drops any default the
      // capability catalogue refuses there (catalogueFilingGrants),
      // where a list naming the same ids is somebody asking for them,
      // and refused. A No repo task under a gcp-key restricted to one
      // repo was refused that way, with nothing on the form to untick
      // the reason from (2026-10-29 test run, Bug 3).
      // A form whose configuration names its own capability defaults
      // files them too -- the server knows only the catalogue's -- less
      // any the catalogue refuses this task, as the server would drop.
      ...(capabilitiesEdited.current
        ? { capabilities }
        : taskForm.capabilities.defaults
          ? { capabilities: capabilities.filter((g) => !catalogueRefusals[g]) }
          : {}),
      // Every field the form rendered, blanks included: the server
      // drops the empty ones (model.NormalizeCustomFieldValues), so
      // what is filed is exactly what was filled in. Left out of the
      // request entirely on a deployment that declares none, which is
      // most of them -- an empty map would be a key on every filing
      // saying nothing.
      // A form that hides its custom fields files their declared
      // defaults, never values a /new link put there out of sight
      // (linkNotice says which were left out).
      ...(Object.keys(customFields).length > 0
        ? {
            customFields: customFieldsHidden
              ? fileableFieldValues(config, null)
              : customFields,
          }
        : {}),
      dependsOn: dependsOn.map((t) => (typeof t === "object" && t ? t.id : t)),
      reads,
      writes: ct ? [] : writes,
      approved,
      atFront,
      ...(() => {
        const parsedPriority = wholeNumber(priority);
        return !Number.isNaN(parsedPriority) && parsedPriority !== 5
          ? { priority: parsedPriority }
          : {};
      })(),
      ...(fromTemplate ? { templateId: fromTemplate } : {}),
      ...(boardId ? { boardId } : {}),
      ...(taskForm.configured ? hiddenPayload() : {}),
    };
    try {
      // Read inside the try: a file that has gone away since it was
      // picked fails here, and that has to land in showError and release
      // the Create button like any other failed filing.
      payload.attachments = await Promise.all(
        attachments.map((f) => fileToAttachment(f)),
      );
      const created = await api("/api/tasks", {
        method: "POST",
        body: JSON.stringify(payload),
      });
      clearTaskDraft();
      form.reset();
      setTitle("");
      setDescription("");
      setRepo(openingRepo);
      repoEdited.current = false;
      setNoRepo(false);
      setChangeTarget("");
      setBase(lastBaseForRepo(tasks, defaultRepo || ""));
      baseEdited.current = false;
      setNewBranch(!!openingDefault("newBranch"));
      setNewBranchName("");
      setDependsOn(asDependencies(openingDefault("dependsOn"), tasks));
      setReads(asList(openingDefault("reads")));
      // Emptied with the rest of the repos block: the extra repos were
      // this task's, and the form is being cleared for the next one.
      setWrites(asWrites(openingDefault("writes")));
      setCapabilities(capabilityDefaultsFor(openingRepo));
      capabilitiesEdited.current = false;
      formEdited.current.clear();
      setFromTemplate(templateId);
      // Back to the declared defaults rather than to what this filing
      // carried: the link's values were for the task just filed, and
      // the form is being emptied for the next one.
      setCustomFields(fileableFieldValues(config, null));
      setAttachments([]);
      setModel(String(openingDefault("model") ?? ""));
      setAgentBackendId(String(openingDefault("agentBackendId")));
      setNetworkAccess(String(openingDefault("networkAccess")));
      setNothingNeededAction(String(openingDefault("nothingNeededAction")));
      setReviewTemplateId(String(openingDefault("reviewTemplateId")));
      setReviewers("");
      setPromptExtension(String(openingDefault("promptExtension")));
      setSandboxCpus(openingShape.cpus);
      setSandboxMemoryMb(openingShape.memoryMb);
      setSandboxDiskGb(openingShape.diskGb);
      setAutoMerge(!!openingDefault("autoMerge"));
      setApproved(!!openingDefault("approved"));
      setNoPort(!!openingDefault("noPort"));
      setKeepRunning(false);
      setFixesBaseBranch(!!openingDefault("fixesBaseBranch"));
      setAtFront(!!openingDefault("atFront"));
      setPriority("5");
      onClose();
      // The filed task, for what its answer says beyond "filed" -- the
      // default grants the capability policy kept off it
      // (DroppedDefaultsNotice).
      await onCreated(created);
    } catch (err) {
      submittingRef.current = false;
      setSubmitting(false);
      showError(err);
    }
  };

  const titleNode = (
    <>
      {/* Focused as the form opens, as New session's and New team's
            first fields are: without it the dialog's own paper held
            focus, so a title typed straight after "+ New task" went
            nowhere and the box was a Tab past "Close dialog" (found in
            the 2026-10-15 intake walkthrough). */}
      <TextField
        name="title"
        label={F.title.label || "Title"}
        helperText={
          F.title.helpText ||
          (pickedTemplate
            ? "left blank, the task takes the template's own title"
            : undefined)
        }
        placeholder={pickedTemplate?.title || undefined}
        required={!pickedTemplate}
        InputLabelProps={{ required: false }}
        autoFocus
        autoComplete="off"
        fullWidth
        margin="normal"
        value={title}
        onChange={(e) => setTitle(e.target.value)}
      />
    </>
  );
  const descriptionField = (
    <TextField
      name="description"
      label={F.description.label || "Description"}
      helperText={F.description.helpText || undefined}
      multiline
      minRows={6}
      fullWidth
      value={description}
      onChange={(e) => edit("description", setDescription)(e.target.value)}
      InputProps={{ sx: { pb: 4 } }}
    />
  );
  const descriptionNode = (
    <>
      {/* minRows, not rows: the box grows with whatever is typed into
            it rather than keeping a fixed five lines and scrolling
            inside itself, so a long description is read in the panel's
            own scroll rather than in a second scrollbar within it
            (grain/task-85). The clip that attaches files sits in its
            bottom-right corner, which is what the padding leaves room
            for. */}
      {F.attachments.visibility === "hidden" ? (
        descriptionField
      ) : (
        <AttachmentPicker
          files={attachments}
          onChange={setAttachments}
          variant="icon"
        >
          {descriptionField}
        </AttachmentPicker>
      )}
    </>
  );
  const linkNoticeNode = linkNotice.length > 0 && (
    <Alert severity="warning" sx={{ mt: 1 }} data-testid="unfiled-link-values">
      This link asked for values this form has left out: {linkNotice.join("; ")}
      .
    </Alert>
  );
  const customFieldsNode = (
    <>
      {/* This deployment's own fields, above the repo and the rest of
            the mechanics: they are part of what the task *is*, like the
            title and the description over them, rather than part of how
            it runs. Nothing renders here on a deployment that declares
            none, which is most of them. */}
      {/* What a /new link asked for that this form left out
            (unfiledLinkValues): a value its field does not offer, or a
            field this deployment does not declare. Said here, beside the
            fields, so the task filed is not quietly a different one from
            the task the link's sender meant -- and at the top of the form
            when these fields are tucked away or hidden (linkNotice). */}
      {F.customFields.visibility === "shown" && linkNoticeNode}
      <CustomFieldInputs
        config={config}
        values={customFields}
        onChange={setCustomField}
      />
    </>
  );
  const newBranchNode = (
    <>
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
              onChange={(e) =>
                edit("newBranch", setNewBranch)(e.target.checked)
              }
            />
          }
          label={F.newBranch.label || "Create a new branch"}
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
    </>
  );
  const baseNode = (
    <>
      {!changeTarget && (
        <>
          {/* A non-git change target task has no branch and lands nowhere
              else: its change is one changelist. */}
          <BranchField
            name="base"
            label={
              F.base.label ||
              (!noRepo && newBranch ? "Create from" : "Base branch")
            }
            helperText={
              F.base.helpText ||
              (!noRepo && newBranch
                ? "optional -- the repo's default branch if blank"
                : "optional")
            }
            margin="dense"
            size="medium"
            repo={noRepo ? "" : repo}
            options={branchesInUse(tasks, repo)}
            warnMissing="push it first, or have the task depend on the one that cuts it"
            // A pull request's head as GitHub shows it ("owner:branch")
            // is filed against that pull request (pkg/forkhead); a new
            // branch cannot be cut from one, which the server says.
            forkHeads={noRepo || !newBranch}
            value={base}
            onChange={(next) => {
              baseEdited.current = true;
              setBase(next);
            }}
          />
        </>
      )}
    </>
  );
  const writesNode = (
    <>
      {!changeTarget && (
        <>
          <WriteReposField
            options={writeOptions}
            value={writes}
            onChange={edit("writes", setWrites)}
          />
        </>
      )}
    </>
  );
  const repoNode = (
    <>
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
          <Alert severity="info" sx={{ mb: 1 }} data-testid="draft-repo-notice">
            {draftRepoNotice}. Discard it to file in {pageRepo}.
          </Alert>
        )}
        <Box component="label" sx={{ display: "block", mb: 0.5 }}>
          <Typography
            variant="caption"
            color="text.secondary"
            sx={{ display: "block", mb: 0.5 }}
          >
            {F.repo.label || "Repos"}{" "}
            <span className="hint">
              {F.repo.helpText ||
                "the first is the one this task's own work lands in"}
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
            options={formTargetOptions}
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
                  : formNoRepo
                    ? NO_REPO
                    : openingRepo
            }
            allowNoRepo={F.repo.allowNoRepo ?? true}
            changeTargets={formChangeTargets}
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
                The deployment&apos;s GitHub credential cannot push to {repo} --
                give it access on GitHub, or choose another in Settings → Git
                connections.
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
              {repo} is not on this deployment&apos;s target repo list. The task
              will be filed but parked, and runs only once an operator adds the
              repo under Settings and replies on it.
            </Typography>
          )}
        </Box>
        {inRepoGroup("newBranch") && newBranchNode}
        {inRepoGroup("base") && baseNode}
        {inRepoGroup("writes") && writesNode}
      </Box>
    </>
  );
  const readsNode = (
    <>
      <ReadOnlyReposField
        options={repoOptions}
        value={reads}
        onChange={edit("reads", setReads)}
        // The repo picked right above, not a saved one: this form is
        // where both halves are chosen, so the read-only picker should
        // stop offering the target the moment it is named -- the same
        // value WriteReposField gets, and "" for a task filed with no
        // repo at all, which has no target for a read to collide with.
        target={noRepo ? "" : repo}
      />
    </>
  );
  const reviewersNode = (
    <>
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
    </>
  );
  const changeTargetNoteNode = (
    <>
      {changeTarget && (
        <>
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
        </>
      )}
    </>
  );
  const autoMergeNode = (
    <>
      {!changeTarget && (
        <>
          <FormControlLabel
            control={
              <Checkbox
                name="autoMerge"
                checked={autoMerge}
                onChange={(e) =>
                  edit("autoMerge", setAutoMerge)(e.target.checked)
                }
              />
            }
            label={F.autoMerge.label || "Auto-merge once checks pass"}
            sx={{ display: "flex", mt: 1 }}
          />
        </>
      )}
    </>
  );
  const noPortNode = (
    <>
      {!changeTarget && (
        <>
          <FormControlLabel
            control={
              <Checkbox
                name="noPort"
                checked={noPort}
                onChange={(e) => edit("noPort", setNoPort)(e.target.checked)}
              />
            }
            label={F.noPort.label || "Don't port to the repo's paired branches"}
            sx={{ display: "flex" }}
          />
        </>
      )}
    </>
  );
  const fixesBaseBranchNode = (
    <>
      {!noRepo && (
        <FormControlLabel
          control={
            <Checkbox
              name="fixesBaseBranch"
              checked={fixesBaseBranch}
              onChange={(e) =>
                edit("fixesBaseBranch", setFixesBaseBranch)(e.target.checked)
              }
            />
          }
          label={
            F.fixesBaseBranch.label ||
            `This fixes a red ${(newBranch ? newBranchName.trim() : base) || "main"}`
          }
          title="While the branch a task lands on is red, grain makes the work aimed at it wait on that branch's repair task. Tick this for a task that is itself fixing the branch -- a de-flake, a broken build -- so it never waits: it counts as the branch's repair, and is left open with a comment, not closed, when the branch goes green."
          sx={{ display: "flex" }}
        />
      )}
    </>
  );
  const keepRunningNode = (
    <>
      <FormControlLabel
        control={
          <Checkbox
            name="keepRunning"
            checked={keepRunning}
            onChange={(e) => setKeepRunning(e.target.checked)}
          />
        }
        label="Keep running"
        title="Immediately restart the task when its run exits instead of completing or parking it."
        sx={{ display: "flex" }}
      />
    </>
  );
  const grantsNode = (
    <>
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
            setCapabilities([...e.target.value, ...apiIds, ...capabilityRefs]);
          }}
          renderValue={(selected) => (
            <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.5 }}>
              {selected.map((id) => (
                <GrantChip
                  key={id}
                  grant={id}
                  config={config}
                  tabIndex={-1}
                  // Marked by its border and icon, its label left at
                  // full contrast: warning-coloured text on a light
                  // chip is too faint to read at this size.
                  {...(catalogueRefusals[id]
                    ? {
                        note: catalogueRefusals[id],
                        variant: "outlined",
                        icon: (
                          <WarningAmberIcon fontSize="small" color="warning" />
                        ),
                        sx: { borderColor: "warning.main" },
                        ariaSuffix: " (refused here)",
                        "data-catalogue-refused": "true",
                      }
                    : {})}
                />
              ))}
            </Box>
          )}
        >
          {capabilityOptions.length === 0 && (
            <MenuItem disabled>{NO_CAPABILITIES_HINT}</MenuItem>
          )}
          {capabilityOptions.map((c) => {
            const refused = catalogueRefusals[c.id] || "";
            const unavailable = refused || capabilityUnavailableHint(c);
            return (
              <MenuItem
                key={c.id}
                value={c.id}
                // One the catalogue refuses here is not offered, but
                // one already ticked stays enabled: its own row is
                // the only way to untick it.
                disabled={Boolean(refused) && !capabilityIds.includes(c.id)}
                data-catalogue-refused={refused ? "true" : undefined}
                title={
                  unavailable
                    ? `${c.description}\n\n${unavailable}`
                    : c.description
                }
              >
                <Checkbox checked={capabilityIds.includes(c.id)} size="small" />
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
        {refusedGrants.length > 0 && (
          <FormHelperText
            data-testid="new-task-catalogue-refused"
            sx={{ color: "warning.main" }}
          >
            {capabilitiesEdited.current
              ? `settings/capabilities.yml refuses ${refusedGrants.join(", ")} on ${noRepo ? "a task with no repo" : policyTarget}: untick ${refusedGrants.length === 1 ? "it" : "them"}, or the task will not be filed.`
              : `settings/capabilities.yml refuses ${refusedGrants.join(", ")} on ${noRepo ? "a task with no repo" : policyTarget}, so ${refusedGrants.length === 1 ? "that default is" : "those defaults are"} left off this task.`}
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
        policyRepo={noRepo ? "" : repo || config?.defaultTarget || ""}
        formCapabilities={taskForm.configured ? formCapabilities : null}
        label={F.grants.label || undefined}
        helpText={F.grants.helpText || undefined}
      />
    </>
  );
  const modelNode = (
    <>
      {/* The model this one task runs on. Its choices are what the
            target team's connections advertise; a model only disabled
            connections serve is still offered, and says so, because
            pinning one of those (Advanced options) is how it runs. Left
            out where the team has no connection at all. */}
      {models.length > 0 && (
        <ModelSelect
          name="model"
          label={F.model.label || "Model"}
          value={model}
          onChange={(next) => {
            edit("model", setModel)(next);
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
              setAgentBackendId(String(openingDefault("agentBackendId")));
          }}
          options={modelOptions(
            formModels,
            teamDefaultLabel(deployment || teamDefaultModel, models),
            model,
          )}
          helperText={
            F.model.helpText ||
            "which model this one task runs on -- leave it on the deployment's default unless it needs a particular one; type to search"
          }
          margin="normal"
        />
      )}
    </>
  );
  const dependsOnNode = (
    <>
      <fieldset>
        <legend>
          {F.dependsOn.label || "Depends on"}{" "}
          <span className="hint">{F.dependsOn.helpText || "optional"}</span>
        </legend>
        {dependsOn.length > 0 && (
          // One full-width chip per line, the shape DetailOverlay's
          // dependency list uses: a picked task then reads as a row,
          // rather than a bubble sized by however long its title
          // happens to be, wrapped in beside the others.
          <Stack spacing={0.6} sx={{ mb: 1 }}>
            {dependsOn.map((t) => (
              <Tooltip key={t.id} title={`${t.id} ${t.title}`} placement="left">
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
    </>
  );
  const agentBackendIdNode = (
    <>
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
          label={F.agentBackendId.label || "Specific connection"}
          value={
            pinChoices.some((c) => c.id === agentBackendId)
              ? agentBackendId
              : ""
          }
          onChange={(e) =>
            edit("agentBackendId", setAgentBackendId)(e.target.value)
          }
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
          <MenuItem value="">Any enabled connection serving the model</MenuItem>
          {pinChoices.map((backend) => (
            <MenuItem key={backend.id} value={backend.id}>
              {connectionLabel(backend)}
            </MenuItem>
          ))}
        </TextField>
      )}
    </>
  );
  const networkAccessNode = (
    <>
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
        label={F.networkAccess.label || "Network access"}
        value={networkAccess}
        onChange={(e) =>
          edit("networkAccess", setNetworkAccess)(e.target.value)
        }
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
        {networkOffered("") && (
          <MenuItem value="">
            {`Deployment default${config && config.networkAccess ? ` (${config.networkAccess === "open" ? "egress" : config.networkAccess})` : ""}`}
          </MenuItem>
        )}
        {networkOffered("open") && (
          <MenuItem value="open">Egress (outbound internet only)</MenuItem>
        )}
        {networkOffered("none") && (
          <MenuItem value="none">None (this deployment only)</MenuItem>
        )}
      </TextField>
    </>
  );
  const nothingNeededActionNode = (
    <>
      {/* What happens when this task's agent ends its run with
                nothing_needed -- looked, and found no change and no
                report anyone has to act on. Closing is the default, so
                deliberate no-ops stay out of the awaiting-ack inbox. */}
      <TextField
        select
        name="nothingNeededAction"
        label={
          F.nothingNeededAction.label || "When the agent finds nothing to do"
        }
        value={nothingNeededAction}
        onChange={(e) =>
          edit("nothingNeededAction", setNothingNeededAction)(e.target.value)
        }
        fullWidth
        margin="normal"
        size="small"
      >
        <MenuItem value="close">Close</MenuItem>
        <MenuItem value="ack">Leave for acknowledgement</MenuItem>
      </TextField>
    </>
  );
  const reviewTemplateIdNode = (
    <>
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
        label={F.reviewTemplateId.label || "Review"}
        value={reviewTemplateId}
        onChange={(e) =>
          edit("reviewTemplateId", setReviewTemplateId)(e.target.value)
        }
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
    </>
  );
  const promptExtensionNode = (
    <>
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
        label={F.promptExtension.label || "Prompt extension override"}
        helperText={
          F.promptExtension.helpText ||
          "Replaces this deployment's and the repo's standing instructions for this task alone. Leave empty to use them."
        }
        multiline
        minRows={3}
        autoComplete="off"
        fullWidth
        margin="normal"
        size="small"
        value={promptExtension}
        onChange={(e) =>
          edit("promptExtension", setPromptExtension)(e.target.value)
        }
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
    </>
  );
  const sandboxShapeNode = (
    <>
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
          {F.sandboxShape.label || "Sandbox shape override"}{" "}
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
            daemon&apos;s own machine, which have no CPU, memory or disk shape
            of their own. An override here is refused rather than ignored: the
            task would be filed, dispatched, and then fail at sandbox setup.
          </Alert>
        )}
        {(F.sandboxShape.presets || []).length > 0 && (
          <TextField
            select
            label="Preset"
            value={
              (F.sandboxShape.presets || []).find(
                (p) =>
                  String(p.cpus || "") === String(sandboxCpus || "") &&
                  String(p.memory_mb || "") === String(sandboxMemoryMb || "") &&
                  String(p.disk_gb || "") === String(sandboxDiskGb || ""),
              )?.id ?? ""
            }
            onChange={(e) => {
              const shape = sandboxShapeDefault({
                presets: F.sandboxShape.presets,
                defaultPreset: e.target.value,
              });
              edit("sandboxShape", setSandboxCpus)(shape.cpus);
              setSandboxMemoryMb(shape.memoryMb);
              setSandboxDiskGb(shape.diskGb);
            }}
            InputLabelProps={{ shrink: true }}
            SelectProps={{ displayEmpty: true }}
            inputProps={{ "data-testid": "sandbox-preset" }}
            fullWidth
            margin="normal"
            size="small"
          >
            <MenuItem value="">Deployment default</MenuItem>
            {F.sandboxShape.presets.map((p) => (
              <MenuItem key={p.id} value={p.id}>
                {p.label || p.id}
              </MenuItem>
            ))}
          </TextField>
        )}
        {F.sandboxShape.allowCustomNumbers !== false && (
          <>
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
              onChange={(e) =>
                edit("sandboxShape", setSandboxCpus)(e.target.value)
              }
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
              onChange={(e) =>
                edit("sandboxShape", setSandboxMemoryMb)(e.target.value)
              }
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
              onChange={(e) =>
                edit("sandboxShape", setSandboxDiskGb)(e.target.value)
              }
            />
          </>
        )}
      </fieldset>
    </>
  );
  const approvedNode = (
    <>
      <FormControlLabel
        control={
          <Checkbox
            name="approved"
            checked={approved}
            onChange={(e) => edit("approved", setApproved)(e.target.checked)}
          />
        }
        label={
          F.approved.label ||
          "Queue immediately (unchecked files it as a proposal, needing approval)"
        }
        sx={{ display: "flex", mt: 1 }}
      />
    </>
  );
  const atFrontNode = (
    <>
      {/* Which end of the backlog the task joins, and the choice
            the next one will start from: the server stores it as it
            files this task (ui.CreateTaskRequest.AtFront), so
            somebody filing a run of urgent work picks "Front" once
            rather than on every task -- or drags it there
            afterwards, which is the same OrderKey either way. */}
      <TextField
        select
        label={F.atFront.label || "Add to backlog"}
        value={atFront ? "front" : "end"}
        onChange={(e) =>
          edit("atFront", setAtFront)(e.target.value === "front")
        }
        helperText={
          F.atFront.helpText ||
          "Remembered: the next task you file starts at this end too."
        }
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
  );
  // The template this task is filed from (fromTemplate, above). Not one
  // of settings/new-task-form.yml's fields -- it is what picks their
  // templates: layer -- so it keeps its own place at the top of the
  // form, ahead of the title it may fill. Switching it re-applies the
  // layer's defaults to every field nobody has touched (formDefaultsKey).
  const templateNode = templatePickerShown ? (
    <TextField
      select
      name="templateId"
      label="Template"
      value={fromTemplate}
      onChange={(e) => setFromTemplate(e.target.value)}
      InputLabelProps={{ shrink: true }}
      SelectProps={{ displayEmpty: true }}
      helperText={
        pickedTemplate
          ? pickedTemplate.repo
            ? `fills what this form leaves blank; always files against ${pickedTemplate.repo}`
            : "fills what this form leaves blank: title, description, reads, writes, capabilities, model"
          : "file from a template to have it fill what this form leaves blank"
      }
      fullWidth
      margin="normal"
      size="small"
    >
      <MenuItem value="">
        <GlyphLabel kind={null}>No template</GlyphLabel>
      </MenuItem>
      {formTemplates.map((t) => (
        <MenuItem key={t.id} value={t.id}>
          <GlyphLabel kind="templates">{templateLabel(t)}</GlyphLabel>
        </MenuItem>
      ))}
    </TextField>
  ) : null;
  const priorityNode = (
    <>
      <TextField
        name="priority"
        label="Task priority"
        helperText="default 5; higher numbers run first"
        type="number"
        inputProps={{ min: 1, step: 1 }}
        autoComplete="off"
        fullWidth
        margin="normal"
        size="small"
        value={priority}
        onChange={(e) => setPriority(e.target.value)}
      />
    </>
  );

  // Where each field is drawn (settings/new-task-form.yml, newTaskForm.js):
  // the shown ones in order around "Advanced options", which holds the
  // more ones and is left out when there are none. Reviewers, Keep
  // running and Task priority are not configurable and keep their own
  // places. A hidden field is not drawn at all, and files its default.
  const fieldNodes = {
    title: titleNode,
    description: descriptionNode,
    customFields: customFieldsNode,
    repo: repoNode,
    newBranch: newBranchNode,
    base: baseNode,
    writes: writesNode,
    reads: readsNode,
    autoMerge: autoMergeNode,
    noPort: noPortNode,
    fixesBaseBranch: fixesBaseBranchNode,
    grants: grantsNode,
    model: modelNode,
    dependsOn: dependsOnNode,
    agentBackendId: agentBackendIdNode,
    networkAccess: networkAccessNode,
    nothingNeededAction: nothingNeededActionNode,
    reviewTemplateId: reviewTemplateIdNode,
    promptExtension: promptExtensionNode,
    sandboxShape: sandboxShapeNode,
    approved: approvedNode,
    atFront: atFrontNode,
  };
  // Whether a field draws anything at all here, for "Advanced options"
  // to know it would be empty.
  const fieldDraws = {
    newBranch: !noRepo,
    base: !changeTarget,
    writes: !changeTarget,
    autoMerge: !changeTarget,
    noPort: !changeTarget,
    fixesBaseBranch: !noRepo,
    model: models.length > 0,
    agentBackendId: connections.length > 0,
  };
  const slots = [
    ...Object.entries(fieldNodes)
      .filter(([id]) => id === "repo" || !inRepoGroup(id))
      .map(([id, node]) => ({
        key: id,
        order: F[id].order,
        visibility: F[id].visibility,
        draws: fieldDraws[id] ?? true,
        node,
      })),
    {
      key: "template",
      order: 5,
      visibility: "shown",
      draws: templatePickerShown,
      node: templateNode,
    },
    {
      key: "reviewers",
      order: 62,
      visibility: "shown",
      draws: true,
      node: reviewersNode,
    },
    {
      key: "changeTargetNote",
      order: 65,
      visibility: "shown",
      draws: !!changeTarget,
      node: changeTargetNoteNode,
    },
    {
      key: "keepRunning",
      order: 85,
      visibility: "shown",
      draws: true,
      node: keepRunningNode,
    },
    {
      key: "priority",
      order: 320,
      visibility: "shown",
      draws: true,
      node: priorityNode,
    },
  ].sort((a, b) => a.order - b.order);
  const shownSlots = slots.filter((s) => s.visibility === "shown");
  const moreSlots = slots.filter((s) => s.visibility === "more");
  const renderSlot = (s) => <Fragment key={s.key}>{s.node}</Fragment>;

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
              (!title.trim() && !pickedTemplate) ||
              (!noRepo && !repo.trim() && !pickedTemplate?.repo) ||
              (!noRepo && newBranch && !newBranchName.trim())
            }
          >
            Create task
          </Button>
        </Stack>
      }
    >
      <>
        {F.customFields.visibility !== "shown" && linkNoticeNode}
        {shownSlots.filter((s) => s.order < ADVANCED_ORDER).map(renderSlot)}
        {moreSlots.some((s) => s.draws) && (
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
            <AccordionDetails>{moreSlots.map(renderSlot)}</AccordionDetails>
          </Accordion>
        )}
        {shownSlots.filter((s) => s.order >= ADVANCED_ORDER).map(renderSlot)}
      </>
    </SidePanel>
  );
}

// asList is a configured default as a list of strings.
function asList(v) {
  if (Array.isArray(v)) return v.map((x) => String(x).trim()).filter(Boolean);
  if (typeof v === "string" && v.trim()) return [v.trim()];
  return [];
}

// asWrites is a configured writes default as {repo, base} rows.
function asWrites(v) {
  if (!Array.isArray(v)) return [];
  return v
    .map((x) =>
      typeof x === "string"
        ? { repo: x, base: "" }
        : { repo: String(x?.repo || ""), base: String(x?.base || "") },
    )
    .filter((w) => w.repo);
}

// asDependencies is a configured dependsOn default (task ids) as the
// {id, title} the picker's chips show.
function asDependencies(v, tasks) {
  return asList(v).map((id) => ({
    id,
    title: (tasks || []).find((t) => t.id === id)?.title || "",
  }));
}

// sandboxShapeDefault is the shape fields' starting values: the
// configured default preset's, or a {cpus, memory_mb, disk_gb} default,
// or blank (the deployment's own).
function sandboxShapeDefault(f) {
  const preset = (f?.presets || []).find((p) => p.id === f?.defaultPreset);
  const src =
    preset || (f?.default && typeof f.default === "object" ? f.default : null);
  if (!src) return { cpus: "", memoryMb: "", diskGb: "" };
  const n = (x) => (x ? String(x) : "");
  return {
    cpus: n(src.cpus),
    memoryMb: n(src.memory_mb),
    diskGb: n(src.disk_gb),
  };
}
