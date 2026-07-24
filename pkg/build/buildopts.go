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

package build

import (
	"bytes"
	"context"
	"encoding/base64"
	"path/filepath"
	"strings"

	"github.com/containerd/platforms"
	bkattestations "github.com/moby/buildkit/frontend/attestations"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/moby/buildkit/util/entitlements"
	"github.com/moby/buildkit/util/progress/progresswriter"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/apple/container-builder-shim/pkg/build/utils"
	"github.com/apple/container-builder-shim/pkg/content"
	"github.com/apple/container-builder-shim/pkg/fssync"
	"github.com/apple/container-builder-shim/pkg/resolver"
	"github.com/apple/container-builder-shim/pkg/stdio"
)

const (
	// Name used to identify the content store.
	KeyContentStoreName = "container"
	// Base64-encoded Dockerfile contents.
	KeyDockerfile = "dockerfile"
	// Base64-encoded Dockerignore contents.
	KeyDockerignore = "dockerignore"
	// Image reference (name:tag) to assign to the built image.
	KeyTag = "tag"
	// Target platforms to build the image for.
	KeyPlatforms = "platforms"
	// Progress output mode: auto, tty, or plain.
	KeyProgress = "progress"
	// When present, disables layer caching.
	KeyNoCache = "no-cache"
	// Build context directory path.
	KeyContext = "context"
	// Dockerfile stage to build up to.
	KeyTarget = "target"
	// Key=value metadata labels to apply to the image.
	KeyLabels = "labels"
	// ARG key=value pairs passed to the Dockerfile.
	KeyBuildArgs = "build-args"
	// Additional named build contexts.
	KeyBuildContexts = "build-contexts"
	// RUN --mount=type=secret,... id:value pairs passed to the Dockerfile.
	KeySecrets = "secrets"
	// SSH agent to forward during the build process.
	KeySSH = "ssh"
	// BuildKit entitlements allowed for the build.
	KeyEntitlements = "entitlements"
	// Additional hosts to expose during Dockerfile RUN steps.
	KeyAddHosts = "add-hosts"
	// Default Dockerfile RUN network mode.
	KeyNetwork = "network"
	// Allow privileged Dockerfile RUN steps.
	KeyPrivileged = "privileged"
	// Size in bytes for /dev/shm in Dockerfile RUN steps.
	KeyShmSize = "shm-size"
	// Ulimit values for Dockerfile RUN steps.
	KeyUlimit = "ulimit"
	// Cache import sources.
	KeyCacheIn = "cache-in"
	// Cache export destinations.
	KeyCacheOut = "cache-out"
	// Additional export destinations.
	KeyOutput = "outputs"
	// Run Dockerfile build checks without exporting an image.
	KeyCheck = "check"
	// Unique build identifier.
	KeyBuildID = "build-id"
	// Provenance attestation metadata key from the host.
	KeyAttestProvenance = "attest-provenance"
	// SBOM attestation metadata key from the host.
	KeyAttestSBOM = "attest-sbom"
	// BuildKit provenance attestation frontend attribute.
	KeyFrontendAttestProvenance = "attest:provenance"
	// BuildKit SBOM attestation frontend attribute.
	KeyFrontendAttestSBOM = "attest:sbom"
)

const (
	// Used to share built artifacts outside VM
	GlobalExportPath = "/var/lib/container-builder-shim/exports"
	// Dockerfile and optional ignore-file contents are staged at
	// DockerfileStaging directory, and buildkit uses them.
	DockerfileStaging = fssync.DockerfileStaging
)

type bOptsContextKey struct{}

var keyBOpts bOptsContextKey

func extractSSHAgentConfigs(values []string) []sshprovider.AgentConfig {
	if len(values) == 0 {
		return nil
	}
	agentConfigs := make([]sshprovider.AgentConfig, 0, len(values))
	for _, value := range values {
		id, path, hasPath := strings.Cut(value, "=")
		id = strings.TrimSpace(id)
		path = strings.TrimSpace(path)
		if !hasPath && strings.HasPrefix(id, "/") {
			path = id
			id = "default"
			hasPath = true
		}
		if id == "" {
			id = "default"
		}
		config := sshprovider.AgentConfig{
			ID: id,
		}
		if hasPath && path != "" {
			config.Paths = []string{path}
		}
		agentConfigs = append(agentConfigs, config)
	}
	return agentConfigs
}

func extractAttestations(contextMap map[string][]string) (map[string]string, error) {
	values := map[string]string{}
	for metadataKey, frontendKey := range map[string]string{
		KeyAttestProvenance: KeyFrontendAttestProvenance,
		KeyAttestSBOM:       KeyFrontendAttestSBOM,
	} {
		rawValues, ok := contextMap[metadataKey]
		if !ok || len(rawValues) == 0 {
			continue
		}
		values[frontendKey] = rawValues[len(rawValues)-1]
	}
	if len(values) == 0 {
		return nil, nil
	}
	if _, err := bkattestations.Parse(values); err != nil {
		return nil, err
	}
	return values, nil
}

func extractBuildContexts(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	contexts := map[string]string{}
	for _, value := range values {
		name, source, ok := strings.Cut(value, "=")
		name = strings.TrimSpace(name)
		source = strings.TrimSpace(source)
		if !ok || name == "" || source == "" {
			return nil, ErrInvalidBuildContext
		}
		contexts[name] = source
	}
	return contexts, nil
}

func normalizedNetworkMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	switch mode {
	case "", "default":
		return "", nil
	case "none", "host", "sandbox":
		return mode, nil
	default:
		return "", ErrInvalidNetworkMode
	}
}

func extractEntitlements(values []string, network string, privileged bool) ([]string, error) {
	entitlementValues := make([]string, 0, len(values)+2)
	entitlementValues = append(entitlementValues, values...)
	if network == "host" {
		entitlementValues = append(entitlementValues, entitlements.EntitlementNetworkHost.String())
	}
	if privileged {
		entitlementValues = append(entitlementValues, entitlements.EntitlementSecurityInsecure.String())
	}

	seen := map[string]struct{}{}
	result := make([]string, 0, len(entitlementValues))
	for _, raw := range entitlementValues {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if _, _, err := entitlements.Parse(value); err != nil {
			return nil, err
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

func hasMetadataFlag(contextMap map[string][]string, key string) bool {
	_, ok := contextMap[key]
	return ok
}

type BOpts struct {
	BuildID        string
	Dockerfile     []byte
	Dockerignore   []byte
	Tag            string
	ContextDir     string
	BuildPlatforms []ocispecs.Platform
	Platforms      []ocispecs.Platform
	NoCache        *string
	Target         string
	BuildArgs      map[string]string
	BuildContexts  map[string]string
	Secrets        map[string][]byte
	SSH            []sshprovider.AgentConfig
	Entitlements   []string
	Attestations   map[string]string
	AddHosts       []string
	Network        string
	ShmSize        string
	Ulimits        []string
	CacheIn        []string
	CacheOut       []string
	Outputs        []string
	Labels         map[string]string
	Check          bool
	ProgressWriter progresswriter.Writer

	ContentStore *content.ContentStoreProxy
	Resolver     *resolver.ResolverProxy
	FSSync       *fssync.FSSyncProxy
	Stdio        *stdio.StdioProxy

	basePath string
}

func NewBuildOpts(ctx context.Context, basePath string, contextMap map[string][]string) (*BOpts, error) {
	first := func(key string) (string, bool) {
		values, ok := contextMap[key]
		if !ok {
			return "", false
		}
		return values[0], true
	}

	buildID, ok := first(KeyBuildID)
	if !ok {
		return nil, ErrMissingBuildID
	}

	dockerfileBase64Bytes, ok := first(KeyDockerfile)
	if !ok {
		return nil, ErrMissingContextDockerfile
	}

	dockerfileBytes, err := base64.StdEncoding.DecodeString(dockerfileBase64Bytes)
	if err != nil {
		return nil, err
	}

	dockerignoreBytes := []byte(DockerfileStaging)
	if dockerignoreBase64Bytes, ok := first(KeyDockerignore); ok {
		dockerignoreBytes, err = base64.StdEncoding.DecodeString(dockerignoreBase64Bytes)
		if err != nil {
			return nil, err
		}

		dockerignoreBytes = append(dockerignoreBytes, []byte("\n"+DockerfileStaging)...)
	}

	progress, ok := first(KeyProgress)
	if !ok {
		progress = "auto"
	}
	switch progress {
	case "auto", "tty", "plain":
	default:
		return nil, ErrInvalidProgress
	}

	var noCache *string
	if values, ok := contextMap[KeyNoCache]; ok {
		value := lastMetadataValue(values)
		noCache = &value
	}

	tag, ok := first(KeyTag)
	if !ok {
		return nil, ErrMissingContextRef
	}

	ctxDir := "."
	if c, ok := first(KeyContext); ok {
		ctxDir = c
	}

	bps := utils.BuildPlatforms()
	if len(bps) == 0 {
		bps = append(bps, platforms.DefaultSpec())
	}

	pls, err := func() ([]ocispecs.Platform, error) {
		pls := []ocispecs.Platform{}
		values, ok := contextMap[KeyPlatforms]
		if !ok {
			return []ocispecs.Platform{platforms.DefaultSpec()}, nil
		}
		for _, plStr := range values {
			pl, err := platforms.Parse(plStr)
			if err != nil {
				return nil, err
			}
			pls = append(pls, pl)
		}
		return pls, nil
	}()
	if err != nil {
		return nil, err
	}

	target := ""
	if tStr, ok := first(KeyTarget); ok {
		target = tStr
	}

	mapExtract := func(key string) map[string]string {
		values, ok := contextMap[key]
		if !ok {
			return map[string]string{}
		}
		args := map[string]string{}
		for _, label := range values {
			parts := strings.SplitN(label, "=", 2)
			switch len(parts) {
			case 1:
				args[parts[0]] = ""
			case 2:
				args[parts[0]] = parts[1]
			}
		}
		return args
	}
	mapExtractB64 := func(key string) (map[string][]byte, error) {
		values, ok := contextMap[key]
		if !ok {
			return map[string][]byte{}, nil
		}
		args := map[string][]byte{}
		for _, label := range values {
			parts := strings.SplitN(label, "=", 2)
			switch len(parts) {
			case 1:
				args[parts[0]] = []byte{}
			case 2:
				dat, err := base64.StdEncoding.DecodeString(parts[1])
				if err != nil {
					return nil, err
				}
				args[parts[0]] = dat
			}
		}
		return args, nil
	}

	labels := mapExtract(KeyLabels)
	buildArgs := mapExtract(KeyBuildArgs)
	buildContexts, err := extractBuildContexts(contextMap[KeyBuildContexts])
	if err != nil {
		return nil, err
	}
	secrets, err := mapExtractB64(KeySecrets)
	if err != nil {
		return nil, err
	}
	ssh := extractSSHAgentConfigs(contextMap[KeySSH])
	network, err := normalizedNetworkMode(lastMetadataValue(contextMap[KeyNetwork]))
	if err != nil {
		return nil, err
	}
	entitlementValues, err := extractEntitlements(contextMap[KeyEntitlements], network, hasMetadataFlag(contextMap, KeyPrivileged))
	if err != nil {
		return nil, err
	}
	attestations, err := extractAttestations(contextMap)
	if err != nil {
		return nil, err
	}
	cacheIn := contextMap[KeyCacheIn]
	cacheOut := contextMap[KeyCacheOut]
	outputs := contextMap[KeyOutput]
	_, check := first(KeyCheck)

	stdioProxy, err := stdio.NewStdioProxy(ctx, progress == "tty")
	if err != nil {
		return nil, err
	}

	dockerfile, err := parser.Parse(bytes.NewReader(dockerfileBytes))
	if err != nil {
		return nil, err
	}

	_, metaArgs, err := instructions.Parse(dockerfile.AST, nil)
	if err != nil {
		return nil, err
	}

	for _, metaArg := range metaArgs {
		for _, arg := range metaArg.Args {
			// Only use the dockerfile meta arg if the user did not overwrite it
			if _, ok := buildArgs[arg.Key]; ok {
				continue
			}
			// Expand with prior args and strip shell quotes
			resolved, err := shell.NewLex('\\').ProcessWordWithMatches(arg.ValueString(), utils.NewMapGetter(buildArgs))
			if err != nil {
				return nil, err
			}
			// Save the resolved value for later use
			buildArgs[arg.Key] = resolved.Result
		}
	}

	pw, err := progresswriter.NewPrinter(ctx, stdioProxy, progress)
	if err != nil {
		return nil, err
	}

	// addedGlobs is the fallback value for followpaths when BuildKit does not
	// supply it. Pre-compute it by scanning the Dockerfile AST for COPY, ADD,
	// and RUN --mount=type=bind source paths so the host packs only the files
	// those instructions need rather than the entire context.
	addedGlobs := []string{}
	for _, node := range dockerfile.AST.Children {
		if strings.EqualFold(node.Value, "COPY") || strings.EqualFold(node.Value, "ADD") {
			addedGlobs = append(addedGlobs, node.Next.Value)
		}

		// Extract source paths from bind mount flags in RUN commands
		if strings.EqualFold(node.Value, "RUN") {
			cmd, err := instructions.ParseInstruction(node)
			if err != nil {
				continue
			} else if runCmd, ok := cmd.(*instructions.RunCommand); ok {
				if err := runCmd.Expand(func(word string) (string, error) {
					// Single word expander to normalize source path
					source := strings.TrimPrefix(word, "/")
					normalized := filepath.Clean(source)
					return normalized, nil
				}); err != nil {
					return nil, err
				}
				mounts := instructions.GetMounts(runCmd)
				for _, mount := range mounts {
					// Only add source paths from bind mounts (not from other stages)
					if mount.Type == instructions.MountTypeBind && mount.Source != "" && mount.From == "" {
						addedGlobs = append(addedGlobs, mount.Source)
					}
				}
			}
		}
	}

	fssyncProxy, err := fssync.NewFSSyncProxy(".", basePath, addedGlobs, dockerfileBytes, dockerignoreBytes)
	if err != nil {
		return nil, err
	}

	contentProxy, err := content.NewContentStoreProxy()
	if err != nil {
		return nil, err
	}

	bopts := &BOpts{
		BuildID:        buildID,
		Dockerfile:     dockerfileBytes,
		Dockerignore:   dockerignoreBytes,
		Tag:            tag,
		BuildPlatforms: bps,
		Platforms:      pls,
		ContextDir:     ctxDir,
		ContentStore:   contentProxy,
		FSSync:         fssyncProxy,
		NoCache:        noCache,
		Resolver:       resolver.NewResolverProxy(),
		ProgressWriter: pw,
		Stdio:          stdioProxy,
		Target:         target,
		Labels:         labels,
		BuildArgs:      buildArgs,
		BuildContexts:  buildContexts,
		Secrets:        secrets,
		SSH:            ssh,
		Entitlements:   entitlementValues,
		Attestations:   attestations,
		AddHosts:       contextMap[KeyAddHosts],
		Network:        network,
		ShmSize:        lastMetadataValue(contextMap[KeyShmSize]),
		Ulimits:        contextMap[KeyUlimit],
		CacheIn:        cacheIn,
		CacheOut:       cacheOut,
		Outputs:        outputs,
		basePath:       filepath.Join(basePath, buildID),
		Check:          check,
	}

	return bopts, nil
}

func lastMetadataValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

func (b *BOpts) dockerfileFrontendAttrs() map[string]string {
	attrs := map[string]string{
		"filename": filepath.Join(DockerfileStaging, "Dockerfile"),
	}
	for name, source := range b.BuildContexts {
		attrs["context:"+name] = source
	}
	if len(b.AddHosts) > 0 {
		attrs["add-hosts"] = strings.Join(b.AddHosts, ",")
	}
	if b.Network != "" {
		attrs["force-network-mode"] = b.Network
	}
	if b.ShmSize != "" {
		attrs["shm-size"] = b.ShmSize
	}
	if len(b.Ulimits) > 0 {
		attrs["ulimit"] = strings.Join(b.Ulimits, ",")
	}
	if b.NoCache != nil {
		attrs["no-cache"] = *b.NoCache
	}
	return attrs
}

func (b *BOpts) Context(parent context.Context) context.Context {
	return context.WithValue(parent, keyBOpts, b)
}

func newBOptsFromContext(ctx context.Context) (*BOpts, error) {
	buildOptsAny := ctx.Value(keyBOpts)
	if buildOptsAny == nil {
		return nil, ErrMissingContext
	}
	buildOpts, ok := buildOptsAny.(*BOpts)
	if !ok {
		return nil, ErrTypeAssertionFail
	}
	return buildOpts, nil
}
