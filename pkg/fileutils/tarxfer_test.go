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

package fileutils

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"
)

func makeTar() ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name:     "file1",
		Mode:     0o644,
		Size:     int64(len("contents")),
		ModTime:  time.Now(),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := tw.Write([]byte("contents")); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type tarEntry struct {
	header tar.Header
	data   []byte
}

func makeArchive(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, entry := range entries {
		header := entry.header
		if header.Typeflag == tar.TypeReg {
			header.Size = int64(len(entry.data))
		}
		if err := tw.WriteHeader(&header); err != nil {
			t.Fatalf("write tar header %q: %v", header.Name, err)
		}
		if _, err := tw.Write(entry.data); err != nil {
			t.Fatalf("write tar data %q: %v", header.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func unpackArchive(t *testing.T, archive []byte) (string, error) {
	t.Helper()
	base := t.TempDir()
	tarPath := filepath.Join(base, "context.tar")
	if err := os.WriteFile(tarPath, archive, 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	destination := filepath.Join(base, "destination")
	return destination, unpackTar(context.Background(), tarPath, destination)
}

func receiveArchive(t *testing.T, cacheBase, checksum string, archive []byte) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	demux := newDemux(ctx)
	if err := demux.Accept(btPacket(nil, false, map[string]string{"hash": checksum})); err != nil {
		t.Fatalf("accept hash: %v", err)
	}
	if err := demux.Accept(btPacket(archive[:512], false, nil)); err != nil {
		t.Fatalf("accept header: %v", err)
	}
	if err := demux.Accept(btPacket(archive[512:], true, nil)); err != nil {
		t.Fatalf("accept body: %v", err)
	}
	_, err := NewTarReceiver(cacheBase, demux).Receive(ctx, func(string, fs.DirEntry, error) error {
		return nil
	})
	return err
}

func newDemux(ctx context.Context) *stream.Demultiplexer {
	return stream.NewDemuxWithContext(ctx, "test‑demux", func(*api.ClientStream) error { return nil }, func(any) {})
}

func btPacket(data []byte, complete bool, meta map[string]string) *api.ClientStream {
	bt := &api.BuildTransfer{Data: data, Complete: complete, Metadata: meta}
	return &api.ClientStream{PacketType: &api.ClientStream_BuildTransfer{BuildTransfer: bt}}
}

func containsPath(paths []string, path string) bool {
	for _, candidate := range paths {
		if candidate == path {
			return true
		}
	}
	return false
}

func TestReceiver_Receive_Success(t *testing.T) {
	archive, err := makeTar()
	if err != nil {
		t.Fatalf("makeTar: %v", err)
	}
	if len(archive) < 512 {
		t.Fatalf("tar archive too small: %d", len(archive))
	}

	hashBytes := sha256.Sum256(archive)
	hash := hex.EncodeToString(hashBytes[:])
	header := archive[:512]
	body := archive[512:]

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	demux := newDemux(ctx)

	_ = demux.Accept(btPacket([]byte{}, false, map[string]string{"hash": hash}))
	_ = demux.Accept(btPacket(header, false, nil))
	_ = demux.Accept(btPacket(body, true, nil))

	tmpDir := t.TempDir()
	r := NewTarReceiver(tmpDir, demux)

	var visited []string
	walkFn := func(p string, _ fs.DirEntry, _ error) error {
		visited = append(visited, p)
		return nil
	}

	checksum, err := r.Receive(ctx, walkFn)
	if err != nil {
		t.Fatalf("Receive returned error: %v", err)
	}

	if checksum != hash {
		t.Fatalf("checksum mismatch: want %s, got %s", hash, checksum)
	}

	if len(visited) != 1 || visited[0] != "file1" {
		t.Fatalf("unexpected visited paths: %v", visited)
	}

	cacheDir := filepath.Join(tmpDir, checksum)
	if fi, err := os.Stat(filepath.Join(cacheDir, "file1")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("extracted file missing or not regular: %v", err)
	}
}

func TestReceiver_Receive_DoesNotPersistSyntheticDockerfiles(t *testing.T) {
	archive, err := makeTar()
	if err != nil {
		t.Fatalf("makeTar: %v", err)
	}
	hashBytes := sha256.Sum256(archive)
	hash := hex.EncodeToString(hashBytes[:])
	header := archive[:512]
	body := archive[512:]

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	demux := newDemux(ctx)

	_ = demux.Accept(btPacket([]byte{}, false, map[string]string{"hash": hash}))
	_ = demux.Accept(btPacket(header, false, nil))
	_ = demux.Accept(btPacket(body, true, nil))

	tmpDir := t.TempDir()
	r := NewTarReceiver(tmpDir, demux)

	var visited []string
	walkFn := func(p string, _ fs.DirEntry, _ error) error {
		visited = append(visited, p)
		return nil
	}

	checksum, err := r.Receive(ctx, walkFn)
	if err != nil {
		t.Fatalf("Receive returned error: %v", err)
	}
	if checksum != hash {
		t.Fatalf("checksum mismatch: want %s, got %s", hash, checksum)
	}

	cacheDir := filepath.Join(tmpDir, checksum)
	if _, err := os.Stat(filepath.Join(cacheDir, DockerfileStaging)); !os.IsNotExist(err) {
		t.Fatalf("synthetic Dockerfile directory persisted in cache: %v", err)
	}
	if containsPath(visited, filepath.Join(DockerfileStaging, "Dockerfile")) {
		t.Fatalf("synthetic Dockerfile was walked from the shared cache: %v", visited)
	}
}

func TestReceiver_Receive_OverflowsDemuxChannel(t *testing.T) {
	// Regression: a context transfer that produces more BuildTransfer
	// packets than the demux channel can hold must not drop the
	// complete=true marker. Accept must apply backpressure rather
	// than drop. Without that, the receiver hung forever waiting on
	// a packet that would never arrive.
	archive, err := makeTar()
	if err != nil {
		t.Fatalf("makeTar: %v", err)
	}
	if len(archive) < 512 {
		t.Fatalf("tar archive too small: %d", len(archive))
	}
	hashBytes := sha256.Sum256(archive)
	hash := hex.EncodeToString(hashBytes[:])
	header := archive[:512]
	body := archive[512:]

	// Split the body into enough chunks to exceed the demux channel
	// capacity, forcing the producer to wait on backpressure.
	const chunkSize = 16
	chunks := make([][]byte, 0, len(body)/chunkSize+1)
	for i := 0; i < len(body); i += chunkSize {
		end := i + chunkSize
		if end > len(body) {
			end = len(body)
		}
		chunks = append(chunks, body[i:end])
	}
	for len(chunks) < 64 {
		chunks = append(chunks, []byte{})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	demux := newDemux(ctx)

	producerErr := make(chan error, 1)
	go func() {
		defer close(producerErr)
		if err := demux.Accept(btPacket(nil, false, map[string]string{"hash": hash})); err != nil {
			producerErr <- err
			return
		}
		if err := demux.Accept(btPacket(header, false, nil)); err != nil {
			producerErr <- err
			return
		}
		for i, chunk := range chunks {
			isLast := i == len(chunks)-1
			if err := demux.Accept(btPacket(chunk, isLast, nil)); err != nil {
				producerErr <- err
				return
			}
		}
	}()

	tmpDir := t.TempDir()
	r := NewTarReceiver(tmpDir, demux)

	var visited []string
	walkFn := func(p string, _ fs.DirEntry, _ error) error {
		visited = append(visited, p)
		return nil
	}

	start := time.Now()
	checksum, err := r.Receive(ctx, walkFn)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Receive failed after %v: %v", elapsed, err)
	}
	if err := <-producerErr; err != nil {
		t.Fatalf("producer Accept failed: %v", err)
	}
	if checksum != hash {
		t.Fatalf("checksum mismatch: want %s, got %s", hash, checksum)
	}
	if len(visited) != 1 || visited[0] != "file1" {
		t.Fatalf("unexpected visited paths: %v", visited)
	}
	if fi, err := os.Stat(filepath.Join(tmpDir, checksum, "file1")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("extracted file missing or not regular: %v", err)
	}
}

func TestReceiver_Receive_ServerError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	demux := newDemux(ctx)

	metaErr := map[string]string{"error": "<err>"}
	_ = demux.Accept(btPacket(nil, true, metaErr))

	tmpDir := t.TempDir()
	r := NewTarReceiver(tmpDir, demux)

	_, err := r.Receive(ctx, func(string, fs.DirEntry, error) error { return nil })
	if err == nil {
		t.Fatalf("expected server error, got nil")
	}
	if !strings.Contains(err.Error(), "<err>") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReceiverReceiveRejectsTamperedArchiveAndAllowsRetry(t *testing.T) {
	archive, err := makeTar()
	if err != nil {
		t.Fatalf("makeTar: %v", err)
	}
	sum := sha256.Sum256(archive)
	checksum := hex.EncodeToString(sum[:])
	tampered := append([]byte(nil), archive...)
	tampered[len(tampered)-1] ^= 0x1

	cacheBase := t.TempDir()
	err = receiveArchive(t, cacheBase, checksum, tampered)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered archive error = %v, want checksum mismatch", err)
	}
	if _, err := os.Stat(filepath.Join(cacheBase, checksum)); !os.IsNotExist(err) {
		t.Fatalf("tampered archive left a cache directory: %v", err)
	}

	if err := receiveArchive(t, cacheBase, checksum, archive); err != nil {
		t.Fatalf("retry after tampered archive failed: %v", err)
	}
}

func TestCheckCacheRequiresCompletionMarker(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	if err := os.Mkdir(cacheDir, 0o755); err != nil {
		t.Fatalf("create cache directory: %v", err)
	}
	cached, err := checkCache(cacheDir)
	if err != nil || cached {
		t.Fatalf("incomplete cache = (%v, %v), want (false, nil)", cached, err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, cacheCompletionMarker), []byte(cacheCompletionValue), 0o600); err != nil {
		t.Fatalf("write completion marker: %v", err)
	}
	cached, err = checkCache(cacheDir)
	if err != nil || !cached {
		t.Fatalf("complete cache = (%v, %v), want (true, nil)", cached, err)
	}
}

func TestCheckCacheRejectsSymlinkedCacheAndMarker(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("create target: %v", err)
	}
	cacheLink := filepath.Join(base, "cache-link")
	if err := os.Symlink(target, cacheLink); err != nil {
		t.Fatalf("create cache symlink: %v", err)
	}
	if _, err := checkCache(cacheLink); err == nil {
		t.Fatal("checkCache accepted a symlinked cache directory")
	}

	cacheDir := filepath.Join(base, "cache")
	if err := os.Mkdir(cacheDir, 0o755); err != nil {
		t.Fatalf("create cache: %v", err)
	}
	markerTarget := filepath.Join(base, "marker")
	if err := os.WriteFile(markerTarget, []byte(cacheCompletionValue), 0o600); err != nil {
		t.Fatalf("write marker target: %v", err)
	}
	if err := os.Symlink(markerTarget, filepath.Join(cacheDir, cacheCompletionMarker)); err != nil {
		t.Fatalf("create marker symlink: %v", err)
	}
	if _, err := checkCache(cacheDir); err == nil {
		t.Fatal("checkCache accepted a symlinked completion marker")
	}
}

func TestValidateChecksum(t *testing.T) {
	archive, err := makeTar()
	if err != nil {
		t.Fatalf("makeTar: %v", err)
	}
	sum := sha256.Sum256(archive)
	valid := hex.EncodeToString(sum[:])
	if err := validateChecksum(valid); err != nil {
		t.Fatalf("validateChecksum rejected a SHA-256 digest: %v", err)
	}
	for _, checksum := range []string{"", "abc", strings.ToUpper(valid), "../../outside"} {
		if err := validateChecksum(checksum); err == nil {
			t.Fatalf("validateChecksum accepted %q", checksum)
		}
	}
}

func TestPublishTarSerializesConcurrentWriters(t *testing.T) {
	archive, err := makeTar()
	if err != nil {
		t.Fatalf("makeTar: %v", err)
	}
	sum := sha256.Sum256(archive)
	checksum := hex.EncodeToString(sum[:])
	cacheBase := t.TempDir()
	cacheDir := filepath.Join(cacheBase, checksum)

	writeArchive := func() string {
		file, err := os.CreateTemp(cacheBase, ".input-*.tar")
		if err != nil {
			t.Fatalf("create archive: %v", err)
		}
		path := file.Name()
		if _, err := file.Write(archive); err != nil {
			_ = file.Close()
			t.Fatalf("write archive: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close archive: %v", err)
		}
		return path
	}

	tars := []string{writeArchive(), writeArchive()}
	defer func() {
		for _, tarPath := range tars {
			_ = os.Remove(tarPath)
		}
	}()

	errs := make(chan error, len(tars))
	for _, tarPath := range tars {
		go func(path string) {
			errs <- publishTar(context.Background(), path, cacheDir, cacheBase)
		}(tarPath)
	}
	for range tars {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent publish failed: %v", err)
		}
	}
	cached, err := checkCache(cacheDir)
	if err != nil || !cached {
		t.Fatalf("published cache = (%v, %v), want (true, nil)", cached, err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "file1")); err != nil {
		t.Fatalf("published archive is incomplete: %v", err)
	}
}

func TestUnpackTarRejectsPathAndLinkEscapes(t *testing.T) {
	tests := []struct {
		name    string
		entries []tarEntry
	}{
		{
			name: "parent directory path",
			entries: []tarEntry{{
				header: tar.Header{Name: "../outside", Typeflag: tar.TypeReg, Mode: 0o644},
				data:   []byte("outside"),
			}},
		},
		{
			name: "reserved staging subtree",
			entries: []tarEntry{{
				header: tar.Header{Name: filepath.Join(DockerfileStaging, "Dockerfile"), Typeflag: tar.TypeReg, Mode: 0o644},
				data:   []byte("FROM scratch"),
			}},
		},
		{
			name: "cache completion marker",
			entries: []tarEntry{{
				header: tar.Header{Name: cacheCompletionMarker, Typeflag: tar.TypeReg, Mode: 0o644},
				data:   []byte("forged"),
			}},
		},
		{
			name: "hard link target outside cache",
			entries: []tarEntry{{
				header: tar.Header{Name: "copy", Typeflag: tar.TypeLink, Linkname: "../outside", Mode: 0o644},
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := unpackArchive(t, makeArchive(t, test.entries...))
			if err == nil {
				t.Fatal("unpackTar succeeded for unsafe archive")
			}
		})
	}
}

func TestUnpackTarRejectsSymlinkTraversal(t *testing.T) {
	external := t.TempDir()
	archive := makeArchive(t,
		tarEntry{header: tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: external, Mode: 0o777}},
		tarEntry{header: tar.Header{Name: filepath.Join("link", "outside"), Typeflag: tar.TypeReg, Mode: 0o644}, data: []byte("outside")},
	)
	_, err := unpackArchive(t, archive)
	if err == nil {
		t.Fatal("unpackTar succeeded through a symlink")
	}
	if _, err := os.Stat(filepath.Join(external, "outside")); !os.IsNotExist(err) {
		t.Fatalf("symlink traversal wrote outside the destination: %v", err)
	}
}
