FROM golang:1.25 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /webhook-relay .

FROM gcr.io/distroless/static-debian12
COPY --from=builder /webhook-relay /webhook-relay
EXPOSE 8080
ENTRYPOINT ["/webhook-relay", "-config", "/app/config.yaml"]
