package gritapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/attest"
	"github.com/bwsalmon/grain/pkg/pkgcache"
)

// Backend is the execution boundary satisfied by both in-process `grit.Local`
// and remote `gritapi.Client` talking to `grit serve`.
type Backend interface {
	Info(ctx context.Context) (Info, error)
	NegotiateTrees(ctx context.Context, sha256s []string) ([]string, error)
	UploadTree(ctx context.Context, sha256Hex string, ustarArchive []byte) error
	StartRun(ctx context.Context, req StartRun) (RunStatus, error)
	GetRun(ctx context.Context, id string) (RunStatus, error)
	CancelRun(ctx context.Context, id string) (RunStatus, error)
	RetriggerRun(ctx context.Context, id string, req RetriggerRequest) (RunStatus, error)
	GetAttestation(ctx context.Context, id string) (*attest.Envelope, error)
	GetLog(ctx context.Context, runID, jobID string, offset, limit int64) (data []byte, nextOffset int64, complete bool, err error)
	GetArtifact(ctx context.Context, runID, jobID, name string) ([]byte, error)
	Events(ctx context.Context, after int64, wait time.Duration, limit int) (EventBatch, error)
	PkgStats(ctx context.Context) (pkgcache.V1Stats, error)
	PkgFlush(ctx context.Context, kind string) (pkgcache.V1Flush, error)
}

// CursorStore persists the consumer's `(epoch, afterSeq)` event stream cursor.
type CursorStore interface {
	LoadCursor() (epoch string, afterSeq int64, err error)
	SaveCursor(epoch string, afterSeq int64) error
}

// ClientConfig configures a remote `/v1` HTTP client.
type ClientConfig struct {
	BaseURL     string
	ClientToken string // `<id>.<secret>` sent in `Grit-Client`
	BearerToken string // optional `Authorization: Bearer ...` for identity proxies
	HTTPClient  *http.Client
	Cursor      CursorStore
}

// Client is an HTTP client for grit's `/v1` and `/pkg/v1` endpoints.
type Client struct {
	baseURL     string
	clientToken string
	bearerToken string
	httpClient  *http.Client
	cursor      CursorStore
}

// NewClient constructs a Client for a remote grit server.
func NewClient(cfg ClientConfig) *Client {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		clientToken: cfg.ClientToken,
		bearerToken: cfg.BearerToken,
		httpClient:  hc,
		cursor:      cfg.Cursor,
	}
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.clientToken != "" {
		req.Header.Set(ClientHeader, c.clientToken)
	}
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, reqBody, respBody any) error {
	var bodyReader io.Reader
	var contentType string
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(b)
		contentType = "application/json"
	}
	req, err := c.newRequest(ctx, method, path, bodyReader, contentType)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DecodeAPIError(resp.StatusCode, raw)
	}
	if respBody != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, respBody); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// Info calls `GET /v1/info`.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var out Info
	err := c.doJSON(ctx, http.MethodGet, "/v1/info", nil, &out)
	return out, err
}

// NegotiateTrees calls `POST /v1/trees:negotiate` and returns the subset of
// sha256s that grit does not yet hold.
func (c *Client) NegotiateTrees(ctx context.Context, sha256s []string) ([]string, error) {
	var resp NegotiateTreesResponse
	if err := c.doJSON(ctx, http.MethodPost, "/v1/trees:negotiate", NegotiateTreesRequest{Trees: sha256s}, &resp); err != nil {
		return nil, err
	}
	return resp.Missing, nil
}

// UploadTree compresses ustarArchive with gzip and uploads it via
// `PUT /v1/trees/{sha256}`.
func (c *Client) UploadTree(ctx context.Context, sha256Hex string, ustarArchive []byte) error {
	var gzBuf bytes.Buffer
	zw := gzip.NewWriter(&gzBuf)
	if _, err := zw.Write(ustarArchive); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPut, "/v1/trees/"+url.PathEscape(sha256Hex), &gzBuf, "application/x-tar+gzip")
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DecodeAPIError(resp.StatusCode, raw)
	}
	return nil
}

// StartRun calls `POST /v1/runs`. If req.RequestID is empty, it is populated
// via ComputeRequestID.
func (c *Client) StartRun(ctx context.Context, req StartRun) (RunStatus, error) {
	if req.RequestID == "" {
		req.RequestID = ComputeRequestID(req)
	}
	var out RunStatus
	err := c.doJSON(ctx, http.MethodPost, "/v1/runs", req, &out)
	return out, err
}

// SubmitRunWithTree ensures the run's tree is present in grit (negotiating
// first or recovering from a `428 tree_missing` after eviction) and submits
// the run with a stable `requestId`.
func (c *Client) SubmitRunWithTree(ctx context.Context, req StartRun, archiveFn func() ([]byte, error)) (RunStatus, error) {
	if req.RequestID == "" {
		req.RequestID = ComputeRequestID(req)
	}
	if req.Tree.SHA256 != "" && archiveFn != nil {
		missing, err := c.NegotiateTrees(ctx, []string{req.Tree.SHA256})
		if err != nil {
			return RunStatus{}, err
		}
		if len(missing) > 0 {
			archive, err := archiveFn()
			if err != nil {
				return RunStatus{}, err
			}
			if err := c.UploadTree(ctx, req.Tree.SHA256, archive); err != nil {
				return RunStatus{}, err
			}
		}
	}
	status, err := c.StartRun(ctx, req)
	if missingSHA, ok := IsTreeMissing(err); ok && archiveFn != nil {
		archive, aErr := archiveFn()
		if aErr != nil {
			return RunStatus{}, aErr
		}
		if uErr := c.UploadTree(ctx, missingSHA, archive); uErr != nil {
			return RunStatus{}, uErr
		}
		return c.StartRun(ctx, req)
	}
	return status, err
}

// GetRun calls `GET /v1/runs/{id}`.
func (c *Client) GetRun(ctx context.Context, id string) (RunStatus, error) {
	var out RunStatus
	err := c.doJSON(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(id), nil, &out)
	return out, err
}

// CancelRun calls `POST /v1/runs/{id}/cancel`.
func (c *Client) CancelRun(ctx context.Context, id string) (RunStatus, error) {
	var out RunStatus
	err := c.doJSON(ctx, http.MethodPost, "/v1/runs/"+url.PathEscape(id)+"/cancel", nil, &out)
	return out, err
}

// RetriggerRun calls `POST /v1/runs/{id}/retrigger`.
func (c *Client) RetriggerRun(ctx context.Context, id string, req RetriggerRequest) (RunStatus, error) {
	var out RunStatus
	err := c.doJSON(ctx, http.MethodPost, "/v1/runs/"+url.PathEscape(id)+"/retrigger", req, &out)
	return out, err
}

// GetAttestation calls `GET /v1/runs/{id}/attestation`.
func (c *Client) GetAttestation(ctx context.Context, id string) (*attest.Envelope, error) {
	var env attest.Envelope
	if err := c.doJSON(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(id)+"/attestation", nil, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// GetLog calls `GET /v1/runs/{id}/logs/{job}?offset=N&limit=M`.
func (c *Client) GetLog(ctx context.Context, runID, jobID string, offset, limit int64) ([]byte, int64, bool, error) {
	q := url.Values{}
	if offset > 0 {
		q.Set("offset", strconv.FormatInt(offset, 10))
	}
	if limit > 0 {
		q.Set("limit", strconv.FormatInt(limit, 10))
	}
	path := "/v1/runs/" + url.PathEscape(runID) + "/logs/" + url.PathEscape(jobID)
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	req, err := c.newRequest(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, offset, false, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, offset, false, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, offset, false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, offset, false, DecodeAPIError(resp.StatusCode, raw)
	}
	nextOffset := offset + int64(len(raw))
	if hdr := resp.Header.Get("X-Grit-Log-Offset"); hdr != "" {
		if v, pErr := strconv.ParseInt(hdr, 10, 64); pErr == nil {
			nextOffset = v
		}
	}
	complete := resp.Header.Get("X-Grit-Log-Complete") == "true"
	return raw, nextOffset, complete, nil
}

// GetArtifact calls `GET /v1/runs/{id}/artifacts/{job}/{name}`.
func (c *Client) GetArtifact(ctx context.Context, runID, jobID, name string) ([]byte, error) {
	path := "/v1/runs/" + url.PathEscape(runID) + "/artifacts/" + url.PathEscape(jobID) + "/" + url.PathEscape(name)
	req, err := c.newRequest(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, DecodeAPIError(resp.StatusCode, raw)
	}
	return raw, nil
}

// Events calls `GET /v1/events?after=<seq>&wait=<dur>&limit=<n>`.
func (c *Client) Events(ctx context.Context, after int64, wait time.Duration, limit int) (EventBatch, error) {
	q := url.Values{}
	q.Set("after", strconv.FormatInt(after, 10))
	if wait > 0 {
		q.Set("wait", wait.String())
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out EventBatch
	err := c.doJSON(ctx, http.MethodGet, "/v1/events?"+q.Encode(), nil, &out)
	return out, err
}

// PkgStats calls `GET /pkg/v1/stats`.
func (c *Client) PkgStats(ctx context.Context) (pkgcache.V1Stats, error) {
	var out pkgcache.V1Stats
	err := c.doJSON(ctx, http.MethodGet, "/pkg/v1/stats", nil, &out)
	return out, err
}

// PkgFlush calls `POST /pkg/v1/flush?kind=<kind>`.
func (c *Client) PkgFlush(ctx context.Context, kind string) (pkgcache.V1Flush, error) {
	path := "/pkg/v1/flush"
	if kind != "" {
		path += "?kind=" + url.QueryEscape(kind)
	}
	var out pkgcache.V1Flush
	err := c.doJSON(ctx, http.MethodPost, path, nil, &out)
	return out, err
}

// SyncEvents reads the next batch of events using the configured CursorStore,
// automatically executing a `GET /v1/runs/{id}` resync over inFlightRunIDs
// if grit returns `410 cursor_gone` or if its `epoch` changed.
func (c *Client) SyncEvents(
	ctx context.Context,
	wait time.Duration,
	limit int,
	inFlightRunIDs []string,
	onEvent func(Event) error,
	onResyncRun func(RunStatus) error,
) error {
	var savedEpoch string
	var afterSeq int64
	if c.cursor != nil {
		var err error
		savedEpoch, afterSeq, err = c.cursor.LoadCursor()
		if err != nil {
			return err
		}
	}

	batch, err := c.Events(ctx, afterSeq, wait, limit)
	if goneErr, ok := IsCursorGone(err); ok {
		return c.resyncInFlight(ctx, goneErr.Epoch, inFlightRunIDs, onResyncRun)
	}
	if err != nil {
		return err
	}

	if savedEpoch != "" && batch.Epoch != "" && savedEpoch != batch.Epoch {
		return c.resyncInFlight(ctx, batch.Epoch, inFlightRunIDs, onResyncRun)
	}

	curEpoch := batch.Epoch
	curSeq := afterSeq
	for _, ev := range batch.Events {
		if onEvent != nil {
			if err := onEvent(ev); err != nil {
				return err
			}
		}
		if ev.Seq > curSeq {
			curSeq = ev.Seq
		}
		if c.cursor != nil {
			if err := c.cursor.SaveCursor(curEpoch, curSeq); err != nil {
				return err
			}
		}
	}
	if len(batch.Events) == 0 && savedEpoch == "" && curEpoch != "" && c.cursor != nil {
		if err := c.cursor.SaveCursor(curEpoch, curSeq); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) resyncInFlight(
	ctx context.Context,
	newEpoch string,
	inFlightRunIDs []string,
	onResyncRun func(RunStatus) error,
) error {
	// Fetch current head seq before reconciling runs so no transition after
	// reconciliation is missed.
	headBatch, err := c.Events(ctx, 0, 0, 1)
	if err != nil {
		return err
	}
	highWater := headBatch.NextSeq - 1
	if highWater < 0 {
		highWater = 0
	}
	if newEpoch == "" {
		newEpoch = headBatch.Epoch
	}
	for _, runID := range inFlightRunIDs {
		rs, rErr := c.GetRun(ctx, runID)
		if rErr != nil {
			var apiErr *APIError
			if asAPIError(rErr, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
				continue
			}
			return rErr
		}
		if onResyncRun != nil {
			if err := onResyncRun(rs); err != nil {
				return err
			}
		}
	}
	// Advance cursor to the latest sequence currently on the server.
	latest, err := c.Events(ctx, highWater, 0, 1000)
	if err == nil && latest.NextSeq > 1 {
		highWater = latest.NextSeq - 1
	}
	if c.cursor != nil {
		return c.cursor.SaveCursor(newEpoch, highWater)
	}
	return nil
}
