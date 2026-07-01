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

package content

import (
	"context"
	"fmt"

	contentx "github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/errdefs"

	"github.com/google/uuid"

	"github.com/apple/container-builder-shim/pkg/api"
	"github.com/apple/container-builder-shim/pkg/stream"
)

var (
	_ contentx.Store = &ContentStoreProxy{}
	_ stream.Stage   = &ContentStoreProxy{}
)

// A content store that proxies requests over the bidirectional grpc stream.
// BuildKit only needs to read committed content through this proxy. Ingestion
// APIs return ErrNotImplemented instead of panicking if BuildKit probes them.
type ContentStoreProxy struct {
	contentx.Store
	stream.UnimplementedBaseStage
}

func NewContentStoreProxy() (*ContentStoreProxy, error) {
	contentProxy := new(ContentStoreProxy)
	return contentProxy, nil
}

func (r *ContentStoreProxy) Filter(c *api.ClientStream) error {
	if it := c.GetImageTransfer(); it != nil {
		if it.Metadata["stage"] == r.String() {
			return nil
		}
	}
	return stream.ErrIgnorePacket
}

func (r *ContentStoreProxy) request(ctx context.Context, packet *api.ImageTransfer) (*api.ImageTransfer, error) {
	cancellableCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	id := uuid.NewString()
	if packet.Id == "" {
		packet.Id = id
	}
	resp, err := r.Request(cancellableCtx, &api.ServerStream{
		BuildId: id,
		PacketType: &api.ServerStream_ImageTransfer{
			ImageTransfer: packet,
		},
	}, id, stream.FilterByBuildID)
	if err != nil {
		return nil, err
	}

	imageTransfer := resp.GetImageTransfer()
	if imageTransfer == nil {
		return nil, fmt.Errorf("content store response missing image transfer")
	}
	return imageTransfer, nil
}

func (r *ContentStoreProxy) String() string {
	return "content-store"
}

func (r *ContentStoreProxy) Writer(ctx context.Context, opts ...contentx.WriterOpt) (contentx.Writer, error) {
	return nil, unsupportedContentIngestion("Writer")
}

func (r *ContentStoreProxy) Status(ctx context.Context, ref string) (contentx.Status, error) {
	return contentx.Status{}, unsupportedContentIngestion("Status")
}

func (r *ContentStoreProxy) ListStatuses(ctx context.Context, filters ...string) ([]contentx.Status, error) {
	return nil, unsupportedContentIngestion("ListStatuses")
}

func (r *ContentStoreProxy) Abort(ctx context.Context, ref string) error {
	return unsupportedContentIngestion("Abort")
}

func unsupportedContentIngestion(method string) error {
	return fmt.Errorf("content store %s is not supported by read-through proxy: %w", method, errdefs.ErrNotImplemented)
}
