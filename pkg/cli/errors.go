// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// errors.go is how the task CLI ends: which exit code an error is
// (design/spec/external-cli/external-cli.spec.md, #XC-13), and what it
// writes on stderr -- a sentence by default, and under -json a single
// {"error": {...}} object a script can read without parsing English.
//
// The exit code is the part a script branches on, so it says what kind
// of thing went wrong rather than that something did:
//
//	0  success
//	1  the thing asked about failed (a red CI run), or the daemon did (5xx)
//	2  the command was wrong: usage, a flag, an argument, a 400/404/409/422
//	3  the daemon would not take this CLI's credential (401, 403, sign-in)
//	4  no daemon answered at all (refused, reset, DNS, TLS, timeout)
//	5  a blocking wait ran out of time before what it waited for happened
package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const (
	exitOK           = 0
	exitFailed       = 1
	exitUsage        = 2
	exitAuth         = 3
	exitUnreachable  = 4
	exitWaitTimedOut = 5
)

// cliError is an error that already knows how it ends: its exit code,
// the snake_case code -json reports it under, and a command that would
// fix it when there is one.
type cliError struct {
	exit int
	code string
	fix  string
	err  error
}

func (e *cliError) Error() string { return e.err.Error() }
func (e *cliError) Unwrap() error { return e.err }

// usageErrorf is a command typed wrong: exit 2.
func usageErrorf(format string, a ...any) error {
	return &cliError{exit: exitUsage, code: "usage", err: fmt.Errorf(format, a...)}
}

// targetFailed is the answer to the question asked being "it failed" --
// a CI run that concluded red -- which is a verdict, not a fault: exit 1.
func targetFailed(err error) error {
	if err == nil {
		return nil
	}
	return &cliError{exit: exitFailed, code: "target_failed", err: err}
}

// waitTimedOut is a blocking wait whose -timeout ran out first: exit 5.
func waitTimedOut(err error) error {
	return &cliError{exit: exitWaitTimedOut, code: "wait_timeout", err: err}
}

// classifyError says how err ends the command: its exit code, its -json
// code, and a remediation when one applies.
func classifyError(err error) (exit int, code, fix string) {
	if err == nil {
		return exitOK, "", ""
	}
	var ce *cliError
	if errors.As(err, &ce) {
		return ce.exit, ce.code, ce.fix
	}
	if errors.Is(err, flag.ErrHelp) {
		return exitOK, "", ""
	}
	var unknown unknownCommandError
	if errors.As(err, &unknown) {
		return exitUsage, "unknown_command", "grain -h"
	}
	if flagParseError(err) {
		return exitUsage, "invalid_flag", ""
	}

	var login *ui.LoginRequiredError
	if errors.As(err, &login) {
		return exitAuth, "login_required", login.Fix
	}
	var expired *ui.TokenExpiredError
	if errors.As(err, &expired) {
		return exitAuth, "auth_expired", "grain login --refresh"
	}
	var notFound *ui.NotFoundError
	var validation *ui.ValidationError
	var forbidden *ui.ForbiddenError
	var unavailable *ui.UnavailableError
	var status *ui.StatusError
	var hubAPI *api.APIError
	var down *ui.DaemonUnavailableError
	switch {
	case errors.As(err, &validation):
		return exitUsage, "invalid_request", ""
	case errors.As(err, &notFound):
		return exitUsage, "not_found", ""
	case errors.As(err, &forbidden):
		return exitAuth, "forbidden", ""
	case errors.As(err, &status):
		return classifyStatus(status.StatusCode)
	case errors.As(err, &hubAPI):
		return classifyStatus(hubAPI.StatusCode)
	case errors.As(err, &unavailable):
		return exitFailed, "unavailable", ""
	case errors.As(err, &down):
		return exitUnreachable, "daemon_unreachable", ""
	}
	if transportError(err) {
		return exitUnreachable, "daemon_unreachable", ""
	}
	if signInPage(err) {
		return exitAuth, "login_required", ""
	}
	// The CLI's own refusals of what it was given, before anything was
	// sent: a missing id, a missing required flag.
	if msg := err.Error(); strings.Contains(msg, " is required") || strings.Contains(msg, " are required") {
		return exitUsage, "usage", ""
	}
	return exitFailed, "error", ""
}

// classifyStatus is classifyError for an HTTP status the daemon answered
// with that pkg/ui has no type of its own for.
func classifyStatus(code int) (int, string, string) {
	switch {
	case code == http.StatusUnauthorized:
		return exitAuth, "unauthenticated", ""
	case code == http.StatusForbidden:
		return exitAuth, "forbidden", ""
	case code == http.StatusConflict:
		return exitUsage, "conflict", ""
	case code == http.StatusUnprocessableEntity:
		return exitUsage, "unprocessable", ""
	case code == http.StatusNotFound:
		return exitUsage, "not_found", ""
	case code == http.StatusTooManyRequests:
		// The daemon's per-token rate limit (externalAgentLimiter): the
		// same command succeeds unchanged once the bucket refills, so it
		// is not a usage error an agent should rewrite its request over.
		return exitFailed, "rate_limited", ""
	case code >= 400 && code < 500:
		return exitUsage, "invalid_request", ""
	default:
		return exitFailed, "server_error", ""
	}
}

// flagParseError reports one of the flag package's own refusals. It has
// no error types of its own, only these sentences.
func flagParseError(err error) bool {
	msg := err.Error()
	for _, prefix := range []string{
		"flag provided but not defined",
		"invalid value ",
		"invalid boolean ",
		"flag needs an argument",
		"bad flag syntax",
	} {
		if strings.HasPrefix(msg, prefix) {
			return true
		}
	}
	return false
}

// transportError is a failure to reach any daemon at all, as opposed to
// one that answered: DNS, a refused or reset connection, TLS, a timeout.
func transportError(err error) bool {
	if daemonRefused(err) || daemonTimedOut(err) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return true
	}
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var record tls.RecordHeaderError
	var verify *tls.CertificateVerificationError
	if errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalid) ||
		errors.As(err, &record) || errors.As(err, &verify) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "tls: ") ||
		strings.Contains(msg, "server gave HTTP response to HTTPS client")
}

// signInPage is a proxy in front of the daemon answering with its HTML
// sign-in page rather than the JSON the CLI asked for: the CLI is not
// signed in to it.
func signInPage(err error) bool {
	msg := err.Error()
	return strings.HasPrefix(msg, "decoding response from ") && strings.Contains(msg, "invalid character '<'")
}

// jsonError is the one object -json writes on stderr for a failed
// command.
type jsonError struct {
	Error jsonErrorBody `json:"error"`
}

type jsonErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

// reportError writes err to stderr the way the command was asked to
// speak and returns the exit code it ends with.
func reportError(stderr io.Writer, err error, asJSON bool) int {
	exit, code, fix := classifyError(err)
	if exit == exitOK {
		return exitOK
	}
	if asJSON {
		if code == "target_failed" {
			// The result document on stdout already says what failed.
			return exit
		}
		enc := json.NewEncoder(stderr)
		_ = enc.Encode(jsonError{Error: jsonErrorBody{Code: code, Message: err.Error(), Fix: fix}})
		return exit
	}
	fmt.Fprintln(stderr, "grain: "+err.Error())
	return exit
}

// jsonRequested reports whether args turn -json on for the task CLI,
// wherever among them it was written (#XC-11): what reportError needs to
// know even when parsing args is what failed.
func jsonRequested(args []string) bool {
	on := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		name, value, hasValue := splitFlag(arg)
		if name != "json" {
			continue
		}
		on = !hasValue || value == "true" || value == "1" || value == "t" || value == "TRUE" || value == "True"
	}
	return on
}

// waitLimitKey carries an explicit -timeout to the commands that block
// until something happens (`grain ci watch`, `grain ci run -watch`).
// Every call is held to -timeout anyway; a wait made of many calls is
// held to it as a whole only when somebody asked for one, so a run that
// takes an hour can still be watched with nothing set.
type waitLimitKey struct{}

func withWaitLimit(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, waitLimitKey{}, d)
}

// waitContext is ctx bounded by the -timeout a blocking wait was given,
// if it was given one.
func waitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if d, ok := ctx.Value(waitLimitKey{}).(time.Duration); ok && d > 0 {
		return context.WithTimeout(ctx, d)
	}
	return context.WithCancel(ctx)
}

// waitResult is err from a blocking wait made under ctx, as a
// wait_timeout (exit 5) when it was ctx's -timeout that ended it.
func waitResult(ctx context.Context, err error) error {
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return waitTimedOut(err)
	}
	return err
}
