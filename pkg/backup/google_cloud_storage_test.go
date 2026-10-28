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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/api/option"
)

func TestGoogleCloudStorageUploadsToTheConfiguredBucket(t *testing.T) {
	var metadata struct {
		Name        string `json:"name"`
		ContentType string `json:"contentType"`
	}
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/upload/storage/v1/b/backup-bucket/o" {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("uploadType") != "multipart" {
			t.Errorf("uploadType = %q", r.URL.Query().Get("uploadType"))
		}
		if r.URL.Query().Get("ifGenerationMatch") != "0" {
			t.Errorf("ifGenerationMatch = %q", r.URL.Query().Get("ifGenerationMatch"))
		}
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		parts := multipart.NewReader(r.Body, params["boundary"])
		metaPart, err := parts.NextPart()
		if err != nil {
			t.Error(err)
			return
		}
		if err := json.NewDecoder(metaPart).Decode(&metadata); err != nil {
			t.Error(err)
			return
		}
		mediaPart, err := parts.NextPart()
		if err != nil {
			t.Error(err)
			return
		}
		uploaded, err = io.ReadAll(mediaPart)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"bucket":"backup-bucket","name":"backup.zip","mediaLink":"https://storage.example/backup.zip"}`)
	}))
	defer server.Close()

	gcs, err := NewGoogleCloudStorage(context.Background(), "backup-bucket", "",
		option.WithEndpoint(server.URL+"/storage/v1/"),
		option.WithHTTPClient(server.Client()),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := gcs.Upload(context.Background(), "backup.zip", "application/zip", bytes.NewBufferString("archive"))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Name != "backup.zip" || metadata.ContentType != "application/zip" {
		t.Fatalf("metadata = %+v", metadata)
	}
	if string(uploaded) != "archive" {
		t.Fatalf("uploaded = %q", uploaded)
	}
	if got.ID != "gs://backup-bucket/backup.zip" || got.URL != "https://storage.example/backup.zip" {
		t.Fatalf("remote = %+v", got)
	}
}

func TestNewGoogleCloudStorageRequiresBucket(t *testing.T) {
	_, err := NewGoogleCloudStorage(context.Background(), "  ", "",
		option.WithoutAuthentication(),
	)
	if err == nil || !strings.Contains(err.Error(), "GCS bucket is required") {
		t.Fatalf("error = %v", err)
	}
}
