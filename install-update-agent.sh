#!/usr/bin/env bash
set -Eeuo pipefail

if (( EUID != 0 )); then
    echo "Run this setup as root: sudo ./install-update-agent.sh" >&2
    exit 1
fi

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
env_file="$repo_dir/.env"
if [[ ! -f "$env_file" || ! -d "$repo_dir/.git" ]]; then
    echo "A configured PDH Git checkout with .env is required." >&2
    exit 1
fi

missing_packages=()
command -v git >/dev/null 2>&1 || missing_packages+=(git)
command -v go >/dev/null 2>&1 || missing_packages+=(golang-go)
command -v openssl >/dev/null 2>&1 || missing_packages+=(openssl)
if (( ${#missing_packages[@]} > 0 )); then
    apt-get update
    apt-get install -y "${missing_packages[@]}"
fi

set_env_value() {
    local key="$1"
    local value="$2"
    if grep -q "^${key}=" "$env_file"; then
        sed -i "s|^${key}=.*|${key}=${value}|" "$env_file"
    else
        printf '\n%s=%s\n' "$key" "$value" >> "$env_file"
    fi
}

token="$(grep '^PDH_UPDATE_AGENT_TOKEN=' "$env_file" | tail -n 1 | cut -d= -f2- || true)"
if [[ ${#token} -lt 32 ]]; then
    token="$(openssl rand -hex 32)"
fi
set_env_value PDH_UPDATE_AGENT_TOKEN "$token"
set_env_value PDH_UPDATE_AGENT_URL "http://127.0.0.1:8091"

build_commit="$(git -C "$repo_dir" rev-parse HEAD)"
install -d -m 0750 /var/cache/pdh-updater
mkdir -p "$repo_dir/bin"
(cd "$repo_dir" && go build -trimpath -ldflags="-s -w -X main.buildCommit=$build_commit" -o bin/pdh ./cmd/server)
(cd "$repo_dir" && go build -trimpath -ldflags="-s -w" -o bin/update-agent ./cmd/update-agent)
install -m 0755 "$repo_dir/bin/update-agent" /usr/local/bin/pdh-update-agent

cat > /etc/systemd/system/pdh-update-agent.service <<EOF
[Unit]
Description=PDH Update Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=$env_file
Environment=PDH_UPDATE_LISTEN=127.0.0.1:8091
Environment=PDH_UPDATE_MODE=systemd
Environment=PDH_UPDATE_REPO_DIR=$repo_dir
Environment=GOCACHE=/var/cache/pdh-updater/go-build
Environment=GOMODCACHE=/var/cache/pdh-updater/go-mod
ExecStart=/usr/local/bin/pdh-update-agent
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ReadWritePaths=$repo_dir /var/cache/pdh-updater /usr/local/bin/pdh-update-agent

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now pdh-update-agent
systemctl restart pdh
echo "PDH Update Agent installed and enabled."