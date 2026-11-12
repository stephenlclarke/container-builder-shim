# Rebase the builder fork onto Apple main

The rebase retains the fork changes and includes Apple main at `1058d059`.
It uses Go 1.26.8 and BuildKit 0.34.1. Both build images are pinned by digest.
Dependencies and vendor files are regenerated with the selected Go version.

The file transfer code requests the whole context when BuildKit sends no file
list. It still supplies generated Dockerfiles and applies the full exclude
rules. Encoded exclude rules retain commas inside a filename. Debug logging
uses the new BuildKit log setting and retains the fork's diagnostic address.

The old Dockerfile path scan is removed because it could omit required files.

## Validation

- All Go tests pass.
- Go vet passes.
- The Linux arm64 build passes.
- Dependency regeneration and module verification pass.
- All Go race tests pass.
- Final review found no further issue after the file filtering fix.

The protected main branch does not permit a force push. The reviewed rebase
is retained on `sync/apple-main-20261112` for publication.

See [the issue record](ISSUE-upstream-sync-20261112.md).
