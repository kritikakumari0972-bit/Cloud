#!/usr/bin/env bash
set -Eeuo pipefail

APP_DIR="${NOVAVPS_HOME:-/opt/novavps}"
PUBLIC_FLAG="$APP_DIR/.public-mode"
BACKUP_DIR="$APP_DIR/backups"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'
BLUE='\033[0;34m'; PURPLE='\033[0;35m'; CYAN='\033[0;36m'
WHITE='\033[0;37m'; GRAY='\033[1;30m'; NC='\033[0m'

info() { printf "${CYAN}[INFO]${NC} %s\n" "$*"; }
ok() { printf "${GREEN}[ OK ]${NC} %s\n" "$*"; }
warn() { printf "${YELLOW}[WARN]${NC} %s\n" "$*"; }
fail() { printf "${RED}[FAIL]${NC} %s\n" "$*" >&2; return 1; }

command -v docker >/dev/null 2>&1 || { fail "Docker is not installed. Run the NovaVPS installer first."; exit 1; }
[[ -f "$APP_DIR/docker-compose.yml" ]] || { fail "NovaVPS project not found at $APP_DIR."; exit 1; }

DOCKER=(docker)
if ! docker info >/dev/null 2>&1; then
  if command -v sudo >/dev/null 2>&1; then DOCKER=(sudo docker); else fail "Cannot access Docker daemon. Use root or configure Docker permissions."; exit 1; fi
fi

compose() {
  local args=(compose --project-directory "$APP_DIR" -f "$APP_DIR/docker-compose.yml")
  [[ -f "$PUBLIC_FLAG" ]] && args+=(--profile public)
  "${DOCKER[@]}" "${args[@]}" "$@"
}

pause() { echo; read -r -n 1 -s -p "Press any key to return..." || true; echo; }

status_cmd() {
  echo -e "${CYAN}NovaVPS services${NC}"
  compose ps || true
  echo
  if curl -fsS --max-time 3 http://127.0.0.1:8080/api/health >/dev/null 2>&1; then
    ok "Panel API is responding on 127.0.0.1:8080"
  else
    warn "Panel API is not responding on 127.0.0.1:8080"
  fi
  if [[ -f "$PUBLIC_FLAG" ]]; then
    local domain
    domain=$(sed -n 's/^NOVAVPS_DOMAIN=//p' "$APP_DIR/.env" | tail -n1)
    info "Public HTTPS profile enabled for ${domain:-configured domain}"
  else
    info "Private mode: loopback-only panel binding"
  fi
}

start_cmd() { compose up -d --build; ok "NovaVPS started."; }
stop_cmd() { compose stop; ok "NovaVPS stopped. Data volumes were preserved."; }
restart_cmd() { compose restart; ok "NovaVPS restarted."; }
logs_cmd() { compose logs --tail=100 -f; }

url_cmd() {
  if [[ -f "$PUBLIC_FLAG" ]]; then
    local domain
    domain=$(sed -n 's/^NOVAVPS_DOMAIN=//p' "$APP_DIR/.env" | tail -n1)
    echo "https://${domain:-YOUR_DOMAIN}"
    echo "Ensure DNS points to this host and ports 80/443 are reachable."
  else
    echo "http://127.0.0.1:8080"
    echo "For remote access run: ssh -L 8080:127.0.0.1:8080 USER@SERVER"
  fi
}

backup_cmd() {
  local container_id volume_name stamp
  container_id=$(compose ps -q novavps | head -n1)
  [[ -n "$container_id" ]] || { fail "NovaVPS container is not created. Start it first."; return 1; }
  volume_name=$("${DOCKER[@]}" inspect "$container_id" --format '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Name}}{{end}}{{end}}')
  [[ -n "$volume_name" ]] || { fail "Could not identify the persistent NovaVPS data volume."; return 1; }
  mkdir -p "$BACKUP_DIR"
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  "${DOCKER[@]}" run --rm \
    -v "${volume_name}:/data:ro" \
    -v "${BACKUP_DIR}:/backup" \
    alpine:3.22 sh -c "tar -czf '/backup/novavps-data-${stamp}.tar.gz' -C /data ."
  ok "Backup created: $BACKUP_DIR/novavps-data-${stamp}.tar.gz"
}

update_cmd() {
  if [[ ! -d "$APP_DIR/.git" ]]; then
    warn "This installation was deployed from a source archive, not a Git checkout. Download the newer release ZIP and rerun its install.sh to update."
    return 0
  fi
  git -C "$APP_DIR" pull --ff-only
  compose up -d --build
  ok "NovaVPS updated from the current Git branch."
}

uninstall_cmd() {
  echo -e "${YELLOW}This removes the NovaVPS containers but preserves the persistent data volume.${NC}"
  read -r -p "Type UNINSTALL to continue: " answer
  [[ "$answer" == "UNINSTALL" ]] || { info "Cancelled."; return 0; }
  compose down
  if [[ "${EUID}" -eq 0 ]]; then rm -f /usr/local/bin/novavps; fi
  warn "Containers removed. Data volume preserved. Project files remain in $APP_DIR."
}

purge_cmd() {
  echo -e "${RED}DANGER: this permanently deletes NovaVPS database files, encryption key, saved SSH credentials and Caddy data.${NC}"
  read -r -p "Type DELETE NOVAVPS DATA to continue: " answer
  [[ "$answer" == "DELETE NOVAVPS DATA" ]] || { info "Cancelled."; return 0; }
  compose down -v
  if [[ "${EUID}" -eq 0 ]]; then rm -f /usr/local/bin/novavps; fi
  rm -rf -- "$APP_DIR"
  warn "NovaVPS project and Docker volumes have been removed."
}

doctor_cmd() {
  echo -e "${CYAN}NovaVPS diagnostics${NC}"
  echo "Project: $APP_DIR"
  echo "OS: $(. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME" || uname -s)"
  echo "Kernel: $(uname -r)"
  echo "Architecture: $(uname -m)"
  echo "Uptime: $(uptime -p 2>/dev/null || uptime)"
  if command -v free >/dev/null 2>&1; then free -h; fi
  docker --version || true
  docker compose version || true
  compose ps || true
  if curl -fsS --max-time 3 http://127.0.0.1:8080/api/health; then echo; else warn "Local API health check failed."; fi
}

help_cmd() {
  cat <<'HELP'
NovaVPS command
  novavps                 Open interactive manager
  novavps start           Build (if needed) and start services
  novavps stop            Stop services; keep data
  novavps restart         Restart services
  novavps status          Show service and API status
  novavps logs            Follow service logs
  novavps url             Show panel URL / SSH tunnel hint
  novavps backup          Back up persistent application data
  novavps update          Update a Git checkout, if present
  novavps doctor          Print diagnostics
  novavps uninstall       Remove containers; preserve data
  novavps purge           Destructively delete containers and volumes
  novavps help            Show this help
HELP
}

header() {
  clear 2>/dev/null || true
  local version="unknown" ram="unavailable" load="unavailable" docker_status="OFFLINE"
  [[ -f "$APP_DIR/VERSION" ]] && version=$(cat "$APP_DIR/VERSION")
  if command -v free >/dev/null 2>&1; then ram=$(free -h | awk '/Mem:/ {print $3 "/" $2}'); fi
  if command -v uptime >/dev/null 2>&1; then load=$(uptime | awk -F'load average:' '{print $2}' | xargs); fi
  "${DOCKER[@]}" info >/dev/null 2>&1 && docker_status="ONLINE" || true
  echo -e "${CYAN}╔══════════════════════════════════════════════════════════════╗${NC}"
  echo -e "${CYAN}║${NC}  ${PURPLE}⚡ NOVAVPS CONTROL CENTER ⚡${NC}  ${GRAY}${version}${NC}                  ${CYAN}║${NC}"
  echo -e "${CYAN}╠══════════════════════════════════════════════════════════════╣${NC}"
  printf "${CYAN}║${NC}  ${GREEN}Docker:${NC} %-10s ${GREEN}RAM:${NC} %-17s ${GREEN}Load:${NC} %-14.14s ${CYAN}║${NC}\n" "$docker_status" "$ram" "$load"
  echo -e "${CYAN}╚══════════════════════════════════════════════════════════════╝${NC}"
}

menu() {
  while true; do
    header
    echo -e "${BLUE}  [ SERVICE CONTROL ]${NC}"
    echo -e "  ${GREEN}1)${NC} Start / Build       ${GRAY}:: Build image and start panel${NC}"
    echo -e "  ${GREEN}2)${NC} Stop                ${GRAY}:: Stop containers, keep data${NC}"
    echo -e "  ${GREEN}3)${NC} Restart             ${GRAY}:: Restart panel services${NC}"
    echo -e "  ${GREEN}4)${NC} Status              ${GRAY}:: Container and API health${NC}"
    echo -e "  ${GREEN}5)${NC} Logs                ${GRAY}:: Follow application logs${NC}"
    echo -e "  ${GREEN}6)${NC} Panel URL           ${GRAY}:: Show access address${NC}"
    echo ""
    echo -e "${YELLOW}  [ MAINTENANCE ]${NC}"
    echo -e "  ${GREEN}7)${NC} Backup data         ${GRAY}:: Save encrypted app data${NC}"
    echo -e "  ${GREEN}8)${NC} Update              ${GRAY}:: Update Git checkout${NC}"
    echo -e "  ${GREEN}9)${NC} Diagnostics         ${GRAY}:: System and Docker checks${NC}"
    echo -e "  ${RED}10)${NC} Uninstall           ${GRAY}:: Remove containers, keep data${NC}"
    echo -e "  ${RED}11)${NC} Purge data          ${GRAY}:: Delete containers and volumes${NC}"
    echo -e "${CYAN}──────────────────────────────────────────────────────────────${NC}"
    echo -e "  ${RED}0) Exit${NC}"
    echo
    read -r -p "${GREEN}➜ Select option [0-11]: ${NC}" choice
    case "$choice" in
      1) start_cmd; pause ;;
      2) stop_cmd; pause ;;
      3) restart_cmd; pause ;;
      4) status_cmd; pause ;;
      5) logs_cmd; pause ;;
      6) url_cmd; pause ;;
      7) backup_cmd; pause ;;
      8) update_cmd; pause ;;
      9) doctor_cmd; pause ;;
      10) uninstall_cmd; pause ;;
      11) purge_cmd; pause ;;
      0) echo -e "${GREEN}NovaVPS manager closed.${NC}"; exit 0 ;;
      *) warn "Invalid option."; sleep 1 ;;
    esac
  done
}

case "${1:-menu}" in
  menu) menu ;;
  start) start_cmd ;;
  stop) stop_cmd ;;
  restart) restart_cmd ;;
  status) status_cmd ;;
  logs) logs_cmd ;;
  url) url_cmd ;;
  backup) backup_cmd ;;
  update) update_cmd ;;
  doctor) doctor_cmd ;;
  uninstall) uninstall_cmd ;;
  purge) purge_cmd ;;
  help|-h|--help) help_cmd ;;
  *) fail "Unknown command: $1"; help_cmd; exit 2 ;;
esac
