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

import { useEffect, useState } from "react";
import api from "../api.js";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";

import GrantChip from "./GrantChip.jsx";

// recordFields is a server's record as key/value pairs, less the two
// every entry already shows in its head (time) or that are the same on
// every one of a grant's records (attempt_id). A string shows as itself
// and anything else as JSON, so an argv array keeps its quoting and an
// empty argument is still visible.
function recordFields(record) {
  if (!record || typeof record !== "object") return [];
  return Object.entries(record)
    .filter(([key]) => key !== "time" && key !== "attempt_id")
    .map(([key, value]) => [
      key,
      typeof value === "string" ? value : JSON.stringify(value),
    ]);
}

// CapabilityAudit lists what an attempt's capability grants did for it,
// as their servers reported it when the attempt released them
// (ui.AttemptCapabilityAudit, design/spec/capabilities/capabilities.spec.md §4.4.4):
// host-script's record of every call it ran or refused, above all.
// Everything shown is the capability server's own record, untrusted,
// argv exactly as the sandbox passed it; a note is grain's own word about
// the grant's audit (records left out, an audit refused). Nothing is
// fetched for an attempt with no records.
export default function CapabilityAudit({ taskId, attempt, showError }) {
  const zone = useTimeZone();
  const count = attempt.capabilityAuditCount || 0;
  const [records, setRecords] = useState(null);

  useEffect(() => {
    if (!count) return;
    let live = true;
    api(
      `/api/tasks/${encodeURIComponent(taskId)}/attempts/${attempt.number}/capability-audit`,
    )
      .then((res) => live && setRecords(res?.records || []))
      .catch((err) => showError && showError(err));
    return () => {
      live = false;
    };
  }, [taskId, attempt.number, count, showError]);

  if (!count) return null;
  return (
    <section className="capability-audit" aria-label="Capability audit">
      <h3>
        Capability audit · {count} record{count === 1 ? "" : "s"}
      </h3>
      <p className="capability-audit-hint">
        What this attempt&apos;s capability grants did, as their servers
        recorded it. Arguments are shown exactly as the sandbox passed them.
      </p>
      {records === null ? (
        <p className="capability-audit-hint">Loading…</p>
      ) : (
        <ol className="capability-audit-list">
          {records.map((r, i) => (
            <li key={i} className="capability-audit-entry">
              <div className="capability-audit-head">
                {/* The server and method tell two grants of one attempt
                    apart at a glance; the whole grant is the chip's
                    tooltip. */}
                <span className="capability-audit-grant">
                  <GrantChip grant={r.grant} variant="outlined" />
                </span>
                <span className="capability-audit-time">
                  {formatDateTime(r.at, zone)}
                </span>
              </div>
              {r.note ? (
                <div className="capability-audit-note">
                  <span className="task-activity-by">grain</span>
                  {r.note}
                </div>
              ) : (
                <dl className="capability-audit-fields">
                  {recordFields(r.record).map(([key, value]) => (
                    <div key={key}>
                      <dt>{key}</dt>
                      <dd>{value}</dd>
                    </div>
                  ))}
                </dl>
              )}
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
