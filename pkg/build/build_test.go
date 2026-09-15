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
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/client"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

type testWriteCloser struct{}

func (testWriteCloser) Write(payload []byte) (int, error) { return len(payload), nil }
func (testWriteCloser) Close() error                      { return nil }

func TestConfiguredExportsCheckBuildNeedsNoExporter(t *testing.T) {
	exports, err := configuredExports(&BOpts{Check: true})
	if err != nil || exports != nil {
		t.Fatalf("configuredExports(check) = (%v, %v), want (nil, nil)", exports, err)
	}
}

func TestConfiguredExportsDefaultsToOCI(t *testing.T) {
	exports, err := configuredExports(&BOpts{BuildID: "test-build", Tag: "example:test"})
	if err != nil {
		t.Fatalf("configuredExports: %v", err)
	}
	if len(exports) != 1 || exports[0].Type != client.ExporterOCI {
		t.Fatalf("configured exports = %+v, want one OCI exporter", exports)
	}
	if exports[0].Attrs["name"] != "example:test" {
		t.Fatalf("export name = %q", exports[0].Attrs["name"])
	}
	writer, err := exports[0].Output(nil)
	if err != nil || writer == nil {
		t.Fatalf("export output = (%v, %v)", writer, err)
	}
	discardExportWriter(writer)
}

func TestConfiguredExportsRejectsInvalidOutput(t *testing.T) {
	if _, err := configuredExports(&BOpts{Outputs: []string{"type=unknown"}}); err == nil {
		t.Fatal("configuredExports accepted an unsupported output type")
	}
}

func TestConfigureExportPreservesExplicitAttributes(t *testing.T) {
	entry, err := configureExport(client.ExportEntry{
		Type: client.ExporterTar,
		Attrs: map[string]string{
			"name": "explicit:name",
			"annotation-index-descriptor.com.apple.containerization.image.name": "explicit:annotation",
		},
	}, &BOpts{BuildID: "build", Tag: "default:tag"}, testWriteCloser{})
	if err != nil {
		t.Fatalf("configureExport: %v", err)
	}
	if entry.Attrs["name"] != "explicit:name" || entry.Attrs["annotation-index-descriptor.com.apple.containerization.image.name"] != "explicit:annotation" {
		t.Fatalf("explicit attributes were replaced: %+v", entry.Attrs)
	}
	writer, err := entry.Output(nil)
	if err != nil {
		t.Fatalf("entry.Output: %v", err)
	}
	if _, ok := writer.(testWriteCloser); !ok {
		t.Fatalf("entry.Output returned %T, want testWriteCloser", writer)
	}
}

func TestExportWriterSkipsLocalOnlyExports(t *testing.T) {
	writer, err := exportWriter([]client.ExportEntry{{Type: client.ExporterLocal}}, "build")
	if err != nil || writer != nil {
		t.Fatalf("exportWriter(local) = (%v, %v), want (nil, nil)", writer, err)
	}
	discardExportWriter(testWriteCloser{})
}

func TestWrappedWriteCloserPublishesBufferedOutput(t *testing.T) {
	temporary, err := os.CreateTemp(t.TempDir(), "export-*")
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "out.tar")
	writer := &wrappedWriteCloser{f: temporary, dest: destination}
	if _, err := writer.Write([]byte("payload")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	payload, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(payload) != "payload" {
		t.Fatalf("published payload = %q", payload)
	}
}

func TestConfigureExportInitializesAttributes(t *testing.T) {
	entry, err := configureExport(client.ExportEntry{Type: client.ExporterOCI}, &BOpts{BuildID: "build", Tag: "example:test"}, testWriteCloser{})
	if err != nil {
		t.Fatalf("configureExport: %v", err)
	}
	if entry.Attrs["name"] != "example:test" || entry.Output == nil {
		t.Fatalf("configured export = %+v", entry)
	}
}

func TestNewSolveOptionsMapsBuildMetadata(t *testing.T) {
	opts := &BOpts{
		Tag:          "example:test",
		Platforms:    []ocispecs.Platform{{OS: "linux", Architecture: "arm64"}},
		BuildArgs:    map[string]string{"MODE": "release"},
		Labels:       map[string]string{"purpose": "test"},
		Attestations: map[string]string{"attest:provenance": "mode=min"},
		Target:       "builder",
	}
	solve, err := newSolveOptions(opts, []client.ExportEntry{{Type: client.ExporterOCI}})
	if err != nil {
		t.Fatalf("newSolveOptions: %v", err)
	}
	want := map[string]string{
		"build-arg:MODE":    "release",
		"label:purpose":     "test",
		"target":            "builder",
		"platform":          "linux/arm64",
		"attest:provenance": "mode=min",
	}
	for key, value := range want {
		if solve.FrontendAttrs[key] != value {
			t.Errorf("FrontendAttrs[%q] = %q, want %q", key, solve.FrontendAttrs[key], value)
		}
	}
	if solve.Frontend != "dockerfile.v1" || len(solve.Exports) != 1 {
		t.Fatalf("unexpected solve options: %+v", solve)
	}
}

func TestNewSolveOptionsRejectsInvalidCacheConfiguration(t *testing.T) {
	for name, opts := range map[string]*BOpts{
		"import": {CacheIn: []string{`type=registry,ref="unterminated`}},
		"export": {CacheOut: []string{`type=registry,ref="unterminated`}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newSolveOptions(opts, nil); err == nil {
				t.Fatal("newSolveOptions accepted invalid cache configuration")
			}
		})
	}
}
