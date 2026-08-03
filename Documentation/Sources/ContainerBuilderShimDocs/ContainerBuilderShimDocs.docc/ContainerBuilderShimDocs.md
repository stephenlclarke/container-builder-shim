# ``ContainerBuilderShimDocs``

Understand the bridge between Apple container build requests and BuildKit.

## Overview

`container-builder-shim` is implemented in Go. This DocC catalogue provides one navigable developer guide alongside the Swift API references in the container project family. It documents the service boundary, package ownership, and primary extension points, then links to the maintained Go source for symbol-level detail.

This guide is generated from [Stephen's fork](https://github.com/stephenlclarke/container-builder-shim). The [Apple upstream repository](https://github.com/apple/container-builder-shim) remains available as a secondary reference.

DocC does not ingest Go symbol graphs, so the Swift namespace on this page is only the documentation host. It is not part of the builder shim runtime or release artefacts.

## Topics

### Architecture

- <doc:BuilderArchitecture>
- <doc:GoPackages>

### Documentation host

- ``ContainerBuilderShimDocumentation``
