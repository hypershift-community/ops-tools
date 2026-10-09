BINARY := cleanleaked
GOLANGCI_VERSION ?= v2.11.4

.PHONY: build test vet lint clean scan dry-run verify

build:
	@mkdir -p bin
	go build -o bin/$(BINARY) .

test:
	go test ./... -count=1 -timeout=120s -v

vet:
	go vet ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION) run --timeout=5m

clean:
	rm -rf bin

scan:
	go run . scan

dry-run:
	go run . delete --dry-run

verify: build test vet lint
