# Build stage
FROM golang:1.25-bookworm AS builder

WORKDIR /build

# Copy go mod files first for better layer caching during rebuilds.
COPY go.mod go.sum ./
RUN go mod download

# Copy source code.
COPY . .

ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64

RUN go build -ldflags="-w -s" --buildvcs=false -o groups ./cmd/groups

# Runtime stage - distroless static image.
FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=builder /build/groups /bin/groups

USER nonroot

ENTRYPOINT ["groups"]

EXPOSE 60000
