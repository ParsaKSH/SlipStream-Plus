# SlipStream-Plus ⚡

**Link to Persian Documentation:** [README.md](README.md)

SlipStream-Plus is a powerful **Load Balancer** and **Advanced Management Panel** built on top of the [Slipstream-Rust](https://github.com/mmb0/slipstream-rust) DNS Tunnel core. It integrates cleanly by natively embedding slipstream components and offers extensive workflow features like connection routing, multi-user authentication, monitoring, and an elegant Web GUI.

---

<details>
<summary><b>🔥 Key Features</b></summary>

- **Multi-Threading Load Balancer:** Runs multiple DNS tunnel server instances simultaneously. Balances SOCKS traffic using techniques like Round-Robin, Random, Least Load, and Least Ping.
- **SOCKS5 & SSH Modes:** Fully supports standard tunneling modes, seamlessly wrapped within Go's robust concurrency routines.
- **Smart Health Checks:** Iteratively queries endpoints asynchronously, removing dead/failing connections directly out of the load balancer traffic queues.
- **User Authentication:** Supports an unlimited amount of user configurations bound to SOCKS5 authentication constraints.
- **Precise Limitations:** Dynamically limit user Data Caps (GB/MB), Bandwidth Speeds (Kbits/Mbits), and Concurrent Active IPs connected to the instance.
- **Advanced Monitoring:** A realtime canvas metric visualization to track TX/RX transfers continuously.
</details>

<details>
<summary><b>🌐 Web Management Panel</b></summary>

SlipStream-Plus comes configured automatically with a lightweight, modern, and dark-themed responsive UI for maximum operability while online.

**Panel Highlights:**
- **🎛️ Dashboard:** High-level metrics showing total connections, running instances, and proxy status.
- **🛰️ Instances Tab:** 
  - Rapidly ADD, EDIT, or DELETE active running Slipstream proxies with total GUI control over parameters (e.g. SOCKS/SSH options, Ports, Certs, and Replicas).
  - Built-in localized Restart button for specific instances in the loop.
- **👥 Users Tab:** 
  - Graphically add/modify SOCKS5 Users seamlessly.
  - Tweak Data Quotas, Bandwidth Speeds, and Concurrent Device Limits live.
  - Active Data displays in a graphical progress bar and allows metric Reset functions.
- **📊 Bandwidth Tab:** Split-chart graphing metrics highlighting detailed Transfer/Receive traffic from up to the last 24H intervals.
- **⚙️ Config Tab:** Toggle strategies dynamically on the fly without stopping the daemon. Contains a Full System **Save & Apply** for a graceful reload along with an immediate System **Restart** equivalent to a `kill` daemon event.
</details>

<details>
<summary><b>🚀 Installation & Setup</b></summary>

Instead of compiling manually, you can instantly download out-of-the-box native executables with the Rust core fully embedded by hitting the [Releases](../../releases) tab above.

```bash
# Download latest stable build for Linux
wget https://github.com/ParsaKSH/SlipStream-Plus/releases/latest/download/slipstreamplus-linux-amd64
chmod +x slipstreamplus-linux-amd64

# Start server against your generated config.json
./slipstreamplus-linux-amd64 -config config.json
```

**Building Manually (For Developers):**
```bash
git clone https://github.com/ParsaKSH/SlipStream-Plus
cd SlipStream-Plus

# Compile Linux (Pulls embedded core natively via 'slipstreamorg')
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags embed_slipstream -ldflags="-s -w" -o slipstreamplus-linux-amd64 ./cmd/slipstreamplus

# Compile Windows
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags embed_slipstream -ldflags="-s -w" -o slipstreamplus-windows-amd64.exe ./cmd/slipstreamplus
```
</details>

<details>
<summary><b>📝 Example Configuration (config.json)</b></summary>

```json
{
  "strategy": "round_robin",
  "gui": {
    "enabled": true,
    "listen": "0.0.0.0:8484",
    "username": "admin",
    "password": "Password123"
  },
  "health_check": {
    "interval": "30s",
    "target": "1.1.1.1:53",
    "timeout": "10s"
  },
  "socks": {
    "listen": "0.0.0.0:1082",
    "buffer_size": 131072,
    "max_connections": 10000,
    "users": [
      {
        "username": "user1",
        "password": "pwd",
        "bandwidth_limit": 500,
        "bandwidth_unit": "kbit",
        "data_limit": 10,
        "data_unit": "gb",
        "ip_limit": 2
      }
    ]
  },
  "instances": [
    {
      "domain": "example.com",
      "resolver": "8.8.8.8",
      "port": "17001-17004",
      "mode": "socks",
      "replicas": 1
    }
  ]
}
```
</details>

<br>
Developed with ❤️ by [ParsaKSH](https://github.com/ParsaKSH).
