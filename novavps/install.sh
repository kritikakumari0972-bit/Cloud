#!/usr/bin/env bash
set -Eeuo pipefail

# NovaVPS installer. Run from an extracted NovaVPS source directory:
#   sudo bash ./install.sh [--public --domain panel.example.com]

APP_NAME="NovaVPS"
INSTALL_DIR="${NOVAVPS_INSTALL_DIR:-/opt/novavps}"
SOURCE_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PUBLIC_MODE=0
DOMAIN=""

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'
CYAN='\033[0;36m'; PURPLE='\033[0;35m'; WHITE='\033[0;37m'; NC='\033[0m'

info() { printf "${CYAN}[INFO]${NC} %s\n" "$*"; }
ok() { printf "${GREEN}[ OK ]${NC} %s\n" "$*"; }
warn() { printf "${YELLOW}[WARN]${NC} %s\n" "$*"; }
fail() { printf "${RED}[FAIL]${NC} %s\n" "$*" >&2; exit 1; }

usage() {
  cat <<'USAGE'
NovaVPS installer

Usage:
  sudo bash ./install.sh [options]

Options:
  --public           Enable Caddy HTTPS reverse proxy (requires a domain)
  --domain DOMAIN    Domain pointing to this server; implies --public
  --local            Keep the panel bound to 127.0.0.1:8080 (default)
  --dir PATH         Installation directory (default: /opt/novavps)
  -h, --help         Show this help

The installer expects the NovaVPS source tree beside install.sh.
USAGE
}

while (($#)); do
  case "$1" in
    --public) PUBLIC_MODE=1; shift ;;
    --domain)
      (($# >= 2)) || fail "--domain requires a hostname"
      DOMAIN="$2"; PUBLIC_MODE=1; shift 2 ;;
    --local) PUBLIC_MODE=0; DOMAIN=""; shift ;;
    --dir)
      (($# >= 2)) || fail "--dir requires a path"
      INSTALL_DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "Unknown option: $1 (use --help)" ;;
  esac
done

[[ "$EUID" -eq 0 ]] || fail "Run as root: sudo bash ./install.sh"
[[ -f "$SOURCE_DIR/docker-compose.yml" && -f "$SOURCE_DIR/Dockerfile" ]] || fail "NovaVPS source files were not found beside install.sh. Extract the complete release ZIP first."

if [[ "$PUBLIC_MODE" -eq 1 ]]; then
  if [[ -z "$DOMAIN" ]]; then
    [[ -t 0 ]] || fail "--public requires --domain when running non-interactively."
    read -r -p "Domain pointing to this server (e.g. panel.example.com): " DOMAIN
  fi
  [[ "$DOMAIN" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]] || fail "Invalid hostname. Enter a plain DNS name without scheme, path, or port."
fi

install_docker() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    ok "Docker Engine and Compose plugin are already installed"
    return
  fi

  . /etc/os-release
  case "${ID:-}" in
    ubuntu|debian) ;;
    *) fail "Automatic Docker installation supports Ubuntu and Debian. Install Docker Engine + Compose manually on ${PRETTY_NAME:-this OS}, then rerun." ;;
  esac
  [[ -n "${VERSION_CODENAME:-}" ]] || fail "Could not detect the OS release codename."
  command -v apt-get >/dev/null 2>&1 || fail "apt-get is required for automatic Docker installation."

  info "Installing Docker Engine and the official Compose plugin"
  apt-get update
  apt-get install -y ca-certificates curl
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL "https://download.docker.com/linux/${ID}/gpg" -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/%s %s stable\n' \
    "$(dpkg --print-architecture)" "$ID" "$VERSION_CODENAME" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
  docker compose version >/dev/null 2>&1 || fail "Docker Compose plugin installation did not complete."
  ok "Docker Engine installed and enabled"
}

install_docker

info "Preparing ${APP_NAME} in ${INSTALL_DIR}"
install -d -m 0750 "$INSTALL_DIR"
# Copy source without replacing the machine's .env, user data, Git metadata or backups.
tar -C "$SOURCE_DIR" \
  --exclude='./.git' --exclude='./.env' --exclude='./data' --exclude='./backups' \
  --exclude='./node_modules' --exclude='./web/node_modules' --exclude='./web/dist' \
  -cf - . | tar -C "$INSTALL_DIR" -xf -

[[ -f "$INSTALL_DIR/.env" ]] || install -m 0600 "$INSTALL_DIR/.env.example" "$INSTALL_DIR/.env"
chmod 0600 "$INSTALL_DIR/.env"

if [[ "$PUBLIC_MODE" -eq 1 ]]; then
  if grep -q '^NOVAVPS_DOMAIN=' "$INSTALL_DIR/.env"; then
    sed -i "s|^NOVAVPS_DOMAIN=.*|NOVAVPS_DOMAIN=${DOMAIN}|" "$INSTALL_DIR/.env"
  else
    printf '\nNOVAVPS_DOMAIN=%s\n' "$DOMAIN" >> "$INSTALL_DIR/.env"
  fi
  if grep -q '^NOVAVPS_COOKIE_SECURE=' "$INSTALL_DIR/.env"; then
    sed -i 's/^NOVAVPS_COOKIE_SECURE=.*/NOVAVPS_COOKIE_SECURE=true/' "$INSTALL_DIR/.env"
  else
    printf 'NOVAVPS_COOKIE_SECURE=true\n' >> "$INSTALL_DIR/.env"
  fi
  touch "$INSTALL_DIR/.public-mode"
else
  rm -f "$INSTALL_DIR/.public-mode"
  if grep -q '^NOVAVPS_COOKIE_SECURE=' "$INSTALL_DIR/.env"; then
    sed -i 's/^NOVAVPS_COOKIE_SECURE=.*/NOVAVPS_COOKIE_SECURE=false/' "$INSTALL_DIR/.env"
  else
    printf 'NOVAVPS_COOKIE_SECURE=false\n' >> "$INSTALL_DIR/.env"
  fi
fi

install -d -m 0750 "$INSTALL_DIR/backups"
chmod +x "$INSTALL_DIR/scripts/novavps-menu.sh"

# Install a small command wrapper; it does not expose server credentials.
cat > /usr/local/bin/novavps <<WRAPPER
#!/usr/bin/env bash
export NOVAVPS_HOME=$(printf '%q' "$INSTALL_DIR")
exec $(printf '%q' "$INSTALL_DIR/scripts/novavps-menu.sh") "\$@"
WRAPPER
chmod 0755 /usr/local/bin/novavps

cd "$INSTALL_DIR"
COMPOSE_ARGS=(--project-directory "$INSTALL_DIR")
if [[ -f "$INSTALL_DIR/.public-mode" ]]; then
  COMPOSE_ARGS+=(--profile public)
fi
info "Building and starting NovaVPS"
docker compose "${COMPOSE_ARGS[@]}" up -d --build

ok "NovaVPS has been deployed"
echo
if [[ -f "$INSTALL_DIR/.public-mode" ]]; then
  printf "${GREEN}Panel URL:${NC} https://%s\n" "$DOMAIN"
  printf "${YELLOW}Note:${NC} DNS must point to this host and inbound ports 80/443 must be reachable for Caddy HTTPS.\n"
else
  printf "${GREEN}Panel URL:${NC} http://127.0.0.1:8080\n"
  printf "${YELLOW}Remote access:${NC} create an SSH tunnel: ssh -L 8080:127.0.0.1:8080 USER@SERVER\n"
fi
printf "${GREEN}Management command:${NC} novavps\n"
printf "${GREEN}First-run account setup:${NC} open the panel and create your own username and password.\n"
if [[ -f "$INSTALL_DIR/.public-mode" ]]; then
  printf "${YELLOW}Setup access:${NC} create an SSH tunnel to localhost before creating the first account.\n"
else
  printf "${YELLOW}Setup URL:${NC} http://127.0.0.1:8080\n"
fi
printf "\n${CYAN}Run 'novavps' to open the interactive manager.${NC}\n"
