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

import { UNCHANGED } from "./poll.js";
import { effectiveTimeZone } from "./PreferencesContext.jsx";

// TIMEZONE_HEADER carries this browser's effective time zone on every
// write (pkg/ui/timezone.go's TimeZoneHeader): a task filed, a session
// opened or a message sent from here records it, which is the zone the
// run that answers is told the time in. Reads go without it -- they
// change nothing the server would record it on.
const TIMEZONE_HEADER = "X-Grain-Time-Zone";

// The ETag each polled path was last answered with, so the next poll of
// it can ask "still this?" instead of asking for the whole thing again.
//
// Module-level rather than held by a caller because it belongs to the
// path and not to whoever is asking about it: the entry is written from
// the response that produced it and read by the next request for the
// same path, which is the only place either half is known.
const etags = new Map();

// forgetETags drops all of that, for a test that wants a fresh page's
// behaviour without a fresh module. Nothing in the app calls it: an ETag
// that no longer matches costs a full response, which is the thing that
// was going to happen anyway.
export function forgetETags() {
  etags.clear();
}

// forgetETag drops one path's entry, for a caller that read a full
// answer and then threw it away -- a poll a later read superseded. The
// entry describes that answer, so keeping it would have the next poll
// told 304 about a body the caller never took, and it would go on
// holding the older one for as long as nothing changes on the server.
export function forgetETag(path) {
  etags.delete(path);
}

// forgetVerifierTrouble ends any run of verifier_unavailable refusals
// verifierTroubleSince is counting, for the same kind of test.
export function forgetVerifierTrouble() {
  setVerifierTrouble(null);
}

// isUnreachable is whether an error api() threw means the daemon could
// not be reached at all -- a network-level failure fetch itself
// rejected with (a dead connection, a timeout, a DNS failure: the Fetch
// spec collapses all of these into one TypeError, and api() never
// throws that type for anything else, including a successful reboot
// cutting its own connection -- AdminOverlay.jsx's power actions key on
// exactly this to tell the two apart) or a 502/503/504 from whatever
// sits in front of the daemon saying it isn't there. Every other
// rejection -- a 500, a 403, a validation error, `notGrain` below -- is
// the daemon (or something that can at least speak for it) answering,
// which is a different problem with a different fix.
//
// A request asked with a `timeout` (api() below) that ran past it
// counts as well: a daemon that accepts the connection and then never
// answers -- deadlocked, stopped, or behind a proxy holding the socket
// open -- is as out of reach as one that refuses it, and without a
// bound on the wait the request never rejects at all, so nothing could
// ever say so (grain/task-3619).
//
// A verifier_unavailable 503 (isVerifierUnavailable below) is not one:
// it is the daemon itself answering, saying the sign-in verifier it
// consults could not check this request's identity right now.
export function isUnreachable(err) {
  if (!err || err.notGrain) return false;
  if (isVerifierUnavailable(err)) return false;
  if (err.timedOut) return true;
  if (typeof err.status === "number") {
    return err.status === 502 || err.status === 503 || err.status === 504;
  }
  return err instanceof TypeError;
}

// isVerifierUnavailable is whether an error api() threw is the daemon
// refusing a request because the sign-in verifier in front of its
// allowlist could not check the caller's identity right now -- its
// identity backend briefly out of reach (pkg/ui's writeVerifierUnavailable).
// It is transient and the daemon is up: a poll retries it quietly, and
// only verifierTroubleSince running past VERIFIER_SUSTAINED_MS is worth
// saying anything about.
export function isVerifierUnavailable(err) {
  return err?.status === 503 && err?.code === "verifier_unavailable";
}

// VERIFIER_SUSTAINED_MS is how long verifier_unavailable refusals have to
// go on, with no request getting through, before the page says so.
export const VERIFIER_SUSTAINED_MS = 30_000;

// verifierTrouble is when the current run of verifier_unavailable
// refusals began, across every request this tab makes: null once any
// request succeeds. Kept here rather than in App's state because every
// poll on every page goes through api(), and any one of them answering
// is the run's end.
let verifierTrouble = null;
const verifierListeners = new Set();

function setVerifierTrouble(next) {
  if (next === verifierTrouble) return;
  verifierTrouble = next;
  for (const fn of verifierListeners) fn(next);
}

// verifierTroubleSince is when the current run of verifier_unavailable
// refusals began, or null when there is none.
export function verifierTroubleSince() {
  return verifierTrouble;
}

// onVerifierTrouble calls fn with verifierTroubleSince's new value each
// time it changes, and returns the function that stops it.
export function onVerifierTrouble(fn) {
  verifierListeners.add(fn);
  return () => verifierListeners.delete(fn);
}

// verifierTroubleSustained is whether the current run of
// verifier_unavailable refusals has lasted VERIFIER_SUSTAINED_MS.
export function verifierTroubleSustained(now = Date.now()) {
  return (
    verifierTrouble !== null &&
    now - verifierTrouble.getTime() >= VERIFIER_SUSTAINED_MS
  );
}

// VERIFIER_ACTION_REASON is what a toast says for an action of the
// operator's own the verifier refused: what happened and that a retry
// is all it needs, rather than that the deployment is unreachable.
export const VERIFIER_ACTION_REASON =
  "Sign-in verification could not check your identity just now, so this was not done -- try again in a few seconds.";

// verifierSustainedReason is the standing warning's sentence for a run of
// verifier_unavailable refusals that began at since.
export function verifierSustainedReason(since, now = Date.now()) {
  const secs = Math.max(
    Math.round(VERIFIER_SUSTAINED_MS / 1000),
    Math.floor((now - since.getTime()) / 1000),
  );
  const age = secs < 120 ? `${secs}s` : `${Math.floor(secs / 60)} min`;
  return `Sign-in verification has been temporarily unable to check your identity for ${age} — retrying automatically.`;
}

const NETWORK_UNREACHABLE_REASON =
  "This deployment cannot be reached: the connection to it failed.";

function gatewayUnreachableReason(status) {
  if (status === 502) {
    return "This deployment cannot be reached: a gateway in front of it reports it is down (502 Bad Gateway).";
  }
  if (status === 503) {
    return "This deployment cannot be reached: a gateway in front of it reports it is unavailable (503 Service Unavailable).";
  }
  return "This deployment cannot be reached: a gateway in front of it timed out waiting for an answer (504 Gateway Timeout).";
}

// RESTARTING_REASON is the sentence for a disconnection that began after
// the daemon said it was restarting (GET /api/config's `restarting`):
// an upgrade, or a restart an operator asked for. Nothing is wrong, and
// the banner should not suggest otherwise.
export const RESTARTING_REASON =
  "grain is restarting -- an upgrade or a restart is replacing the server.";

// unreachableReason is the sentence to show for an error isUnreachable
// says is one: the friendly one this module composed, since the
// browser's own wording for a network failure ("Failed to fetch",
// "NetworkError when attempting to fetch resource", "Load failed" --
// one per browser, none of them written for an operator) is not fit to
// put on screen, and a bare "502 Bad Gateway" names the symptom without
// saying what it means.
export function unreachableReason(err) {
  if (err?.timedOut) {
    return `This deployment cannot be reached: it accepted the connection but sent no answer within ${Math.round(err.timeoutMs / 1000)} seconds.`;
  }
  if (typeof err?.status === "number")
    return gatewayUnreachableReason(err.status);
  return NETWORK_UNREACHABLE_REASON;
}

// Thin fetch wrapper: every call is JSON in, JSON (or nothing) out, and
// a non-2xx response throws an Error carrying the server's own message
// (with the status riding along on it) so callers can hand it straight
// to showError. isUnreachable above tells the daemon-could-not-be-
// reached cases among those apart from the rest.
//
// `poll: true` in opts asks for the conditional form instead, for the
// caller re-reading something it already has: the last ETag this path
// answered with is sent as If-None-Match, and a server that says 304
// gets UNCHANGED (poll.js) back rather than a body. It is opt-in per
// call site rather than automatic on every GET because the ETag is
// shared per path -- a second caller of the same path, wanting the body
// and holding no copy of its own, must not be handed somebody else's
// "you already have this".
//
// `timeout: ms` bounds the whole exchange, body included: past it the
// request is aborted and api() throws an error isUnreachable counts as
// one, carrying `timedOut`, `timeoutMs` and `sentAt` (when it was
// asked, which is when the silence began). Opt-in, and only for a read
// whose answer is expected promptly -- never a long poll the daemon
// holds open on purpose, nor a mutation, whose abort would leave its
// outcome unknown.
export default async function api(path, opts) {
  const { timeout, ...rest } = opts || {};
  if (!timeout) return request(path, rest);
  const controller = new AbortController();
  const sentAt = new Date();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeout);
  try {
    return await request(path, { ...rest, signal: controller.signal });
  } catch (err) {
    if (!timedOut) throw err;
    const late = new Error(`${path} sent no answer within ${timeout}ms`);
    late.timedOut = true;
    late.timeoutMs = timeout;
    late.sentAt = sentAt;
    throw late;
  } finally {
    clearTimeout(timer);
  }
}

async function request(path, opts) {
  // headers is taken out of init so the caller's own are merged into
  // ours below rather than replacing them wholesale in the Object.assign
  // that builds the request -- which would drop the Content-Type and the
  // If-None-Match a poll depends on.
  const { poll = false, headers: extra, ...init } = opts || {};
  const headers = { "Content-Type": "application/json" };
  const method = (init.method || "GET").toUpperCase();
  if (method !== "GET" && method !== "HEAD") {
    const zone = effectiveTimeZone();
    if (zone) headers[TIMEZONE_HEADER] = zone;
  }
  if (extra) {
    Object.assign(headers, extra);
  }
  const known = poll ? etags.get(path) : null;
  if (known) headers["If-None-Match"] = known;
  // No try/catch around this: a network-level failure is the TypeError
  // the Fetch spec itself throws, and isUnreachable above is written to
  // recognise exactly that -- there is nothing to add to it here, and
  // wrapping it would be the one thing that must not happen to it
  // (AdminOverlay.jsx's own `instanceof TypeError` check).
  const res = await fetch(
    path,
    Object.assign(
      { headers },
      // Our own revalidation, not the browser's. Left to its own HTTP
      // cache, fetch would revalidate with an If-None-Match of its own
      // and hand back a 200 with the cached body -- saving the wire and
      // none of the parse, which is most of what this is for. no-store
      // keeps the browser out of it so the 304 reaches this function.
      poll ? { cache: "no-store" } : null,
      init,
    ),
  );
  if (res.ok || res.status === 304) setVerifierTrouble(null);
  if (poll && res.status === 304) return UNCHANGED;
  const contentType = res.headers.get("Content-Type") || "";
  const isJSON = contentType.includes("application/json");
  const body = isJSON ? await res.json() : null;
  if (!res.ok) {
    // status rides along for the caller that answers a refusal
    // differently from a failure -- Team Settings' "not a member" state.
    const err = new Error(
      (body && body.error) || `${res.status} ${res.statusText}`,
    );
    err.status = res.status;
    // The server's own classification, where it gives one -- "stale"
    // for a save from a copy somebody has since saved over
    // (components/StaleEdit.jsx).
    if (body && body.code) err.code = body.code;
    // What a verifier_unavailable refusal says about itself: the backend
    // that was out of reach ("tonic_unreachable") and when to ask again.
    if (body && body.reason) err.reason = body.reason;
    if (body && typeof body.retryAfter === "number") {
      err.retryAfter = body.retryAfter;
    }
    // Whether this was a read or an action, for showError: a refused
    // read is retried by the poll that asked, a refused action is not.
    err.method = method;
    if (isVerifierUnavailable(err) && verifierTrouble === null) {
      setVerifierTrouble(new Date());
    }
    // A capability grant's refusal keyed by the argument it is about
    // (pkg/ui's ValidationError fields), which a grant form marks.
    if (body && body.fields) err.fields = body.fields;
    // A config change the pre-review suite refused (POST
    // /api/state-repo/pull-requests' 422): its report, which
    // components/ConfigRefusal.jsx lists.
    if (body && body.validation) err.validation = body.validation;
    // A configuration write refused because the configuration lives in
    // an external config repository, with the file and the patch to
    // commit there instead (components/ConfigRefusal.jsx).
    if (body && body.code === "config_managed_externally") {
      err.configRefusal = body;
    }
    // Whether the message is a sentence somebody wrote for this failure
    // rather than the bare status line, which showError uses to keep it
    // in place of the generic "cannot be reached" wording.
    err.explained = Boolean(body && body.error);
    throw err;
  }
  // A successful HTML page is never grain's API talking: grain answers
  // JSON or nothing. It is something in front of grain answering for it
  // -- a captive portal, a corporate proxy, an auth front end's sign-in
  // page -- and handing callers null for it left the app on its loading
  // splash forever, waiting for a config that had in fact "arrived"
  // (grain/task-1435). Thrown, it reaches the caller's error handling
  // like any other failure, and App can say what it most likely is.
  if (contentType.includes("text/html")) {
    const err = new Error(
      `${path} answered ${res.status} with an HTML page rather than grain's JSON -- ` +
        "likely a proxy or sign-in page in front of grain",
    );
    err.notGrain = true;
    throw err;
  }
  if (poll) {
    const etag = res.headers.get("ETag");
    // Dropped when absent rather than kept: this answer is the current
    // one and it came with no validator, so the stored one describes
    // nothing the server would still recognise.
    if (etag) etags.set(path, etag);
    else etags.delete(path);
  }
  return body;
}
