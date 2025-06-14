FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /ocifit-webhook ./cmd/ocifit/main.go

FROM alpine:latest
WORKDIR /
COPY --from=builder /ocifit-webhook /ocifit-webhook
EXPOSE 8443
ENTRYPOINT ["/ocifit-webhook"]