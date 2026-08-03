# Builder Architecture

Follow a build request across the host and builder-container boundary.

## Request flow

1. `container` starts the shim and sends build requests over the Builder gRPC API.
2. The server translates request metadata into BuildKit frontend and solve options.
3. File sync, content, resolver, exporter, and stdio proxies bridge BuildKit session calls back to the macOS host.
4. BuildKit performs the solve and returns progress, output, metadata, or structured errors through the shim.

The shim owns the builder-container side of this protocol. The macOS host remains responsible for enforcing the source context boundary and for runtime/image operations that cannot execute inside the builder container.

## Primary boundaries

- The generated [`Builder.proto`](https://github.com/stephenlclarke/container-builder-shim/blob/main/pkg/api/Builder.proto) contract defines the host/shim message surface.
- [`pkg/server`](https://github.com/stephenlclarke/container-builder-shim/tree/main/pkg/server) owns the service lifecycle and request dispatch.
- [`pkg/build`](https://github.com/stephenlclarke/container-builder-shim/tree/main/pkg/build) maps requests to BuildKit solve operations.
- [`pkg/fssync`](https://github.com/stephenlclarke/container-builder-shim/tree/main/pkg/fssync) and [`pkg/fileutils`](https://github.com/stephenlclarke/container-builder-shim/tree/main/pkg/fileutils) implement the build-context transfer boundary.
