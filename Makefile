.PHONY: lint test fuzz vuln build

lint:
	go vet -p 2 ./...
	golangci-lint run

test:
	go test -p 2 $(if $(filter 1,$(shell go env CGO_ENABLED)),-race) -parallel 2 -coverprofile=coverage.out ./...

fuzz:
	go test -p 2 -parallel 2 -run Fuzz -fuzz FuzzParse -fuzztime 5s ./internal/spec
	go test -p 2 -parallel 2 -run Fuzz -fuzz FuzzParseDestination -fuzztime 5s ./internal/probe
	go test -p 2 -parallel 2 -run Fuzz -fuzz FuzzDecodeObservations -fuzztime 5s ./internal/runner
	go test -p 2 -parallel 2 -run Fuzz -fuzz FuzzProbeInput -fuzztime 5s ./cmd/egress-proof-probe

vuln:
	govulncheck ./...

build:
	go build -p 2 -o bin/ ./cmd/...
