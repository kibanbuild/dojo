.PHONY: verify lint test e2e vuln tools

# Pinned tool versions, installed into the repo (never a shared bin) so the local gate and CI
# run exactly the same ones. They are built for this machine; GOOS only changes what they check.
GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK_VERSION := v1.9.0
TOOLS := $(CURDIR)/.tools
GOLANGCI_LINT := $(TOOLS)/golangci-lint-$(GOLANGCI_LINT_VERSION)
GOVULNCHECK := $(TOOLS)/govulncheck-$(GOVULNCHECK_VERSION)

# The CLI ships for these; lint and vet each so platform-only files are checked too.
PLATFORMS := darwin linux windows

verify: lint test vuln

tools: $(GOLANGCI_LINT) $(GOVULNCHECK)

$(GOLANGCI_LINT):
	GOOS= GOARCH= GOBIN=$(TOOLS) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv $(TOOLS)/golangci-lint $@

$(GOVULNCHECK):
	GOOS= GOARCH= GOBIN=$(TOOLS) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	mv $(TOOLS)/govulncheck $@

lint: $(GOLANGCI_LINT)
	@for os in $(PLATFORMS); do \
		echo "lint $$os"; \
		GOOS=$$os $(GOLANGCI_LINT) run ./... || exit 1; \
		GOOS=$$os go vet -tags e2e ./... || exit 1; \
	done

test:
	go test -race -count=1 -cover ./...

# Boots a real Lima VM and takes minutes: not part of verify.
e2e:
	go test -tags e2e -count=1 -v -timeout 30m ./internal/lab/

vuln: $(GOVULNCHECK)
	$(GOVULNCHECK) ./...
