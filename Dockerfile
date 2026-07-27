# Build stage
FROM golang:1.26-bookworm AS builder

WORKDIR /build

# Copy go mod files first for better layer caching during rebuilds.
COPY go.mod go.sum ./
RUN go mod download

# Copy source code.
COPY . .

ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64

# Regenerate the Swagger docs so the image is never built from stale docs. The
# swag version is pinned by the tool directive in go.mod.
RUN go tool swag init --parseDependency -g app.go -d cmd/groups/ -o docs/

RUN go build -ldflags="-w -s" --buildvcs=false -o groups ./cmd/groups

# The Grouper importer ships in the same image so it can run as a Job against the
# same database and configuration. The entrypoint is still the service; a Job
# overrides the command.
RUN go build -ldflags="-w -s" --buildvcs=false -o grouper-import ./cmd/grouper-import

# Runtime stage - distroless static image.
FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=builder /build/groups /bin/groups
COPY --from=builder /build/grouper-import /bin/grouper-import

USER nonroot

ENTRYPOINT ["groups"]

EXPOSE 60000
