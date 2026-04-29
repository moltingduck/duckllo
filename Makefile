.PHONY: help setup db serve runner runner-once selfhost dogfood-smoke build test vet fmt clean

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

# Full duckllo-on-duckllo smoke: spins up Postgres + server, runs
# selfhost, clones the repo into a scratch workspace, creates a
# trivial spec, drives the runner with claude-code, asserts the
# expected file change happened. Tears everything down on exit.
# Requires: docker, the `claude` CLI on PATH and logged in.
dogfood-smoke:
	./scripts/dogfood-smoke.sh

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -o bin/duckllo     ./cmd/duckllo
	CGO_ENABLED=0 go build -o bin/runner      ./cmd/runner
	CGO_ENABLED=0 go build -o bin/mcp-duckllo ./cmd/mcp-duckllo

test:
	@if [ -z "$$TEST_DATABASE_URL" ]; then \
		echo "TEST_DATABASE_URL not set — pointing at default localhost:5432"; \
		TEST_DATABASE_URL='postgres://duckllo:duckllo@localhost:5432/duckllo?sslmode=disable' \
			go test -p 1 ./...; \
	else \
		go test -p 1 ./...; \
	fi
	@# -p 1 forces serial test-package execution. Multiple packages
	@# (internal/http and internal/selfhost) wipe + re-migrate the same
	@# DB; running them concurrently corrupts state. -count would also
	@# defeat the test cache; we keep the cache so re-runs are fast,
	@# only forcing serial when actually executing.

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin
	docker compose down
