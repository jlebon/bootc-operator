# Build both binaries
FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# Build both binaries
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager ./cmd/operator
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o daemon ./cmd/daemon

# Operator image
FROM gcr.io/distroless/static:nonroot AS operator
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]

# Daemon image -- runs privileged on the host with nsenter access.
# Uses fedora-minimal for nsenter (util-linux), which is needed to
# enter PID 1's mount namespace and run bootc commands on the host
# filesystem.
FROM quay.io/fedora/fedora-minimal AS daemon
RUN microdnf install -y --setopt=install_weak_deps=0 util-linux-core && \
    microdnf clean all
WORKDIR /
COPY --from=builder /workspace/daemon .
ENTRYPOINT ["/daemon"]
