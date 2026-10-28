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

package statemachine

// The REST contract a repository's definition implements
// (docs/task-lifecycle-sandbox.md, "The REST contract").
//
// Every row of the error table is a test here, because the table is the
// contract and most of its rows are indistinguishable from each other
// until something depends on the difference. The row that matters most is
// the last one: "the definition said something wrong" is the definition's
// problem and belongs in reducer.failed, while "the machine went away" is
// grain's problem and must not consume the event or move the snapshot.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const okBody = `{"id":"observation-12","state":{"phase":"ready_to_submit"},` +
	`"effects":[{"type":"task.submit","data":{}}],"accepts":["task.observed","submission.cancelled"],` +
	`"lifecycle":"active","attention":false,` +
	`"presentation":{"label":"Ready to submit","description":"CI is green on the third attempt",` +
	`"tone":"success","activity":"idle"},` +
	`"inputs":[{"id":"cancel_submission","kind":"confirm","label":"Cancel submission",` +
	`"event":"submission.cancelled"}]}`

// serve starts a definition and returns a reducer pointed at it.
func serve(t *testing.T, handler http.HandlerFunc) HTTPReducer {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return HTTPReducer{Client: server.Client(), Endpoint: server.URL + ReducePath, Token: "sealed", Timeout: 2 * time.Second}
}

// reduceWith calls Reduce the way the engine does, with the instance and
// sequence beside the call rather than in it.
func reduceWith(ctx context.Context, r HTTPReducer, state string, event Event) (Result, error) {
	ctx = WithReduceContext(ctx, ReduceContext{Instance: "123", Sequence: 11})
	return r.Reduce(ctx, json.RawMessage(state), event)
}

func TestReduceRoundTripsTheWholeResult(t *testing.T) {
	var body []byte
	var headers http.Header
	var method, path string
	reducer := serve(t, func(w http.ResponseWriter, req *http.Request) {
		method, path, headers = req.Method, req.URL.Path, req.Header.Clone()
		body, _ = readAll(req)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, okBody)
	})

	result, err := reduceWith(context.Background(), reducer, `{"phase":"waiting_for_ci"}`, testEvent)
	if err != nil {
		t.Fatalf("Reduce: %v", err)
	}
	if method != http.MethodPost || path != ReducePath {
		t.Errorf("called %s %s", method, path)
	}

	// The envelope, which is the authoritative copy.
	var request reduceRequest
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if request.ID != testEvent.ID || request.Instance != "123" || request.Sequence != 11 {
		t.Errorf("envelope = %+v", request)
	}
	if string(request.State) != `{"phase":"waiting_for_ci"}` || request.Event.Type != "task.observed" {
		t.Errorf("envelope state/event = %s %+v", request.State, request.Event)
	}

	// The headers, each of which duplicates something in the body so a
	// handler can route and log without parsing it.
	for name, want := range map[string]string{
		"Authorization":     "Bearer sealed",
		"Content-Type":      "application/json",
		"Grain-Instance":    "123",
		"Grain-Sequence":    "11",
		"Grain-Event-Id":    "observation-12",
		"Grain-Deadline-Ms": "2000",
		"Idempotency-Key":   ReduceContext{Instance: "123", Sequence: 11}.IdempotencyKey("observation-12"),
	} {
		if got := headers.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	if string(result.State) != `{"phase":"ready_to_submit"}` {
		t.Errorf("state = %s", result.State)
	}
	if len(result.Effects) != 1 || result.Effects[0].Type != "task.submit" {
		t.Errorf("effects = %+v", result.Effects)
	}
	if result.Lifecycle != "active" || result.Attention {
		t.Errorf("lifecycle = %q, attention = %v", result.Lifecycle, result.Attention)
	}
	if result.Presentation.Tone != "success" || result.Presentation.Activity != "idle" ||
		result.Presentation.Label != "Ready to submit" {
		t.Errorf("presentation = %+v", result.Presentation)
	}
	if len(result.Inputs) != 1 || result.Inputs[0].Event != "submission.cancelled" {
		t.Errorf("inputs = %+v", result.Inputs)
	}
	// And the whole of it survives the engine's own validation, which is
	// the same validateResult a subprocess answer went through.
	if err := validateResult(result); err != nil {
		t.Errorf("validateResult: %v", err)
	}
}

// 200 with a body validateResult refuses is a reducer failure, and the
// engine turns exactly one of those into a reducer.failed against the
// last committed state.
func TestReduceResultIsValidatedAgainstAccepts(t *testing.T) {
	reducer := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":"observation-12","state":{},"effects":[],"accepts":["task.observed"],`+
			`"inputs":[{"id":"cancel","kind":"confirm","label":"Cancel","event":"submission.cancelled"}]}`)
	})
	result, err := reduceWith(context.Background(), reducer, `{}`, testEvent)
	if err != nil {
		t.Fatalf("Reduce: %v", err)
	}
	err = validateResult(result)
	if err == nil {
		t.Fatal("an input offering an event the machine would refuse was accepted")
	}
	if !strings.Contains(err.Error(), "not in accepts") {
		t.Errorf("%v", err)
	}
	if errors.Is(err, ErrReducerUnavailable) {
		t.Error("a wrong answer is the definition's problem, not an unreachable machine")
	}
}

// The id echo: the cheapest possible check that a long-lived, concurrent
// server has not confused two requests.
func TestReduceRefusesAMismatchedIDEcho(t *testing.T) {
	reducer := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":"someone-elses","state":{},"effects":[],"accepts":[]}`)
	})
	_, err := reduceWith(context.Background(), reducer, `{}`, testEvent)
	if err == nil || !strings.Contains(err.Error(), "confused two requests") {
		t.Fatalf("error = %v", err)
	}
	if errors.Is(err, ErrReducerUnavailable) {
		t.Error("a confused answer is a reducer failure, not an unreachable machine")
	}
}

func TestReduceRefusesAnUnknownFieldInTheResponse(t *testing.T) {
	reducer := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":"observation-12","state":{},"effects":[],"accepts":[],"nonce":1}`)
	})
	_, err := reduceWith(context.Background(), reducer, `{}`, testEvent)
	if err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("error = %v", err)
	}
}

// 400 and 422 are the definition refusing this event, with the body's
// error string as the diagnostic. A reducer failure, so the definition
// gets to react to its own bug.
func TestReduceTreatsARefusalAsAReducerFailure(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity} {
		reducer := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":"approved is not a boolean"}`)
		})
		_, err := reduceWith(context.Background(), reducer, `{}`, testEvent)
		if err == nil {
			t.Fatalf("%d: no error", status)
		}
		if !strings.Contains(err.Error(), "approved is not a boolean") {
			t.Errorf("%d: %v does not carry the definition's own diagnostic", status, err)
		}
		if errors.Is(err, ErrReducerUnavailable) {
			t.Errorf("%d: a refusal is the definition's problem", status)
		}
	}
}

func TestReduceTreatsAServerErrorAsAReducerFailure(t *testing.T) {
	reducer := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"KeyError: phase"}`)
	})
	_, err := reduceWith(context.Background(), reducer, `{}`, testEvent)
	if err == nil || !strings.Contains(err.Error(), "KeyError") {
		t.Fatalf("error = %v", err)
	}
	if errors.Is(err, ErrReducerUnavailable) {
		t.Error("a definition that threw is the definition's problem")
	}
}

// 401, 404 and 405 are not a definition failing this event -- they are a
// definition not implementing the protocol. Held as a lifecycle error on
// the repository, not charged to the event.
func TestReduceTreatsANonContractAnswerAsUnavailable(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusMethodNotAllowed} {
		reducer := serve(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
		_, err := reduceWith(context.Background(), reducer, `{}`, testEvent)
		if !errors.Is(err, ErrReducerUnavailable) {
			t.Errorf("%d: error = %v, want an unavailable reducer", status, err)
		}
		if err != nil && !strings.Contains(err.Error(), "not serving the reduce contract") {
			t.Errorf("%d: %v does not say what is wrong", status, err)
		}
	}
}

// 429 is the definition asking for a moment, not failing. The same
// request goes again, which the engine's deduplication makes safe.
func TestReduceBacksOffAndResubmitsOn429(t *testing.T) {
	var attempts atomic.Int32
	var keys []string
	reducer := serve(t, func(w http.ResponseWriter, req *http.Request) {
		keys = append(keys, req.Header.Get("Idempotency-Key"))
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, okBody)
	})
	result, err := reduceWith(context.Background(), reducer, `{}`, testEvent)
	if err != nil {
		t.Fatalf("Reduce: %v", err)
	}
	if string(result.State) != `{"phase":"ready_to_submit"}` {
		t.Errorf("state = %s", result.State)
	}
	if attempts.Load() != 2 {
		t.Errorf("attempts = %d", attempts.Load())
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Errorf("the resubmission was not the same request: %q", keys)
	}
}

// A definition that keeps saying "not now" does not get to outlive the
// deadline the engine gave this reduce.
func TestReduceDeadlinesAPermanent429(t *testing.T) {
	reducer := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	reducer.Timeout = 150 * time.Millisecond
	start := time.Now()
	_, err := reduceWith(context.Background(), reducer, `{}`, testEvent)
	if err == nil {
		t.Fatal("a definition that never stopped refusing was waited on forever")
	}
	// Either phrasing, because which one wins is a race and both are
	// true: the deadline can expire during the backoff wait, or during
	// the request that wait was waiting to make. What must not vary is
	// that it deadlined at all, and that it is the definition's problem
	// rather than an unreachable machine -- a server answering 429 is a
	// server that is answering.
	if !strings.Contains(err.Error(), "backing off") && !strings.Contains(err.Error(), "exceeded 150ms") {
		t.Errorf("error = %v, which says neither that it deadlined backing off nor that it ran out of time", err)
	}
	if errors.Is(err, ErrReducerUnavailable) {
		t.Error("a definition that kept refusing was reported as unreachable")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("it waited %s past a %s deadline", elapsed, reducer.Timeout)
	}
}

// The timeout is the definition taking too long, which is its own bug:
// a reducer failure, with the connection dropped under a handler that is
// still running.
func TestReduceTimesOutAsAReducerFailure(t *testing.T) {
	released := make(chan struct{})
	reducer := serve(t, func(w http.ResponseWriter, req *http.Request) {
		select {
		case <-req.Context().Done():
		case <-released:
		}
	})
	// Released before the server is closed, not after: httptest.Server
	// waits for its outstanding handlers, and this one is deliberately
	// still running when the request is dropped under it.
	defer close(released)
	reducer.Timeout = 100 * time.Millisecond
	_, err := reduceWith(context.Background(), reducer, `{}`, testEvent)
	if err == nil || !strings.Contains(err.Error(), "exceeded 100ms") {
		t.Fatalf("error = %v", err)
	}
	if errors.Is(err, ErrReducerUnavailable) {
		t.Error("a slow definition is a reducer failure, not an unreachable machine")
	}
}

// The distinction the process adapter could not draw. The machine went
// away, so the event is not consumed and the snapshot does not move.
func TestReduceTreatsATransportFailureAsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	reducer := HTTPReducer{Client: server.Client(), Endpoint: server.URL + ReducePath, Timeout: time.Second}
	server.Close()
	_, err := reduceWith(context.Background(), reducer, `{}`, testEvent)
	if !errors.Is(err, ErrReducerUnavailable) {
		t.Fatalf("error = %v, want an unavailable reducer", err)
	}
}

// 8 MiB of response, and larger is a reducer failure rather than a
// truncation: half a result is not a result.
func TestReduceBoundsTheResponse(t *testing.T) {
	reducer := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":"observation-12","state":{"pad":"`+strings.Repeat("x", 4096)+`"},"effects":[],"accepts":[]}`)
	})
	reducer.MaxBytes = 1024
	_, err := reduceWith(context.Background(), reducer, `{}`, testEvent)
	if err == nil || !strings.Contains(err.Error(), "exceeds 1024 bytes") {
		t.Fatalf("error = %v", err)
	}
}

func TestHealthIsReadyOnlyOnTheProtocolItSpeaks(t *testing.T) {
	var path string
	var auth string
	reducer := serve(t, func(w http.ResponseWriter, req *http.Request) {
		path, auth = req.URL.Path, req.Header.Get("Authorization")
		fmt.Fprint(w, `{"protocol":"1","ready":true,"definition":"acme task lifecycle v4"}`)
	})
	report, err := reducer.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if path != HealthPath {
		t.Errorf("polled %q", path)
	}
	if auth != "Bearer sealed" {
		t.Errorf("health went unauthenticated: %q", auth)
	}
	if report.Definition != "acme task lifecycle v4" || !report.Ready {
		t.Errorf("report = %+v", report)
	}
}

func TestHealthRefusesAnotherProtocolVersion(t *testing.T) {
	reducer := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"protocol":"2","ready":true}`)
	})
	_, err := reducer.Health(context.Background())
	if err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("error = %v", err)
	}
	// A skewed protocol is a refusal, not a machine that went away: the
	// server is there and answering, and what it answers is unusable.
	if errors.Is(err, ErrReducerUnavailable) {
		t.Error("a protocol mismatch is a refusal naming both versions")
	}
}

// Not yet, rather than a failure: a connection refused while the process
// is still starting is what readiness polling is for.
func TestHealthReportsNotReadyAsUnavailable(t *testing.T) {
	reducer := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"protocol":"1","ready":false,"definition":"starting"}`)
	})
	_, err := reducer.Health(context.Background())
	if !errors.Is(err, ErrReducerUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func readAll(req *http.Request) ([]byte, error) {
	defer req.Body.Close()
	buf := new(strings.Builder)
	if _, err := fmt.Fprint(buf, ""); err != nil {
		return nil, err
	}
	body := make([]byte, 0, 1024)
	chunk := make([]byte, 1024)
	for {
		n, err := req.Body.Read(chunk)
		body = append(body, chunk[:n]...)
		if err != nil {
			return body, nil
		}
	}
}
