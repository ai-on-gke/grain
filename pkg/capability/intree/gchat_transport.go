package intree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// This file is how the gchat server reaches Google Chat: a space's
// incoming webhook, or the Chat API as a Chat app with the chat.bot
// scope, under a configured or an ambient credential.

// GChatBotScope is the OAuth scope a Chat app posts with.
const GChatBotScope = "https://www.googleapis.com/auth/chat.bot"

// GChatAPIBase is the Chat API's origin.
const GChatAPIBase = "https://chat.googleapis.com"

// gchatPostTimeout bounds one post.
const gchatPostTimeout = 20 * time.Second

// GChatADC finds the process's Application Default Credentials: what an
// app whose credential_source is ambient posts as. google.FindDefaultCredentials
// in the server; a fake in tests.
type GChatADC func(ctx context.Context, scopes ...string) (*google.Credentials, error)

// gchatMessage is the part of a Chat API Message the server sends and
// reads back.
type gchatMessage struct {
	Name    string          `json:"name,omitempty"`
	Text    string          `json:"text,omitempty"`
	CardsV2 []any           `json:"cardsV2,omitempty"`
	Thread  *gchatThreadRef `json:"thread,omitempty"`
}

type gchatThreadRef struct {
	Name      string `json:"name,omitempty"`
	ThreadKey string `json:"threadKey,omitempty"`
}

// gchatPoster posts one message to one space.
type gchatPoster interface {
	post(ctx context.Context, msg gchatMessage, replyOption string) (gchatMessage, error)
}

// webhookPoster posts through a space's incoming webhook. Its URL holds
// the webhook's key and token: it is never logged or put in an error.
type webhookPoster struct {
	url    string
	client *http.Client
}

func (p *webhookPoster) post(ctx context.Context, msg gchatMessage, replyOption string) (gchatMessage, error) {
	u, err := url.Parse(p.url)
	if err != nil {
		return gchatMessage{}, errors.New("the webhook URL does not parse")
	}
	q := u.Query()
	q.Set("messageReplyOption", replyOption)
	u.RawQuery = q.Encode()
	return gchatSend(ctx, p.client, u.String(), nil, msg)
}

// appPoster posts as a Chat app through the Chat API.
type appPoster struct {
	base    string
	space   string
	ts      oauth2.TokenSource
	project string
	client  *http.Client
}

func (p *appPoster) post(ctx context.Context, msg gchatMessage, replyOption string) (gchatMessage, error) {
	tok, err := p.ts.Token()
	if err != nil {
		return gchatMessage{}, fmt.Errorf("getting a token: %s", scrubURLError(err))
	}
	h := http.Header{}
	tok.SetAuthHeader(&http.Request{Header: h})
	if p.project != "" {
		h.Set("X-Goog-User-Project", p.project)
	}
	target := strings.TrimRight(p.base, "/") + "/v1/" + p.space + "/messages?messageReplyOption=" + url.QueryEscape(replyOption)
	return gchatSend(ctx, p.client, target, h, msg)
}

// gchatSend POSTs msg and reads the created message back. Errors name
// the HTTP status and Chat's own error message, never the URL.
func gchatSend(ctx context.Context, client *http.Client, target string, h http.Header, msg gchatMessage) (gchatMessage, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return gchatMessage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, gchatPostTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return gchatMessage{}, errors.New("building the request failed")
	}
	for k, v := range h {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return gchatMessage{}, errors.New(scrubURLError(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return gchatMessage{}, fmt.Errorf("Google Chat answered %s%s", resp.Status, chatErrorMessage(raw))
	}
	var out gchatMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return gchatMessage{}, fmt.Errorf("Google Chat answered %s with a body that is not a message", resp.Status)
	}
	return out, nil
}

// chatErrorMessage is ": <message>" from a Google API error body, or "".
func chatErrorMessage(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Error.Message == "" {
		return ""
	}
	m := e.Error.Message
	if len(m) > 300 {
		m = m[:300] + "..."
	}
	return ": " + m
}

// scrubURLError is err without the URL a *url.Error quotes: a webhook's
// URL is its credential.
func scrubURLError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Op + ": " + scrubURLError(ue.Err)
	}
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return "the token endpoint answered " + re.Response.Status
	}
	return err.Error()
}

// gchatAuth is an app's resolved credential: where its tokens come from
// and who it is.
type gchatAuth struct {
	ts oauth2.TokenSource
	// identity is who the app posts as, as Test reports it.
	identity string
	// kind is the credential's type: service_account, external_account,
	// authorized_user, impersonated_service_account, or metadata.
	kind string
}

// gchatResolver turns an app's credential source into a gchatAuth.
type gchatResolver struct {
	adc GChatADC
	// metadataEmail is the metadata server's default service account,
	// for ambient credentials that come from it.
	metadataEmail func(ctx context.Context) (string, error)
	// tokenInfo says whose an access token is, for an ambient user
	// credential whose JSON names nobody.
	tokenInfo func(ctx context.Context, accessToken string) (string, error)
}

func (r gchatResolver) resolve(ctx context.Context, app GChatApp) (*gchatAuth, error) {
	var creds *google.Credentials
	var err error
	switch app.CredentialSource {
	case GChatSourceConfigured:
		creds, err = google.CredentialsFromJSON(ctx, []byte(app.Credential), GChatBotScope)
		if err != nil {
			// The library's message can quote the credential.
			return nil, fmt.Errorf("app %q: its configured credential is not a usable service-account key or workload-identity config", app.Name)
		}
	case GChatSourceAmbient:
		adc := r.adc
		if adc == nil {
			adc = google.FindDefaultCredentials
		}
		creds, err = adc(ctx, GChatBotScope)
		if err != nil {
			return nil, fmt.Errorf("app %q: no ambient credentials where this server runs (Application Default Credentials): %s", app.Name, scrubURLError(err))
		}
	default:
		return nil, fmt.Errorf("app %q: credential_source must be %s or %s", app.Name, GChatSourceConfigured, GChatSourceAmbient)
	}
	a := &gchatAuth{ts: creds.TokenSource}
	var head struct {
		Type           string `json:"type"`
		ClientEmail    string `json:"client_email"`
		Impersonation  string `json:"service_account_impersonation_url"`
		Account        string `json:"account"`
		QuotaProjectID string `json:"quota_project_id"`
	}
	if len(creds.JSON) > 0 {
		_ = json.Unmarshal(creds.JSON, &head)
	}
	a.kind = head.Type
	switch {
	case len(creds.JSON) == 0:
		a.kind = "metadata"
		if r.metadataEmail != nil {
			a.identity, _ = r.metadataEmail(ctx)
		} else if metadata.OnGCE() {
			a.identity, _ = metadata.EmailWithContext(ctx, "default")
		}
	case head.ClientEmail != "":
		a.identity = head.ClientEmail
	case head.Impersonation != "":
		a.identity = impersonatedEmail(head.Impersonation)
	case head.Account != "":
		a.identity = head.Account
	}
	if a.identity == "" && a.kind != "metadata" {
		if tok, err := creds.TokenSource.Token(); err == nil {
			ti := r.tokenInfo
			if ti == nil {
				ti = googleTokenInfo
			}
			a.identity, _ = ti(ctx, tok.AccessToken)
		}
	}
	if a.identity == "" {
		a.identity = "unknown"
	}
	return a, nil
}

// impersonatedEmail is the service account an impersonation URL names:
// .../serviceAccounts/<email>:generateAccessToken.
func impersonatedEmail(u string) string {
	_, rest, ok := strings.Cut(u, "/serviceAccounts/")
	if !ok {
		return ""
	}
	email, _, _ := strings.Cut(rest, ":")
	return email
}

// googleTokenInfo asks Google whose an access token is.
func googleTokenInfo(ctx context.Context, accessToken string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/tokeninfo",
		strings.NewReader(url.Values{"access_token": {accessToken}}.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", errors.New(scrubURLError(err))
	}
	defer resp.Body.Close()
	var out struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return "", err
	}
	return out.Email, nil
}

// gchatCanPost asks the Chat API whether the app can see space: a GET
// of the space, which a Chat app can make only for a space it is a
// member of. It is what Test reports for an app space.
func gchatCanPost(ctx context.Context, client *http.Client, base, space, project string, ts oauth2.TokenSource) error {
	tok, err := ts.Token()
	if err != nil {
		return fmt.Errorf("getting a token: %s", scrubURLError(err))
	}
	ctx, cancel := context.WithTimeout(ctx, gchatPostTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/v1/"+space, nil)
	if err != nil {
		return err
	}
	tok.SetAuthHeader(req)
	if project != "" {
		req.Header.Set("X-Goog-User-Project", project)
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New(scrubURLError(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Google Chat answered %s%s", resp.Status, chatErrorMessage(raw))
	}
	return nil
}
