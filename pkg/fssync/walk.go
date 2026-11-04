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
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/fileutils"
	"github.com/apple/container-builder-shim/pkg/stream"
	"google.golang.org/grpc/metadata"
)

/*
Walk requests build-context files from the macOS host and presents them to BuildKit.

The host is asked for a tar archive containing the paths identified by
followpaths (glob patterns BuildKit sends in the request metadata). The shim
unpacks the tar to a content-addressed local cache and then walks the unpacked
tree, passing every entry to fn. Exclude-pattern filtering (from .dockerignore)
is applied by the fsutil filter that DiffCopy wraps around this FS.

Only TAR mode is supported. The JSON mode wire format is defined in
RawFileInfo below but is not exercised by the current shim.

If BuildKit does not supply followpaths, the shim falls back to addedGlobs —
source paths pre-computed from the Dockerfile AST (see pkg/build/buildopts.go).

Request Format:

	BuildTransfer {
	    ID: $uuid,
	    Direction: OUTOF,
	    Source: $path,
	    Metadata: {
	        "os":    "linux",
	        "stage": "fssync",
	        "method": "Walk",
	        "mode":  "json" | "tar"
	    }
	}

Depending on the specified mode, the server may respond with file info in JSON format,
or send a tar archive for remote file data.

Response Format ('json' mode):

	BuildTransfer {
	    ID: $uuid,
	    Direction: INTO,
	    Source: $path,
	    Metadata: {
	        "os":          "linux",
	        "stage":       "fssync",
	        "method":      "Walk",
	        "size":        "$size",
	        "mode":        $file_mode, // uint32 value
	        "modified_at": "$modified_timestamp",
	        "uid":         $uid,
	        "gid":         $gid,
	    },
	    "is_directory": $is_directory,
	    "complete":     "true"
	}

In TAR mode, the server sends a tar archive; we unpack it locally and then walk
the resulting directory paths.
*/
func (f *FS) Walk(ctx context.Context, target string, fn fs.WalkDirFunc) error {
	cancellableCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	walkMeta, err := unmarshalWalkMetadata(cancellableCtx)
	if err != nil {
		return err
	}

	id := uuid.NewString()
	demux := stream.NewDemuxWithContext(cancellableCtx, id, stream.FilterByBuildID(id), func(any) {
		// The operation-scoped context owns cleanup when this walk returns.
	})
	f.proxy.RegisterDemux(id, demux)

	followPaths := walkMeta.FollowPaths
	if followPaths == "" {
		followPaths = strings.Join(f.proxy.addedGlobs, ",")
	}
	syntheticFollowPaths := requestedSyntheticDockerfilePaths(followPaths)
	dirName := walkMeta.DirName
	if len(syntheticFollowPaths) > 0 {
		dirName = "context"
	}

	packet := &api.BuildTransfer{
		Id:        id,
		Direction: api.TransferDirection_OUTOF,
		Source:    &f.root,
		Metadata: map[string]string{
			"os":               "linux",
			"stage":            "fssync",
			"method":           "Walk",
			"dir-name":         dirName,
			"include-patterns": walkMeta.IncludePatterns,
			"followpaths":      followPaths,
			"mode":             string(walkMeta.Mode),
		},
	}
	if err := f.proxy.Send(&api.ServerStream{
		BuildId: id,
		PacketType: &api.ServerStream_BuildTransfer{
			BuildTransfer: packet,
		},
	}); err != nil {
		return fmt.Errorf("failed sending walk request: %w", err)
	}

	switch walkMeta.Mode {
	case ModeTAR:
		receiver := fileutils.NewTarReceiver(f.fsPath, demux)
		syntheticWalker := newSyntheticDockerfileWalker(f.proxy, syntheticFollowPaths, fn)
		checksum, err := receiver.Receive(ctx, syntheticWalker.Walk)
		if err != nil {
			return err
		}
		if err := syntheticWalker.Finish(); err != nil {
			return err
		}
		f._checksumMutex.Lock()
		defer f._checksumMutex.Unlock()
		f._checksum = checksum
		return nil
	default:
		return fmt.Errorf("unsupported walk mode: %q", walkMeta.Mode)
	}
}

// syntheticDockerfileWalker merges Dockerfile inputs from the active build
// request into a cache-backed walk without persisting them in the shared,
// content-addressed context cache.
type syntheticDockerfileWalker struct {
	entries []syntheticDockerfileEntry
	next    int
	fn      fs.WalkDirFunc
}

type syntheticDockerfileEntry struct {
	path string
	data []byte
}

func newSyntheticDockerfileWalker(proxy *FSSyncProxy, requested map[string]struct{}, fn fs.WalkDirFunc) *syntheticDockerfileWalker {
	walker := &syntheticDockerfileWalker{fn: fn}
	if proxy == nil || len(requested) == 0 {
		return walker
	}

	files := []syntheticDockerfileEntry{
		{path: filepath.Join(DockerfileStaging, "Dockerfile"), data: proxy.dockerfile},
		{path: filepath.Join(DockerfileStaging, "Dockerfile.dockerignore"), data: proxy.dockerignore},
	}

	hasFile := false
	for _, file := range files {
		if _, ok := requested[file.path]; ok && len(file.data) > 0 {
			hasFile = true
			break
		}
	}
	if !hasFile {
		return walker
	}

	walker.entries = append(walker.entries, syntheticDockerfileEntry{path: DockerfileStaging})
	for _, file := range files {
		if _, ok := requested[file.path]; ok && len(file.data) > 0 {
			walker.entries = append(walker.entries, file)
		}
	}
	return walker
}

func (w *syntheticDockerfileWalker) Walk(path string, entry fs.DirEntry, walkErr error) error {
	for w.next < len(w.entries) && w.entries[w.next].path < path {
		if err := w.emit(w.entries[w.next]); err != nil {
			return err
		}
		w.next++
	}
	return w.fn(path, entry, walkErr)
}

func (w *syntheticDockerfileWalker) Finish() error {
	for w.next < len(w.entries) {
		if err := w.emit(w.entries[w.next]); err != nil {
			return err
		}
		w.next++
	}
	return nil
}

func (w *syntheticDockerfileWalker) emit(entry syntheticDockerfileEntry) error {
	if entry.path == DockerfileStaging {
		return w.fn(DockerfileStaging, fs.FileInfoToDirEntry(&fileutils.FileInfo{
			NameVal:  DockerfileStaging,
			ModeVal:  fs.ModeDir | 0o755,
			IsDirVal: true,
		}), nil)
	}
	return w.fn(entry.path, fs.FileInfoToDirEntry(&fileutils.FileInfo{
		NameVal: entry.path,
		SizeVal: int64(len(entry.data)),
		ModeVal: 0o644,
	}), nil)
}

func requestedSyntheticDockerfilePaths(followPaths string) map[string]struct{} {
	paths := map[string]struct{}{}
	for _, path := range strings.Split(followPaths, ",") {
		path = filepath.Clean(strings.TrimSpace(path))
		switch path {
		case filepath.Join(DockerfileStaging, "Dockerfile"),
			filepath.Join(DockerfileStaging, "Dockerfile.dockerignore"):
			paths[path] = struct{}{}
		}
	}
	return paths
}

// RawFileInfo is the wire‑format for Walk (json mode).
type RawFileInfo struct {
	Name    string `json:"name"`
	Size    uint64 `json:"size"`
	Mode    uint32 `json:"mode"`
	IsDir   bool   `json:"isDir"`
	ModTime string `json:"modTime"`
	UID     uint32 `json:"uid"`
	GID     uint32 `json:"gid"`
	Target  string `json:"target"`
}

type WalkMetadata struct {
	IncludePatterns  string
	ExcludedPatterns []string
	FollowPaths      string
	DirName          string
	Mode             TransferMode
}

func unmarshalWalkMetadata(ctx context.Context) (*WalkMetadata, error) {
	md := &WalkMetadata{}
	if m, ok := metadata.FromIncomingContext(ctx); ok {
		var err error
		if md.IncludePatterns, err = metadataValue(m, "include-patterns"); err != nil {
			return nil, err
		}
		if md.ExcludedPatterns, err = metadataValues(m, "exclude-patterns"); err != nil {
			return nil, err
		}
		if md.FollowPaths, err = metadataValue(m, "followpaths"); err != nil {
			return nil, err
		}
		if md.DirName, err = metadataValue(m, "dir-name"); err != nil {
			return nil, err
		}
		modeStr := strings.Join(m["mode"], ",")
		switch modeStr {
		case "", "tar":
			modeStr = string(ModeTAR)
		default:
			return nil, fmt.Errorf("invalid walk mode: %s", modeStr)
		}
		md.Mode = TransferMode(modeStr)
	} else {
		md.Mode = ModeTAR
	}
	return md, nil
}

// metadataValue returns the comma-joined values of key. BuildKit query-escapes
// every value of a key when any of them contains a non-ASCII character, and
// marks this by setting "<key>-encoded" to "1", so such values are unescaped
// here. An invalid flag or a value that fails to unescape is an error rather
// than being used as-is, since an undecoded exclude pattern would silently
// match nothing.
func metadataValue(m metadata.MD, key string) (string, error) {
	values, err := metadataValues(m, key)
	if err != nil {
		return "", err
	}
	return strings.Join(values, ","), nil
}

// metadataValues preserves individual patterns, including literal commas, for
// the fsutil filter while applying BuildKit's encoded-metadata convention.
func metadataValues(m metadata.MD, key string) ([]string, error) {
	values := m[key]
	encoded := false
	if flag := m[key+"-encoded"]; len(flag) > 0 {
		var err error
		if encoded, err = strconv.ParseBool(flag[0]); err != nil {
			return nil, fmt.Errorf("invalid %s-encoded metadata: %w", key, err)
		}
	}
	if !encoded {
		return values, nil
	}
	decoded := make([]string, len(values))
	for i, v := range values {
		d, err := url.QueryUnescape(v)
		if err != nil {
			return nil, fmt.Errorf("failed to decode %s metadata: %w", key, err)
		}
		decoded[i] = d
	}
	return decoded, nil
}
