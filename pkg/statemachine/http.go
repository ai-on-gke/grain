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

// The reducer as an HTTP server in a sandbox, rather than a subprocess of
// the controller (docs/task-lifecycle-sandbox.md).
//
// This is one more implementation of Reducer and nothing else: the
// engine, the store, the transition log, the effect outbox, the
// idempotency keys and the reducer.failed retry are all untouched. What
// changes is where the definition runs -- with NetworkAccess none, no
// checkout, no capability material and no inbound route but one unix
// socket grain exec's its way to, instead of in the controller's own
// container next to the deployment's credentials.
//
// HTTP over that socket, rather than a bespoke framing on the same pipe,
// is what buys the things a line-of-NDJSON protocol had to hand-roll:
// request/response correlation, content length, status codes, timeouts,
// and a definition author writing against a web framework they already
// know instead of against "write exactly one line of JSON to stdout and
// print your diagnostics to stderr".
//
// The status codes are the interesting part, and the table in Errors
// below is the contract. The distinction it draws that a subprocess could
// not is between a definition that answered wrongly and a machine that
// went away: the first is ErrReducerUnavailable's doc comment, and the
// second is the whole reason that error exists.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// ProtocolVersion is the contract version GET /v1/health must report.
	// A server answering anything else is a server speaking a protocol
	// this grain does not, which is a refusal rather than a negotiation:
	// there is one version, and when there are two this is where they
	// meet.
	ProtocolVersion = "1"

	// ReducePath and HealthPath are the whole of the definition's API.
	ReducePath = "/v1/reduce"
	HealthPath = "/v1/health"

	// defaultMaxResponseBytes bounds one answer. Larger is a reducer
	// failure rather than a truncation: half a result is not a result, and
	// a definition that produced eight megabytes of state has a bug that
	// silently keeping the first eight would hide.
	defaultMaxResponseBytes = 8 << 20

	// maxRetryAfter caps how long a 429 may park one reduce. A definition
	// asking for longer than this is asking grain to hold an instance's
	// lock for longer than the deadline it was given, which the deadline
	// is there to prevent.
	maxRetryAfter = 5 * time.Second
)

// HTTPReducer reduces over one serving sandbox's link.
//
// Client's transport is the interesting field: it dials by exec'ing a
// one-connection forwarder in the guest, because grain has no route to a
// sandbox's guest and never has had one -- see the Transport section of
// docs/task-lifecycle-sandbox.md, and DialStream on the sandbox backends.
// Nothing here knows that; as far as this type is concerned it is an
// ordinary http.Client whose host happens to be a label.
type HTTPReducer struct {
	// Client dials the sandbox. Required.
	Client *http.Client
	// Endpoint is the reduce URL -- "http://lifecycle/v1/reduce". The
	// host is a label: the transport ignores it and dials the sandbox it
	// was built for.
	Endpoint string
	// Token is the bearer token grain generated for this sandbox and
	// passed into the serve command's environment. The server compares it
	// in constant time and answers 401 to anything else. The socket is
	// already inside a sandbox with no network, so this is a thin
	// boundary -- its job is to keep something *else* that ends up in the
	// guest from driving the reducer, not to be the isolation.
	Token string
	// Timeout bounds one reduce. Zero is the declaration's default.
	Timeout time.Duration
	// MaxBytes bounds one response. Zero is defaultMaxResponseBytes.
	MaxBytes int64
}

// reduceRequest is the envelope. Every field of it is also a header, so a
// handler can route and log without parsing the body; none of the headers
// is authoritative, and this is.
type reduceRequest struct {
	ID       string          `json:"id"`
	Instance string          `json:"instance"`
	Sequence uint64          `json:"sequence"`
	State    json.RawMessage `json:"state"`
	Event    Event           `json:"event"`
}

// reduceResponse is Result plus the id echo.
//
// The echo is the cheapest possible check that a server has not confused
// two requests, and it is worth having precisely because this server is
// long-lived and concurrent where the subprocess it replaces was neither.
type reduceResponse struct {
	ID           string          `json:"id"`
	State        json.RawMessage `json:"state"`
	Effects      []Effect        `json:"effects"`
	Accepts      []string        `json:"accepts"`
	Lifecycle    string          `json:"lifecycle"`
	Attention    bool            `json:"attention"`
	Presentation Presentation    `json:"presentation"`
	Inputs       []Input         `json:"inputs"`
}

// HealthReport is GET /v1/health's body.
type HealthReport struct {
	// Protocol must be ProtocolVersion.
	Protocol string `json:"protocol"`
	// Ready is whether the definition can answer a reduce now.
	Ready bool `json:"ready"`
	// Definition is free prose for operators and logs. Grain never parses
	// it.
	Definition string `json:"definition"`
}

// errorBody is what a definition says when it refuses. Surfaced on the
// workflow's public error field.
type errorBody struct {
	Error string `json:"error"`
}

// Health asks whether the definition can answer yet.
//
// Polled after start and before the first reduce, and again on a schedule
// while the sandbox is held. A connection refused while the process is
// still starting is "not yet" rather than a failure, which is why this
// returns ready=false with an error the caller may keep retrying against
// until ready-timeout-seconds runs out.
func (r HTTPReducer) Health(ctx context.Context) (HealthReport, error) {
	endpoint, err := r.resolve(HealthPath)
	if err != nil {
		return HealthReport{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return HealthReport{}, fmt.Errorf("%w: building health request: %v", ErrReducerUnavailable, err)
	}
	r.authorize(request)
	response, err := r.client().Do(request)
	if err != nil {
		return HealthReport{}, fmt.Errorf("%w: %s: %v", ErrReducerUnavailable, HealthPath, err)
	}
	defer drain(response)
	body, err := r.read(response)
	if err != nil {
		return HealthReport{}, err
	}
	if response.StatusCode != http.StatusOK {
		return HealthReport{}, r.notServingContract(response, body)
	}
	var report HealthReport
	if err := json.Unmarshal(body, &report); err != nil {
		return HealthReport{}, fmt.Errorf("%w: decoding health: %v", ErrReducerUnavailable, err)
	}
	if report.Protocol != ProtocolVersion {
		return report, fmt.Errorf("definition speaks reduce protocol %q, and this grain speaks %q",
			report.Protocol, ProtocolVersion)
	}
	if !report.Ready {
		return report, fmt.Errorf("%w: definition %q is not ready yet", ErrReducerUnavailable, report.Definition)
	}
	return report, nil
}

// Reduce implements Reducer over the link.
//
// The whole of the error contract is here, and the classification matters
// more than any of the codes:
//
//	200, valid body          commit the transition, dispatch the effects
//	200, invalid body        reducer failure
//	400, 422                 reducer failure, with the body's error as the diagnostic
//	401, 404, 405            not serving the contract: ErrReducerUnavailable
//	429 with Retry-After     not a failure; back off and resubmit
//	500-599                  reducer failure
//	timeout at r.Timeout     reducer failure; the connection is dropped
//	transport failure        ErrReducerUnavailable
//
// A reducer failure is an ordinary error return, which the engine turns
// into one reducer.failed against the last committed state. An
// ErrReducerUnavailable is not charged to the event at all.
func (r HTTPReducer) Reduce(ctx context.Context, state json.RawMessage, event Event) (Result, error) {
	endpoint, err := r.resolve(ReducePath)
	if err != nil {
		return Result{}, err
	}
	rc, _ := ReduceContextFrom(ctx)
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	// One deadline for the whole call, retries included. A 429 that keeps
	// asking for more time does not get to outlive the budget the engine
	// gave this reduce.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(reduceRequest{
		ID: event.ID, Instance: rc.Instance, Sequence: rc.Sequence, State: state, Event: event,
	})
	if err != nil {
		return Result{}, fmt.Errorf("encoding reduce request: %w", err)
	}

	for {
		result, retryAfter, err := r.attempt(ctx, endpoint, timeout, rc, event, body)
		if retryAfter <= 0 {
			return result, err
		}
		// 429 is the definition asking for a moment, not failing. Nothing
		// was committed for a reduce still in progress, and the request
		// carries the same Idempotency-Key (instance, sequence, event id)
		// every time, so resubmitting the identical request is safe.
		timer := time.NewTimer(retryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Result{}, fmt.Errorf("reduce of %s deadlined at %s while the definition was backing off: %w",
				event.Type, timeout, ctx.Err())
		case <-timer.C:
		}
	}
}

// attempt is one request. A positive retryAfter means "ask again", and is
// the only case in which the Result and the error are both empty.
func (r HTTPReducer) attempt(ctx context.Context, endpoint string, timeout time.Duration,
	rc ReduceContext, event Event, body []byte) (Result, time.Duration, error) {

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return Result{}, 0, fmt.Errorf("%w: building reduce request: %v", ErrReducerUnavailable, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	r.authorize(request)
	// Headers that duplicate the envelope, so a handler can route and log
	// without parsing the body. None of them is authoritative.
	request.Header.Set("Grain-Instance", rc.Instance)
	request.Header.Set("Grain-Sequence", strconv.FormatUint(rc.Sequence, 10))
	request.Header.Set("Grain-Event-Id", event.ID)
	request.Header.Set("Grain-Deadline-Ms", strconv.FormatInt(timeout.Milliseconds(), 10))
	request.Header.Set("Idempotency-Key", rc.IdempotencyKey(event.ID))

	response, err := r.client().Do(request)
	if err != nil {
		// Two very different things arrive here, and they are told apart
		// by whose deadline expired. The exec died or the sandbox is gone
		// -- grain's problem, and the event is not consumed. Or this
		// reduce ran out of the time it was given, which is the
		// definition taking too long: a reducer failure, and the dropped
		// connection kills a handler that is still running.
		if ctx.Err() != nil {
			return Result{}, 0, fmt.Errorf("reduce of %s exceeded %s", event.Type, timeout)
		}
		return Result{}, 0, fmt.Errorf("%w: %s: %v", ErrReducerUnavailable, ReducePath, err)
	}
	defer drain(response)

	if response.StatusCode == http.StatusTooManyRequests {
		return Result{}, r.retryAfter(response), nil
	}
	payload, err := r.read(response)
	if err != nil {
		return Result{}, 0, err
	}
	switch {
	case response.StatusCode == http.StatusOK:
		result, err := decodeResult(payload, event.ID)
		return result, 0, err
	case response.StatusCode == http.StatusBadRequest,
		response.StatusCode == http.StatusUnprocessableEntity:
		return Result{}, 0, fmt.Errorf("definition refused %s with %d: %s",
			event.Type, response.StatusCode, diagnostic(payload))
	case response.StatusCode == http.StatusUnauthorized,
		response.StatusCode == http.StatusNotFound,
		response.StatusCode == http.StatusMethodNotAllowed:
		return Result{}, 0, r.notServingContract(response, payload)
	case response.StatusCode >= 500:
		return Result{}, 0, fmt.Errorf("definition failed %s with %d: %s",
			event.Type, response.StatusCode, diagnostic(payload))
	default:
		return Result{}, 0, fmt.Errorf("definition answered %s with %d, which is not in the reduce contract: %s",
			event.Type, response.StatusCode, diagnostic(payload))
	}
}

// notServingContract is the 401/404/405 case, and the reason it is not a
// reducer failure: a definition that cannot be authenticated to, or has
// no such route, or will not take a POST there, has not made a mistake
// about this event -- it is not implementing the protocol at all. That is
// held as a lifecycle error on the repository, where somebody can fix
// the server, rather than fed back to the definition as its own bug.
func (r HTTPReducer) notServingContract(response *http.Response, body []byte) error {
	return fmt.Errorf("%w: definition answered %s %s with %d, so it is not serving the reduce contract: %s",
		ErrReducerUnavailable, response.Request.Method, response.Request.URL.Path,
		response.StatusCode, diagnostic(body))
}

// retryAfter reads the header, clamped. A 429 without one still backs
// off, briefly: the definition said "not now", and hammering it is not a
// reading of that.
func (r HTTPReducer) retryAfter(response *http.Response) time.Duration {
	const minimum = 50 * time.Millisecond
	value := strings.TrimSpace(response.Header.Get("Retry-After"))
	if value == "" {
		return minimum
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		wait := time.Duration(seconds) * time.Second
		return clamp(wait, minimum, maxRetryAfter)
	}
	if at, err := http.ParseTime(value); err == nil {
		return clamp(time.Until(at), minimum, maxRetryAfter)
	}
	return minimum
}

func clamp(d, low, high time.Duration) time.Duration {
	switch {
	case d < low:
		return low
	case d > high:
		return high
	}
	return d
}

// read takes the body, bounded. Over the bound is a reducer failure
// rather than a truncation, and it is deliberately checked before the
// status code is: an eight-megabyte 500 is still eight megabytes.
func (r HTTPReducer) read(response *http.Response) ([]byte, error) {
	limit := r.MaxBytes
	if limit <= 0 {
		limit = defaultMaxResponseBytes
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("definition's response was cut off by the deadline")
		}
		return nil, fmt.Errorf("%w: reading the definition's response: %v", ErrReducerUnavailable, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("definition's response exceeds %d bytes", limit)
	}
	return body, nil
}

// decodeResult is the 200 path: strict decoding, then the id echo.
func decodeResult(body []byte, eventID string) (Result, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var response reduceResponse
	if err := decoder.Decode(&response); err != nil {
		return Result{}, fmt.Errorf("decoding the definition's response: %w", err)
	}
	if response.ID != eventID {
		return Result{}, fmt.Errorf("definition answered with id %q for request id %q -- it has confused two requests",
			response.ID, eventID)
	}
	return Result{
		State: response.State, Effects: response.Effects, Accepts: response.Accepts,
		Lifecycle: response.Lifecycle, Attention: response.Attention,
		Presentation: response.Presentation, Inputs: response.Inputs,
	}, nil
}

// diagnostic is the body's error string when there is one, and the body
// otherwise, bounded so that a definition answering HTML does not put a
// page in a log line.
func diagnostic(body []byte) string {
	var refusal errorBody
	if err := json.Unmarshal(body, &refusal); err == nil && strings.TrimSpace(refusal.Error) != "" {
		return truncate(strings.TrimSpace(refusal.Error), 2048)
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "(no body)"
	}
	return truncate(text, 512)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func (r HTTPReducer) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return http.DefaultClient
}

func (r HTTPReducer) authorize(request *http.Request) {
	if r.Token != "" {
		request.Header.Set("Authorization", "Bearer "+r.Token)
	}
}

// resolve turns Endpoint into one of the two paths. Endpoint is the
// reduce URL because that is the one every caller needs; health hangs off
// the same origin, and saying so here keeps the two from drifting into
// two settings that could disagree.
func (r HTTPReducer) resolve(path string) (string, error) {
	if strings.TrimSpace(r.Endpoint) == "" {
		return "", errors.New("reducer endpoint is empty")
	}
	parsed, err := url.Parse(r.Endpoint)
	if err != nil {
		return "", fmt.Errorf("reducer endpoint %q: %w", r.Endpoint, err)
	}
	parsed.Path, parsed.RawQuery, parsed.Fragment = path, "", ""
	return parsed.String(), nil
}

// drain closes the body, reading a little of it first so that a
// keep-alive connection to the sandbox stays reusable instead of one
// unread refusal costing an exec.
func drain(response *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	_ = response.Body.Close()
}
