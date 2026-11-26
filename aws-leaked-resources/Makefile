BINARY := cleanleaked
GOLANGCI_VERSION ?= v2.11.4
REGION ?= us-east-1
MIN_AGE ?= 24h
TARGET ?=
OUTPUT_DIR ?= output
INPUT ?=
PROOF ?=
HTML ?= output/report.html

.PHONY: build test vet lint clean scan prove report flow dry-run delete pipeline verify

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
	go run . scan --region "$(REGION)" --output json

prove:
	go run . prove --input "$(INPUT)"

report:
	go run . report --input "$(INPUT)" --proof "$(PROOF)" --output "$(HTML)"

flow: build
	./bin/$(BINARY) flow --region "$(REGION)" --min-age "$(MIN_AGE)" --target "$(TARGET)" --output-dir "$(OUTPUT_DIR)"

dry-run:
	go run . delete --dry-run --region "$(REGION)" --proof "$(PROOF)"

delete:
	@test -n "$(PROOF)" || { echo 'Usage: make delete PROOF=output/proofs-<timestamp>.json [REGION=us-east-1]' >&2; exit 1; }
	go run . delete --interactive --region "$(REGION)" --proof "$(PROOF)"

pipeline:
	@set -eu; mkdir -p output; dir=$$(mktemp -d output/pipeline.XXXXXXXX); \
	  echo "Pipeline reports: $$dir" >&2; \
	  go run . scan --region "$(REGION)" --output json --output-dir '' > "$$dir/scan.json"; \
	  go run . prove --input "$$dir/scan.json" --json --output-dir '' > "$$dir/proofs.json"; \
	  go run . report --input "$$dir/scan.json" --proof "$$dir/proofs.json" --output "$$dir/report.html"; \
	  go run . delete --dry-run --region "$(REGION)" --proof "$$dir/proofs.json" --output-dir "$$dir"

verify: build test vet lint
