# The Silo hub connection was removed

On 2026-10-19 the owner decided to take the Silo hub connection out of
grain entirely (task 4826). That removed:

- the hub node client, `pkg/hubnode`;
- the daemon's hub registration, event stream and hub filings
  (`pkg/daemon/hub.go`, `hub_events.go`, `hub_filing.go`), and its
  `-hub-*` flags;
- `grain hub login`/`status`, and `grain init workstation`'s hub sign-in;
- Silo claims in the local policy (`silo_issuer`, `silo_keys_url`,
  `silo_audience`, `pkg/policy/silo.go`, `pkg/policy/policytest`), and
  the `task_silo_attestation` and `hub_filing` tables;
- the hub-aware `propose_task`, hub task tags and `depends-on-hub` links;
- the hub status and hub proposal routes, the UI's Silo hub panel, hub
  chips and notes, and the `-hub-tasks` demo seed;
- the hub round-trip test and the `hub e2e` CI job that ran it, the
  hub-connect install walkthrough, and `docs/design/grain-silo.md`.

The local policy and its audit log stay. The audit types now live in
`pkg/policy/audit.go`, and the log keeps its `hub-audit.log` file name.

The last commit on `main` that had the hub is
`d2115416e4c7fc0558ea224cb0eb175e60bacd1a`.

## Restoring it

Revert the removal, or bring back the pieces you need from that commit:

```sh
git revert <the removal's merge commit>
# or, piece by piece:
git checkout d2115416e4c7fc0558ea224cb0eb175e60bacd1a -- pkg/hubnode pkg/daemon/hub.go
```

Then rebuild the schema and the state-repo table lists to match, and
re-add `github.com/bwsalmon/silo` to `go.mod` for the security tests that
used it.
