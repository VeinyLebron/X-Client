.PHONY: build test run clean docker-build

build:
	go build -o bin/x-client cmd/server/main.go

test:
	go test -v ./...

run: build
	./bin/x-client -config config/config.yaml

docker-build:
	docker build -t fabricx-endorser-client:latest .

clean:
	rm -rf bin/
