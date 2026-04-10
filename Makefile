# Makefile for Scheduler Service

include ../../../app.mk

# Scheduler-specific variables
SCHEDULER_IMAGE_NAME ?= menta2l/scheduler-service
SCHEDULER_IMAGE_TAG ?= $(VERSION)
DOCKER_REGISTRY ?=

# Build the server binary
.PHONY: build-server
build-server:
	@echo "Building Scheduler server..."
	@go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o ./bin/scheduler-server ./cmd/server

# Build Docker image
.PHONY: docker
docker:
	@echo "Building Docker image $(SCHEDULER_IMAGE_NAME):$(SCHEDULER_IMAGE_TAG)..."
	@docker build \
		-t $(SCHEDULER_IMAGE_NAME):$(SCHEDULER_IMAGE_TAG) \
		-t $(SCHEDULER_IMAGE_NAME):latest \
		--build-arg APP_VERSION=$(VERSION) \
		-f ./Dockerfile \
		../../../

# Build Docker image with custom registry
.PHONY: docker-tag
docker-tag: docker
ifdef DOCKER_REGISTRY
	@echo "Tagging image for registry $(DOCKER_REGISTRY)..."
	@docker tag $(SCHEDULER_IMAGE_NAME):$(SCHEDULER_IMAGE_TAG) $(DOCKER_REGISTRY)/$(SCHEDULER_IMAGE_NAME):$(SCHEDULER_IMAGE_TAG)
	@docker tag $(SCHEDULER_IMAGE_NAME):latest $(DOCKER_REGISTRY)/$(SCHEDULER_IMAGE_NAME):latest
endif

# Push Docker image to registry
.PHONY: docker-push
docker-push: docker-tag
ifdef DOCKER_REGISTRY
	@echo "Pushing image to $(DOCKER_REGISTRY)..."
	@docker push $(DOCKER_REGISTRY)/$(SCHEDULER_IMAGE_NAME):$(SCHEDULER_IMAGE_TAG)
	@docker push $(DOCKER_REGISTRY)/$(SCHEDULER_IMAGE_NAME):latest
else
	@echo "Pushing image to Docker Hub..."
	@docker push $(SCHEDULER_IMAGE_NAME):$(SCHEDULER_IMAGE_TAG)
	@docker push $(SCHEDULER_IMAGE_NAME):latest
endif

# Run the server locally
.PHONY: run-server
run-server:
	@go run ./cmd/server -c ./configs

# Generate ent schema
.PHONY: ent
ent:
ifneq ("$(wildcard ./internal/data/ent)","")
	@ent generate \
		--feature sql/modifier \
		--feature sql/upsert \
		--feature sql/lock \
		./internal/data/ent/schema
endif

# Generate wire dependencies
.PHONY: wire
wire:
	@cd ./cmd/server && wire

# Run tests
.PHONY: test
test:
	@go test -v ./...

# Run tests with coverage
.PHONY: test-cover
test-cover:
	@go test -v -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

# Clean build artifacts
.PHONY: clean
clean:
	@rm -rf ./bin
	@rm -f coverage.out coverage.html
	@echo "Clean complete!"

# Generate all (ent + wire + proto)
.PHONY: generate
generate: ent wire
	@echo "Generation complete!"
