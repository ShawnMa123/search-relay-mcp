# syntax=docker/dockerfile:1

FROM golang:1.25.6-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/search-relay-mcp ./cmd/search-relay-mcp

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/search-relay-mcp /search-relay-mcp

USER 10001:10001
EXPOSE 18081
ENTRYPOINT ["/search-relay-mcp"]