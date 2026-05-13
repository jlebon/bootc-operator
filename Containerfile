FROM quay.io/fedora/fedora-minimal:44 AS builder
ARG DNF_FLAGS="-y --setopt=install_weak_deps=False"

RUN --mount=type=cache,id=dnf,target=/var/cache/libdnf5 \
    dnf install ${DNF_FLAGS} golang

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -a -o manager ./cmd/

FROM quay.io/fedora/fedora-minimal:44
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]
