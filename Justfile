dockerfile := "Dockerfile"
image-name := "harbor.cyverse.org/de/groups"
tag := "latest"
platform := "linux/amd64"
build-context := "."
container-runtime := "docker"
build-flags := ""

default: build

build: groups

groups:
    go build -o bin/groups ./cmd/groups

build-image:
    {{ container-runtime }} build -f {{ build-context }}/{{ dockerfile }} -t {{ image-name }}:{{ tag }} --platform {{ platform }} {{ build-flags }} {{ build-context }}

push:
    {{ container-runtime }} push {{ image-name }}:{{ tag }}

test:
    go test ./...

docs:
    go tool swag init --parseDependency -g app.go -d cmd/groups/ -o docs/

clean:
    #!/usr/bin/env bash
    go clean
    if [ -f bin/groups ]; then
        rm bin/groups
    fi
