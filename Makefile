# Every check is defined here once; git hooks and CI call these targets.
# Tools are pinned in tools/<name>/go.mod and run with `go tool`.

TOOL = go tool -modfile=tools/$(1)/go.mod $(1)

.PHONY: help check fmt lint dockerlint vet test cover vuln secrets secrets-staged tidy build integration hooks clean

# Linters that aren't Go programs; required so local runs match CI.
need = @command -v $(1) >/dev/null || { echo "$(1) is required: brew install $(1) (or your package manager)"; exit 1; }

help: ## list targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "}{printf "  %-16s %s\n", $$1, $$2}'

check: lint dockerlint vet cover vuln secrets tidy build ## everything CI runs

fmt: ## format code
	$(call TOOL,golangci-lint) fmt ./...

lint: ## static analysis, security lint, workflow lint
	$(call need,shellcheck)
	$(call TOOL,golangci-lint) run ./...
	$(call TOOL,actionlint)

dockerlint: ## Dockerfile lint
	$(call need,hadolint)
	hadolint Dockerfile sandbox/Dockerfile

vet:
	go vet ./... && go vet -tags integration ./...

test: ## unit tests with the race detector
	go test -race -count=1 ./...

cover: ## unit tests with coverage thresholds
	scripts/coverage.sh

vuln: ## known vulnerabilities in reachable code, including the standard library
	$(call TOOL,govulncheck) ./...

secrets: ## scan the whole git history for secrets
	$(call TOOL,gitleaks) git --no-banner --redact .

secrets-staged: ## scan staged changes for secrets
	$(call TOOL,gitleaks) git --no-banner --redact --staged .

tidy: ## go.mod and go.sum are tidy, for the module and every tool
	@set -e; for m in . tools/*; do (cd $$m && go mod tidy -diff) || \
		{ echo "$$m: go.mod/go.sum are not tidy; run 'go mod tidy' there"; exit 1; }; done

build: ## cross-compile every release target
	@set -e; for t in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do \
		GOOS=$${t%/*} GOARCH=$${t#*/} CGO_ENABLED=0 go build -o /dev/null ./cmd/pocketagent; done

integration: ## tests against real agents, whisper and Docker (costs a few cents)
	go test -tags integration -count=1 ./...

hooks: ## install the git hooks
	git config core.hooksPath .githooks
	@echo "hooks installed: pre-commit (secrets, lint) and pre-push (make check)"

clean:
	rm -rf dist coverage.out
