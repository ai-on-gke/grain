package lifecycle

// The two things this package needs of whatever holds a serving sandbox:
// the environment the serve command runs with, and what happens on a
// deployment where nothing can hold one.
//
// Plus the one diagnostic worth having: a definition that never becomes
// ready should say what it wrote, because "not ready in 30s" and "not
// ready in 30s: ModuleNotFoundError" send an operator to two different
// places.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// The token is the only thing authenticating the reducer and the listen
// address saves the server parsing the declaration it was started from.
// Both are grain's, added after the declaration's own literals -- and
// pkg/repodecl refuses a declaration that names either, so this cannot be
// overwritten from a repository and does not silently lose to one.
func TestServeEnvironmentCarriesGrainsOwnVariablesAndTheDeclarationsLiterals(t *testing.T) {
	declared := declaration()
	declared.Serve.Env = map[string]string{"LIFECYCLE_LOG_LEVEL": "info"}
	env := ServeEnvironment(ServeRequest{Declaration: declared, Token: "sealed"})

	if env["LIFECYCLE_LOG_LEVEL"] != "info" {
		t.Errorf("the declaration's own literal was dropped: %v", env)
	}
	if env[model.LifecycleTokenEnv] != "sealed" {
		t.Errorf("%s = %q", model.LifecycleTokenEnv, env[model.LifecycleTokenEnv])
	}
	if env[model.LifecycleListenEnv] != declared.Serve.Listen {
		t.Errorf("%s = %q, want the resolved listen address", model.LifecycleListenEnv, env[model.LifecycleListenEnv])
	}
	// The declaration's map is not the one handed out, so a caller
	// mutating what it got does not edit the declaration it came from.
	env["LIFECYCLE_LOG_LEVEL"] = "debug"
	if declared.Serve.Env["LIFECYCLE_LOG_LEVEL"] != "info" {
		t.Error("the declaration's own environment was modified")
	}
}

// A deployment whose backend cannot serve fails the way every other
// lifecycle failure fails: held, with a reason on the repository's
// lifecycle that an operator reads in the pane. Not a nil-map panic on the
// first observation of the first repository that declares one.
func TestAnUnsupportedBackendHoldsWithItsOwnReason(t *testing.T) {
	sandboxes := UnsupportedSandboxes(errors.New("the local container backend refuses NetworkAccess none"))
	r := &Registry{
		Declarations: &fakeDeclarations{declared: declaration(), commit: "c0ffee"},
		Images:       &fakeImages{digest: testDigest, local: "local:abc"},
		Sandboxes:    sandboxes,
		IdleTTL:      time.Hour,
	}
	t.Cleanup(func() { r.Close(context.Background()) })

	version, err := r.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_, err = r.Reducer(context.Background(), version.String())
	if err == nil {
		t.Fatal("it served a version on a deployment with nothing to serve it")
	}
	if !strings.Contains(err.Error(), "refuses NetworkAccess none") {
		t.Errorf("%v does not carry the backend's own reason", err)
	}
	status := r.Status(context.Background(), repo)
	if status.State != model.LifecycleFailed || status.Error == "" {
		t.Errorf("status = %+v -- an operator has nothing to act on", status)
	}
}

// talkativeServing is a fakeServing that can say what the server wrote.
type talkativeServing struct {
	*fakeServing
	said string
	asks int
}

func (s *talkativeServing) Output(ctx context.Context) string {
	s.asks++
	// The ask must survive the expired context the readiness timeout is
	// reporting on, so a fake that respected it would hide a real bug.
	if err := ctx.Err(); err != nil {
		return "(the ask was handed an expired context: " + err.Error() + ")"
	}
	return s.said
}

// talkativeSandboxes hands out one.
type talkativeSandboxes struct {
	inner *fakeSandboxes
	last  *talkativeServing
	said  string
}

func (f *talkativeSandboxes) Serve(ctx context.Context, req ServeRequest) (Serving, error) {
	served, err := f.inner.Serve(ctx, req)
	if err != nil {
		return nil, err
	}
	f.last = &talkativeServing{fakeServing: served.(*fakeServing), said: f.said}
	return f.last, nil
}

// A definition that never answers ready is a lifecycle failure, and the
// failure should carry what the server wrote -- which is very often the
// whole answer.
func TestAReadinessTimeoutNamesWhatTheServerWrote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"protocol":"1","ready":false,"definition":"starting"}`)
	}))
	t.Cleanup(server.Close)

	declared := declaration()
	declared.Serve.ReadyTimeout = 200 * time.Millisecond
	sandboxes := &talkativeSandboxes{
		inner: &fakeSandboxes{server: server},
		said:  "ModuleNotFoundError: No module named 'lifecycle'",
	}
	r := &Registry{
		Declarations: &fakeDeclarations{declared: declared, commit: "c0ffee"},
		Images:       &fakeImages{digest: testDigest, local: "local:abc"},
		Sandboxes:    sandboxes,
		IdleTTL:      time.Hour,
	}
	t.Cleanup(func() { r.Close(context.Background()) })

	version, err := r.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Reducer(context.Background(), version.String())
	if err == nil {
		t.Fatal("a definition that never became ready was served")
	}
	if !strings.Contains(err.Error(), "ready-timeout-seconds") {
		t.Errorf("%v does not name the timeout", err)
	}
	if !strings.Contains(err.Error(), "ModuleNotFoundError") {
		t.Errorf("%v does not carry what the server wrote -- which is the whole answer here", err)
	}
	if sandboxes.last == nil || sandboxes.last.asks != 1 {
		t.Errorf("the server was asked for its output %d times", sandboxes.last.asks)
	}
}

// A backend that cannot produce the output should still be able to serve:
// it is a diagnostic, not a capability.
func TestAReadinessTimeoutWithoutAnOutputIsStillAReadinessTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"protocol":"1","ready":false}`)
	}))
	t.Cleanup(server.Close)

	declared := declaration()
	declared.Serve.ReadyTimeout = 200 * time.Millisecond
	r := registry(t, &fakeDeclarations{declared: declared, commit: "c0ffee"},
		&fakeImages{digest: testDigest, local: "local:abc"}, &fakeSandboxes{server: server})

	version, err := r.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reducer(context.Background(), version.String()); err == nil {
		t.Fatal("served")
	} else if !strings.Contains(err.Error(), "ready-timeout-seconds") {
		t.Errorf("%v", err)
	}
}

// And a server that wrote nothing does not decorate the error with an
// empty clause.
func TestNoOutputAddsNothingToTheError(t *testing.T) {
	quiet := &talkativeServing{fakeServing: &fakeServing{}, said: "   "}
	if got := servedOutput(context.Background(), quiet); got != "" {
		t.Errorf("got %q", got)
	}
	loud := &talkativeServing{fakeServing: &fakeServing{}, said: "boom"}
	if got := servedOutput(context.Background(), loud); !strings.Contains(got, "boom") {
		t.Errorf("got %q", got)
	}
	// A Serving that cannot say anything is not asked.
	if got := servedOutput(context.Background(), &fakeServing{}); got != "" {
		t.Errorf("got %q", got)
	}
}
