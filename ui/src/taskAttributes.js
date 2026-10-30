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

// taskAttributes.js is what a task view knows how to ask about: every
// attribute a filter or a sort can name, and the type that decides which
// comparisons apply to it.
//
// It was carved out of taskFilters.js's own FILTERS list (grain/task-42).
// That list held seven attributes, each of them offered as one Select
// with one value picked -- so "repo is acme/widgets" was sayable and
// "repo is either of these two", "created this week", "title mentions
// flake" were not, and every attribute a task carries beyond those seven
// (its state, the repos it only reads, its pull request, the review
// attached to it) was not askable at all. taskFilters.js is the
// comparisons now; this file is the vocabulary they are made of.
//
// # What an attribute is
//
//   id        what a filter, a sort and the URL all name it by. Stable:
//             a link somebody kept says "repo", so an id is not renamed
//             without leaving the old one parsing.
//   label     what it is called on screen.
//   type      which comparisons it takes (TYPES below), and which value
//             control the editor puts beside them.
//   values    every value one task has for it -- [] for a task that has
//             none, more than one for the multi-valued (tags) ones. The
//             only thing that reads a task, so an attribute over some
//             other shape of task is a matter of writing this function.
//   labelOf   how one value is written, when it is not the value itself
//             (a capability id is not its name).
//   order     the order to offer values in, for the attributes whose
//             values are a fixed vocabulary rather than whatever the
//             tasks happen to hold. The rest are offered alphabetically
//             by what the menu shows.
//   noneLabel what "has no value for this" is called, for the attributes
//             where having none is a real answer worth filtering on.
//   glyphOf   the item glyph a value's menu row carries, for the
//             attributes whose values *are* one of the kinds grain keeps
//             a list of (ItemGlyph.jsx, docs/brand.md, grain/task-38) --
//             every value of the repo menu is a repo, and two of the
//             origin menu's are the schedule and the suite whose figures
//             the rows being filtered already carry. Returning null (an
//             origin nobody files from a list, "No repo") leaves the
//             row's icon slot empty rather than unaligned.
//   people    whether every value is a person's name. Its conditions can
//             match on part of a name as well as pick one.
//   key       what to sort by, when it is not the first value -- a date
//             sorts as a moment and an id as a number, neither of which
//             is what comparing their text would do.
//
// # Custom attributes
//
// Everything a view does with an attribute goes through this list, and
// nothing in it is written down twice anywhere else: the filter editor
// builds its menus from the descriptors, the sort builds its comparators
// from them, and paths.js writes their ids into the query. So a
// deployment-defined attribute is a descriptor added to the list --
// which is what attributesFor is for. It takes the config the UI already
// has and returns the built-ins plus whatever custom attributes that
// config declares, and every caller asks it rather than importing
// ATTRIBUTES directly.
//
// What a deployment declares is a *custom field* (model.CustomField,
// grain/task-53): a name, a label, a type and, for an enum, its options,
// written down in its state repository and served on GET /api/config as
// `customFields`. A task's own values for them ride on the task itself,
// as `customFields`, name to value. That is exactly the two halves an
// attribute needs -- a vocabulary, and a way to read one task -- so a
// declared field *is* a custom attribute here (customAttribute below)
// rather than a second thing a deployment has to declare beside it: one
// entry in the state repository's `custom_field` table is the input on
// the new-task form, the row on the task's own pane (CustomFields.jsx)
// and the menu in this toolbar at once, and nowhere does a deployment
// say "priority is one of low, medium, high" twice.
//
// The ids are namespaced (CUSTOM_PREFIX) so a field called `repo` adds a
// menu of its own rather than quietly replacing the built-in of that
// name -- in the query string as well as in the menus.
import {
  ASKED_STATES,
  DISPLAY_STATE_ORDER,
  STATE_LABELS,
  STATE_ORDER,
  capabilityName,
  displayState,
  statesOf,
  frameworkLabel,
  taskMergedAt,
} from "./state.js";

// TYPES is what a value *is*, which is the whole of what decides the
// comparisons an attribute offers (taskFilters.js's OPERATORS) and the
// control the editor puts beside it. Kept to five deliberately: every
// attribute below is one of these, and a sixth would have to earn both a
// set of comparisons and an editor control of its own.
export const TYPE_ENUM = "enum"; // one value out of a vocabulary
export const TYPE_TAGS = "tags"; // any number of values at once
export const TYPE_TEXT = "text"; // free text nobody picks from a menu
export const TYPE_DATE = "date"; // a moment
export const TYPE_NUMBER = "number";

// NONE is the value standing for "this task has no value for the
// attribute at all" -- no target repo, no capabilities. It is a string a
// real value can't be: a repo is owner/name and a capability id is a
// bare word, so neither can be "__none__", and it is never displayed (an
// attribute's own noneLabel is).
//
// It lives here rather than with the comparisons that use it
// (taskFilters.js re-exports it) because a descriptor below has to be
// able to recognise it -- glyphOf is asked about it like any other
// value.
export const NONE = "__none__";

// NONE_LABEL_FALLBACK is what "has no value for this" is called for an
// attribute that never said (a custom one, most likely). Attributes that
// do say it are more specific -- "No repo", "Repo default" -- because
// what it means differs: a task with no base branch has a base, the
// repo's own.
export const NONE_LABEL_FALLBACK = "Not set";

const ORIGIN_LABELS = {
  hand: "Filed by hand",
  proposed: "Proposed by an agent",
  schedule: "Scheduled",
  suite: "Suite run",
  imported: "Imported issue",
  fix: "Merge fix",
  review: "Review",
};

// originOf is the one thing that put this task in the list -- what the
// row's own scheduled/suite/merge-fix chips say, collapsed to a single
// value so it can be asked about as one attribute. The order matters
// where a task carries more than one marker: a merge fix is always
// generated from another task, so answering "proposed" for it would say
// the less specific of two true things.
export function originOf(t) {
  // Review before fix: both are stacked, and only one of them is a
  // repair of a red build.
  if (t.review) return "review";
  if (t.stacked) return "fix";
  if (t.scheduled) return "schedule";
  if (t.suiteRun) return "suite";
  if (t.imported) return "imported";
  if (t.generatedFrom) return "proposed";
  return "hand";
}

// one/text are the two shapes almost every `values` below takes: a task
// has this attribute or it does not. Written out so the descriptors read
// as a table rather than as fifteen copies of the same conditional.
const one = (pick) => (t) => {
  const v = pick(t);
  return v === undefined || v === null || v === "" ? [] : [v];
};

// stateRank puts a task where STATE_ORDER -- the sidebar's own order,
// proposed through closed -- says it goes. A state this build has never
// heard of sorts after all of them instead of before, so a newer
// daemon's unknown state doesn't quietly take over the top of the list.
export function stateRank(t) {
  const i = STATE_ORDER.indexOf(t.state);
  return i === -1 ? STATE_ORDER.length : i;
}

// idKey sorts task ids as the numbers they are ("9" before "10"), while
// leaving anything that is not a number to sort as text after them --
// ids are strings to everything else here, and a deployment that ever
// issues a non-numeric one should not have its list silently reordered.
function idKey(t) {
  const n = Number(t.id);
  return Number.isFinite(n) ? n : t.id;
}

// ATTRIBUTES is the built-in vocabulary, in the order the "add a filter"
// menu offers it: the four an operator reaches for most (state, repo,
// title, capability) ahead of the rest.
//
// Everything here is a field the task list endpoint actually sends
// (pkg/ui.taskFrom) -- an attribute over a field only the detail view
// fetches would read as empty for every task and quietly narrow a list
// to nothing.
export const ATTRIBUTES = [
  {
    id: "state",
    label: "State",
    type: TYPE_ENUM,
    values: one((t) => t.state),
    // A menu offers both asked states as the one "Asked" they are shown
    // as (state.js's displayState), which matches either of them
    // (expand). A condition kept from before then -- a saved board or a
    // link naming awaiting_reply alone -- still matches exactly that
    // state, and is labelled with which of the two it is.
    optionOf: displayState,
    expand: statesOf,
    // order is model.State's own (the sort rank, and what pkg/cli's
    // vocabulary test reads); menuOrder is what the menu offers.
    order: STATE_ORDER,
    menuOrder: [...DISPLAY_STATE_ORDER, ...ASKED_STATES],
    labelOf: (v) =>
      v === "awaiting_reply"
        ? "Asked (reply)"
        : v === "awaiting_submit"
          ? "Asked (submit)"
          : STATE_LABELS[v] || v,
    key: stateRank,
  },
  {
    id: "repo",
    label: "Repo",
    type: TYPE_ENUM,
    // The repo this task's own runs push to. Deliberately just that: a
    // repo's own page asks the wider question (repoTasks, state.js --
    // filed here, or may land work here), but this attribute is what a
    // kept link says, so `repo:any:acme/widgets` goes on meaning "filed
    // against acme/widgets" and nothing else. The repos a task reads and
    // the other repos its work may land in are attributes of their own,
    // below ("reads" and "writes"); a task that only reads a repo is not
    // one of that repo's tasks under any of them.
    values: one((t) => t.repo),
    noneLabel: "No repo",
    glyphOf: (v) => (v === NONE ? null : "repos"),
  },
  {
    id: "title",
    label: "Title",
    type: TYPE_TEXT,
    values: one((t) => t.title),
  },
  {
    id: "capability",
    label: "Capability",
    type: TYPE_TAGS,
    values: (t) => t.capabilities || [],
    labelOf: (v, config) => capabilityName(config, v),
    noneLabel: "No capabilities",
  },
  {
    id: "base",
    label: "Base",
    type: TYPE_ENUM,
    values: one((t) => t.base),
    noneLabel: "Repo default",
    // Every value of this menu is a branch, which is a kind with a
    // figure of its own (grain/task-372) -- "Repo default" is not one,
    // and gets the empty slot GlyphLabel holds open for a row like it.
    glyphOf: (v) => (v === NONE ? null : "branches"),
  },
  {
    id: "reads",
    label: "Reads",
    type: TYPE_TAGS,
    values: (t) => t.reads || [],
    noneLabel: "No read-only repos",
    glyphOf: (v) => (v === NONE ? null : "repos"),
  },
  {
    id: "writes",
    label: "Writes",
    type: TYPE_TAGS,
    // The repo names alone, though a write target is a repo *and* a
    // branch (ui.Task.Writes, grain/task-54). The question this
    // attribute exists to answer is "which tasks may land work in
    // acme/widgets", and a menu offering "acme/widgets@deploy" would be
    // a vocabulary nobody could guess at -- while a filter on the branch
    // alone is a question nobody asks. The branch each target goes on
    // stays where it is chosen, on the task's own page
    // (WriteReposField).
    //
    // Its own attribute rather than folded into "repo": that one is
    // still the single repo this task's own runs push to, and widening
    // it would change what a link somebody kept already matches.
    // "Everything that may land work in acme/widgets" is the two
    // conditions together -- which is exactly what a repo's own page
    // scopes its task list by (repoTasks in state.js, grain/task-116),
    // spelled out there rather than by bending either attribute.
    values: (t) => (t.writes || []).map((w) => w?.repo).filter(Boolean),
    noneLabel: "No other writeable repos",
    glyphOf: (v) => (v === NONE ? null : "repos"),
  },
  {
    id: "author",
    label: "Author",
    type: TYPE_ENUM,
    values: one((t) => t.author),
    people: true,
  },
  {
    id: "creator",
    label: "Creator",
    type: TYPE_ENUM,
    values: one((t) => t.creator),
    people: true,
  },
  {
    // "Whose task is this?" without having to know which of the two
    // fields above answers it for a given task: the name the UI filed it
    // under (creator), and the person it is attributed to (author) when
    // that is a person rather than an agent's run or the deployment
    // itself. A task filed by alice and attributed to alice is one value,
    // not two.
    id: "user",
    label: "User",
    type: TYPE_ENUM,
    values: (t) => {
      const out = [];
      if (t.creator) out.push(t.creator);
      const human = !t.authorKind || t.authorKind === "human";
      if (t.author && human && !out.includes(t.author)) out.push(t.author);
      return out;
    },
    people: true,
  },
  {
    id: "origin",
    label: "Origin",
    type: TYPE_ENUM,
    values: (t) => [originOf(t)],
    order: ["hand", "proposed", "schedule", "suite", "fix", "review"],
    labelOf: (v) => ORIGIN_LABELS[v] || v,
    // The same two figures the rows carry: a task filed by a schedule
    // wears the hourglass on its own chip (TaskList/TaskBoard), so the
    // menu that finds those tasks wears it too. A review comes out of a
    // template, but what it names here is the task's origin rather than
    // the template it was written from -- the templates figure belongs
    // on the Review row of the task itself (DetailOverlay), and putting
    // it here as well would say the review *is* a template.
    glyphOf: (v) =>
      v === "schedule" ? "schedules" : v === "suite" ? "suites" : null,
  },
  {
    id: "autoMerge",
    label: "Auto-merge",
    type: TYPE_ENUM,
    values: (t) => [t.autoMerge ? "on" : "off"],
    order: ["on", "off"],
    labelOf: (v) => (v === "on" ? "On" : "Off"),
  },
  {
    id: "blocked",
    label: "Blocked",
    type: TYPE_ENUM,
    values: (t) => [t.blocked ? "yes" : "no"],
    order: ["yes", "no"],
    labelOf: (v) => (v === "yes" ? "Blocked" : "Not blocked"),
  },
  {
    id: "created",
    label: "Created",
    type: TYPE_DATE,
    values: one((t) => t.createdAt),
  },
  {
    id: "started",
    label: "Started",
    type: TYPE_DATE,
    values: one((t) => t.startedAt),
  },
  {
    id: "completed",
    label: "Completed",
    type: TYPE_DATE,
    values: one((t) => t.endedAt),
  },
  {
    id: "merged",
    label: "Merged",
    type: TYPE_DATE,
    values: one(taskMergedAt),
  },
  {
    id: "pullRequest",
    label: "Pull request",
    type: TYPE_TEXT,
    values: one((t) => t.pullRequest),
    noneLabel: "No pull request",
  },
  {
    id: "review",
    label: "Review",
    type: TYPE_ENUM,
    // The review attached to a task, by the name of the template it
    // names -- which is what the deployment calls it and what the task's
    // own page shows. A task whose template has since been deleted
    // reports none, the same as a task that never had one: the id it
    // still carries names nothing anybody could pick from this menu.
    values: one((t) => t.reviewTemplateName),
    noneLabel: "No review",
  },
  {
    id: "framework",
    label: "Agent",
    type: TYPE_ENUM,
    values: one((t) => t.agentFramework),
    labelOf: (v) => frameworkLabel(v),
    noneLabel: "Deployment default",
  },
  {
    id: "dependsOn",
    label: "Depends on",
    type: TYPE_TAGS,
    values: (t) => t.dependsOn || [],
    noneLabel: "Nothing",
  },
  {
    id: "id",
    label: "Task",
    type: TYPE_TEXT,
    values: one((t) => t.id),
    key: idKey,
  },
];

// CUSTOM_PREFIX namespaces every deployment-defined attribute, so
// declaring one called "repo" adds "custom.repo" rather than quietly
// replacing the built-in of that name -- in the URL as well as in the
// menus.
export const CUSTOM_PREFIX = "custom.";

// declaredFields is the fields this deployment declares, always an
// array -- a deployment that declares none, an older server that does
// not send the key, and a config that has not loaded yet all read as
// "no fields" rather than as something each caller guards against.
//
// It lives here, with the vocabulary, rather than with the inputs that
// render one (CustomFields.jsx imports it back): a declaration is what
// an attribute is made of, and this file is the only one that may not
// depend on a view.
export function declaredFields(config) {
  return (config && config.customFields) || [];
}

// fieldLabel is what to call a field on screen: its label, or its name
// when whoever declared it thought the name was good enough
// (model.CustomField.DisplayName, the same rule on this side).
export function fieldLabel(field) {
  return field.label || field.name;
}

// The three below read a declaration against a value something is
// already holding, rather than against a value somebody is about to
// pick. They live here, with the vocabulary, for the reason
// declaredFields does: the narrowing bar asks the same question a form
// does ("is a legacy value", taskFilters.js), and this file is the one
// that may not depend on a view. CustomFields.jsx, which draws what
// they find, imports them back and re-exports them.

// valueList is one field's values as an array, whatever shape they
// arrived in: the wire sends a string when a task has exactly one and an
// array when it has several (ui.CustomFieldValue), and a link's query
// string can carry either. Empty values are dropped, since a blank is
// how "nothing" is said everywhere else here.
export function valueList(value) {
  const all = Array.isArray(value) ? value : [value];
  return all
    .filter((v) => typeof v === "string" || typeof v === "number")
    .map((v) => String(v))
    .filter((v) => v !== "");
}

// isOffered says whether one value is one this declaration could still
// produce today -- the frontend's half of model.CustomField.
// NormalizeValue, asked so a form can tell a value somebody picked from
// a value left over from a declaration that has changed since.
//
// Deliberately lenient where the model is: text takes anything, and a
// type this build has never heard of is text (taskAttributes.js reads
// an unknown type the same way), so a build older than a declaration
// marks nothing as legacy rather than marking everything.
export function isOffered(field, value) {
  const v = String(value ?? "").trim();
  if (v === "") return true;
  switch (field.type) {
    case "enum":
      return (field.options || []).includes(v);
    case "boolean":
      // What the store holds, which is what NormalizeValue canonicalizes
      // "yes"/"on"/"1" into on the way in.
      return v === "true" || v === "false";
    case "number":
      // model.customFieldNumber: HTML's own valid floating-point
      // number, which is what <input type="number"> will hold and hand
      // back -- not everything Number() will parse. A value a number
      // input blanks has to be drawn as a legacy one or it is shown as
      // an empty box over a value that is really there.
      return (
        /^-?\d+(\.\d+)?([eE][-+]?\d+)?$/.test(v) && Number.isFinite(Number(v))
      );
    case "date":
      // model.CustomFieldDateLayout, which is what <input type="date">
      // both produces and reads -- year 0001 or later, since a date
      // input blanks year zero.
      return (
        /^\d{4}-\d{2}-\d{2}$/.test(v) &&
        Number(v.slice(0, 4)) >= 1 &&
        !Number.isNaN(Date.parse(v))
      );
    default:
      return true;
  }
}

// legacyValues is what one field's values hold that its declaration has
// moved on from: the values it no longer offers, and -- on a field that
// has stopped being `multiple` -- the whole set, since each value may
// be fine while there being two of them is not.
//
// Empty for the ordinary case, which is what every caller checks: a
// field is only ever drawn as a legacy one when there is something
// legacy to draw.
export function legacyValues(field, value) {
  const held = valueList(value);
  if (!field.multiple && held.length > 1) return held;
  return held.filter((v) => !isOffered(field, v));
}

// FIELD_TYPES maps what a deployment may declare (model.CustomFieldTypes)
// onto what a view can compare (TYPES above). Four of the five are the
// same idea under the same name; a boolean is an enum of two values,
// since "is any of Yes" is the question somebody asks about one and
// there is nothing a sixth type would earn it.
//
// A type this build has never heard of -- a newer daemon offering one --
// reads as text, which is the type that assumes least: it compares the
// value as the string it arrived as rather than dropping the field out
// of the menus.
const FIELD_TYPES = {
  text: TYPE_TEXT,
  number: TYPE_NUMBER,
  enum: TYPE_ENUM,
  date: TYPE_DATE,
  boolean: TYPE_ENUM,
};

// BOOLEAN_ORDER and BOOLEAN_LABELS are how the two values of a boolean
// field are offered and written. "true" is what the store holds
// (model.CustomField.NormalizeValue) and "Yes" is what a menu should
// say; "false" is offered as well because the API accepts it, even
// though the checkbox on the form records an unticked box as no value
// at all rather than as "false".
const BOOLEAN_ORDER = ["true", "false"];
const BOOLEAN_LABELS = { true: "Yes", false: "No" };

// customAttribute builds a descriptor for one declared field. Its values
// come from the task's own `customFields` map (ui.Task.CustomFields),
// under the declared name.
//
// The values are read defensively: a string is one value, an array is
// several (ui.CustomFieldValue sends whichever the task actually has,
// so a field that can hold several still arrives as a string when it
// holds one), and anything else is no value rather than "[object
// Object]" in a menu.
//
// A field declared `multiple` (model.CustomField.Multiple) is a TYPE_TAGS
// attribute whatever its own type is, since that is what "several values
// at once" is called here -- so it is compared with "is any of", "is
// none of" and "has all of" (taskFilters.js's OPERATORS), which are the
// questions worth asking of a set. Its own type still decides how the
// values are *offered*: a multi-valued enum keeps its declared options
// as the order of the menu, and its `order` below is what says so.
//
// Every custom attribute can be empty, and says so: unlike a title or a
// state, "nobody filled this in" is the ordinary answer for a field
// (model.CustomField.NormalizeValue -- there are deliberately no
// required ones), so "is empty" and "is set" are worth offering and the
// value menus carry a "Not set" row.
function customAttribute(field) {
  const valueType = FIELD_TYPES[field.type] || TYPE_TEXT;
  const type = field.multiple ? TYPE_TAGS : valueType;
  const boolean = field.type === "boolean";
  return {
    id: `${CUSTOM_PREFIX}${field.name}`,
    label: fieldLabel(field),
    type,
    custom: true,
    values: (t) => {
      const v = t.customFields?.[field.name];
      if (v === undefined || v === null || v === "") return [];
      if (Array.isArray(v)) return v.filter((x) => typeof x === "string");
      if (typeof v === "string" || typeof v === "number") return [String(v)];
      return [];
    },
    // An enum's options in the order they were declared, which is the
    // order they mean something in: "low, medium, high" is not
    // alphabetical and whoever wrote it down meant that order
    // (model.CustomField.Options).
    order: boolean
      ? BOOLEAN_ORDER
      : valueType === TYPE_ENUM && Array.isArray(field.options)
        ? field.options
        : undefined,
    labelOf: boolean ? (v) => BOOLEAN_LABELS[v] || v : undefined,
    noneLabel: NONE_LABEL_FALLBACK,
    // Whether this task is carrying something the declaration has
    // moved on from -- what the "is a legacy value" comparison asks
    // (taskFilters.js), and what the task's own pane already draws a
    // notice over (CustomFields.jsx). No built-in has it: their
    // vocabularies are this build's own, and only a deployment's own
    // declaration can change under a value already stored.
    legacy: (t) => legacyValues(field, t.customFields?.[field.name]).length > 0,
  };
}

// attributesFor is the vocabulary as of this config: the built-ins,
// then one attribute per field this deployment declares, in the order
// it declares them (model.SortCustomFields -- position, then name).
//
// Called with a config that declares none it is ATTRIBUTES itself,
// uncopied -- so a view can compare the result against a previous
// render's without walking it.
export function attributesFor(config) {
  const declared = declaredFields(config);
  if (declared.length === 0) return ATTRIBUTES;
  const seen = new Set();
  const customAttrs = [];
  for (const f of declared) {
    if (!f || !f.name || seen.has(f.name)) continue;
    seen.add(f.name);
    customAttrs.push(customAttribute(f));
  }
  return [...ATTRIBUTES, ...customAttrs];
}

// attributeById is the descriptor one filter or sort key names, or
// undefined for a name this build has never heard of -- a custom
// attribute the deployment has since dropped, or a link from a newer
// build. Callers treat undefined as "ignore this clause" rather than as
// an error: a stale link should open the list it names, not an empty one
// (paths.js).
export function attributeById(attributes, id) {
  return attributes.find((a) => a.id === id);
}

// valueLabel is how one value of one attribute is written on screen.
export function valueLabel(attr, value, config) {
  return attr.labelOf ? attr.labelOf(value, config) : value;
}
