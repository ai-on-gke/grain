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

// notifications.js is operator notifications in the UI
// (docs/design/durable-tasks.md, DT-5): the open ones, read from
// GET /api/notifications?open=1, joining the alerts bell's list
// (notificationAlerts) and the Inbox's Notifications section.
//
// Acknowledging hides a notification from the bell and nothing else: it
// stays on the Inbox until whatever raised it clears it.

// POLL_MS is how often the open list is re-read. The bell and the Inbox
// share it, so a raise shows up in both within one poll.
const POLL_MS = 30000;

// useOpenNotifications is the open notifications, newest first, and a
// function that re-reads them now. A failed read keeps the last list. A
// hidden tab skips the poll, like the rest of the app's, and catches up
// on its next tick once it is shown again.
export function useOpenNotifications() {
  const [list, setList] = useState([]);
  const reload = useCallback(async () => {
    try {
      const next = await api("/api/notifications?open=1");
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
          ...(release
            ? [
                {
                  kind: "approve-release",
                  label: "Approve",
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
// release whose soak passed under ("release.pending:vX.Y.0",
// docs/design/release-from-trunk.md §4).
const PENDING_RELEASE_FAMILY = "release.pending";

// pendingReleaseOf is the release a notification asks a person to
// approve, { repo, version }, or null when it asks nothing of the kind.
export function pendingReleaseOf(n) {
  const family = n?.family || (n?.subject || "").split(":")[0];
  if (family !== PENDING_RELEASE_FAMILY) return null;
  if (!(n.actions || []).includes("approve-release")) return null;
  const version = (n.subject || "").slice(PENDING_RELEASE_FAMILY.length + 1);
  if (!/^v\d+\.\d+\.0$/.test(version)) return null;
  return { repo: n.repo || "bwsalmon/grain", version };
}

// decideRelease approves (or rejects) a pending release, after asking:
// approving makes it official -- vX.Y.0 tagged, `latest` moved -- and
// rejecting drops its release candidate. The daemon refuses anybody but
// a person. Resolves to false when the person thought better of it.
export async function decideRelease(release, approve) {
  const question = approve
    ? `Approve ${release.version}? This tags ${release.version}, moves latest onto it and publishes its images.`
    : `Reject ${release.version}? Its release candidate is dropped, and the next release train run cuts the next one.`;
  if (!window.confirm(question)) return false;
  const [owner, name] = release.repo.split("/");
  await api(
    `/api/repos/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/pending-releases/${encodeURIComponent(release.version)}/${approve ? "approve" : "reject"}`,
    { method: "POST" },
  );
  return true;
}

// notificationSentence is a notification's body as one line under its
// title, with how often it has been raised when that is more than once.
export function notificationSentence(n) {
  const body = (n.body || "").trim().split("\n")[0];
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
