#!/bin/sh
# Install script for dotnet-debug
# Usage: curl -sSfL https://raw.githubusercontent.com/AiriaLLC/dotnet-debug/main/scripts/install.sh | sh
#
# Environment variables:
#   INSTALL_DIR  - installation directory (default: /usr/local/bin, or ~/.local/bin if no write access)
#   VERSION      - specific version to install (default: latest)

set -e

REPO="AiriaLLC/dotnet-debug"
BINARY="dotnet-debug"

# Detect OS
OS="$(uname -s)"
case "$OS" in
  Darwin)  OS="darwin" ;;
  Linux)   OS="linux" ;;
  MINGW*|MSYS*|CYGWIN*) OS="windows" ;;
  *) echo "Unsupported OS: $OS" >&2; exit 1 ;;
esac

# Detect architecture
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64)  ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

# Windows + arm64 is not supported
if [ "$OS" = "windows" ] && [ "$ARCH" = "arm64" ]; then
  echo "Windows arm64 is not supported. Use amd64." >&2
  exit 1
fi

# Determine version
if [ -z "$VERSION" ]; then
  VERSION="$(curl -sSf "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"v?([^"]+)".*/\1/')"
  if [ -z "$VERSION" ]; then
    echo "Failed to determine latest version." >&2
    exit 1
  fi
fi
# Strip leading 'v' if present for the archive name
VERSION_CLEAN="${VERSION#v}"

# Determine install directory
if [ -z "$INSTALL_DIR" ]; then
  if [ -w /usr/local/bin ]; then
    INSTALL_DIR="/usr/local/bin"
  else
    INSTALL_DIR="${HOME}/.local/bin"
    mkdir -p "$INSTALL_DIR"
  fi
fi

# Build download URL
if [ "$OS" = "windows" ]; then
  EXT="zip"
  ARCHIVE="${BINARY}_${VERSION_CLEAN}_${OS}_${ARCH}.${EXT}"
else
  EXT="tar.gz"
  ARCHIVE="${BINARY}_${VERSION_CLEAN}_${OS}_${ARCH}.${EXT}"
fi

URL="https://github.com/${REPO}/releases/download/v${VERSION_CLEAN}/${ARCHIVE}"

echo "Installing ${BINARY} v${VERSION_CLEAN} (${OS}/${ARCH})..."
echo "  from: ${URL}"
echo "  to:   ${INSTALL_DIR}/${BINARY}"

# Download and extract
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

curl -sSfL "$URL" -o "${TMP_DIR}/${ARCHIVE}"

case "$EXT" in
  tar.gz)
    tar -xzf "${TMP_DIR}/${ARCHIVE}" -C "$TMP_DIR"
    ;;
  zip)
    unzip -q "${TMP_DIR}/${ARCHIVE}" -d "$TMP_DIR"
    ;;
esac

# Install binary
install -m 755 "${TMP_DIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"

echo "Installed ${BINARY} v${VERSION_CLEAN} to ${INSTALL_DIR}/${BINARY}"

# Verify
if command -v "$BINARY" >/dev/null 2>&1; then
  echo "Verify: $("$BINARY" version)"
else
  echo ""
  echo "NOTE: ${INSTALL_DIR} may not be in your PATH."
  echo "Add it with:  export PATH=\"${INSTALL_DIR}:\$PATH\""
fi
