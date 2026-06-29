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
	"path/filepath"
	"testing"
	"time"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"
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

func (p *FSSyncProxy) Send(s *api.ServerStream) error {
	id := s.BuildId
	d := demuxes[id]
	lastWalkRequest = s.GetBuildTransfer()
	checksum, full := makeNestedTarHeaderAndBody()
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
	fs := NewFS(ctx, proxy, "/", tmp)
	lastWalkRequest = nil

	var walked []string
	err := fs.Walk(ctx, "", func(path string, _ gofs.DirEntry, _ error) error {
		walked = append(walked, path)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk returned err=%v", err)
	}
	if !containsPath(walked, filepath.Join(DockerfileStaging, "Dockerfile")) {
		t.Fatalf("synthetic Dockerfile was not walked: %v", walked)
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
}

func containsPath(paths []string, path string) bool {
	for _, candidate := range paths {
		if candidate == path {
			return true
		}
	}
	return false
}
