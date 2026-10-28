package cli

// schemaupgrade.go is what the UI's Upgrade button leaves behind when the
// build it is cutting over to knows a different schema, and what the
// build it cut over to does with it (grain/task-59).
//
// The problem it exists for. `scripts/setup.sh` has always handled a
// schema change by moving the store aside before the new build ever
// opens it, and by saying so to the state repository
// (reformat_store_if_schema_changed, and reformatStateRepo in
// staterepo.go). The Upgrade button does none of that: it pulls an
// image, runs `grain schema-version` in it as a health check, rewrites
// the ref file and restarts. That health check touches no store on
// purpose, so a release that changed the schema passed it, cut over, and
// then met a store Init refuses -- ErrSchemaTooOld, before the UI ever
// binds a port, with systemd restarting into the identical failure and
// no rollback possible, because the image is perfectly healthy and only
// this host's data is in the way.
//
// The answer here is not to refuse such an upgrade, and not to destroy
// the deployment's state faster. It is to carry the rows across:
//
//  1. before it cuts over, the running build exports its whole database
//     into <data-dir>/schema-upgrade -- an ordinary staterepo dump, plus
//     upgrade.json naming the two schema versions (writeSchemaUpgrade,
//     called by pkg/upgrade's schema step)
//  2. the same step asks the *new* image to rehearse the import, against
//     that dump, at its own schema (`grain state check -migrate`). A
//     failure there stops the upgrade with nothing changed and the old
//     build still serving -- the check the health check could not make
//  3. the new build starts, finds a store at the old schema and this
//     note beside it, moves the store aside under a timestamped name --
//     never deletes it -- creates a fresh one at its own schema, and
//     imports the dump into it (openStoreForSchemaUpgrade)
//  4. the state repository holds the same rows at the same old schema, so
//     it gets the same treatment one step later: staterepo.Migrated
//     exports the migrated database over the dump it cannot read and
//     pushes it, in a commit that says the rows came across
//     (migrateStateRepo, in staterepo.go)
//  5. the note is spent and removed.
//
// What survives is every row the new schema still has somewhere to put.
// What does not is anything written between the export in step 1 and the
// restart seconds later -- that is in the archived store, and nowhere
// else. Saying that plainly is better than the alternatives: an export
// taken at shutdown would have to happen in a process systemd is killing,
// and a deployment quiesced for the length of an image pull is a
// deployment down for the length of an image pull.
//
// Why a copy of the dump rather than the state repository's own working
// tree, which holds one already: the tree is only as fresh as the last
// sync, it may be mid-pull, and a deployment need not have a state
// repository at all. A self-contained directory makes the migration one
// thing to read, one thing to hand `grain state check`, and one thing an
// operator can keep if they want the rows twice over.
