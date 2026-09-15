//===----------------------------------------------------------------------===//
// Copyright © 2026 Apple Inc. and the container-builder-shim project authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//===----------------------------------------------------------------------===//

package utils

import "testing"

func TestBuildPlatformsReturnsPreferredArchitecturesFirst(t *testing.T) {
	platforms := BuildPlatforms()
	if len(platforms) != 9 {
		t.Fatalf("BuildPlatforms returned %d platforms, want 9", len(platforms))
	}
	if platforms[0].OS != "linux" {
		t.Fatalf("first platform OS = %q, want linux", platforms[0].OS)
	}
	if platforms[0].Architecture != "amd64" && platforms[0].Architecture != "arm64" {
		t.Fatalf("first architecture = %q, want a native BuildKit architecture", platforms[0].Architecture)
	}
}

func TestPlatformRank(t *testing.T) {
	if got := platformRank("arm64", "arm64"); got != 0 {
		t.Fatalf("arm64 rank on arm64 = %d, want 0", got)
	}
	if got := platformRank("amd64", "amd64"); got != 0 {
		t.Fatalf("amd64 rank on amd64 = %d, want 0", got)
	}
	if got := platformRank("unknown", "amd64"); got != 8 {
		t.Fatalf("unknown rank = %d, want 8", got)
	}
}
