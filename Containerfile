FROM quay.io/fedora/fedora-minimal:44 AS buildroot
ARG DNF_FLAGS="-y --setopt=install_weak_deps=False"
RUN --mount=type=cache,id=dnf,target=/var/cache/libdnf5 \
    dnf install ${DNF_FLAGS} golang

FROM buildroot as builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN --mount=type=cache,id=gomod,target=/root/go/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,id=gomod,target=/root/go/pkg/mod \
    --mount=type=cache,id=gobuild,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o manager ./cmd/

FROM quay.io/fedora/fedora-minimal:44
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]
