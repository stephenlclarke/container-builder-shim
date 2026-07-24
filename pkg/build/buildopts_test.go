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
	if got, want := string(opts.Dockerignore), DockerfileStaging; got != want {
		t.Fatalf("Dockerignore = %q, want %q", got, want)
	}
}

func TestNewBuildOptsParsesDockerfileFrontendMetadata(t *testing.T) {
	opts, err := NewBuildOpts(context.Background(), t.TempDir(), map[string][]string{
		KeyBuildID:       {"build-id"},
		KeyDockerfile:    {base64.StdEncoding.EncodeToString([]byte("FROM scratch\n"))},
		KeyTag:           {"example/app:latest"},
		KeyBuildContexts: {"shared=local:shared", "base=docker-image://example/base:latest"},
		KeyEntitlements:  {"network.host"},
		KeyAddHosts:      {"build.local=127.0.0.1", "cache.local=127.0.0.2"},
		KeyNetwork:       {"host"},
		KeyPrivileged:    {""},
		KeyShmSize:       {"67108864"},
		KeyUlimit:        {"nofile=1024:2048", "nproc=512"},
	})
	if err != nil {
		t.Fatalf("NewBuildOpts() error = %v", err)
	}

	if got, want := opts.BuildContexts, map[string]string{"shared": "local:shared", "base": "docker-image://example/base:latest"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildContexts = %#v, want %#v", got, want)
	}
	if got, want := opts.Entitlements, []string{"network.host", "security.insecure"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Entitlements = %#v, want %#v", got, want)
	}
	if got, want := opts.dockerfileFrontendAttrs(), map[string]string{
		"filename":           ".com.apple.container/Dockerfile",
		"context:shared":     "local:shared",
		"context:base":       "docker-image://example/base:latest",
		"add-hosts":          "build.local=127.0.0.1,cache.local=127.0.0.2",
		"force-network-mode": "host",
		"shm-size":           "67108864",
		"ulimit":             "nofile=1024:2048,nproc=512",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dockerfileFrontendAttrs = %#v, want %#v", got, want)
	}
}

func TestNewBuildOptsPreservesNoCacheMetadata(t *testing.T) {
	tests := []struct {
		name        string
		values      []string
		want        string
		wantPresent bool
	}{
		{
			name: "absent",
		},
		{
			name:        "all stages",
			values:      []string{""},
			wantPresent: true,
		},
		{
			name:        "named stages",
			values:      []string{"base,compile"},
			want:        "base,compile",
			wantPresent: true,
		},
		{
			name:        "last metadata value",
			values:      []string{"base", "final"},
			want:        "final",
			wantPresent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := map[string][]string{
				KeyBuildID:    {"build-id"},
				KeyDockerfile: {base64.StdEncoding.EncodeToString([]byte("FROM scratch\n"))},
				KeyTag:        {"example/app:latest"},
			}
			if tt.values != nil {
				metadata[KeyNoCache] = tt.values
			}

			opts, err := NewBuildOpts(context.Background(), t.TempDir(), metadata)
			if err != nil {
				t.Fatalf("NewBuildOpts() error = %v", err)
			}
			got, present := opts.dockerfileFrontendAttrs()[KeyNoCache]
			if present != tt.wantPresent || got != tt.want {
				t.Fatalf("no-cache = %q, %t; want %q, %t", got, present, tt.want, tt.wantPresent)
			}
		})
	}
}

func TestNewBuildOptsRejectsInvalidBuildNetworkMode(t *testing.T) {
	_, err := NewBuildOpts(context.Background(), t.TempDir(), map[string][]string{
		KeyBuildID:    {"build-id"},
		KeyDockerfile: {base64.StdEncoding.EncodeToString([]byte("FROM scratch\n"))},
		KeyTag:        {"example/app:latest"},
		KeyNetwork:    {"bridge"},
	})
	if err != ErrInvalidNetworkMode {
		t.Fatalf("NewBuildOpts() error = %v, want %v", err, ErrInvalidNetworkMode)
	}
}
