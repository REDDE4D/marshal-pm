.PHONY: ui build test version proto

# Version is derived from git tags (e.g. v0.1.0, or v0.1.0-3-gabc123 between
# tags, with -dirty when the tree has uncommitted changes). Falls back to the
# in-source default when git is unavailable.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")

# Build the web dashboard SPA into internal/dashboard/dist (embedded by Go).
ui:
	cd web && npm install && npm run build

# Build the marshal binary, stamping the version via -ldflags.
build:
	go build -ldflags "-X github.com/REDDE4D/marshal-pm/internal/version.Version=$(VERSION)" -o marshal ./cmd/marshal

# Print the version that `make build` would stamp.
version:
	@echo $(VERSION)

test:
	go test ./... -race -count=1

.PHONY: proto
proto: ## regenerate internal/pb from proto/marshal/v1
	./scripts/gen-proto.sh
