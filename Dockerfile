# Build both operator and daemon binaries
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
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o bin/operator cmd/operator/main.go
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o bin/daemon cmd/daemon/main.go

# Operator image
FROM quay.io/hummingbird/core-runtime:latest AS operator
WORKDIR /
COPY --from=builder /workspace/bin/operator /manager
ENTRYPOINT ["/manager"]

# Daemon image -- runs as root because it needs to chroot into the host
# rootfs to execute bootc commands.
FROM quay.io/hummingbird/core-runtime:latest AS daemon
WORKDIR /
COPY --from=builder /workspace/bin/daemon /daemon
USER 0
ENTRYPOINT ["/daemon"]
