#!/usr/bin/env bash
# Regenerate internal/pb from proto/marshal/v1/*.proto.
# Requires: protoc, and protoc-gen-go (go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11).
set -euo pipefail
cd "$(dirname "$0")/.."
command -v protoc >/dev/null || { echo "protoc not found (brew install protobuf)"; exit 1; }
command -v protoc-gen-go >/dev/null || { echo "protoc-gen-go not found (go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11)"; exit 1; }
command -v protoc-gen-go-grpc >/dev/null || { echo "protoc-gen-go-grpc not found (go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest)"; exit 1; }
protoc -I proto \
  --go_out=. --go_opt=module=marshal \
  --go-grpc_out=. --go-grpc_opt=module=marshal \
  proto/marshal/v1/*.proto
echo "regenerated internal/pb"
