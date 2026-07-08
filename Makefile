.PHONY: build build-all clean cov docker docker-run docker-stop format help init-solverd integrationtest lint proto run setup-test-env sqlc teardown-test-env test vet

VERSION ?= dev
LDFLAGS := -s -w -X main.Version=$(VERSION)

GOLANGCI_LINT ?= $(shell \
	echo "docker run --rm -v $$(pwd):/app -w /app golangci/golangci-lint:v2.9.0 golangci-lint"; \
)

## build: build solverd, solver and banco binaries
build:
	@echo "Building solverd..."
	@go build -o solverd ./cmd/solverd/
	@echo "Building solver CLI..."
	@go build -o solver ./cmd/solver/
	@echo "Building banco CLI..."
	@go build -o banco ./cmd/banco/

## run: build and run solverd locally against the fulmine test stack (arkd@7070, emulator@7273)
run: build
	@echo "Running solverd against local test stack..."
	@SOLVER_ARK_URL=localhost:7070 \
	SOLVER_EMULATOR_URL=localhost:7173 \
	SOLVER_WALLET_SEED=ed1f6ad1c0aa1bbdcc14a4dc26ff5d31cca6df11617f2bbb24a4e0e6f72f7a5d \
	SOLVER_WALLET_PASSWORD=password \
	SOLVER_DATADIR=$${SOLVER_DATADIR:-$$(mktemp -d)} \
	go run ./cmd/solverd/main.go

## init-solverd: fund the running solverd, mint an asset, and register pairs (run after `make run`)
init-solverd:
	@echo "Initializing solverd (fund + mint asset + register pairs)..."
	@ARK_URL=$${ARK_URL:-localhost:7070} \
	ARK_HTTP_URL=$${ARK_HTTP_URL:-http://localhost:7071} \
	SOLVER_GRPC=$${SOLVER_GRPC:-localhost:7170} \
	PRICEFEED_URL=$${PRICEFEED_URL:-http://localhost:8088} \
	go run ./test/init/

## build-all: cross-compile solverd and solver for linux/darwin × amd64/arm64 (release artifacts)
build-all:
	@echo "Building solverd and solver for all release platforms (VERSION=$(VERSION))..."
	@for goos in linux darwin; do \
		for goarch in amd64 arm64; do \
			for bin in solverd solver; do \
				echo "  -> $$bin-$$goos-$$goarch"; \
				CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch \
					go build -trimpath -ldflags "$(LDFLAGS)" \
					-o build/$$bin-$$goos-$$goarch ./cmd/$$bin/ || exit 1; \
			done; \
		done; \
	done

## proto: generate protobuf code
proto:
	@echo "Generating protobuf code..."
	@cd api-spec && buf generate

## sqlc: generate sqlc code
sqlc:
	@echo "Generating sqlc code..."
	@cd internal/infrastructure/db/sqlite/sqlc && sqlc generate

## docker: build production Docker image
docker:
	@echo "Building Docker image..."
	@docker build -t solverd .

## clean: cleans build artifacts
clean:
	@echo "Cleaning..."
	@go clean
	@rm -f solverd solver banco

## cov: generates coverage report
cov:
	@echo "Coverage..."
	@go test -cover ./...

## help: prints this help message
help:
	@echo "Usage: \n"
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' |  sed -e 's/^/ /'

## format: rewrite Go files in-place using gofmt + goimports
format:
	@echo "Formatting code..."
	@$(GOLANGCI_LINT) fmt

## lint: lint codebase
lint:
	@echo "Linting code..."
	@$(GOLANGCI_LINT) run --fix --tests=false

## test: runs unit tests (excludes test/e2e)
test:
	@echo "Running unit tests..."
	@go test -v -race --count=1 $(shell go list ./... | grep -v /test/e2e)

## docker-run: start arkd stack and fund wallet (assumes nigiri is running)
docker-run:
	@echo "Starting arkd stack..."
	@docker compose -f test/docker-compose.yml up -d --build
	@echo "Waiting for services..."
	@sleep 15
	@echo "Creating arkd wallet..."
	@docker exec solverd-arkd arkd wallet create --password password || true
	@docker exec solverd-arkd arkd wallet unlock --password password || true
	@echo "Funding arkd..."
	@for i in 1 2 3; do nigiri faucet $$(docker exec solverd-arkd arkd wallet address | tr -d '[:space:]') 1; done
	@sleep 5
	@echo "Test environment ready."

## docker-stop: stop arkd stack
docker-stop:
	@echo "Stopping arkd stack..."
	@docker compose -f test/docker-compose.yml down -v --remove-orphans

## setup-test-env: start nigiri + arkd stack for integration tests
setup-test-env:
	@echo "Starting nigiri..."
	@nigiri start
	@$(MAKE) docker-run

## teardown-test-env: stop arkd stack + nigiri
teardown-test-env:
	@$(MAKE) docker-stop
	@echo "Stopping nigiri..."
	@nigiri stop --delete

## integrationtest: run integration tests (requires setup-test-env)
integrationtest:
	@echo "Running integration tests..."
	@go test -v -count=1 -timeout=10m -race -p=1 ./test/e2e/...

## vet: code analysis
vet:
	@echo "Running code analysis..."
	@go vet ./...
