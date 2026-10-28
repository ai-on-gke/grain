// Formatting an absolute time, in the zone the *deployment* keeps rather
// than the one this browser happens to be in (grain/task-368).
//
// Every timestamp on screen used to be `new Date(iso).toLocaleString()`,
// which is the browser's own zone. That reads fine on a laptop sitting
// next to the daemon and badly everywhere else: a schedule fires on the
// deployment's clock (model.Config.TimeZone, which
// orchestrator.reconcileSchedule reads), so a "next run" printed on the
// reader's clock and a time-of-day typed into a schedule form were two
// different clocks with nothing on screen to say so. The zone comes from
// GET /api/config, is handed down through TimeZoneContext, and is what
// the four helpers here format against.
//
// An empty zone means "whatever this browser is in", which is what every
// one of these did before and what a test rendering a component outside
// a provider still gets.

// formatDateTime is the date and time of an ISO timestamp (or a Date, or
// anything the Date constructor takes), in zone.
//
// A value that isn't a time at all comes back as the empty string rather
// than "Invalid Date": these are printed inside sentences ("started ...")
// where a browser's own words for a parse failure would read as data.
export function formatDateTime(value, zone) {
  return format(value, zone, {});
}

// formatTime is the clock time alone -- the one thing a countdown's own
// "until" needs, where the date is either today or already said.
export function formatTime(value, zone) {
  return format(value, zone, {
    hour: "numeric",
    minute: "2-digit",
    second: "2-digit",
  });
}

function format(value, zone, options) {
  const at = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(at.getTime())) return "";
  const opts = { ...options };
  if (zone) opts.timeZone = zone;
  try {
    return at.toLocaleString(undefined, opts);
  } catch {
    // An unknown zone throws a RangeError rather than falling back. The
    // daemon validates the name before storing it, so this is a browser
    // whose ICU data is older than the zone somebody chose -- the time
    // is still worth printing, in the only zone this browser has.
    delete opts.timeZone;
    return at.toLocaleString(undefined, opts);
  }
}

// formatDate is the day alone, short -- "Sep 3" -- for a label that
// stands under a tick on a time axis rather than inside a sentence
// (gantt.js). The year is left off on purpose: a chart says which
// stretch of the calendar it is showing above itself, and repeating
// "2026" under every tick spends the width a day label has.
export function formatDate(value, zone) {
  return format(value, zone, { month: "short", day: "numeric" });
}

// formatHour is the same for a tick inside a single day -- "14:00", no
// seconds, since nothing on an axis is read to the second.
export function formatHour(value, zone) {
  return format(value, zone, { hour: "2-digit", minute: "2-digit" });
}

// zoneOffset is how far ahead of UTC `zone` is at one instant, in
// milliseconds -- read by asking Intl what the clock there says and
// comparing it with what UTC says.
//
// It is the piece behind every "which day is this?" question asked in
// the deployment's own zone rather than the browser's: which day a
// created-on filter means (taskFilters.js's dayStart), and where a
// timeline's day boundaries fall (gantt.js's startOfDay). It lives here,
// with the rest of the zone handling, because the two would otherwise
// carry a copy each of the same Intl reading and be free to disagree
// about midnight.
//
// A zone this browser has never heard of (an old ICU data set, a
// deployment naming something exotic) reads as UTC rather than throwing,
// which is this file's own fallback for the same case elsewhere: the
// question is still worth answering, in the only reckoning this browser
// can make.
export function zoneOffset(ms, zone) {
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: zone,
      hour12: false,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    }).formatToParts(new Date(ms));
    const at = {};
    for (const p of parts) at[p.type] = p.value;
    const asUTC = Date.UTC(
      Number(at.year),
      Number(at.month) - 1,
      Number(at.day),
      Number(at.hour) % 24,
      Number(at.minute),
      Number(at.second),
    );
    return Number.isNaN(asUTC) ? 0 : asUTC - ms;
  } catch {
    return 0;
  }
}

// zoneAbbreviation is what a clock in this zone is *called* right now --
// "PDT", "GMT+2" -- for labelling a field whose value is a bare time of
// day. Empty when the browser cannot say, so a caller can leave the
// label alone rather than print a gap.
export function zoneAbbreviation(zone, at = new Date()) {
  if (!zone) return "";
  try {
    const parts = new Intl.DateTimeFormat(undefined, {
      timeZone: zone,
      timeZoneName: "short",
    }).formatToParts(at);
    return parts.find((p) => p.type === "timeZoneName")?.value || "";
  } catch {
    return "";
  }
}

// browserTimeZone is the zone this browser is in, for offering "the one
// you are actually looking at this from" as a choice. Empty if the
// browser will not say.
export function browserTimeZone() {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  } catch {
    return "";
  }
}

// FALLBACK_TIME_ZONES is the selector's vocabulary on a browser with no
// Intl.supportedValuesOf (which is every one of them older than 2022):
// enough zones to cover where a single-operator deployment plausibly
// runs, rather than a list nobody can complete by hand. A browser that
// does have it gets the whole IANA database instead, so this is a floor
// and not the offer.
export const FALLBACK_TIME_ZONES = [
  "UTC",
  "America/Los_Angeles",
  "America/Denver",
  "America/Chicago",
  "America/New_York",
  "America/Sao_Paulo",
  "Europe/London",
  "Europe/Berlin",
  "Europe/Moscow",
  "Asia/Dubai",
  "Asia/Kolkata",
  "Asia/Singapore",
  "Asia/Tokyo",
  "Australia/Sydney",
  "Pacific/Auckland",
];

// timeZoneOptions is every zone the selector offers, sorted, with
// `current` guaranteed to be among them -- a deployment set to a zone
// this browser has never heard of must still show what it is set to
// rather than silently reading as some other zone the moment somebody
// opens Settings.
export function timeZoneOptions(current = "") {
  let zones = [];
  try {
    zones = Intl.supportedValuesOf("timeZone");
  } catch {
    zones = [];
  }
  if (!zones.length) zones = FALLBACK_TIME_ZONES;
  const all = new Set(zones);
  all.add("UTC");
  if (current) all.add(current);
  return [...all].sort();
}

export function timeZoneHelp(zone) {
  const now = formatDateTime(new Date(), zone);
  const reads = now ? ` It reads ${now} there right now.` : "";
  return (
    "Every time grain shows in this browser is read on this clock; schedules still fire on their own time zone." +
    reads
  );
}
