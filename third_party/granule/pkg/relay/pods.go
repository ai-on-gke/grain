package relay

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// In-cluster service account paths, as every pod sees them.
const (
	saTokenPath     = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	saCAPath        = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	saNamespacePath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

// PodResolverConfig configures a PodResolver.
type PodResolverConfig struct {
	// APIServer is the Kubernetes API base URL, e.g.
	// "https://10.0.0.1:443". InClusterPodResolver fills it in.
	APIServer string

	// HTTPClient talks to APIServer (TLS roots already set).
	HTTPClient *http.Client

	// BearerToken returns the token sent with every request. It is called
	// per request so a rotated, projected service account token is picked
	// up.
	BearerToken func() (string, error)

	// Namespace is the only namespace targets are resolved in.
	Namespace string

	// Selector is a label selector ("k=v,k2=v2", "k!=v", "k") every target
	// pod must match. Required: the relay is never a route to arbitrary
	// pods.
	Selector string

	// NameLabel, when set, is a label whose value names the sandbox; a
	// target matches a pod by that label's value as well as by pod name.
	NameLabel string
}

// PodResolver resolves sandbox names to pod IPs from an informer-style
// cache: one list of the pods in Namespace matching Selector, then a watch
// that keeps it current, relisting when the watch ends. Only get, list and
// watch on pods are used.
type PodResolver struct {
	cfg      PodResolverConfig
	selector labelSelector

	mu     sync.RWMutex
	pods   map[string]podInfo // by pod name
	synced chan struct{}
	once   sync.Once
}

type podInfo struct {
	name   string
	ip     string
	ready  bool
	labels map[string]string
}

// InClusterPodResolver builds a PodResolver from the pod's own service
// account and KUBERNETES_SERVICE_HOST/PORT. An empty namespace means the
// relay's own namespace.
func InClusterPodResolver(namespace, selector, nameLabel string) (*PodResolver, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("relay: not running in a cluster (KUBERNETES_SERVICE_HOST/PORT unset)")
	}
	caPEM, err := os.ReadFile(saCAPath)
	if err != nil {
		return nil, fmt.Errorf("relay: reading the service account CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("relay: %s holds no certificates", saCAPath)
	}
	if namespace == "" {
		b, err := os.ReadFile(saNamespacePath)
		if err != nil {
			return nil, fmt.Errorf("relay: no namespace given and reading %s: %w", saNamespacePath, err)
		}
		namespace = strings.TrimSpace(string(b))
	}
	return NewPodResolver(PodResolverConfig{
		APIServer: "https://" + net.JoinHostPort(host, port),
		HTTPClient: &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		}},
		BearerToken: func() (string, error) {
			b, err := os.ReadFile(saTokenPath)
			return strings.TrimSpace(string(b)), err
		},
		Namespace: namespace,
		Selector:  selector,
		NameLabel: nameLabel,
	})
}

// NewPodResolver validates cfg and returns a PodResolver. Call Run to fill
// its cache.
func NewPodResolver(cfg PodResolverConfig) (*PodResolver, error) {
	if cfg.APIServer == "" || cfg.HTTPClient == nil || cfg.BearerToken == nil {
		return nil, errors.New("relay: pod resolver needs an API server, HTTP client and bearer token")
	}
	if !dnsName.MatchString(cfg.Namespace) {
		return nil, fmt.Errorf("relay: invalid namespace %q", cfg.Namespace)
	}
	sel, err := parseSelector(cfg.Selector)
	if err != nil {
		return nil, err
	}
	if len(sel) == 0 {
		return nil, errors.New("relay: a pod label selector is required, so the relay cannot reach arbitrary pods")
	}
	return &PodResolver{
		cfg:      cfg,
		selector: sel,
		pods:     make(map[string]podInfo),
		synced:   make(chan struct{}),
	}, nil
}

// dnsName matches a Kubernetes object name (DNS-1123 subdomain). A target
// is used in an API path, so nothing else is accepted.
var dnsName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

// Resolve implements Resolver. It waits for the first list to finish, then
// answers from the cache, falling back to a single GET for a pod created
// since the last watch event. The pod must be in the configured namespace,
// match the selector, be Running and Ready, and have an IP.
func (r *PodResolver) Resolve(ctx context.Context, target string) (string, error) {
	if !dnsName.MatchString(target) {
		return "", fmt.Errorf("target %q is not a pod or sandbox name in namespace %s", target, r.cfg.Namespace)
	}
	select {
	case <-r.synced:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if p, ok := r.lookup(target); ok {
		return r.check(target, p)
	}
	p, err := r.getPod(ctx, target)
	if err != nil {
		return "", err
	}
	return r.check(target, p)
}

func (r *PodResolver) lookup(target string) (podInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.pods[target]; ok {
		return p, true
	}
	if r.cfg.NameLabel != "" {
		for _, p := range r.pods {
			if p.labels[r.cfg.NameLabel] == target {
				return p, true
			}
		}
	}
	return podInfo{}, false
}

func (r *PodResolver) check(target string, p podInfo) (string, error) {
	if !r.selector.matches(p.labels) {
		return "", fmt.Errorf("target %q is outside the relay's selector %q in namespace %s", target, r.cfg.Selector, r.cfg.Namespace)
	}
	if p.ip == "" || !p.ready {
		return "", fmt.Errorf("sandbox %q is not ready", target)
	}
	return p.ip, nil
}

// Run keeps the cache current until ctx is cancelled: list, then watch
// from the list's resourceVersion, relisting with backoff whenever the
// watch ends or fails.
func (r *PodResolver) Run(ctx context.Context) error {
	backoff := 200 * time.Millisecond
	for ctx.Err() == nil {
		rv, err := r.list(ctx)
		if err == nil {
			r.once.Do(func() { close(r.synced) })
			backoff = 200 * time.Millisecond
			err = r.watch(ctx, rv)
		}
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(backoff):
			}
			if backoff < 10*time.Second {
				backoff *= 2
			}
		}
	}
	return ctx.Err()
}

// Synced is closed once the first list has filled the cache.
func (r *PodResolver) Synced() <-chan struct{} { return r.synced }

type apiPod struct {
	Metadata struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		Labels            map[string]string `json:"labels"`
		DeletionTimestamp *string           `json:"deletionTimestamp"`
		ResourceVersion   string            `json:"resourceVersion"`
	} `json:"metadata"`
	Status struct {
		Phase      string `json:"phase"`
		PodIP      string `json:"podIP"`
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

func (p apiPod) info() podInfo {
	ready := p.Status.Phase == "Running" && p.Metadata.DeletionTimestamp == nil
	if ready {
		ready = false
		for _, c := range p.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				ready = true
			}
		}
	}
	return podInfo{name: p.Metadata.Name, ip: p.Status.PodIP, ready: ready, labels: p.Metadata.Labels}
}

func (r *PodResolver) podsURL(extra url.Values) string {
	q := url.Values{"labelSelector": {r.cfg.Selector}}
	for k, v := range extra {
		q[k] = v
	}
	return fmt.Sprintf("%s/api/v1/namespaces/%s/pods?%s", strings.TrimRight(r.cfg.APIServer, "/"), r.cfg.Namespace, q.Encode())
}

func (r *PodResolver) do(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	tok, err := r.cfg.BearerToken()
	if err != nil {
		return nil, fmt.Errorf("relay: reading the service account token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	resp, err := r.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, &apiError{code: resp.StatusCode, body: strings.TrimSpace(string(body))}
	}
	return resp, nil
}

type apiError struct {
	code int
	body string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("kubernetes API returned %d: %s", e.code, e.body)
}

func (r *PodResolver) list(ctx context.Context) (string, error) {
	resp, err := r.do(ctx, r.podsURL(nil))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var list struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Items []apiPod `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return "", fmt.Errorf("decoding pod list: %w", err)
	}
	pods := make(map[string]podInfo, len(list.Items))
	for _, p := range list.Items {
		pods[p.Metadata.Name] = p.info()
	}
	r.mu.Lock()
	r.pods = pods
	r.mu.Unlock()
	return list.Metadata.ResourceVersion, nil
}

func (r *PodResolver) watch(ctx context.Context, rv string) error {
	resp, err := r.do(ctx, r.podsURL(url.Values{
		"watch":               {"1"},
		"resourceVersion":     {rv},
		"allowWatchBookmarks": {"true"},
		"timeoutSeconds":      {"300"},
	}))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	stop := context.AfterFunc(ctx, func() { resp.Body.Close() })
	defer stop()
	dec := json.NewDecoder(bufio.NewReader(resp.Body))
	for {
		var ev struct {
			Type   string          `json:"type"`
			Object json.RawMessage `json:"object"`
		}
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch ev.Type {
		case "ADDED", "MODIFIED", "DELETED":
			var p apiPod
			if err := json.Unmarshal(ev.Object, &p); err != nil {
				return err
			}
			r.mu.Lock()
			if ev.Type == "DELETED" {
				delete(r.pods, p.Metadata.Name)
			} else {
				r.pods[p.Metadata.Name] = p.info()
			}
			r.mu.Unlock()
		case "ERROR":
			// Usually 410 Gone: the resourceVersion is too old. Relist.
			return fmt.Errorf("watch error: %s", ev.Object)
		}
	}
}

func (r *PodResolver) getPod(ctx context.Context, name string) (podInfo, error) {
	u := fmt.Sprintf("%s/api/v1/namespaces/%s/pods/%s", strings.TrimRight(r.cfg.APIServer, "/"), r.cfg.Namespace, name)
	resp, err := r.do(ctx, u)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.code == http.StatusNotFound {
			return podInfo{}, fmt.Errorf("no sandbox %q in namespace %s", name, r.cfg.Namespace)
		}
		return podInfo{}, err
	}
	defer resp.Body.Close()
	var p apiPod
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return podInfo{}, err
	}
	if p.Metadata.Namespace != "" && p.Metadata.Namespace != r.cfg.Namespace {
		return podInfo{}, fmt.Errorf("target %q is outside namespace %s", name, r.cfg.Namespace)
	}
	return p.info(), nil
}

// labelSelector is the equality-based subset of Kubernetes label selectors:
// "k=v", "k==v", "k!=v", "k" (exists) and "!k" (absent), comma-separated.
type labelSelector []selectorTerm

type selectorTerm struct {
	key, value string
	op         string // "=", "!=", "exists", "!exists"
}

func parseSelector(s string) (labelSelector, error) {
	var sel labelSelector
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var t selectorTerm
		switch {
		case strings.Contains(part, "!="):
			k, v, _ := strings.Cut(part, "!=")
			t = selectorTerm{key: strings.TrimSpace(k), value: strings.TrimSpace(v), op: "!="}
		case strings.Contains(part, "=="):
			k, v, _ := strings.Cut(part, "==")
			t = selectorTerm{key: strings.TrimSpace(k), value: strings.TrimSpace(v), op: "="}
		case strings.Contains(part, "="):
			k, v, _ := strings.Cut(part, "=")
			t = selectorTerm{key: strings.TrimSpace(k), value: strings.TrimSpace(v), op: "="}
		case strings.HasPrefix(part, "!"):
			t = selectorTerm{key: strings.TrimSpace(part[1:]), op: "!exists"}
		default:
			t = selectorTerm{key: part, op: "exists"}
		}
		if t.key == "" || strings.ContainsAny(t.key+t.value, " ()") {
			return nil, fmt.Errorf("relay: unsupported label selector term %q (only k=v, k!=v, k and !k)", part)
		}
		sel = append(sel, t)
	}
	return sel, nil
}

func (sel labelSelector) matches(labels map[string]string) bool {
	for _, t := range sel {
		v, ok := labels[t.key]
		switch t.op {
		case "=":
			if !ok || v != t.value {
				return false
			}
		case "!=":
			if ok && v == t.value {
				return false
			}
		case "exists":
			if !ok {
				return false
			}
		case "!exists":
			if ok {
				return false
			}
		}
	}
	return true
}
