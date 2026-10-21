# Multi-stage build for Fabric-X Endorser REST Client
FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /x-client cmd/server/main.go

FROM alpine:latest
RUN apk --no-cache add ca-certificates curl

WORKDIR /root/
COPY --from=builder /x-client /usr/local/bin/x-client
COPY config/config.yaml /root/config/config.yaml

EXPOSE 8080

ENTRYPOINT ["x-client"]
CMD ["-config", "/root/config/config.yaml"]
