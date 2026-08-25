# syntax=docker/dockerfile:1

FROM golang:1.25-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tapd-dingding ./cmd/tapd-dingding

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --create-home --home-dir /app appuser

WORKDIR /app
COPY --from=build /out/tapd-dingding /usr/local/bin/tapd-dingding

USER appuser
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/tapd-dingding"]
