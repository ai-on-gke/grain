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

// The hostd registry's API (model.Hostd): Settings -> Hostds lists the
// hostds this daemon takes host work through, creates one and shows its
// token once, rotates and revokes tokens, and sets a hostd's owner and
// labels. A hostd dialing in is named by its token
// (hostd.Remote.ServeHTTP); one dialing in through IAP or another
// verifier's proxy must also be verified as its owner.

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
)

// HostdView is one registry row as Settings shows it, with whether the
// hostd is connected now.
type HostdView struct {
	model.Hostd
	// Connection is what the hostd last said when it dialed in; nil for
	// one that has not connected since the daemon started (and for
	// local, which does not dial in).
	Connection *hostd.RemoteStatus `json:"connection,omitempty"`
	// Shared marks the hostd the deprecated shared -hostd-token-file
	// names: it is not in the registry, so it is listed to be seen and
	// has no token here to rotate or revoke.
	Shared bool `json:"shared,omitempty"`
}

// CreateHostdRequest is POST /api/hostds.
type CreateHostdRequest struct {
	Name   string            `json:"name"`
	Owner  string            `json:"owner,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// UpdateHostdRequest is PATCH /api/hostds/{name}: a field left out is
// left as it is.
type UpdateHostdRequest struct {
	Owner  *string            `json:"owner,omitempty"`
	Labels *map[string]string `json:"labels,omitempty"`
}

// IssuedHostdToken is the answer to creating a hostd or rotating its
// token: the only time the token itself is ever shown.
type IssuedHostdToken struct {
	Hostd model.Hostd `json:"hostd"`
	Token string      `json:"token"`
	// Install is what the command that installs the hostd on its machine
	// passes /api/hostd/install.sh (HostdInstall).
	Install *HostdInstall `json:"install,omitempty"`
}

// hostdStatuses is what Config.HostdRemote can say about the hostds
// connected to it (hostd.Remote).
type hostdStatuses interface {
	Statuses() map[string]hostd.RemoteStatus
}

// hostdDisconnector drops a live dialing-in hostd session on revocation
// (hostd.Remote).
type hostdDisconnector interface {
	Disconnect(name string) bool
}

// hostdSharedNamer is the name the deprecated shared token's hostd holds,
// which the registry may not also take (hostd.Remote).
type hostdSharedNamer interface {
	SharedTokenName() string
}

// hostdSharedOwner is the owner -hostd-token-owner gives the shared
// token's hostd (hostd.Remote).
type hostdSharedOwner interface {
	SharedTokenOwner() string
}

func hostdError(err error) error {
	switch {
	case errors.Is(err, model.ErrHostdNotFound):
		return &NotFoundError{message: err.Error()}
	case errors.Is(err, model.ErrHostdExists), errors.Is(err, model.ErrHostdRevoked):
		return validationErrorf("%v", err)
	}
	return err
}

// ListHostds is the registry, with each hostd's connection.
func (c *Client) ListHostds(ctx context.Context) ([]HostdView, error) {
	rows, err := c.Store.ListHostds(ctx)
	if err != nil {
		return nil, err
	}
	var statuses map[string]hostd.RemoteStatus
	if st, ok := c.Config.HostdRemote.(hostdStatuses); ok {
		statuses = st.Statuses()
	}
	out := make([]HostdView, 0, len(rows)+1)
	for _, h := range rows {
		v := HostdView{Hostd: h}
		if st, ok := statuses[h.Name]; ok {
			v.Connection = &st
		}
		out = append(out, v)
	}
	// The deprecated shared token's hostd has no registry row, but it is
	// a hostd this daemon takes host work through -- its default one --
	// so it is listed beside them rather than only in the host executor's
	// status.
	if s, ok := c.Config.HostdRemote.(hostdSharedNamer); ok && s.SharedTokenName() != "" {
		v := HostdView{Hostd: model.Hostd{Name: s.SharedTokenName(), CreatedBy: "-hostd-token-file"}, Shared: true}
		if o, ok := c.Config.HostdRemote.(hostdSharedOwner); ok {
			v.Owner = o.SharedTokenOwner()
		}
		if st, ok := statuses[v.Name]; ok {
			v.Connection = &st
		}
		out = append(out, v)
	}
	return out, nil
}

// CreateHostd adds a remote hostd and mints its first token.
func (c *Client) CreateHostd(ctx context.Context, req CreateHostdRequest) (IssuedHostdToken, error) {
	name := strings.TrimSpace(req.Name)
	if err := model.ValidateHostdName(name); err != nil {
		return IssuedHostdToken{}, validationErrorf("%v", err)
	}
	if name == model.LocalHostd {
		return IssuedHostdToken{}, validationErrorf("%q is the co-located hostd's name; a remote hostd needs another", name)
	}
	if s, ok := c.Config.HostdRemote.(hostdSharedNamer); ok && s.SharedTokenName() == name {
		return IssuedHostdToken{}, validationErrorf("%q is the name -hostd-token-name gives the shared token's hostd; a registry hostd needs another", name)
	}
	if _, err := model.NormalizeHostdOwner(req.Owner); err != nil {
		return IssuedHostdToken{}, validationErrorf("%v", err)
	}
	if err := model.ValidateHostdLabels(req.Labels); err != nil {
		return IssuedHostdToken{}, validationErrorf("%v", err)
	}
	h, token, err := c.Store.CreateHostd(ctx, model.Hostd{
		Name: name, Owner: req.Owner, Labels: req.Labels, CreatedBy: RequestActor(ctx).Actor.ID,
	})
	if err != nil {
		return IssuedHostdToken{}, hostdError(err)
	}
	c.auditHostd(ctx, h.Name, "registered hostd "+h.Name)
	return IssuedHostdToken{Hostd: *h, Token: token}, nil
}

// RotateHostdToken retires name's tokens and mints a new one.
func (c *Client) RotateHostdToken(ctx context.Context, name string) (IssuedHostdToken, error) {
	if name == model.LocalHostd {
		return IssuedHostdToken{}, validationErrorf("%q is the co-located hostd, and it takes no token", name)
	}
	token, err := c.Store.RotateHostdToken(ctx, name)
	if err != nil {
		return IssuedHostdToken{}, hostdError(err)
	}
	h, err := c.Store.GetHostd(ctx, name)
	if err != nil {
		return IssuedHostdToken{}, err
	}
	c.auditHostd(ctx, name, "rotated hostd "+name+"'s token")
	return IssuedHostdToken{Hostd: *h, Token: token}, nil
}

// RevokeHostd revokes name and every token it has, dropping its live
// session at once if it is connected.
func (c *Client) RevokeHostd(ctx context.Context, name string) (model.Hostd, error) {
	if name == model.LocalHostd {
		return model.Hostd{}, validationErrorf("%q is the co-located hostd and cannot be revoked", name)
	}
	h, err := c.Store.RevokeHostd(ctx, name)
	if err != nil {
		return model.Hostd{}, hostdError(err)
	}
	if d, ok := c.Config.HostdRemote.(hostdDisconnector); ok {
		d.Disconnect(name)
	}
	if _, err := c.Store.ClearNotification(ctx, "hostd-disconnected:"+name, "automation:hostd"); err != nil {
		log.Printf("grain ui: clearing hostd-disconnected:%s after revoke: %v", name, err)
	}
	c.auditHostd(ctx, name, "revoked hostd "+name+" and its tokens")
	return *h, nil
}

// UpdateHostd sets name's owner and labels.
func (c *Client) UpdateHostd(ctx context.Context, name string, req UpdateHostdRequest) (model.Hostd, error) {
	if req.Owner != nil {
		if _, err := model.NormalizeHostdOwner(*req.Owner); err != nil {
			return model.Hostd{}, validationErrorf("%v", err)
		}
	}
	if req.Labels != nil {
		if err := model.ValidateHostdLabels(*req.Labels); err != nil {
			return model.Hostd{}, validationErrorf("%v", err)
		}
	}
	h, err := c.Store.UpdateHostd(ctx, name, req.Owner, req.Labels)
	if err != nil {
		return model.Hostd{}, hostdError(err)
	}
	c.auditHostd(ctx, name, "set hostd "+name+"'s owner or labels")
	if req.Owner != nil {
		c.dropSessionNotOwnedBy(name, h.Owner)
	}
	return *h, nil
}

// dropSessionNotOwnedBy closes name's live session when it was admitted
// as a verified identity that is no longer its owner: admission checks the
// owner only when a hostd dials in, so without this a hostd whose owner
// was changed away from it -- someone who left, a service account being
// replaced -- would keep serving host work until it next dropped. Its
// redial is then judged against the new owner. A session admitted by its
// token alone (a loopback hostd, or a daemon with no verifier) was never
// bound to an owner and is left alone.
func (c *Client) dropSessionNotOwnedBy(name, owner string) {
	st, ok := c.Config.HostdRemote.(hostdStatuses)
	if !ok {
		return
	}
	cur, ok := st.Statuses()[name]
	if !ok || !cur.Connected || cur.Principal == "" || strings.EqualFold(cur.Principal, owner) {
		return
	}
	if d, ok := c.Config.HostdRemote.(hostdDisconnector); ok && d.Disconnect(name) {
		log.Printf("grain hostd: dropped hostd %s's session: it connected as %s, and its owner is now %q", name, cur.Principal, owner)
	}
}

func (s *Server) handleListHostds(w http.ResponseWriter, r *http.Request) {
	out, err := s.tasks.ListHostds(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateHostd(w http.ResponseWriter, r *http.Request) {
	var req CreateHostdRequest
	if !readJSON(w, r, &req) {
		return
	}
	issued, err := s.tasks.CreateHostd(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	log.Printf("grain hostd: registered hostd %s (owner %q)", issued.Hostd.Name, issued.Hostd.Owner)
	issued.Install = s.hostdInstallInfo()
	writeJSON(w, http.StatusCreated, issued)
}

func (s *Server) handleRotateHostdToken(w http.ResponseWriter, r *http.Request) {
	issued, err := s.tasks.RotateHostdToken(r.Context(), r.PathValue("name"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	log.Printf("grain hostd: rotated hostd %s's token", issued.Hostd.Name)
	issued.Install = s.hostdInstallInfo()
	writeJSON(w, http.StatusOK, issued)
}

func (s *Server) handleRevokeHostd(w http.ResponseWriter, r *http.Request) {
	h, err := s.tasks.RevokeHostd(r.Context(), r.PathValue("name"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	log.Printf("grain hostd: revoked hostd %s and its tokens", h.Name)
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleUpdateHostd(w http.ResponseWriter, r *http.Request) {
	var req UpdateHostdRequest
	if !readJSON(w, r, &req) {
		return
	}
	h, err := s.tasks.UpdateHostd(r.Context(), r.PathValue("name"), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

// auditHostd records a change to the hostd registry in config_audit, with
// the person or agent who made it (ui.RequestActor). The summary names the
// hostd and never carries its token.
func (c *Client) auditHostd(ctx context.Context, name, summary string) {
	by := RequestActor(ctx)
	if _, err := c.Store.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		By: by.Actor, OnBehalfOf: by.OnBehalfOf, Surface: "hostd:" + name, Summary: summary,
	}); err != nil {
		log.Printf("grain hostd: recording %q in the config audit: %v", summary, err)
	}
}

// ListHostds is GET /api/hostds.
func (c *HTTPClient) ListHostds(ctx context.Context) ([]HostdView, error) {
	var out []HostdView
	err := c.do(ctx, http.MethodGet, "/api/hostds", nil, &out)
	return out, err
}

// CreateHostd is POST /api/hostds: the new hostd and its token, shown
// this once.
func (c *HTTPClient) CreateHostd(ctx context.Context, req CreateHostdRequest) (IssuedHostdToken, error) {
	var out IssuedHostdToken
	err := c.do(ctx, http.MethodPost, "/api/hostds", req, &out)
	return out, err
}

// UpdateHostd is PATCH /api/hostds/{name}.
func (c *HTTPClient) UpdateHostd(ctx context.Context, name string, req UpdateHostdRequest) (model.Hostd, error) {
	var out model.Hostd
	err := c.do(ctx, http.MethodPatch, "/api/hostds/"+url.PathEscape(name), req, &out)
	return out, err
}
