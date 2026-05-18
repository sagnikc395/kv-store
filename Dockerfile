FROM golang:1.26 AS build

WORKDIR /app

COPY go.mod ./
COPY cmd/ cmd/
COPY internal/ internal/

RUN CGO_ENABLED=0 go build -o /out/kv-node ./cmd/kv-node && \
    CGO_ENABLED=0 go build -o /out/kv-proxy ./cmd/kv-proxy

FROM gcr.io/distroless/static-debian12

COPY --from=build /out/kv-node /kv-node
COPY --from=build /out/kv-proxy /kv-proxy

ENTRYPOINT ["/kv-node"]
