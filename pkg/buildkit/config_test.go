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

package buildkit

import (
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestDefaultConfigMarshalKeepsExpectedKeys(t *testing.T) {
	data, err := toml.Marshal(DefaultConfig)
	if err != nil {
		t.Fatalf("toml.Marshal(DefaultConfig) error = %v", err)
	}

	output := string(data)
	for _, want := range []string{
		"debug = false",
		"trace = false",
		"[worker.oci]",
		"enabled = true",
		"binary = ",
		"/usr/bin/buildkit-runc",
		"gc = true",
		"gckeepstorage = 34359738368",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("marshaled DefaultConfig missing %q:\n%s", want, output)
		}
	}
}
