#!/usr/bin/env bash
set -euo pipefail

# Install netcoredbg for the current platform.
# On macOS arm64: downloads pre-built binary from Cliffback/netcoredbg-macOS-arm64.nvim
# On other platforms: downloads from Samsung/netcoredbg official releases

VERSION="${1:-latest}"
INSTALL_DIR="${DOTNET_DEBUG_HOME:-$HOME/.dotnet-debug}/bin/netcoredbg"
OS="$(uname -s)"
ARCH="$(uname -m)"

echo "Installing netcoredbg to $INSTALL_DIR"
echo "Platform: $OS/$ARCH"

mkdir -p "$INSTALL_DIR"

if [[ "$OS" == "Darwin" && "$ARCH" == "arm64" ]]; then
    echo "Using Cliffback/netcoredbg-macOS-arm64.nvim (pre-built arm64)"

    if [[ "$VERSION" == "latest" ]]; then
        VERSION=$(gh release list -R Cliffback/netcoredbg-macOS-arm64.nvim --limit 1 --json tagName -q '.[0].tagName')
    fi
    echo "Version: $VERSION"

    TMPFILE=$(mktemp)
    gh release download "$VERSION" \
        -R Cliffback/netcoredbg-macOS-arm64.nvim \
        -p "netcoredbg-osx-arm64.tar.gz" \
        -D "$(dirname "$TMPFILE")" \
        --clobber \
        -O "$TMPFILE"

    tar xzf "$TMPFILE" -C "$(dirname "$INSTALL_DIR")"
    rm -f "$TMPFILE"

elif [[ "$OS" == "Darwin" && "$ARCH" == "x86_64" ]]; then
    ASSET="netcoredbg-osx-amd64.tar.gz"
    echo "Using Samsung/netcoredbg official release (x86_64)"

    if [[ "$VERSION" == "latest" ]]; then
        VERSION=$(gh release list -R Samsung/netcoredbg --limit 1 --json tagName -q '.[0].tagName')
    fi
    echo "Version: $VERSION"

    TMPFILE=$(mktemp)
    gh release download "$VERSION" -R Samsung/netcoredbg -p "$ASSET" -D "$(dirname "$TMPFILE")" --clobber -O "$TMPFILE"
    tar xzf "$TMPFILE" -C "$(dirname "$INSTALL_DIR")"
    rm -f "$TMPFILE"

elif [[ "$OS" == "Linux" ]]; then
    if [[ "$ARCH" == "aarch64" || "$ARCH" == "arm64" ]]; then
        ASSET="netcoredbg-linux-arm64.tar.gz"
    else
        ASSET="netcoredbg-linux-amd64.tar.gz"
    fi
    echo "Using Samsung/netcoredbg official release ($ARCH)"

    if [[ "$VERSION" == "latest" ]]; then
        VERSION=$(gh release list -R Samsung/netcoredbg --limit 1 --json tagName -q '.[0].tagName')
    fi
    echo "Version: $VERSION"

    TMPFILE=$(mktemp)
    gh release download "$VERSION" -R Samsung/netcoredbg -p "$ASSET" -D "$(dirname "$TMPFILE")" --clobber -O "$TMPFILE"
    tar xzf "$TMPFILE" -C "$(dirname "$INSTALL_DIR")"
    rm -f "$TMPFILE"

else
    echo "Unsupported platform: $OS/$ARCH"
    echo "For Windows, download from https://github.com/Samsung/netcoredbg/releases"
    echo "Extract to %USERPROFILE%\\.dotnet-debug\\bin\\netcoredbg\\"
    exit 1
fi

# Verify
if "$INSTALL_DIR/netcoredbg" --version; then
    echo ""
    echo "Installed successfully to: $INSTALL_DIR/netcoredbg"
    echo "dotnet-debug will find it automatically."
else
    echo "ERROR: Installation failed — binary doesn't run"
    exit 1
fi
