.PHONY: build test vet fmt run run-centralbank run-bank-a run-bank-b clean docker-build

build:
	go build -o bin/x-client ./cmd/server

test:
	go test -race ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "gofmt needed" && exit 1)

fmt:
	gofmt -w .

# Standalone: in-memory ledger, all three organizations in one process.
run: build
	./bin/x-client -config config/config.yaml

# Against fabric-x-samples/devnet-3org (run ./scripts/copy-crypto.sh first).
run-centralbank: build
	./bin/x-client -config config/centralbank.yaml

run-bank-a: build
	./bin/x-client -config config/bank_a.yaml

run-bank-b: build
	./bin/x-client -config config/bank_b.yaml

docker-build:
	docker build -t fabricx-endorser-client:latest .

clean:
	rm -rf bin/
