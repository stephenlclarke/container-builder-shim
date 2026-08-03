# Go Packages

Use the package map to find the maintained implementation behind each builder capability.

| Package | Responsibility |
| --- | --- |
| [`pkg/api`](https://github.com/stephenlclarke/container-builder-shim/tree/main/pkg/api) | Generated Builder protobuf and gRPC contracts. |
| [`pkg/build`](https://github.com/stephenlclarke/container-builder-shim/tree/main/pkg/build) | Build request parsing, frontend selection, solve options, and BuildKit execution. |
| [`pkg/buildkit`](https://github.com/stephenlclarke/container-builder-shim/tree/main/pkg/buildkit) | BuildKit daemon configuration and lifecycle. |
| [`pkg/content`](https://github.com/stephenlclarke/container-builder-shim/tree/main/pkg/content) | Host-backed content-store access. |
| [`pkg/exporter`](https://github.com/stephenlclarke/container-builder-shim/tree/main/pkg/exporter) | Build output and metadata transfer. |
| [`pkg/fssync`](https://github.com/stephenlclarke/container-builder-shim/tree/main/pkg/fssync) | Build-context file synchronisation and BuildKit filesync adaptation. |
| [`pkg/prefetch`](https://github.com/stephenlclarke/container-builder-shim/tree/main/pkg/prefetch) | Bounded read-ahead for remotely supplied content. |
| [`pkg/resolver`](https://github.com/stephenlclarke/container-builder-shim/tree/main/pkg/resolver) | Image reference resolution across the host boundary. |
| [`pkg/server`](https://github.com/stephenlclarke/container-builder-shim/tree/main/pkg/server) | Builder service lifecycle and request handling. |
| [`pkg/stdio`](https://github.com/stephenlclarke/container-builder-shim/tree/main/pkg/stdio) | Standard I/O and terminal command bridging. |
| [`pkg/stream`](https://github.com/stephenlclarke/container-builder-shim/tree/main/pkg/stream) | Bidirectional stream filtering, staging, and demultiplexing. |
