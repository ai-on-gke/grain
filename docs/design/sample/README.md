# Design-doc checks in `grain declarations check`

This is the sample design doc for the design-doc planner
(`docs/design-docs.md`). `.grain/triggers/design-docs.yml` watches
`docs/design/**`, so a merged edit to this file is what starts the planner on
this folder (that trigger is turned off in this repository for now; a
planner is filed by hand from the `design-planner` template instead). The planner files tasks for the requirements below; the merged
pull request that changed this file is their origin, and grain's progress
comment on it lists them.

## Background

`pkg/designdoc` checks a design doc's requirement IDs, but today the only
thing that runs that check is that package's own test of this folder. A
malformed ID in any other repository is noticed only when the next planner
run reads the doc.

The first requirement this doc carried (`D-1`, checking the planner's
`plan.yml` ledgers) left it when the ledger was replaced by commit trailers.
Its ID stays taken.

## Requirements

### Check requirement IDs in `grain declarations check` {#D-2}

`grain declarations check` also reads the design docs under the paths of
each `on: merge` trigger whose template is the design planner, and fails on a
requirement ID that is malformed or used twice in one doc
(`designdoc.Requirements`). A heading with no ID is not an error. The planner
assigns IDs to those itself.
