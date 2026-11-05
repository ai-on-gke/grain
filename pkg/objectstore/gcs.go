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

package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/storage/v1"
)

// GCSStore implements Store over Google Cloud Storage under one bucket and
// optional object key prefix (for example "instances/calm-otter-7f3a/").
//
// Every call operates on storage.ObjectsService under prefix; Buckets.Get is
// never called so an identity holding roles/storage.objectUser on a Managed
// Folder alone can read, write, list, and delete its own objects.
type GCSStore struct {
	objects *storage.ObjectsService
	bucket  string
	prefix  string
}

// NewGCS constructs a GCSStore for bucket and optional prefix using
// Application Default Credentials unless overridden by opts.
func NewGCS(ctx context.Context, bucket, prefix string, opts ...option.ClientOption) (*GCSStore, error) {
	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return nil, errors.New("objectstore: GCS bucket is required")
	}
	prefix = strings.TrimPrefix(prefix, "/")
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/")
		if err := ValidatePrefix(prefix); err != nil {
			return nil, err
		}
		prefix += "/"
	}
	// Prepend the read-write scope so caller-supplied opts (such as
	// WithoutAuthentication in tests) take precedence.
	allOpts := append([]option.ClientOption{option.WithScopes(storage.DevstorageReadWriteScope)}, opts...)
	svc, err := storage.NewService(ctx, allOpts...)
	if err != nil {
		return nil, fmt.Errorf("objectstore: creating GCS client: %w", err)
	}
	return &GCSStore{
		objects: svc.Objects,
		bucket:  bucket,
		prefix:  prefix,
	}, nil
}

// Bucket returns the configured GCS bucket name.
func (g *GCSStore) Bucket() string { return g.bucket }

// Prefix returns the normalized object prefix (ending with "/" when non-empty).
func (g *GCSStore) Prefix() string { return g.prefix }

func (g *GCSStore) objectName(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	return g.prefix + key, nil
}

// Put implements Store. Overwrites existing objects in place without
// generation preconditions so periodic transcript flushes and turn-boundary
// session archive updates succeed atomically.
func (g *GCSStore) Put(ctx context.Context, key string, r io.Reader) error {
	name, err := g.objectName(key)
	if err != nil {
		return err
	}
	_, err = g.objects.Insert(g.bucket, &storage.Object{Name: name}).
		Media(r).
		Context(ctx).
		Fields("name,size,updated").
		Do()
	if err != nil {
		return fmt.Errorf("objectstore: GCS put gs://%s/%s: %w", g.bucket, name, err)
	}
	return nil
}

// Get implements Store.
func (g *GCSStore) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	name, err := g.objectName(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	meta, err := g.objects.Get(g.bucket, name).
		Context(ctx).
		Fields("name,size,updated").
		Do()
	if err != nil {
		if isGCSNotFound(err) {
			return nil, ObjectInfo{}, fmt.Errorf("objectstore: gs://%s/%s: %w", g.bucket, name, ErrNotExist)
		}
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: GCS stat gs://%s/%s: %w", g.bucket, name, err)
	}
	resp, err := g.objects.Get(g.bucket, name).Context(ctx).Download()
	if err != nil {
		if isGCSNotFound(err) {
			return nil, ObjectInfo{}, fmt.Errorf("objectstore: gs://%s/%s: %w", g.bucket, name, ErrNotExist)
		}
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: GCS download gs://%s/%s: %w", g.bucket, name, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: gs://%s/%s: %w", g.bucket, name, ErrNotExist)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: GCS download gs://%s/%s: HTTP %d: %s",
			g.bucket, name, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	info := objectInfoFromGCS(key, meta)
	if info.Size == 0 && resp.ContentLength > 0 {
		info.Size = resp.ContentLength
	}
	return resp.Body, info, nil
}

// Stat implements Store.
func (g *GCSStore) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	name, err := g.objectName(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	meta, err := g.objects.Get(g.bucket, name).
		Context(ctx).
		Fields("name,size,updated").
		Do()
	if err != nil {
		if isGCSNotFound(err) {
			return ObjectInfo{}, fmt.Errorf("objectstore: gs://%s/%s: %w", g.bucket, name, ErrNotExist)
		}
		return ObjectInfo{}, fmt.Errorf("objectstore: GCS stat gs://%s/%s: %w", g.bucket, name, err)
	}
	return objectInfoFromGCS(key, meta), nil
}

// Delete implements Store.
func (g *GCSStore) Delete(ctx context.Context, key string) error {
	name, err := g.objectName(key)
	if err != nil {
		return err
	}
	err = g.objects.Delete(g.bucket, name).Context(ctx).Do()
	if err != nil && !isGCSNotFound(err) {
		return fmt.Errorf("objectstore: GCS delete gs://%s/%s: %w", g.bucket, name, err)
	}
	return nil
}

// List implements Store. Always passes Prefix rooted under g.prefix so Managed
// Folder IAM policies on instances/<name>/ authorize the call.
func (g *GCSStore) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	if err := ValidatePrefix(prefix); err != nil {
		return nil, err
	}
	fullPrefix := g.prefix + prefix
	var (
		out       []ObjectInfo
		pageToken string
	)
	for {
		call := g.objects.List(g.bucket).
			Prefix(fullPrefix).
			Fields("nextPageToken,items(name,size,updated)").
			Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		res, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("objectstore: GCS list gs://%s/%s: %w", g.bucket, fullPrefix, err)
		}
		for _, item := range res.Items {
			if item == nil || !strings.HasPrefix(item.Name, g.prefix) {
				continue
			}
			rel := strings.TrimPrefix(item.Name, g.prefix)
			if rel == "" || strings.HasSuffix(rel, "/") {
				continue
			}
			out = append(out, objectInfoFromGCS(rel, item))
		}
		if res.NextPageToken == "" {
			break
		}
		pageToken = res.NextPageToken
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func objectInfoFromGCS(key string, obj *storage.Object) ObjectInfo {
	info := ObjectInfo{Key: key}
	if obj == nil {
		return info
	}
	info.Size = int64(obj.Size)
	if obj.Updated != "" {
		if t, err := time.Parse(time.RFC3339Nano, obj.Updated); err == nil {
			info.Updated = t.UTC()
		} else if t, err := time.Parse(time.RFC3339, obj.Updated); err == nil {
			info.Updated = t.UTC()
		}
	}
	return info
}

func isGCSNotFound(err error) bool {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound {
		return true
	}
	return false
}
