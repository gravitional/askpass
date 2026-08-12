#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

export CGO_ENABLED=0

BUILD_DIR="build"
GO_COMMAND=""

if command -v go >/dev/null 2>&1; then
    GO_COMMAND="go"
elif command -v go.exe >/dev/null 2>&1; then
    GO_COMMAND="go.exe"
else
    echo "Error: Go was not found in PATH" >&2
    exit 1
fi

GO_OS="$($GO_COMMAND env GOOS)"

mkdir -p "$BUILD_DIR"

"$GO_COMMAND" fmt ./...

for binary_name in ssh-wrapper scp-wrapper; do
    artifact_name="$binary_name"
    if [[ "$GO_OS" == "windows" ]]; then
        artifact_name="${binary_name}.exe"
    fi
    echo "Building . -> $BUILD_DIR/$artifact_name"
    "$GO_COMMAND" build -ldflags='-s -w' -o "$BUILD_DIR/$artifact_name" .
done

CONFIG_PATH="$BUILD_DIR/ssh-wrapper-config.toml"
if [[ ! -e "$CONFIG_PATH" ]]; then
    cp ssh-wrapper-config.toml "$CONFIG_PATH"
    echo "Created $CONFIG_PATH from the example config"
fi

echo "Build completed: $BUILD_DIR"
