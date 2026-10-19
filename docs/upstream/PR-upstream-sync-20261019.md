# fix(sync): incorporate upstream BuildKit metadata decoding

## Problem and behavior

Merge the current Apple builder-shim source while adapting metadata decoding to the fork's array-valued fsutil exclusions. Unicode patterns decode correctly; comma-containing filenames remain one pattern; malformed metadata fails explicitly. Synthetic Dockerfile isolation and host boundary handling remain unchanged.

## Validation

Run the complete Go test suite with coverage, Go vet, Linux production cross-compilation, formatting checks and scoped Markdown lint against the signed merge commit. Retain raw outputs and exact source identity separately. Existing release artifacts and benchmark baselines remain immutable.

## Compatibility and risks

This is a source synchronization, not a new builder image release or qualification of upper-layer consumers. A newly published image still requires the normal release gates. Related: [issue](ISSUE-upstream-sync-20261019.md) and [Apple PR 103](https://github.com/apple/container-builder-shim/pull/103).
