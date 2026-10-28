# Capability audit in the attempt view (task 5144)

What an attempt's capability grants recorded doing, shown in the task's
attempt overlay and counted on its timeline entry, over the real
`grain demo` with nothing stubbed. The demo's "Add pagination to the
tasks API" task has a finished first attempt that carries host-script
records (`pkg/demo` `demoCapabilityAudit`).

Taken by `ui/e2e/capability-audit.spec.js` with `SHOTS_DIR` set, at
1440px (desktop) and 390px (phone), in light and dark, with Chromium:

- `attempt-entry-*`: the timeline entry. "3 capability audit records"
  sits on its own line under the started/finished meta line.
- `attempt-audit-*`: the overlay. The "Capability audit · 3 records"
  section sits above the transcript. It shows two host-script records
  (script, argv as JSON, exit code, duration, caller, and a refusal
  reason) and grain's note on a record the server left out. The list
  scrolls within 30vh. At 390px the third entry is below that fold
  inside the list, and nothing overflows sideways (the spec asserts
  this with bounding boxes and the list's scroll width).

Checked: records, notes and transcript are legible at both widths in
both themes, with no clipping or sideways overflow. At 390px the
"finished" timestamp on the meta line splits across two lines. It did
that before this change too.
