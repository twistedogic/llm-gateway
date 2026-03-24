.PHONY: build run test lint clean docker-build docker-up docker-down

BINARY=llm-gateway
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_LDFLAGS=-ldflags "-X main.Version=$(VERSION)"

# Build
build:
	go build $(BUILD_LDFLAGS) -o bin/$(BINARY) ./cmd/gateway

# Run (uses configs/ locally; set GATEWAY_CONFIG_PATH for production)
run: build
	./bin/$(BINARY) --config ./configs/gateway.yaml

# Run with live reload (requires air: https://github.com/air-verse/air)
dev:
	air -c .air.toml

# Test
test:
	go test -v -race -cover ./...

test/short:
	go test -v -short ./...

# Lint
lint:
	golangci-lint run ./...

# Format
fmt:
	go fmt ./...

# Dependencies
deps:
	go mod download
	go mod tidy

# Clean
clean:
	rm -rf bin/
	rm -f $(BINARY)

# Docker
docker-build:
	docker build -t $(BINARY):$(VERSION) .

docker-buildx:
	docker buildx build --platform linux/amd64,linux/arm64 \
		-t $(BINARY):$(VERSION) .

docker-up:
	docker-compose up -d

docker-down:
	docker-compose down

docker-logs:
	docker-compose logs -f

# Temporal (local dev)
temporal/start:
	docker run --rm -p 7233:7233 \
		-v /tmp/temporal-data:/tmp/data \
		temporalio/auto-setup:1.26.0

# Prometheus
prometheus/start:
	docker run --rm -p 9090:9090 \
		-v $(PWD)/configs/prometheus.yml:/etc/prometheus/prometheus.yml \
		prom/prometheus:latest

# Generate mocks (requires mockgen)
mocks:
	mockgen -source=internal/provider/router.go -destination=internal/provider/mock_router.go -package=provider
	mockgen -source=internal/hooks/hook.go -destination=internal/hooks/mock_hook.go -package=hooks

# Verify build compiles
check:
	go build -n ./...

# Run benchmarks
bench:
	go test -bench=. -benchmem ./internal/ratelimit/...

# Help
help:
	@grep -E '^[a-zA-Z_-]+:.*##' Makefile | sort | awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
