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

# Daemon image -- runs privileged with the host rootfs mounted at
# /run/rootfs. Bootc commands run via chroot into the host root.
# Must run as root for CAP_SYS_CHROOT.
FROM gcr.io/distroless/static AS daemon
WORKDIR /
COPY --from=builder /workspace/daemon .
ENTRYPOINT ["/daemon"]
