FROM golang:1.18-alpine

WORKDIR /app
COPY * ./

RUN go mod download

# You need CGO_ENABLED=0 to make it so the binary isn't dynamically linked
# for more information: https://stackoverflow.com/a/55106860/57626
RUN CGO_ENABLED=0 GOOS=linux go build -o /ambient2mqtt

FROM scratch

COPY --from=0 /ambient2mqtt /ambient2mqtt

CMD ["/ambient2mqtt", "-config", "/config.toml"]
