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

import { useEffect, useRef, useState } from "react";
import {
  awaitsGitHubApproval,
  baseRepairWaitReason,
  completionPhase,
  inboxGateKey,
  inboxGates,
  inboxTasks,
  mergeQueueStalled,
  waitingBaseRepair,
} from "./state.js";

// Desktop notifications for the inbox (grain/task-2324): when something
// new starts waiting on the reader, the browser's own Notification API
// says so, so a person who has left the tab in the background finds out
// without having to come back and look at the rail's count.
//
// # What counts as new
//
// An inbox entry is one task for one reason, or one base-branch gate
// (state.js's inboxTasks and inboxGates). The tasks are the list the
// page and the rail's chip read. The gates are no longer on that page
// (they moved to the repository's own page and its Releases), so a
// gate's notification opens its repository rather than an inbox that
// would not show it. Its key carries the reason as well as the id, so
// a task that leaves the inbox and comes back -- answered, then asking
// again -- is new the second time, and so is one that moves from one wait
// to another (a proposal that, once approved, fails).
//
// Whatever is already in the inbox when the page loads is the baseline,
// not news: opening grain should not fire a notification for every task
// that was waiting yesterday. Only an entry the page has watched arrive
// is announced.
//
// # When it stays quiet
//
// When the page is in front of the reader -- visible and focused -- the
// rail's count already says it, and a system notification on top would
// be the same news twice. When the reader has not granted permission,
// or has turned these off here, nothing is shown at all.

// PREF_KEY is where the reader's own off switch is kept, per browser.
// Permission is the browser's to keep; this is only the "not now" a
// reader who has granted it can still say without digging into site
// settings.
export const PREF_KEY = "grain.inboxNotifications";

// BURST is how many entries arriving together are worth one notification
// each. Past it, one summary says how many: a dozen proposals filed by
// one run should not be a dozen toasts.
export const BURST = 3;

// taskReason is why one inbox task is waiting, as an id for its key and
// a phrase for the notification -- InboxPage's group for it, said as a
// sentence about the task.
export function taskReason(t) {
  if (waitingBaseRepair(t))
    return {
      id: "red_base_repair",
      text: `is repairing red base branch ${t.redBaseBranch} (${baseRepairWaitReason(t)})`,
    };
  if (t.state === "awaiting_reply")
    return { id: "reply", text: "asked a question" };
  if (t.state === "awaiting_submit" && t.reviewQuestions)
    return { id: "review_questions", text: "has review questions for you" };
  if (t.state === "awaiting_submit")
    return { id: "submit", text: "is ready to submit" };
  if (t.state === "failed") return { id: "failed", text: "failed" };
  if (t.state === "proposed")
    return { id: "proposed", text: "was proposed and needs approval" };
  if (awaitsGitHubApproval(t))
    return { id: "github", text: "needs approval on GitHub" };
  if (mergeQueueStalled(t))
    return { id: "merge_stalled", text: "has a stalled merge" };
  if (t.state === "completed" && completionPhase(t) !== null)
    return { id: "merge_blocked", text: "has a merge that needs unblocking" };
  if (t.stall) return { id: "stalled", text: "has stalled" };
  return { id: t.state || "waiting", text: "is waiting on you" };
}

// inboxEntries is everything the inbox holds, as the entries this file
// compares from one poll to the next.
export function inboxEntries(tasks, gates) {
  const entries = inboxTasks(tasks).map((t) => {
    const reason = taskReason(t);
    // A question is announced by its own one-line summary
    // (Task.QuestionSummary) -- what is being asked, not merely that
    // something was -- with which task asked it underneath.
    if (reason.id === "reply" && t.questionSummary) {
      return {
        key: `task:${t.id}:${reason.id}`,
        taskId: t.id,
        title: t.questionSummary,
        body: `${t.id} ${reason.text}${t.title ? `: ${t.title}` : ""}`,
      };
    }
    return {
      key: `task:${t.id}:${reason.id}`,
      taskId: t.id,
      title: `${t.id} ${reason.text}`,
      body: t.title || "",
    };
  });
  for (const g of inboxGates(gates)) {
    const where = g.branch ? `${g.repo} ${g.branch}` : g.repo;
    entries.push({
      key: `gate:${inboxGateKey(g)}`,
      repo: g.repo,
      title: `${where} is not being tested`,
      body: "The merge queue is merging onto a branch nothing is judging.",
    });
  }
  return entries;
}

// freshEntries is the entries in `entries` whose keys `seen` does not
// hold.
export function freshEntries(seen, entries) {
  return entries.filter((e) => !seen.has(e.key));
}

function notificationApi() {
  return typeof window !== "undefined" && "Notification" in window
    ? window.Notification
    : null;
}

// notificationsEnabled is whether this browser will show one now: the
// browser has said yes and the reader has not said "off" here.
export function notificationsEnabled() {
  const N = notificationApi();
  if (!N || N.permission !== "granted") return false;
  try {
    return localStorage.getItem(PREF_KEY) !== "off";
  } catch {
    return true;
  }
}

function pageInFront() {
  if (typeof document === "undefined") return false;
  const focused =
    typeof document.hasFocus === "function" ? document.hasFocus() : true;
  return document.visibilityState === "visible" && focused;
}

// announce shows the notifications for a batch of fresh entries. The tag
// is the entry's key, so two grain tabs open on the same deployment
// replace each other's notification rather than stacking two.
export function announce(
  entries,
  { onOpenTask, onOpenInbox, onOpenRepo } = {},
) {
  const N = notificationApi();
  if (!N || entries.length === 0) return [];
  const show = (title, body, tag, onClick) => {
    try {
      const n = new N(title, { body, tag });
      n.onclick = () => {
        try {
          window.focus();
        } catch {
          // A window that cannot be focused still gets the navigation.
        }
        onClick?.();
        n.close();
      };
      return n;
    } catch (err) {
      // Some browsers (Chrome on Android) only show notifications through
      // a service worker and throw on the constructor; the rail's count
      // still says it.
      console.warn("grain: notification not shown", err);
      return null;
    }
  };
  if (entries.length > BURST) {
    return [
      show(
        `${entries.length} new items in your inbox`,
        entries
          .slice(0, BURST)
          .map((e) => e.title)
          .join("\n"),
        "grain-inbox-summary",
        onOpenInbox,
      ),
    ].filter(Boolean);
  }
  return entries
    .map((e) =>
      show(e.title, e.body, e.key, () => {
        if (e.taskId) onOpenTask?.(e.taskId);
        else if (e.repo && onOpenRepo) onOpenRepo(e.repo);
        else onOpenInbox?.();
      }),
    )
    .filter(Boolean);
}

// useInboxNotifications watches the inbox and announces what arrives in
// it. `ready` is whether the lists have loaded at all: until they have,
// an empty list is not "nothing waiting" but "nothing fetched", and the
// first real answer is the baseline. Gates load on their own clock, so
// they get their own readiness and their own baseline.
export function useInboxNotifications({
  tasks,
  gates,
  tasksReady,
  gatesReady,
  onOpenTask,
  onOpenInbox,
  onOpenRepo,
}) {
  const seen = useRef(null);
  const gatesSeeded = useRef(false);
  const handlers = useRef({ onOpenTask, onOpenInbox, onOpenRepo });
  handlers.current = { onOpenTask, onOpenInbox, onOpenRepo };

  useEffect(() => {
    if (!tasksReady) return;
    const entries = inboxEntries(tasks, gatesReady ? gates : []);
    const keys = new Set(entries.map((e) => e.key));
    if (seen.current === null || (gatesReady && !gatesSeeded.current)) {
      // A baseline: the first answer for tasks, or the first for gates.
      // Anything new among the tasks in the same tick as the gates'
      // first answer is still announced.
      const fresh =
        seen.current === null
          ? []
          : freshEntries(seen.current, entries).filter((e) => e.taskId);
      seen.current = keys;
      gatesSeeded.current = gatesReady;
      if (fresh.length && notificationsEnabled() && !pageInFront())
        announce(fresh, handlers.current);
      return;
    }
    const fresh = freshEntries(seen.current, entries);
    seen.current = keys;
    if (fresh.length && notificationsEnabled() && !pageInFront())
      announce(fresh, handlers.current);
  }, [tasks, gates, tasksReady, gatesReady]);
}

// useNotificationSetting is the inbox header's switch: what the browser
// allows, whether this reader has them on, and the one action that
// changes it. Turning them on asks the browser for permission the first
// time -- which browsers only allow from a click, which is why this is a
// button and not something the page does on load.
export function useNotificationSetting() {
  const read = () => {
    const N = notificationApi();
    if (!N) return "unsupported";
    if (N.permission === "denied") return "denied";
    if (N.permission !== "granted") return "off";
    return notificationsEnabled() ? "on" : "off";
  };
  const [status, setStatus] = useState(read);
  const toggle = async () => {
    const N = notificationApi();
    if (!N) return;
    if (status === "on") {
      try {
        localStorage.setItem(PREF_KEY, "off");
      } catch {
        // Nothing to persist to; the switch still reads back below.
      }
      setStatus(read());
      return;
    }
    try {
      localStorage.removeItem(PREF_KEY);
    } catch {
      // As above.
    }
    if (N.permission !== "granted") {
      try {
        await N.requestPermission();
      } catch {
        // Older Safari takes a callback and returns nothing; the read
        // below picks up whatever it decided.
      }
    }
    setStatus(read());
  };
  return { status, toggle };
}
