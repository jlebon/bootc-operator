FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o operator cmd/operator/main.go
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o daemon cmd/daemon/main.go

FROM gcr.io/distroless/static:nonroot AS operator
WORKDIR /
COPY --from=builder /workspace/operator .
USER 65532:65532
ENTRYPOINT ["/operator"]

FROM gcr.io/distroless/static:nonroot AS daemon
WORKDIR /
COPY --from=builder /workspace/daemon .
USER 65532:65532
ENTRYPOINT ["/daemon"]
