// Copyright 2026 The grain Authors. All rights reserved.
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

package server

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultConnectPerEntryRate is 12 handshakes/min (0.2/s), burst 6.
	DefaultConnectPerEntryRate  = 12.0 / 60.0
	DefaultConnectPerEntryBurst = 6

	// DefaultPublicPerIPRate is 30 attempts/min (0.5/s), burst 10.
	DefaultPublicPerIPRate  = 30.0 / 60.0
	DefaultPublicPerIPBurst = 10

	// DefaultFailedAuthPerIPRate is 5 failed attempts/min, burst 5.
	DefaultFailedAuthPerIPRate  = 5.0 / 60.0
	DefaultFailedAuthPerIPBurst = 5

	// DefaultProxyPerEntryRate is 100 req/s sustained, burst 200.
	DefaultProxyPerEntryRate  = 100.0
	DefaultProxyPerEntryBurst = 200

	// DefaultHeartbeatPerEntryRate is 12 heartbeats/min (0.2/s), burst 4.
	DefaultHeartbeatPerEntryRate  = 12.0 / 60.0
	DefaultHeartbeatPerEntryBurst = 4

	// DefaultMutationsPerUserRate is 10 mutations/min, burst 10.
	DefaultMutationsPerUserRate  = 10.0 / 60.0
	DefaultMutationsPerUserBurst = 10

	// DefaultCreatesPerUserRate is 20 creates/day, burst 20.
	DefaultCreatesPerUserRate  = 20.0 / 86400.0
	DefaultCreatesPerUserBurst = 20
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// KeyedLimiter manages per-key token buckets with a fixed refill rate (tokens/sec) and burst.
type KeyedLimiter struct {
	rate  float64
	burst int
	nowFn func() time.Time

	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

// NewKeyedLimiter creates a new KeyedLimiter.
func NewKeyedLimiter(ratePerSec float64, burst int) *KeyedLimiter {
	if ratePerSec <= 0 {
		ratePerSec = 1
	}
	if burst <= 0 {
		burst = 1
	}
	return &KeyedLimiter{
		rate:    ratePerSec,
		burst:   burst,
		nowFn:   time.Now,
		buckets: make(map[string]*tokenBucket),
	}
}

// SetNow overrides the clock used by KeyedLimiter (for deterministic tests).
func (l *KeyedLimiter) SetNow(fn func() time.Time) {
	if fn != nil {
		l.mu.Lock()
		l.nowFn = fn
		l.mu.Unlock()
	}
}

// Allow checks whether `key` has at least 1 token available and consumes it if so.
// When denied, retryAfterSeconds (>= 1) indicates when 1 token will next be available.
func (l *KeyedLimiter) Allow(key string) (allowed bool, retryAfterSeconds int) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.nowFn()
	b, ok := l.buckets[key]
	if !ok {
		b = &tokenBucket{
			tokens: float64(l.burst),
			last:   now,
		}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * l.rate
			if b.tokens > float64(l.burst) {
				b.tokens = float64(l.burst)
			}
			b.last = now
		}
	}

	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true, 0
	}

	deficit := 1.0 - b.tokens
	waitSec := int(math.Ceil(deficit / l.rate))
	if waitSec < 1 {
		waitSec = 1
	}
	return false, waitSec
}

// RateLimiters groups the registry's connect, heartbeat, auth-failure, mutation, and proxy limiters.
type RateLimiters struct {
	ConnectPerEntry   *KeyedLimiter
	HeartbeatPerEntry *KeyedLimiter
	PublicPerIP       *KeyedLimiter
	FailedAuthPerIP   *KeyedLimiter
	ProxyPerEntry     *KeyedLimiter
	MutationsPerUser  *KeyedLimiter
	CreatesPerUser    *KeyedLimiter
}

// NewDefaultRateLimiters constructs the default v1 registry rate limiters.
func NewDefaultRateLimiters() *RateLimiters {
	return &RateLimiters{
		ConnectPerEntry:   NewKeyedLimiter(DefaultConnectPerEntryRate, DefaultConnectPerEntryBurst),
		HeartbeatPerEntry: NewKeyedLimiter(DefaultHeartbeatPerEntryRate, DefaultHeartbeatPerEntryBurst),
		PublicPerIP:       NewKeyedLimiter(DefaultPublicPerIPRate, DefaultPublicPerIPBurst),
		FailedAuthPerIP:   NewKeyedLimiter(DefaultFailedAuthPerIPRate, DefaultFailedAuthPerIPBurst),
		ProxyPerEntry:     NewKeyedLimiter(DefaultProxyPerEntryRate, DefaultProxyPerEntryBurst),
		MutationsPerUser:  NewKeyedLimiter(DefaultMutationsPerUserRate, DefaultMutationsPerUserBurst),
		CreatesPerUser:    NewKeyedLimiter(DefaultCreatesPerUserRate, DefaultCreatesPerUserBurst),
	}
}

// SetNow overrides the clock across all rate limiters.
func (r *RateLimiters) SetNow(fn func() time.Time) {
	if r == nil || fn == nil {
		return
	}
	r.ConnectPerEntry.SetNow(fn)
	r.HeartbeatPerEntry.SetNow(fn)
	r.PublicPerIP.SetNow(fn)
	r.FailedAuthPerIP.SetNow(fn)
	r.ProxyPerEntry.SetNow(fn)
	r.MutationsPerUser.SetNow(fn)
	r.CreatesPerUser.SetNow(fn)
}

// ClientIP extracts the normalized source IP from `r.RemoteAddr`.
func ClientIP(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	addr := strings.TrimSpace(r.RemoteAddr)
	if addr == "" {
		return "unknown"
	}
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "" {
		return host
	}
	return addr
}

// WriteRateLimited writes a 429 Too Many Requests response with `Retry-After`.
func WriteRateLimited(w http.ResponseWriter, retryAfterSec int, msg string) {
	if retryAfterSec < 1 {
		retryAfterSec = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSec))
	w.Header().Set("Cache-Control", "no-store")
	if msg == "" {
		msg = "rate limit exceeded; retry later"
	}
	writeJSONError(w, http.StatusTooManyRequests, "rate_limited", msg)
}
