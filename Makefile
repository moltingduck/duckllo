.PHONY: help setup db serve runner selfhost build test vet fmt clean

help:
	@echo "duckllo development targets"
	@echo ""
	@echo "  make setup     copy .duckllo.env.example to .duckllo.env (one-time)"
	@echo "  make db        bring up Postgres in the background"
	@echo "  make serve     run the duckllo server (server reads .duckllo.env)"
	@echo "  make selfhost  bootstrap duckllo developing duckllo:"
	@echo "                 mint a project API key, seed harness rules,"
	@echo "                 write .duckllo.env. Idempotent."
	@echo "  make runner    run the harness runner against the server"
	@echo "  make build     compile all binaries to ./bin/"
	@echo "  make test      run the integration test (requires Postgres at localhost:5432)"
	@echo "  make vet       go vet ./..."
	@echo "  make fmt       gofmt -w ."

setup:
	@if [ -e .duckllo.env ]; then \
		echo ".duckllo.env already exists — refusing to overwrite."; exit 1; \
	fi
	@cp .duckllo.env.example .duckllo.env
	@echo ".duckllo.env created. Edit it to set DUCKLLO_GIN_PASSWORD, DUCKLLO_KEY, DUCKLLO_PROJECT, ANTHROPIC_API_KEY."

db:
	docker compose up db -d

serve:
	go run ./cmd/duckllo serve

selfhost:
	go run ./cmd/duckllo selfhost

runner:
	go run ./cmd/runner

# Drain the queue end-to-end (plan + execute + validate for one spec)
# and exit on first idle claim. Useful for one-shot dogfood verification:
#   make selfhost && make serve & make runner-once
runner-once:
	go run ./cmd/runner --exit-when-idle

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -o bin/duckllo     ./cmd/duckllo
	CGO_ENABLED=0 go build -o bin/runner      ./cmd/runner
	CGO_ENABLED=0 go build -o bin/mcp-duckllo ./cmd/mcp-duckllo

test:
	@if [ -z "$$TEST_DATABASE_URL" ]; then \
		echo "TEST_DATABASE_URL not set — pointing at default localhost:5432"; \
		TEST_DATABASE_URL='postgres://duckllo:duckllo@localhost:5432/duckllo?sslmode=disable' go test ./...; \
	else \
		go test ./...; \
	fi

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin
	docker compose down
