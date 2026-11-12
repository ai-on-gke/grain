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

package gcpfake

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
)

// Server is Cloud over HTTP: the REST surface of IAM, Resource Manager,
// IAM Credentials, Storage and Artifact Registry that gcpsets'
// GoogleConnector calls, and an OAuth2 token endpoint and STS that issue
// the admin tokens those calls must carry. Serve it (httptest.NewServer,
// or any listener on loopback) and point a GoogleConnector -- or a real
// `grain capability gcp -test-endpoints` -- at Endpoints(its URL), and
// the managed token reconcile runs end to end over real Google clients
// against the in-memory fake.
//
// It checks nothing a test does not ask it to: token requests are not
// verified (any JWT assertion or subject token gets a token), and the
// tokens it issues are the only credential its APIs accept.
type Server struct {
	Cloud *Cloud

	mu sync.Mutex
	// tokens is, by access token issued, who it was issued to.
	tokens map[string]string
	n      int
	// requests are the API requests served, "METHOD /path".
	requests []string
}

// NewServer serves c.
func NewServer(c *Cloud) *Server {
	return &Server{Cloud: c, tokens: map[string]string{}}
}

// Endpoints are the gcpsets.Endpoints for s served at base (its root
// URL, "http://127.0.0.1:port"): each service under its own path.
func Endpoints(base string) gcpsets.Endpoints {
	base = strings.TrimSuffix(base, "/")
	e := gcpsets.Endpoints{}
	for _, svc := range gcpsets.EndpointServices {
		e[svc] = base + "/" + svc + "/"
	}
	return e
}

// Requests are the API requests s has served, "METHOD /path", in order.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// Issued is who each access token s has issued was issued to.
func (s *Server) Issued() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.tokens))
	for k, v := range s.tokens {
		out[k] = v
	}
	return out
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	service, path, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	switch service {
	case gcpsets.EndpointOAuth2:
		s.oauth2(w, r, path)
		return
	case gcpsets.EndpointSTS:
		s.sts(w, r, path)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	_, authed := s.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	s.mu.Unlock()
	if !authed {
		writeError(w, http.StatusUnauthorized, "Request had invalid authentication credentials: no access token this stub issued")
		return
	}
	ctx := r.Context()
	var out any
	var err error
	switch service {
	case gcpsets.EndpointIAM:
		out, err = s.iam(ctx, r, strings.TrimPrefix(path, "v1/"))
	case gcpsets.EndpointResourceManager:
		out, err = s.crm(ctx, r, strings.TrimPrefix(path, "v1/"))
	case gcpsets.EndpointIAMCredentials:
		out, err = s.iamcredentials(ctx, r, strings.TrimPrefix(path, "v1/"))
	case gcpsets.EndpointStorage:
		out, err = s.storage(ctx, r, path)
	case gcpsets.EndpointArtifactRegistry:
		out, err = s.artifactregistry(ctx, r, strings.TrimPrefix(path, "v1/"))
	default:
		err = errNotFound
	}
	if err != nil {
		code := statusOf(err)
		writeError(w, code, strings.TrimPrefix(err.Error(), "googleapi: Error "+strconv.Itoa(code)+": "))
		return
	}
	writeJSON(w, out)
}

var errNotFound = errors.New("404 NOT_FOUND: no such method on this stub")

var statusCode = regexp.MustCompile(`\b(400|401|403|404|409)\b`)

// statusOf is the HTTP status the fake's error stands for: a lost etag
// race is 409, and otherwise the first status code its message names.
func statusOf(err error) int {
	if errors.Is(err, gcpsets.ErrConflict) {
		return http.StatusConflict
	}
	if m := statusCode.FindString(err.Error()); m != "" {
		n, _ := strconv.Atoi(m)
		return n
	}
	if strings.Contains(err.Error(), "FAILED_PRECONDITION") {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func writeJSON(w http.ResponseWriter, v any) {
	if v == nil {
		v = struct{}{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": code, "message": msg, "status": strings.ToUpper(strings.ReplaceAll(http.StatusText(code), " ", "_")),
	}})
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return fmt.Errorf("400 INVALID_ARGUMENT: %v", err)
	}
	return nil
}

// issue is a new access token for who.
func (s *Server) issue(who string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	tok := fmt.Sprintf("stub-admin-%d", s.n)
	s.tokens[tok] = who
	return map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": 3600}
}

// oauth2 is the token endpoint a service-account key's token_uri names
// and the tokeninfo lookup GoogleConnector.Identity does.
func (s *Server) oauth2(w http.ResponseWriter, r *http.Request, path string) {
	switch path {
	case "token":
		r.ParseForm()
		writeJSON(w, s.issue(assertionIssuer(r.PostForm.Get("assertion"))))
	case "tokeninfo":
		s.mu.Lock()
		who, ok := s.tokens[r.URL.Query().Get("access_token")]
		s.mu.Unlock()
		if !ok || who == "" {
			writeError(w, http.StatusBadRequest, "invalid_token")
			return
		}
		writeJSON(w, map[string]string{"email": who})
	default:
		writeError(w, http.StatusNotFound, "no such OAuth2 endpoint on this stub")
	}
}

// sts is the token exchange a workload-identity config's token_url names.
func (s *Server) sts(w http.ResponseWriter, r *http.Request, path string) {
	if path != "v1/token" {
		writeError(w, http.StatusNotFound, "no such STS endpoint on this stub")
		return
	}
	r.ParseForm()
	out := s.issue("")
	out["issued_token_type"] = "urn:ietf:params:oauth:token-type:access_token"
	writeJSON(w, out)
}

// assertionIssuer is the iss of a JWT bearer assertion -- the key's
// client_email -- read without checking its signature.
func assertionIssuer(jwt string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Iss string `json:"iss"`
	}
	json.Unmarshal(b, &claims)
	return claims.Iss
}

type wireBinding struct {
	Role    string   `json:"role"`
	Members []string `json:"members"`
}

type wirePolicy struct {
	Bindings []wireBinding `json:"bindings,omitempty"`
	Etag     string        `json:"etag,omitempty"`
	Version  int           `json:"version,omitempty"`
}

func toWire(p *gcpsets.Policy) wirePolicy {
	out := wirePolicy{Etag: p.Etag, Version: 1}
	for _, b := range p.Bindings {
		out.Bindings = append(out.Bindings, wireBinding{Role: b.Role, Members: b.Members})
	}
	return out
}

func fromWire(p wirePolicy) *gcpsets.Policy {
	out := &gcpsets.Policy{Etag: p.Etag}
	for _, b := range p.Bindings {
		out.Bindings = append(out.Bindings, gcpsets.Binding{Role: b.Role, Members: b.Members})
	}
	return out
}

// policyCall serves resource's :getIamPolicy, :setIamPolicy and
// :testIamPermissions; test is how testIamPermissions answers.
func (s *Server) policyCall(ctx context.Context, r *http.Request, resource, verb string, test func([]string) ([]string, error)) (any, error) {
	switch verb {
	case "getIamPolicy":
		p, err := s.Cloud.GetPolicy(ctx, resource)
		if err != nil {
			return nil, err
		}
		return toWire(p), nil
	case "setIamPolicy":
		var req struct {
			Policy wirePolicy `json:"policy"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := s.Cloud.SetPolicy(ctx, resource, fromWire(req.Policy)); err != nil {
			return nil, err
		}
		p, err := s.Cloud.GetPolicy(ctx, resource)
		if err != nil {
			return nil, err
		}
		return toWire(p), nil
	case "testIamPermissions":
		var req struct {
			Permissions []string `json:"permissions"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		has, err := test(req.Permissions)
		if err != nil {
			return nil, err
		}
		return map[string]any{"permissions": has}, nil
	}
	return nil, errNotFound
}

// accountPath matches "projects/<p>/serviceAccounts[/<email>[/keys[/<id>]]][:verb]".
var accountPath = regexp.MustCompile(`^projects/([^/:]+)/serviceAccounts(?:/([^/:]+)(?:/keys(?:/([^/:]+))?)?)?(?::([A-Za-z]+))?$`)

func (s *Server) iam(ctx context.Context, r *http.Request, path string) (any, error) {
	m := accountPath.FindStringSubmatch(path)
	if m == nil {
		return nil, errNotFound
	}
	project, email, keyID, verb := m[1], m[2], m[3], m[4]
	account := "projects/" + project + "/serviceAccounts/" + email
	hasKeys := strings.Contains(path, "/keys")
	switch {
	case email == "" && verb == "" && r.Method == http.MethodGet:
		accounts, err := s.Cloud.ListServiceAccounts(ctx, project)
		if err != nil {
			return nil, err
		}
		list := []map[string]string{}
		for _, a := range accounts {
			list = append(list, map[string]string{"name": "projects/" + project + "/serviceAccounts/" + a.Email, "email": a.Email, "description": a.Description})
		}
		return map[string]any{"accounts": list}, nil
	case email == "" && verb == "" && r.Method == http.MethodPost:
		var req struct {
			AccountID      string `json:"accountId"`
			ServiceAccount struct {
				DisplayName string `json:"displayName"`
				Description string `json:"description"`
			} `json:"serviceAccount"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		sa, err := s.Cloud.CreateServiceAccount(ctx, project, req.AccountID, req.ServiceAccount.DisplayName, req.ServiceAccount.Description)
		if err != nil {
			return nil, err
		}
		return map[string]string{"name": "projects/" + project + "/serviceAccounts/" + sa.Email, "email": sa.Email, "description": sa.Description, "displayName": req.ServiceAccount.DisplayName}, nil
	case email != "" && !hasKeys && verb == "" && r.Method == http.MethodDelete:
		return nil, s.Cloud.DeleteServiceAccount(ctx, project, email)
	case email != "" && !hasKeys && verb == "testIamPermissions":
		return s.policyCall(ctx, r, account, verb, func(perms []string) ([]string, error) {
			return s.Cloud.TestAccountPermissions(ctx, email, perms)
		})
	case email != "" && !hasKeys && verb != "":
		return s.policyCall(ctx, r, account, verb, nil)
	case hasKeys && keyID == "" && r.Method == http.MethodPost:
		id, keyJSON, err := s.Cloud.CreateKey(ctx, account)
		if err != nil {
			return nil, err
		}
		return map[string]string{"name": account + "/keys/" + id, "privateKeyData": base64.StdEncoding.EncodeToString([]byte(keyJSON))}, nil
	case hasKeys && keyID == "" && r.Method == http.MethodGet:
		keys, err := s.Cloud.ListKeys(ctx, account)
		if err != nil {
			return nil, err
		}
		list := []map[string]string{}
		for _, k := range keys {
			list = append(list, map[string]string{"name": account + "/keys/" + k.ID, "validAfterTime": k.CreatedAt.UTC().Format(time.RFC3339), "keyType": "USER_MANAGED"})
		}
		return map[string]any{"keys": list}, nil
	case hasKeys && keyID != "" && r.Method == http.MethodDelete:
		return nil, s.Cloud.DeleteKey(ctx, account, keyID)
	}
	return nil, errNotFound
}

func (s *Server) crm(ctx context.Context, r *http.Request, path string) (any, error) {
	rest, ok := strings.CutPrefix(path, "projects/")
	project, verb, hasVerb := strings.Cut(rest, ":")
	if !ok || !hasVerb || strings.Contains(project, "/") {
		return nil, errNotFound
	}
	return s.policyCall(ctx, r, "projects/"+project, verb, func(perms []string) ([]string, error) {
		return s.Cloud.TestPermissions(ctx, project, perms)
	})
}

func (s *Server) artifactregistry(ctx context.Context, r *http.Request, path string) (any, error) {
	resource, verb, ok := strings.Cut(path, ":")
	if !ok {
		return nil, errNotFound
	}
	return s.policyCall(ctx, r, resource, verb, func(perms []string) ([]string, error) {
		return s.Cloud.TestResourcePermissions(ctx, resource, perms)
	})
}

// storage is the bucket IAM calls: GET and PUT b/<bucket>/iam, and
// GET b/<bucket>/iam/testPermissions.
func (s *Server) storage(ctx context.Context, r *http.Request, path string) (any, error) {
	rest, ok := strings.CutPrefix(path, "b/")
	bucket, tail, _ := strings.Cut(rest, "/")
	if !ok || bucket == "" {
		return nil, errNotFound
	}
	resource := "buckets/" + bucket
	switch {
	case tail == "iam" && r.Method == http.MethodGet:
		p, err := s.Cloud.GetPolicy(ctx, resource)
		if err != nil {
			return nil, err
		}
		return toWire(p), nil
	case tail == "iam" && r.Method == http.MethodPut:
		var p wirePolicy
		if err := decode(r, &p); err != nil {
			return nil, err
		}
		if err := s.Cloud.SetPolicy(ctx, resource, fromWire(p)); err != nil {
			return nil, err
		}
		got, err := s.Cloud.GetPolicy(ctx, resource)
		if err != nil {
			return nil, err
		}
		return toWire(got), nil
	case tail == "iam/testPermissions" && r.Method == http.MethodGet:
		has, err := s.Cloud.TestResourcePermissions(ctx, resource, r.URL.Query()["permissions"])
		if err != nil {
			return nil, err
		}
		return map[string]any{"permissions": has}, nil
	}
	return nil, errNotFound
}

// iamcredentials is generateAccessToken and generateIdToken on
// projects/-/serviceAccounts/<email>.
func (s *Server) iamcredentials(ctx context.Context, r *http.Request, path string) (any, error) {
	m := accountPath.FindStringSubmatch(path)
	if m == nil || m[2] == "" || m[3] != "" || strings.Contains(path, "/keys") {
		return nil, errNotFound
	}
	email, verb := m[2], m[4]
	switch verb {
	case "generateAccessToken":
		var req struct {
			Scope    []string `json:"scope"`
			Lifetime string   `json:"lifetime"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		lifetime := time.Hour
		if req.Lifetime != "" {
			d, err := time.ParseDuration(req.Lifetime)
			if err != nil {
				return nil, fmt.Errorf("400 INVALID_ARGUMENT: lifetime %q", req.Lifetime)
			}
			lifetime = d
		}
		tok, exp, err := s.Cloud.GenerateAccessToken(ctx, email, req.Scope, lifetime)
		if err != nil {
			return nil, err
		}
		return map[string]string{"accessToken": tok, "expireTime": exp.UTC().Format(time.RFC3339)}, nil
	case "generateIdToken":
		var req struct {
			Audience string `json:"audience"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		tok, err := s.Cloud.GenerateIDToken(ctx, email, req.Audience)
		if err != nil {
			return nil, err
		}
		return map[string]string{"token": tok}, nil
	}
	return nil, errNotFound
}
