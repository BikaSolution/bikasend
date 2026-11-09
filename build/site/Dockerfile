# Build Section
# Pinned by digest; Dependabot bumps it.
FROM golang:1.27-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS builder

WORKDIR /build

RUN apk --no-cache add ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/site ./cmd/site
COPY internal/site ./internal/site
COPY pkg ./pkg

# Non-root user for the scratch image
RUN mkdir -p ./rootfs/etc && \
  echo "appuser:x:10100:10100::/:" > ./rootfs/etc/passwd && \
  echo "appuser:x:10100:" > ./rootfs/etc/group

RUN CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -ldflags="-s -w" -o ./site ./cmd/site

# Run Section
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /build/site /site
COPY --from=builder /build/rootfs/etc/passwd /etc/passwd
COPY --from=builder /build/rootfs/etc/group /etc/group

EXPOSE 8091

USER appuser

# Reached only through the tunnel (no published ports), so the Cloudflare client IP header is trusted.
ENTRYPOINT ["/site", "-host", "0.0.0.0", "-behind-cloudflare"]
