//===----------------------------------------------------------------------===//
// Copyright © 2025-2026 Apple Inc. and the container-builder-shim project authors.
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

package fssync

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	gofs "io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"
	"github.com/moby/patternmatcher"
	"google.golang.org/grpc/metadata"
)

func (p *FSSyncProxy) RegisterDemux(id string, d *stream.Demultiplexer) {
	demuxes[id] = d
}

var demuxes = map[string]*stream.Demultiplexer{}
var lastWalkRequest *api.BuildTransfer

func makeNestedTarHeaderAndBody() (checksum string, full []byte) {
	const payload = "hello from tar with nesting\n"

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	dirHeader := &tar.Header{
		Name:     "dir",
		Typeflag: tar.TypeDir,
		Mode:     0o755,
		ModTime:  time.Time{},
	}
	_ = tw.WriteHeader(dirHeader)

	fileHeader := &tar.Header{
		Name:     "dir/foo",
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     int64(len(payload)),
		ModTime:  time.Time{},
	}
	_ = tw.WriteHeader(fileHeader)
	_, _ = tw.Write([]byte(payload))

	_ = tw.Close()

	full = buf.Bytes()
	header := sha256.Sum256(full)
	return hex.EncodeToString(header[:]), full
}

// testTarFactory produces the tar content served by the fake Send below.
// Tests that need a different build-context tree may override it and restore
// the previous value when done.
var testTarFactory = makeNestedTarHeaderAndBody

func (p *FSSyncProxy) Send(s *api.ServerStream) error {
	id := s.BuildId
	d := demuxes[id]
	lastWalkRequest = s.GetBuildTransfer()
	checksum, full := testTarFactory()
	go func() {
		_ = d.Accept(&api.ClientStream{
			BuildId: id,
			PacketType: &api.ClientStream_BuildTransfer{
				BuildTransfer: &api.BuildTransfer{
					Id:        id,
					Direction: api.TransferDirection_INTO,
					Metadata:  map[string]string{"hash": checksum},
				},
			},
		})

		_ = d.Accept(&api.ClientStream{
			BuildId: id,
			PacketType: &api.ClientStream_BuildTransfer{
				BuildTransfer: &api.BuildTransfer{
					Id:        id,
					Direction: api.TransferDirection_INTO,
					Data:      full,
				},
			},
		})

		_ = d.Accept(&api.ClientStream{
			BuildId: id,
			PacketType: &api.ClientStream_BuildTransfer{
				BuildTransfer: &api.BuildTransfer{
					Id:        id,
					Direction: api.TransferDirection_INTO,
					Complete:  true,
				},
			},
		})
	}()
	return nil
}

func TestUnmarshalWalkMetadata_Defaults(t *testing.T) {
	md, err := unmarshalWalkMetadata(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if md.Mode != ModeTAR {
		t.Fatalf("default mode = %q, want %q", md.Mode, ModeTAR)
	}
}

func TestUnmarshalWalkMetadata_InvalidMode(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("mode", "json"))
	_, err := unmarshalWalkMetadata(ctx)
	if err == nil {
		t.Fatal("expected error for unsupported mode 'json', got nil")
	}
}

// BuildKit query-escapes all values of a key once any of them is non-ASCII.
func TestUnmarshalWalkMetadata_DecodesEncodedValues(t *testing.T) {
	excludes := []string{"ignored/*", "report-日本語.txt", "literal,comma.txt"}
	md := metadata.MD{"exclude-patterns-encoded": {"1"}}
	for _, e := range excludes {
		md["exclude-patterns"] = append(md["exclude-patterns"], url.QueryEscape(e))
	}
	md["followpaths"] = []string{url.QueryEscape("café"), url.QueryEscape("src")}
	md["followpaths-encoded"] = []string{"1"}

	got, err := unmarshalWalkMetadata(metadata.NewIncomingContext(context.Background(), md))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if want := "ignored/*,report-日本語.txt,literal,comma.txt"; strings.Join(got.ExcludedPatterns, ",") != want {
		t.Errorf("ExcludedPatterns = %q, want %q", got.ExcludedPatterns, want)
	}
	if want := "café,src"; got.FollowPaths != want {
		t.Errorf("FollowPaths = %q, want %q", got.FollowPaths, want)
	}

	matcher, err := patternmatcher.New(got.ExcludedPatterns)
	if err != nil {
		t.Fatalf("patternmatcher.New: %v", err)
	}
	for _, path := range []string{"ignored/file.txt", "report-日本語.txt", "literal,comma.txt"} {
		excluded, err := matcher.MatchesOrParentMatches(path)
		if err != nil {
			t.Fatalf("MatchesOrParentMatches(%q): %v", path, err)
		}
		if !excluded {
			t.Errorf("%q is not excluded", path)
		}
	}
}

func TestUnmarshalWalkMetadata_KeepsUnencodedValues(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("exclude-patterns", "100%+done"))
	got, err := unmarshalWalkMetadata(ctx)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if want := "100%+done"; strings.Join(got.ExcludedPatterns, ",") != want {
		t.Errorf("ExcludedPatterns = %q, want %q", got.ExcludedPatterns, want)
	}
}

func TestUnmarshalWalkMetadata_InvalidEncoding(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"exclude-patterns", "%zz",
		"exclude-patterns-encoded", "1",
	))
	if _, err := unmarshalWalkMetadata(ctx); err == nil {
		t.Fatal("expected error for invalid encoding, got nil")
	}
}

func TestUnmarshalWalkMetadata_InvalidEncodedFlag(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"exclude-patterns", url.QueryEscape("ignored/*"),
		"exclude-patterns-encoded", "yes",
	))
	if _, err := unmarshalWalkMetadata(ctx); err == nil {
		t.Fatal("expected error for invalid encoded flag, got nil")
	}
}

func TestUnmarshalWalkMetadata_InvalidEncodedStringFields(t *testing.T) {
	for _, key := range []string{"include-patterns", "followpaths", "dir-name"} {
		t.Run(key, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
				key, "%zz", key+"-encoded", "1",
			))
			if _, err := unmarshalWalkMetadata(ctx); err == nil {
				t.Fatal("expected invalid encoded metadata to fail")
			}
		})
	}
}

func TestWalk_UnsupportedMode(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("mode", "json"))
	fs := NewFS(ctx, &FSSyncProxy{}, "/", t.TempDir()) // proxy never used
	var fn gofs.WalkDirFunc = func(string, gofs.DirEntry, error) error { return nil }
	err := fs.Walk(ctx, "", fn)
	if err == nil {
		t.Fatal("Walk returned nil error, want unsupported-mode error")
	}
}

func TestWalk_TarModeSuccess(t *testing.T) {
	tmp := t.TempDir()

	_, full := makeNestedTarHeaderAndBody()

	fs := NewFS(context.Background(), &FSSyncProxy{}, "/", tmp)

	var walked []string
	err := fs.Walk(context.Background(), "", func(path string, _ gofs.DirEntry, _ error) error {
		walked = append(walked, path)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk returned err=%v", err)
	}
	fsSum := sha256.Sum256(full)
	fsChecksum := hex.EncodeToString(fsSum[:])
	if fs.getChecksum() != fsChecksum {
		t.Errorf("checksum = %q, want %q", fs.getChecksum(), fsChecksum)
	}
	if len(walked) == 0 {
		t.Errorf("walk callback not invoked")
	}
}

func TestWalk_TarModeKeepsRequestedSyntheticDockerfile(t *testing.T) {
	tmp := t.TempDir()

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"mode", "tar",
		"dir-name", "dockerfile",
		"followpaths", filepath.Join(DockerfileStaging, "Dockerfile"),
		"exclude-patterns", DockerfileStaging,
	))
	proxy := &FSSyncProxy{
		dockerfile:   []byte("FROM scratch\n"),
		dockerignore: []byte(DockerfileStaging),
	}
	rawFS := NewFS(ctx, proxy, "/", tmp)
	fs, err := filteredFS(ctx, proxy, rawFS)
	if err != nil {
		t.Fatalf("filteredFS returned err=%v", err)
	}
	lastWalkRequest = nil

	var walked []string
	err = fs.Walk(ctx, "", func(path string, _ gofs.DirEntry, _ error) error {
		walked = append(walked, path)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk returned err=%v", err)
	}
	if !containsPath(walked, filepath.Join(DockerfileStaging, "Dockerfile")) {
		t.Fatalf("synthetic Dockerfile was not walked: %v", walked)
	}
	if indexOfPath(walked, DockerfileStaging) > indexOfPath(walked, "dir") {
		t.Fatalf("synthetic Dockerfile directory was emitted out of order: %v", walked)
	}
	if lastWalkRequest == nil {
		t.Fatal("host walk request was not captured")
	}
	if got, want := lastWalkRequest.GetMetadata()["dir-name"], "context"; got != want {
		t.Fatalf("host walk dir-name = %q, want %q", got, want)
	}
	if containsPath(walked, filepath.Join(DockerfileStaging, "Dockerfile.dockerignore")) {
		t.Fatalf("synthetic Dockerfile.dockerignore should remain excluded: %v", walked)
	}
	if _, err := os.Stat(filepath.Join(tmp, rawFS.getChecksum(), DockerfileStaging)); !os.IsNotExist(err) {
		t.Fatalf("synthetic Dockerfile directory persisted in shared cache: %v", err)
	}
}

func indexOfPath(paths []string, path string) int {
	for index, candidate := range paths {
		if candidate == path {
			return index
		}
	}
	return len(paths)
}

func containsPath(paths []string, path string) bool {
	for _, candidate := range paths {
		if candidate == path {
			return true
		}
	}
	return false
}
