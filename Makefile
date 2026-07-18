# ac3go - build, test, and the WebAssembly bundle.

GOROOT := $(shell go env GOROOT)
WASM_EXEC := $(GOROOT)/lib/wasm/wasm_exec.js

.PHONY: build test vet fmt wasm wasm-smoke clean

build: ## static library + CLI for the current platform
	CGO_ENABLED=0 go build ./...

test: ## run the tests (race is CI-only; no cgo locally)
	CGO_ENABLED=0 go test ./... -count=1

vet: ## vet for the host and for 32-bit, where the size guards can fire
	CGO_ENABLED=0 go vet ./...
	CGO_ENABLED=0 GOOS=linux GOARCH=386 go vet ./...

fmt: ## report anything gofmt would change
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

wasm: ## web/ac3go.wasm + web/wasm_exec.js (the browser bundle)
	CGO_ENABLED=0 GOOS=js GOARCH=wasm go build -o web/ac3go.wasm ./cmd/ac3go-wasm
	cp "$(WASM_EXEC)" web/wasm_exec.js
	@echo "built web/ac3go.wasm ($$(du -h web/ac3go.wasm | cut -f1)); serve web/ and open /example/"

wasm-smoke: wasm ## build the wasm bundle, then run the Node end-to-end check
	node scripts/wasm_smoke.mjs

clean:
	rm -f web/ac3go.wasm web/wasm_exec.js
