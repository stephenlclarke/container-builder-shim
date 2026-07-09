#!/usr/bin/env bash
# Copyright © 2025-2026 Apple Inc. and the container-builder-shim project authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#   https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

if command -v .local/bin/hawkeye >/dev/null 2>&1; then
    echo "hawkeye already installed"
    exit 0
fi

VERSION=v6.5.1

# Select the pinned Hawkeye release artifact for the current platform.
case "$(uname -s)/$(uname -m)" in
    Darwin/arm64)
        ARTIFACT="hawkeye-aarch64-apple-darwin.tar.xz"
        EXPECTED_SHA256="99777f21e4e56c9946ed93621885532c6a0476377f497565c583f5911f2cbb1f"
        ;;
    Darwin/x86_64)
        ARTIFACT="hawkeye-x86_64-apple-darwin.tar.xz"
        EXPECTED_SHA256="23d53443fd810df74b21f6f82ccc3d1db7df6c33962239dcefd581cef74b59c1"
        ;;
    Linux/x86_64)
        ARTIFACT="hawkeye-x86_64-unknown-linux-gnu.tar.xz"
        EXPECTED_SHA256="d6eb0505a45a15244f4f789158aafe5e3f1a7dc86c9dc1d7651f3cb1e1b321e0"
        ;;
    Linux/aarch64|Linux/arm64)
        ARTIFACT="hawkeye-aarch64-unknown-linux-gnu.tar.xz"
        EXPECTED_SHA256="51962edc7008658d7d44f637ea33582bb441635496c617b2a63ca61ab4ed43b6"
        ;;
    *)
        echo "error: install-hawkeye.sh does not support $(uname -s)/$(uname -m)" >&2
        exit 1
        ;;
esac

ARTIFACT_URL="https://github.com/korandoru/hawkeye/releases/download/${VERSION}/${ARTIFACT}"

echo "Installing hawkeye ${VERSION}"
workdir="$(mktemp -d)"
trap 'rm -rf "${workdir}"' EXIT
tarball="${workdir}/${ARTIFACT}"

# Download the tarball, verify it against the pinned checksum (aborts on
# mismatch), then extract just the hawkeye binary into .local/bin.
curl --proto '=https' --tlsv1.2 -LsSf "${ARTIFACT_URL}" -o "${tarball}"
echo "${EXPECTED_SHA256}  ${tarball}" | shasum -a 256 -c -

tar -xf "${tarball}" --strip-components 1 -C "${workdir}"
mkdir -p .local/bin
mv "${workdir}/hawkeye" .local/bin/hawkeye
chmod +x .local/bin/hawkeye

echo "hawkeye ${VERSION} installed to .local/bin/hawkeye"
