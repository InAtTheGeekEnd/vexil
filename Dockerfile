# syntax=docker/dockerfile:1

# Build stage. It runs on the build host and cross-compiles for the target
# platform, so a multi-arch build does not need emulation.
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/vexil ./cmd/vexil \
    && mkdir -p /out/data

# Final stage. Distroless static has CA certificates for HTTPS checks and
# time zone data, and nothing else. The nonroot user is uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/vexil /vexil
COPY --from=build --chown=65532:65532 /out/data /data
ENV VEXIL_DATA=/data
VOLUME /data
EXPOSE 8080
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/vexil", "healthcheck"]
ENTRYPOINT ["/vexil"]
