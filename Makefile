VERSION ?= dev

.PHONY: build test clean
build:
	go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o camwall ./cmd/camwall

test:
	go vet ./...
	go test ./...

clean:
	rm -f camwall
