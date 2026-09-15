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

func extractSSHAgentConfigs(values []string) ([]sshprovider.AgentConfig, error) {
	if len(values) == 0 {
		return nil, nil
	}
	agentConfigs := make([]sshprovider.AgentConfig, 0, len(values))
	seenIDs := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "default" {
			if _, exists := seenIDs["default"]; exists {
				return nil, ErrUnsupportedSSH
			}
			seenIDs["default"] = struct{}{}
			agentConfigs = append(agentConfigs, sshprovider.AgentConfig{ID: "default"})
			continue
		}

		id, path, hasPath := strings.Cut(value, "=")
		id = strings.TrimSpace(id)
		path = strings.TrimSpace(path)
		if !hasPath || id == "" || !isForwardedSSHSocketPath(path) {
			return nil, ErrUnsupportedSSH
		}
		if _, exists := seenIDs[id]; exists {
			return nil, ErrUnsupportedSSH
		}
		seenIDs[id] = struct{}{}
		config := sshprovider.AgentConfig{
			ID:    id,
			Paths: []string{path},
		}
		agentConfigs = append(agentConfigs, config)
	}
	return agentConfigs, nil
}

func isForwardedSSHSocketPath(path string) bool {
	const socketDirectory = "/var/host-services/"
	const socketPrefix = "ssh-auth"
	const socketSuffix = ".sock"

	if !strings.HasPrefix(path, socketDirectory) {
		return false
	}
	name := strings.TrimPrefix(path, socketDirectory)
	if name == "" || strings.Contains(name, "/") {
		return false
	}
	return strings.HasPrefix(name, socketPrefix) && strings.HasSuffix(name, socketSuffix)
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
	request, err := parseBuildRequest(contextMap)
	if err != nil {
		return nil, err
	}
	services, err := createBuildServices(ctx, basePath, request)
	if err != nil {
		return nil, err
	}
	return &BOpts{
		BuildID:        request.buildID,
		Dockerfile:     request.dockerfile,
		Dockerignore:   request.dockerignore,
		Tag:            request.tag,
		BuildPlatforms: request.buildPlatforms,
		Platforms:      request.platforms,
		ContextDir:     request.contextDir,
		ContentStore:   services.content,
		FSSync:         services.fssync,
		NoCache:        request.noCache,
		Resolver:       resolver.NewResolverProxy(),
		ProgressWriter: services.progress,
		Stdio:          services.stdio,
		Target:         request.target,
		Labels:         request.labels,
		BuildArgs:      request.buildArgs,
		BuildContexts:  request.buildContexts,
		Secrets:        request.secrets,
		SSH:            request.ssh,
		Entitlements:   request.entitlements,
		Attestations:   request.attestations,
		AddHosts:       contextMap[KeyAddHosts],
		Network:        request.network,
		ShmSize:        lastMetadataValue(contextMap[KeyShmSize]),
		Ulimits:        contextMap[KeyUlimit],
		CacheIn:        contextMap[KeyCacheIn],
		CacheOut:       contextMap[KeyCacheOut],
		Outputs:        contextMap[KeyOutput],
		basePath:       filepath.Join(basePath, request.buildID),
		Check:          request.check,
	}, nil
}

type parsedBuildRequest struct {
	buildID, tag, contextDir, target, progress string
	dockerfile, dockerignore                   []byte
	buildPlatforms, platforms                  []ocispecs.Platform
	noCache                                    *string
	labels, buildArgs, buildContexts           map[string]string
	secrets                                    map[string][]byte
	ssh                                        []sshprovider.AgentConfig
	entitlements                               []string
	attestations                               map[string]string
	network                                    string
	check                                      bool
}

func parseBuildRequest(contextMap map[string][]string) (*parsedBuildRequest, error) {
	buildID, ok := firstMetadataValue(contextMap, KeyBuildID)
	if !ok {
		return nil, ErrMissingBuildID
	}
	tag, ok := firstMetadataValue(contextMap, KeyTag)
	if !ok {
		return nil, ErrMissingContextRef
	}
	dockerfile, dockerignore, err := decodeDockerfileInputs(contextMap)
	if err != nil {
		return nil, err
	}
	progress, err := parseProgress(contextMap)
	if err != nil {
		return nil, err
	}
	platformValues, err := parseTargetPlatforms(contextMap[KeyPlatforms])
	if err != nil {
		return nil, err
	}
	features, err := parseBuildFeatures(contextMap)
	if err != nil {
		return nil, err
	}
	buildPlatforms := utils.BuildPlatforms()
	if len(buildPlatforms) == 0 {
		buildPlatforms = append(buildPlatforms, platforms.DefaultSpec())
	}
	contextDir, _ := firstMetadataValue(contextMap, KeyContext)
	if contextDir == "" {
		contextDir = "."
	}
	target, _ := firstMetadataValue(contextMap, KeyTarget)
	_, check := firstMetadataValue(contextMap, KeyCheck)
	return &parsedBuildRequest{
		buildID: buildID, tag: tag, contextDir: contextDir, target: target, progress: progress,
		dockerfile: dockerfile, dockerignore: dockerignore, buildPlatforms: buildPlatforms, platforms: platformValues,
		noCache: features.noCache, labels: features.labels, buildArgs: features.buildArgs,
		buildContexts: features.buildContexts, secrets: features.secrets, ssh: features.ssh,
		entitlements: features.entitlements, attestations: features.attestations, network: features.network, check: check,
	}, nil
}

type parsedBuildFeatures struct {
	noCache                          *string
	labels, buildArgs, buildContexts map[string]string
	secrets                          map[string][]byte
	ssh                              []sshprovider.AgentConfig
	entitlements                     []string
	attestations                     map[string]string
	network                          string
}

func parseBuildFeatures(contextMap map[string][]string) (*parsedBuildFeatures, error) {
	buildContexts, err := extractBuildContexts(contextMap[KeyBuildContexts])
	if err != nil {
		return nil, err
	}
	secrets, err := extractBase64Map(contextMap[KeySecrets])
	if err != nil {
		return nil, err
	}
	ssh, err := extractSSHAgentConfigs(contextMap[KeySSH])
	if err != nil {
		return nil, err
	}
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
	var noCache *string
	if values, ok := contextMap[KeyNoCache]; ok {
		value := lastMetadataValue(values)
		noCache = &value
	}
	return &parsedBuildFeatures{
		noCache: noCache, labels: extractStringMap(contextMap[KeyLabels]), buildArgs: extractStringMap(contextMap[KeyBuildArgs]),
		buildContexts: buildContexts, secrets: secrets, ssh: ssh, entitlements: entitlementValues,
		attestations: attestations, network: network,
	}, nil
}

type buildServices struct {
	stdio    *stdio.StdioProxy
	progress progresswriter.Writer
	fssync   *fssync.FSSyncProxy
	content  *content.ContentStoreProxy
}

func createBuildServices(ctx context.Context, basePath string, request *parsedBuildRequest) (*buildServices, error) {
	stdioProxy, err := stdio.NewStdioProxy(ctx, request.progress == "tty")
	if err != nil {
		return nil, err
	}
	dockerfile, err := parser.Parse(bytes.NewReader(request.dockerfile))
	if err != nil {
		return nil, err
	}
	_, metaArgs, err := instructions.Parse(dockerfile.AST, nil)
	if err != nil {
		return nil, err
	}
	if err := applyDockerfileMetaArgs(metaArgs, request.buildArgs); err != nil {
		return nil, err
	}
	progress, err := progresswriter.NewPrinter(ctx, stdioProxy, request.progress)
	if err != nil {
		return nil, err
	}
	addedGlobs, err := dockerfileAddedGlobs(dockerfile)
	if err != nil {
		return nil, err
	}
	fssyncProxy, err := fssync.NewFSSyncProxy(".", basePath, addedGlobs, request.dockerfile, request.dockerignore)
	if err != nil {
		return nil, err
	}
	contentProxy, err := content.NewContentStoreProxy()
	if err != nil {
		return nil, err
	}
	return &buildServices{stdio: stdioProxy, progress: progress, fssync: fssyncProxy, content: contentProxy}, nil
}

func firstMetadataValue(contextMap map[string][]string, key string) (string, bool) {
	values, ok := contextMap[key]
	if !ok || len(values) == 0 {
		return "", false
	}
	return values[0], true
}

func decodeDockerfileInputs(contextMap map[string][]string) ([]byte, []byte, error) {
	encodedDockerfile, ok := firstMetadataValue(contextMap, KeyDockerfile)
	if !ok {
		return nil, nil, ErrMissingContextDockerfile
	}
	dockerfile, err := base64.StdEncoding.DecodeString(encodedDockerfile)
	if err != nil {
		return nil, nil, err
	}
	dockerignore := []byte(DockerfileStaging)
	if encodedIgnore, exists := firstMetadataValue(contextMap, KeyDockerignore); exists {
		dockerignore, err = base64.StdEncoding.DecodeString(encodedIgnore)
		if err != nil {
			return nil, nil, err
		}
		dockerignore = append(dockerignore, []byte("\n"+DockerfileStaging)...)
	}
	return dockerfile, dockerignore, nil
}

func parseProgress(contextMap map[string][]string) (string, error) {
	progress, ok := firstMetadataValue(contextMap, KeyProgress)
	if !ok {
		return "auto", nil
	}
	switch progress {
	case "auto", "tty", "plain":
		return progress, nil
	default:
		return "", ErrInvalidProgress
	}
}

func parseTargetPlatforms(values []string) ([]ocispecs.Platform, error) {
	if len(values) == 0 {
		return []ocispecs.Platform{platforms.DefaultSpec()}, nil
	}
	parsed := make([]ocispecs.Platform, 0, len(values))
	for _, value := range values {
		platform, err := platforms.Parse(value)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, platform)
	}
	return parsed, nil
}

func extractStringMap(values []string) map[string]string {
	result := map[string]string{}
	for _, value := range values {
		key, mappedValue, found := strings.Cut(value, "=")
		if !found {
			mappedValue = ""
		}
		result[key] = mappedValue
	}
	return result
}

func extractBase64Map(values []string) (map[string][]byte, error) {
	result := map[string][]byte{}
	for _, value := range values {
		key, encoded, found := strings.Cut(value, "=")
		if !found {
			result[key] = []byte{}
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, err
		}
		result[key] = decoded
	}
	return result, nil
}

func applyDockerfileMetaArgs(metaArgs []instructions.ArgCommand, buildArgs map[string]string) error {
	for _, metaArg := range metaArgs {
		for _, arg := range metaArg.Args {
			if _, overridden := buildArgs[arg.Key]; overridden {
				continue
			}
			resolved, err := shell.NewLex('\\').ProcessWordWithMatches(arg.ValueString(), utils.NewMapGetter(buildArgs))
			if err != nil {
				return err
			}
			buildArgs[arg.Key] = resolved.Result
		}
	}
	return nil
}

func dockerfileAddedGlobs(dockerfile *parser.Result) ([]string, error) {
	addedGlobs := []string{}
	for _, node := range dockerfile.AST.Children {
		if strings.EqualFold(node.Value, "COPY") || strings.EqualFold(node.Value, "ADD") {
			addedGlobs = append(addedGlobs, node.Next.Value)
		}
		if strings.EqualFold(node.Value, "RUN") {
			mountGlobs, err := runMountGlobs(node)
			if err != nil {
				return nil, err
			}
			addedGlobs = append(addedGlobs, mountGlobs...)
		}
	}
	return addedGlobs, nil
}

func runMountGlobs(node *parser.Node) ([]string, error) {
	command, err := instructions.ParseInstruction(node)
	if err != nil {
		return nil, nil
	}
	runCommand, ok := command.(*instructions.RunCommand)
	if !ok {
		return nil, nil
	}
	if err := runCommand.Expand(func(word string) (string, error) {
		return filepath.Clean(strings.TrimPrefix(word, "/")), nil
	}); err != nil {
		return nil, err
	}
	result := []string{}
	for _, mount := range instructions.GetMounts(runCommand) {
		if mount.Type == instructions.MountTypeBind && mount.Source != "" && mount.From == "" {
			result = append(result, mount.Source)
		}
	}
	return result, nil
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
