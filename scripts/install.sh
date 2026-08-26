#!/usr/bin/env bash
# Scraps installer — downloads a release archive from GitHub and installs
# the `scrap` and `scrapd` binaries.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/peelar/scraps/main/scripts/install.sh | bash
#
# Environment:
#   SCRAPS_VERSION     release tag to install, e.g. v0.1.0 (default: latest)
#   SCRAPS_INSTALL_DIR install directory (default: /usr/local/bin, falling
#                      back to ~/.local/bin when not writable)
#   SCRAPS_BASE_URL    release download base (default: GitHub; override for
#                      mirrors)

set -euo pipefail

REPO="peelar/scraps"

info() { printf '==> %s\n' "$*" >&2; }
warn() { printf 'warning: %s\n' "$*" >&2; }
err()  { printf 'error: %s\n' "$*" >&2; }

tmpdir="$(mktemp -d)"
cleanup() { rm -rf "$tmpdir"; }
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Detect platform (must match the goreleaser build matrix)
# ---------------------------------------------------------------------------
os="$(uname -s)"
arch="$(uname -m)"

case "$os" in
  Darwin) os="darwin" ;;
  Linux)  os="linux" ;;
  *) err "unsupported operating system: $os (builds exist for darwin and linux)"; exit 1 ;;
esac

case "$arch" in
  x86_64|amd64)   arch="amd64" ;;
  aarch64|arm64)  arch="arm64" ;;
  *) err "unsupported architecture: $arch (builds exist for amd64 and arm64)"; exit 1 ;;
esac

# ---------------------------------------------------------------------------
# Resolve the version to install
# ---------------------------------------------------------------------------
if [ -n "${SCRAPS_VERSION:-}" ]; then
  version="${SCRAPS_VERSION#v}"
else
  info "finding latest release"
  api_body="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null || true)"
  tag="$(printf '%s' "$api_body" | grep -oE '"tag_name":[[:space:]]*"[^"]+"' \
    | head -n1 | cut -d'"' -f4 || true)"
  if [ -z "$tag" ]; then
    err "could not determine the latest release; no published releases yet?"
    err "publish a release, or pin one with: SCRAPS_VERSION=v0.1.0 bash install.sh"
    exit 1
  fi
  version="${tag#v}"
fi

archive="scraps_${version}_${os}_${arch}.tar.gz"
base_url="${SCRAPS_BASE_URL:-https://github.com/${REPO}/releases/download}/v${version}"

# ---------------------------------------------------------------------------
# Download and verify
# ---------------------------------------------------------------------------
for cmd in curl tar; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    err "required command not found: $cmd"
    exit 1
  fi
done

info "downloading scraps v${version} (${os}/${arch})"
curl -fsSL -o "$tmpdir/$archive" "${base_url}/${archive}"
curl -fsSL -o "$tmpdir/checksums.txt" "${base_url}/checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
  verify_with() { sha256sum -c --strict -; }
elif command -v shasum >/dev/null 2>&1; then
  verify_with() { shasum -a 256 -c -; }
else
  verify_with() { warn "no sha256 tool found; skipping checksum verification"; }
fi

if [ "$(type -t verify_with)" = "function" ]; then
  sum_line="$(grep " ${archive}\$" "$tmpdir/checksums.txt" || true)"
  sum_hash="$(printf '%s' "$sum_line" | awk '{print $1}')"
  if [ -z "$sum_hash" ] || ! [[ "$sum_hash" =~ ^[0-9a-fA-F]{64}$ ]]; then
    err "checksums.txt has no valid sha256 entry for ${archive}"
    exit 1
  fi
  (cd "$tmpdir" && printf '%s\n' "$sum_line" | verify_with)
fi

tar -xzf "$tmpdir/$archive" -C "$tmpdir"

# ---------------------------------------------------------------------------
# Install
# ---------------------------------------------------------------------------
use_sudo=0
if [ -n "${SCRAPS_INSTALL_DIR:-}" ]; then
  install_dir="${SCRAPS_INSTALL_DIR}"
  mkdir -p "$install_dir"
elif [ -w /usr/local/bin ]; then
  install_dir="/usr/local/bin"
elif command -v sudo >/dev/null 2>&1; then
  install_dir="/usr/local/bin"
  use_sudo=1
else
  install_dir="${HOME}/.local/bin"
  mkdir -p "$install_dir"
fi

for bin in scrap scrapd; do
  if [ "$use_sudo" = "1" ]; then
    sudo install -m 0755 "$tmpdir/$bin" "$install_dir/$bin"
  else
    install -m 0755 "$tmpdir/$bin" "$install_dir/$bin"
  fi
done

info "installed to ${install_dir}"
case ":${PATH}:" in
  *":${install_dir}:"*) ;;
  *)
    warn "${install_dir} is not on your PATH; add it with:"
    warn '  export PATH="'"${install_dir}"':$PATH"'
    ;;
esac

"$install_dir/scrap" version || true

# ---------------------------------------------------------------------------
# Configure the client (best effort)
# ---------------------------------------------------------------------------
config="${XDG_CONFIG_HOME:-$HOME/.config}/scraps/client.json"
if [ -f "$config" ]; then
  info "client profile found — verify with: scrap status"
elif "$install_dir/scrap" attach; then
  info "worker attached — verify with: scrap status, then run pi and use /scrap"
else
  info "no worker attached (see above); configure later with:"
  info "  - tailnet worker: scrap attach"
  info "  - local worker:   scrap configure && make up   (from a source checkout)"
fi
