# Issue 26: clear inherited SonarQube findings on main

## Problem description

The first authoritative `main` analysis for `container-builder-shim` correctly
refused publication after finding 36 unresolved maintained-code issues. The
standard quality gate was green because the repository had no comparable prior
version, so the strict repository check exposed debt that would otherwise have
silently become old code at the next version boundary.

## Requested outcome

- Remove every actionable maintained-code issue reported by the first main scan.
- Retain only exact-file analyzer dispositions for required interface contexts,
  the loopback-only diagnostic endpoint, and root inside the isolated builder VM.
- Split complex build, prefetch, stream, cache, platform, and test paths into
  focused units without changing the builder protocol.
- Remove dead local-file branches and unused state.
- Make branch and main scans reject all unresolved issues and hotspots, while
  pull-request scans reject all unresolved new-code findings.
- Keep full unit, race, leak, workflow, and coverage evidence.

## Acceptance evidence

- All Go tests and the race/leak suite pass.
- Workflow and Markdown lint pass.
- The pull request reports no new issues or hotspots.
- The exact merged-main SHA reports zero unresolved maintained-code issues and
  zero security hotspots.
- Current-image publication is withheld until that main analysis succeeds.

## Related work

This implementation closes [issue 26](https://github.com/stephenlclarke/container-builder-shim/issues/26)
through [pull request 27](https://github.com/stephenlclarke/container-builder-shim/pull/27)
and follows the gate introduced by [pull request 25](https://github.com/stephenlclarke/container-builder-shim/pull/25).
