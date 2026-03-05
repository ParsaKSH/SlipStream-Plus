package gui

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/slipstreamplus/slipstreamplus/internal/config"
	"github.com/slipstreamplus/slipstreamplus/internal/engine"
)

type APIServer struct {
	manager    *engine.Manager
	cfg        *config.Config
	configPath string
	listenAddr string
}

func NewAPIServer(mgr *engine.Manager, cfg *config.Config, configPath string) *APIServer {
	return &APIServer{
		manager:    mgr,
		cfg:        cfg,
		configPath: configPath,
		listenAddr: cfg.GUI.Listen,
	}
}

func (s *APIServer) Start() error {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/instance/", s.handleInstance)

	// Serve the embedded HTML dashboard
	mux.HandleFunc("/", s.handleDashboard)

	log.Printf("[gui] web dashboard available at http://%s", s.listenAddr)

	go func() {
		if err := http.ListenAndServe(s.listenAddr, mux); err != nil {
			log.Printf("[gui] server error: %v", err)
		}
	}()

	return nil
}

func (s *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	status := s.manager.StatusAll()
	json.NewEncoder(w).Encode(map[string]any{
		"instances": status,
		"strategy":  s.cfg.Strategy,
		"socks":     s.cfg.Socks.Listen,
	})
}

func (s *APIServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method == "GET" {
		json.NewEncoder(w).Encode(s.cfg)
		return
	}

	if r.Method == "POST" {
		var newCfg config.Config
		if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
			http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
			return
		}
		if err := newCfg.Validate(); err != nil {
			http.Error(w, fmt.Sprintf("validation error: %v", err), http.StatusBadRequest)
			return
		}
		if err := newCfg.Save(s.configPath); err != nil {
			http.Error(w, fmt.Sprintf("save error: %v", err), http.StatusInternalServerError)
			return
		}
		*s.cfg = newCfg
		json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
		return
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (s *APIServer) handleInstance(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Parse /api/instance/{id}/restart
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[3] != "restart" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	id, err := strconv.Atoi(parts[2])
	if err != nil {
		http.Error(w, "invalid instance id", http.StatusBadRequest)
		return
	}

	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := s.manager.RestartInstance(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "restarting"})
}

func (s *APIServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>SlipstreamPlus Dashboard</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700&display=swap" rel="stylesheet">
<style>
*{margin:0;padding:0;box-sizing:border-box}
:root{
  --bg:#0a0a0f;--bg2:#12121a;--bg3:#1a1a2e;--bg4:#252540;
  --accent:#6c63ff;--accent2:#a78bfa;--accent-glow:rgba(108,99,255,0.3);
  --green:#10b981;--red:#ef4444;--yellow:#f59e0b;--blue:#3b82f6;
  --text:#e2e8f0;--text2:#94a3b8;--text3:#64748b;
  --border:rgba(255,255,255,0.06);--glass:rgba(255,255,255,0.03);
  --radius:16px;--radius-sm:10px;
}
body{font-family:'Inter',system-ui,sans-serif;background:var(--bg);color:var(--text);min-height:100vh;overflow-x:hidden}
.noise{position:fixed;top:0;left:0;width:100%;height:100%;opacity:0.015;pointer-events:none;
  background-image:url("data:image/svg+xml,%3Csvg viewBox='0 0 256 256' xmlns='http://www.w3.org/2000/svg'%3E%3Cfilter id='noise'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='0.9' numOctaves='4' stitchTiles='stitch'/%3E%3C/filter%3E%3Crect width='100%25' height='100%25' filter='url(%23noise)'/%3E%3C/svg%3E")}
.glow-orb{position:fixed;border-radius:50%;filter:blur(120px);opacity:0.08;pointer-events:none;z-index:0}
.glow-orb.purple{width:600px;height:600px;background:#6c63ff;top:-200px;right:-100px}
.glow-orb.blue{width:400px;height:400px;background:#3b82f6;bottom:-100px;left:-100px}
.container{max-width:1200px;margin:0 auto;padding:24px;position:relative;z-index:1}
header{display:flex;justify-content:space-between;align-items:center;margin-bottom:32px;padding:20px 28px;
  background:var(--glass);backdrop-filter:blur(20px);border:1px solid var(--border);border-radius:var(--radius)}
header h1{font-size:22px;font-weight:700;background:linear-gradient(135deg,var(--accent),var(--accent2));
  -webkit-background-clip:text;-webkit-text-fill-color:transparent}
header .subtitle{font-size:13px;color:var(--text3);margin-top:2px}
.badge{display:inline-flex;align-items:center;gap:6px;padding:6px 14px;border-radius:20px;font-size:12px;font-weight:500}
.badge.running{background:rgba(16,185,129,0.1);color:var(--green);border:1px solid rgba(16,185,129,0.2)}
.badge.stopped{background:rgba(239,68,68,0.1);color:var(--red);border:1px solid rgba(239,68,68,0.2)}
.dot{width:7px;height:7px;border-radius:50%;animation:pulse 2s infinite}
.dot.green{background:var(--green)}
.dot.red{background:var(--red)}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:0.4}}
.stats-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:16px;margin-bottom:28px}
.stat-card{padding:20px 24px;background:var(--glass);backdrop-filter:blur(20px);border:1px solid var(--border);
  border-radius:var(--radius);transition:all 0.3s}
.stat-card:hover{border-color:rgba(108,99,255,0.2);box-shadow:0 0 30px var(--accent-glow)}
.stat-label{font-size:12px;color:var(--text3);text-transform:uppercase;letter-spacing:1px;margin-bottom:8px}
.stat-value{font-size:28px;font-weight:700;letter-spacing:-0.5px}
.stat-value.accent{color:var(--accent2)}
.section{margin-bottom:28px}
.section-header{display:flex;justify-content:space-between;align-items:center;margin-bottom:16px}
.section-title{font-size:16px;font-weight:600;color:var(--text)}
.btn{padding:8px 18px;border:1px solid var(--border);background:var(--bg3);color:var(--text);border-radius:var(--radius-sm);
  cursor:pointer;font-family:inherit;font-size:13px;font-weight:500;transition:all 0.2s}
.btn:hover{background:var(--bg4);border-color:var(--accent)}
.btn.primary{background:var(--accent);border-color:var(--accent);color:#fff}
.btn.primary:hover{background:#5b54e6;box-shadow:0 0 20px var(--accent-glow)}
.btn.sm{padding:5px 12px;font-size:11px}
.btn.danger{border-color:rgba(239,68,68,0.3);color:var(--red)}
.btn.danger:hover{background:rgba(239,68,68,0.1)}
table{width:100%;border-collapse:separate;border-spacing:0;background:var(--glass);backdrop-filter:blur(20px);
  border:1px solid var(--border);border-radius:var(--radius);overflow:hidden}
th{padding:14px 20px;text-align:left;font-size:11px;font-weight:600;color:var(--text3);text-transform:uppercase;
  letter-spacing:1px;background:rgba(255,255,255,0.02);border-bottom:1px solid var(--border)}
td{padding:14px 20px;font-size:13px;border-bottom:1px solid var(--border);transition:background 0.2s}
tr:last-child td{border-bottom:none}
tr:hover td{background:rgba(108,99,255,0.03)}
.state-badge{padding:3px 10px;border-radius:12px;font-size:11px;font-weight:600;text-transform:uppercase}
.state-badge.healthy{background:rgba(16,185,129,0.15);color:var(--green)}
.state-badge.starting{background:rgba(245,158,11,0.15);color:var(--yellow)}
.state-badge.unhealthy{background:rgba(239,68,68,0.15);color:var(--red)}
.state-badge.dead{background:rgba(100,116,139,0.15);color:var(--text3)}
.config-panel{padding:24px;background:var(--glass);backdrop-filter:blur(20px);border:1px solid var(--border);border-radius:var(--radius)}
.form-group{margin-bottom:18px}
.form-group label{display:block;font-size:12px;color:var(--text2);margin-bottom:6px;font-weight:500}
.form-group input,.form-group select,.form-group textarea{width:100%;padding:10px 14px;background:var(--bg2);
  border:1px solid var(--border);border-radius:var(--radius-sm);color:var(--text);font-family:inherit;font-size:13px;
  transition:border-color 0.2s}
.form-group input:focus,.form-group select:focus,.form-group textarea:focus{outline:none;border-color:var(--accent)}
.form-group textarea{min-height:200px;font-family:'SF Mono',Monaco,Consolas,monospace;font-size:12px;resize:vertical}
.form-row{display:grid;grid-template-columns:1fr 1fr;gap:16px}
.tabs{display:flex;gap:4px;margin-bottom:20px;background:var(--bg2);border-radius:var(--radius-sm);padding:4px;
  border:1px solid var(--border)}
.tab{padding:8px 20px;border:none;background:none;color:var(--text3);cursor:pointer;border-radius:8px;
  font-family:inherit;font-size:13px;font-weight:500;transition:all 0.2s}
.tab.active{background:var(--accent);color:#fff}
.tab:hover:not(.active){color:var(--text);background:var(--bg3)}
footer{text-align:center;padding:24px;margin-top:40px;border-top:1px solid var(--border)}
footer a{color:var(--accent2);text-decoration:none;font-weight:500;font-size:13px}
footer a:hover{text-decoration:underline}
.toast{position:fixed;bottom:24px;right:24px;padding:12px 20px;background:var(--green);color:#fff;border-radius:var(--radius-sm);
  font-size:13px;font-weight:500;opacity:0;transform:translateY(10px);transition:all 0.3s;pointer-events:none;z-index:100}
.toast.show{opacity:1;transform:translateY(0)}
.toast.error{background:var(--red)}
@media(max-width:768px){
  .stats-grid{grid-template-columns:1fr 1fr}
  .form-row{grid-template-columns:1fr}
  header{flex-direction:column;gap:12px;text-align:center}
}
</style>
</head>
<body>
<div class="noise"></div>
<div class="glow-orb purple"></div>
<div class="glow-orb blue"></div>
<div id="toast" class="toast"></div>

<div class="container">
  <header>
    <div>
      <h1>⚡ SlipstreamPlus</h1>
      <div class="subtitle">Multi-threaded DNS Tunnel Load Balancer</div>
    </div>
    <div id="header-badge" class="badge running"><span class="dot green"></span> Running</div>
  </header>

  <div class="stats-grid" id="stats-grid">
    <div class="stat-card"><div class="stat-label">Total Instances</div><div class="stat-value accent" id="s-total">-</div></div>
    <div class="stat-card"><div class="stat-label">Healthy</div><div class="stat-value" id="s-healthy" style="color:var(--green)">-</div></div>
    <div class="stat-card"><div class="stat-label">Active Connections</div><div class="stat-value accent" id="s-conns">-</div></div>
    <div class="stat-card"><div class="stat-label">Strategy</div><div class="stat-value" id="s-strategy" style="font-size:18px">-</div></div>
  </div>

  <div class="tabs">
    <button class="tab active" onclick="switchTab('instances')">Instances</button>
    <button class="tab" onclick="switchTab('config')">Configuration</button>
  </div>

  <div id="tab-instances" class="section">
    <div class="section-header">
      <div class="section-title">Instance Status</div>
    </div>
    <table>
      <thead><tr>
        <th>ID</th><th>Domain</th><th>Resolver</th><th>Port</th><th>State</th>
        <th>Connections</th><th>Latency</th><th>Actions</th>
      </tr></thead>
      <tbody id="instance-table"></tbody>
    </table>
  </div>

  <div id="tab-config" class="section" style="display:none">
    <div class="section-header">
      <div class="section-title">Configuration</div>
      <button class="btn primary" onclick="saveConfig()">💾 Save Config</button>
    </div>
    <div class="config-panel">
      <div class="form-row">
        <div class="form-group">
          <label>SOCKS Listen Address</label>
          <input type="text" id="cfg-socks-listen" placeholder="0.0.0.0:1080">
        </div>
        <div class="form-group">
          <label>Strategy</label>
          <select id="cfg-strategy">
            <option value="round_robin">Round Robin</option>
            <option value="random">Random</option>
            <option value="least_ping">Least Ping</option>
            <option value="least_load">Least Load</option>
          </select>
        </div>
      </div>
      <div class="form-row">
        <div class="form-group">
          <label>Buffer Size</label>
          <input type="number" id="cfg-buffer" placeholder="65536">
        </div>
        <div class="form-group">
          <label>Max Connections</label>
          <input type="number" id="cfg-maxconns" placeholder="10000">
        </div>
      </div>
      <div class="form-row">
        <div class="form-group">
          <label>Health Check Interval</label>
          <input type="text" id="cfg-health-interval" placeholder="10s">
        </div>
        <div class="form-group">
          <label>Health Check Target</label>
          <input type="text" id="cfg-health-target" placeholder="google.com">
        </div>
      </div>
      <div class="form-group">
        <label>Instances (JSON)</label>
        <textarea id="cfg-instances" placeholder='[{"domain":"...","resolver":"...","port":17001,"replicas":1}]'></textarea>
      </div>
    </div>
  </div>

  <footer>
    Developed by <a href="https://github.com/ParsaKSH/slipstream-plus" target="_blank">ParsaKSH</a> &mdash;
    <a href="https://github.com/ParsaKSH/slipstream-plus" target="_blank">GitHub Repository</a>
  </footer>
</div>

<script>
let currentConfig = null;

function switchTab(tab) {
  document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
  document.getElementById('tab-instances').style.display = tab === 'instances' ? 'block' : 'none';
  document.getElementById('tab-config').style.display = tab === 'config' ? 'block' : 'none';
  event.target.classList.add('active');
  if (tab === 'config' && currentConfig) populateConfigForm(currentConfig);
}

function toast(msg, isError) {
  const t = document.getElementById('toast');
  t.textContent = msg;
  t.className = 'toast show' + (isError ? ' error' : '');
  setTimeout(() => t.className = 'toast', 3000);
}

async function fetchStatus() {
  try {
    const r = await fetch('/api/status');
    const data = await r.json();
    updateDashboard(data);
  } catch(e) {
    document.getElementById('header-badge').innerHTML = '<span class="dot red"></span> Disconnected';
    document.getElementById('header-badge').className = 'badge stopped';
  }
}

function updateDashboard(data) {
  const instances = data.instances || [];
  const healthy = instances.filter(i => i.state === 'healthy').length;
  const totalConns = instances.reduce((s, i) => s + i.active_conns, 0);

  document.getElementById('s-total').textContent = instances.length;
  document.getElementById('s-healthy').textContent = healthy;
  document.getElementById('s-conns').textContent = totalConns;
  document.getElementById('s-strategy').textContent = (data.strategy || '').replace('_', ' ');
  document.getElementById('header-badge').innerHTML = '<span class="dot green"></span> Running';
  document.getElementById('header-badge').className = 'badge running';

  const tbody = document.getElementById('instance-table');
  tbody.innerHTML = instances.map(i => {
    const ping = i.last_ping_ms > 0 ? i.last_ping_ms + 'ms' : '—';
    return '<tr>' +
      '<td>#' + i.id + '</td>' +
      '<td style="font-weight:500">' + esc(i.domain) + '</td>' +
      '<td style="color:var(--text2)">' + esc(i.resolver) + '</td>' +
      '<td>' + i.port + '</td>' +
      '<td><span class="state-badge ' + i.state + '">' + i.state + '</span></td>' +
      '<td>' + i.active_conns + '</td>' +
      '<td>' + ping + '</td>' +
      '<td><button class="btn sm danger" onclick="restartInstance(' + i.id + ')">↻ Restart</button></td>' +
      '</tr>';
  }).join('');
}

function esc(s) { const d = document.createElement('div'); d.textContent = s; return d.innerHTML; }

async function restartInstance(id) {
  try {
    await fetch('/api/instance/' + id + '/restart', {method: 'POST'});
    toast('Instance #' + id + ' restarting...');
  } catch(e) { toast('Failed to restart', true); }
}

async function loadConfig() {
  try {
    const r = await fetch('/api/config');
    currentConfig = await r.json();
    populateConfigForm(currentConfig);
  } catch(e) { toast('Failed to load config', true); }
}

function populateConfigForm(cfg) {
  document.getElementById('cfg-socks-listen').value = cfg.socks?.listen || '';
  document.getElementById('cfg-strategy').value = cfg.strategy || 'round_robin';
  document.getElementById('cfg-buffer').value = cfg.socks?.buffer_size || 65536;
  document.getElementById('cfg-maxconns').value = cfg.socks?.max_connections || 10000;
  document.getElementById('cfg-health-interval').value = cfg.health_check?.interval || '10s';
  document.getElementById('cfg-health-target').value = cfg.health_check?.target || 'google.com';
  document.getElementById('cfg-instances').value = JSON.stringify(cfg.instances || [], null, 2);
}

async function saveConfig() {
  try {
    let instances;
    try { instances = JSON.parse(document.getElementById('cfg-instances').value); }
    catch(e) { toast('Invalid instances JSON', true); return; }

    const cfg = {
      socks: {
        listen: document.getElementById('cfg-socks-listen').value,
        buffer_size: parseInt(document.getElementById('cfg-buffer').value) || 65536,
        max_connections: parseInt(document.getElementById('cfg-maxconns').value) || 10000
      },
      slipstream_binary: currentConfig.slipstream_binary,
      strategy: document.getElementById('cfg-strategy').value,
      health_check: {
        interval: document.getElementById('cfg-health-interval').value,
        target: document.getElementById('cfg-health-target').value,
        timeout: currentConfig.health_check?.timeout || '5s'
      },
      gui: currentConfig.gui || {},
      instances: instances
    };

    const r = await fetch('/api/config', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(cfg)
    });

    if (!r.ok) { const t = await r.text(); toast(t, true); return; }
    currentConfig = cfg;
    toast('Config saved! Restart to apply changes.');
  } catch(e) { toast('Failed to save config', true); }
}

fetchStatus();
loadConfig();
setInterval(fetchStatus, 2000);
</script>
</body>
</html>`
