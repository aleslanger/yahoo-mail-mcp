GO ?= go
BUILD_FLAGS := -buildvcs=false
.PHONY: test race vet build check clean dist
build:
	$(GO) build $(BUILD_FLAGS) -o yahoo-mcp ./cmd/yahoo-mcp
test:
	$(GO) test ./...
race:
	$(GO) test -race ./...
vet:
	$(GO) vet ./...
check: test race vet build
dist:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -trimpath -o dist/yahoo-mcp-linux-amd64 ./cmd/yahoo-mcp
	GOOS=linux GOARCH=arm64 $(GO) build $(BUILD_FLAGS) -trimpath -o dist/yahoo-mcp-linux-arm64 ./cmd/yahoo-mcp
	GOOS=darwin GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -trimpath -o dist/yahoo-mcp-darwin-amd64 ./cmd/yahoo-mcp
	GOOS=darwin GOARCH=arm64 $(GO) build $(BUILD_FLAGS) -trimpath -o dist/yahoo-mcp-darwin-arm64 ./cmd/yahoo-mcp
	GOOS=windows GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -trimpath -o dist/yahoo-mcp-windows-amd64.exe ./cmd/yahoo-mcp
	cd dist && sha256sum yahoo-mcp-* > SHA256SUMS
clean:
	rm -rf dist yahoo-mcp
