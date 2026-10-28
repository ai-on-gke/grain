// Package authfixtest registers a made-up authfix classifier, so that core
// packages can test what they do with a lapsed host credential without
// knowing any real environment's.
//
// The made-up host refuses a binary with ErrLapsed (or says Reason on its
// stderr), and the fix is to run `renew-host-creds`.
package authfixtest

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/authfix"
)

// Reason is what the made-up host says when it refuses a binary.
const Reason = "host credential lapsed"

// ErrLapsed is the made-up errno-like refusal, for exec errors.
var ErrLapsed = errors.New(Reason)

// LoginHost is the made-up login page a lapsed credential is redirected to.
const LoginHost = "login.example.test"

// Account and Host are who and where Who answers while Register is in
// effect.
const (
	Account = "alice"
	Host    = "build-1.example.test"
)

// Summary and Action are the made-up Fix's words.
const (
	Summary = "host credentials expired"
	Action  = "run `renew-host-creds` as " + Account + " on " + Host + ", then retry"
)

// Fix is the made-up classifier's verdict.
var Fix = authfix.Fix{Summary: Summary, Action: Action, Reason: Reason}

// Register installs the made-up classifiers, and fixes who and where, for
// the rest of the test binary: they are left in place rather than removed
// at t's end, so that parallel tests registering the same never see them
// vanish under each other. Nothing outside a test imports this package.
func Register(t testing.TB) {
	t.Helper()
	authfix.RegisterExec("zz-authfixtest", classify)
	authfix.RegisterRedirect("zz-authfixtest", func(loc *url.URL) (authfix.Fix, bool) {
		return Fix, loc.Hostname() == LoginHost
	})
	authfix.SetWho(Account, Host)
}

func classify(err error, stderr string) (authfix.Fix, bool) {
	if errors.Is(err, ErrLapsed) || (err != nil && strings.Contains(err.Error(), Reason)) ||
		strings.Contains(stderr, Reason) {
		return Fix, true
	}
	return authfix.Fix{}, false
}
