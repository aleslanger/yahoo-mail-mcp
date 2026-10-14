GO ?= go
BUILD_FLAGS := -buildvcs=false
CONFIG ?= $(CURDIR)/config.json
MAIL_ACCOUNT ?= personal
CODEX ?= codex
export MAIL_ACCOUNT MAIL_ADDRESS
.DEFAULT_GOAL := help
.PHONY: help test race vet build check clean dist config-init config-check storage-status account-test account-add account-list account-remove smtp-test credential-store codex update

help:
	@printf '%s\n' \
		'Yahoo Mail MCP commands:' \
		'  make build         Build the local yahoo-mcp binary' \
		'  make config-init   Copy examples/config.json if config.json is absent' \
		'  make config-check  Validate JSON configuration (does not log in)' \
		'  make storage-status Report private journal and download usage without login' \
		'  make account-test  Test read-only IMAP login (MAIL_ACCOUNT=personal)' \
		'  make account-add   Add a read-only Yahoo account (MAIL_ACCOUNT=work MAIL_ADDRESS=work@example.com)' \
		'  make account-list  List configured accounts without secrets' \
		'  make account-remove Remove account from config (MAIL_ACCOUNT=work)' \
		'  make smtp-test     Test SMTP login without sending mail (MAIL_ACCOUNT=personal)' \
		'  make credential-store  Save app password in a private file (MAIL_ACCOUNT=personal)' \
		'  make codex         Prompt for app password and launch Codex with MCP access' \
		'  make test          Run Go tests' \
		'  make race          Run Go tests with race detector' \
		'  make vet           Run go vet' \
		'  make check         Run tests, race tests, vet and build' \
		'  make update        Fast-forward clean Git checkout, then run check' \
		'  make dist          Build release binaries and checksums' \
		'  make clean         Remove generated binaries'

build:
	$(GO) build $(BUILD_FLAGS) -o yahoo-mcp ./cmd/yahoo-mcp

config-init:
	@if [ -e "$(CONFIG)" ] || [ -L "$(CONFIG)" ]; then \
		echo "Configuration already exists: $(CONFIG)"; \
	else \
		cp examples/config.json "$(CONFIG)" && chmod 600 "$(CONFIG)" && echo "Created $(CONFIG)"; \
	fi

config-check: build
	YAHOO_MCP_CONFIG="$(CONFIG)" ./yahoo-mcp config-validate

storage-status: build
	@YAHOO_MCP_CONFIG="$(CONFIG)" ./yahoo-mcp storage-status

account-test: build
	YAHOO_MCP_CONFIG="$(CONFIG)" ./yahoo-mcp account-test "$$MAIL_ACCOUNT"

account-add: build
	@test -n "$$MAIL_ADDRESS" || { echo 'Set MAIL_ADDRESS to the account address.' >&2; exit 1; }
	YAHOO_MCP_CONFIG="$(CONFIG)" ./yahoo-mcp account-add "$$MAIL_ACCOUNT" "$$MAIL_ADDRESS"

account-list: build
	YAHOO_MCP_CONFIG="$(CONFIG)" ./yahoo-mcp accounts-list

account-remove: build
	YAHOO_MCP_CONFIG="$(CONFIG)" ./yahoo-mcp account-remove "$$MAIL_ACCOUNT"

smtp-test: build
	YAHOO_MCP_CONFIG="$(CONFIG)" ./yahoo-mcp smtp-test "$$MAIL_ACCOUNT"

credential-store:
	@./scripts/store-personal-password.sh "$$MAIL_ACCOUNT"

codex: config-check
	@command -v "$(CODEX)" >/dev/null 2>&1 || { echo 'Codex command not found.' >&2; exit 1; }
	@tty=/dev/tty; \
		old_stty=$$(stty -g < "$$tty") || { echo 'An interactive terminal is required.' >&2; exit 1; }; \
		trap 'stty "$$old_stty" < "$$tty"' EXIT HUP INT TERM; \
		stty -echo < "$$tty" || exit 1; \
		printf 'Yahoo app password: ' > "$$tty"; \
		IFS= read -r YAHOO_MCP_PASSWORD_PERSONAL < "$$tty"; read_status=$$?; \
		stty "$$old_stty" < "$$tty"; trap - EXIT HUP INT TERM; \
		printf '\n' > "$$tty"; \
		if [ "$$read_status" -ne 0 ] || [ -z "$$YAHOO_MCP_PASSWORD_PERSONAL" ]; then \
			echo 'No app password entered.' >&2; exit 1; \
		fi; \
		export YAHOO_MCP_PASSWORD_PERSONAL; \
		export YAHOO_MCP_CONFIG="$(CONFIG)"; \
		echo 'Starting a new Codex session with Yahoo MCP credentials; use this session for mail tools.' >&2; \
		exec "$(CODEX)"

test:
	$(GO) test ./...
race:
	$(GO) test -race ./...
vet:
	$(GO) vet ./...
check: test race vet build

update:
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo 'Update requires a clean Git checkout. Commit or stash local changes first.' >&2; \
		exit 1; \
	fi
	@git rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' >/dev/null || { \
		echo 'Update requires a branch with an upstream remote.' >&2; \
		exit 1; \
	}
	git pull --ff-only
	$(MAKE) check

dist:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -trimpath -o dist/yahoo-mcp-linux-amd64 ./cmd/yahoo-mcp
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(BUILD_FLAGS) -trimpath -o dist/yahoo-mcp-linux-arm64 ./cmd/yahoo-mcp
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -trimpath -o dist/yahoo-mcp-darwin-amd64 ./cmd/yahoo-mcp
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build $(BUILD_FLAGS) -trimpath -o dist/yahoo-mcp-darwin-arm64 ./cmd/yahoo-mcp
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -trimpath -o dist/yahoo-mcp-windows-amd64.exe ./cmd/yahoo-mcp
	cd dist && sha256sum yahoo-mcp-* > SHA256SUMS
clean:
	rm -rf dist yahoo-mcp
