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

func TestDNSConfigMarshalKeepsBuildkitKeys(t *testing.T) {
	config := DefaultConfig
	config.DNS = &DNSConfig{
		Nameservers:   []string{"127.0.0.1"},
		Options:       []string{"ndots:1"},
		SearchDomains: []string{"build.internal"},
	}

	data, err := toml.Marshal(config)
	if err != nil {
		t.Fatalf("toml.Marshal(config) error = %v", err)
	}

	output := string(data)
	for _, want := range []string{
		"[dns]",
		"nameservers = ['127.0.0.1']",
		"options = ['ndots:1']",
		"searchDomains = ['build.internal']",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("marshaled DNS config missing %q:\n%s", want, output)
		}
	}
}

func marshalToMap(t *testing.T, cfg BuildkitdConfig) map[string]any {
	t.Helper()
	data, err := toml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := toml.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", data, err)
	}
	return got
}

// buildkitd v0.34 logs a deprecation warning when the top-level debug option is
// true. Debug logging must be requested through the [log] section instead.
func TestDebugLoggingUsesLogLevel(t *testing.T) {
	cfg := DefaultConfig
	cfg.Log = &LogConfig{Level: "debug"}

	got := marshalToMap(t, cfg)

	logSection, ok := got["log"].(map[string]any)
	if !ok {
		t.Fatalf("expected a [log] section, got %v", got["log"])
	}
	if level := logSection["level"]; level != "debug" {
		t.Errorf("log.level = %v, want debug", level)
	}
	if debug := got["debug"]; debug != false {
		t.Errorf("top-level debug = %v, want false (the deprecated option must stay unset)", debug)
	}
	if trace := got["trace"]; trace != false {
		t.Errorf("top-level trace = %v, want false (the deprecated option must stay unset)", trace)
	}
}

// Without --debug there is no [log] section, so buildkitd keeps its default level.
func TestDefaultConfigHasNoLogSection(t *testing.T) {
	got := marshalToMap(t, DefaultConfig)

	if logSection, present := got["log"]; present {
		t.Errorf("default config unexpectedly has a [log] section: %v", logSection)
	}
}
