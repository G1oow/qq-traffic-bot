#!/usr/bin/env bash

set -Eeuo pipefail

APP_NAME="qq-traffic-bot"
APP_DIR="/opt/${APP_NAME}"
SERVICE_FILE="/etc/systemd/system/${APP_NAME}.service"
GO_VERSION="1.25.0"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
WORK_DIR=""

log() {
  printf '[%s] %s\n' "${APP_NAME}" "$*"
}

die() {
  printf '[%s] ERROR: %s\n' "${APP_NAME}" "$*" >&2
  exit 1
}

cleanup() {
  if [[ -n "${WORK_DIR}" && -d "${WORK_DIR}" && "${WORK_DIR}" == /tmp/* ]]; then
    rm -rf -- "${WORK_DIR}"
  fi
}
trap cleanup EXIT

require_root() {
  [[ "${EUID}" -eq 0 ]] || die "请使用 sudo bash deploy/install.sh 运行此脚本"
  [[ -d /run/systemd/system ]] || die "当前系统未运行 systemd"
  [[ -f "${SOURCE_DIR}/go.mod" ]] || die "请从仓库根目录中的 deploy/install.sh 运行"
}

install_packages() {
  local packages=(ca-certificates curl nftables tar)

  if command -v apt-get >/dev/null 2>&1; then
    log "安装基础依赖"
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y "${packages[@]}"
  elif command -v dnf >/dev/null 2>&1; then
    log "安装基础依赖"
    dnf install -y "${packages[@]}"
  elif command -v yum >/dev/null 2>&1; then
    log "安装基础依赖"
    yum install -y "${packages[@]}"
  else
    die "不支持当前包管理器，请先安装 curl、tar、ca-certificates 和 nftables"
  fi
}

version_at_least() {
  local current="$1"
  local required="$2"
  [[ "$(printf '%s\n%s\n' "${required}" "${current}" | sort -V | head -n 1)" == "${required}" ]]
}

select_go() {
  local current=""
  local arch=""
  local archive=""
  local checksum=""

  if command -v go >/dev/null 2>&1; then
    current="$(go env GOVERSION | sed 's/^go//')"
    if version_at_least "${current}" "${GO_VERSION}"; then
      log "使用系统 Go ${current}"
      return
    fi
  fi

  case "$(uname -m)" in
    x86_64|amd64)
      arch="amd64"
      checksum="2852af0cb20a13139b3448992e69b868e50ed0f8a1e5940ee1de9e19a123b613"
      ;;
    aarch64|arm64)
      arch="arm64"
      checksum="05de75d6994a2783699815ee553bd5a9327d8b79991de36e38b66862782f54ae"
      ;;
    *) die "仅支持 amd64 和 arm64 架构" ;;
  esac

  WORK_DIR="$(mktemp -d /tmp/${APP_NAME}.XXXXXX)"
  archive="go${GO_VERSION}.linux-${arch}.tar.gz"
  log "临时下载 Go ${GO_VERSION} (${arch})"
  curl --fail --location --proto '=https' --tlsv1.2 \
    --output "${WORK_DIR}/${archive}" "https://go.dev/dl/${archive}"
  printf '%s  %s\n' "${checksum}" "${WORK_DIR}/${archive}" | sha256sum --check --status - \
    || die "Go 下载文件校验失败"
  tar -C "${WORK_DIR}" -xzf "${WORK_DIR}/${archive}"
  export PATH="${WORK_DIR}/go/bin:${PATH}"
}

check_nftables() {
  systemctl cat nft-perip.service >/dev/null 2>&1 \
    || die "未找到 nft-perip.service，请先安装逐 IP nftables 规则服务"
  systemctl start nft-perip.service
  nft -j list set ip perip4 hitv4 >/dev/null \
    || die "无法读取 nftables set: ip perip4 hitv4"
  nft -j list set ip6 perip6 hitv6 >/dev/null \
    || die "无法读取 nftables set: ip6 perip6 hitv6"
}

write_env() {
  local env_file="${APP_DIR}/.env"
  local source_env="${SOURCE_DIR}/.env"

  if [[ -f "${env_file}" && -z "${APPID:-}" && -z "${SECRET:-}" ]]; then
    chmod 600 "${env_file}"
    validate_env "${env_file}"
    log "复用现有 ${env_file}"
    return
  fi

  if [[ -f "${source_env}" && -z "${APPID:-}" && -z "${SECRET:-}" ]]; then
    install -m 600 "${source_env}" "${env_file}"
    validate_env "${env_file}"
    log "使用仓库根目录中的 .env"
    return
  fi

  if [[ -z "${APPID:-}" ]]; then
    read -r -p 'QQ APPID: ' APPID
  fi
  if [[ -z "${SECRET:-}" ]]; then
    read -r -s -p 'QQ SECRET: ' SECRET
    printf '\n'
  fi
  [[ -n "${APPID}" ]] || die "APPID 不能为空"
  [[ -n "${SECRET}" ]] || die "SECRET 不能为空"
  [[ "${APPID}" != *$'\n'* && "${SECRET}" != *$'\n'* ]] || die "凭证不能包含换行符"

  umask 077
  printf 'APPID=%s\nSECRET=%s\n' "${APPID}" "${SECRET}" > "${env_file}"
  chmod 600 "${env_file}"
  validate_env "${env_file}"
}

validate_env() {
  local env_file="$1"
  grep -Eq '^APPID=.+$' "${env_file}" || die "${env_file} 中缺少 APPID"
  grep -Eq '^SECRET=.+$' "${env_file}" || die "${env_file} 中缺少 SECRET"
}

build_and_install() {
  local output=""

  [[ -n "${WORK_DIR}" ]] || WORK_DIR="$(mktemp -d /tmp/${APP_NAME}.XXXXXX)"
  output="${WORK_DIR}/${APP_NAME}"
  log "下载 Go 模块并构建"
  (
    cd "${SOURCE_DIR}"
    go mod download
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "${output}" ./cmd/qq-traffic-bot
  )

  install -d -m 750 "${APP_DIR}" "${APP_DIR}/data"
  install -m 755 "${output}" "${APP_DIR}/${APP_NAME}"
  install -m 644 "${SOURCE_DIR}/deploy/${APP_NAME}.service" "${SERVICE_FILE}"
  write_env
}

start_service() {
  log "检查程序并启动 systemd 服务"
  (
    cd "${APP_DIR}"
    "./${APP_NAME}" -check
  )
  systemctl daemon-reload
  systemctl enable "${APP_NAME}.service"
  systemctl restart "${APP_NAME}.service"
  systemctl --no-pager --full status "${APP_NAME}.service" || true
}

main() {
  require_root
  install_packages
  select_go
  check_nftables
  build_and_install
  start_service
  log "部署完成。查看日志：journalctl -u ${APP_NAME} -f"
}

main "$@"
