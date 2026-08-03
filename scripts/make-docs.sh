#!/usr/bin/env bash
# Copyright 2026 container-builder-shim project authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

if (( $# != 2 )); then
    printf 'usage: %s OUTPUT_PATH HOSTING_BASE_PATH\n' "$0" >&2
    exit 2
fi

output_path="$1"
hosting_base_path="$2"
repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
scratch_path="${DOCS_SCRATCH_PATH:-$repository_root/.build/docc}"
source_reference="${DOCS_SOURCE_REFERENCE:-${GITHUB_SHA:-main}}"

arguments=(
    --package-path "$repository_root/Documentation"
    --scratch-path "$scratch_path"
    --allow-writing-to-directory "$output_path"
    generate-documentation
    --target ContainerBuilderShimDocs
    --output-path "$output_path"
    --disable-indexing
    --transform-for-static-hosting
    --hosting-base-path "$hosting_base_path"
    --source-service github
    --source-service-base-url "https://github.com/stephenlclarke/container-builder-shim/blob/$source_reference/Documentation"
    --checkout-path "$repository_root/Documentation"
)

swift package "${arguments[@]}"

printf '{}\n' > "$output_path/theme-settings.json"
cat > "$output_path/index.html" <<'EOF'
<!doctype html>
<html lang="en-US">
  <head>
    <meta charset="utf-8">
    <title>container-builder-shim documentation</title>
    <meta http-equiv="refresh" content="0; url=./documentation/containerbuildershimdocs/">
  </head>
  <body>
    <p>If you are not redirected automatically, <a href="./documentation/containerbuildershimdocs/">open the container-builder-shim developer guide</a>.</p>
  </body>
</html>
EOF
