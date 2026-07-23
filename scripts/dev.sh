#!/usr/bin/env bash
# Start chatgpt-web-voice for WSL + Windows / VS Code Remote.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

# This helper is intentionally development-only. The production container
# gets VOICE_ENV=production from the Docker image and never generates a cert.
export VOICE_ENV=development
export VOICE_LISTEN_ADDR="${VOICE_LISTEN_ADDR:-0.0.0.0:8090}"

TLS_REQUESTED=false
case "${VOICE_TLS:-false}" in
  1|[Tt][Rr][Uu][Ee]|[Yy][Ee][Ss]|[Oo][Nn]) TLS_REQUESTED=true ;;
esac

if [[ "${1:-}" == "--tls" ]] || [[ "${TLS_REQUESTED}" == "true" ]]; then
  export VOICE_TLS=true
  SCHEME="https"
else
  export VOICE_TLS=false
  SCHEME="http"
fi

if [[ -z "${VOICE_AUTH_USERNAME:-}" ]] || [[ -z "${VOICE_AUTH_PASSWORD:-}" ]]; then
  echo "missing gateway login credentials — set both variables in .env:"
  echo "  VOICE_AUTH_USERNAME=admin"
  echo "  VOICE_AUTH_PASSWORD=<long-random-password>"
  exit 1
fi

PORT="${VOICE_LISTEN_ADDR##*:}"
WSL_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"

echo "=============================================="
echo " chatgpt-web-voice (WSL)"
echo "=============================================="
echo " listen:      ${VOICE_LISTEN_ADDR}"
echo " environment: ${VOICE_ENV}"
echo " auth user:   ${VOICE_AUTH_USERNAME}"
echo " database:    ${VOICE_DATABASE_FILE:-./data/voice.db}"
echo
echo " Open in browser (microphone needs secure context):"
echo "   1) VS Code Ports: forward ${PORT}, then"
echo "        ${SCHEME}://127.0.0.1:${PORT}/voice"
echo "      (works on Windows + WSL, mic OK)"
echo "   2) WSL terminal browser / curl:"
echo "        ${SCHEME}://127.0.0.1:${PORT}/voice"
if [[ -n "${WSL_IP}" ]]; then
  if [[ "${SCHEME}" == "https" ]]; then
    echo "   3) Windows without port-forward (LAN IP, accept self-signed cert):"
    echo "        https://${WSL_IP}:${PORT}/voice   # mic OK"
  else
    echo "   3) Windows without port-forward (LAN IP, mic needs TLS):"
    echo "        http://${WSL_IP}:${PORT}/voice   # page only"
    echo "        ./scripts/dev.sh --tls then https://${WSL_IP}:${PORT}/voice  # mic OK"
  fi
fi
echo "=============================================="
echo

if [[ "${SCHEME}" == "https" ]]; then
  echo "TLS enabled for development (self-signed cert in data/certs/)"
fi

exec go run ./cmd/server
