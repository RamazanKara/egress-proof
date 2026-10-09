.PHONY: lint test build

lint:
	go vet -p 2 ./...
	golangci-lint run

test:
	go test -p 2 -race -parallel 2 -coverprofile=coverage.out ./...

build:
	go build -p 2 -o bin/ ./cmd/...
