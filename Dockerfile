FROM golang:1.24-bookworm AS builder

WORKDIR /app

COPY go.mod ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /auto-tr-ai .

FROM debian:bookworm-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/*

COPY --from=builder /auto-tr-ai /app/auto-tr-ai

ENV PORT=8080

EXPOSE 8080

CMD ["/app/auto-tr-ai"]
