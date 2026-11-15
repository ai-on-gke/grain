//go:build ignore

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

// gcsstub is a tiny GCS JSON API stand-in for the 2026-11-07 §2
// walkthrough: objects.insert (multipart, media and resumable uploads),
// objects.get (metadata and alt=media), objects.list and objects.delete.
// GET /_reset?bucket=B empties a bucket and GET /_fail?name=S makes
// every upload whose object name contains S fail with a 503 (empty S
// stops). Every stored object is mirrored byte-for-byte under -dir/<bucket>/<name>
// so a walkthrough can grep exactly what crossed the wire, and every
// request is logged to stdout.
//
//	go run gcsstub.go -addr 127.0.0.1:4293 -dir /tmp/walk/gcs
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type object struct {
	body    []byte
	gen     int64
	updated time.Time
	ctype   string
}

var (
	mu      sync.Mutex
	objects = map[string]map[string]*object{} // bucket -> name -> object
	uploads = map[string]*pending{}
	nextGen int64
	dir     string
	failPut string // object-name substring whose uploads get a 503
)

type pending struct {
	bucket, name, ctype string
	ifGen               string
	buf                 bytes.Buffer
}

func meta(bucket, name string, o *object) map[string]any {
	return map[string]any{
		"kind": "storage#object", "bucket": bucket, "name": name,
		"size": strconv.Itoa(len(o.body)), "generation": strconv.FormatInt(o.gen, 10),
		"updated": o.updated.Format(time.RFC3339Nano), "contentType": o.ctype,
		"mediaLink": "http://" + flagAddr + "/download/storage/v1/b/" + bucket + "/o/" + url.PathEscape(name) + "?alt=media",
	}
}

var flagAddr string

func store(w http.ResponseWriter, bucket, name, ctype, ifGen string, body []byte) {
	mu.Lock()
	defer mu.Unlock()
	if failPut != "" && strings.Contains(name, failPut) {
		http.Error(w, `{"error":{"code":503,"message":"injected failure"}}`, http.StatusServiceUnavailable)
		return
	}
	if objects[bucket] == nil {
		objects[bucket] = map[string]*object{}
	}
	if ifGen == "0" && objects[bucket][name] != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPreconditionFailed)
		io.WriteString(w, `{"error":{"code":412,"message":"conditionNotMet"}}`)
		return
	}
	nextGen++
	o := &object{body: body, gen: nextGen, updated: time.Now().UTC(), ctype: ctype}
	objects[bucket][name] = o
	if dir != "" {
		p := filepath.Join(dir, bucket, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, body, 0o644)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(meta(bucket, name, o))
}

func handler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	q := r.URL.Query()
	log.Printf("%s %s?%s", r.Method, path, r.URL.RawQuery)
	switch {
	case r.Method == http.MethodPut && strings.HasPrefix(path, "/upload/resumable/"):
		id := strings.TrimPrefix(path, "/upload/resumable/")
		mu.Lock()
		up := uploads[id]
		mu.Unlock()
		if up == nil {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		up.buf.Write(b)
		// Content-Range: bytes a-b/total, or bytes */total, or bytes a-b/*
		cr := r.Header.Get("Content-Range")
		if strings.HasSuffix(cr, "/*") {
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", up.buf.Len()-1))
			w.WriteHeader(308)
			return
		}
		mu.Lock()
		delete(uploads, id)
		mu.Unlock()
		store(w, up.bucket, up.name, up.ctype, up.ifGen, up.buf.Bytes())
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/upload/storage/v1/b/"):
		bucket := strings.TrimSuffix(strings.TrimPrefix(path, "/upload/storage/v1/b/"), "/o")
		var m struct {
			Name        string `json:"name"`
			ContentType string `json:"contentType"`
		}
		var body []byte
		ct, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		switch q.Get("uploadType") {
		case "multipart":
			mr := multipart.NewReader(r.Body, params["boundary"])
			p1, err := mr.NextPart()
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			mb, _ := io.ReadAll(p1)
			_ = json.Unmarshal(mb, &m)
			p2, err := mr.NextPart()
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if m.ContentType == "" {
				m.ContentType = p2.Header.Get("Content-Type")
			}
			body, _ = io.ReadAll(p2)
		case "resumable":
			mb, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(mb, &m)
			if m.Name == "" {
				m.Name = q.Get("name")
			}
			mu.Lock()
			nextGen++
			id := strconv.FormatInt(nextGen, 10)
			uploads[id] = &pending{bucket: bucket, name: m.Name, ctype: r.Header.Get("X-Upload-Content-Type"), ifGen: q.Get("ifGenerationMatch")}
			mu.Unlock()
			w.Header().Set("Location", "http://"+r.Host+"/upload/resumable/"+id)
			w.WriteHeader(200)
			return
		default:
			m.Name = q.Get("name")
			m.ContentType = ct
			body, _ = io.ReadAll(r.Body)
		}
		if m.Name == "" {
			m.Name = q.Get("name")
		}
		store(w, bucket, m.Name, m.ContentType, q.Get("ifGenerationMatch"), body)
	case strings.HasPrefix(path, "/storage/v1/b/") || strings.HasPrefix(path, "/download/storage/v1/b/"):
		rest := strings.TrimPrefix(strings.TrimPrefix(path, "/download"), "/storage/v1/b/")
		bucket, objPart, _ := strings.Cut(rest, "/o")
		name, _ := url.PathUnescape(strings.TrimPrefix(objPart, "/"))
		mu.Lock()
		defer mu.Unlock()
		if name == "" && r.Method == http.MethodGet {
			prefix := q.Get("prefix")
			var names []string
			for n := range objects[bucket] {
				if strings.HasPrefix(n, prefix) {
					names = append(names, n)
				}
			}
			sort.Strings(names)
			items := []map[string]any{}
			for _, n := range names {
				items = append(items, meta(bucket, n, objects[bucket][n]))
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"kind": "storage#objects", "items": items})
			return
		}
		o := objects[bucket][name]
		if o == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":404,"message":"No such object"}}`)
			return
		}
		switch r.Method {
		case http.MethodDelete:
			delete(objects[bucket], name)
			if dir != "" {
				_ = os.Remove(filepath.Join(dir, bucket, filepath.FromSlash(name)))
			}
			w.WriteHeader(204)
		case http.MethodGet:
			if q.Get("alt") == "media" {
				w.Header().Set("Content-Type", o.ctype)
				w.Header().Set("Content-Length", strconv.Itoa(len(o.body)))
				w.Write(o.body)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(meta(bucket, name, o))
		default:
			http.Error(w, "unsupported", 405)
		}
	case path == "/_reset":
		mu.Lock()
		delete(objects, q.Get("bucket"))
		if dir != "" && q.Get("bucket") != "" {
			_ = os.RemoveAll(filepath.Join(dir, filepath.Base(q.Get("bucket"))))
		}
		mu.Unlock()
		fmt.Fprintf(w, "bucket %q emptied\n", q.Get("bucket"))
	case path == "/_fail":
		mu.Lock()
		failPut = q.Get("name")
		mu.Unlock()
		fmt.Fprintf(w, "failing uploads matching %q\n", failPut)
	default:
		http.NotFound(w, r)
	}
}

func main() {
	flag.StringVar(&flagAddr, "addr", "127.0.0.1:4293", "listen address")
	flag.StringVar(&dir, "dir", "", "mirror stored objects under this directory")
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Printf("gcsstub listening on %s (mirror %q)", flagAddr, dir)
	log.Fatal(http.ListenAndServe(flagAddr, http.HandlerFunc(handler)))
}
