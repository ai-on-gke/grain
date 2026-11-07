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

// Package objectstore provides a minimal prefix-scoped object store backed by
// Google Cloud Storage (gs://), a local directory (file://), or an in-memory
// fake (MemoryStore) for run transcripts, CI artifacts, and durable
// agent-session archives.
//
// In hosted deployments each instance's service account holds
// roles/storage.objectUser on a single Managed Folder (instances/<name>/)
// rather than bucket-wide permissions, so the GCS implementation operates
// strictly on object endpoints under its configured prefix and never inspects
// bucket metadata.
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/api/option"
)

// ErrNotExist is returned by Get and Stat when an object does not exist.
var ErrNotExist = fs.ErrNotExist

// ObjectInfo describes one stored object relative to the Store's prefix.
type ObjectInfo struct {
	Key     string
	Size    int64
	Updated time.Time
}

// Store reads and writes named byte blobs under a logical key namespace.
// Keys use forward-slash ("/") separators and never start with "/".
type Store interface {
	// Put creates or overwrites key with the contents of r.
	Put(ctx context.Context, key string, r io.Reader) error
	// Get opens key for reading alongside its metadata, or returns ErrNotExist.
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	// Stat returns key's metadata without downloading its body, or ErrNotExist.
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	// Delete removes key. Deleting a key that does not exist returns nil.
	Delete(ctx context.Context, key string) error
	// List returns every object whose key starts with prefix, sorted by Key.
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
}

// ValidateKey checks that key is a clean, non-empty relative "/" path with no
// empty, "." or ".." segments, backslashes, or NUL bytes.
func ValidateKey(key string) error {
	if key == "" {
		return errors.New("objectstore: object key is empty")
	}
	if strings.HasSuffix(key, "/") {
		return fmt.Errorf("objectstore: object key %q must not end with '/'", key)
	}
	return validateSegments(key, false)
}

// ValidatePrefix checks a prefix passed to List or WithPrefix. Empty is valid;
// non-empty prefixes may end with "/" and must contain no ".", "..", empty
// segments, backslashes, leading "/", or NUL bytes.
func ValidatePrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	trimmed := strings.TrimSuffix(prefix, "/")
	if trimmed == "" {
		return fmt.Errorf("objectstore: invalid prefix %q", prefix)
	}
	return validateSegments(trimmed, true)
}

func validateSegments(s string, isPrefix bool) error {
	kind := "key"
	if isPrefix {
		kind = "prefix"
	}
	if strings.HasPrefix(s, "/") {
		return fmt.Errorf("objectstore: object %s %q must not start with '/'", kind, s)
	}
	if strings.ContainsRune(s, '\\') || strings.ContainsRune(s, 0) {
		return fmt.Errorf("objectstore: object %s %q contains invalid characters", kind, s)
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("objectstore: object %s %q contains invalid path segment %q", kind, s, seg)
		}
	}
	return nil
}

// ParsedURI is the validated decomposition of an -object-store URI.
type ParsedURI struct {
	Scheme string
	Bucket string // for gs://
	Prefix string // normalized with trailing "/" when non-empty, for gs://
	Dir    string // clean absolute path, for file://
}

// String formats u back as a canonical URI.
func (u ParsedURI) String() string {
	switch u.Scheme {
	case "gs":
		return "gs://" + u.Bucket + "/" + u.Prefix
	case "file":
		return "file://" + u.Dir
	default:
		return ""
	}
}

// ParseURI parses and validates an object store URI ("gs://<bucket>[/<prefix>]"
// or "file://<abs-dir>").
func ParseURI(raw string) (ParsedURI, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ParsedURI{}, errors.New("objectstore: URI is empty")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return ParsedURI{}, fmt.Errorf("objectstore: invalid URI %q: %w", raw, err)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return ParsedURI{}, fmt.Errorf("objectstore: URI %q must not include query or fragment", raw)
	}
	if u.User != nil {
		return ParsedURI{}, fmt.Errorf("objectstore: URI %q must not include userinfo", raw)
	}
	switch u.Scheme {
	case "gs":
		bucket := strings.TrimSpace(u.Host)
		if bucket == "" {
			return ParsedURI{}, fmt.Errorf("objectstore: gs:// URI %q is missing bucket name", raw)
		}
		if strings.ContainsAny(bucket, "/\\ \t") {
			return ParsedURI{}, fmt.Errorf("objectstore: gs:// URI %q has invalid bucket %q", raw, bucket)
		}
		prefix := strings.TrimPrefix(u.Path, "/")
		prefix = strings.TrimSuffix(prefix, "/")
		if prefix != "" {
			if err := ValidatePrefix(prefix); err != nil {
				return ParsedURI{}, fmt.Errorf("objectstore: invalid gs:// prefix in %q: %w", raw, err)
			}
			prefix += "/"
		}
		return ParsedURI{
			Scheme: "gs",
			Bucket: bucket,
			Prefix: prefix,
		}, nil
	case "file":
		if u.Host != "" && u.Host != "localhost" {
			return ParsedURI{}, fmt.Errorf("objectstore: file:// URI %q must have an empty host (use file:///abs/path)", raw)
		}
		if u.Path == "" || !filepath.IsAbs(u.Path) {
			return ParsedURI{}, fmt.Errorf("objectstore: file:// URI %q must name an absolute directory path", raw)
		}
		for _, seg := range strings.Split(u.Path, "/") {
			if seg == ".." {
				return ParsedURI{}, fmt.Errorf("objectstore: file:// URI %q must not contain '..'", raw)
			}
		}
		return ParsedURI{
			Scheme: "file",
			Dir:    filepath.Clean(u.Path),
		}, nil
	default:
		return ParsedURI{}, fmt.Errorf("objectstore: unsupported URI scheme %q in %q (expected gs:// or file://)", u.Scheme, raw)
	}
}

// ValidateURI reports whether raw is a well-formed -object-store URI.
func ValidateURI(raw string) error {
	_, err := ParseURI(raw)
	return err
}

// Option configures Open.
type Option func(*openOptions)

type openOptions struct {
	clientOpts []option.ClientOption
}

// WithClientOptions appends Google API client options when opening a gs:// store.
func WithClientOptions(opts ...option.ClientOption) Option {
	return func(o *openOptions) {
		o.clientOpts = append(o.clientOpts, opts...)
	}
}

// GCSEndpointEnv overrides the GCS API endpoint (with authentication disabled)
// when set in test environments.
const GCSEndpointEnv = "GRAIN_OBJECT_STORE_GCS_ENDPOINT"

// Open opens the Store named by uri ("gs://<bucket>[/<prefix>]" or
// "file://<abs-dir>").
func Open(ctx context.Context, uri string, opts ...Option) (Store, error) {
	parsed, err := ParseURI(uri)
	if err != nil {
		return nil, err
	}
	var cfg openOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	switch parsed.Scheme {
	case "gs":
		clientOpts := cfg.clientOpts
		if ep := strings.TrimSpace(os.Getenv(GCSEndpointEnv)); ep != "" {
			clientOpts = append([]option.ClientOption{
				option.WithEndpoint(ep),
				option.WithoutAuthentication(),
			}, clientOpts...)
		}
		return NewGCS(ctx, parsed.Bucket, parsed.Prefix, clientOpts...)
	case "file":
		return NewDir(parsed.Dir)
	default:
		return nil, fmt.Errorf("objectstore: unsupported scheme %q", parsed.Scheme)
	}
}

// WithPrefix returns a view of s rooted at prefix (e.g. "transcripts/").
// If s is nil, WithPrefix returns nil. Panics if prefix fails ValidatePrefix.
func WithPrefix(s Store, prefix string) Store {
	if s == nil {
		return nil
	}
	if err := ValidatePrefix(prefix); err != nil {
		panic(err)
	}
	normalized := strings.TrimSuffix(prefix, "/")
	if normalized == "" {
		return s
	}
	return &prefixStore{
		inner:  s,
		prefix: normalized + "/",
	}
}

type prefixStore struct {
	inner  Store
	prefix string
}

func (p *prefixStore) fullKey(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	return p.prefix + key, nil
}

func (p *prefixStore) Put(ctx context.Context, key string, r io.Reader) error {
	fk, err := p.fullKey(key)
	if err != nil {
		return err
	}
	return p.inner.Put(ctx, fk, r)
}

func (p *prefixStore) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	fk, err := p.fullKey(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	rc, info, err := p.inner.Get(ctx, fk)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	info.Key = strings.TrimPrefix(info.Key, p.prefix)
	return rc, info, nil
}

func (p *prefixStore) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	fk, err := p.fullKey(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	info, err := p.inner.Stat(ctx, fk)
	if err != nil {
		return ObjectInfo{}, err
	}
	info.Key = strings.TrimPrefix(info.Key, p.prefix)
	return info, nil
}

func (p *prefixStore) Delete(ctx context.Context, key string) error {
	fk, err := p.fullKey(key)
	if err != nil {
		return err
	}
	return p.inner.Delete(ctx, fk)
}

func (p *prefixStore) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	if err := ValidatePrefix(prefix); err != nil {
		return nil, err
	}
	items, err := p.inner.List(ctx, p.prefix+prefix)
	if err != nil {
		return nil, err
	}
	out := make([]ObjectInfo, 0, len(items))
	for _, it := range items {
		if !strings.HasPrefix(it.Key, p.prefix) {
			continue
		}
		it.Key = strings.TrimPrefix(it.Key, p.prefix)
		out = append(out, it)
	}
	return out, nil
}
