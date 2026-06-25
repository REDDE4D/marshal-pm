#!/bin/sh
# Marshal installer — downloads the latest (or $MARSHAL_VERSION) release binary
# from GitHub, verifies its checksum, and installs it to $BIN_DIR.
#
#   curl -fsSL https://raw.githubusercontent.com/REDDE4D/marshal-pm/main/install.sh | sh
#
# Env overrides:
#   MARSHAL_VERSION=v0.4.0   pin a version (default: latest release)
#   BIN_DIR=/usr/local/bin   install location (default: /usr/local/bin, or
#                            ~/.local/bin if the former is not writable)
set -eu

REPO="REDDE4D/marshal-pm"
BIN_DIR="${BIN_DIR:-/usr/local/bin}"

err() { echo "install: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || err "required command not found: $1"; }

need uname
need tar
if command -v curl >/dev/null 2>&1; then
  dl() { curl -fsSL "$1"; }
  dlo() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  dl() { wget -qO- "$1"; }
  dlo() { wget -qO "$2" "$1"; }
else
  err "need curl or wget"
fi

# Detect OS/arch and map to GoReleaser's naming.
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin) ;;
  *) err "unsupported OS: $os (Marshal ships linux and darwin only)";;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch="amd64";;
  arm64|aarch64) arch="arm64";;
  *) err "unsupported architecture: $arch";;
esac

# Resolve the version (latest release tag unless pinned).
version="${MARSHAL_VERSION:-}"
if [ -z "$version" ]; then
  version=$(dl "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | cut -d'"' -f4)
  [ -n "$version" ] || err "could not determine the latest release tag"
fi
ver_nov="${version#v}" # archive names use the version without the leading v

archive="marshal_${ver_nov}_${os}_${arch}.tar.gz"
base="https://github.com/${REPO}/releases/download/${version}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "install: downloading ${archive} (${version})"
dlo "${base}/${archive}" "${tmp}/${archive}" || err "download failed: ${base}/${archive}"

# Verify the checksum if checksums.txt is present and a sha256 tool exists.
if dlo "${base}/checksums.txt" "${tmp}/checksums.txt" 2>/dev/null; then
  if command -v sha256sum >/dev/null 2>&1; then
    sha=$(sha256sum "${tmp}/${archive}" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    sha=$(shasum -a 256 "${tmp}/${archive}" | awk '{print $1}')
  else
    sha=""
  fi
  if [ -n "$sha" ]; then
    grep -q "$sha" "${tmp}/checksums.txt" || err "checksum verification failed for ${archive}"
    echo "install: checksum OK"
  fi
fi

tar -xzf "${tmp}/${archive}" -C "$tmp"
[ -f "${tmp}/marshal" ] || err "archive did not contain a 'marshal' binary"
chmod +x "${tmp}/marshal"

# Pick a writable bin dir.
if [ ! -d "$BIN_DIR" ] || [ ! -w "$BIN_DIR" ]; then
  if [ -w "$(dirname "$BIN_DIR")" ] 2>/dev/null; then
    :
  else
    BIN_DIR="${HOME}/.local/bin"
    mkdir -p "$BIN_DIR"
  fi
fi

if mv "${tmp}/marshal" "${BIN_DIR}/marshal" 2>/dev/null; then
  :
elif command -v sudo >/dev/null 2>&1; then
  echo "install: ${BIN_DIR} needs elevated permissions"
  sudo mv "${tmp}/marshal" "${BIN_DIR}/marshal"
else
  err "cannot write to ${BIN_DIR}; set BIN_DIR to a writable location"
fi

echo "install: installed marshal ${version} to ${BIN_DIR}/marshal"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    echo "install: NOTE — ${BIN_DIR} is not on your PATH. Add it:"
    echo "    export PATH=\"${BIN_DIR}:\$PATH\"                       # current shell"
    echo "    echo 'export PATH=\"${BIN_DIR}:\$PATH\"' >> ~/.bashrc   # persist (bash)"
    echo "install: (or run it directly: ${BIN_DIR}/marshal)"
    ;;
esac
"${BIN_DIR}/marshal" --version 2>/dev/null || true
