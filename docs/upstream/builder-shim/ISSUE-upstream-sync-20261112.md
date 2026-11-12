# Update the builder fork from Apple main

Apple main is at `1058d0591a6e0c184b35523612a199af309823f3`.
The fork was at `fba00bd41542bfb0b31c6107e7fd066a701cc47b`.

Keep the fork features while bringing in Apple dependency, logging and file
transfer fixes. Keep generated Dockerfiles outside the shared file cache.

Apple merged [PR 100](https://github.com/apple/container-builder-shim/pull/100).
The newer gRPC 1.84.0 includes that fix. The new dependency set also meets or
exceeds the versions from the fork security update, PR 30. That update is
superseded in the rebased history; its security fixes remain included.

See [the change record](PR-upstream-sync-20261112.md).
