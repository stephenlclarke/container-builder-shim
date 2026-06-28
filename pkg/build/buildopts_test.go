//===----------------------------------------------------------------------===//
// Copyright © 2026 Apple Inc. and the container-builder-shim project authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//===----------------------------------------------------------------------===//

package build

import (
	"context"
	"encoding/base64"
	"reflect"
	"testing"
)

func TestExtractAttestations(t *testing.T) {
	got, err := extractAttestations(map[string][]string{
		KeyAttestProvenance: {"mode=max"},
		KeyAttestSBOM:       {""},
	})
	if err != nil {
		t.Fatalf("extractAttestations() error = %v", err)
	}

	want := map[string]string{
		KeyFrontendAttestProvenance: "mode=max",
		KeyFrontendAttestSBOM:       "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extractAttestations() = %#v, want %#v", got, want)
	}
}

func TestExtractAttestationsUsesLastValue(t *testing.T) {
	got, err := extractAttestations(map[string][]string{
		KeyAttestProvenance: {"mode=min", "mode=max"},
	})
	if err != nil {
		t.Fatalf("extractAttestations() error = %v", err)
	}

	if got[KeyFrontendAttestProvenance] != "mode=max" {
		t.Fatalf("provenance attestation = %q, want mode=max", got[KeyFrontendAttestProvenance])
	}
}

func TestExtractAttestationsRejectsInvalidCSV(t *testing.T) {
	_, err := extractAttestations(map[string][]string{
		KeyAttestSBOM: {`generator="unterminated`},
	})
	if err == nil {
		t.Fatal("expected invalid attestation CSV to fail")
	}
}

func TestNewBuildOptsParsesCheckMetadata(t *testing.T) {
	opts, err := NewBuildOpts(context.Background(), t.TempDir(), map[string][]string{
		KeyBuildID:    {"build-id"},
		KeyDockerfile: {base64.StdEncoding.EncodeToString([]byte("FROM scratch\n"))},
		KeyTag:        {"example/app:latest"},
		KeyCheck:      {""},
	})
	if err != nil {
		t.Fatalf("NewBuildOpts() error = %v", err)
	}
	if !opts.Check {
		t.Fatal("opts.Check = false, want true")
	}
}
