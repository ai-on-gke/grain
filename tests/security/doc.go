// Package security is grain's security regression suite: one place that
// states, as tests, the trust boundaries the rest of the tree is meant to
// keep, so that a change which quietly moves one fails here by name
// rather than surfacing in the next audit.
//
// It has two layers, and `make security` runs both:
//
//   - Layer 1, static (static_test.go): rules over the non-test Go
//     source under cmd/ and pkg/ -- constructs that have no business in
//     this tree whatever the surrounding code does, like a TLS client
//     that skips verification outright or an identity read out of a URL.
//     Each rule says why it exists and what to do instead, and an
//     exemption is a line in the rule's allowlist with its reason beside
//     it, so relaxing one is a reviewed diff rather than a comment
//     somebody sprinkled on a call site.
//   - Layer 1, ratchet (ratchet_test.go): rules the tree does not keep
//     everywhere yet -- exec.Command inheriting the daemon's environment,
//     default HTTP clients in the packages that carry credentials, TLS
//     verification turned off in image builds and scripts, variables
//     handed to git ahead of "--", and lexical strings.HasPrefix path
//     containment checks instead of safepath.Within. Today's
//     violations are counted per file in .grain/baselines/<rule>.txt;
//     a count that rises fails, and so does one that falls without the
//     baseline being lowered, so the count only ever goes down.
//   - Layer 1b, route authorization (routes_test.go): every route
//     pkg/ui's Server registers is in a table saying who may call it --
//     public, or only a caller the single-tenant daemon trusts as itself
//     -- and each trusted route must refuse a caller without the access
//     code and answer the one with it. Gaps are a ratchet like the ones
//     above, in .grain/baselines/route-authorization.txt.
//   - Layer 2, behavioural (the other files): the boundaries themselves,
//     driven through the exported entry points real requests reach --
//     who a request is believed to be, what a session token has to be to
//     be accepted (identity_test.go), which CI artifact names can become
//     a path on the daemon's disk, what a sandbox's file tools will touch
//     and what run_command inherits (containment_test.go), and:
//   - the git proxy (gitproxy_test.go): a sandbox's token fetches and
//     pushes its own task's target, only fetches its Reads, and is
//     refused "not in scope for this sandbox" for anything else --
//     another live task's repository included;
//   - capabilities (capabilities_test.go): a team-restricted capability
//     is refused to every other team, a proposal cannot widen what its
//     parent holds without a person, and a scheduled CI run gets no
//     publishing credential and no release secret;
//   - egress (egress_test.go): a sandbox with network access "none"
//     resolves the package cache and nothing else, with no query
//     forwarded upstream, and a backend that cannot isolate refuses one;
//   - MCP tools (mcp_tools_test.go): a run with no grant is offered its
//     sandbox's tools, the escape hatches and its own branch's CI and
//     nothing more, each grant's tools appear only with it, and
//     repository-scoped tools refuse a repository outside the task.
//
// A behavioural boundary found broken is not fixed in this package: it
// is filed, and its test names it as a known gap -- a knownGap constant
// or list beside the test, which skips with what it found while the gap
// stands and fails once it closes, so the entry is removed and the
// boundary is held from then on.
//
// It is an ordinary package in the main module, so `go test ./...` -- and
// with it the `go` job in .grain/workflows/tests.yml -- runs it on every
// commit; `make security` is the same tests on their own, plus `go vet`,
// for somebody who wants the verdict without the rest of the suite.
package security
