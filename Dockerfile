# Multi-stage build for the Fabric-X Endorser REST Client
FROM golang:1.27-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /x-client ./cmd/server

FROM alpine:3.22
RUN apk --no-cache add ca-certificates curl \
    && adduser -D -u 10001 xclient

WORKDIR /app
COPY --from=builder /x-client /usr/local/bin/x-client
COPY config/ /app/config/

# Crypto material is mounted at /app/crypto by docker-compose.2org.yaml.
USER xclient
EXPOSE 8080

ENTRYPOINT ["x-client"]
CMD ["-config", "/app/config/config.yaml"]
