#!/usr/bin/env bash
# Regenerates the Go bindings for the .proto files under proto/, plus
# each plugin's own proto/ directory (wiki/plugin/<name>/proto/v1/*.proto -
# e.g. wiki/plugin/ledger's PluginData payload messages, see its own doc
# comment), into gen/. Requires protoc, protoc-gen-go, and
# protoc-gen-go-grpc on $PATH
# (`go install google.golang.org/protobuf/cmd/protoc-gen-go@latest` and
# `go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`).
set -euo pipefail
cd "$(dirname "$0")/.."

protoc \
  --go_out=. --go_opt=module=git.noncepad.com/pkg/optimizer \
  --go-grpc_out=. --go-grpc_opt=module=git.noncepad.com/pkg/optimizer \
  proto/wiki/v1/wiki.proto \
  proto/wikiadmin/v1/admin.proto \
  proto/chat/v1/chat.proto \
  proto/ledger/v1/ledger.proto \
  proto/coder/v1/build.proto \
  proto/coder/v1/flow.proto \
  proto/coder/v1/machine.proto
