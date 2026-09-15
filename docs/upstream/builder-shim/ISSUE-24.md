# Issue 24: establish an authoritative SonarQube gate

## Problem description

The Container-family SonarQube inventory had no project or workflow for `container-builder-shim`. The fork therefore published release-quality builder images without a previous-version quality gate, imported Go coverage, or an authoritative SonarQube security analysis. A first diagnostic scan also found production diagnostic listeners bound to every network interface, workflow permissions broader than their consuming jobs, findings attributed to vendored dependencies, and a race between prefetch work registration and shutdown.

## Requested outcome

- Create a public SonarQube project for the Stephen-owned fork.
- Apply the explicit `previous_version` new-code policy and record each exact commit SHA as `sonar.projectVersion`.
- Import Go coverage and wait for the quality gate on pull requests and branches.
- Require zero unresolved new issues and security hotspots.
- Bind optional production diagnostics only to loopback.
- Grant GitHub token permissions at the narrowest consuming job.
- Exclude generated protobufs and vendored dependencies without excluding maintained Go code.
- Prevent prefetch operations from being registered after shutdown starts and retain the race/leak suite as regression evidence.
- Add the same CI and SonarQube badge set used by the other Container-family repositories.

## Acceptance evidence

- Unit, race/leak, coverage, vet, lint, workflow, and Markdown checks pass.
- The exact pull-request head passes the SonarQube quality gate with no new issue or hotspot.
- The main analysis reports its exact lowercase 40-character source SHA as the project version.
- The public dashboard uses `main` as its main branch and the explicit previous-version policy.
- The existing builder image and current-image publication workflows remain green.

## Related work

This implementation closes [issue 24](https://github.com/stephenlclarke/container-builder-shim/issues/24). It is a fork-quality and release-evidence change; it does not alter the builder protocol, build semantics, or the immutable image pin consumed by Container.
