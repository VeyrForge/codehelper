# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY . .
RUN go build -o /out/api ./cmd/api

FROM alpine:3.20 AS runtime
COPY --from=builder /out/api /usr/local/bin/api
EXPOSE 8080
CMD ["api"]
