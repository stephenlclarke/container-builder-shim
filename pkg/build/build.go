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

package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/cmd/buildctl/build"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/sirupsen/logrus"
	"github.com/tonistiigi/go-csvvalue"
	"google.golang.org/grpc"
)

func Build(ctx context.Context, opts *BOpts) error {
	buildkit, err := newBuildkitClient(ctx)
	if err != nil {
		return err
	}
	defer buildkit.Close()

	exports, err := configuredExports(opts)
	if err != nil {
		return err
	}
	solveOpt, err := newSolveOptions(opts, exports)
	if err != nil {
		return err
	}

	_, err = buildkit.Build(opts.Context(ctx), solveOpt, "", frontend, opts.ProgressWriter.Status())
	<-opts.ProgressWriter.Done()
	return err
}

func newBuildkitClient(ctx context.Context) (*client.Client, error) {
	grpcOpts := []grpc.DialOption{
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),

		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(512<<20), // 512MB
			grpc.MaxCallSendMsgSize(512<<20), // 512MB
		),
	}
	var clientOpts []client.ClientOpt
	for _, opt := range grpcOpts {
		clientOpts = append(clientOpts, client.WithGRPCDialOption(opt))
	}

	buildkit, err := client.New(ctx, "", clientOpts...)
	if err != nil {
		logrus.Debugf("failed to connect to buildkit")
		return nil, err
	}
	return buildkit, nil
}

func configuredExports(opts *BOpts) ([]client.ExportEntry, error) {
	if opts.Check {
		return nil, nil
	}
	exports, err := parseOutput(opts.Outputs)
	if err != nil {
		return nil, err
	}

	if len(exports) == 0 {
		exports = append(exports, client.ExportEntry{Type: "oci", Attrs: map[string]string{}})
	}

	wf, err := exportWriter(exports, opts.BuildID)
	if err != nil {
		return nil, err
	}
	configured := make([]client.ExportEntry, 0, len(exports))
	for _, export := range exports {
		entry, err := configureExport(export, opts, wf)
		if err != nil {
			discardExportWriter(wf)
			return nil, err
		}
		configured = append(configured, entry)
	}
	return configured, nil
}

func discardExportWriter(writer io.WriteCloser) {
	wrapped, ok := writer.(*wrappedWriteCloser)
	if !ok {
		return
	}
	_ = wrapped.f.Close()
	_ = os.Remove(wrapped.f.Name())
}

func exportWriter(exports []client.ExportEntry, buildID string) (io.WriteCloser, error) {
	for _, export := range exports {
		if export.Type == client.ExporterLocal {
			continue
		}
		file, err := os.CreateTemp("", "")
		if err != nil {
			return nil, err
		}
		// Buffer to disk before copying to virtiofs to prevent unbounded gRPC buffer growth.
		return &wrappedWriteCloser{f: file, dest: filepath.Join(GlobalExportPath, buildID, "out.tar")}, nil
	}
	return nil, nil
}

func configureExport(export client.ExportEntry, opts *BOpts, writer io.WriteCloser) (client.ExportEntry, error) {
	if export.Attrs == nil {
		export.Attrs = map[string]string{}
	}
	if export.Type == client.ExporterLocal {
		localDest := filepath.Join(GlobalExportPath, opts.BuildID, "local")
		if err := os.MkdirAll(localDest, 0o755); err != nil {
			return export, err
		}
		if export.OutputDir == "" {
			export.OutputDir = localDest
		}
		export.Attrs["dest"] = localDest
	} else {
		export.Output = func(map[string]string) (io.WriteCloser, error) { return writer, nil }
		export.Attrs["output"] = filepath.Join(GlobalExportPath, opts.BuildID, "out.tar")
	}
	setDefaultExportAttributes(export.Attrs, opts.Tag)
	return export, nil
}

func setDefaultExportAttributes(attributes map[string]string, tag string) {
	if _, ok := attributes["name"]; !ok {
		attributes["name"] = tag
	}
	const imageNameAnnotation = "annotation-index-descriptor.com.apple.containerization.image.name"
	if _, ok := attributes[imageNameAnnotation]; !ok {
		attributes[imageNameAnnotation] = tag
	}
}

func newSolveOptions(opts *BOpts, exports []client.ExportEntry) (client.SolveOpt, error) {
	cacheImports, err := build.ParseImportCache(opts.CacheIn)
	if err != nil {
		return client.SolveOpt{}, err
	}

	cacheExports, err := build.ParseExportCache(opts.CacheOut)
	if err != nil {
		return client.SolveOpt{}, err
	}

	solveOpt := client.SolveOpt{
		Exports:      exports,
		CacheImports: cacheImports,
		CacheExports: cacheExports,
		Session: []session.Attachable{
			opts.FSSync,
			secretsprovider.FromMap(opts.Secrets),
		},
		FrontendAttrs: map[string]string{},
	}
	solveOpt.OCIStores = map[string]content.Store{
		KeyContentStoreName: opts.ContentStore,
	}

	for k, v := range opts.dockerfileFrontendAttrs() {
		solveOpt.FrontendAttrs[k] = v
	}
	solveOpt.AllowedEntitlements = append(solveOpt.AllowedEntitlements, opts.Entitlements...)

	for k, v := range opts.BuildArgs {
		solveOpt.FrontendAttrs["build-arg:"+k] = v
	}

	var platformStrings []string
	for _, platform := range opts.Platforms {
		platformStrings = append(platformStrings, platforms.Format(platforms.Normalize(platform)))
	}
	if len(opts.Platforms) > 0 {
		solveOpt.FrontendAttrs["platform"] = strings.Join(platformStrings, ",")
	}
	if len(opts.Platforms) > 1 {
		solveOpt.FrontendAttrs["multi-platform"] = "true"
	}
	if opts.Target != "" {
		solveOpt.FrontendAttrs["target"] = opts.Target
	}
	for k, v := range opts.Labels {
		solveOpt.FrontendAttrs["label:"+k] = v
	}
	for k, v := range opts.Attestations {
		solveOpt.FrontendAttrs[k] = v
	}
	solveOpt.Frontend = "dockerfile.v1"

	if len(opts.SSH) > 0 {
		sshProvider, err := sshprovider.NewSSHAgentProvider(opts.SSH)
		if err != nil {
			return client.SolveOpt{}, err
		}
		solveOpt.Session = append(solveOpt.Session, sshProvider)
	}
	return solveOpt, nil
}

type wrappedWriteCloser struct {
	f    *os.File
	dest string
}

func (w *wrappedWriteCloser) Write(p []byte) (n int, err error) {
	return w.f.Write(p)
}

func (w *wrappedWriteCloser) Close() error {
	defer w.f.Close()
	defer os.RemoveAll(w.f.Name())

	if err := w.f.Sync(); err != nil {
		return err
	}

	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return err
	}

	outFile, err := os.Create(w.dest)
	if err != nil {
		return err
	}
	defer outFile.Close()

	if _, err := io.CopyBuffer(outFile, w.f, make([]byte, 1<<20)); err != nil {
		return err
	}
	return nil
}

// parseOutput parses CSV output strings and returns ExportEntry slice.
// It validates the output types and allows type=local without dest field.
// Supported types: oci, tar, local
func parseOutput(outputs []string) ([]client.ExportEntry, error) {
	var entries []client.ExportEntry

	for _, output := range outputs {
		entry, err := parseOutputCSV(output)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// parseOutputCSV parses a single CSV output string into an ExportEntry
func parseOutputCSV(output string) (client.ExportEntry, error) {
	entry := client.ExportEntry{
		Attrs: make(map[string]string),
	}

	// Parse CSV fields
	fields, err := csvvalue.Fields(output, nil)
	if err != nil {
		return entry, fmt.Errorf("failed to parse CSV: %w", err)
	}

	// Process each field
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return entry, fmt.Errorf("invalid field format: %s (expected key=value)", field)
		}

		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)

		switch key {
		case "type":
			entry.Type = value
		default:
			entry.Attrs[key] = value
		}
	}

	// Validate type is provided
	if entry.Type == "" {
		return entry, errors.New("output type is required (type=<type>)")
	}

	// Validate supported types
	switch entry.Type {
	case "oci", "tar", "local":
		// These are the supported types
	default:
		return entry, fmt.Errorf("unsupported output type: %s (supported: oci, tar, local)", entry.Type)
	}

	// No path validation - just return the parsed entry

	return entry, nil
}
