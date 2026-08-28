# Issue 13: repair matched Compose documentation links

## Problem

The fork README linked to the former root-level Compose `STATUS.md` and
`BRANCHES.md` locations after the matched Compose repository reorganized its
documentation under `docs/`.

## Required outcome

- Point the status link at `docs/project/STATUS.md`.
- Replace the removed branch guide with the maintained build and worktree guide
  at `docs/guides/BUILD.md`.
- Validate the Markdown and both repository-relative GitHub targets before the
  0.14.0 documentation sites are rebuilt.

Tracking issue:
[#13](https://github.com/stephenlclarke/container-builder-shim/issues/13).
Implementation:
[pull request 14](https://github.com/stephenlclarke/container-builder-shim/pull/14).

Related release work:
[Container Compose issue 332](https://github.com/stephenlclarke/container-compose/issues/332)
and
[pull request 333](https://github.com/stephenlclarke/container-compose/pull/333).
