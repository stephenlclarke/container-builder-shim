# Issue 11: skip uploads for cached build contexts

## Problem description

Repeated builds upload and drain an identical build-context tar even when the builder already has a verified, completion-marked content-addressed cache entry. The redundant transfer adds host CPU, I/O, memory pressure, and warm-build latency.

## Requested outcome

- Add an additive builder RPC that reports whether a lowercase SHA-256 context digest is present.
- Reuse only cache entries that have been atomically published with the completion marker.
- Let a cache-hit transfer finish after its metadata header.
- Continue draining the body from older clients that do not query the cache first.
- Reject malformed digests before resolving cache paths.
- Publish a validated immutable builder image for the matching Container client.

## Acceptance evidence

- Focused tests cover present, missing, incomplete, malformed, optimized, and legacy transfer paths.
- The full Go suite, vet, lint, formatting, generated bindings, and diff checks pass.
- The Container client pins the matching merged source and immutable builder image.
- Same-host Compose benchmarks retain functional parity while measuring the combined optimization stack.

## Related work

This implementation closes [issue 11](https://github.com/stephenlclarke/container-builder-shim/issues/11). It supports [Container pull request 142](https://github.com/stephenlclarke/container/pull/142), [Containerization pull request 37](https://github.com/stephenlclarke/containerization/pull/37), and the cross-repository performance objective in [Compose issue 278](https://github.com/stephenlclarke/container-compose/issues/278).

The benchmarked implementation was merged as
[builder-shim pull request 12](https://github.com/stephenlclarke/container-builder-shim/pull/12).
As of 28 August 2026, neither this optimization nor its matched Container and
Containerization changes has a submitted Apple upstream pull request. Local
Apple-shaped branches remain submission candidates rather than upstream PRs.
