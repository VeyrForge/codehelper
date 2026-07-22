# MCP server image for Glama introspection/release and container installs.
# Starts the stdio MCP server (initialize + tools/list). Diagnostics go to stderr only.
# Build: docker build -t codehelper-mcp .
# Run:   docker run --rm -i codehelper-mcp

FROM golang:1.25-bookworm AS build
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev \
	&& rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=1 go build -trimpath \
	-ldflags="-s -w -X github.com/VeyrForge/codehelper/internal/version.linkVersion=${VERSION}" \
	-o /codehelper-mcp ./cmd/codehelper-mcp

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git \
	&& rm -rf /var/lib/apt/lists/*
COPY --from=build /codehelper-mcp /usr/local/bin/codehelper-mcp
RUN mkdir -p /root/.codehelper
WORKDIR /workspace
ENTRYPOINT ["codehelper-mcp"]
