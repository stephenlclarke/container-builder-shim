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

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareUnixSocketCreatesUsableDirectoryAndRemovesStaleSocket(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "run", "buildkit", "shim.sock")
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socketPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := prepareUnixSocket(socketPath); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Dir(socketPath))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("expected socket parent to be a directory")
	}
	if info.Mode().Perm()&0o700 != 0o700 {
		t.Fatalf("expected owner to have rwx permission, got %o", info.Mode().Perm())
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("expected stale socket path to be removed, got %v", err)
	}
}

func TestParseRegistryMirrorPreservesEqualsInValue(t *testing.T) {
	key, value, err := parseRegistryMirror("docker.io=https://mirror.example/token=a=b")
	if err != nil {
		t.Fatal(err)
	}
	if key != "docker.io" {
		t.Fatalf("unexpected key %q", key)
	}
	if value != "https://mirror.example/token=a=b" {
		t.Fatalf("unexpected value %q", value)
	}
}

func TestParseRegistryMirrorRejectsMissingSeparator(t *testing.T) {
	if _, _, err := parseRegistryMirror("docker.io"); err == nil {
		t.Fatal("expected missing separator to fail")
	}
}

func TestParseRegistryMirrorRejectsEmptyKey(t *testing.T) {
	if _, _, err := parseRegistryMirror("=https://mirror.example"); err == nil {
		t.Fatal("expected empty key to fail")
	}
}
