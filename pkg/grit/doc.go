// Package grit is grain's CI engine as a service of its own
// (docs/design/grit-split.md). It knows trees, workflows, jobs, results
// and attestations, and nothing about tasks, pull requests or the merge
// queue.
//
// The service itself arrives over GS-2. What lives here now is the
// import lint (independence_test.go) that keeps grit's packages off
// grain-only ones from the start; the one-job Actions runner that used
// to be this package is pkg/grit/actions.
package grit
