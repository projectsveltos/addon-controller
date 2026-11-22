# Build the manager binary
FROM golang:1.27.4 AS builder

ARG BUILDOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the whole repo, .git included. The Go toolchain reads .git to stamp the
# main module version (tag or pseudo-version) in the binary build info, which
# is what image scanners report. Copying only some files would make the tree
# look dirty and produce a "+dirty" version.
COPY . .

# Build. The package is built (not main.go), otherwise the Go toolchain does
# not stamp the module version and vcs info.
RUN CGO_ENABLED=0 GOOS=$BUILDOS GOARCH=$TARGETARCH go build -a -o manager ./cmd

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot

ARG GIT_VERSION=unknown
ARG GIT_COMMIT=unknown

LABEL org.opencontainers.image.source="https://github.com/projectsveltos/addon-controller" \
      org.opencontainers.image.url="https://projectsveltos.io" \
      org.opencontainers.image.documentation="https://projectsveltos.github.io/sveltos/" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.vendor="projectsveltos" \
      org.opencontainers.image.title="addon-controller" \
      org.opencontainers.image.description="Deploys Kubernetes add-ons and applications (Helm charts, Kustomize, raw YAML) across fleets of clusters, with built-in multi-tenancy support." \
      org.opencontainers.image.version="$GIT_VERSION" \
      org.opencontainers.image.revision="$GIT_COMMIT"

WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
