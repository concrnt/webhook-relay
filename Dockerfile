FROM golang:1.25 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /webhook-relay .

# CA bundle: distroless ships its own, which lagged behind the eMudhra intermediate CA
# compromise (2026-10), and the golang builder image is not always current either, so
# build a fresh Debian bundle here. The CI workflow excludes this stage from the layer
# cache (no-cache-filters) so apt really runs on every build.
FROM debian:trixie-slim AS certificates
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates

FROM gcr.io/distroless/static-debian12
COPY --from=certificates /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /webhook-relay /webhook-relay
EXPOSE 8080
ENTRYPOINT ["/webhook-relay", "-config", "/app/config.yaml"]
