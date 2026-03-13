# SlipStream-Plus ⚡

[English](README-en.md)

اسلیپ‌استریم-پلاس یک **توزیع‌کننده بار (Load Balancer)** و **پنل مدیریت پیشرفته** برای تانل‌های DNS مبتنی بر هسته قدرتمند [Slipstream-Rust](https://github.com/mmb0/slipstream-rust) است. این نرم‌افزار به صورت یکپارچه (Embedded) همراه با هسته کامپایل می‌شود و امکاناتی نظیر مدیریت اتصال، تعریف چند کاربر، پنل وب مدرن و مانیتورینگ مصرف پهنای باند را فراهم می‌کند.

این پروژه این امکان را میدهد که همزمان از چندین کانفیگ SlipStream استفاده کنید، که در این صورت بار بین اینستنس های شما تقسیم شده و کیفیت کانکشن نهایی شما افزایش می‌یابد.

از این هسته میتوانید هم در سرور هایی که به ریزالور ها دسترسی دارند استفاده کنید، و هم در دستگاه های شخصی خود.

---

<details>
<summary><b>🔥 ویژگی‌های کلیدی</b></summary>

- **مدیریت چندگانه (Multi-Threading):** اجرای همزمان چندین نمونه سرور DNS با قابلیت توزیع بار (Round-Robin, Random, Least Load, Least Ping).
- **اتصال همزمان SOCKS5 و SSH:** پشتیبانی از هر دو حالت پروکسی و فوروارد پورت.
- **بررسی سلامت هوشمند (Smart Health Checks):** تایید دوره ای زنده بودن تانل‌ها و حذف خودکار سرورهای مرده از چرخه.
- **احراز هویت کاربران:** کنترل دسترسی و تعریف نامحدود کاربر SOCKS5 به همراه سیستم سهمیه‌بندی.
- **محدودسازی دقیق:** اعمال محدودیت حجم (GB/MB)، سرعت (Limit Bandwidth)، و محدودیت اتصال همزمان برای هر کاربر (IP Limit).
- **مانیتورینگ پیشرفته:** نمودار لحظه‌ای مصرف ترافیک و پهنای باند سرورها.
</details>

<details>
<summary><b>🌐 پنل مدیریت وب</b></summary>

SlipStream-Plus دارای یک پنل وب با معماری بسیار سبک و مدرن (طراحی تاریک و واکنش‌گرا) برای کنترل کامل برنامه در حین اجراست.

**امکانات پنل مرورگر:**
- **🎛️ داشبورد:** مشاهده وضعیت کلی اتصال‌ها، کاربران سالم، و پهنای‌باند.
- **🛰️ تب Instances (سرورها):** 
  - اضافه، ویرایش و حذف نمونه‌های جدید Slipstream در حال اجرا روی سرور.
  - پشتیبانی کامل از تغییر SOCKS، SSH، Certificate، Replica، و Authoritative.
  - دکمه ری‌استارت لحظه‌ای هر سرور برای اعمال تنظیمات.
- **👥 تب Users (کاربران):** 
  - تعریف ضرب‌الاجلی یوزر و پسورد SOCKS5.
  - تعیین محدودیت حجم، پهنای باند، محدودیت IP به شکل گرافیکی.
  - نمایش میزان ترافیک مصرف شده با Progress Bar و قابلیت ریست مصرف هر فرد.
- **📊 تب Bandwidth:** نمودار گرافیکی تفکیک‌شده آپلود (TX) و دانلود (RX) طی 24 ساعت گذشته.
- **⚙️ تب Config:** تغییر استراتژی لودبالانسر بدون خاموش شدن سرور + دکمه **Save & Apply** که باعث اعمال کامل تنظیمات، مدیریت Health Checker ها و راه‌اندازی نرم پروسس‌ها می‌شود. همچنین دکمه **Restart** برای ری‌استارت شبیه‌ساز `Ctrl+C` تعبیه شده.
</details>

<details>
<summary><b>🚀 نصب و راه‌اندازی (لینوکس و ویندوز)</b></summary>

شما به راحتی می‌توانید از فایل‌های پیش‌ساخته در سربرگ [Releases](../../releases) مخزن گیت‌هاب استفاده کنید. 
این فایل‌ها شامل هسته کامپایل‌شده Rust درون خودشون (Embedded) هستند، بنابراین فقط کافیست یک فایل را اجرا کنید:

```bash
# دانلود آخرین نسخه لینوکس
wget https://github.com/ParsaKSH/SlipStream-Plus/releases/latest/download/slipstreamplus-linux-amd64
chmod +x slipstreamplus-linux-amd64

# اجرای سرور با فایل کانفیگ
./slipstreamplus-linux-amd64 -config config.json
```

**کامپایل دستی (توسعه‌دهندگان):**
```bash
git clone https://github.com/ParsaKSH/SlipStream-Plus
cd SlipStream-Plus

# بیلد برای لینوکس (شامل کلاینت رصد شده در slipstreamorg)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags embed_slipstream -ldflags="-s -w" -o slipstreamplus-linux-amd64 ./cmd/slipstreamplus

# بیلد برای ویندوز
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags embed_slipstream -ldflags="-s -w" -o slipstreamplus-windows-amd64.exe ./cmd/slipstreamplus
```
</details>

<details>
<summary><b>📝 نمونه فایل تنظیمات (config.json)</b></summary>

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
<summary><b>🔬 توضیحات فنی و معماری پیشرفته</b></summary>

پروژه **SlipStream-Plus** فراتر از یک رابط مدیریتی ساده برای سرویس‌گیرنده Rust عمل می‌کند. هسته این سیستم بر مبنای یک ساختار هم‌زمان قدرتمند (Goroutine-based Concurrency) طراحی شده تا نهایت بهره‌وری را در شبکه‌های محدود ارائه دهد.

**معماری لود بالانسر و پراکسی پایپلاین:**
هنگامی که کاربری به پورت SOCKS5 یا SSH محلی متصل می‌شود، درخواست وی از یک هندشیک (Handshake) بهینه‌شده عبور کرده و یک مسیر (Session) با کلاینت برقرار می‌کند. برای جلوگیری از تاخیر اضافی (Latency Overhead)، درخواست‌های SOCKS Greeting و CONNECT در قالب پردازش خط‌لوله‌ای (Pipelining) ادغام شده و از افزایش زمان رفت‌وبرگشت در تانل DNS (Round-Trip Time) جلوگیری می‌شود.

**مدیریت پردازش‌ها (Process Supervisor):**
تمام نمونه‌های در حال اجرای کلاینت Rust به‌صورت زنده توسط Supervisor داخلی مانیتور می‌شوند. هرگاه به هر دلیلی هسته‌ای دچار اختلال یا قطعی شود (مانند مشکلات شبکه)، SlipStream-Plus بلافاصله آن پروسه را خاتمه داده و اتصال جایگزین (Replica) را بازتولید می‌کند که این پروسه بدون ایجاد وقفه ملموس برای کاربر رخ می‌دهد.

**مکانیزم کنترل پهنای باند و نشست‌ها:**
کنترل نشست هر کاربر (Active IPs Tracking) بر اساس ترکیبی از یک مکانیسم Map Counter همراه با Cooldown Time (زمان انتظار خاموشی) پیاده‌سازی شده تا از اتمام ظاهری ظرفیت در صورت ناپایداری‌های پروتکل DNS جلوگیری کند. محدودکننده‌های ترافیک (Bandwidth Shapers) با استفاده از روش Token Bucket (نرخ تطبیقی سایز Burst) بسته‌های اطلاعاتی را توزیع کرده و مانع مصرف انفجاری سیستم در سرورهایی با منابع محدود می‌شوند.
</details>

---

## ❤️ حمایت مالی از پروژه

اگر پروژه برای شما مفید بود، برای حمایت مالی می‌توانید از آدرس‌های زیر استفاده کنید:

| ارز | آدرس والت |
|-------|------------|
| **Tron** | `TD3vY9Drpo3eLi8z2LtGT9Vp4ESuF2AEgo` |
| **USDT(ERC20)** | `0x800680F566A394935547578bc5599D98B139Ea22` |
| **TON** | `UQAm3obHuD5kWf4eE4JmAO_5rkQdZPhaEpmRWs6Rk8vGQJog` |
| **BTC** | `bc1qaquv5vg35ua7qnd3wlueytw0fugpn8qkkuq9r2` |

<a href="https://nowpayments.io/donation?api_key=FH429FA-35N4AGZ-MFMRQ3Q-2H4BF98" target="_blank" rel="noreferrer noopener">
    <img src="https://nowpayments.io/images/embeds/donation-button-white.svg" width="200" alt="Crypto donation button by NOWPayments">
</a>

از حمایت شما ممنونم 

---

## ⭐️ ستاره دادن به پروژه

اگر این پروژه برایتان مفید بود، خوشحال می‌شوم به آن ستاره بدهید. این باعث می‌شود افراد بیشتری از آن استفاده کنند.

---

توسعه داده شده به کمک claude opus 4.6

به امید سربلندی ایران آباد... 
با آرزوی پیروزی برای شما 🚀✨
