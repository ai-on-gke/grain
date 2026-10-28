package intree

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bwsalmon/grain/pkg/capability/protocol"
)

// The gchat server lets an agent post to Google Chat spaces the operator
// configured -- one way: nothing a space says comes back. Each space is
// reached through one of two transports:
//
//   - webhook: the space's incoming webhook URL, a secret. No Chat app
//     has to be approved; the sender's name is the webhook's own, set
//     when the webhook was created.
//   - app: the Chat API as a Chat app (chat.bot scope), under one of the
//     configured apps' credentials, described by source: configured, a
//     service-account key or workload-identity config stored in grain's
//     secrets; or ambient, whatever identity the server process already
//     has where it runs (Application Default Credentials) -- in a
//     container its ADC or metadata identity, on the host through grain
//     hostd (run: host) the user's own.
//
// A grant names one space, with: {space}, an enum of the configured
// spaces' names, and gets an attempt-scoped toolset with one tool,
// post_message. Every guardrail is enforced here: a per-attempt and a
// per-space rate limit, a maximum length, Chat text formatting only, no
// @-mentions or @all unless the space allows them, and a stamp on every
// message naming the task and linking to it in grain's UI. Each message
// also carries a card header with the space's display name and avatar,
// which is how a webhook post shows a name of the operator's choosing.
// Release closes the attempt's toolset, and returns what it posted as
// the release's audit.

// GChatName is the server's name.
const GChatName = "gchat"

// GChatMethod is its one method, gchat#post.
const GChatMethod = "post"

// DefaultGChatPort is the data-plane listener's port unless -listen says
// otherwise (docs/ports.md).
const DefaultGChatPort = 4251

// Credential sources (docs/capabilities-spec.md §4.5.8).
const (
	GChatSourceConfigured = "configured"
	GChatSourceAmbient    = "ambient"
)

// Transports.
const (
	GChatTransportWebhook = "webhook"
	GChatTransportApp     = "app"
)

// Mention policies a space allows.
const (
	GChatMentionsNone  = "none"
	GChatMentionsUsers = "users"
	GChatMentionsAll   = "all"
)

// GChatChatMaxLen is the longest text Google Chat takes in one message.
const GChatChatMaxLen = 4096

// Defaults.
const (
	defaultGChatMaxLen       = 2000
	defaultGChatPerAttempt   = 20
	defaultGChatPerSpaceHour = 60
	defaultGChatDisplayName  = "grain"
)

// GChatTestTool is the control-plane tool Settings' Test post calls.
const GChatTestTool = "test_post"

// GChatUsageTool is the control-plane tool Settings reads usage from.
const GChatUsageTool = "usage"

// gchatTestCaller is the attempt id a Test post is recorded under.
const gchatTestCaller = "settings-test"

var gchatNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// gchatSpacePattern is a Chat API space resource name.
var gchatSpacePattern = regexp.MustCompile(`^spaces/[A-Za-z0-9_-]{1,64}$`)

// gchatThreadPattern is a thread resource name a post may reply into.
var gchatThreadPattern = regexp.MustCompile(`^spaces/[A-Za-z0-9_-]{1,64}/threads/[A-Za-z0-9_.-]{1,128}$`)

// GChatApp is one Chat app the server posts as.
type GChatApp struct {
	Name             string
	CredentialSource string
	// Project is the quota project sent with the app's Chat API calls.
	Project string
	// Credential is the configured credential's JSON; empty for ambient.
	// It is a secret: never log it or put it in an error.
	Credential string
}

// GChatSpace is one space agents may be granted.
type GChatSpace struct {
	Name      string
	Transport string
	// Space is spaces/<id>, for transport app.
	Space string
	// App names the GChatApp an app space posts as.
	App string
	// WebhookURL is a webhook space's URL. It is a secret.
	WebhookURL  string
	DisplayName string
	AvatarURL   string
	// Threads allows thread: "new" (and replies into a thread other
	// than the task's own).
	Threads  bool
	MaxLen   int
	Mentions string
	PerHour  int
}

// GChatConfig is the server's parsed configuration.
type GChatConfig struct {
	Token       string
	Deployment  string
	GrainURL    string
	DisplayName string
	AvatarURL   string
	PerAttempt  int
	Apps        []GChatApp
	Spaces      []GChatSpace
}

func (c *GChatConfig) space(name string) *GChatSpace {
	for i := range c.Spaces {
		if c.Spaces[i].Name == name {
			return &c.Spaces[i]
		}
	}
	return nil
}

func (c *GChatConfig) app(name string) *GChatApp {
	for i := range c.Apps {
		if c.Apps[i].Name == name {
			return &c.Apps[i]
		}
	}
	return nil
}

// gchatConfigSchema is the configure schema (§4.5). Secrets are whole
// top-level strings: webhook_urls and app_credentials are JSON objects
// keyed by space and app name, the shape gcp's admin_credentials has.
func gchatConfigSchema() map[string]any {
	name := map[string]any{"type": "string", "pattern": gchatNamePattern.String()}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			protocol.DataPlaneTokenConfig: map[string]any{
				"type": "string", "writeOnly": true, "title": "Data-plane token", "minLength": MinTokenBytes,
				"description": DataPlaneTokenDescription,
			},
			protocol.DeploymentIDConfig: map[string]any{"type": "string"},
			"grain_url": map[string]any{
				"type": "string", "title": "Grain UI URL", "pattern": `^https?://[^\s]+$`,
				"description": "The address people open grain's UI at, e.g. https://grain.example.com. Every message links to its task under it.",
			},
			"display_name": map[string]any{"type": "string", "title": "Default display name", "maxLength": 80,
				"description": "The name on each message's card header, for a space that sets none. Default: grain."},
			"avatar_url": map[string]any{"type": "string", "title": "Default avatar URL", "pattern": `^https://[^\s]+$`},
			"per_attempt": map[string]any{"type": "integer", "title": "Posts per attempt", "minimum": 1, "maximum": 1000,
				"description": fmt.Sprintf("The most messages one attempt may post, across its spaces. Default %d.", defaultGChatPerAttempt)},
			"apps": map[string]any{
				"type": "array", "title": "Chat apps",
				"description": "Chat apps app spaces post as, each with its credential source.",
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []any{"name", "credential_source"},
					"properties": map[string]any{
						"name": name,
						"credential_source": map[string]any{"type": "string", "enum": []any{GChatSourceConfigured, GChatSourceAmbient},
							"description": "configured: a credential in app_credentials; ambient: the identity the server already runs as (its ADC)."},
						"project": map[string]any{"type": "string", "pattern": `^[a-z][a-z0-9-]{4,61}[a-z0-9]$`,
							"description": "Quota project for the app's Chat API calls."},
					},
				},
			},
			"app_credentials": map[string]any{
				"type": "string", "writeOnly": true, "title": "Configured app credentials",
				"description": `JSON object from app name to its credential (service-account key JSON or workload-identity config, granted the chat.bot scope), for each app whose credential_source is configured: {"grain-bot": {...}}.`,
			},
			"spaces": map[string]any{
				"type": "array", "title": "Spaces",
				"description": "The spaces a grant may name. Nothing else can be posted to.",
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []any{"name", "transport"},
					"properties": map[string]any{
						"name":      name,
						"transport": map[string]any{"type": "string", "enum": []any{GChatTransportWebhook, GChatTransportApp}, "description": "webhook: the URL in webhook_urls; app: the Chat API as app."},
						"space":     map[string]any{"type": "string", "pattern": gchatSpacePattern.String(), "description": "spaces/<id>, for transport app."},
						"app":       map[string]any{"type": "string", "pattern": gchatNamePattern.String(), "description": "The app an app space posts as."},
						"display_name": map[string]any{"type": "string", "maxLength": 80,
							"description": "The name on this space's message card header. A webhook's sender name is set when the webhook is created."},
						"avatar_url": map[string]any{"type": "string", "pattern": `^https://[^\s]+$`},
						"allowed": map[string]any{
							"type": "object", "additionalProperties": false,
							"properties": map[string]any{
								"threads":  map[string]any{"type": "boolean", "default": false, "description": `Allow thread: "new" and replies outside the task's own thread.`},
								"max_len":  map[string]any{"type": "integer", "minimum": 1, "maximum": GChatChatMaxLen, "description": fmt.Sprintf("Longest message text. Default %d.", defaultGChatMaxLen)},
								"mentions": map[string]any{"type": "string", "enum": []any{GChatMentionsNone, GChatMentionsUsers, GChatMentionsAll}, "default": GChatMentionsNone, "description": "none, users (<users/...>), or all (users and @all)."},
								"per_hour": map[string]any{"type": "integer", "minimum": 1, "maximum": 10000, "description": fmt.Sprintf("The most messages posted to the space per hour, across attempts. Default %d.", defaultGChatPerSpaceHour)},
							},
						},
					},
				},
			},
			"webhook_urls": map[string]any{
				"type": "string", "writeOnly": true, "title": "Webhook URLs",
				"description": `JSON object from webhook space name to its incoming webhook URL: {"team-room": "https://chat.googleapis.com/v1/spaces/.../messages?key=...&token=..."}.`,
			},
		},
	}
}

// parseGChatConfig reads and validates a configure call's config.
// Errors name fields and entries, never a secret's value.
func parseGChatConfig(config map[string]any, webhookHosts []string) (*GChatConfig, error) {
	c := &GChatConfig{PerAttempt: defaultGChatPerAttempt, DisplayName: defaultGChatDisplayName}
	c.Token, _ = config[protocol.DataPlaneTokenConfig].(string)
	c.Deployment, _ = config[protocol.DeploymentIDConfig].(string)
	c.GrainURL = strings.TrimRight(str(config["grain_url"]), "/")
	if v := str(config["display_name"]); v != "" {
		c.DisplayName = v
	}
	c.AvatarURL = str(config["avatar_url"])
	if n, ok := configNumber(config["per_attempt"]); ok {
		c.PerAttempt = int(n)
	}
	creds := map[string]string{}
	if raw := strings.TrimSpace(str(config["app_credentials"])); raw != "" {
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &m) != nil {
			return nil, fmt.Errorf("app_credentials is not a JSON object from app name to credential")
		}
		for k, v := range m {
			var s string
			if json.Unmarshal(v, &s) == nil {
				creds[k] = s
			} else {
				creds[k] = string(v)
			}
		}
	}
	hooks := map[string]string{}
	if raw := strings.TrimSpace(str(config["webhook_urls"])); raw != "" {
		if json.Unmarshal([]byte(raw), &hooks) != nil {
			return nil, fmt.Errorf("webhook_urls is not a JSON object from space name to webhook URL")
		}
	}
	for _, item := range list(config["apps"]) {
		m, _ := item.(map[string]any)
		a := GChatApp{Name: str(m["name"]), CredentialSource: str(m["credential_source"]), Project: str(m["project"])}
		a.Credential = creds[a.Name]
		c.Apps = append(c.Apps, a)
	}
	for _, item := range list(config["spaces"]) {
		m, _ := item.(map[string]any)
		s := GChatSpace{
			Name: str(m["name"]), Transport: str(m["transport"]), Space: str(m["space"]), App: str(m["app"]),
			DisplayName: str(m["display_name"]), AvatarURL: str(m["avatar_url"]),
			MaxLen: defaultGChatMaxLen, Mentions: GChatMentionsNone, PerHour: defaultGChatPerSpaceHour,
		}
		if al, ok := m["allowed"].(map[string]any); ok {
			s.Threads, _ = al["threads"].(bool)
			if n, ok := configNumber(al["max_len"]); ok {
				s.MaxLen = int(n)
			}
			if v := str(al["mentions"]); v != "" {
				s.Mentions = v
			}
			if n, ok := configNumber(al["per_hour"]); ok {
				s.PerHour = int(n)
			}
		}
		s.WebhookURL = hooks[s.Name]
		c.Spaces = append(c.Spaces, s)
	}
	if err := c.validate(creds, hooks, webhookHosts); err != nil {
		return nil, err
	}
	return c, nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

// gchatCredentialKind checks a configured credential is a service-account
// key or a workload-identity config, without quoting it.
func gchatCredentialKind(raw string) error {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(raw), &head) != nil {
		return fmt.Errorf("its configured credential is not JSON")
	}
	switch head.Type {
	case "service_account", "external_account":
		return nil
	}
	return fmt.Errorf("its configured credential must be a service-account key (type service_account) or a workload-identity config (type external_account), not %q", head.Type)
}

// validate checks the configuration as a whole.
func (c *GChatConfig) validate(creds, hooks map[string]string, webhookHosts []string) error {
	apps := map[string]bool{}
	for _, a := range c.Apps {
		if !gchatNamePattern.MatchString(a.Name) {
			return fmt.Errorf("app name %q does not match %s", a.Name, gchatNamePattern)
		}
		if apps[a.Name] {
			return fmt.Errorf("app %q is configured twice", a.Name)
		}
		apps[a.Name] = true
		switch a.CredentialSource {
		case GChatSourceConfigured:
			if strings.TrimSpace(a.Credential) == "" {
				return fmt.Errorf("app %q: credential_source is configured but app_credentials has no credential for it", a.Name)
			}
			if err := gchatCredentialKind(a.Credential); err != nil {
				return fmt.Errorf("app %q: %v", a.Name, err)
			}
		case GChatSourceAmbient:
			if a.Credential != "" {
				return fmt.Errorf("app %q: credential_source is ambient, so app_credentials must not hold a credential for it", a.Name)
			}
		default:
			return fmt.Errorf("app %q: credential_source must be %s or %s", a.Name, GChatSourceConfigured, GChatSourceAmbient)
		}
	}
	if extra := missingKeys(creds, apps); len(extra) > 0 {
		return fmt.Errorf("app_credentials names %s, which no app is called", strings.Join(extra, ", "))
	}
	spaces := map[string]bool{}
	webhooks := map[string]bool{}
	for _, s := range c.Spaces {
		if !gchatNamePattern.MatchString(s.Name) {
			return fmt.Errorf("space name %q does not match %s", s.Name, gchatNamePattern)
		}
		if spaces[s.Name] {
			return fmt.Errorf("space %q is configured twice", s.Name)
		}
		spaces[s.Name] = true
		switch s.Transport {
		case GChatTransportWebhook:
			webhooks[s.Name] = true
			if s.Space != "" || s.App != "" {
				return fmt.Errorf("space %q: a webhook space takes no space or app; its webhook URL says where it posts", s.Name)
			}
			if s.WebhookURL == "" {
				return fmt.Errorf("space %q: transport is webhook but webhook_urls has no URL for it", s.Name)
			}
			if err := checkWebhookURL(s.WebhookURL, webhookHosts); err != nil {
				return fmt.Errorf("space %q: %v", s.Name, err)
			}
		case GChatTransportApp:
			if !gchatSpacePattern.MatchString(s.Space) {
				return fmt.Errorf("space %q: transport app needs space, a Chat space name like spaces/AAAAxxxx", s.Name)
			}
			if !apps[s.App] {
				return fmt.Errorf("space %q: app %q is not one of the configured apps", s.Name, s.App)
			}
		default:
			return fmt.Errorf("space %q: transport must be %s or %s", s.Name, GChatTransportWebhook, GChatTransportApp)
		}
		if s.MaxLen < 1 || s.MaxLen > GChatChatMaxLen {
			return fmt.Errorf("space %q: max_len must be between 1 and %d", s.Name, GChatChatMaxLen)
		}
		switch s.Mentions {
		case GChatMentionsNone, GChatMentionsUsers, GChatMentionsAll:
		default:
			return fmt.Errorf("space %q: mentions must be none, users or all", s.Name)
		}
	}
	if extra := missingKeys(hooks, webhooks); len(extra) > 0 {
		return fmt.Errorf("webhook_urls names %s, which is not a webhook space", strings.Join(extra, ", "))
	}
	if len(c.Spaces) > 0 && c.GrainURL == "" {
		return fmt.Errorf("grain_url is required: every message links to its task in grain's UI")
	}
	if c.GrainURL != "" {
		if u, err := url.Parse(c.GrainURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("grain_url %q is not an http(s) URL", c.GrainURL)
		}
	}
	return nil
}

func missingKeys(m map[string]string, known map[string]bool) []string {
	var out []string
	for k := range m {
		if !known[k] {
			out = append(out, strconv.Quote(k))
		}
	}
	sort.Strings(out)
	return out
}

// checkWebhookURL accepts only a Google Chat incoming webhook (or, in
// tests, a host in extra). It never quotes the URL back.
func checkWebhookURL(raw string, extra []string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("its webhook URL does not parse")
	}
	host := u.Hostname()
	ok := u.Scheme == "https" && host == "chat.googleapis.com"
	for _, h := range extra {
		ok = ok || u.Host == h
	}
	if !ok {
		return fmt.Errorf("its webhook URL must be https://chat.googleapis.com/v1/spaces/<id>/messages?key=...&token=...")
	}
	if !strings.HasPrefix(u.Path, "/v1/spaces/") || !strings.HasSuffix(u.Path, "/messages") {
		return fmt.Errorf("its webhook URL's path must be /v1/spaces/<id>/messages")
	}
	if u.Query().Get("key") == "" || u.Query().Get("token") == "" {
		return fmt.Errorf("its webhook URL must carry the webhook's key and token")
	}
	return nil
}

// GChatServerConfig is what NewGChat builds the server from.
type GChatServerConfig struct {
	// Listen is the data plane's loopback address.
	Listen string
	// Token is the data-plane token from the environment, the fallback
	// for a configure that gives no dataPlaneToken.
	Token string
	// ADC finds ambient credentials; nil is google.FindDefaultCredentials.
	ADC GChatADC
	// MetadataEmail and TokenInfo name an ambient identity; nil asks the
	// metadata server and Google's tokeninfo.
	MetadataEmail func(ctx context.Context) (string, error)
	TokenInfo     func(ctx context.Context, accessToken string) (string, error)
	// ChatBase is the Chat API's origin; empty is GChatAPIBase.
	ChatBase string
	// WebhookHosts are extra host:ports a webhook URL may name (tests).
	WebhookHosts []string
	Client       *http.Client
	Log          *log.Logger
	Now          func() time.Time
}

// GChatAudit is one post, or one refused post, of an attempt.
type GChatAudit struct {
	Time      time.Time `json:"time"`
	AttemptID string    `json:"attempt_id"`
	Task      string    `json:"task,omitempty"`
	Space     string    `json:"space"`
	Message   string    `json:"message,omitempty"`
	Thread    string    `json:"thread,omitempty"`
	Text      string    `json:"text,omitempty"`
	Refused   string    `json:"refused,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// GChatUsage is one space's usage, as Settings shows it.
type GChatUsage struct {
	Space     string     `json:"space"`
	Transport string     `json:"transport"`
	Posted    int        `json:"posted"`
	Refused   int        `json:"refused"`
	Failed    int        `json:"failed"`
	LastHour  int        `json:"last_hour"`
	PerHour   int        `json:"per_hour"`
	LastPost  *time.Time `json:"last_post,omitempty"`
}

type gchatServer struct {
	cfg GChatServerConfig
	srv *Server
	// listening is whether the data plane is bound: only once a space is
	// configured, so an unused built-in holds no port.
	listening bool

	mu      sync.Mutex
	conf    *GChatConfig
	posters map[string]gchatPoster
	grants  map[string]*gchatGrant
	retired map[string][]GChatAudit
	// recent are each space's post times in the last hour.
	recent map[string][]time.Time
	usage  map[string]*GChatUsage
	// desc and args are the last method built's description and with:
	// schema, for tests.
	desc string
	args map[string]any
}

type gchatGrant struct {
	s         *gchatServer
	attemptID string
	task      string
	space     string
	mcp       *protocol.CapabilityServer

	mu      sync.Mutex
	posted  int
	threads map[string]bool
	records []GChatAudit
}

// NewGChat builds the gchat server. It offers no method, and binds no
// data-plane listener, until configure gives it at least one space and a
// data-plane token.
func NewGChat(cfg GChatServerConfig) (*Server, error) {
	srv, _, err := newGChat(cfg)
	return srv, err
}

func newGChat(cfg GChatServerConfig) (*Server, *gchatServer, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ChatBase == "" {
		cfg.ChatBase = GChatAPIBase
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: gchatPostTimeout}
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:" + strconv.Itoa(DefaultGChatPort)
	}
	g := &gchatServer{cfg: cfg, grants: map[string]*gchatGrant{}, retired: map[string][]GChatAudit{},
		recent: map[string][]time.Time{}, usage: map[string]*GChatUsage{}}
	s, err := newServer(GChatName, cfg.Log)
	if err != nil {
		return nil, nil, err
	}
	g.cfg.Log = s.Log
	g.srv = s
	s.onClose(g.closeAll)
	if err := s.HandleTool(GChatTestTool, "Post a clearly marked test message to one configured space, outside any grant, and say what it posted. Grain's Settings calls it; a sandbox cannot.",
		map[string]any{"type": "object", "additionalProperties": false, "required": []any{"space"},
			"properties": map[string]any{"space": map[string]any{"type": "string"}}}, g.testPost); err != nil {
		s.Close(context.Background())
		return nil, nil, err
	}
	if err := s.HandleTool(GChatUsageTool, "Each configured space's usage: messages posted, refused and failed since the server started, and in the last hour.",
		map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}}, g.usageTool); err != nil {
		s.Close(context.Background())
		return nil, nil, err
	}
	err = s.configurable(gchatConfigSchema(), func(ctx context.Context, config map[string]any) ([]*protocol.Method, error) {
		m, err := g.configure(ctx, config)
		if err != nil || m == nil {
			return nil, err
		}
		return []*protocol.Method{m}, nil
	})
	if err != nil {
		s.Close(context.Background())
		return nil, nil, err
	}
	return s, g, nil
}

// configure builds the posters and gchat#post, with a report -- each
// app's resolved identity and whether it can post, each space's
// transport -- in the method's description, which Settings' Test shows.
func (g *gchatServer) configure(ctx context.Context, config map[string]any) (*protocol.Method, error) {
	c, err := parseGChatConfig(config, g.cfg.WebhookHosts)
	if err != nil {
		return nil, fmt.Errorf("gchat: %v", err)
	}
	if c.Token == "" {
		c.Token = g.cfg.Token
	}
	if len(c.Spaces) == 0 {
		// Unconfigured: no method, which Settings shows as needing
		// configuring, rather than an error that would fail the attach.
		g.mu.Lock()
		g.conf, g.posters = c, nil
		g.mu.Unlock()
		return nil, nil
	}
	if len(c.Token) < MinTokenBytes {
		return nil, fmt.Errorf("gchat offers no method until it has a data-plane token: %s", MissingDataPlaneToken)
	}
	if !g.listening {
		if _, err := g.srv.listen(g.cfg.Listen, g); err != nil {
			return nil, fmt.Errorf("gchat: data plane: %w", err)
		}
		g.listening = true
	}
	resolver := gchatResolver{adc: g.cfg.ADC, metadataEmail: g.cfg.MetadataEmail, tokenInfo: g.cfg.TokenInfo}
	auths := map[string]*gchatAuth{}
	var report []string
	for _, a := range c.Apps {
		auth, err := resolver.resolve(ctx, a)
		if err != nil {
			report = append(report, err.Error())
			continue
		}
		auths[a.Name] = auth
		line := fmt.Sprintf("app %s (%s): posts as %s (%s)", a.Name, a.CredentialSource, auth.identity, auth.kind)
		if auth.kind == "authorized_user" {
			line += "; a user credential calls Chat as that user, so messages show the user rather than an app"
		}
		report = append(report, line)
	}
	posters := map[string]gchatPoster{}
	for _, s := range c.Spaces {
		switch s.Transport {
		case GChatTransportWebhook:
			posters[s.Name] = &webhookPoster{url: s.WebhookURL, client: g.cfg.Client}
			report = append(report, fmt.Sprintf("space %s: webhook (the sender name is the webhook's own; cards show %q)", s.Name, displayName(c, &s)))
		case GChatTransportApp:
			a := c.app(s.App)
			auth := auths[s.App]
			if auth == nil {
				report = append(report, fmt.Sprintf("space %s: app %s has no usable credential, so posts to it fail", s.Name, s.App))
				continue
			}
			posters[s.Name] = &appPoster{base: g.cfg.ChatBase, space: s.Space, ts: auth.ts, project: a.Project, client: g.cfg.Client}
			if err := gchatCanPost(ctx, g.cfg.Client, g.cfg.ChatBase, s.Space, a.Project, auth.ts); err != nil {
				report = append(report, fmt.Sprintf("space %s: app %s as %s cannot post to %s: %v", s.Name, s.App, auth.identity, s.Space, err))
			} else {
				report = append(report, fmt.Sprintf("space %s: app %s as %s can post to %s", s.Name, s.App, auth.identity, s.Space))
			}
		}
	}
	g.mu.Lock()
	g.conf, g.posters = c, posters
	for _, s := range c.Spaces {
		if g.usage[s.Name] == nil {
			g.usage[s.Name] = &GChatUsage{Space: s.Name}
		}
		g.usage[s.Name].Transport, g.usage[s.Name].PerHour = s.Transport, s.PerHour
	}
	g.mu.Unlock()
	names := make([]any, len(c.Spaces))
	for i, s := range c.Spaces {
		names[i] = s.Name
	}
	desc := "Post messages to one configured Google Chat space, one way: a post_message tool, rate-limited, stamped with the task and its link. Nothing posted in the space comes back."
	if len(report) > 0 {
		desc += "\n\n" + strings.Join(report, "\n")
	}
	args := map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"space"},
		"properties": map[string]any{
			"space": map[string]any{"type": "string", "title": "Space", "enum": names, "description": "The configured space this grant posts to."},
		},
	}
	g.mu.Lock()
	g.desc, g.args = desc, args
	g.mu.Unlock()
	return protocol.NewMethod(protocol.MethodSpec{
		Name:        GChatMethod,
		Description: desc,
		Args:        args,
		Mint:        g.mint,
		Release:     g.release,
		Audit:       g.takeAudit,
		Now:         g.cfg.Now,
	})
}

func displayName(c *GChatConfig, s *GChatSpace) string {
	if s.DisplayName != "" {
		return s.DisplayName
	}
	return c.DisplayName
}

func avatarURL(c *GChatConfig, s *GChatSpace) string {
	if s.AvatarURL != "" {
		return s.AvatarURL
	}
	return c.AvatarURL
}

// gchatTaskOf is the task an attempt_id belongs to: the segment after
// the deployment id when it is known, otherwise taskOf's.
func gchatTaskOf(deployment, attemptID string) string {
	if deployment != "" && strings.HasPrefix(attemptID, deployment+"-") {
		parts := strings.Split(strings.TrimPrefix(attemptID, deployment+"-"), "-")
		if len(parts) >= 3 {
			return strings.Join(parts[:len(parts)-2], "-")
		}
	}
	return taskOf(attemptID)
}

func (g *gchatServer) mint(_ context.Context, attemptID string, _ time.Time, args map[string]any) (*protocol.MintResponse, error) {
	if !pathSafe(attemptID) {
		return refusedResponse("attempt_id " + attemptID + " is not one a grant can have"), nil
	}
	name, _ := args["space"].(string)
	g.mu.Lock()
	c := g.conf
	g.mu.Unlock()
	if c == nil {
		return refusedResponse("gchat is not configured"), nil
	}
	sp := c.space(name)
	if sp == nil {
		return refusedResponse(fmt.Sprintf("space %q is not one of the configured spaces", name)), nil
	}
	task := gchatTaskOf(c.Deployment, attemptID)
	gr := &gchatGrant{s: g, attemptID: attemptID, task: task, space: sp.Name, threads: map[string]bool{}}
	var err error
	if gr.mcp, err = gr.toolset(c.Token, sp); err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.grants[attemptID] = gr
	g.mu.Unlock()
	threads := "Every message goes to this task's own thread in the space"
	if sp.Threads {
		threads += `; thread: "new" starts another, and a thread id post_message returned replies into that one`
	}
	return &protocol.MintResponse{
		Status:      protocol.StatusOK,
		MCPEndpoint: protocol.EndpointPath(attemptID),
		PromptSection: fmt.Sprintf("You can post to the Google Chat space %q with the post_message tool (one way: you cannot read the space or its replies). %s. "+
			"Use Chat formatting (*bold*, _italic_, `code`, <https://url|text>), not Markdown; at most %d characters; %s. "+
			"Each message is stamped with this task and a link to it. Post sparingly: there is a limit per attempt and per hour.",
			sp.Name, threads, sp.MaxLen, mentionRule(sp.Mentions)),
	}, nil
}

func mentionRule(m string) string {
	switch m {
	case GChatMentionsAll:
		return "@-mentions (<users/...>) and @all are allowed"
	case GChatMentionsUsers:
		return "@-mentions of people (<users/...>) are allowed, @all is not"
	}
	return "no @-mentions or @all"
}

func (g *gchatServer) release(_ context.Context, attemptID string) error {
	g.mu.Lock()
	gr := g.grants[attemptID]
	delete(g.grants, attemptID)
	g.mu.Unlock()
	if gr == nil {
		return nil
	}
	gr.mu.Lock()
	recs := append([]GChatAudit(nil), gr.records...)
	gr.mu.Unlock()
	g.mu.Lock()
	g.retired[attemptID] = append(g.retired[attemptID], recs...)
	g.mu.Unlock()
	return nil
}

func (g *gchatServer) takeAudit(attemptID string) ([]any, int) {
	g.mu.Lock()
	recs := g.retired[attemptID]
	delete(g.retired, attemptID)
	g.mu.Unlock()
	out := make([]any, len(recs))
	for i, r := range recs {
		out[i] = r
	}
	return out, 0
}

func (g *gchatServer) closeAll() error {
	g.mu.Lock()
	g.grants = map[string]*gchatGrant{}
	g.mu.Unlock()
	return nil
}

// ServeHTTP is the data plane: /runs/<attempt_id>/mcp, the grant's
// toolset behind the data-plane token. A released grant's path is gone.
func (g *gchatServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/runs/")
	id, kind, _ := strings.Cut(rest, "/")
	if !ok || kind != "mcp" || !protocol.ValidAttemptID(id) {
		http.NotFound(w, r)
		return
	}
	g.mu.Lock()
	gr := g.grants[id]
	g.mu.Unlock()
	if gr == nil {
		http.Error(w, "no grant "+id+": it was released, or never minted", http.StatusNotFound)
		return
	}
	gr.mcp.ServeHTTP(w, r)
}

// toolset is the grant's MCP server: post_message, behind token.
func (gr *gchatGrant) toolset(token string, sp *GChatSpace) (*protocol.CapabilityServer, error) {
	cs, err := protocol.NewCapabilityServer(GChatName + "-" + sp.Name)
	if err != nil {
		return nil, err
	}
	cs.Token = token
	threadDesc := `Leave it out to post in this task's own thread.`
	if sp.Threads {
		threadDesc = `Leave it out to post in this task's own thread; "new" starts a new thread; a thread id an earlier post_message returned replies into that thread.`
	}
	schema := map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"text"},
		"properties": map[string]any{
			"text":   map[string]any{"type": "string", "minLength": 1, "description": fmt.Sprintf("The message, in Google Chat text formatting, at most %d characters.", sp.MaxLen)},
			"thread": map[string]any{"type": "string", "description": threadDesc},
		},
	}
	err = cs.HandleTool("post_message", fmt.Sprintf("Post a message to the Google Chat space %q. Returns the message and thread ids. One way: replies are not readable.", sp.Name), schema,
		func(ctx context.Context, a map[string]any) ([]byte, error) {
			text, _ := a["text"].(string)
			thread, _ := a["thread"].(string)
			res, err := gr.s.post(ctx, gr, gr.space, text, thread, false)
			if err != nil {
				return nil, err
			}
			return json.Marshal(res)
		})
	if err != nil {
		return nil, err
	}
	return cs, nil
}

// gchatPostResult is what post_message returns.
type gchatPostResult struct {
	Message string `json:"message"`
	Thread  string `json:"thread"`
	Space   string `json:"space"`
}

var (
	gchatUserMention = regexp.MustCompile(`<users/[^>]*>`)
	gchatAllMention  = regexp.MustCompile(`(?i)<users/all>|(^|[^\w@])@(all|everyone|here)\b`)
	gchatAngle       = regexp.MustCompile(`<([^<>]*)>`)
	gchatMDLink      = regexp.MustCompile(`\[[^\]\n]+\]\((https?://|mailto:)[^)\s]+\)`)
	gchatMDHeading   = regexp.MustCompile(`(?m)^#{1,6}\s`)
	gchatMDBold      = regexp.MustCompile(`\*\*[^*\n]+\*\*|__[^_\n]+__`)
)

// checkText is the guardrails on a message's text, before it is stamped.
func checkText(sp *GChatSpace, text string) string {
	if strings.TrimSpace(text) == "" {
		return "the message is empty"
	}
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return "the message is not valid UTF-8 text"
	}
	if n := utf8.RuneCountInString(text); n > sp.MaxLen {
		return fmt.Sprintf("the message is %d characters, over space %s's limit of %d", n, sp.Name, sp.MaxLen)
	}
	if gchatAllMention.MatchString(text) && sp.Mentions != GChatMentionsAll {
		return fmt.Sprintf("space %s does not allow @all (or @everyone, @here)", sp.Name)
	}
	if gchatUserMention.MatchString(text) && sp.Mentions == GChatMentionsNone {
		return fmt.Sprintf("space %s does not allow @-mentions", sp.Name)
	}
	for _, m := range gchatAngle.FindAllStringSubmatch(text, -1) {
		inner := m[1]
		if strings.HasPrefix(inner, "https://") || strings.HasPrefix(inner, "http://") || strings.HasPrefix(inner, "mailto:") || strings.HasPrefix(inner, "users/") {
			continue
		}
		return "use Google Chat text formatting: <" + inner + "> is not a Chat link (<https://url|text>) or mention; HTML is not supported"
	}
	switch {
	case gchatMDLink.MatchString(text):
		return "use Google Chat text formatting: links are <https://url|text>, not Markdown [text](url)"
	case gchatMDHeading.MatchString(text):
		return "use Google Chat text formatting: Chat has no # headings"
	case gchatMDBold.MatchString(text):
		return "use Google Chat text formatting: bold is *text*, not **text**"
	}
	return ""
}

// stamp is the message a post sends: text with a line naming the task
// and linking to it, and a card whose header carries the display name
// and avatar.
func stamp(c *GChatConfig, sp *GChatSpace, task, text string, test bool) gchatMessage {
	link := c.GrainURL + "/tasks/" + url.PathEscape(task)
	label := "task " + task
	if test {
		link = c.GrainURL + "/settings/capabilities"
		label = "grain Settings"
	}
	footer := fmt.Sprintf("\n\n_Posted by grain for <%s|%s>_", link, label)
	header := map[string]any{"title": displayName(c, sp), "subtitle": strings.TrimSpace("grain · " + label)}
	if av := avatarURL(c, sp); av != "" {
		header["imageUrl"], header["imageType"] = av, "CIRCLE"
	}
	card := map[string]any{
		"cardId": "grain",
		"card": map[string]any{
			"header": header,
			"sections": []any{map[string]any{"widgets": []any{map[string]any{"buttonList": map[string]any{"buttons": []any{
				map[string]any{"text": "Open " + label, "onClick": map[string]any{"openLink": map[string]any{"url": link}}},
			}}}}}},
		},
	}
	return gchatMessage{Text: text + footer, CardsV2: []any{card}}
}

// post enforces the guardrails, posts, and records the result.
func (g *gchatServer) post(ctx context.Context, gr *gchatGrant, space, text, thread string, test bool) (*gchatPostResult, error) {
	g.mu.Lock()
	c := g.conf
	var poster gchatPoster
	if g.posters != nil {
		poster = g.posters[space]
	}
	g.mu.Unlock()
	if c == nil || c.space(space) == nil {
		return nil, fmt.Errorf("space %q is not configured", space)
	}
	sp := c.space(space)
	attemptID, task := gchatTestCaller, ""
	if gr != nil {
		attemptID, task = gr.attemptID, gr.task
	}
	rec := GChatAudit{Time: g.cfg.Now().UTC(), AttemptID: attemptID, Task: task, Space: space, Text: text}
	refuse := func(reason string) (*gchatPostResult, error) {
		rec.Refused = reason
		g.record(gr, rec, "refused")
		return nil, fmt.Errorf("refused: %s", reason)
	}
	if reason := checkText(sp, text); reason != "" {
		return refuse(reason)
	}
	threadKey, threadName := "", ""
	taskKey := "grain-task-" + task
	if c.Deployment != "" {
		taskKey = "grain-" + c.Deployment + "-task-" + task
	}
	switch {
	case test:
		threadKey = "grain-settings-test"
	case thread == "":
		threadKey = taskKey
	case thread == "new":
		if !sp.Threads {
			return refuse(fmt.Sprintf(`space %s does not allow thread: "new"; leave thread out to post in this task's thread`, space))
		}
		suffix, err := randomToken()
		if err != nil {
			return nil, err
		}
		threadKey = taskKey + "-" + suffix[:8]
	default:
		gr.mu.Lock()
		known := gr.threads[thread]
		gr.mu.Unlock()
		if !sp.Threads || !gchatThreadPattern.MatchString(thread) || !known {
			return refuse(fmt.Sprintf("thread %q is not one this attempt posted to in space %s; leave thread out, or pass \"new\" where allowed", thread, space))
		}
		threadName = thread
	}
	// Rate limits, checked and counted together so concurrent posts
	// cannot both pass.
	now := g.cfg.Now()
	if gr != nil {
		gr.mu.Lock()
		if gr.posted >= c.PerAttempt {
			gr.mu.Unlock()
			return refuse(fmt.Sprintf("this attempt has posted %d messages, its limit", c.PerAttempt))
		}
		gr.posted++
		gr.mu.Unlock()
	}
	g.mu.Lock()
	recent := g.recent[space][:0]
	for _, t := range g.recent[space] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= sp.PerHour {
		g.recent[space] = recent
		g.mu.Unlock()
		if gr != nil {
			gr.mu.Lock()
			gr.posted--
			gr.mu.Unlock()
		}
		return refuse(fmt.Sprintf("space %s has had %d messages in the last hour, its limit; try again later", space, sp.PerHour))
	}
	g.recent[space] = append(recent, now)
	g.mu.Unlock()

	if poster == nil {
		rec.Error = "the space has no usable transport; see the server's Test report"
		g.record(gr, rec, "failed")
		return nil, fmt.Errorf("posting to space %s: %s", space, rec.Error)
	}
	msg := stamp(c, sp, task, text, test)
	reply := "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"
	if threadName != "" {
		msg.Thread = &gchatThreadRef{Name: threadName}
		reply = "REPLY_MESSAGE_OR_FAIL"
	} else {
		msg.Thread = &gchatThreadRef{ThreadKey: threadKey}
	}
	got, err := poster.post(ctx, msg, reply)
	if err != nil {
		rec.Error = err.Error()
		g.record(gr, rec, "failed")
		return nil, fmt.Errorf("posting to space %s: %v", space, err)
	}
	res := &gchatPostResult{Message: got.Name, Space: space}
	if got.Thread != nil {
		res.Thread = got.Thread.Name
	}
	if gr != nil && res.Thread != "" {
		gr.mu.Lock()
		gr.threads[res.Thread] = true
		gr.mu.Unlock()
	}
	rec.Message, rec.Thread = res.Message, res.Thread
	g.record(gr, rec, "posted")
	return res, nil
}

// record keeps rec in the grant's audit and counts it in the space's
// usage. The log line names the space and ids, never a URL or token.
func (g *gchatServer) record(gr *gchatGrant, rec GChatAudit, outcome string) {
	detail := rec.Message
	if rec.Refused != "" {
		detail = rec.Refused
	} else if rec.Error != "" {
		detail = rec.Error
	}
	g.cfg.Log.Printf("gchat: attempt %s space %s %s (%d characters): %s", rec.AttemptID, rec.Space, outcome, utf8.RuneCountInString(rec.Text), detail)
	if gr != nil {
		gr.mu.Lock()
		gr.records = append(gr.records, rec)
		gr.mu.Unlock()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	u := g.usage[rec.Space]
	if u == nil {
		u = &GChatUsage{Space: rec.Space}
		g.usage[rec.Space] = u
	}
	switch outcome {
	case "posted":
		u.Posted++
		t := rec.Time
		u.LastPost = &t
	case "refused":
		u.Refused++
	default:
		u.Failed++
	}
}

// GChatTestResult is what test_post returns.
type GChatTestResult struct {
	Space   string `json:"space"`
	Message string `json:"message,omitempty"`
	Thread  string `json:"thread,omitempty"`
	Text    string `json:"text,omitempty"`
	Error   string `json:"error,omitempty"`
}

// GChatTestText is the text of a Test post.
const GChatTestText = "*[grain test]* This is a test message from grain's Settings, checking that grain can post here. Nobody needs to act on it."

func (g *gchatServer) testPost(ctx context.Context, args map[string]any) ([]byte, error) {
	space, _ := args["space"].(string)
	res, err := g.post(ctx, nil, space, GChatTestText, "", true)
	out := GChatTestResult{Space: space, Text: GChatTestText}
	if err != nil {
		out.Error = err.Error()
	} else {
		out.Message, out.Thread = res.Message, res.Thread
	}
	return json.Marshal(out)
}

func (g *gchatServer) usageTool(context.Context, map[string]any) ([]byte, error) {
	now := g.cfg.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []GChatUsage{}
	if g.conf != nil {
		for _, s := range g.conf.Spaces {
			u := GChatUsage{Space: s.Name, Transport: s.Transport, PerHour: s.PerHour}
			if have := g.usage[s.Name]; have != nil {
				u = *have
				u.Transport, u.PerHour = s.Transport, s.PerHour
			}
			for _, t := range g.recent[s.Name] {
				if now.Sub(t) < time.Hour {
					u.LastHour++
				}
			}
			out = append(out, u)
		}
	}
	return json.Marshal(out)
}

func init() {
	register(Builder{
		Name:        GChatName,
		NeedsConfig: true,
		Host:        true,
		Summary:     "post to allowlisted Google Chat spaces as a bot, one way, through a webhook or a Chat app (mint_post)",
		Args:        []string{"-listen", "127.0.0.1:" + strconv.Itoa(DefaultGChatPort)},
		Build: func(ctx context.Context, args []string, env Env, logger *log.Logger) (*Server, error) {
			fs := newFlags(GChatName, logOutput(logger))
			listen := fs.String("listen", "127.0.0.1:"+strconv.Itoa(DefaultGChatPort), "the data-plane listener's loopback address")
			if err := fs.Parse(args); err != nil {
				return nil, err
			}
			if fs.NArg() > 0 {
				return nil, fmt.Errorf("grain capability gchat takes no arguments, only -listen")
			}
			return NewGChat(GChatServerConfig{Listen: listenAddr(env, *listen), Token: env(protocol.DataPlaneTokenEnv), Log: logger})
		},
	})
}
