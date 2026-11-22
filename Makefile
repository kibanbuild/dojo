.PHONY: build verify lint test test-integration e2e vuln tools sqlc sqlc-check dev down

# Pinned tool versions, installed into the repo (never a shared bin) so the local gate and CI
# run exactly the same ones. They are built for this machine; GOOS only changes what they check.
GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK_VERSION := v1.9.0
SQLC_VERSION := v1.31.1
TOOLS := $(CURDIR)/.tools
GOLANGCI_LINT := $(TOOLS)/golangci-lint-$(GOLANGCI_LINT_VERSION)
GOVULNCHECK := $(TOOLS)/govulncheck-$(GOVULNCHECK_VERSION)
SQLC := $(TOOLS)/sqlc-$(SQLC_VERSION)

# The integration stack runs under its own compose project, apart from any other.
IT_PROJECT := dojo-it
COMPOSE := EDGE_PORT= FAKE_GITHUB_PORT= docker compose -p $(IT_PROJECT) -f compose.yaml -f compose.it.yaml --env-file .env.example

# The dev stack is the default compose project (dojo); a local .env wins over the fixed values.
# compose.dev.yaml makes http://localhost:4321 (Astro) the public origin, which only this path uses.
DEV_COMPOSE := docker compose -f compose.yaml -f compose.dev.yaml $(if $(wildcard .env),,--env-file .env.example)
NODE_VERSION := $(shell cat web/.nvmrc)

# The SPA dev server runs as the node process itself, so its pid file holds Vite's own pid. A recorded
# pid only counts when that process is still a Vite: a stale file is never trusted or killed.
SPA_PID := $(CURDIR)/app/.dev.pid
SPA_UP = [ -s $(SPA_PID) ] && ps -p "$$(cat $(SPA_PID))" -o command= 2>/dev/null | grep -q 'vite/bin/vite.js'

# The CLI ships for these; lint and vet each so platform-only files are checked too.
PLATFORMS := darwin linux windows

verify: lint test vuln sqlc-check

build:
	go build -o bin/dojo ./cmd/dojo

tools: $(GOLANGCI_LINT) $(GOVULNCHECK) $(SQLC)

$(GOLANGCI_LINT):
	GOOS= GOARCH= GOBIN=$(TOOLS) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv $(TOOLS)/golangci-lint $@

$(GOVULNCHECK):
	GOOS= GOARCH= GOBIN=$(TOOLS) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	mv $(TOOLS)/govulncheck $@

$(SQLC):
	GOOS= GOARCH= GOBIN=$(TOOLS) go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	mv $(TOOLS)/sqlc $@

# Regenerates the committed sqlc code from db/migrations and the queries.
sqlc: $(SQLC)
	$(SQLC) generate

# Fails when the committed generated code is stale.
sqlc-check: $(SQLC)
	$(SQLC) diff

lint: $(GOLANGCI_LINT)
	@for os in $(PLATFORMS); do \
		echo "lint $$os"; \
		GOOS=$$os $(GOLANGCI_LINT) run ./... || exit 1; \
		GOOS=$$os go vet -tags "e2e integration" ./... || exit 1; \
	done

test:
	go test -race -count=1 -cover ./...

# Needs Docker. Starts the stack from a clean state, tests it, and always tears it down.
test-integration:
	@$(COMPOSE) down -v --remove-orphans
	@set -e; trap '$(COMPOSE) down -v --remove-orphans' EXIT; \
	$(COMPOSE) up -d --build --wait; \
	port=$$($(COMPOSE) port api 8080 | sed 's/.*://'); \
	edge=$$($(COMPOSE) port edge 8080 | sed 's/.*://'); \
	fake=$$($(COMPOSE) port fake-github 8081 | sed 's/.*://'); \
	DOJO_API_URL=http://127.0.0.1:$$port DOJO_EDGE_URL=http://127.0.0.1:$$edge DOJO_FAKE_GITHUB_URL=http://127.0.0.1:$$fake DOJO_COMPOSE_PROJECT=$(IT_PROJECT) \
		go test -tags integration -count=1 -v ./internal/server/ ./internal/github/ \
		|| { rc=$$?; $(COMPOSE) logs --no-color --tail=100 api fake-github; exit $$rc; }

# Starts the stack in the background, then the SPA's Vite dev server (background, its own pid in app/.dev.pid)
# and the web dev server in the foreground (Astro detaches when there is no terminal). The web dev
# server proxies /v1 to the edge and /app to Vite, so page, API and SPA share one origin as behind
# Caddy. With a terminal, leaving Astro also stops Vite; without one, `make down` does.
dev:
	@node --version 2>/dev/null | grep -qx 'v$(NODE_VERSION)' || \
		{ echo "make dev needs Node $(NODE_VERSION) (web/.nvmrc); with fnm: fnm install && fnm use"; exit 1; }
	$(DEV_COMPOSE) up -d --build --wait
	cd web && corepack pnpm install --frozen-lockfile
	cd app && corepack pnpm install --frozen-lockfile
	@if $(SPA_UP); then echo "SPA dev server already running (pid $$(cat $(SPA_PID)))"; else \
		cd app && { nohup node node_modules/vite/bin/vite.js > .dev.log 2>&1 & echo $$! > .dev.pid; }; \
		for i in 1 2 3 4 5 6 7 8 9 10; do curl -fs --max-time 2 -o /dev/null http://127.0.0.1:5174/app/ 2>/dev/null && break; sleep 1; done; \
		sleep 1; $(SPA_UP) || { echo "SPA dev server failed to start:"; tail -n 20 .dev.log; exit 1; }; \
	fi
	@edge=$$($(DEV_COMPOSE) port edge 8080 | sed 's/.*://'); \
		if [ -t 1 ]; then trap 'if $(SPA_UP); then kill $$(cat $(SPA_PID)); fi; rm -f $(SPA_PID)' EXIT; fi; \
		cd web && DOJO_EDGE_URL=http://127.0.0.1:$$edge ASTRO_TELEMETRY_DISABLED=1 corepack pnpm dev

# Stops the dev stack, keeping its database volume, the SPA dev server (by its own pid file, never by
# pattern) and a web dev server Astro left in the background (it detaches when there is no terminal).
down:
	$(DEV_COMPOSE) down
	-@if $(SPA_UP); then kill $$(cat $(SPA_PID)); fi; rm -f $(SPA_PID)
	-@cd web && corepack pnpm exec astro dev stop 2>/dev/null

# Boots a real Lima VM and takes minutes: not part of verify.
e2e:
	go test -tags e2e -count=1 -v -timeout 30m ./internal/lab/

vuln: $(GOVULNCHECK)
	$(GOVULNCHECK) ./...
