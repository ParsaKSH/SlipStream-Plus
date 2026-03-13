# SlipStream-Plus ⚡

**لینک به نسخه انگلیسی (English README):** [README-en.md](README-en.md)

اسلیپ‌استریم-پلاس یک **توزیع‌کننده بار (Load Balancer)** و **پنل مدیریت پیشرفته** برای تانل‌های DNS مبتنی بر هسته قدرتمند [Slipstream-Rust](https://github.com/mmb0/slipstream-rust) است. این نرم‌افزار به صورت یکپارچه (Embedded) همراه با هسته کامپایل می‌شود و امکاناتی نظیر مدیریت اتصال، تعریف چند کاربر، پنل وب مدرن و مانیتورینگ مصرف پهنای باند را فراهم می‌کند.

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
توسعه یافته با ❤️ توسط [ParsaKSH](https://github.com/ParsaKSH).
