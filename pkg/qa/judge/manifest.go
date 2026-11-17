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

package judge

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type ReadoutManifest struct {
	ExperimentID string
	ArmIDs       []string
	TotalPackets int
}

type PacketScore struct {
	PacketID string
	ArmID    string
	Score    float64
}

type FindingSeverityRecord struct {
	PacketID string
	Severity int
}

type ProbeReport struct {
	AUC float64
}

func WriteReadoutBundle(readoutDir string, manifest ReadoutManifest, scores []PacketScore, severities []FindingSeverityRecord, probe ProbeReport, packets []JudgePacket) error {
	if err := os.MkdirAll(filepath.Join(readoutDir, "packets"), 0755); err != nil {
		return err
	}

	b, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(readoutDir, "manifest.json"), b, 0644); err != nil {
		return err
	}

	b, err = json.Marshal(probe)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(readoutDir, "probe.json"), b, 0644); err != nil {
		return err
	}

	fScores, err := os.Create(filepath.Join(readoutDir, "scores.jsonl"))
	if err != nil {
		return err
	}
	defer fScores.Close()
	encScores := json.NewEncoder(fScores)
	for _, s := range scores {
		if err := encScores.Encode(s); err != nil {
			return err
		}
	}

	fSev, err := os.Create(filepath.Join(readoutDir, "severity.jsonl"))
	if err != nil {
		return err
	}
	defer fSev.Close()
	encSev := json.NewEncoder(fSev)
	for _, s := range severities {
		if err := encSev.Encode(s); err != nil {
			return err
		}
	}

	for _, p := range packets {
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(readoutDir, "packets", p.PacketID+".json"), b, 0644); err != nil {
			return err
		}
	}

	return nil
}

func LoadReadoutBundle(readoutDir string) (ReadoutManifest, []PacketScore, []FindingSeverityRecord, ProbeReport, []JudgePacket, error) {
	var manifest ReadoutManifest
	b, err := os.ReadFile(filepath.Join(readoutDir, "manifest.json"))
	if err != nil {
		return manifest, nil, nil, ProbeReport{}, nil, err
	}
	if err := json.Unmarshal(b, &manifest); err != nil {
		return manifest, nil, nil, ProbeReport{}, nil, err
	}

	var probe ProbeReport
	b, err = os.ReadFile(filepath.Join(readoutDir, "probe.json"))
	if err != nil {
		return manifest, nil, nil, ProbeReport{}, nil, err
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return manifest, nil, nil, ProbeReport{}, nil, err
	}

	var scores []PacketScore
	fScores, err := os.Open(filepath.Join(readoutDir, "scores.jsonl"))
	if err != nil {
		return manifest, nil, nil, ProbeReport{}, nil, err
	}
	defer fScores.Close()
	decScores := json.NewDecoder(fScores)
	for decScores.More() {
		var s PacketScore
		if err := decScores.Decode(&s); err != nil {
			return manifest, nil, nil, ProbeReport{}, nil, err
		}
		scores = append(scores, s)
	}

	var severities []FindingSeverityRecord
	fSev, err := os.Open(filepath.Join(readoutDir, "severity.jsonl"))
	if err != nil {
		return manifest, scores, nil, ProbeReport{}, nil, err
	}
	defer fSev.Close()
	decSev := json.NewDecoder(fSev)
	for decSev.More() {
		var s FindingSeverityRecord
		if err := decSev.Decode(&s); err != nil {
			return manifest, scores, nil, ProbeReport{}, nil, err
		}
		severities = append(severities, s)
	}

	var packets []JudgePacket
	files, err := os.ReadDir(filepath.Join(readoutDir, "packets"))
	if err != nil {
		return manifest, scores, severities, probe, nil, err
	}
	for _, fi := range files {
		if filepath.Ext(fi.Name()) == ".json" {
			b, err := os.ReadFile(filepath.Join(readoutDir, "packets", fi.Name()))
			if err != nil {
				return manifest, scores, severities, probe, nil, err
			}
			var p JudgePacket
			if err := json.Unmarshal(b, &p); err != nil {
				return manifest, scores, severities, probe, nil, err
			}
			packets = append(packets, p)
		}
	}

	return manifest, scores, severities, probe, packets, nil
}
