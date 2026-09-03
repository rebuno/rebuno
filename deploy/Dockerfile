FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-X main.Version=$VERSION" -o /rebuno ./cmd/rebuno

FROM alpine:3.24
RUN addgroup -S rebuno && adduser -S rebuno -G rebuno
COPY --from=builder /rebuno /usr/local/bin/rebuno
USER rebuno
EXPOSE 8080
ENTRYPOINT ["rebuno", "server"]
