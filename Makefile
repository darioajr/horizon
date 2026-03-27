.PHONY: all build build-linux build-windows build-darwin build-all docker docker-build docker-run docker-multiplatform docker-multiplatform-push docker-darwin-artifacts clean test

# Version info
VERSION ?= 0.1.0
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)"

# Build tags for optional storage backends (e.g. "s3 redis infinispan" or "all_backends")
BUILD_TAGS ?= all_backends
TAG_FLAGS := $(if $(BUILD_TAGS),-tags '$(BUILD_TAGS)',)

# Docker
DOCKER_IMAGE ?= horizon
DOCKER_PLATFORMS ?= linux/amd64,linux/arm64

# Output directory
DIST := dist

all: build

# Build for current platform
build:
	CGO_ENABLED=0 go build -trimpath $(TAG_FLAGS) $(LDFLAGS) -o horizon ./cmd/horizon

# Build for Linux
build-linux:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath $(TAG_FLAGS) $(LDFLAGS) -o $(DIST)/horizon-linux-amd64 ./cmd/horizon
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath $(TAG_FLAGS) $(LDFLAGS) -o $(DIST)/horizon-linux-arm64 ./cmd/horizon

# Build for Windows
build-windows:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath $(TAG_FLAGS) $(LDFLAGS) -o $(DIST)/horizon-windows-amd64.exe ./cmd/horizon
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath $(TAG_FLAGS) $(LDFLAGS) -o $(DIST)/horizon-windows-arm64.exe ./cmd/horizon

# Build for macOS
build-darwin:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath $(TAG_FLAGS) $(LDFLAGS) -o $(DIST)/horizon-darwin-amd64 ./cmd/horizon
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath $(TAG_FLAGS) $(LDFLAGS) -o $(DIST)/horizon-darwin-arm64 ./cmd/horizon

# Build for all platforms
build-all: build-linux build-windows build-darwin

# Build using Docker (cross-platform)
docker-build:
	docker-compose -f deployments/docker-compose.yml --profile build run --rm builder

# Build Docker image (single platform, current arch)
docker:
	docker build -f build/Dockerfile \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(DOCKER_IMAGE):$(VERSION) -t $(DOCKER_IMAGE):latest .

# Build multi-platform Docker images (local load — only one platform at a time)
docker-multiplatform:
	docker buildx build -f build/Dockerfile \
		--platform $(DOCKER_PLATFORMS) \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(DOCKER_IMAGE):$(VERSION) -t $(DOCKER_IMAGE):latest .

# Build and push multi-platform Docker images to registry
docker-multiplatform-push:
	docker buildx build -f build/Dockerfile \
		--platform $(DOCKER_PLATFORMS) \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(DOCKER_IMAGE):$(VERSION) -t $(DOCKER_IMAGE):latest --push .

# Extract Darwin binaries from Docker build (no Docker image needed)
docker-darwin-artifacts:
	@mkdir -p $(DIST)
	docker buildx build -f build/Dockerfile \
		--target darwin-artifacts \
		--output type=local,dest=$(DIST)/darwin .
	@echo "Darwin binaries exported to $(DIST)/darwin/"

# Build multi-platform using pre-built binaries (from make build-all) + root DOCKERFILE
docker-from-dist:
	docker buildx build -f DOCKERFILE \
		--platform $(DOCKER_PLATFORMS) \
		-t $(DOCKER_IMAGE):$(VERSION) -t $(DOCKER_IMAGE):latest .

# Setup docker buildx builder (run once)
docker-buildx-setup:
	docker buildx create --name horizon-builder --use --bootstrap || docker buildx use horizon-builder

# Run in Docker
docker-run:
	docker-compose -f deployments/docker-compose.yml up -d horizon

# Stop Docker container
docker-stop:
	docker-compose -f deployments/docker-compose.yml down

# Run tests
test:
	go test -v ./...

# Run tests with coverage
test-coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Format code
fmt:
	go fmt ./...

# Lint code
lint:
	golangci-lint run ./...

# Clean build artifacts
clean:
	rm -rf $(DIST)
	rm -f horizon horizon.exe
	rm -f coverage.out coverage.html

# Download dependencies
deps:
	go mod download
	go mod tidy

# Generate (if needed)
generate:
	go generate ./...

# Help
help:
	@echo "Horizon Build System"
	@echo ""
	@echo "Usage:"
	@echo "  make build                    - Build for current platform"
	@echo "  make build-linux              - Build for Linux (amd64, arm64)"
	@echo "  make build-windows            - Build for Windows (amd64, arm64)"
	@echo "  make build-darwin             - Build for macOS (amd64, arm64)"
	@echo "  make build-all                - Build for all platforms"
	@echo "  make docker                   - Build Docker image (current arch)"
	@echo "  make docker-multiplatform     - Build multi-arch Docker images (amd64+arm64)"
	@echo "  make docker-multiplatform-push- Push multi-arch images to registry"
	@echo "  make docker-darwin-artifacts  - Extract Darwin binaries from Docker build"
	@echo "  make docker-from-dist         - Build multi-arch images from pre-built binaries"
	@echo "  make docker-buildx-setup      - Setup buildx builder (run once)"
	@echo "  make docker-build             - Build all platforms using Docker"
	@echo "  make docker-run               - Run in Docker"
	@echo "  make test                     - Run tests"
	@echo "  make clean                    - Clean build artifacts"
