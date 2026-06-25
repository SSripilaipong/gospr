WEB := sandbox/web

.PHONY: all build web rebuild check test vet run sandbox clean

# Build everything: the embedded sandbox SPA, then the Go binaries.
all: web build

# Compile the Go packages (needs z3 on PATH — the builder proves convergence on deploy).
build:
	go build ./...

# Rebuild the sandbox SPA into sandbox/web/dist (committed + //go:embed-ed, so a plain
# `go build` needs no Node). Run this after editing anything under sandbox/web/src.
web rebuild:
	cd $(WEB) && npm install && npm run build

# Rebuild the SPA and then the Go binary in one step.
check: web build vet

test:
	go test -timeout 60s ./...

vet:
	go vet ./...

run:
	go run . server local

sandbox:
	go run . sandbox run --nodes=5 --port=9060

clean:
	rm -rf $(WEB)/dist $(WEB)/node_modules
