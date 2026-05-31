# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /qbit_exporter .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /qbit_exporter /usr/local/bin/qbit_exporter
EXPOSE 9879
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/qbit_exporter"]
