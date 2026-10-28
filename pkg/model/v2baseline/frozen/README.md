# grain 2.0.0's schema, frozen

Everything here was written once, by the build that shipped
`model.V2BaselineSchemaVersion` (27), with

    go test ./pkg/model/v2baseline -run TestFreeze -freeze-v2-baseline

and is never edited or regenerated. `TestTheFrozenBaselineIsUnchanged` pins
each file's digest. See `pkg/model/v2baseline`'s package comment and
`docs/data-model.md` ("Schema changes after 2.0") for what holds later builds
to opening it.

- `schema.sqlite.sql`, `schema.postgres.sql`: the DDL 2.0.0's `Store.Init`
  created on each engine.
- `rows.sql`: every row of the fixture deployment (`v2baseline.Seed`).
- `state-repo/`: the same deployment's state-repository dump, stamped 27.
