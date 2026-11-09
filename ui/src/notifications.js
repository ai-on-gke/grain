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

import { useCallback, useEffect, useState } from "react";
import api from "./api.js";
import { repoApiRoot } from "./repoApi.js";

// notifications.js is operator notifications in the UI
// (design/design/durable-tasks.design.md, DT-5): the open ones, read from
// GET /api/notifications?open=1, joining the alerts bell's list
// (notificationAlerts) and the Inbox's Notifications section. The poll
// asks fields=list, which cuts each body to the first line
// notificationSentence draws (a pending release's is kept whole, for
// pendingReleaseOf's soak lines), rather than carrying every open
// notification's whole body every POLL_MS.
//
// Acknowledging hides a notification from the bell and nothing else: it
// stays on the Inbox until whatever raised it clears it.

// POLL_MS is how often the open list is re-read. The bell and the Inbox
// share it, so a raise shows up in both within one poll.
const POLL_MS = 30000;

// POLL_TIMEOUT_MS bounds each read, App.jsx's CONFIG_POLL_TIMEOUT_MS
// for the same reason: a read nothing gives up on holds one of the
// browser's few connections to the daemon for as long as the daemon, or
// a proxy in front of it, keeps it open.
const POLL_TIMEOUT_MS = 10_000;

// useOpenNotifications is the open notifications, newest first, and a
// function that re-reads them now. A failed read keeps the last list. A
// hidden tab skips the poll, like the rest of the app's, and catches up
// on its next tick once it is shown again.
export function useOpenNotifications() {
  const [list, setList] = useState([]);
  const reload = useCallback(async () => {
    try {
      // Bounded, as App.jsx's background polls are (POLL_TIMEOUT_MS).
      const next = await api("/api/notifications?open=1&fields=list", {
        timeout: POLL_TIMEOUT_MS,
      });
      setList(Array.isArray(next) ? next : []);
    } catch {
      // The daemon being unreachable is DisconnectedBanner's to say.
    }
  }, []);
  useEffect(() => {
    reload();
    const timer = setInterval(() => {
      if (document.visibilityState !== "hidden") reload();
    }, POLL_MS);
    return () => clearInterval(timer);
  }, [reload]);
  return [list, reload];
}

// notificationAlerts is the bell's rows for notifications: the open,
// unacknowledged ones, with the same severities alerts.js uses. A
// pending release (pendingReleaseOf) offers Approve and Reject first:
// they are what the notification is asking a person for.
export function notificationAlerts(notifications) {
  return (notifications || [])
    .filter((n) => !n.clearedAt && !n.ackedAt)
    .map((n) => {
      const release = pendingReleaseOf(n);
      return {
        id: `notification-${n.id}`,
        severity: n.severity,
        title: n.title,
        sentence: notificationSentence(n),
        scope: { kind: "deployment" },
        since: n.raisedAt,
        actions: [
          // The task the notification is about -- a red base branch's
          // repair -- as the Inbox's row offers it ("Open task"); its
          // body names it only as a markdown link the bell draws as text.
          ...(n.taskId
            ? [
                {
                  kind: "open-task",
                  label: `Open #${n.taskId}`,
                  taskId: n.taskId,
                },
              ]
            : []),
          ...(release
            ? [
                {
                  kind: "approve-release",
                  label: release.shortSoak ? "Approve short soak" : "Approve",
                  notification: n,
                  release,
                },
                {
                  kind: "reject-release",
                  label: "Reject",
                  notification: n,
                  release,
                },
              ]
            : []),
          { kind: "ack-notification", label: "Acknowledge", notification: n },
          {
            kind: "investigate-notification",
            label: n.investigationTaskId ? "Open investigation" : "Investigate",
            notification: n,
          },
        ],
      };
    });
}

// PENDING_RELEASE_FAMILY is the family the release train raises a
// qualified release under ("release.pending:vX.Y.Z", minor or patch;
// design/playbook/releases.playbook.md,
// design/design/release-from-trunk.design.md §4).
const PENDING_RELEASE_FAMILY = "release.pending";

// pendingReleaseOf is the release a notification asks a person to
// approve, { repo, version, soak, shortSoak }, or null when it asks
// nothing of the kind: a minor release (vX.Y.0) or a patch release the
// train staged (vX.Y.Z, Z >= 1).
//
// soak ("soaked 1h00m of 24h (FORCE=true)") and shortSoak (the SHORT
// SOAK warning) are read off the body only for soak-era -pending tags
// whose Soak-Seconds and Soak-Forced trailers wrote them there; the
// qualification-driven train writes "Qualification: PASS" instead, so
// both are null on new notifications.
export function pendingReleaseOf(n) {
  const family = n?.family || (n?.subject || "").split(":")[0];
  if (family !== PENDING_RELEASE_FAMILY) return null;
  if (!(n.actions || []).includes("approve-release")) return null;
  const version = (n.subject || "").slice(PENDING_RELEASE_FAMILY.length + 1);
  if (!/^v\d+\.\d+\.\d+$/.test(version)) return null;
  const lines = (n.body || "").split("\n").map((l) => l.trim());
  const soak =
    lines
      .find((l) => l.startsWith("Soak: "))
      ?.match(/soaked \d+h\d{2}m of \d+h(?: \(FORCE=true\))?/)?.[0] || null;
  const shortSoak = lines.find((l) => l.startsWith("SHORT SOAK:")) || null;
  return { repo: n.repo || "ai-on-gke/grain", version, soak, shortSoak };
}

// decideRelease approves (or rejects) a pending release, after asking:
// approving makes it official -- vX.Y.Z tagged, `latest` moved -- and
// rejecting drops its release candidate. The daemon refuses anybody but
// a person. A soak-era release whose soak was short is approved with
// short_soak_ok, and only after a question that says so: without it the
// approve job refuses the release. Resolves to false when the person
// thought better of it.
export async function decideRelease(release, approve) {
  const shortSoakOk = approve && !!release.shortSoak;
  const question = !approve
    ? `Reject ${release.version}? Its release candidate is dropped, and the next release train run cuts the next one.`
    : shortSoakOk
      ? `Approve ${release.version} on a SHORT SOAK? It ${release.soak || "has no recorded soak"}, under the minimum. ` +
        `This accepts the short soak (short_soak_ok), tags ${release.version}, moves latest onto it and publishes its images.`
      : `Approve ${release.version}? This tags ${release.version}, moves latest onto it and publishes its images.`;
  if (!window.confirm(question)) return false;
  await api(
    `${repoApiRoot(release.repo)}/pending-releases/${encodeURIComponent(release.version)}/${approve ? "approve" : "reject"}`,
    shortSoakOk
      ? { method: "POST", body: JSON.stringify({ short_soak_ok: true }) }
      : { method: "POST" },
  );
  return true;
}

// notificationSentence is a notification's body as one line under its
// title, with how often it has been raised when that is more than once.
// A body is written as markdown (the base-branch reconciler's names the
// branch as `main` and its repair as [Task #5](/tasks/5)), but the bell
// and the Inbox draw this line as text, so a link is reduced to its text
// and inline code to its contents rather than shown as markup.
export function notificationSentence(n) {
  const body = (n.body || "")
    .trim()
    .split("\n")[0]
    .replace(/\[([^\]\n]+)\]\([^)\s]+\)/g, "$1")
    .replace(/`([^`\n]+)`/g, "$1");
  const again = n.raiseCount > 1 ? ` Raised ${n.raiseCount} times.` : "";
  return `${body || n.subject}${again}`;
}

export async function ackNotification(n) {
  await api(`/api/notifications/${n.id}/ack`, { method: "POST" });
}

// investigateNotification files (or finds) the notification's
// investigation task and resolves to its id.
export async function investigateNotification(n) {
  const resp = await api(`/api/notifications/${n.id}/investigate`, {
    method: "POST",
  });
  return resp?.taskId || n.investigationTaskId || null;
}
