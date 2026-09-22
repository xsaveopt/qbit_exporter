# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
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

RUN mkdir -p /data

FROM gcr.io/distroless/static:nonroot
COPY --from=build /qbit_exporter /usr/local/bin/qbit_exporter
COPY --from=build --chown=nonroot:nonroot /data /data
ENV QBIT_DB_PATH=/data/qbit_exporter.db
EXPOSE 9879
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/qbit_exporter", "-healthcheck"]
ENTRYPOINT ["/usr/local/bin/qbit_exporter"]
