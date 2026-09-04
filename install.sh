#!/usr/bin/env bash
set -Eeuo pipefail

APP_NAME="nswl-ui"
SERVICE_USER="nswl-ui"
INSTALL_PATH="/usr/local/bin/nswl-ui"
CONFIG_DIR="/etc/nswl-ui"
CONFIG_PATH="${CONFIG_DIR}/config.json"
SERVICE_PATH="/etc/systemd/system/nswl-ui.service"
REPO_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

say() { printf '\n==> %s\n' "$*"; }
fail() { printf 'Hata: %s\n' "$*" >&2; exit 1; }

if [[ ${EUID} -ne 0 ]]; then
  fail "Root yetkisi gerekli. sudo ./install.sh /nswl/log/klasoru komutunu kullanın."
fi

for command_name in go systemctl install useradd nice; do
  command -v "${command_name}" >/dev/null 2>&1 || fail "${command_name} komutu bulunamadı."
done

LOG_PATH="${1:-}"
PORT="${2:-8080}"

if [[ ! -f "${CONFIG_PATH}" && -z "${LOG_PATH}" ]]; then
  fail "İlk kurulumda log klasörünü verin: sudo ./install.sh /nswl/log/klasoru"
fi

if [[ -n "${LOG_PATH}" ]]; then
  [[ -d "${LOG_PATH}" ]] || fail "Log klasörü bulunamadı: ${LOG_PATH}"
  [[ "${PORT}" =~ ^[0-9]+$ ]] || fail "Port sayı olmalıdır: ${PORT}"
  (( PORT >= 1 && PORT <= 65535 )) || fail "Port 1-65535 arasında olmalıdır."
  [[ "${LOG_PATH}" != *\"* && "${LOG_PATH}" != *$'\n'* ]] || fail "Log yolu geçersiz karakter içeriyor."
fi

BUILD_FILE="$(mktemp "${TMPDIR:-/tmp}/nswl-ui-build.XXXXXX")"
CONFIG_TMP=""
cleanup() {
  rm -f -- "${BUILD_FILE}"
  [[ -z "${CONFIG_TMP}" ]] || rm -f -- "${CONFIG_TMP}"
}
trap cleanup EXIT

say "Testler çalıştırılıyor"
cd "${REPO_DIR}"
GOMAXPROCS=2 nice -n 10 go test -p 1 ./...

say "Linux binary derleniyor"
GOMAXPROCS=2 nice -n 10 go build -p 1 -trimpath -ldflags='-s -w' -o "${BUILD_FILE}" ./cmd/nswl-ui

say "Servis kullanıcısı hazırlanıyor"
if ! id -u "${SERVICE_USER}" >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /sbin/nologin "${SERVICE_USER}"
fi

say "Uygulama kuruluyor"
install -m 0755 "${BUILD_FILE}" "${INSTALL_PATH}.new"
mv -f -- "${INSTALL_PATH}.new" "${INSTALL_PATH}"

install -d -m 0755 "${CONFIG_DIR}"
if [[ -n "${LOG_PATH}" ]]; then
  CONFIG_TMP="$(mktemp "${TMPDIR:-/tmp}/nswl-ui-config.XXXXXX")"
  printf '{\n  "bind": "0.0.0.0",\n  "port": %s,\n  "log_path": "%s",\n  "database_path": "/var/lib/nswl-ui/nswl.db",\n  "index_interval_seconds": 2,\n  "log_timezone": "Europe/Istanbul"\n}\n' "${PORT}" "${LOG_PATH}" >"${CONFIG_TMP}"
  install -m 0644 "${CONFIG_TMP}" "${CONFIG_PATH}"
  say "Config oluşturuldu: ${CONFIG_PATH}"
else
  say "Mevcut config korunuyor: ${CONFIG_PATH}"
fi

if ! runuser -u "${SERVICE_USER}" -- test -r "${CONFIG_PATH}"; then
  fail "Servis kullanıcısı config dosyasını okuyamıyor: ${CONFIG_PATH}"
fi
if ! runuser -u "${SERVICE_USER}" -- test -r "${LOG_PATH:-$(sed -n 's/.*"log_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${CONFIG_PATH}")}"; then
  printf 'Uyarı: %s kullanıcısı log klasörünü okuyamıyor. Dizin izinlerini kontrol edin.\n' "${SERVICE_USER}" >&2
fi

say "systemd servisi kuruluyor"
install -m 0644 "${REPO_DIR}/deploy/nswl-ui.service" "${SERVICE_PATH}"
systemctl daemon-reload
systemctl enable "${APP_NAME}.service" >/dev/null
systemctl restart "${APP_NAME}.service"

if ! systemctl is-active --quiet "${APP_NAME}.service"; then
  systemctl status "${APP_NAME}.service" --no-pager || true
  fail "Servis başlatılamadı. Yukarıdaki systemd çıktısını kontrol edin."
fi

say "Kurulum tamamlandı"
systemctl status "${APP_NAME}.service" --no-pager --lines=5
printf '\nArayüz: http://SUNUCU_IP:%s\n' "$(sed -n 's/.*"port"[[:space:]]*:[[:space:]]*\([0-9]*\).*/\1/p' "${CONFIG_PATH}")"
printf 'Loglar: journalctl -u %s -f\n' "${APP_NAME}"
