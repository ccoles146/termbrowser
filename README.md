# termbrowser

Single-binary web terminal for Proxmox VE hosts. Runs on the Proxmox host and exposes browser-based terminals for the host and all LXC containers. Sessions persist via tmux. Authentication uses password + TOTP.

## Features

- Browser-based terminal via xterm.js
- Sidebar listing the Proxmox host and all LXC containers (via `pct list`)
- Persistent sessions — close the tab and reconnect without losing state (tmux)
- Password + TOTP two-factor authentication
- JWT session cookies (24h expiry)
- Single static binary with all web assets embedded (`go:embed`)
- Dark theme UI

## Requirements

- Proxmox VE host (or any Linux system with `tmux` installed)
- `tmux` must be available in `$PATH`
- For container access: `pct` CLI (ships with Proxmox VE)

## Installation

### From releases

Download the latest binary from the [Releases](../../releases) page:

```bash
# Download and install
curl -L https://github.com/chris/termbrowser/releases/latest/download/termbrowser-linux-amd64 \
  -o /usr/local/bin/termbrowser
chmod +x /usr/local/bin/termbrowser
```

### Build from source

Requires Go 1.24+:

```bash
git clone https://github.com/chris/termbrowser.git
cd termbrowser
CGO_ENABLED=0 go build -ldflags="-s -w" -o termbrowser .
```

The resulting binary is fully static (`ldd` reports "not a dynamic executable").

## Setup

On first run, termbrowser launches an interactive setup wizard:

```bash
termbrowser
```

Or explicitly:

```bash
termbrowser --setup
```

The wizard will:

1. Prompt for a password (entered twice, no echo)
2. Generate a TOTP secret and print the `otpauth://` URI — scan this with your authenticator app (Google Authenticator, Authy, etc.)
3. Save configuration to `config.yaml` next to the binary

### Configuration file

`config.yaml` is created automatically by the setup wizard:

```yaml
password_hash: "$2a$12$..."   # bcrypt hash
totp_secret: "BASE32SECRET"   # TOTP shared secret
port: 8765                    # listen port
jwt_secret: "hex..."          # 32-byte random hex string
```

To change the password or regenerate TOTP, re-run `termbrowser --setup`.

### Custom config path

```bash
termbrowser --config /etc/termbrowser/config.yaml
```

## Usage

```bash
# Start the server (default port 8765)
termbrowser

# Custom config location
termbrowser --config /path/to/config.yaml
```

Open `http://<host-ip>:8765` in a browser, log in with your password and TOTP code.

### Systemd service

```ini
# /etc/systemd/system/termbrowser.service
[Unit]
Description=termbrowser
After=network.target

[Service]
ExecStart=/usr/local/bin/termbrowser
User=root
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now termbrowser
```

## WebSocket protocol

| Direction | Frame type | Payload |
|---|---|---|
| Client to Server | Binary | Raw keyboard input bytes |
| Client to Server | Text (JSON) | `{"type":"resize","cols":N,"rows":N}` |
| Server to Client | Binary | PTY output bytes |

## API endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/api/login` | No | `{"password":"...","totp_code":"..."}` |
| POST | `/api/logout` | No | Clears session cookie |
| GET | `/api/containers` | Yes | Returns JSON array of containers |
| GET | `/ws/terminal/{id}` | Yes | WebSocket terminal (`host` or container CTID) |
| GET | `/` | No | Serves embedded web UI |

## Project structure

```
termbrowser/
├── main.go              # entry point, go:embed, flag parsing
├── config/config.go     # config load/save, first-run setup wizard
├── auth/auth.go         # bcrypt, TOTP, JWT, cookie middleware
├── terminal/terminal.go # PTY session registry, WebSocket handler
├── containers/          # pct list parsing
├── server/server.go     # HTTP routes, WebSocket upgrade
└── web/                 # embedded frontend (xterm.js, app.js, styles)
```
