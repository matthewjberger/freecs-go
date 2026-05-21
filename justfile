set windows-shell := ["powershell.exe"]

# Lists all available recipes
@just:
    just --list

# Library: runs all tests
test:
    go test ./...

# Library: go vet + gofmt -l (fails if anything is unformatted)
check:
    go vet ./...
    $unformatted = (gofmt -l . | Out-String).Trim(); if ($unformatted) { Write-Host $unformatted; exit 1 }

# Library: gofmt -w
format:
    gofmt -w .

# Library: go mod tidy
tidy:
    go mod tidy

# Library: shows what `go mod tidy` would change
tidy-check:
    go mod tidy -diff

# Library: deps with available updates
outdated:
    go list -m -u all

# Library: check + test (run before pushing)
ci: check test

# Library: full read-only audit
audit: check tidy-check outdated test

# Breakout (desktop): build and run
run:
    go run -C examples/breakout .

# Breakout (desktop): build only
build:
    go build -C examples/breakout .

# Breakout (wasm): build into examples/breakout/docs/
build-wasm:
    cd examples/breakout; $env:GOOS = "js"; $env:GOARCH = "wasm"; go build -o docs/main.wasm .
    cp ((go env GOROOT) + "/lib/wasm/wasm_exec.js") examples/breakout/docs/wasm_exec.js

# Breakout (wasm): serve docs/ at http://localhost:8080
serve:
    go run -C examples/breakout ./cmd/serve

# Breakout (wasm): build + serve
run-wasm: build-wasm serve

# Removes generated artifacts
clean:
    Remove-Item -Force -ErrorAction SilentlyContinue examples/breakout/breakout.exe
    Remove-Item -Force -ErrorAction SilentlyContinue examples/breakout/docs/main.wasm

# Displays Go tool version
@versions:
    go version
