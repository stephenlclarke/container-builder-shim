//===----------------------------------------------------------------------===//
// Copyright © 2025-2026 Apple Inc. and the container-builder-shim project authors.
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

import (
	"sort"

	"github.com/containerd/platforms"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

func BuildPlatforms() []ocispecs.Platform {
	pl := func(arch, variant string) ocispecs.Platform {
		return ocispecs.Platform{
			Architecture: arch,
			Variant:      variant,
			OS:           "linux",
		}
	}
	pls := []ocispecs.Platform{
		pl("arm64", "v8"),
		pl("arm", "v7"),
		pl("arm", "v6"),
		pl("amd64", ""),
		pl("mips64", ""),
		pl("mips64el", ""),
		pl("ppc64le", ""),
		pl("riscv64", ""),
		pl("s390x", ""),
	}
	currentPlatform := platforms.DefaultSpec()

	// order of platforms matters because this is the order in which
	// buildkit pulls dependencies for a build
	sort.SliceStable(pls, func(i, j int) bool {
		if currentPlatform.Architecture != "amd64" && currentPlatform.Architecture != "arm64" {
			return pls[i].Architecture < pls[j].Architecture
		}
		return platformRank(pls[i].Architecture, currentPlatform.Architecture) <
			platformRank(pls[j].Architecture, currentPlatform.Architecture)
	})
	return pls
}

func platformRank(architecture, currentArchitecture string) int {
	order := []string{"amd64", "arm64", "arm", "mips64", "mips64el", "ppc64le", "riscv64", "s390x"}
	if currentArchitecture == "arm64" {
		order = []string{"arm64", "arm", "amd64", "mips64", "mips64el", "ppc64le", "riscv64", "s390x"}
	}
	for rank, candidate := range order {
		if architecture == candidate {
			return rank
		}
	}
	return len(order)
}
