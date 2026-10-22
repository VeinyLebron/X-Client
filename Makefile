.PHONY: build test vet fmt run run-org1 run-org2 clean docker-build

build:
	go build -o bin/x-client ./cmd/server

test:
	go test -race ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "gofmt needed" && exit 1)

fmt:
	gofmt -w .

# Standalone: in-memory ledger, both organizations in one process.
run: build
	./bin/x-client -config config/config.yaml

# Against fabric-x-samples/devnet (run ./scripts/copy-crypto.sh first).
run-org1: build
	./bin/x-client -config config/org1.yaml

run-org2: build
	./bin/x-client -config config/org2.yaml

docker-build:
	docker build -t fabricx-endorser-client:latest .

clean:
	rm -rf bin/
