.PHONY: all build test clean run-worker run-supervisor docker-build up down

all: test build

build:
	@mkdir -p bin
	go build -o bin/subzero-supervisor ./cmd/subzero-supervisor
	go build -o bin/subzero-worker ./cmd/subzero-worker
	go build -o bin/subzero-cli ./cmd/subzero-cli
	go build -o bin/subzero-admin ./cmd/subzero-admin

test:
	go test -v -race ./...

docker-build:
	docker build -t subzero-worker:latest -f Dockerfile.worker .
	docker build -t subzero:latest .

up:
	docker compose up -d

down:
	docker compose down -v

clean:
	rm -rf bin/ tmp/ *.sock
