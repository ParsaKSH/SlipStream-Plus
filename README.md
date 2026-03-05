# SlipstreamPlus

<p align="center">
  <b>Multi-threaded DNS Tunnel Load Balancer</b><br>
  A high-performance Go wrapper for <a href="https://github.com/Mygod/slipstream-rust">slipstream-rust</a>
</p>

<p align="center">
  <a href="https://github.com/ParsaKSH/slipstream-plus">GitHub</a> •
  <a href="#installation">Installation</a> •
  <a href="#configuration">Configuration</a> •
  <a href="#web-gui">Web GUI</a>
</p>

---

## What is SlipstreamPlus?

SlipstreamPlus manages **multiple slipstream-client instances** behind a single TCP proxy with load balancing, health monitoring, and automatic restart. It turns multiple DNS tunnels into a single reliable, high-bandwidth connection.

### Features

- 🔀 **Load Balancing** — Round-robin, random, least-ping, or least-load strategies
- 🔄 **Auto-Restart** — Crashed instances automatically respawn with exponential backoff
- 📊 **Health Monitoring** — Periodic latency probes through each tunnel
- 🎯 **Replicas** — Spin up N copies of an instance with port ranges
- 🖥️ **Web GUI** — Real-time dark-themed dashboard for monitoring and config
- 📦 **Embedded Binary** — Optional: embed slipstream-client inside the Go binary

### Architecture

```
End User ──► TCP Proxy (:1080) ──► Load Balancer ──┬──► Instance 0 (slipstream-client :17001)
                                                    ├──► Instance 1 (slipstream-client :17002)
                                                    ├──► Instance 2 (slipstream-client :17003)
                                                    └──► Instance N ...
                                                              │
                                                      DNS Tunnel (QUIC over DNS)
                                                              │
                                                    slipstream-server (:53) ──► Target
```

---

## Installation

### Prerequisites

- Go 1.21+
- Rust toolchain (for building slipstream-rust)
- cmake, pkg-config, libssl-dev

### Build

```bash
# Clone
git clone https://github.com/ParsaKSH/slipstream-plus.git
cd slipstream-plus

# Build slipstream-rust
cd orgslipstream/slipstream-rust
git submodule update --init --recursive
cargo build --release -p slipstream-client -p slipstream-server
cd ../..

# Build SlipstreamPlus (CLI only)
CGO_ENABLED=0 go build -o slipstreamplus ./cmd/slipstreamplus/

# Build with GUI
CGO_ENABLED=0 go build -o slipstreamplus ./cmd/slipstreamplus/
# (GUI is always included, enable via --gui flag or config)
```

### Build with Embedded Binary

To bundle slipstream-client inside the Go binary:

```bash
# Copy the binary to the embed directory
cp orgslipstream/slipstream-rust/target/release/slipstream-client internal/embedded/

# Build with embed tag
CGO_ENABLED=0 go build -tags embed_slipstream -o slipstreamplus ./cmd/slipstreamplus/
```

---

## Usage

```bash
# Basic usage
./slipstreamplus -config config.json

# With GUI enabled
./slipstreamplus -config config.json -gui

# With custom GUI port
./slipstreamplus -config config.json -gui -gui-listen 0.0.0.0:9090
```

---

## Configuration

### Full Example (`config.json`)

```json
{
    "socks": {
        "listen": "0.0.0.0:1080",
        "buffer_size": 65536,
        "max_connections": 10000
    },
    "slipstream_binary": "./orgslipstream/slipstream-rust/target/release/slipstream-client",
    "strategy": "round_robin",
    "health_check": {
        "interval": "10s",
        "target": "google.com",
        "timeout": "5s"
    },
    "gui": {
        "enabled": false,
        "listen": "127.0.0.1:8384"
    },
    "instances": [
        {
            "domain": "t1.example.com",
            "resolver": "8.8.8.8:53",
            "port": 17001,
            "replicas": 1,
            "authoritative": false
        },
        {
            "domain": "t2.example.com",
            "resolver": "1.1.1.1:53",
            "port": "17002-17005",
            "replicas": 4,
            "authoritative": false
        }
    ]
}
```

### Config Reference

| Field | Description | Default |
|-------|-------------|---------|
| `socks.listen` | TCP proxy listen address | `0.0.0.0:1080` |
| `socks.buffer_size` | Per-connection buffer size (bytes) | `65536` |
| `socks.max_connections` | Max concurrent connections | `10000` |
| `slipstream_binary` | Path to slipstream-client binary | (required) |
| `strategy` | `round_robin`, `random`, `least_ping`, `least_load` | `round_robin` |
| `health_check.interval` | Health probe interval | `10s` |
| `health_check.target` | HTTP target for latency measurement | `google.com` |
| `health_check.timeout` | Probe timeout | `5s` |
| `gui.enabled` | Enable web dashboard | `false` |
| `gui.listen` | GUI listen address | `127.0.0.1:8384` |

### Instance Config

| Field | Description | Example |
|-------|-------------|---------|
| `domain` | DNS tunnel domain | `"t1.example.com"` |
| `resolver` | DNS resolver address | `"8.8.8.8:53"` |
| `port` | Local TCP port or range | `17001` or `"17001-17004"` |
| `replicas` | Number of copies to spawn | `4` |
| `authoritative` | Use authoritative mode | `false` |
| `cert` | TLS cert for pinning (optional) | `"cert.pem"` |

### Replicas & Port Ranges

When `replicas` > 1, you can provide a port range:

```json
{
    "domain": "tunnel.example.com",
    "resolver": "8.8.8.8:53",
    "port": "17001-17004",
    "replicas": 4
}
```

This spawns 4 slipstream-client instances on ports 17001, 17002, 17003, 17004 — all using the same domain and resolver. If you provide a single port with replicas > 1, ports are auto-incremented from the base.

### Load Balancing Strategies

| Strategy | Description |
|----------|-------------|
| `round_robin` | Each new connection goes to the next instance in order |
| `random` | Random instance selection |
| `least_ping` | Routes to the instance with lowest measured latency |
| `least_load` | Routes to the instance with fewest active connections |

---

## Web GUI

Enable with `--gui` flag or set `"gui": {"enabled": true}` in config.

The dashboard provides:
- **Real-time stats**: total instances, healthy count, active connections
- **Instance table**: status, domain, resolver, port, latency, connections
- **Config editor**: modify all settings and save without restarting
- **Instance control**: restart individual instances

Access at `http://127.0.0.1:8384` (default).

---

## Server-Side: Multi-Domain Setup

The `slipstream-server` natively supports **multiple domains on a single instance**. You do NOT need multiple server processes.

### Setup

1. **Run one slipstream-server** with multiple `--domain` flags:

```bash
./slipstream-server \
    --domain t1.example.com \
    --domain t2.example.com \
    --domain t3.example.com \
    --dns-listen-port 53 \
    --target-address 127.0.0.1:5201 \
    --cert cert.pem \
    --key key.pem \
    --reset-seed ./reset-seed
```

2. **Configure DNS**: Point all subdomain NS records to your server's IP address:

```
t1.example.com.  NS  ns1.example.com.
t2.example.com.  NS  ns1.example.com.
t3.example.com.  NS  ns1.example.com.
ns1.example.com. A   YOUR_SERVER_IP
```

3. **Tune conntrack** (recommended for production):

```bash
sysctl -w net.netfilter.nf_conntrack_max=262144
sysctl -w net.netfilter.nf_conntrack_udp_timeout=15
sysctl -w net.netfilter.nf_conntrack_udp_timeout_stream=60
```

### Why only one server instance?

Since recursive DNS resolvers query port 53 of the NS for a subdomain, and `slipstream-server` matches QNAMEs by longest suffix, a single process on port 53 handles **all** domains. No multi-instance needed.

---

## Developer

<p align="center">
  Developed by <a href="https://github.com/ParsaKSH">ParsaKSH</a><br>
  <a href="https://github.com/ParsaKSH/slipstream-plus">https://github.com/ParsaKSH/slipstream-plus</a>
</p>

## License

Apache-2.0

---
---

<div dir="rtl">

# اسلیپ‌استریم‌پلاس

<p align="center">
  <b>لودبالانسر چند‌نخی تونل DNS</b><br>
  یک wrapper پرفورمنس بالا به زبان Go برای <a href="https://github.com/Mygod/slipstream-rust">slipstream-rust</a>
</p>

---

## اسلیپ‌استریم‌پلاس چیست؟

اسلیپ‌استریم‌پلاس **چندین اینستنس slipstream-client** را پشت یک پروکسی TCP با لودبالانسینگ، مانیتورینگ سلامت و ریستارت خودکار مدیریت می‌کند. چندین تونل DNS را به یک اتصال قابل‌اعتماد و پرسرعت تبدیل می‌کند.

### ویژگی‌ها

- 🔀 **لودبالانسینگ** — استراتژی‌های round-robin، random، least-ping و least-load
- 🔄 **ریستارت خودکار** — اینستنس‌هایی که crash می‌کنند با backoff نمایی خودکار ریستارت می‌شوند
- 📊 **مانیتورینگ سلامت** — بررسی دوره‌ای تأخیر از طریق هر تونل
- 🎯 **تکرار (Replicas)** — اجرای N کپی از یک اینستنس با رنج پورت
- 🖥️ **رابط وب** — داشبورد بلادرنگ با تم تاریک برای مانیتورینگ و تنظیمات
- 📦 **باینری تعبیه‌شده** — اختیاری: slipstream-client را داخل باینری Go قرار دهید

---

## نصب

### پیش‌نیازها

- Go نسخه 1.21 به بالا
- Rust toolchain (برای بیلد slipstream-rust)
- cmake, pkg-config, libssl-dev

### بیلد

```bash
# کلون
git clone https://github.com/ParsaKSH/slipstream-plus.git
cd slipstream-plus

# بیلد slipstream-rust
cd orgslipstream/slipstream-rust
git submodule update --init --recursive
cargo build --release -p slipstream-client -p slipstream-server
cd ../..

# بیلد SlipstreamPlus
CGO_ENABLED=0 go build -o slipstreamplus ./cmd/slipstreamplus/
```

### بیلد با باینری تعبیه‌شده

```bash
cp orgslipstream/slipstream-rust/target/release/slipstream-client internal/embedded/
CGO_ENABLED=0 go build -tags embed_slipstream -o slipstreamplus ./cmd/slipstreamplus/
```

---

## استفاده

```bash
# استفاده ساده
./slipstreamplus -config config.json

# با رابط وب
./slipstreamplus -config config.json -gui

# با پورت سفارشی رابط وب
./slipstreamplus -config config.json -gui -gui-listen 0.0.0.0:9090
```

---

## پیکربندی

### مثال کامل (`config.json`)

```json
{
    "socks": {
        "listen": "0.0.0.0:1080",
        "buffer_size": 65536,
        "max_connections": 10000
    },
    "slipstream_binary": "./orgslipstream/slipstream-rust/target/release/slipstream-client",
    "strategy": "round_robin",
    "health_check": {
        "interval": "10s",
        "target": "google.com",
        "timeout": "5s"
    },
    "gui": {
        "enabled": false,
        "listen": "127.0.0.1:8384"
    },
    "instances": [
        {
            "domain": "t1.example.com",
            "resolver": "8.8.8.8:53",
            "port": 17001,
            "replicas": 1,
            "authoritative": false
        },
        {
            "domain": "t2.example.com",
            "resolver": "1.1.1.1:53",
            "port": "17002-17005",
            "replicas": 4,
            "authoritative": false
        }
    ]
}
```

### تکرار و رنج پورت

وقتی `replicas` بزرگتر از 1 باشد، یک رنج پورت بدهید:

```json
{
    "domain": "tunnel.example.com",
    "resolver": "8.8.8.8:53",
    "port": "17001-17004",
    "replicas": 4
}
```

این 4 اینستنس slipstream-client روی پورت‌های 17001 تا 17004 اجرا می‌کند — همه با دامنه و ریزالور یکسان.

### استراتژی‌های لودبالانسینگ

| استراتژی | توضیح |
|----------|-------|
| `round_robin` | هر کانکشن جدید به ترتیب به اینستنس بعدی می‌رود |
| `random` | انتخاب تصادفی |
| `least_ping` | ارسال به اینستنس با کمترین تأخیر |
| `least_load` | ارسال به اینستنس با کمترین کانکشن فعال |

---

## سمت سرور: استفاده از چندین دامنه

`slipstream-server` بصورت بومی از **چندین دامنه روی یک اینستنس** پشتیبانی می‌کند. نیازی به اجرای چند پروسه سرور ندارید.

### راه‌اندازی

1. **یک slipstream-server** با چندین فلگ `--domain` اجرا کنید:

```bash
./slipstream-server \
    --domain t1.example.com \
    --domain t2.example.com \
    --domain t3.example.com \
    --dns-listen-port 53 \
    --target-address 127.0.0.1:5201 \
    --cert cert.pem \
    --key key.pem
```

2. **تنظیم DNS**: رکوردهای NS ساب‌دامنه‌ها را به آیپی سرور خود ارجاع دهید:

```
t1.example.com.  NS  ns1.example.com.
t2.example.com.  NS  ns1.example.com.
t3.example.com.  NS  ns1.example.com.
ns1.example.com. A   آیپی_سرور_شما
```

3. **تنظیم conntrack** (پیشنهاد شده برای پروداکشن):

```bash
sysctl -w net.netfilter.nf_conntrack_max=262144
sysctl -w net.netfilter.nf_conntrack_udp_timeout=15
sysctl -w net.netfilter.nf_conntrack_udp_timeout_stream=60
```

### چرا فقط یک اینستنس سرور کافی است؟

ریکورسیو DNS ها فقط پورت 53 NS مربوط به هر ساب‌دامنه را کوئری می‌زنند. `slipstream-server` ورودی‌ها را بر اساس longest suffix match تطبیق می‌دهد، پس یک پروسه روی پورت 53 **تمام** دامنه‌ها را هندل می‌کند.

---

## توسعه‌دهنده

<p align="center">
  توسعه داده شده توسط <a href="https://github.com/ParsaKSH">ParsaKSH</a><br>
  <a href="https://github.com/ParsaKSH/slipstream-plus">https://github.com/ParsaKSH/slipstream-plus</a>
</p>

</div>
