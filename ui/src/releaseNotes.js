// The shaping behind ReleaseNotesPanel.jsx, kept out of the component
// for memory.js's reason: these are answers about what a week's notes
// mean, they are worth testing without a DOM, and a component that both
// decided them and drew them would be one nobody could ask a question
// of.

// weekLabel is an ISO week as a reader sees it in the picker: the week
// itself, which is the key and the file name, and the year said plainly
// beside it rather than left as a prefix to decode.
//
// Deliberately not a date range. Which Monday "2026-W38" starts on is a
// calendar question the server has already answered for the week being
// shown (ClusterStats.since/until), and a second answer computed in the
// browser -- against the browser's zone rather than the deployment's --
// would be the one that eventually disagrees.
export function weekLabel(period) {
  if (typeof period !== "string") return "";
  const [year, week] = period.split("-W");
  if (!year || !week) return period;
  return `Week ${Number(week)}, ${year}`;
}

// releaseNoteAuthor is who published a week, in the words this pane says
// it in.
//
// Three answers, not two, for memoryAuthor's reason: a person correcting
// a week and the weekly job take the same route, so the stored kind is
// the only thing that tells them apart -- and a row written before that
// column meant anything says nothing rather than being dressed up as
// either. What a reader is deciding here is whether the account they are
// reading is the machine's own or somebody's revision of it, which is
// exactly the distinction that would be lost by guessing.
export function releaseNoteAuthor(note) {
  const kind = note?.updatedByKind;
  if (kind === "human") return "Written by hand";
  if (kind === "agent" || kind === "automation") {
    return "Written by grain's release-notes job";
  }
  return "Published";
}
