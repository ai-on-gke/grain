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

package backup

import (
	"context"
	"fmt"
	"io"
	"strings"

	"google.golang.org/api/option"
	"google.golang.org/api/storage/v1"
)

// GoogleCloudStorage stores backups as objects in one GCS bucket. The
// configured identity must be allowed to create objects in the bucket.
type GoogleCloudStorage struct {
	objects *storage.ObjectsService
	bucket  string
}

// NewGoogleCloudStorage uses Application Default Credentials unless
// credentialsFile names a service-account or OAuth credential JSON file. The
// credential is consumed by the Google client and is never passed to the
// archive builder.
func NewGoogleCloudStorage(ctx context.Context, bucket, credentialsFile string, opts ...option.ClientOption) (*GoogleCloudStorage, error) {
	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return nil, fmt.Errorf("backup: GCS bucket is required")
	}
	opts = append(opts, option.WithScopes(storage.DevstorageReadWriteScope))
	if credentialsFile != "" {
		opts = append(opts, option.WithCredentialsFile(credentialsFile))
	}
	service, err := storage.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("backup: creating GCS client: %w", err)
	}
	return &GoogleCloudStorage{objects: service.Objects, bucket: bucket}, nil
}

// Upload implements Destination.
func (d *GoogleCloudStorage) Upload(ctx context.Context, name, mediaType string, content io.Reader) (Remote, error) {
	if d == nil || d.objects == nil || d.bucket == "" {
		return Remote{}, fmt.Errorf("backup: GCS is not configured")
	}
	stored, err := d.objects.Insert(d.bucket, &storage.Object{
		Name:        name,
		ContentType: mediaType,
	}).
		IfGenerationMatch(0).
		Media(content).
		Context(ctx).
		Fields("bucket,name,mediaLink").
		Do()
	if err != nil {
		return Remote{}, fmt.Errorf("backup: GCS: creating gs://%s/%s: %w", d.bucket, name, err)
	}
	bucket := stored.Bucket
	if bucket == "" {
		bucket = d.bucket
	}
	object := stored.Name
	if object == "" {
		object = name
	}
	return Remote{ID: "gs://" + bucket + "/" + object, URL: stored.MediaLink}, nil
}
