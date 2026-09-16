#!/bin/bash
# Install surviva + CRIU + the systemd unit, and write a basic
# /etc/surviva/surviva.conf, on Ubuntu/Debian, RHEL-family (RHEL/CentOS/
# Rocky/Alma/Fedora), or Amazon Linux (2 or 2023). See DEPLOYMENT.md for
# what this automates and for options (MySQL backend, Lambda/Step
# Functions notify) it deliberately leaves out of the generated config.
#
# Usage: run as root from inside a checked-out surviva repo:
#   sudo ./deploy/install.sh
#
# Idempotent: safe to re-run. Skips the CRIU build if already installed,
# never overwrites an existing surviva.conf.
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "install.sh: must run as root (sudo ./deploy/install.sh)" >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CRIU_VERSION="v4.2"
GO_VERSION="1.26.1"

CLOUD_PROVIDER="${CLOUD_PROVIDER:-aws}"
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-5}"
CONF_DIR=/etc/surviva
DATA_DIR=/var/lib/surviva
LOG_DIR=/var/log/surviva
CONF_PATH="$CONF_DIR/surviva.conf"

echo "==> detecting OS"
. /etc/os-release
case "${ID:-} ${ID_LIKE:-}" in
  *ubuntu*|*debian*) PKG_FAMILY=debian ;;
  *amzn*)            PKG_FAMILY=amzn ;;
  *rhel*|*fedora*|*centos*) PKG_FAMILY=rhel ;;
  *)
    echo "install.sh: unsupported OS (ID=${ID:-unknown}); expected ubuntu/debian, an RHEL-family distro, or Amazon Linux" >&2
    exit 1
    ;;
esac
echo "    family: $PKG_FAMILY (ID=${ID:-unknown})"

echo "==> installing CRIU build dependencies"
case "$PKG_FAMILY" in
  debian)
    apt-get update
    apt-get install -y build-essential pkg-config \
      libprotobuf-dev libprotobuf-c-dev protobuf-c-compiler protobuf-compiler \
      python3-protobuf libcap-dev libnl-3-dev libnet-dev libbsd-dev iproute2 uuid-dev git curl
    ;;
  amzn|rhel)
    PM=yum; command -v dnf >/dev/null 2>&1 && PM=dnf
    # ponytail: EPEL enablement is best-effort across AL2/AL2023/RHEL/Rocky/Alma --
    # package names and repo setup differ enough that a single command can't cover
    # all of them reliably. Failures here are non-fatal; if a package below turns
    # out to need EPEL and isn't found, enable it for your distro and re-run.
    "$PM" install -y epel-release >/dev/null 2>&1 || true
    command -v amazon-linux-extras >/dev/null 2>&1 && amazon-linux-extras install -y epel >/dev/null 2>&1 || true
    "$PM" groupinstall -y "Development Tools" || "$PM" install -y gcc make
    "$PM" install -y pkgconfig protobuf-devel protobuf-c-devel protobuf-compiler \
      python3-protobuf libcap-devel libnl3-devel libnet-devel libbsd-devel \
      iproute libuuid-devel git curl
    ;;
esac

echo "==> installing CRIU $CRIU_VERSION"
if command -v criu >/dev/null 2>&1 && criu --version 2>/dev/null | grep -q "${CRIU_VERSION#v}"; then
  echo "    already installed, skipping build"
else
  WORKDIR="$(mktemp -d)"
  trap 'rm -rf "$WORKDIR"' EXIT
  git clone --depth 1 --branch "$CRIU_VERSION" https://github.com/checkpoint-restore/criu.git "$WORKDIR/criu"
  make -C "$WORKDIR/criu" -j"$(nproc)"
  make -C "$WORKDIR/criu" install-criu
  rm -rf "$WORKDIR"
  trap - EXIT
fi
criu --version
criu check || echo "    warning: 'criu check' reported issues -- may be fine under a restrictive kernel config, see LIMITATIONS.md"

echo "==> checking Go toolchain (need >= $GO_VERSION)"
NEED_GO=1
if command -v go >/dev/null 2>&1; then
  CUR_GO="$(go version | awk '{print $3}' | sed 's/^go//')"
  [ "$(printf '%s\n%s\n' "$GO_VERSION" "$CUR_GO" | sort -V | head -1)" = "$GO_VERSION" ] && NEED_GO=0
fi
if [ "$NEED_GO" = "1" ]; then
  ARCH="$(uname -m)"
  case "$ARCH" in
    x86_64)  GOARCH=amd64 ;;
    aarch64) GOARCH=arm64 ;;
    *) echo "install.sh: unsupported architecture $ARCH" >&2; exit 1 ;;
  esac
  echo "    installing go$GO_VERSION ($GOARCH) to /usr/local/go"
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${GOARCH}.tar.gz" -o /tmp/go.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tar.gz
  rm -f /tmp/go.tar.gz
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
fi
export PATH="/usr/local/go/bin:$PATH"
go version

echo "==> building and installing surviva"
BUILD_OUT="$(mktemp)"
( cd "$REPO_ROOT" && go build -o "$BUILD_OUT" ./cmd/surviva )
install -m 0755 "$BUILD_OUT" /usr/local/bin/surviva
rm -f "$BUILD_OUT"
surviva -h >/dev/null

echo "==> creating directories"
mkdir -p "$CONF_DIR" "$DATA_DIR/checkpoints" "$LOG_DIR"
chmod 700 "$CONF_DIR" # surviva.conf can hold a MySQL password in plaintext

if [ -f "$CONF_PATH" ]; then
  echo "==> $CONF_PATH already exists, leaving it alone"
else
  echo "==> writing $CONF_PATH"
  cat > "$CONF_PATH" <<EOF
CloudProvider=$CLOUD_PROVIDER
PollIntervalSeconds=$POLL_INTERVAL_SECONDS
AuditLogPath=$LOG_DIR/audit.log
CheckpointBaseDir=$DATA_DIR/checkpoints
DBPath=$DATA_DIR/jobs.db
EOF
  chmod 600 "$CONF_PATH"
fi

echo "==> installing systemd unit"
cp "$REPO_ROOT/deploy/systemd/surviva-daemon.service" /etc/systemd/system/surviva-daemon.service
systemctl daemon-reload
systemctl enable --now surviva-daemon

sleep 1
systemctl --no-pager --full status surviva-daemon || true

cat <<EOF

surviva is installed and running.
  Config:  $CONF_PATH  (MySQL backend / notify target: see DEPLOYMENT.md)
  Binary:  /usr/local/bin/surviva
  Logs:    journalctl -u surviva-daemon -f

Try it:
  surviva run -- sleep 300 &
  surviva list
  surviva pause <job-id>
  surviva show -history <job-id>
EOF
