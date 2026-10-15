# Cross-compiles on the build host for each target platform, so multi-arch
# builds (amd64, arm64, arm/v7) don't run the Go toolchain under emulation.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH TARGETVARIANT
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
# components.toml is embedded into the binary
COPY *.go components.toml ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags="-s -w" -o /ambient2mqtt

FROM scratch
COPY --from=build /ambient2mqtt /ambient2mqtt
EXPOSE 2466
CMD ["/ambient2mqtt", "-config", "/config.toml"]
