# Commands run from repository root.
.PHONY: fmt test race vet build check
fmt:
	go fmt ./...

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

build:
	go build ./cmd/...

check: test race vet build
