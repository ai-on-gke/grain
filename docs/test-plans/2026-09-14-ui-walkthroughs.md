# UI walkthroughs

Six independent follow-up tasks divide Grain's UI by operator workflow. The
division was checked against the navigation and browser-test surface, then
reviewed with Gemini 2.5 Flash. It keeps each walkthrough small enough for one
agent to operate the UI carefully while still covering the handoffs between
screens.

| Walkthrough | Surface |
| --- | --- |
| Task intake, Inbox, and lifecycle | Inbox proposals and questions; task creation, approval, editing, defer/pick-up, narrowing, detail, and bulk editing |
| Saved boards and visualizations | Board index and ordering; list, kanban, timeline, dashboard, and report views |
| Repositories, releases, and memory | Repo list and page; task scoping, branches, capabilities, instructions, memory, releases, and release trains |
| Schedules, templates, suites, and runs | Recurring task and suite schedules; reusable templates; suite configuration and run history |
| Deployment settings and operations | Settings, Sandboxes, CI, System, Metrics, and safe handling of host controls |
| Responsive navigation and live sessions | Desktop, tablet, phone, keyboard and touch navigation; standing chats and task consoles |

## Shared walkthrough contract

Every task must build and start a real `grain demo`, open the built frontend in
an actual Playwright browser, and perform the workflow through visible UI
controls. Existing automated tests are useful orientation, but running them or
reading components is not the walkthrough. The agent watches browser-console
and network failures while it works and covers loading, empty, error,
validation, cancellation, and confirmation states that belong to its workflow.

Every mutation is checked again after returning to the screen and, where the
surface has a URL, after reload or direct navigation. An unavailable GitHub,
agent, host, CI, or streaming integration may be stubbed only at that external
boundary; the real Grain server, built React UI, browser layout, and user
gesture stay in the loop. Stubs and their response states are named in the
walkthrough report.

Each agent writes a concise dated report under `docs/test-runs/` with the commit
tested, environment, exact operations, results, and external boundaries it had
to simulate. Generated browser artifacts and credentials are not committed.

A reproducible bug is fixed in the walkthrough task when the correction is
safe and scoped, with regression coverage at the strongest practical level. A
bug that needs separate infrastructure, authority, or a materially broader
change becomes a focused follow-up task containing the reproduction, evidence,
and expected behavior. Those follow-ups use automerge just like the six
walkthrough tasks.

The walkthrough agents use the sandbox's Gemini credential for Gemini-backed
model tooling without printing, logging, or committing the key.
