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

package gitproxy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

// DefaultSpoolThreshold is the most of a push request body held in memory
// before spilling to a temporary file on disk. Bodies at or under this
// threshold stay in memory as a byte slice.
const DefaultSpoolThreshold int64 = 16 << 20 // 16 MiB

// spooledBody is a request body held in memory or spooled to a temporary
// file on disk. It must be closed with Close() to release resources and
// deterministically remove any temporary file.
type spooledBody struct {
	mem  []byte
	file *os.File
	path string
	size int64
}

// spoolBody reads body bounded by limit (returning errBodyTooLarge if it
// exceeds limit). If the body fits within threshold bytes, it is kept in
// memory. Otherwise, the buffered start and the remainder are streamed to
// a temporary file on disk. A nil body produces a nil spooledBody.
func spoolBody(body io.Reader, limit, threshold int64) (*spooledBody, error) {
	if body == nil {
		return nil, nil
	}
	if threshold <= 0 {
		threshold = DefaultSpoolThreshold
	}

	// Read up to threshold to see if it fits entirely in memory.
	buf := make([]byte, 0, minInt64(threshold, 64<<10))
	limitReader := io.LimitReader(body, limit+1)

	chunk := make([]byte, 32<<10)
	for int64(len(buf)) < threshold {
		toRead := int64(len(chunk))
		if remaining := threshold - int64(len(buf)); remaining < toRead {
			toRead = remaining
		}
		n, err := limitReader.Read(chunk[:toRead])
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if errors.Is(err, io.EOF) {
			return &spooledBody{mem: buf, size: int64(len(buf))}, nil
		}
		if err != nil {
			return nil, err
		}
	}

	// Try reading one more byte to determine if threshold was the exact EOF.
	n, err := limitReader.Read(chunk[:1])
	if n == 0 || errors.Is(err, io.EOF) {
		return &spooledBody{mem: buf, size: int64(len(buf))}, nil
	}
	if err != nil {
		return nil, err
	}
	firstExtraByte := chunk[0]

	// The body exceeds threshold. Spool to a temporary file.
	tmp, err := os.CreateTemp("", "grain-gitproxy-push-*")
	if err != nil {
		return nil, fmt.Errorf("creating spool temp file: %w", err)
	}
	path := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(path)
		}
	}()

	if _, err := tmp.Write(buf); err != nil {
		return nil, fmt.Errorf("writing to spool temp file: %w", err)
	}
	if _, err := tmp.Write([]byte{firstExtraByte}); err != nil {
		return nil, fmt.Errorf("writing to spool temp file: %w", err)
	}

	totalWritten := int64(len(buf)) + 1
	written, err := io.Copy(tmp, limitReader)
	if err != nil {
		return nil, fmt.Errorf("spooling request body to disk: %w", err)
	}
	totalWritten += written

	if totalWritten > limit {
		return nil, errBodyTooLarge
	}

	// Rewind the file descriptor so subsequent reads can read from start if needed,
	// or NewReader can seek.
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seeking spool temp file: %w", err)
	}

	cleanup = false
	return &spooledBody{
		file: tmp,
		path: path,
		size: totalWritten,
	}, nil
}

// Len returns the total size of the body in bytes.
func (s *spooledBody) Len() int64 {
	if s == nil {
		return 0
	}
	return s.size
}

// Bytes returns the body as a byte slice. If spooled to disk, it reads the
// file from offset 0.
func (s *spooledBody) Bytes() ([]byte, error) {
	if s == nil {
		return nil, nil
	}
	if s.mem != nil {
		return s.mem, nil
	}
	data := make([]byte, s.size)
	if _, err := s.file.ReadAt(data, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reading spooled body: %w", err)
	}
	return data, nil
}

// Head returns up to n initial bytes of the body. If the body is shorter,
// all bytes are returned. Does not load the full file into memory.
func (s *spooledBody) Head(n int) ([]byte, error) {
	if s == nil {
		return nil, nil
	}
	if s.mem != nil {
		if len(s.mem) <= n {
			return s.mem, nil
		}
		return s.mem[:n], nil
	}
	toRead := n
	if int64(toRead) > s.size {
		toRead = int(s.size)
	}
	buf := make([]byte, toRead)
	readN, err := s.file.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reading head of spooled body: %w", err)
	}
	return buf[:readN], nil
}

// NewReader returns an io.ReadSeeker for reading the body from the beginning.
func (s *spooledBody) NewReader() (io.ReadSeeker, error) {
	if s == nil {
		return bytes.NewReader(nil), nil
	}
	if s.mem != nil {
		return bytes.NewReader(s.mem), nil
	}
	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("opening spooled body for reading: %w", err)
	}
	return f, nil
}

// reader returns a reader over the body from its start: the bytes in
// memory, or the spool file read in place -- through ReadAt, so any number
// of readers may be open on it at once and none needs closing. A nil body
// reads as empty.
func (s *spooledBody) reader() io.Reader {
	if s == nil {
		return bytes.NewReader(nil)
	}
	if s.file == nil {
		return bytes.NewReader(s.mem)
	}
	return io.NewSectionReader(s.file, 0, s.size)
}

// Close closes and removes any underlying temporary file.
func (s *spooledBody) Close() error {
	if s == nil || s.path == "" {
		return nil
	}
	var closeErr error
	if s.file != nil {
		closeErr = s.file.Close()
		s.file = nil
	}
	removeErr := os.Remove(s.path)
	s.path = ""
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
