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

package expstats

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mathrand "math/rand/v2"
)

type ArmWeight struct {
	ID     string
	Weight int
}

func GenerateSalt() (string, error) {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func HashBucket(salt, rootOrigin string) int {
	h := sha256.Sum256([]byte(salt + ":" + rootOrigin))
	return int(binary.BigEndian.Uint32(h[0:4]) % 10000)
}

func AllocateHashArm(salt, rootOrigin string, arms []ArmWeight) (armID string, bucket int) {
	bucket = HashBucket(salt, rootOrigin)
	var cum int
	for _, a := range arms {
		cum += a.Weight
		if bucket < cum {
			return a.ID, bucket
		}
	}
	if len(arms) > 0 {
		return arms[0].ID, bucket
	}
	return "", bucket
}

type ExplicitEntry struct {
	Key     string `json:"key"`
	ArmID   string `json:"arm_id"`
	Stratum string `json:"stratum"`
	PairID  string `json:"pair_id"`
}

func GenerateExplicitList(design string, armIDs []string, nPairsOrTotal int, strata []string, seed int64) (entries []ExplicitEntry, listHash string, err error) {
	rng := mathrand.New(mathrand.NewPCG(uint64(seed), 1))

	keyIdx := 1
	nextKey := func() string {
		k := fmt.Sprintf("alloc-%04d", keyIdx)
		keyIdx++
		return k
	}

	if design == "paired" {
		for i := 0; i < nPairsOrTotal; i++ {
			pairID := fmt.Sprintf("pair-%03d", i+1)
			shuffledArms := make([]string, len(armIDs))
			copy(shuffledArms, armIDs)
			rng.Shuffle(len(shuffledArms), func(i, j int) {
				shuffledArms[i], shuffledArms[j] = shuffledArms[j], shuffledArms[i]
			})
			for _, arm := range shuffledArms {
				entries = append(entries, ExplicitEntry{
					Key:    nextKey(),
					ArmID:  arm,
					PairID: pairID,
				})
			}
		}
	} else if design == "blocked" {
		if len(strata) == 0 {
			strata = []string{""}
		}
		blockSize := 2 * len(armIDs)
		if blockSize == 0 {
			blockSize = 1
		}
		entriesPerStrat := nPairsOrTotal / len(strata)
		for _, strat := range strata {
			stratEntries := 0
			for stratEntries < entriesPerStrat {
				block := make([]string, 0, blockSize)
				for i := 0; i < 2; i++ {
					for _, arm := range armIDs {
						block = append(block, arm)
					}
				}
				rng.Shuffle(len(block), func(i, j int) {
					block[i], block[j] = block[j], block[i]
				})

				for _, arm := range block {
					if stratEntries >= entriesPerStrat {
						break
					}
					entries = append(entries, ExplicitEntry{
						Key:     nextKey(),
						ArmID:   arm,
						Stratum: strat,
					})
					stratEntries++
				}
			}
		}
	}

	b, err := json.Marshal(entries)
	if err != nil {
		return nil, "", err
	}
	listHash = fmt.Sprintf("%x", sha256.Sum256(b))
	return entries, listHash, nil
}

type AuditMismatch struct {
	RootOrigin string
	Expected   string
	Actual     string
}

type AuditReport struct {
	Passed       bool
	AgreementPct float64
	Total        int
	Mismatches   []AuditMismatch
}

type Enrollment struct {
	RootOrigin string
	Key        string
	ArmID      string
	Excluded   bool
}

func AuditAssignments(mode string, salt string, weights []ArmWeight, entries []ExplicitEntry, enrollments []Enrollment) AuditReport {
	var mismatches []AuditMismatch
	var total int

	if mode == "hash" {
		for _, e := range enrollments {
			if e.Excluded {
				continue
			}
			total++
			expected, _ := AllocateHashArm(salt, e.RootOrigin, weights)
			if expected != e.ArmID {
				mismatches = append(mismatches, AuditMismatch{
					RootOrigin: e.RootOrigin,
					Expected:   expected,
					Actual:     e.ArmID,
				})
			}
		}
	} else if mode == "explicit" {
		expectedMap := make(map[string]string)
		for _, ent := range entries {
			expectedMap[ent.Key] = ent.ArmID
		}
		usedKeys := make(map[string]bool)

		for _, e := range enrollments {
			if e.Excluded {
				continue
			}
			total++
			expected, ok := expectedMap[e.Key]
			if !ok {
				mismatches = append(mismatches, AuditMismatch{
					RootOrigin: e.RootOrigin,
					Expected:   "<invalid_key>",
					Actual:     e.ArmID,
				})
				continue
			}
			if usedKeys[e.Key] {
				mismatches = append(mismatches, AuditMismatch{
					RootOrigin: e.RootOrigin,
					Expected:   "<unique_key>",
					Actual:     "<reused_key>",
				})
				continue
			}
			usedKeys[e.Key] = true

			if expected != e.ArmID {
				mismatches = append(mismatches, AuditMismatch{
					RootOrigin: e.RootOrigin,
					Expected:   expected,
					Actual:     e.ArmID,
				})
			}
		}
	}

	pct := 100.0
	if total > 0 {
		pct = 100.0 * float64(total-len(mismatches)) / float64(total)
	}
	return AuditReport{
		Passed:       len(mismatches) == 0,
		AgreementPct: pct,
		Total:        total,
		Mismatches:   mismatches,
	}
}
