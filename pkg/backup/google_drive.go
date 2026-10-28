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

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// GoogleDrive stores backups in one Google Drive folder. FolderID may be
// "root". The identity must have write access to that folder.
type GoogleDrive struct {
	files    *drive.FilesService
	folderID string
}

// NewGoogleDrive uses Application Default Credentials unless credentialsFile
// names a service-account or OAuth credential JSON file. The credential is
// consumed by the Google client and is never passed to the archive builder.
func NewGoogleDrive(ctx context.Context, folderID, credentialsFile string, opts ...option.ClientOption) (*GoogleDrive, error) {
	opts = append(opts, option.WithScopes(drive.DriveFileScope))
	if credentialsFile != "" {
		opts = append(opts, option.WithCredentialsFile(credentialsFile))
	}
	service, err := drive.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("backup: creating Google Drive client: %w", err)
	}
	return &GoogleDrive{files: service.Files, folderID: folderID}, nil
}

// Upload implements Destination.
func (d *GoogleDrive) Upload(ctx context.Context, name, mediaType string, content io.Reader) (Remote, error) {
	if d == nil || d.files == nil {
		return Remote{}, fmt.Errorf("backup: Google Drive is not configured")
	}
	file := &drive.File{Name: name, MimeType: mediaType}
	if d.folderID != "" {
		file.Parents = []string{d.folderID}
	}
	stored, err := d.files.Create(file).
		SupportsAllDrives(true).
		Media(content).
		Context(ctx).
		Fields("id,webViewLink").
		Do()
	if err != nil {
		return Remote{}, fmt.Errorf("backup: Google Drive: creating %s: %w", name, err)
	}
	return Remote{ID: stored.Id, URL: stored.WebViewLink}, nil
}
