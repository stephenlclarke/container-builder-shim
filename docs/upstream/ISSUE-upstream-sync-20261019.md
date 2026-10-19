# Synchronize BuildKit metadata decoding with upstream

## Problem

Apple's `c328f217dbf247695f038ffa959f12049e97cb29` fixes URL-encoded BuildKit filesync metadata. Without it, non-ASCII patterns can stop all `.dockerignore` patterns matching. The fork also keeps exclusions as an array for fsutil so literal commas remain part of a pattern.

## Expected behavior

Retain Apple's metadata decoding and invalid-encoding errors while preserving the fork's fsutil filter and request-scoped synthetic Dockerfile handling. Decode array values individually without converting exclusions into comma-separated patterns.

## Validation and compatibility

The upstream decoding cases cover Unicode, malformed escapes and flags, and unencoded percent/plus characters. The adapted regression also checks a literal comma in an encoded pattern. No dependency or publication pins change. See [implementation](PR-upstream-sync-20261019.md).
