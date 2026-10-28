package authfix_test

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"testing"

	"github.com/bwsalmon/grain/pkg/authfix"
	"github.com/bwsalmon/grain/pkg/authfix/authfixtest"
)

func TestWrapUsesTheRegisteredClassifier(t *testing.T) {
	authfixtest.Register(t)
	const path = "/opt/tools/cli"
	execErr := &fs.PathError{Op: "fork/exec", Path: path, Err: authfixtest.ErrLapsed}
	err := authfix.Wrap(fmt.Errorf("running: %w", execErr), "", "agy")
	got, ok := authfix.As(err)
	if !ok {
		t.Fatalf("Wrap = %v, want an *authfix.Error", err)
	}
	if got.Path != path {
		t.Errorf("Path = %q, want %q (exec's own path, over the caller's)", got.Path, path)
	}
	want := authfixtest.Summary + ": " + authfixtest.Action + " (" + path + " could not run: " + authfixtest.Reason + ")"
	if got.Error() != want {
		t.Errorf("Error() = %q\nwant %q", got.Error(), want)
	}
	if !errors.Is(err, authfixtest.ErrLapsed) {
		t.Error("the wrapped error no longer answers errors.Is")
	}
	if again := authfix.Wrap(err, "", "other"); again != err {
		t.Errorf("Wrap of an *Error = %v, want it unchanged", again)
	}
}

func TestWrapReadsStderr(t *testing.T) {
	authfixtest.Register(t)
	err := authfix.Wrap(nil, "  tool: "+authfixtest.Reason+"\n", "tool")
	got, ok := authfix.As(err)
	if !ok || got.Path != "tool" || got.Err.Error() != "tool: "+authfixtest.Reason {
		t.Fatalf("Wrap = %#v, want an *authfix.Error for tool carrying its stderr", err)
	}
}

func TestWrapLeavesUnrecognisedFailuresAlone(t *testing.T) {
	authfixtest.Register(t)
	for _, err := range []error{
		nil,
		errors.New("exit status 1"),
		&fs.PathError{Op: "fork/exec", Path: "/usr/bin/agy", Err: fs.ErrNotExist},
	} {
		if got := authfix.Wrap(err, "something else went wrong", "agy"); got != err {
			t.Errorf("Wrap(%v) = %v, want it unchanged", err, got)
		}
	}
}

func TestClassifiersAreConsultedInNameOrderAndUnregister(t *testing.T) {
	first := authfix.Fix{Summary: "first", Action: "a", Reason: "boom"}
	second := authfix.Fix{Summary: "second", Action: "b", Reason: "boom"}
	unB := authfix.RegisterExec("authfix-test-b", func(error, string) (authfix.Fix, bool) { return second, true })
	unA := authfix.RegisterExec("authfix-test-a", func(error, string) (authfix.Fix, bool) { return first, true })
	if fix, ok := authfix.ClassifyExec(errors.New("boom"), ""); !ok || fix != first {
		t.Errorf("ClassifyExec = %+v, %v; want the first by name", fix, ok)
	}
	unA()
	if fix, ok := authfix.ClassifyExec(errors.New("boom"), ""); !ok || fix != second {
		t.Errorf("after unregistering: ClassifyExec = %+v, %v; want the second", fix, ok)
	}
	unB()
	if _, ok := authfix.ClassifyExec(nil, ""); ok {
		t.Error("ClassifyExec of nothing recognised something")
	}
}

func TestClassifyRedirect(t *testing.T) {
	authfixtest.Register(t)
	if fix, ok := authfix.ClassifyRedirect(&url.URL{Scheme: "https", Host: authfixtest.LoginHost, Path: "/sso"}); !ok || fix != authfixtest.Fix {
		t.Errorf("ClassifyRedirect(login page) = %+v, %v", fix, ok)
	}
	if _, ok := authfix.ClassifyRedirect(&url.URL{Scheme: "https", Host: "forge.example.test"}); ok {
		t.Error("a redirect elsewhere was read as a lapsed credential")
	}
	if _, ok := authfix.ClassifyRedirect(nil); ok {
		t.Error("no redirect was read as a lapsed credential")
	}
}

func TestWho(t *testing.T) {
	defer authfix.SetWho("", "")()
	t.Setenv("USER", "")
	if account, host := authfix.Who(); account != "the daemon's account" || host != "this host" {
		t.Errorf("Who() = %q, %q; want descriptions rather than nothing", account, host)
	}
}
