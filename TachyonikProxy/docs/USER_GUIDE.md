<!--
TachyonikProxy
SPDX-FileCopyrightText: 2026 Tachyonik GmbH
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Tachyonik TachyonikProxy User Guide

TachyonikProxy is an MCP (Model Context Protocol) server that runs in your network and
exposes local security tools (Nmap, Nikto, sqlmap, etc.) to the Tachyonik platform. It
connects securely to ToolManager via mutual TLS, allowing ChatAI to execute tools on
your behalf through natural language.

```
┌─────────────────────┐         mTLS          ┌────────────────────┐
│   Your Network      │◄─────────────────────►│ Tachyonik Platform │
│                     │                       │                    │
│   TachyonikProxy    │       JSON-RPC/SSE    │   ToolManager      │
│   ├── nmap          │                       │   ├── ChatAI       │
│   └── nikto         │                       │   └── WebUI        │
│                     │                       │                    │
└─────────────────────┘                       └────────────────────┘
```

---

## Table of Contents

1. [Installation](#1-installation)
   - [Linux (.deb / .rpm / tarball)](#linux)
   - [macOS (.pkg / tarball)](#macos)
   - [Windows (.msi / .zip)](#windows)
2. [Configuration](#2-configuration)
   - [Basic Setup](#basic-setup)
   - [Adding Tools](#adding-tools)
   - [Tool Configuration Reference](#tool-configuration-reference)
3. [Connecting to Tachyonik](#3-connecting-to-tachyonik)
   - [Quick Setup: One-Command Enrollment](#quick-setup-one-command-enrollment-recommended)
   - [Reverse Enrollment (strict-egress networks)](#reverse-enrollment-strict-egress-networks)
   - [Manual Setup (Alternative)](#manual-setup-alternative)
4. [Registering the TachyonikProxy in Tachyonik](#4-registering-the-tachyonikproxy-in-tachyonik)
5. [Using Tools via ChatAI](#5-using-tools-via-chatai)
6. [Managing the Service](#6-managing-the-service)
7. [Troubleshooting](#7-troubleshooting)

---

## 1. Installation

### Linux

**Debian / Ubuntu (.deb)**

```bash
sudo dpkg -i tachyonikproxy-1.0.0-linux-amd64.deb
```

**RHEL / Fedora (.rpm)**

```bash
sudo rpm -i tachyonikproxy-1.0.0-linux-amd64.rpm
```

Both package formats automatically:
- Install the binary to `/usr/bin/tachyonikproxy`
- Create a `tachyonikproxy` system user
- Place a default config at `/etc/tachyonik/tachyonikproxy/config.yaml`
- Install a systemd service unit
- Create `/var/log/tachyonik/` for logs

**Portable tarball**

```bash
tar xzf tachyonikproxy-1.0.0-linux-amd64.tar.gz
cd tachyonikproxy-1.0.0/

# Run directly
./tachyonikproxy

# Or install manually
sudo cp tachyonikproxy /usr/bin/
sudo mkdir -p /etc/tachyonik/tachyonikproxy
sudo cp config.yaml /etc/tachyonik/tachyonikproxy/
sudo cp tachyonikproxy.service /lib/systemd/system/
sudo systemctl daemon-reload
```

### macOS

**Installer package (.pkg)**

```bash
sudo installer -pkg tachyonikproxy-1.0.0-darwin-arm64.pkg -target /
```

This installs:
- Binary to `/usr/local/bin/tachyonikproxy`
- Default config to `/etc/tachyonik/tachyonikproxy/config.yaml`
- LaunchDaemon plist to `/Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist`

**Portable tarball**

```bash
tar xzf tachyonikproxy-1.0.0-darwin-arm64.tar.gz
cd tachyonikproxy-1.0.0/

# Run directly
./tachyonikproxy

# Or install to system paths
sudo cp tachyonikproxy /usr/local/bin/
sudo mkdir -p /etc/tachyonik/tachyonikproxy
sudo cp config.yaml /etc/tachyonik/tachyonikproxy/
```

### Windows

**Installer (.msi)**

Run `tachyonikproxy-1.0.0-windows-amd64.msi` and follow the wizard. The installer
places files in `C:\Program Files\Tachyonik\TachyonikProxy\`.

After installation, register the Windows service:

```powershell
cd "C:\Program Files\Tachyonik\TachyonikProxy"
.\tachyonikproxy.exe install
```

**Portable (.zip)**

Extract `tachyonikproxy-1.0.0-windows-amd64.zip` to a directory of your choice.

```powershell
# Run interactively
.\tachyonikproxy.exe

# Or install as a Windows service
.\tachyonikproxy.exe install
```

**Verify the installation** on any platform:

```bash
tachyonikproxy version
# Tachyonik TachyonikProxy 1.0.0
```

---

## 2. Configuration

### Basic Setup

Edit the configuration file. On package installs, this is located at:

| Platform | Path |
|----------|------|
| Linux    | `/etc/tachyonik/tachyonikproxy/config.yaml` |
| macOS    | `/etc/tachyonik/tachyonikproxy/config.yaml` |
| Windows  | `C:\Program Files\Tachyonik\TachyonikProxy\config.yaml` |

For portable installs, `config.yaml` is in the same directory as the binary. You can
override the path with the `TACHYONIKPROXY_CONFIG` environment variable.

**Minimal config.yaml:**

```yaml
server:
  port: 9100
  host: "0.0.0.0"

proxy:
  name: "my-kali-box"
  description: "Kali Linux penetration testing tools"

log:
  file_path: "/var/log/tachyonik/tachyonikproxy.log"
  to_console: false
  to_file: true
  level: INFO

tools: []

mcp_servers: []
allow_remote_config: false
```

- **proxy.name** — A unique identifier for this proxy. This name appears in
  the Tachyonik WebUI and is used as a prefix for tool names (e.g. `my-kali-box.nmap_scan`).
- **server.host** — Use `0.0.0.0` to accept connections from ToolManager. Use
  `127.0.0.1` if ToolManager runs on the same host.
- **allow_remote_config** — When `true`, ToolManager can push configuration changes
  to this proxy remotely. Set to `false` in high-security environments.

### Adding Tools

Each tool definition maps a tool name to a local command. Here is a complete example
with three tools:

```yaml
tools:
  - name: "nmap_scan"
    description: "Run an Nmap network scan to discover hosts and services"
    command: "nmap"
    args_schema:
      type: "object"
      properties:
        target:
          type: "string"
          description: "Target IP, hostname, or CIDR range"
        scan_type:
          type: "string"
          enum: ["-sS", "-sT", "-sU", "-sV", "-A"]
          description: "Scan type flag"
        ports:
          type: "string"
          description: "Port specification (e.g. '22,80,443' or '1-1024')"
      required: ["target"]
    arg_template: "{{.scan_type}} {{if .ports}}-p {{.ports}}{{end}} {{.target}}"
    timeout: 300
    allowed_exit_codes: [0, 1]
    allowed_chars: "a-zA-Z0-9.:/\\-_, "
    max_output_bytes: 5242880
    max_cpu_seconds: 300

  - name: "nikto_scan"
    description: "Run Nikto web server scanner for known vulnerabilities"
    command: "nikto"
    args_schema:
      type: "object"
      properties:
        host:
          type: "string"
          description: "Target URL or hostname"
        tuning:
          type: "string"
          description: "Scan tuning options"
      required: ["host"]
    arg_template: "-h {{.host}} {{if .tuning}}-Tuning {{.tuning}}{{end}}"
    timeout: 600
    allowed_chars: "a-zA-Z0-9.:/\\-_, "

  - name: "searchsploit"
    description: "Search the Exploit Database for known exploits"
    command: "searchsploit"
    args_schema:
      type: "object"
      properties:
        query:
          type: "string"
          description: "Search query (e.g. 'apache 2.4')"
        json_output:
          type: "boolean"
          description: "Return results as JSON"
      required: ["query"]
    arg_template: "{{if .json_output}}-j{{end}} {{.query}}"
    timeout: 30
    allowed_chars: "a-zA-Z0-9. -_"
```

### Tool Configuration Reference

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Unique tool name (alphanumeric + underscore) |
| `description` | Yes | Human-readable description. The AI reads this to decide when to use the tool. |
| `command` | Yes | Local command to execute (must be in PATH) |
| `args_schema` | Yes | JSON Schema describing the tool's input parameters |
| `arg_template` | Yes | Go template that renders arguments from the schema into a command line |
| `timeout` | No | Maximum execution time in seconds (default: 120) |
| `allowed_exit_codes` | No | Exit codes considered successful (default: `[0]`) |
| `allowed_chars` | No | Regex character class for argument sanitization (e.g. `"a-zA-Z0-9.-"`) |
| `max_output_bytes` | No | Truncate output beyond this size (default: 10 MB) |
| `max_memory_mb` | No | Memory limit for the process (Linux only, via ulimit) |
| `max_cpu_seconds` | No | CPU time limit (Linux only, via ulimit) |

**Writing good descriptions:** The AI uses `description` to choose the right tool.
Be specific about what the tool does and when it should be used. For example,
"Run Nmap to scan a network target for open ports and running services" is better
than "nmap wrapper".

**Writing arg_template:** Uses Go's `text/template` syntax. Schema properties are
available as `.property_name`. Use `{{if .prop}}...{{end}}` for optional arguments.

**Example: Windows tool (ipconfig)**

```yaml
tools:
  - name: "ipconfig"
    description: "Show Windows network adapter configuration"
    command: "ipconfig"
    args_schema:
      type: "object"
      properties:
        all:
          type: "boolean"
          description: "Show detailed configuration for all adapters"
    arg_template: "{{if .all}}/all{{end}}"
    timeout: 10
    allowed_chars: "a-zA-Z0-9/ "
```

**Example: macOS tool (system_profiler)**

```yaml
tools:
  - name: "system_info"
    description: "Show macOS hardware and software overview"
    command: "system_profiler"
    args_schema:
      type: "object"
      properties:
        data_type:
          type: "string"
          enum: ["SPHardwareDataType", "SPSoftwareDataType", "SPNetworkDataType"]
          description: "Type of system information to retrieve"
      required: ["data_type"]
    arg_template: "{{.data_type}}"
    timeout: 30
    allowed_chars: "a-zA-Z"
```

---

## 3. Connecting to Tachyonik

TachyonikProxy and ToolManager authenticate each other using mutual TLS (mTLS). Both
sides present certificates signed by a shared private Certificate Authority (CA).
This ensures:

- ToolManager can verify it is talking to a legitimate TachyonikProxy
- TachyonikProxy can verify that only an authorised ToolManager connects to it
- All traffic is encrypted with TLS 1.3

### Quick Setup: One-Command Enrollment (Recommended)

The easiest way to connect a TachyonikProxy to Tachyonik is the enrollment flow. An admin
creates a one-time enrollment token in the WebUI, and the proxy operator runs a
single command to receive certificates and configure everything automatically.

**Step 1: Create an enrollment token (Admin, in WebUI)**

1. Log in to the Tachyonik WebUI.
2. Navigate to **Proxies**.
3. Click **Add Proxy**. The "Enroll (recommended)" tab is selected by default.
4. Enter a proxy **Name** and optional **Description**.
5. Optionally set the proxy **URL** (if known) and **Token lifetime** (default:
   60 minutes).
6. Click **Create Enrollment Token**.
7. Copy the enrollment command that appears.

**Step 2: Enroll the proxy (Operator, on the proxy host)**

```bash
sudo tachyonikproxy enroll "https://tachyonik.example.com/api/proxy-enroll?token=TOKEN"
```

The `enroll` command:
- Contacts the Tachyonik server and exchanges the token for TLS certificates
- Writes `ca.crt`, `server.crt`, and `server.key` to the cert directory
- Updates `config.yaml` with the TLS section and proxy name
- Reports its hostname and IP so Tachyonik knows how to reach it

**Step 3: Start the service**

```bash
sudo systemctl start tachyonikproxy
```

The proxy status in the WebUI changes from "pending" to "enrolled" after
enrollment, and to "connected" once ToolManager reaches it.

**Enrollment flags:**

| Flag | Description |
|------|-------------|
| `--config <path>` | Path to config.yaml (default: auto-detect) |
| `--cert-dir <path>` | Certificate output directory (default: `./certs`) |
| `--port <port>` | Override listening port reported to server (default: 9100) |
| `--insecure` | Skip TLS verification (for self-signed reverse proxy setups) |
| `--listen` | Reverse enrollment: wait for Tachyonik to dial in (see next section) |
| `--san <names>` | Comma-separated extra DNS names/IPs for the bootstrap cert (listen mode) |
| `--force` | Skip the "already enrolled" confirmation prompt |

**Re-enrollment:** To rotate certificates for an existing proxy, open it in the
WebUI and click "Re-Enroll (new token)". This generates a new token. Run
`tachyonikproxy enroll` again on the proxy host with the new URL.

**Security:** The enrollment token is single-use, time-limited, and stored as a
SHA-256 hash in the database. The certificate exchange happens over HTTPS.

---

### Reverse Enrollment (strict-egress networks)

The online enrollment flow above needs one-time outbound HTTPS from the proxy
to Tachyonik. On strict-egress networks — where only ToolManager → Proxy
traffic is allowed — use **reverse enrollment**. The proxy listens on :9100,
prints a pairing code, and waits for Tachyonik to dial in and configure it.
No proxy-originated network traffic at any point.

**Step 1: Start the listener (Operator, on the proxy host)**

```bash
sudo tachyonikproxy enroll --listen
```

The command prints a 6-digit pairing code (e.g. `418-273`) and listens on
port 9100 with a short-lived self-signed bootstrap certificate. The window
is 10 minutes; after that the command exits and you must re-run it.

If Tachyonik reaches the proxy via a hostname or IP that isn't its local
hostname (e.g. a DMZ name or NAT address), add it with `--san`:

```bash
sudo tachyonikproxy enroll --listen --san proxy.dmz.example.com,10.0.1.42
```

**Step 2: Enter the pairing code (Admin, in WebUI)**

1. Log in to the Tachyonik WebUI and navigate to **Proxies → Add Proxy**.
2. Switch the Setup toggle to **Reverse Enroll**.
3. Fill in **Name**, **Proxy URL** (e.g. `https://proxy.dmz.example.com:9100`),
   the **Pairing code** printed on the proxy host, and any
   **Additional addresses** to include as SANs in the signed certificate.
4. Click **Start Reverse Enrollment**. Tachyonik dials the proxy, verifies
   the code, signs the proxy's CSR, and delivers the TLS bundle. The proxy
   status flips from **Pending** to **Enrolled** when the handshake finishes
   (typically a few seconds).

**Security model:**

- The pairing code is single-use, constant-time compared, and rate-limited
  (5 attempts per source IP before cool-down, 15 total before the listener
  terminates).
- The proxy's server key is generated on the proxy host and never leaves it.
- The bootstrap TLS cert fingerprint is pinned between `/enroll/hello` and
  `/enroll/complete` to prevent split-session MITM.
- The ticket is single-use and expires 5 minutes after creation in the WebUI.

**Re-enrollment:** To rotate certificates, run `tachyonikproxy reset-enrollment`
on the proxy host, then repeat the reverse-enrollment flow. The admin re-using
the same proxy Name keeps the WebUI history in place.

---

### Manual Setup (Alternative)

If you prefer to manage certificates manually (e.g. in air-gapped environments),
you can generate them with scripts and configure everything by hand.

#### Generating Certificates

**Step 1: Generate the CA** on a trusted machine:

```bash
./scripts/gen-ca-manager-certs.sh /path/to/certs
```

This creates `ca.key` (keep secure) and `ca.crt` (distribute to both sides).

**Step 2: Generate per-proxy certificates:**

```bash
./scripts/gen-proxy-certs.sh /path/to/certs my-proxy
```

This creates:

| File | Purpose | Deploy to |
|------|---------|-----------|
| `my-proxy.key` | TachyonikProxy server private key | TachyonikProxy only |
| `my-proxy.crt` | TachyonikProxy server certificate | TachyonikProxy only |
| `my-proxy-client.key` | Client private key for this proxy | Store in WebUI credential |
| `my-proxy-client.crt` | Client certificate for this proxy | Store in WebUI credential |

**Transfer** `ca.crt`, `my-proxy.crt`, and `my-proxy.key` to the TachyonikProxy host
via a secure channel (SCP, USB drive, etc.).

#### Configuring TachyonikProxy (Server Side)

Add the `tls` section to the TachyonikProxy's `config.yaml`:

```yaml
tls:
  ca_cert: "/etc/tachyonik/tachyonikproxy/certs/ca.crt"
  server_cert: "/etc/tachyonik/tachyonikproxy/certs/my-proxy.crt"
  server_key: "/etc/tachyonik/tachyonikproxy/certs/my-proxy.key"
  allowed_clients:
    - "my-proxy-client"
```

- **allowed_clients** — List of Common Names (CNs) from client certificates that
  are allowed to connect. For a proxy named `my-proxy`, the generated client
  certificate has `CN=my-proxy-client`.

After restarting, the log should show:

```
INFO  TachyonikProxy listening on https://0.0.0.0:9100 (mTLS enabled)
INFO  Allowed clients: [my-proxy-client]
```

#### Configuring ToolManager (Platform Side)

ToolManager uses **per-proxy TLS credentials** stored in the Tachyonik WebUI. There is
no global TLS configuration — each proxy gets its own credential.

---

## 4. Registering the TachyonikProxy in Tachyonik

If you used the enrollment flow (section 3), the proxy is already registered and
this step is complete. The sections below apply to the manual setup.

### Step 1: Create a Credential

1. Log in to the Tachyonik WebUI as an admin.
2. Navigate to **Proxies**.
3. Click **Add Proxy** and switch to the **Manual Setup** tab.
4. Fill in **Name**, **URL** (`https://proxy-host:9100`), and **Description**.
5. In the **Credential** dropdown, click **Generate** to auto-generate a
   certificate credential signed by the CA, or select an existing one.
6. Click **Save**.

Alternatively, create a credential manually under **Sources > Credentials** by
pasting PEM content for the CA cert, client cert, and client key.

Every proxy **must** have a linked credential for HTTPS connections. ToolManager
will refuse to connect to an HTTPS proxy without one.

**Example: Two proxies with independent certificates**

```
Proxy A (kali-box)     → Credential "kali-mtls"   → CA-A + cert-A + key-A
Proxy B (windows-scan) → Credential "win-mtls"    → CA-B + cert-B + key-B
```

Revoking access to Proxy A is as simple as deleting the `kali-mtls` credential.
Proxy B is unaffected.

ToolManager automatically connects to registered proxies when a user sends a
tool-enabled chat message.

**Verify the connection** — Navigate to **AI > ToolManager** in the sidebar. Check
that the proxy appears in the settings page and its tools are listed.

---

## 5. Using Tools via ChatAI

With a TachyonikProxy registered and connected, you can use its tools through the
ChatAI dialog in the WebUI.

### Enabling Tool Mode

1. Open the **ChatAI** dialog (click the chat icon in the bottom-right).
2. Click the **wrench icon** (🔧) next to the Send button. It turns blue when
   tools are enabled.
3. Type your message and press **Send**.

When tool mode is active, ChatAI routes your message through ToolManager, which
has access to all tools from your connected ToolProxies.

### Example Conversations

**Example 1: Network scanning**

> **You:** Scan 192.168.1.0/24 for open web servers
>
> **ChatAI:** I'll run an Nmap scan targeting common web ports on that subnet.
>
> *[Executing tool: my-kali-box.nmap_scan with target=192.168.1.0/24,
> scan_type=-sS, ports=80,443,8080,8443]*
>
> Here are the results:
>
> | Host | Port | State | Service |
> |------|------|-------|---------|
> | 192.168.1.1 | 80/tcp | open | nginx |
> | 192.168.1.1 | 443/tcp | open | nginx |
> | 192.168.1.50 | 8080/tcp | open | Apache Tomcat |
> | 192.168.1.100 | 443/tcp | open | Apache httpd |
>
> 3 hosts found with open web server ports. The Tomcat instance on
> 192.168.1.50:8080 may warrant further investigation — Tomcat management
> interfaces are frequently targeted.

**Example 2: Vulnerability lookup**

> **You:** Are there any known exploits for Apache 2.4.49?
>
> *[Executing tool: my-kali-box.searchsploit with query=apache 2.4.49,
> json_output=true]*
>
> **ChatAI:** Yes, Apache 2.4.49 has a critical path traversal vulnerability
> (CVE-2021-41773). ExploitDB lists:
>
> - **Apache 2.4.49 - Path Traversal & RCE** (EDB-50383)
> - **Apache 2.4.49/50 - Remote Code Execution** (EDB-50406)
>
> This allows an unauthenticated attacker to read files outside the document
> root and, when `mod_cgi` is enabled, execute arbitrary commands. Upgrade to
> Apache 2.4.51 or later immediately.

**Example 3: Web vulnerability scanning**

> **You:** Run a Nikto scan against https://staging.example.com
>
> *[Executing tool: my-kali-box.nikto_scan with host=https://staging.example.com]*
>
> **ChatAI:** Nikto scan complete. Key findings:
>
> - **Server:** Apache/2.4.41 (Ubuntu)
> - `/admin/` directory found (not linked) — potential admin panel exposure
> - X-Frame-Options header missing — susceptible to clickjacking
> - Server leaks version via `Server` header
> - 2 outdated components detected
>
> Recommended actions: restrict `/admin/` access, add security headers, and
> update Apache to the latest version.

**Example 4: Multi-step investigation**

> **You:** Scan 10.0.0.5 to find open services, then check if any have known
> exploits
>
> *[Executing tool: my-kali-box.nmap_scan with target=10.0.0.5, scan_type=-sV]*
>
> *[Executing tool: my-kali-box.searchsploit with query=vsftpd 2.3.4]*
>
> **ChatAI:** The scan found 3 open ports on 10.0.0.5:
>
> - 21/tcp — vsftpd 2.3.4
> - 22/tcp — OpenSSH 8.9
> - 80/tcp — Apache 2.4.52
>
> I checked for exploits and found a critical issue: **vsftpd 2.3.4** contains
> a backdoor (CVE-2011-2523, EDB-17491) that allows unauthenticated remote code
> execution. This version must be replaced immediately.

The AI automatically selects which tools to use, what arguments to pass, and can
chain multiple tool calls in sequence to answer complex questions.

---

## 6. Managing the Service

### Linux (systemd)

```bash
# Start the service
sudo systemctl start tachyonikproxy

# Stop the service
sudo systemctl stop tachyonikproxy

# Restart after config changes
sudo systemctl restart tachyonikproxy

# Check status
sudo systemctl status tachyonikproxy

# View logs
sudo journalctl -u tachyonikproxy -f

# Enable auto-start on boot (done automatically by the .deb/.rpm)
sudo systemctl enable tachyonikproxy
```

### macOS (launchd)

```bash
# Start the service
sudo launchctl load /Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist

# Stop the service
sudo launchctl unload /Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist

# Restart after config changes
sudo launchctl unload /Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist
sudo launchctl load /Library/LaunchDaemons/com.tachyonik.tachyonikproxy.plist

# View logs
tail -f /var/log/tachyonik/tachyonikproxy.log
```

### Windows (Service)

```powershell
# Install the service (first time only, requires Administrator)
.\tachyonikproxy.exe install

# Start
net start TachyonikTachyonikProxy

# Stop
net stop TachyonikTachyonikProxy

# Restart after config changes
net stop TachyonikTachyonikProxy && net start TachyonikTachyonikProxy

# Uninstall the service
.\tachyonikproxy.exe uninstall

# View logs
Get-Content "C:\Program Files\Tachyonik\TachyonikProxy\tachyonikproxy.log" -Tail 50 -Wait
```

### Resetting Enrollment

To clear all enrollment material (CA, server cert, server key, client cert/key,
and the corresponding fields in `config.yaml`), run:

```bash
sudo tachyonikproxy reset-enrollment
```

The command lists what it will delete and prompts for `DELETE` typed literally
as confirmation. Use `--force` to skip the prompt in unattended scripts. This
is the expected way to rotate certificates before re-running either `enroll`
or `enroll --listen`.

`reset-enrollment` preserves the `tools:`, `log:`, and `server.port/host`
sections of your config — only the server-provisioned fields are cleared.

### Running Interactively (All Platforms)

For testing or debugging, run the binary directly in a terminal:

```bash
# Linux / macOS
TACHYONIKPROXY_CONFIG=/path/to/config.yaml tachyonikproxy

# Windows PowerShell
$env:TACHYONIKPROXY_CONFIG="C:\path\to\config.yaml"
.\tachyonikproxy.exe
```

Press `Ctrl+C` to stop.

### Environment Variable Overrides

These override values from config.yaml:

| Variable | Description |
|----------|-------------|
| `TACHYONIKPROXY_CONFIG` | Path to config file (default: `./config.yaml`) |
| `TACHYONIKPROXY_PORT` | Server port |
| `TACHYONIKPROXY_HOST` | Server bind address |
| `TACHYONIKPROXY_LOG_LEVEL` | Log level: `DEBUG`, `INFO`, `WARN`, `ERROR` |

---

## 7. Troubleshooting

### TachyonikProxy won't start

**Symptom:** "Address already in use"

Another process is using port 9100. Change the port in `config.yaml` or find the
conflicting process:

```bash
# Linux / macOS
lsof -i :9100

# Windows
netstat -ano | findstr :9100
```

**Symptom:** "Failed to load TLS config"

Check that all three TLS files (`ca_cert`, `server_cert`, `server_key`) exist and
are readable by the `tachyonikproxy` user. Verify the paths in `config.yaml` are
absolute and correct.

### ToolManager cannot connect

**Symptom:** "failed to contact ToolManager" or connection timeout

1. Verify the TachyonikProxy is reachable from the ToolManager host:
   ```bash
   curl -k https://proxy-host:9100/health
   # Should return: {"status":"ok"}
   ```
2. Check firewall rules — port 9100 must be open for inbound TCP.
3. If using mTLS, test with the proxy's client certificate:
   ```bash
   curl --cert my-proxy-client.crt --key my-proxy-client.key --cacert ca.crt \
     https://proxy-host:9100/health
   ```

**Symptom:** "TLS handshake failure" or "certificate signed by unknown authority"

- Ensure both sides use the same `ca.crt` file.
- Verify the ToolManager certificate's CN matches an entry in `allowed_clients`.
- Check that certificates haven't expired:
  ```bash
  openssl x509 -in tachyonikproxy.crt -noout -dates
  ```

### Tools not appearing in ChatAI

1. Verify the TachyonikProxy source is registered in Tachyonik (Sources page, category
   `tool`).
2. Check that the source URL matches the proxy's address including the port.
3. Verify the TachyonikProxy has tools configured (not an empty `tools: []` list).
4. Check the ToolManager log for connection errors.

### Tool execution fails

**Symptom:** "executable file not found in $PATH"

The tool's `command` must be available in the system PATH for the user running
TachyonikProxy. On Linux package installs, the service runs as the `tachyonikproxy`
user. Verify:

```bash
sudo -u tachyonikproxy which nmap
```

If the tool isn't in the service user's PATH, use the full path in `command`:

```yaml
tools:
  - name: "nmap_scan"
    command: "/usr/bin/nmap"
    # ...
```

**Symptom:** "argument contains disallowed characters"

The `allowed_chars` field restricts which characters can appear in tool arguments.
If legitimate input is blocked, expand the character class:

```yaml
# Before (too restrictive)
allowed_chars: "a-zA-Z0-9."

# After (allows hyphens and slashes for URLs)
allowed_chars: "a-zA-Z0-9.:/\\-_"
```

**Symptom:** "tool execution timed out"

Increase the `timeout` value for long-running tools. The default is 120 seconds.
Network scans of large subnets may need 600 seconds or more.

### Regenerating certificates

If certificates are compromised or expired, regenerate the CA and per-proxy
certificates:

```bash
# Regenerate the CA
./scripts/gen-ca-manager-certs.sh /path/to/certs

# Regenerate certificates for each proxy
./scripts/gen-proxy-certs.sh /path/to/certs my-proxy
```

Then replace the certificate files on the TachyonikProxy host, update the credential
in the WebUI with the new client certificate and key, and restart the TachyonikProxy
service.
