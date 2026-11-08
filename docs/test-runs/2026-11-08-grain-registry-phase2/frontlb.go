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

//go:build ignore

// frontlb is the walkthrough's stand-in for the registry's load balancer: a
// TCP proxy that sends each new connection to the first backend whose health
// URL answers 200 (a draining grain-registry answers 503 on /healthz), so a
// connected daemon redials onto a live replica during a rolling drain.
//
//	go run frontlb.go -addr 127.0.0.1:4259 \
//	  -backend 127.0.0.1:4251=http://127.0.0.1:4253/healthz \
//	  -backend 127.0.0.1:4261=http://127.0.0.1:4263/healthz
package main

import (
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

type backends []string

func (b *backends) String() string     { return strings.Join(*b, ",") }
func (b *backends) Set(v string) error { *b = append(*b, v); return nil }

func main() {
	addr := flag.String("addr", "127.0.0.1:4259", "listen address")
	var bs backends
	flag.Var(&bs, "backend", "dial-addr=health-url (repeatable, in preference order)")
	flag.Parse()
	hc := &http.Client{Timeout: 300 * time.Millisecond}
	pick := func() string {
		for _, b := range bs {
			dial, health, _ := strings.Cut(b, "=")
			resp, err := hc.Get(health)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return dial
				}
			}
		}
		return ""
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("frontlb: %s -> %v", *addr, bs)
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go func(c net.Conn) {
			defer c.Close()
			target := pick()
			if target == "" {
				log.Printf("frontlb: no healthy backend")
				return
			}
			u, err := net.Dial("tcp", target)
			if err != nil {
				log.Printf("frontlb: dial %s: %v", target, err)
				return
			}
			defer u.Close()
			log.Printf("frontlb: %s -> %s", c.RemoteAddr(), target)
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(u, c); done <- struct{}{} }()
			go func() { _, _ = io.Copy(c, u); done <- struct{}{} }()
			<-done
		}(c)
	}
}
