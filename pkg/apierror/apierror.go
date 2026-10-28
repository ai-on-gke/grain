// Package apierror decides what an HTTP API may say about a failure.
//
// A handler's error is written for the operator reading the daemon's
// log: it carries SQL driver messages, dial addresses, file paths and
// whatever an upstream service said back. None of that belongs in a
// response body, which reaches any caller who can make the request.
// Body keeps a 4xx error's message -- it describes the caller's own
// request -- and replaces a 5xx one with a short classified summary and
// a reference that finds the full error in the log.
//
// A 5xx message written for the caller on purpose ("this deployment
// does not run a proxy cache") is marked with Public and passes through.
package apierror

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/url"
	"os"
)

// Response is the JSON body of an API error.
type Response struct {
	// Error is the message shown to the caller.
	Error string `json:"error"`
	// Code classifies a server-side failure; empty for a 4xx.
	Code string `json:"code,omitempty"`
	// Ref is the log reference of a sanitized server-side failure.
	Ref string `json:"ref,omitempty"`
}

// Classification codes for sanitized server-side failures.
const (
	CodeInternal    = "internal"
	CodeTimeout     = "timeout"
	CodeCanceled    = "canceled"
	CodeUpstream    = "upstream_unavailable"
	CodeStorage     = "storage"
	CodeUnavailable = "unavailable"
)

type publicError struct {
	msg string
	err error
}

func (e *publicError) Error() string { return e.msg }
func (e *publicError) Unwrap() error { return e.err }

// Public returns an error whose message is safe to show any caller,
// whatever status it is written with.
func Public(msg string) error { return &publicError{msg: msg} }

// Expose marks err's whole message as meant for the caller: a route
// whose caller acts on what went wrong (an agent told why its pull
// request did not open, an administrator told why a reboot failed) and
// is already trusted with it. Every use is a decision to disclose, so
// make it where the handler knows who is asking, never in a helper.
func Expose(err error) error {
	if err == nil {
		return nil
	}
	return &publicError{msg: err.Error(), err: err}
}

// Body returns the response body for err written with status, logging
// the full error when it is withheld from the caller.
func Body(status int, err error) Response {
	if err == nil {
		err = errors.New("unknown error")
	}
	if status < 500 {
		return Response{Error: err.Error()}
	}
	var pe *publicError
	if errors.As(err, &pe) {
		return Response{Error: pe.msg}
	}
	code, msg := Classify(status, err)
	ref := newRef()
	log.Printf("api: %d %s [ref %s]: %v", status, code, ref, err)
	return Response{Error: msg + " (ref " + ref + ")", Code: code, Ref: ref}
}

// Classify names the kind of server-side failure err is and a message
// for it that says nothing about the failure's internals.
func Classify(status int, err error) (code, msg string) {
	var netErr net.Error
	var urlErr *url.Error
	var opErr *net.OpError
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded),
		errors.As(err, &netErr) && netErr.Timeout():
		return CodeTimeout, "the operation timed out"
	case errors.Is(err, context.Canceled):
		return CodeCanceled, "the operation was canceled"
	case errors.Is(err, sql.ErrConnDone), errors.Is(err, driver.ErrBadConn), errors.Is(err, sql.ErrTxDone):
		return CodeStorage, "the database is unavailable"
	case errors.As(err, &opErr), errors.As(err, &urlErr):
		return CodeUpstream, "an upstream service could not be reached"
	}
	switch status {
	case 502, 504:
		return CodeUpstream, "an upstream service returned an error"
	case 503:
		return CodeUnavailable, "the service is temporarily unavailable"
	}
	return CodeInternal, "internal server error"
}

func newRef() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}
