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

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/apierror"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Rate-limit bounds for pending 24-hour host join requests.
const (
	maxPendingJoinPerCode     = 32
	maxPendingJoinPerIdentity = 8
)

// Bounds on what a joining host says about itself. POST /api/hostd/join is
// served before the enrolling identity is known, and these fields reach the
// deployment owner verbatim -- the pending card in Settings -> Hosts and
// `grain host pending`'s terminal output -- as what they accept the host on.
const (
	maxJoinHostnameLen = 253
	maxJoinFieldLen    = 64
	maxJoinVersionLen  = 128
	maxJoinCLIs        = 32
)

// validateJoinMetadata refuses a join request whose self-description could
// mislead the owner deciding on it: a control character (an ANSI escape that
// rewrites `grain host pending`'s lines, a newline that forges one), a
// formatting character (a bidi override that reorders what is shown), or a
// field longer than any real host's.
func validateJoinMetadata(req hostd.JoinSubmitRequest) error {
	check := func(field, v string, max int, spaces bool) error {
		v = strings.TrimSpace(v)
		if len(v) > max {
			return fmt.Errorf("%s is longer than %d bytes", field, max)
		}
		for _, r := range v {
			if r == utf8.RuneError || !unicode.IsPrint(r) || (!spaces && unicode.IsSpace(r)) {
				return fmt.Errorf("%s holds a control, formatting or whitespace character", field)
			}
		}
		return nil
	}
	if err := check("hostname", req.Hostname, maxJoinHostnameLen, false); err != nil {
		return err
	}
	if err := check("os", req.OS, maxJoinFieldLen, false); err != nil {
		return err
	}
	if err := check("arch", req.Arch, maxJoinFieldLen, false); err != nil {
		return err
	}
	if err := check("version", req.Version, maxJoinVersionLen, true); err != nil {
		return err
	}
	if err := check("local_user", req.LocalUser, maxJoinFieldLen, false); err != nil {
		return err
	}
	if len(req.CLIs) > maxJoinCLIs {
		return fmt.Errorf("clis names more than %d agent CLIs", maxJoinCLIs)
	}
	for _, c := range req.CLIs {
		if err := check("clis", c, maxJoinFieldLen, false); err != nil {
			return err
		}
		if strings.Contains(c, ",") {
			return errors.New("clis holds a ','")
		}
	}
	return nil
}

// hostdEnrollOpenRoute reports whether r is one of the self-authenticating
// Ed25519-signed host enrollment or silent-renewal endpoints.
func hostdEnrollOpenRoute(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/api/hostd/join", "/api/hostd/renew":
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/api/hostd/join/") && strings.HasSuffix(r.URL.Path, "/poll") {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/hostd/join/"), "/poll")
		return id != "" && !strings.Contains(id, "/")
	}
	return false
}

// HostEnrollmentCodeView is one shared enrollment code as shown to the
// deployment owner in Settings -> Hosts -> Invite hosts.
type HostEnrollmentCodeView struct {
	model.HostEnrollmentCode
	Code    string        `json:"code,omitempty"`
	Install *HostdInstall `json:"install,omitempty"`
}

// CreateHostEnrollmentCodeRequest is POST /api/hostds/enrollment-codes.
type CreateHostEnrollmentCodeRequest struct {
	Name              string            `json:"name"`
	ExpiresAt         *time.Time        `json:"expires_at,omitempty"`
	ExpiresIn         string            `json:"expires_in,omitempty"`
	MaxUses           int               `json:"max_uses,omitempty"`
	DefaultLabels     map[string]string `json:"default_labels,omitempty"`
	DefaultCapacity   int               `json:"default_capacity,omitempty"`
	AllowedPrincipals []string          `json:"allowed_principals,omitempty"`
}

// PendingHostJoinView is one pending join request waiting for the deployment
// owner's pairing-code check in Settings -> Hosts.
type PendingHostJoinView struct {
	ID              string            `json:"id"`
	EnrollmentCode  string            `json:"enrollment_code,omitempty"`
	RekeyHostd      string            `json:"rekey_hostd,omitempty"`
	Hostname        string            `json:"hostname"`
	SuggestedName   string            `json:"suggested_name"`
	OS              string            `json:"os"`
	Arch            string            `json:"arch"`
	CLIs            []string          `json:"clis,omitempty"`
	Version         string            `json:"version,omitempty"`
	LocalUser       string            `json:"local_user,omitempty"`
	KeyFingerprint  string            `json:"key_fingerprint"`
	EnrolledBy      string            `json:"enrolled_by"`
	DefaultLabels   map[string]string `json:"default_labels,omitempty"`
	DefaultCapacity int               `json:"default_capacity,omitempty"`
	PairingCode     string            `json:"pairing_code"`
	CreatedAt       time.Time         `json:"created_at"`
	ExpiresAt       time.Time         `json:"expires_at"`
}

// AcceptHostJoinRequest is POST /api/hostds/pending/{id}/accept.
type AcceptHostJoinRequest struct {
	Name     string             `json:"name,omitempty"`
	Labels   *map[string]string `json:"labels,omitempty"`
	Capacity *int               `json:"capacity,omitempty"`
}

func hostEnrollError(err error) error {
	switch {
	case errors.Is(err, model.ErrEnrollmentCodeNotFound), errors.Is(err, model.ErrJoinRequestNotFound):
		return &NotFoundError{message: err.Error()}
	case errors.Is(err, model.ErrRekeyKeyMismatch):
		return &ForbiddenError{err: err}
	case errors.Is(err, model.ErrEnrollmentCodeExists),
		errors.Is(err, model.ErrEnrollmentCodeRevoked),
		errors.Is(err, model.ErrEnrollmentCodeExpired),
		errors.Is(err, model.ErrEnrollmentCodeExhausted),
		errors.Is(err, model.ErrJoinRequestAlreadyAccepted),
		errors.Is(err, model.ErrHostdExists),
		errors.Is(err, model.ErrHostdRevoked):
		return validationErrorf("%v", err)
	}
	return err
}

func parseEnrollmentExpiry(now time.Time, at *time.Time, in string) (*time.Time, error) {
	if at != nil && strings.TrimSpace(in) != "" {
		return nil, validationErrorf("specify at most one of expires_at or expires_in")
	}
	if at != nil {
		utc := at.UTC()
		if !utc.After(now) {
			return nil, validationErrorf("expires_at must be in the future")
		}
		return &utc, nil
	}
	in = strings.TrimSpace(in)
	if in == "" {
		return nil, nil
	}
	var d time.Duration
	if daysStr, ok := strings.CutSuffix(strings.ToLower(in), "d"); ok {
		var days int
		if _, err := fmt.Sscanf(daysStr, "%d", &days); err != nil || days <= 0 {
			return nil, validationErrorf("invalid expires_in %q: expected a positive duration such as 24h or 7d", in)
		}
		d = time.Duration(days) * 24 * time.Hour
	} else {
		var err error
		d, err = time.ParseDuration(in)
		if err != nil || d <= 0 {
			return nil, validationErrorf("invalid expires_in %q: expected a positive duration such as 24h or 7d", in)
		}
	}
	exp := now.Add(d).UTC()
	return &exp, nil
}

// ListHostEnrollmentCodes returns all shared enrollment codes (ensuring a
// default code on first owner visit when none has ever been created). Only the
// deployment owner may view enrollment codes.
func (c *Client) ListHostEnrollmentCodes(ctx context.Context) ([]model.HostEnrollmentCode, error) {
	if err := c.requireDeploymentOwner(ctx, "view host enrollment codes"); err != nil {
		return nil, err
	}
	if _, err := c.Store.EnsureDefaultHostEnrollmentCode(ctx, RequestActor(ctx).Actor.ID); err != nil {
		return nil, err
	}
	return c.Store.ListHostEnrollmentCodes(ctx)
}

// CreateHostEnrollmentCode creates a named shared enrollment code. Only the
// deployment owner may create enrollment codes.
func (c *Client) CreateHostEnrollmentCode(ctx context.Context, req CreateHostEnrollmentCodeRequest) (model.HostEnrollmentCode, error) {
	if err := c.requireDeploymentOwner(ctx, "create host enrollment codes"); err != nil {
		return model.HostEnrollmentCode{}, err
	}
	name := strings.TrimSpace(req.Name)
	if err := model.ValidateHostdName(name); err != nil {
		return model.HostEnrollmentCode{}, validationErrorf("%v", err)
	}
	exp, err := parseEnrollmentExpiry(c.now(), req.ExpiresAt, req.ExpiresIn)
	if err != nil {
		return model.HostEnrollmentCode{}, err
	}
	if err := model.ValidateHostdLabels(req.DefaultLabels); err != nil {
		return model.HostEnrollmentCode{}, validationErrorf("%v", err)
	}
	principals, err := model.NormalizeHostEnrollmentPrincipals(req.AllowedPrincipals)
	if err != nil {
		return model.HostEnrollmentCode{}, validationErrorf("%v", err)
	}
	if req.MaxUses < 0 {
		return model.HostEnrollmentCode{}, validationErrorf("max_uses may not be negative")
	}
	if req.DefaultCapacity < 0 {
		return model.HostEnrollmentCode{}, validationErrorf("default_capacity may not be negative")
	}
	created, _, err := c.Store.CreateHostEnrollmentCode(ctx, model.HostEnrollmentCode{
		Name:              name,
		CreatedBy:         RequestActor(ctx).Actor.ID,
		ExpiresAt:         exp,
		MaxUses:           req.MaxUses,
		DefaultLabels:     req.DefaultLabels,
		DefaultCapacity:   req.DefaultCapacity,
		AllowedPrincipals: principals,
	})
	if err != nil {
		return model.HostEnrollmentCode{}, hostEnrollError(err)
	}
	c.auditHostd(ctx, "code:"+created.Name, "created host enrollment code "+created.Name)
	return *created, nil
}

// RotateHostEnrollmentCode rotates name's shared enrollment code value.
func (c *Client) RotateHostEnrollmentCode(ctx context.Context, name string) (model.HostEnrollmentCode, error) {
	if err := c.requireDeploymentOwner(ctx, "rotate host enrollment codes"); err != nil {
		return model.HostEnrollmentCode{}, err
	}
	name = strings.TrimSpace(name)
	rotated, _, err := c.Store.RotateHostEnrollmentCode(ctx, name)
	if err != nil {
		return model.HostEnrollmentCode{}, hostEnrollError(err)
	}
	c.auditHostd(ctx, "code:"+rotated.Name, "rotated host enrollment code "+rotated.Name)
	return *rotated, nil
}

// RevokeHostEnrollmentCode revokes name so no further join requests can use it.
func (c *Client) RevokeHostEnrollmentCode(ctx context.Context, name string) (model.HostEnrollmentCode, error) {
	if err := c.requireDeploymentOwner(ctx, "revoke host enrollment codes"); err != nil {
		return model.HostEnrollmentCode{}, err
	}
	name = strings.TrimSpace(name)
	revoked, err := c.Store.RevokeHostEnrollmentCode(ctx, name)
	if err != nil {
		return model.HostEnrollmentCode{}, hostEnrollError(err)
	}
	c.auditHostd(ctx, "code:"+revoked.Name, "revoked host enrollment code "+revoked.Name)
	return *revoked, nil
}

// ListPendingHostJoins returns pending 24h join requests with their SAS pairing
// codes for the deployment owner to inspect.
func (c *Client) ListPendingHostJoins(ctx context.Context) ([]PendingHostJoinView, error) {
	if err := c.requireDeploymentOwner(ctx, "view pending host join requests"); err != nil {
		return nil, err
	}
	rows, err := c.Store.ListPendingHostJoinRequests(ctx, c.now())
	if err != nil {
		return nil, err
	}
	out := make([]PendingHostJoinView, 0, len(rows))
	for _, r := range rows {
		suggested := model.SanitizeHostdName(r.Hostname)
		if r.RekeyHostd != "" {
			suggested = r.RekeyHostd
		}
		out = append(out, PendingHostJoinView{
			ID:              r.ID,
			EnrollmentCode:  r.EnrollmentCode,
			RekeyHostd:      r.RekeyHostd,
			Hostname:        r.Hostname,
			SuggestedName:   suggested,
			OS:              r.OS,
			Arch:            r.Arch,
			CLIs:            r.CLIs,
			Version:         r.Version,
			LocalUser:       r.LocalUser,
			KeyFingerprint:  r.KeyFingerprint,
			EnrolledBy:      r.EnrolledBy,
			DefaultLabels:   r.DefaultLabels,
			DefaultCapacity: r.DefaultCapacity,
			PairingCode:     hostd.DerivePairingCode(r.ID, r.ServerNonce, r.PublicKey, r.Hostname),
			CreatedAt:       r.CreatedAt,
			ExpiresAt:       r.ExpiresAt,
		})
	}
	return out, nil
}

// AcceptPendingHostJoin approves a pending join request and binds the host's
// Ed25519 public key. Only the deployment owner may accept hosts.
func (c *Client) AcceptPendingHostJoin(ctx context.Context, id string, req AcceptHostJoinRequest) (model.Hostd, error) {
	if err := c.requireDeploymentOwner(ctx, "accept pending hosts"); err != nil {
		return model.Hostd{}, err
	}
	id = strings.TrimSpace(id)
	name := strings.TrimSpace(req.Name)
	if name != "" {
		if name == model.LocalHostd {
			return model.Hostd{}, validationErrorf("%q is the co-located host's reserved name", model.LocalHostd)
		}
		if err := model.ValidateHostdName(name); err != nil {
			return model.Hostd{}, validationErrorf("%v", err)
		}
		if s, ok := c.Config.HostdRemote.(hostdSharedNamer); ok && s.SharedTokenName() == name {
			return model.Hostd{}, validationErrorf("%q is the name -hostd-token-name gives the shared token's host; choose another", name)
		}
	}
	var labels map[string]string
	hasLabels := false
	if req.Labels != nil {
		hasLabels = true
		labels = *req.Labels
		if err := model.ValidateHostdLabels(labels); err != nil {
			return model.Hostd{}, validationErrorf("%v", err)
		}
	}
	if req.Capacity != nil && *req.Capacity < 0 {
		return model.Hostd{}, validationErrorf("host capacity %d may not be negative", *req.Capacity)
	}
	actor := RequestActor(ctx).Actor.ID
	h, jr, err := c.Store.AcceptHostJoinRequest(ctx, id, model.AcceptHostJoinOptions{
		Name:       name,
		Labels:     labels,
		HasLabels:  hasLabels,
		Capacity:   req.Capacity,
		AcceptedBy: actor,
	}, c.now())
	if err != nil {
		return model.Hostd{}, hostEnrollError(err)
	}
	summary := fmt.Sprintf("accepted host %s (enrolled by %s, key %s", h.Name, jr.EnrolledBy, h.KeyFingerprint)
	if jr.EnrollmentCode != "" {
		summary += ", code " + jr.EnrollmentCode
	}
	summary += ")"
	c.auditHostd(ctx, h.Name, summary)
	return *h, nil
}

// RejectPendingHostJoin rejects a pending join request and deletes it cleanly.
// Only the deployment owner may reject hosts.
func (c *Client) RejectPendingHostJoin(ctx context.Context, id string) error {
	if err := c.requireDeploymentOwner(ctx, "reject pending hosts"); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	jr, err := c.Store.RejectHostJoinRequest(ctx, id, c.now())
	if err != nil {
		return hostEnrollError(err)
	}
	c.auditHostd(ctx, "join:"+id, fmt.Sprintf("rejected host join request %s (%s, enrolled by %s, key %s)",
		id, jr.Hostname, jr.EnrolledBy, jr.KeyFingerprint))
	return nil
}

func (s *Server) toEnrollmentCodeView(r *http.Request, c model.HostEnrollmentCode) HostEnrollmentCodeView {
	v := HostEnrollmentCodeView{HostEnrollmentCode: c}
	if c.RevokedAt == nil && c.SecretCode != "" {
		v.Code = c.SecretCode
		v.Install = s.hostInviteInstallFor(r, c.SecretCode)
	}
	return v
}

func (s *Server) handleListHostEnrollmentCodes(w http.ResponseWriter, r *http.Request) {
	codes, err := s.tasks.ListHostEnrollmentCodes(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	out := make([]HostEnrollmentCodeView, 0, len(codes))
	for _, c := range codes {
		out = append(out, s.toEnrollmentCodeView(r, c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateHostEnrollmentCode(w http.ResponseWriter, r *http.Request) {
	var req CreateHostEnrollmentCodeRequest
	if !readJSON(w, r, &req) {
		return
	}
	created, err := s.tasks.CreateHostEnrollmentCode(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.toEnrollmentCodeView(r, created))
}

func (s *Server) handleRotateHostEnrollmentCode(w http.ResponseWriter, r *http.Request) {
	rotated, err := s.tasks.RotateHostEnrollmentCode(r.Context(), r.PathValue("name"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toEnrollmentCodeView(r, rotated))
}

func (s *Server) handleRevokeHostEnrollmentCode(w http.ResponseWriter, r *http.Request) {
	revoked, err := s.tasks.RevokeHostEnrollmentCode(r.Context(), r.PathValue("name"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, HostEnrollmentCodeView{HostEnrollmentCode: revoked})
}

func (s *Server) handleListPendingHostJoins(w http.ResponseWriter, r *http.Request) {
	out, err := s.tasks.ListPendingHostJoins(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAcceptPendingHostJoin(w http.ResponseWriter, r *http.Request) {
	var req AcceptHostJoinRequest
	if r.ContentLength != 0 {
		if !readJSON(w, r, &req) {
			return
		}
	}
	h, err := s.tasks.AcceptPendingHostJoin(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	log.Printf("grain hostd: accepted host %s (owner %q, key %s)", h.Name, h.Owner, h.KeyFingerprint)
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleRejectPendingHostJoin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.RejectPendingHostJoin(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	log.Printf("grain hostd: rejected pending host join request %s", id)
	writeJSON(w, http.StatusOK, map[string]string{"rejected": id})
}

// handleHostdJoin is POST /api/hostd/join: an enrolling host's Ed25519-signed
// join request carrying an enrollment code (or existing host credential for
// rekey) and verified enrolling identity.
func (s *Server) handleHostdJoin(w http.ResponseWriter, r *http.Request) {
	var req hostd.JoinSubmitRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := validateJoinMetadata(req); err != nil {
		writeError(w, http.StatusBadRequest, apierror.Public("invalid host join request: "+err.Error()))
		return
	}
	now := time.Now().UTC()
	if s.tasks != nil {
		now = s.tasks.now()
	}
	pub, err := hostd.VerifyJoinSubmit(req, now, &s.hostdJoinReplay)
	if err != nil {
		writeError(w, http.StatusBadRequest, apierror.Public("invalid host join signature: "+err.Error()))
		return
	}
	if s.tasks == nil || s.tasks.Store == nil {
		writeError(w, http.StatusServiceUnavailable, apierror.Public("this daemon has no store for host enrollment"))
		return
	}

	ctx := r.Context()
	var code *model.HostEnrollmentCode
	needGitHubOrgs := false
	if req.RekeyHostd != "" {
		curTok := hostd.RequestToken(r)
		if curTok == "" {
			writeError(w, http.StatusUnauthorized, apierror.Public("rekeying a host requires its current credential"))
			return
		}
		h, err := s.tasks.Store.HostdForTokenAt(ctx, curTok, now)
		if err != nil || h == nil || h.Name != strings.TrimSpace(req.RekeyHostd) || h.Revoked() {
			writeError(w, http.StatusUnauthorized, apierror.Public("invalid current host credential for rekey"))
			return
		}
		// A key-bound host's credential is useless without its private key
		// (/api/hostd/connect refuses it), so a copied one must not be able
		// to move the host to a new key either: only a request signed by the
		// bound key itself rekeys it.
		if h.KeyBound() && !model.SameHostPublicKey(h.PublicKey, req.PublicKey) {
			writeError(w, http.StatusForbidden, apierror.Public(model.ErrRekeyKeyMismatch.Error()))
			return
		}
	} else {
		var err error
		code, err = s.tasks.Store.LookupHostEnrollmentCode(ctx, req.EnrollmentCode, now)
		switch {
		case errors.Is(err, model.ErrEnrollmentCodeNotFound):
			writeError(w, http.StatusUnauthorized, apierror.Public("unknown or rotated host enrollment code"))
			return
		case errors.Is(err, model.ErrEnrollmentCodeRevoked):
			writeError(w, http.StatusForbidden, apierror.Public("host enrollment code has been revoked"))
			return
		case errors.Is(err, model.ErrEnrollmentCodeExpired):
			writeError(w, http.StatusForbidden, apierror.Public("host enrollment code has expired"))
			return
		case errors.Is(err, model.ErrEnrollmentCodeExhausted):
			writeError(w, http.StatusForbidden, apierror.Public("host enrollment code has reached its maximum number of uses"))
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if code == nil {
			writeError(w, http.StatusUnauthorized, apierror.Public("unknown or rotated host enrollment code"))
			return
		}
		for _, p := range code.AllowedPrincipals {
			if strings.HasPrefix(p, "org:") {
				needGitHubOrgs = true
				break
			}
		}
	}

	enrolledBy, ghOrgs, status, authChallenge, err := s.verifyHostJoinEnrollee(r, req.GitHubToken, needGitHubOrgs)
	if authChallenge != nil {
		writeJSON(w, http.StatusUnauthorized, authChallenge)
		return
	}
	if err != nil {
		if status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", strconv.Itoa(verifierRetryAfter))
		}
		writeError(w, status, apierror.Public(err.Error()))
		return
	}

	if code != nil && len(code.AllowedPrincipals) > 0 {
		if err := s.checkEnrollmentCodePrincipal(ctx, code, enrolledBy, ghOrgs); err != nil {
			writeError(w, http.StatusForbidden, apierror.Public(err.Error()))
			return
		}
	}

	byCode, byIdentity, err := s.tasks.Store.CountPendingHostJoinRequests(ctx, s.enrollmentCodeName(code), enrolledBy, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if code != nil && byCode >= maxPendingJoinPerCode {
		writeError(w, http.StatusTooManyRequests, apierror.Public("too many pending join requests for this enrollment code"))
		return
	}
	if enrolledBy != "" && byIdentity >= maxPendingJoinPerIdentity {
		writeError(w, http.StatusTooManyRequests, apierror.Public("too many pending join requests from "+enrolledBy))
		return
	}

	jr, err := s.tasks.Store.CreateHostJoinRequest(ctx, model.HostJoinRequest{
		RekeyHostd:     strings.TrimSpace(req.RekeyHostd),
		PublicKey:      strings.TrimSpace(req.PublicKey),
		KeyFingerprint: hostd.KeyFingerprint(pub),
		Hostname:       strings.TrimSpace(req.Hostname),
		OS:             strings.TrimSpace(req.OS),
		Arch:           strings.TrimSpace(req.Arch),
		CLIs:           req.CLIs,
		Version:        strings.TrimSpace(req.Version),
		LocalUser:      strings.TrimSpace(req.LocalUser),
		EnrolledBy:     enrolledBy,
	}, req.EnrollmentCode, now)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrEnrollmentCodeNotFound):
			writeError(w, http.StatusUnauthorized, apierror.Public("unknown or rotated host enrollment code"))
		case errors.Is(err, model.ErrEnrollmentCodeRevoked),
			errors.Is(err, model.ErrEnrollmentCodeExpired),
			errors.Is(err, model.ErrEnrollmentCodeExhausted):
			writeError(w, http.StatusForbidden, apierror.Public(err.Error()))
		default:
			writeClientError(w, hostEnrollError(err))
		}
		return
	}

	pairingCode := hostd.DerivePairingCode(jr.ID, jr.ServerNonce, jr.PublicKey, jr.Hostname)
	writeJSON(w, http.StatusCreated, hostd.JoinSubmitResponse{
		ID:             jr.ID,
		ServerNonce:    jr.ServerNonce,
		PairingCode:    pairingCode,
		KeyFingerprint: jr.KeyFingerprint,
		EnrolledBy:     jr.EnrolledBy,
		ExpiresAt:      jr.ExpiresAt,
	})
}

func (s *Server) enrollmentCodeName(c *model.HostEnrollmentCode) string {
	if c == nil {
		return ""
	}
	return c.Name
}

func (s *Server) verifyHostJoinEnrollee(r *http.Request, ghToken string, needOrgs bool) (string, []string, int, *hostd.JoinSubmitResponse, error) {
	ctx := r.Context()
	cv := cachedOrVerifyCaller(r)
	if cv.present {
		if cv.err != nil {
			if verifierUnavailable(cv.err) {
				return "", nil, http.StatusServiceUnavailable, nil, fmt.Errorf("identity verifier temporarily unavailable: %v", cv.err)
			}
			return "", nil, http.StatusUnauthorized, nil, fmt.Errorf("enrolling identity did not verify: %v", cv.err)
		}
		email := normalizeIdentityEmail(cv.id.Email)
		if cv.id.Kind == model.PrincipalService && s.tasks != nil && s.tasks.gcpKeyPoolSet(ctx)[email] {
			return "", nil, http.StatusForbidden, nil, errors.New("sandbox pool service accounts cannot enroll hosts")
		}
		return email, nil, 0, nil, nil
	}

	verifiersActive := len(activeVerifiers(ctx)) > 0
	if !verifiersActive && strings.TrimSpace(ghToken) != "" {
		login, orgs, err := s.verifyGitHubJoinToken(ctx, strings.TrimSpace(ghToken), needOrgs)
		if err != nil {
			return "", nil, http.StatusUnauthorized, nil, fmt.Errorf("GitHub identity verification failed: %v", err)
		}
		return "github:" + login, orgs, 0, nil, nil
	}

	cfg, _ := IAPConfigFromContext(ctx)
	if accessCodeAccepted(r, cfg) && presentsAccessCode(r, cfg) && s.tasks != nil {
		owner := s.tasks.operatorID()
		if owner == "" {
			owner = "owner"
		}
		return strings.ToLower(owner), nil, 0, nil, nil
	}

	if verifiersActive {
		return "", nil, http.StatusUnauthorized, nil, errors.New("sign in through this deployment's identity proxy to enroll a host")
	}

	return "", nil, http.StatusUnauthorized, &hostd.JoinSubmitResponse{
		AuthMode:       "github_device",
		GitHubClientID: gitHubJoinOAuthClientID(),
		GitHubWebURL:   s.gitHubDeviceWebURL(ctx),
		Error:          "sign in with GitHub to enroll this host",
	}, nil
}

func gitHubJoinOAuthClientID() string {
	if id := strings.TrimSpace(os.Getenv("GRAIN_GITHUB_CLIENT_ID")); id != "" {
		return id
	}
	return github.DefaultOAuthClientID
}

func (s *Server) gitHubDeviceWebURL(ctx context.Context) string {
	if s.tasks == nil {
		return "https://github.com"
	}
	host, insecure := s.tasks.gitHubHost(ctx)
	if host == "" || strings.EqualFold(host, "github.com") {
		return "https://github.com"
	}
	if insecure {
		return "http://" + host
	}
	return "https://" + host
}

func (s *Server) verifyGitHubJoinToken(ctx context.Context, token string, needOrgs bool) (string, []string, error) {
	host := "github.com"
	insecure := false
	if s.tasks != nil {
		host, insecure = s.tasks.gitHubHost(ctx)
	}
	scheme := "https"
	if insecure {
		scheme = "http"
	}
	var prefixes []string
	apiHost := host
	if host == "" || strings.EqualFold(host, "github.com") {
		apiHost = "api.github.com"
		prefixes = []string{""}
	} else {
		prefixes = []string{"/api/v3", ""}
	}

	client := &http.Client{Timeout: 15 * time.Second}
	fetchJSON := func(urlStr string, dst any) (int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "grain")
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return resp.StatusCode, err
		}
		if resp.StatusCode != http.StatusOK {
			return resp.StatusCode, fmt.Errorf("HTTP %d from GitHub /user", resp.StatusCode)
		}
		return resp.StatusCode, json.Unmarshal(body, dst)
	}

	var user struct {
		Login string `json:"login"`
	}
	usedPrefix := ""
	var lastErr error
	for _, pfx := range prefixes {
		code, err := fetchJSON(scheme+"://"+apiHost+pfx+"/user", &user)
		if err == nil && strings.TrimSpace(user.Login) != "" {
			usedPrefix = pfx
			lastErr = nil
			break
		}
		lastErr = err
		if code != http.StatusNotFound {
			break
		}
	}
	if lastErr != nil {
		return "", nil, lastErr
	}
	login := strings.ToLower(strings.TrimSpace(user.Login))
	if login == "" {
		return "", nil, errors.New("GitHub /user returned an empty login")
	}

	var orgs []string
	if needOrgs {
		var orgRows []struct {
			Login string `json:"login"`
		}
		if _, err := fetchJSON(scheme+"://"+apiHost+usedPrefix+"/user/orgs?per_page=100", &orgRows); err != nil {
			return "", nil, fmt.Errorf("reading GitHub organization memberships: %w", err)
		}
		for _, o := range orgRows {
			if slug := strings.ToLower(strings.TrimSpace(o.Login)); slug != "" {
				orgs = append(orgs, slug)
			}
		}
	}
	return login, orgs, nil
}

func (s *Server) checkEnrollmentCodePrincipal(ctx context.Context, code *model.HostEnrollmentCode, enrolledBy string, ghOrgs []string) error {
	enrolledBy = strings.ToLower(strings.TrimSpace(enrolledBy))
	var groups []string
	for _, entry := range code.AllowedPrincipals {
		prefix, val, ok := strings.Cut(entry, ":")
		if !ok {
			continue
		}
		val = strings.ToLower(strings.TrimSpace(val))
		switch prefix {
		case "user":
			if enrolledBy == val {
				return nil
			}
		case "github":
			if enrolledBy == "github:"+val {
				return nil
			}
		case "org":
			if strings.HasPrefix(enrolledBy, "github:") && slices.Contains(ghOrgs, val) {
				return nil
			}
		case "group":
			groups = append(groups, val)
		}
	}
	if len(groups) > 0 && strings.Contains(enrolledBy, "@") && !strings.HasPrefix(enrolledBy, "github:") {
		checker := s.groupChecker()
		for _, grp := range groups {
			ok, err := checker.IsMember(ctx, grp, enrolledBy)
			if err == nil && ok {
				return nil
			}
		}
	}
	return fmt.Errorf("principal %q is not permitted to use enrollment code %q", enrolledBy, code.Name)
}

// handleHostdJoinPoll is POST /api/hostd/join/{id}/poll: the waiting host's
// Ed25519-signed poll that claims its 30-day credential once accepted.
func (s *Server) handleHostdJoinPoll(w http.ResponseWriter, r *http.Request) {
	var req hostd.JoinPollRequest
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Nonce) < 16 || len(req.Nonce) > 128 || strings.TrimSpace(req.Timestamp) == "" || strings.TrimSpace(req.Signature) == "" {
		writeError(w, http.StatusBadRequest, apierror.Public("invalid host join poll signature"))
		return
	}
	if s.tasks == nil || s.tasks.Store == nil {
		writeError(w, http.StatusServiceUnavailable, apierror.Public("this daemon has no store for host enrollment"))
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	now := s.tasks.now()
	jr, err := s.tasks.Store.GetHostJoinRequest(r.Context(), id, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if jr == nil {
		writeError(w, http.StatusNotFound, apierror.Public("join request was rejected or expired"))
		return
	}
	if err := hostd.VerifyJoinPoll(id, jr.PublicKey, req, now, &s.hostdJoinReplay); err != nil {
		writeError(w, http.StatusUnauthorized, apierror.Public("invalid host join poll signature: "+err.Error()))
		return
	}
	_, host, tok, exp, accepted, err := s.tasks.Store.ClaimAcceptedHostJoinRequest(r.Context(), id, now)
	if errors.Is(err, model.ErrJoinRequestNotFound) {
		writeError(w, http.StatusNotFound, apierror.Public("join request was rejected or expired"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !accepted || host == nil {
		writeJSON(w, http.StatusOK, hostd.JoinPollResponse{Status: "pending"})
		return
	}
	writeJSON(w, http.StatusOK, hostd.JoinPollResponse{
		Status:    "accepted",
		Name:      host.Name,
		Token:     tok,
		ExpiresAt: exp,
	})
}

type hostdConnectedChecker interface {
	IsConnected(name string) bool
}

type hostdReplayCacher interface {
	ReplayCache() *hostd.NonceReplayCache
}

// handleHostdRenew is POST /api/hostd/renew: silent renewal of a connected
// host's 30-day key-bound credential.
func (s *Server) handleHostdRenew(w http.ResponseWriter, r *http.Request) {
	var proof hostd.RenewProof
	if !readJSON(w, r, &proof) {
		return
	}
	if len(proof.Nonce) < 16 || len(proof.Nonce) > 128 || strings.TrimSpace(proof.Timestamp) == "" || strings.TrimSpace(proof.Signature) == "" {
		writeError(w, http.StatusBadRequest, apierror.Public("invalid host credential renewal proof"))
		return
	}
	if s.tasks == nil || s.tasks.Store == nil {
		writeError(w, http.StatusServiceUnavailable, apierror.Public("this daemon has no store for host renewal"))
		return
	}
	tok := hostd.RequestToken(r)
	if tok == "" {
		tok = strings.TrimSpace(proof.CurrentToken)
	}
	if tok == "" {
		writeError(w, http.StatusUnauthorized, apierror.Public("missing current host credential"))
		return
	}
	now := s.tasks.now()
	h, err := s.tasks.Store.HostdForTokenAt(r.Context(), tok, now)
	if err != nil || h == nil || h.Revoked() {
		writeError(w, http.StatusUnauthorized, apierror.Public("invalid or expired host credential"))
		return
	}
	if conn, ok := s.tasks.Config.HostdRemote.(hostdConnectedChecker); ok && !conn.IsConnected(h.Name) {
		writeError(w, http.StatusForbidden, apierror.Public("host must have an active connection to renew its credential"))
		return
	}
	// Only key-bound credentials renew, and only with a fresh Ed25519 proof:
	// a legacy per-host token is a bearer secret with no key behind it.
	if !h.KeyBound() {
		writeError(w, http.StatusForbidden, apierror.Public(model.ErrHostdNotKeyBound.Error()))
		return
	}
	// Check the nonce against the replay cache the in-band OpRenew and
	// /api/hostd/connect use, so one proof renews once whichever way it
	// arrives, and so anonymous join traffic filling hostdJoinReplay cannot
	// refuse a connected host's renewal.
	replay := &s.hostdJoinReplay
	if rc, ok := s.tasks.Config.HostdRemote.(hostdReplayCacher); ok {
		if c := rc.ReplayCache(); c != nil {
			replay = c
		}
	}
	if err := hostd.VerifyRenewProof(h.Name, tok, h.PublicKey, proof, now, replay); err != nil {
		writeError(w, http.StatusUnauthorized, apierror.Public("invalid renewal signature: "+err.Error()))
		return
	}
	newTok, exp, err := s.tasks.Store.RenewHostdCredential(r.Context(), h.Name, tok, now)
	if err != nil {
		writeError(w, http.StatusUnauthorized, apierror.Public(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, hostd.RenewReply{
		Token:     newTok,
		ExpiresAt: exp,
	})
}

// ListHostEnrollmentCodes is GET /api/hostds/enrollment-codes.
func (c *HTTPClient) ListHostEnrollmentCodes(ctx context.Context) ([]HostEnrollmentCodeView, error) {
	var out []HostEnrollmentCodeView
	err := c.do(ctx, http.MethodGet, "/api/hostds/enrollment-codes", nil, &out)
	return out, err
}

// CreateHostEnrollmentCode is POST /api/hostds/enrollment-codes.
func (c *HTTPClient) CreateHostEnrollmentCode(ctx context.Context, req CreateHostEnrollmentCodeRequest) (HostEnrollmentCodeView, error) {
	var out HostEnrollmentCodeView
	err := c.do(ctx, http.MethodPost, "/api/hostds/enrollment-codes", req, &out)
	return out, err
}

// RotateHostEnrollmentCode is POST /api/hostds/enrollment-codes/{name}/rotate.
func (c *HTTPClient) RotateHostEnrollmentCode(ctx context.Context, name string) (HostEnrollmentCodeView, error) {
	var out HostEnrollmentCodeView
	err := c.do(ctx, http.MethodPost, "/api/hostds/enrollment-codes/"+url.PathEscape(name)+"/rotate", nil, &out)
	return out, err
}

// RevokeHostEnrollmentCode is POST /api/hostds/enrollment-codes/{name}/revoke.
func (c *HTTPClient) RevokeHostEnrollmentCode(ctx context.Context, name string) (HostEnrollmentCodeView, error) {
	var out HostEnrollmentCodeView
	err := c.do(ctx, http.MethodPost, "/api/hostds/enrollment-codes/"+url.PathEscape(name)+"/revoke", nil, &out)
	return out, err
}

// ListPendingHostJoins is GET /api/hostds/pending.
func (c *HTTPClient) ListPendingHostJoins(ctx context.Context) ([]PendingHostJoinView, error) {
	var out []PendingHostJoinView
	err := c.do(ctx, http.MethodGet, "/api/hostds/pending", nil, &out)
	return out, err
}

// AcceptPendingHostJoin is POST /api/hostds/pending/{id}/accept.
func (c *HTTPClient) AcceptPendingHostJoin(ctx context.Context, id string, req AcceptHostJoinRequest) (model.Hostd, error) {
	var out model.Hostd
	err := c.do(ctx, http.MethodPost, "/api/hostds/pending/"+url.PathEscape(id)+"/accept", req, &out)
	return out, err
}

// RejectPendingHostJoin is POST /api/hostds/pending/{id}/reject.
func (c *HTTPClient) RejectPendingHostJoin(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/hostds/pending/"+url.PathEscape(id)+"/reject", nil, nil)
}
