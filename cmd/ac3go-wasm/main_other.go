//go:build !(js && wasm)

// Non-wasm stub so `go build ./...` and `go vet ./...` stay green on every
// platform; the real entry point is main.go (js && wasm).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "ac3go-wasm targets WebAssembly only; build with: GOOS=js GOARCH=wasm go build ./cmd/ac3go-wasm (or `make wasm`)")
	os.Exit(1)
}
