.PHONY: dev api web web-dev test build run deps

# Run API (:8080) and dashboard (:5173, proxies /api) together.
# Installs web dependencies first if they are missing or out of date.
dev: deps
	@$(MAKE) -j2 api web-dev

deps: web/node_modules/.installed

# Re-run npm ci whenever the lockfile changes.
web/node_modules/.installed: web/package.json web/package-lock.json
	cd web && npm ci --no-audit --no-fund
	@touch $@

api:
	cd backend && go run ./cmd/auris

web: deps
	cd web && npm run dev

# Under `make dev`, hold Vite back until the API is listening (up to 60s,
# `go run` compiles first) so the proxy doesn't log ECONNREFUSED at startup.
web-dev: deps
	@for i in $$(seq 120); do nc -z 127.0.0.1 8080 2>/dev/null && break; sleep 0.5; done
	cd web && npm run dev

test: deps
	cd backend && go vet ./... && go test -race ./...
	cd web && npm run typecheck

# Single binary that also serves the built dashboard.
build: deps
	cd web && npm run build
	cd backend && go build -o bin/auris ./cmd/auris

run: build
	AURIS_STATIC_DIR=web/dist backend/bin/auris
