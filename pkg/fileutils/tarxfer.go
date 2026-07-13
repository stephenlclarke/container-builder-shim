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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/apple/container-builder-shim/pkg/stream"
	"github.com/gofrs/flock"
)

const (
	DockerfileStaging     = ".com.apple.container"
	cacheCompletionMarker = ".container-builder-shim-complete"
	cacheCompletionValue  = "complete\n"
)

// Receiver streams a tar archive from the macOS host, stores it in a
// content-addressed cache under cacheBase, unpacks it, and walks the result.
//
// Symlinks are unpacked as real OS symlinks (os.Symlink). filepath.Walk does
// not follow them, so they appear in the walked tree with their Linkname
// intact. BuildKit decides at COPY/ADD time whether to dereference them based
// on its own copy semantics.
//
// If the cache directory for the tar's content hash already exists the
// download is skipped and the cached tree is used directly.
type Receiver struct {
	demux     *stream.Demultiplexer
	cacheBase string
}

func NewTarReceiver(cacheBase string, demux *stream.Demultiplexer) *Receiver {
	return &Receiver{demux: demux, cacheBase: cacheBase}
}

func (r *Receiver) Receive(ctx context.Context, fn fs.WalkDirFunc) (string, error) {
	errCh := make(chan error, 1)
	hashCh := make(chan string, 1)
	dataCh := make(chan []byte)
	go startTar(r.demux, errCh, hashCh, dataCh)

	checksum, err := readTarHash(ctx, errCh, hashCh)
	if err != nil {
		return "", err
	}

	if err := validateChecksum(checksum); err != nil {
		return "", err
	}
	if err := os.MkdirAll(r.cacheBase, 0o755); err != nil {
		return "", err
	}

	cacheDir := filepath.Join(r.cacheBase, checksum)

	cached, err := checkCache(cacheDir)
	if err != nil {
		return "", err
	}

	header, err := readTarHeader(ctx, errCh, dataCh)
	if err != nil {
		return "", err
	}

	tarFile := ""
	if !cached {
		tarFile, err = writeTar(r.cacheBase, header)
		if err != nil {
			return "", err
		}
		defer os.Remove(tarFile)
	}

	err = readTarBody(ctx, errCh, dataCh, tarFile, cached)
	if err != nil {
		return "", err
	}

	if !cached {
		if err := verifyTarHash(tarFile, checksum); err != nil {
			return "", err
		}
		if err := publishTar(ctx, tarFile, cacheDir, r.cacheBase); err != nil {
			return "", err
		}
	}

	return checksum, filepath.Walk(cacheDir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(cacheDir, p)
		if err != nil || rel == "." || rel == cacheCompletionMarker {
			return err
		}
		if info.Mode().IsRegular() || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			linkName := ""
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(p)
				if err != nil {
					return err
				}
				linkName = target
			}
			return fn(rel, fs.FileInfoToDirEntry(&FileInfo{
				NameVal:    rel,
				SizeVal:    info.Size(),
				ModeVal:    info.Mode(),
				ModTimeVal: info.ModTime(),
				IsDirVal:   info.IsDir(),
				LinkName:   linkName,
			}), nil)
		}
		return nil
	})
}

func startTar(demux *stream.Demultiplexer, errCh chan<- error, hashCh chan<- string, dataCh chan<- []byte) {
	defer close(errCh)
	defer close(hashCh)
	defer close(dataCh)

	for {
		resp, err := demux.Recv()
		if err != nil {
			if err == io.EOF || strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "transport is closing") {
				errCh <- fmt.Errorf("tar stream ended before completion")
				return
			}
			errCh <- err
			return
		}
		if bt := resp.GetBuildTransfer(); bt != nil {
			if errMsg, ok := bt.Metadata["error"]; ok {
				errCh <- fmt.Errorf("server error in TAR mode: %s", errMsg)
				return
			}

			if hash, ok := bt.Metadata["hash"]; ok {
				hashCh <- hash
				continue
			}

			dataCh <- bt.Data
			if bt.Complete {
				errCh <- nil
				return
			}
			continue
		}
		if it := resp.GetImageTransfer(); it != nil {
			if errMsg, ok := it.Metadata["error"]; ok {
				errCh <- fmt.Errorf("server error in TAR mode: %s", errMsg)
				return
			}
			dataCh <- it.Data
			if it.Complete {
				errCh <- nil
				return
			}
			continue
		}
		errCh <- fmt.Errorf("tar stream: unexpected packet type")
	}
}

func readTarHash(ctx context.Context, errCh <-chan error, hashCh <-chan string) (string, error) {
	select {
	case h, ok := <-hashCh:
		if !ok {
			if e := <-errCh; e != nil {
				return "", e
			}
			return "", fmt.Errorf("hash channel closed, no hash received")
		}
		return h, nil
	case e := <-errCh:
		return "", e
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func readTarHeader(ctx context.Context, errCh <-chan error, dataCh <-chan []byte) ([]byte, error) {
	select {
	case d, ok := <-dataCh:
		if !ok {
			if e := <-errCh; e != nil {
				return nil, e
			}
			return nil, fmt.Errorf("data channel closed")
		}
		return d, nil
	case e := <-errCh:
		return nil, e
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func readTarBody(ctx context.Context, errCh <-chan error, dataCh <-chan []byte, tarPath string, cached bool) error {
	var f *os.File
	var err error
	if !cached {
		f, err = os.OpenFile(tarPath, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
	}

	for {
		select {
		case d, ok := <-dataCh:
			if !ok {
				select {
				case e := <-errCh:
					if e != nil {
						return e
					}
				default:
				}
				return nil
			}
			if !cached {
				if _, wErr := f.Write(d); wErr != nil {
					return wErr
				}
			}
		case e, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if e != nil {
				return e
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func validateChecksum(checksum string) error {
	if len(checksum) != sha256.Size*2 {
		return fmt.Errorf("invalid tar checksum: expected a SHA-256 digest")
	}
	decoded, err := hex.DecodeString(checksum)
	if err != nil || hex.EncodeToString(decoded) != checksum {
		return fmt.Errorf("invalid tar checksum: expected lowercase hexadecimal")
	}
	return nil
}

func checkCache(cachePath string) (bool, error) {
	fi, err := os.Lstat(cachePath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.IsDir() {
		return false, fmt.Errorf("cache path is not a directory: %s", cachePath)
	}
	markerPath := filepath.Join(cachePath, cacheCompletionMarker)
	markerInfo, err := os.Lstat(markerPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !markerInfo.Mode().IsRegular() {
		return false, fmt.Errorf("cache completion marker is not a regular file: %s", markerPath)
	}
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		return false, err
	}
	return string(marker) == cacheCompletionValue, nil
}

func writeTar(base string, data []byte) (string, error) {
	f, err := os.CreateTemp(base, ".context-*.tar")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func verifyTarHash(path, checksum string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if actual := hex.EncodeToString(h.Sum(nil)); actual != checksum {
		return fmt.Errorf("tar checksum mismatch: expected %s, got %s", checksum, actual)
	}
	return nil
}

func publishTar(ctx context.Context, tarFile, cacheDir, cacheBase string) error {
	lockPath := cacheDir + ".lock"
	if info, err := os.Lstat(lockPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cache lock path is a symlink: %s", lockPath)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	lock := flock.New(lockPath, flock.SetPermissions(0o600))
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("failed to acquire cache lock for %s", cacheDir)
	}
	defer func() { _ = lock.Unlock() }()

	cached, err := checkCache(cacheDir)
	if err != nil {
		return err
	}
	if cached {
		return nil
	}
	if err := os.RemoveAll(cacheDir); err != nil {
		return err
	}

	tempDir, err := os.MkdirTemp(cacheBase, ".context-*")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(tempDir)
		}
	}()

	if err := unpackTar(ctx, tarFile, tempDir); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tempDir, cacheCompletionMarker), []byte(cacheCompletionValue), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tempDir, cacheDir); err != nil {
		return err
	}
	published = true
	return nil
}

func unpackTar(ctx context.Context, tarFile, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}

	r, err := os.Open(tarFile)
	if err != nil {
		return err
	}
	defer r.Close()

	tr := tar.NewReader(r)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target, err := tarTarget(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := ensureDirectory(dest, target); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := ensureNewPath(dest, target); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, fs.FileMode(hdr.Mode&0o777))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := ensureNewPath(dest, target); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			linkTarget, err := tarTarget(dest, hdr.Linkname)
			if err != nil {
				return err
			}
			if err := ensurePathParent(dest, linkTarget); err != nil {
				return err
			}
			info, err := os.Lstat(linkTarget)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("hard link target is not a regular file: %s", hdr.Linkname)
			}
			if err := ensureNewPath(dest, target); err != nil {
				return err
			}
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported tar entry type for %s", hdr.Name)
		}
	}
	return nil
}

func tarTarget(dest, name string) (string, error) {
	clean := filepath.Clean(name)
	if clean == "." || !filepath.IsLocal(clean) {
		return "", fmt.Errorf("invalid tar path: %s", name)
	}
	if clean == DockerfileStaging || strings.HasPrefix(clean, DockerfileStaging+string(os.PathSeparator)) || clean == cacheCompletionMarker {
		return "", fmt.Errorf("cannot use reserved path: %s", name)
	}
	return filepath.Join(dest, clean), nil
}

func ensureDirectory(root, target string) error {
	if err := ensurePathParent(root, target); err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return os.Mkdir(target, 0o755)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("tar directory conflicts with existing path: %s", target)
	}
	return nil
}

func ensureNewPath(root, target string) error {
	if err := ensurePathParent(root, target); err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("tar path already exists: %s", target)
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func ensurePathParent(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || !filepath.IsLocal(relative) {
		return fmt.Errorf("invalid tar target: %s", target)
	}
	current := root
	for _, component := range strings.Split(filepath.Dir(relative), string(os.PathSeparator)) {
		if component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("tar path crosses a non-directory: %s", current)
		}
	}
	return nil
}
