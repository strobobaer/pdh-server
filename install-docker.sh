#!/usr/bin/env bash
set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
env_file="$repo_dir/.env.docker"

if [[ "$(uname -s)" != "Linux" ]]; then
    echo "This installer supports Ubuntu Linux only." >&2
    exit 1
fi

if [[ ! -f "$repo_dir/compose.yaml" || ! -f "$repo_dir/Dockerfile" ]]; then
    echo "Run this script from a PDH Server checkout." >&2
    exit 1
fi

if [[ ! -r /etc/os-release ]]; then
    echo "Cannot determine Linux distribution." >&2
    exit 1
fi

# shellcheck disable=SC1091
source /etc/os-release
if [[ "${ID:-}" != "ubuntu" ]]; then
    echo "Ubuntu is required; detected ${PRETTY_NAME:-unknown}." >&2
    exit 1
fi

if (( EUID != 0 )) && ! command -v sudo >/dev/null 2>&1; then
    echo "Install sudo or run this script as root." >&2
    exit 1
fi

as_root() {
    if (( EUID == 0 )); then
        "$@"
    else
        sudo "$@"
    fi
}

as_docker() {
    if (( EUID == 0 )); then
        docker "$@"
    else
        sudo docker "$@"
    fi
}

if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
    as_root apt-get update
    as_root apt-get install -y docker.io docker-compose-v2 openssl
elif ! command -v openssl >/dev/null 2>&1; then
    as_root apt-get update
    as_root apt-get install -y openssl
fi

as_root systemctl enable --now docker

if [[ ! -f "$env_file" ]]; then
    database_password="$(openssl rand -hex 32)"
    jwt_secret="$(openssl rand -hex 32)"
    umask 077
    cat > "$env_file" <<EOF
PDH_SERVER_HOST=0.0.0.0
PDH_SERVER_PORT=8090
PDH_SERVER_ENV=production
PDH_DATABASE_HOST=db
PDH_DATABASE_PORT=5432
PDH_DATABASE_USER=pdh
PDH_DATABASE_PASSWORD=$database_password
PDH_DATABASE_NAME=pdh
PDH_DATABASE_SSLMODE=disable
PDH_AUTH_JWTSECRET=$jwt_secret
PDH_AUTH_TOKENDURATION=24
PDH_COPILOT_BACKEND=ollama
PDH_COPILOT_OLLAMAURL=http://localhost:11434
PDH_COPILOT_MODEL=llama3.2
PDH_COPILOT_ANTHROPICKEY=
PDH_COPILOT_ANTHROPICMODEL=claude-sonnet-4-20250514
EOF
    chmod 600 "$env_file"
    echo "Generated database password and JWT secret in .env.docker."
else
    chmod 600 "$env_file"
    echo "Using existing .env.docker; it was not overwritten."
fi

as_docker compose \
    --env-file "$env_file" \
    --project-directory "$repo_dir" \
    -f "$repo_dir/compose.yaml" \
    up --detach --build

echo "PDH is starting. Open http://localhost:8090 when its health check passes."
