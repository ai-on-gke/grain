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

func TestGoogleDriveUploadsToTheConfiguredFolder(t *testing.T) {
	var metadata struct {
		Name     string   `json:"name"`
		MimeType string   `json:"mimeType"`
		Parents  []string `json:"parents"`
	}
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/upload/drive/v3/files") {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("supportsAllDrives") != "true" {
			t.Errorf("supportsAllDrives = %q", r.URL.Query().Get("supportsAllDrives"))
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
		io.WriteString(w, `{"id":"drive-id","webViewLink":"https://drive.example/drive-id"}`)
	}))
	defer server.Close()

	drive, err := NewGoogleDrive(context.Background(), "folder-id", "",
		option.WithEndpoint(server.URL+"/"),
		option.WithHTTPClient(server.Client()),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := drive.Upload(context.Background(), "backup.zip", "application/zip", bytes.NewBufferString("archive"))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Name != "backup.zip" || metadata.MimeType != "application/zip" ||
		len(metadata.Parents) != 1 || metadata.Parents[0] != "folder-id" {
		t.Fatalf("metadata = %+v", metadata)
	}
	if string(uploaded) != "archive" {
		t.Fatalf("uploaded = %q", uploaded)
	}
	if got.ID != "drive-id" || got.URL != "https://drive.example/drive-id" {
		t.Fatalf("remote = %+v", got)
	}
}
