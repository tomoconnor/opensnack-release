# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

# Build stage
FROM golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /opensnack ./cmd/opensnack

# Runtime stage: distroless static, which is the CA certificates and time
# zone data the static binary needs and nothing else (no shell, no package
# manager to age out; Alpine 3.20 reached end of life in April 2026). The
# default user is root, as it was on Alpine; Kubernetes deployments set
# their own.
FROM gcr.io/distroless/static-debian13@sha256:15f8637509792e05b97d83451b0b6ccfa4abd548f997e406ac1e72c87fe1fa78

WORKDIR /app

# Copy binary from builder
COPY --from=builder /opensnack /app/opensnack

# Expose the application port
EXPOSE 4566

# Set the entrypoint
ENTRYPOINT ["/app/opensnack"]
