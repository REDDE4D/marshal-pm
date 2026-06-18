.PHONY: ui build test

# Build the web dashboard SPA into internal/dashboard/dist (embedded by Go).
ui:
	cd web && npm install && npm run build

# Build the marshal binary.
build:
	go build -o marshal ./cmd/marshal

test:
	go test ./... -race -count=1
