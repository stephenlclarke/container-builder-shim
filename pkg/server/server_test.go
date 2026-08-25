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

package server

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apple/container-builder-shim/pkg/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLookupContext(t *testing.T) {
	basePath := t.TempDir()
	proxy := &BuilderProxy{path: basePath}
	presentDigest := strings.Repeat("a", sha256.Size*2)
	missingDigest := strings.Repeat("b", sha256.Size*2)

	if err := os.MkdirAll(filepath.Join(basePath, "fssync", presentDigest), 0o755); err != nil {
		t.Fatalf("create cached context: %v", err)
	}
	if err := os.WriteFile(filepath.Join(basePath, "fssync", presentDigest, ".container-builder-shim-complete"), []byte("complete\n"), 0o600); err != nil {
		t.Fatalf("mark cached context: %v", err)
	}

	present, err := proxy.LookupContext(context.Background(), &api.LookupContextRequest{Digest: presentDigest})
	if err != nil {
		t.Fatalf("lookup present context: %v", err)
	}
	if !present.GetPresent() {
		t.Fatal("expected cached context to be present")
	}

	missing, err := proxy.LookupContext(context.Background(), &api.LookupContextRequest{Digest: missingDigest})
	if err != nil {
		t.Fatalf("lookup missing context: %v", err)
	}
	if missing.GetPresent() {
		t.Fatal("expected missing context not to be present")
	}
}

func TestLookupContextRejectsInvalidDigest(t *testing.T) {
	proxy := &BuilderProxy{path: t.TempDir()}
	_, err := proxy.LookupContext(context.Background(), &api.LookupContextRequest{Digest: "../context"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid argument, got %v", err)
	}
}
