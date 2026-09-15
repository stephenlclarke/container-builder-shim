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
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/containerd/containerd/reference"
	"github.com/containerd/platforms"
	dref "github.com/distribution/reference"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
	"github.com/moby/buildkit/frontend/dockerui"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/frontend/subrequests/lint"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/progress/progresswriter"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"

	"github.com/apple/container-builder-shim/pkg/build/utils"
)

func frontend(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
	bopts, err := newBOptsFromContext(ctx)
	if err != nil {
		return nil, err
	}

	if bopts.Check {
		return checkBuild(ctx, bopts, c)
	}

	res := gateway.NewResult()
	expPlatforms := &exptypes.Platforms{
		Platforms: make([]exptypes.Platform, len(bopts.Platforms)),
	}

	clog := func(format string, params ...interface{}) {
		if bopts.ProgressWriter != nil {
			msg := fmt.Sprintf(format, params...)
			progresswriter.Write(bopts.ProgressWriter, msg, nil)
		}
	}

	plWG := sync.WaitGroup{}
	plErrCh := make(chan error)
	plDoneCh := make(chan struct{})
	for i := range bopts.Platforms {
		plWG.Add(1)
		go func(i int) {
			defer plWG.Done()
			pl := bopts.Platforms[i]

			states, err := resolveStates(ctx, bopts, pl, clog)
			if err != nil {
				plErrCh <- err
			}

			ref, cfgJSON, err := solvePlatform(ctx, bopts, pl, c, states)
			if err != nil {
				plErrCh <- err
				return
			}
			plStr := platforms.FormatAll(platforms.Normalize(pl))
			res.AddRef(plStr, ref)
			res.AddMeta(fmt.Sprintf("%s/%s", exptypes.ExporterImageConfigKey, plStr), cfgJSON)
			expPlatforms.Platforms[i] = exptypes.Platform{
				ID:       plStr,
				Platform: pl,
			}
		}(i)
	}
	go func() { plWG.Wait(); plDoneCh <- struct{}{} }()
	select {
	case err := <-plErrCh:
		return nil, err
	case <-plDoneCh:
	}

	dt, err := json.Marshal(expPlatforms)
	if err != nil {
		return nil, err
	}
	res.AddMeta(exptypes.ExporterPlatformsKey, dt)
	return res, nil
}

func checkBuild(ctx context.Context, bopts *BOpts, c gateway.Client) (*gateway.Result, error) {
	pl := bopts.Platforms[0]
	results, res, err := checkPlatform(ctx, bopts, pl, c)
	if err != nil {
		return nil, err
	}

	if bopts.ProgressWriter != nil {
		if text := res.Metadata["result.txt"]; len(text) > 0 {
			progresswriter.Write(bopts.ProgressWriter, string(text), nil)
		}
		if results.Error != nil {
			buf := bytes.NewBuffer(nil)
			if len(results.Warnings) > 0 {
				buf.WriteByte('\n')
			}
			results.PrintErrorTo(buf, nil)
			progresswriter.Write(bopts.ProgressWriter, buf.String(), nil)
		} else if len(results.Warnings) == 0 {
			progresswriter.Write(bopts.ProgressWriter, "Check complete, no warnings found.\n", nil)
		}
	}

	if len(results.Warnings) > 0 || results.Error != nil {
		return nil, fmt.Errorf("build check failed")
	}
	return res, nil
}

func checkPlatform(ctx context.Context, bopts *BOpts, pl ocispecs.Platform, c gateway.Client) (*lint.LintResults, *gateway.Result, error) {
	states, err := resolveStates(ctx, bopts, pl, func(format string, params ...interface{}) {
		if bopts.ProgressWriter != nil {
			msg := fmt.Sprintf(format, params...)
			progresswriter.Write(bopts.ProgressWriter, msg, nil)
		}
	})
	if err != nil {
		return nil, nil, err
	}

	convertOpt, _, _, err := dockerfileConvertOpt(ctx, bopts, pl, c, states)
	if err != nil {
		return nil, nil, err
	}

	results, err := dockerfile2llb.DockerfileLint(ctx, bopts.Dockerfile, convertOpt)
	if err != nil {
		return nil, nil, err
	}

	res, err := results.ToResult(nil)
	if err != nil {
		return nil, nil, err
	}
	return results, res, nil
}

type stateMeta struct {
	state   llb.State
	imgMeta []byte
}

func resolveStates(ctx context.Context, bopts *BOpts, platform ocispecs.Platform, clog func(string, ...interface{})) (map[string]stateMeta, error) {
	dockerfile, err := parser.Parse(bytes.NewReader(bopts.Dockerfile))
	if err != nil {
		return nil, err
	}

	stages, _, err := instructions.Parse(dockerfile.AST, nil)
	if err != nil {
		return nil, err
	}

	resolver := &sourceStateResolver{ctx: ctx, bopts: bopts, log: clog, states: map[string]stateMeta{}}
	if err := resolveDockerfileStages(dockerfile.EscapeToken, stages, platform, resolver); err != nil {
		return nil, err
	}
	if err := preseedCopyFromSources(stages, platform, resolver.resolve); err != nil {
		return nil, err
	}
	return resolver.states, nil
}

type sourceStateResolver struct {
	ctx    context.Context
	bopts  *BOpts
	log    func(string, ...interface{})
	states map[string]stateMeta
	mutex  sync.Mutex
}

func (r *sourceStateResolver) resolve(name string, platform ocispecs.Platform) error {
	if strings.EqualFold(name, "scratch") || strings.EqualFold(name, "context") {
		return nil
	}
	ref, err := dref.ParseAnyReference(name)
	if err != nil {
		if err == reference.ErrObjectRequired {
			return nil
		}
		return fmt.Errorf("invalid ref: %s", name)
	}
	r.log("[resolver] fetching image...%s", ref.String())
	digest, image, err := r.resolveImage(name, platform)
	if err != nil {
		return err
	}
	if digest == "" {
		return nil
	}
	stateName, state, metadata, err := resolvedImageState(ref, digest.String(), image, platform)
	if err != nil {
		return err
	}
	r.mutex.Lock()
	r.states[stateName] = stateMeta{state: state, imgMeta: metadata}
	r.mutex.Unlock()
	return nil
}

func (r *sourceStateResolver) resolveImage(name string, platform ocispecs.Platform) (digest.Digest, []byte, error) {
	options := sourceresolver.Opt{
		ImageOpt:     &sourceresolver.ResolveImageOpt{Platform: &platform, ResolveMode: llb.ResolveModePreferLocal.String()},
		OCILayoutOpt: &sourceresolver.ResolveOCILayoutOpt{Store: sourceresolver.ResolveImageConfigOptStore{StoreID: "container"}},
	}
	_, imageDigest, image, err := r.bopts.Resolver.ResolveImageConfig(r.ctx, name, options)
	if err == reference.ErrObjectRequired {
		return "", nil, nil
	}
	return imageDigest, image, err
}

func resolvedImageState(ref dref.Reference, imageDigest string, image []byte, platform ocispecs.Platform) (string, llb.State, []byte, error) {
	fqdn := ref.String()
	if _, alreadyDigested := ref.(dref.Digested); !alreadyDigested {
		fqdn += "@" + imageDigest
	}
	state := llb.OCILayout(fqdn, llb.OCIStore("", "container"), llb.Platform(platform)).Platform(platform)
	named, err := dref.ParseNormalizedNamed(ref.String())
	if err != nil {
		return "", llb.State{}, nil, fmt.Errorf("invalid context name %s %v", ref.String(), err)
	}
	name := strings.TrimSuffix(dref.FamiliarString(named), ":latest") + "::" + platforms.FormatAll(platforms.Normalize(platform))
	metadata, err := json.Marshal(map[string][]byte{exptypes.ExporterImageConfigKey: image})
	return name, state, metadata, err
}

func resolveDockerfileStages(escapeToken rune, stages []instructions.Stage, platform ocispecs.Platform, resolver *sourceStateResolver) error {
	errCh := make(chan error, len(stages))
	var wg sync.WaitGroup
	for index, stage := range stages {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := resolveDockerfileStage(escapeToken, stages, index, stage, platform, resolver); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		return err
	}
	return nil
}

func resolveDockerfileStage(escapeToken rune, stages []instructions.Stage, index int, stage instructions.Stage, platform ocispecs.Platform, resolver *sourceStateResolver) error {
	lexer := shell.NewLex(escapeToken)
	global := globalArgs(resolver.bopts.BuildPlatforms[0], platform, resolver.bopts.BuildArgs, resolver.bopts.Target)
	baseName, err := lexer.ProcessWordWithMatches(stage.BaseName, global)
	if err != nil {
		return fmt.Errorf("invalid arg for stage[%s]: %v", stage.BaseName, err)
	}
	stagePlatform, err := resolvedStagePlatform(lexer, stage, global, platform)
	if err != nil {
		return err
	}
	namedIndex, hasNamedStage := instructions.HasStage(stages, baseName.Result)
	if hasNamedStage && namedIndex < index {
		return nil
	}
	if err := resolver.resolve(baseName.Result, stagePlatform); err != nil {
		logrus.Errorf("error resolving image: %v", err)
		return err
	}
	return nil
}

func resolvedStagePlatform(lexer *shell.Lex, stage instructions.Stage, global shell.EnvGetter, fallback ocispecs.Platform) (ocispecs.Platform, error) {
	if stage.Platform == "" {
		return fallback, nil
	}
	value, err := lexer.ProcessWordWithMatches(stage.Platform, global)
	if err != nil {
		return ocispecs.Platform{}, fmt.Errorf("invalid platform for stage[%s]: %v", stage.BaseName, err)
	}
	parsed, err := platforms.Parse(value.Result)
	if err != nil {
		return ocispecs.Platform{}, fmt.Errorf("invalid platform for stage[%s]: %v", stage.BaseName, err)
	}
	return parsed, nil
}

type frontendClient struct {
	gateway.Client
	frontendOpt    map[string]string
	frontendInputs map[string]*pb.Definition
}

func (fc *frontendClient) BuildOpts() gateway.BuildOpts {
	opts := fc.Client.BuildOpts()

	for k, v := range fc.frontendOpt {
		if _, ok := opts.Opts[k]; !ok {
			opts.Opts[k] = v
			splits := strings.SplitN(k, "::", 2)
			if len(splits) != 2 {
				continue
			}
			opts.Opts[splits[0]] = v
		}
	}

	return opts
}

func (fc *frontendClient) Inputs(ctx context.Context) (map[string]llb.State, error) {
	inputs, err := fc.Client.Inputs(ctx)
	if err != nil {
		return nil, err
	}

	for k, v := range fc.frontendInputs {
		if _, ok := inputs[k]; !ok {
			defOp, err := llb.NewDefinitionOp(v)
			if err != nil {
				return nil, err
			}

			inputs[k] = llb.NewState(defOp.Output())
		}
	}
	return inputs, nil
}

func solvePlatform(ctx context.Context, bopts *BOpts, pl ocispecs.Platform, c gateway.Client, states map[string]stateMeta) (gateway.Reference, []byte, error) {
	convertOpt, frontendOpt, frontendInputs, err := dockerfileConvertOpt(ctx, bopts, pl, c, states)
	if err != nil {
		return nil, nil, err
	}

	result, err := dockerfile2llb.Dockerfile2LLB(ctx, bopts.Dockerfile, convertOpt)
	if err != nil {
		return nil, nil, err
	}

	def, err := result.State.Marshal(ctx)
	if err != nil {
		return nil, nil, err
	}

	if _, err := result.State.GetPlatform(ctx); err != nil {
		return nil, nil, err
	}

	r, err := c.Solve(ctx, gateway.SolveRequest{
		Evaluate:       false,
		Definition:     def.ToPB(),
		FrontendOpt:    frontendOpt,
		FrontendInputs: frontendInputs,
	})
	if err != nil {
		return nil, nil, err
	}

	ref, err := r.SingleRef()
	if err != nil {
		return nil, nil, err
	}

	// Handle metadata-only builds (ENV/ARG/LABEL without RUN/COPY/ADD)
	if ref == nil {
		if result.Image == nil {
			return nil, nil, ErrNoBuildDirectives
		}

		// OCI manifests require a layers array (cannot be null)
		const metadataOnlyMarker = "/.container-metadata-only"
		stateWithLayer := llb.Scratch().
			File(llb.Mkfile(metadataOnlyMarker, 0644, []byte("# This image contains only metadata (ENV/ARG/LABEL)\n"))).
			Platform(pl)

		layerDef, err := stateWithLayer.Marshal(ctx)
		if err != nil {
			return nil, nil, err
		}

		layerResult, err := c.Solve(ctx, gateway.SolveRequest{
			Evaluate:       false,
			Definition:     layerDef.ToPB(),
			FrontendOpt:    frontendOpt,
			FrontendInputs: frontendInputs,
		})
		if err != nil {
			return nil, nil, err
		}

		ref, err = layerResult.SingleRef()
		if err != nil {
			return nil, nil, err
		}
	}

	_, err = ref.ToState()
	if err != nil {
		return nil, nil, err
	}

	cfgJSON, err := json.Marshal(result.Image)
	if err != nil {
		return nil, nil, err
	}
	return ref, cfgJSON, nil
}

func dockerfileConvertOpt(ctx context.Context, bopts *BOpts, pl ocispecs.Platform, c gateway.Client, states map[string]stateMeta) (dockerfile2llb.ConvertOpt, map[string]string, map[string]*pb.Definition, error) {
	capset := pb.Caps.CapSet(utils.Caps().All())
	frontendOpt := map[string]string{
		// In v0.21.0, this was being defaulted to "true"
		// We want to disable it, as it could break fssync
		"local.metadatatransfer": "false",

		// https://github.com/moby/buildkit/pull/5899 introduced a change
		// that ignore apple's xattrs while diffing. This breaks differ
		// breaks due to lack of xattrs, so it is turned off
		"local.differ": "none",
	}
	for k, v := range bopts.dockerfileFrontendAttrs() {
		frontendOpt[k] = v
	}

	frontendInputs := map[string]*pb.Definition{}
	for k, v := range states {
		frontendOpt["context:"+k] = "input:" + k
		frontendOpt["input-metadata:"+k] = string(v.imgMeta)

		def, err := v.state.Marshal(ctx)
		if err != nil {
			return dockerfile2llb.ConvertOpt{}, nil, nil, err
		}
		frontendInputs[k] = def.ToPB()
	}
	cl, err := dockerui.NewClient(&frontendClient{
		Client:         c,
		frontendInputs: frontendInputs,
		frontendOpt:    frontendOpt,
	})
	if err != nil {
		return dockerfile2llb.ConvertOpt{}, nil, nil, err
	}

	source, err := cl.ReadEntrypoint(ctx, "dockerfile")
	if err != nil {
		return dockerfile2llb.ConvertOpt{}, nil, nil, err
	}

	convertOpt := dockerfile2llb.ConvertOpt{
		TargetPlatform: &pl,
		MetaResolver:   bopts.Resolver,
		LLBCaps:        &capset,
		Client:         cl,
	}
	if err := setDockerfileSourceMap(&convertOpt, source); err != nil {
		return dockerfile2llb.ConvertOpt{}, nil, nil, err
	}

	convertOpt.BuildPlatforms = bopts.BuildPlatforms
	convertOpt.TargetPlatforms = bopts.Platforms
	convertOpt.BuildArgs = bopts.BuildArgs
	convertOpt.Labels = bopts.Labels
	convertOpt.Target = bopts.Target
	convertOpt.MultiPlatformRequested = true
	convertOpt.ImageResolveMode = llb.ResolveModePreferLocal
	convertOpt.ExtraHosts = cl.ExtraHosts
	convertOpt.NetworkMode = cl.NetworkMode
	convertOpt.ShmSize = cl.ShmSize
	convertOpt.Ulimits = cl.Ulimits

	return convertOpt, frontendOpt, frontendInputs, nil
}

func setDockerfileSourceMap(convertOpt *dockerfile2llb.ConvertOpt, source *dockerui.Source) error {
	if source == nil || source.SourceMap == nil {
		return fmt.Errorf("dockerfile source map missing")
	}
	convertOpt.SourceMap = source.SourceMap
	return nil
}

// Pre-seed external COPY --from=<image> refs as named contexts, so BuildKit
// does not fall back to remote resolver for normalized docker.io refs.
func preseedCopyFromSources(stages []instructions.Stage, platform ocispecs.Platform, resolveSource func(string, ocispecs.Platform) error) error {
	for _, stage := range stages {
		for _, cmd := range stage.Commands {
			c, ok := cmd.(*instructions.CopyCommand)
			if !ok || c.From == "" {
				continue
			}
			// Skip numeric stage indexes.
			_, err := strconv.Atoi(c.From)
			if err == nil {
				continue
			}
			_, hasNamedStage := instructions.HasStage(stages, c.From)
			if hasNamedStage {
				continue
			}
			// BuildKit does not support arg expansion in COPY --from, so use literal c.From.
			err = resolveSource(c.From, platform)
			if err != nil {
				logrus.Errorf("error resolving COPY --from image: %v", err)
				return err
			}
		}
	}
	return nil
}

func globalArgs(buildPlatform, targetPlatform ocispecs.Platform, buildArgs map[string]string, target string) utils.MapGetter {
	if target == "" {
		target = "default"
	}
	args := map[string]string{
		"BUILDPLATFORM":   platforms.Format(buildPlatform),
		"BUILDOS":         buildPlatform.OS,
		"BUILDOSVERSION":  buildPlatform.OSVersion,
		"BUILDARCH":       buildPlatform.Architecture,
		"BUILDVARIANT":    buildPlatform.Variant,
		"TARGETPLATFORM":  platforms.FormatAll(targetPlatform),
		"TARGETOS":        targetPlatform.OS,
		"TARGETOSVERSION": targetPlatform.OSVersion,
		"TARGETARCH":      targetPlatform.Architecture,
		"TARGETVARIANT":   targetPlatform.Variant,
		"TARGETSTAGE":     target,
	}
	for k, v := range buildArgs {
		args[k] = v
	}
	return utils.NewMapGetter(args)
}
