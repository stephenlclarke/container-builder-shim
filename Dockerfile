# Pin both multi-platform base-image indexes so a release is reproducible even
# if the readable upstream tags advance after this source commit is published.
ARG BUILD_IMAGE=docker.io/library/golang@sha256:8ac98ca534ac3f51e1f420a1dd2c15e74c75cfa0f23f3ad27eb5d7236c349a0c
ARG FINAL_IMAGE=docker.io/moby/buildkit@sha256:99368f3e46177c6405b9740e4461d91cee004128daeecca78d4ceac50ec1da5c
ARG SOURCE_REPOSITORY=https://github.com/stephenlclarke/container-builder-shim

FROM --platform=$BUILDPLATFORM ${BUILD_IMAGE} AS build-base

ARG GIT_TAG
ARG TARGETARCH
ARG TARGETOS
ENV VERSION=${GIT_TAG:-dev}

WORKDIR /src

# Install build dependencies
RUN apk update && \
	apk add --no-cache ca-certificates && \
	update-ca-certificates

# Copy dependency manifests and vendor directory first for better caching
COPY go.mod go.sum ./
COPY vendor vendor

# Copy the rest of the application source code
COPY *.go .
COPY ./pkg ./pkg

# Ensure TARGETARCH and TARGETOS are set if cross-compiling (e.g., via --build-arg)
# Defaulting to arm64/linux if not set
RUN GOARCH=${TARGETARCH:-arm64} GOOS=${TARGETOS:-linux} CGO_ENABLED=0 go build \
	-v \
	-ldflags "-w -s -extldflags '-static' -X main.VERSION=${VERSION}" \
	-tags "osusergo netgo static_build seccomp" \
	-o /usr/local/bin/container-builder-shim

# Final Image
FROM ${FINAL_IMAGE} AS final
ARG SOURCE_REPOSITORY
LABEL org.opencontainers.image.source=${SOURCE_REPOSITORY}
RUN apk add --no-cache ca-certificates
COPY --from=build-base /usr/local/bin/container-builder-shim /usr/local/bin/container-builder-shim
COPY LICENSE NOTICE.md /licenses/

ENTRYPOINT ["/usr/local/bin/container-builder-shim"]
