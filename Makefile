.PHONY: run build test clean

run:
	APP_ENV=dev go run ./cmd/synapse/

build:
	go build -o bin/synapse ./cmd/synapse/

test:
	go test ./...

clean:
	rm -rf bin/
