# Build stage.
FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/aim-feishu-rm-assistant .

# Runtime stage.
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 app
USER app

COPY --from=builder /out/aim-feishu-rm-assistant /usr/local/bin/aim-feishu-rm-assistant

ENV SQLITE_PATH=/data/assistant.db
ENV LOG_DIR=/data/logs
VOLUME ["/data"]

ENTRYPOINT ["aim-feishu-rm-assistant"]
