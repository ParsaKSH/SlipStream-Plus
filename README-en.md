# SlipStream-Plus ⚡

[Persian (فارسی)](README.md)

SlipStream-Plus is a **Load Balancer** and **Advanced Management Panel** built on top of the powerful [Slipstream-Rust](https://github.com/Mygod/slipstream-rust) DNS tunnel core. This software natively embeds the core binary allowing you to manage multi-client connections, multi-user definitions, graphical monitoring, and bandwidth caps seamlessly.

This project enables you to run multiple SlipStream configurations concurrently. The active traffic bounds are automatically load-balanced across your instances which significantly increases overall connection quality and throughput speeds.

You can operate this core either on remote servers that have fast connections bridging to DNS resolvers, or standalone right from your personal machine.

---

<details>
<summary><b>🔥 Key Features</b></summary>

- **Multi-Threading Load Balancer:** Runs multiple DNS tunnel server instances simultaneously. Balances SOCKS traffic using techniques like Round-Robin, Random, Least Load, and Least Ping.
- **SOCKS5 & SSH Modes:** Fully supports standard proxy modes alongside port forwarding.
- **Smart Health Checks:** Iteratively queries endpoints asynchronously, removing dead/failing connections directly out of the load balancer traffic queues.
- **User Authentication:** Supports an unlimited amount of user configurations bound to SOCKS5 authentication constraints alongside quota systems.
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
  - Active Data displays in a graphical progress bar and allows metric Reset functions per user.
- **📊 Bandwidth Tab:** Split-chart graphing metrics highlighting detailed Transfer/Receive traffic from up to the last 24H intervals.
- **⚙️ Config Tab:** Toggle strategies dynamically on the fly without stopping the daemon. Contains a Full System **Save & Apply** for a graceful reload along with an immediate System **Restart** equivalent to a `kill` daemon event.
</details>

<details>
<summary><b>🚀 Installation & Setup</b></summary>

Instead of compiling manually, you can instantly download out-of-the-box native executables with the Rust core fully embedded by hitting the [Releases](../../releases) tab repository page.

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
  "strategy": "least_load",
  "gui": {
    "enabled": true,
    "listen": "0.0.0.0:8484",
    "username": "admin",
    "password": "Password123"
  },
  "health_check": {
    "interval": "30s",
    "target": "google.com",
    "timeout": "10s"
  },
  "socks": {
    "listen": "0.0.0.0:1080",
    "buffer_size": 65536,
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
      "port": "17001",
      "mode": "socks",
      "replicas": 1
    }
  ]
}
```
</details>

<details>
<summary><b>🔬 Technical & Advanced Run Details</b></summary>

**SlipStream-Plus** operates vastly beyond a simple wrapper for the Rust client. The backbone of the integration revolves around a resilient Goroutine-based Concurrency pool architected specifically to extract latency gains across highly restricted DNS network tunnels.

**Load Balancer & Pipe Proxying Architecture:**
Whenever an inbound client reaches your SOCKS5/SSH port socket, the system executes an optimized Handshake protocol routing the user directly to the most stable replica active. In an effort to minimize connection drag on restricted networks, SOCKS Greeting blocks are linearly pipelined into singular TCP IO requests to directly cut DNS Tunnel Round-Trip Time down significantly.

**Process Management Supervisor:**
Every instance of the Rust Slipstream Core runs monitored inside a supervised daemon thread. If an instance experiences core crashes, heavy disconnections, or failure loops stemming from restricted routing, the Supervisor handles graceful SIGKILL events to free bounded replica ports, subsequently rebuilding that specific runtime instance back into the traffic pool automatically—eliminating downtime.

**Bandwidth Tracking & Quota Controllers:**
All user traffic maps flow through a custom Token-Bucket shaping routine explicitly defined by burst sizing limitations that inherently prevents connection-heavy users from exhausting small-memory networking resources. Concurrency rules operate off a mapped cooldown tracker preventing sudden DNS delays artificially rejecting properly structured authenticated attempts within the proxy flow logic.
</details>

---

## ❤️ Support the project

If you found this project helpful, you can actively verify your support through the addresses below:

| Currency | Wallet Address |
|-------|------------|
| **Tron** | `TD3vY9Drpo3eLi8z2LtGT9Vp4ESuF2AEgo` |
| **USDT(ERC20)** | `0x800680F566A394935547578bc5599D98B139Ea22` |
| **TON** | `UQAm3obHuD5kWf4eE4JmAO_5rkQdZPhaEpmRWs6Rk8vGQJog` |
| **BTC** | `bc1qaquv5vg35ua7qnd3wlueytw0fugpn8qkkuq9r2` |

<a href="https://nowpayments.io/donation?api_key=FH429FA-35N4AGZ-MFMRQ3Q-2H4BF98" target="_blank" rel="noreferrer noopener">
    <img src="https://nowpayments.io/images/embeds/donation-button-white.svg" width="200" alt="Crypto donation button by NOWPayments">
</a>

Thank you for your support! 

---

## ⭐️ Star the Repository

If this repository assisted your networking goals, please consider leaving a Star. Visibility allows others finding robust alternative routing solutions easier access.

---

Developed with the help of **Claude Opus 4.6**

Wishing prosperity for an ever-growing Iran... 
And success for you! 🚀✨
