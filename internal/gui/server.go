package gui

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/config"
	"github.com/ParsaKSH/SlipStream-Plus/internal/engine"
)

type APIServer struct {
	manager    *engine.Manager
	cfg        *config.Config
	configPath string
	listenAddr string

	bwMu      sync.RWMutex
	bwHistory []bwPoint // bandwidth history for daily graph
	lastTx    int64
	lastRx    int64
}

type bwPoint struct {
	Time int64 `json:"t"`
	Tx   int64 `json:"tx"` // bytes/sec
	Rx   int64 `json:"rx"` // bytes/sec
}

func NewAPIServer(mgr *engine.Manager, cfg *config.Config, configPath string) *APIServer {
	s := &APIServer{
		manager:    mgr,
		cfg:        cfg,
		configPath: configPath,
		listenAddr: cfg.GUI.Listen,
		bwHistory:  make([]bwPoint, 0, 8640),
	}
	go s.collectBandwidth()
	return s
}

func (s *APIServer) collectBandwidth() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		instances := s.manager.AllInstances()
		var totalTx, totalRx int64
		for _, inst := range instances {
			totalTx += inst.TxBytes()
			totalRx += inst.RxBytes()
		}

		s.bwMu.Lock()
		txRate := (totalTx - s.lastTx) / 10
		rxRate := (totalRx - s.lastRx) / 10
		s.lastTx = totalTx
		s.lastRx = totalRx

		s.bwHistory = append(s.bwHistory, bwPoint{
			Time: time.Now().Unix(),
			Tx:   txRate,
			Rx:   rxRate,
		})
		// Keep last 24 hours (10s intervals = 8640 points)
		if len(s.bwHistory) > 8640 {
			s.bwHistory = s.bwHistory[len(s.bwHistory)-8640:]
		}
		s.bwMu.Unlock()
	}
}

func (s *APIServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/reload", s.handleReload)
	mux.HandleFunc("/api/bandwidth", s.handleBandwidth)
	mux.HandleFunc("/api/instance/", s.handleInstance)
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

func (s *APIServer) handleBandwidth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	s.bwMu.RLock()
	data := make([]bwPoint, len(s.bwHistory))
	copy(data, s.bwHistory)
	s.bwMu.RUnlock()

	json.NewEncoder(w).Encode(data)
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

func (s *APIServer) handleReload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	newCfg, err := config.Load(s.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("load config: %v", err), http.StatusBadRequest)
		return
	}
	if newCfg.SlipstreamBinary == "" {
		newCfg.SlipstreamBinary = s.cfg.SlipstreamBinary
	}
	if err := s.manager.Reload(newCfg); err != nil {
		http.Error(w, fmt.Sprintf("reload: %v", err), http.StatusInternalServerError)
		return
	}
	*s.cfg = *newCfg
	log.Printf("[gui] config reloaded and instances restarted")
	json.NewEncoder(w).Encode(map[string]string{"status": "reloaded"})
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
<style>
*{margin:0;padding:0;box-sizing:border-box}
:root{
  --bg:#0b0d11;--bg2:#141720;--bg3:#1c2030;--bg4:#262b3d;
  --accent:#7c6cff;--accent2:#a78bfa;--accent-glow:rgba(124,108,255,0.25);
  --green:#22c55e;--red:#ef4444;--yellow:#f59e0b;--blue:#3b82f6;--cyan:#06b6d4;
  --text:#e8eaf0;--text2:#8b95a8;--text3:#5a6478;
  --border:rgba(255,255,255,0.07);--glass:rgba(255,255,255,0.04);
  --r:14px;--rs:8px;
}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:var(--bg);color:var(--text);min-height:100vh}
.container{max-width:1280px;margin:0 auto;padding:20px}
header{display:flex;justify-content:space-between;align-items:center;margin-bottom:24px;padding:16px 24px;background:var(--glass);border:1px solid var(--border);border-radius:var(--r)}
header h1{font-size:20px;font-weight:700;background:linear-gradient(135deg,var(--accent),var(--cyan));-webkit-background-clip:text;-webkit-text-fill-color:transparent}
header .sub{font-size:12px;color:var(--text3);margin-top:2px}
.badge{display:inline-flex;align-items:center;gap:5px;padding:5px 12px;border-radius:16px;font-size:11px;font-weight:600}
.badge.on{background:rgba(34,197,94,0.12);color:var(--green);border:1px solid rgba(34,197,94,0.2)}
.dot{width:6px;height:6px;border-radius:50%;background:currentColor;animation:pulse 2s infinite}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.3}}
.grid4{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:14px;margin-bottom:24px}
.card{padding:16px 20px;background:var(--glass);border:1px solid var(--border);border-radius:var(--r);transition:border-color .2s}
.card:hover{border-color:rgba(124,108,255,0.2)}
.card .lbl{font-size:11px;color:var(--text3);text-transform:uppercase;letter-spacing:.8px;margin-bottom:6px}
.card .val{font-size:24px;font-weight:700;letter-spacing:-.5px}
.card .val.a{color:var(--accent2)}
.card .val.g{color:var(--green)}
.card .val.c{color:var(--cyan)}
.tabs{display:flex;gap:3px;margin-bottom:18px;background:var(--bg2);border-radius:var(--rs);padding:3px;border:1px solid var(--border)}
.tab{padding:7px 18px;border:none;background:none;color:var(--text3);cursor:pointer;border-radius:6px;font:500 12px inherit;transition:.2s}
.tab.active{background:var(--accent);color:#fff}
.tab:hover:not(.active){color:var(--text);background:var(--bg3)}
.panel{padding:20px;background:var(--glass);border:1px solid var(--border);border-radius:var(--r)}
table{width:100%;border-collapse:collapse}
th{padding:10px 14px;text-align:left;font-size:10px;font-weight:600;color:var(--text3);text-transform:uppercase;letter-spacing:.8px;border-bottom:1px solid var(--border)}
td{padding:10px 14px;font-size:12px;border-bottom:1px solid var(--border)}
tr:last-child td{border-bottom:none}
tr:hover td{background:rgba(124,108,255,0.03)}
.sb{padding:2px 8px;border-radius:10px;font-size:10px;font-weight:700;text-transform:uppercase}
.sb.healthy{background:rgba(34,197,94,0.15);color:var(--green)}
.sb.starting{background:rgba(245,158,11,0.15);color:var(--yellow)}
.sb.unhealthy{background:rgba(239,68,68,0.15);color:var(--red)}
.sb.dead{background:rgba(90,100,120,0.15);color:var(--text3)}
.btn{padding:6px 14px;border:1px solid var(--border);background:var(--bg3);color:var(--text);border-radius:var(--rs);cursor:pointer;font:500 11px inherit;transition:.15s}
.btn:hover{background:var(--bg4);border-color:var(--accent)}
.btn.pri{background:var(--accent);border-color:var(--accent);color:#fff}
.btn.pri:hover{background:#6b5ce6;box-shadow:0 0 16px var(--accent-glow)}
.btn.grn{border-color:rgba(34,197,94,0.3);color:var(--green)}
.btn.grn:hover{background:rgba(34,197,94,0.1)}
.btn.red{border-color:rgba(239,68,68,0.3);color:var(--red)}
.btn.red:hover{background:rgba(239,68,68,0.1)}
.btn.sm{padding:4px 10px;font-size:10px}
.form-group{margin-bottom:14px}
.form-group label{display:block;font-size:11px;color:var(--text2);margin-bottom:4px;font-weight:500}
.form-group input,.form-group select,.form-group textarea{width:100%;padding:8px 12px;background:var(--bg2);border:1px solid var(--border);border-radius:var(--rs);color:var(--text);font:13px inherit}
.form-group input:focus,.form-group select:focus,.form-group textarea:focus{outline:none;border-color:var(--accent)}
.form-group textarea{min-height:180px;font:11px 'SF Mono',Monaco,Consolas,monospace;resize:vertical}
.form-row{display:grid;grid-template-columns:1fr 1fr;gap:12px}
canvas{width:100%;height:200px;border-radius:var(--rs);background:var(--bg2);border:1px solid var(--border)}
.toast{position:fixed;bottom:20px;right:20px;padding:10px 18px;background:var(--green);color:#fff;border-radius:var(--rs);font:500 12px inherit;opacity:0;transform:translateY(8px);transition:.3s;pointer-events:none;z-index:99}
.toast.show{opacity:1;transform:translateY(0)}.toast.error{background:var(--red)}
.section-hdr{display:flex;justify-content:space-between;align-items:center;margin-bottom:12px}
.section-hdr h3{font-size:14px;font-weight:600}
@media(max-width:768px){.grid4{grid-template-columns:1fr 1fr}.form-row{grid-template-columns:1fr}header{flex-direction:column;gap:10px}}
</style>
</head>
<body>
<div id="toast" class="toast"></div>
<div class="container">
  <header>
    <div><h1>⚡ SlipstreamPlus</h1><div class="sub">Multi-threaded DNS Tunnel Load Balancer</div></div>
    <div class="badge on"><span class="dot"></span> Running</div>
  </header>

  <div class="grid4">
    <div class="card"><div class="lbl">Instances</div><div class="val a" id="s-total">-</div></div>
    <div class="card"><div class="lbl">Healthy</div><div class="val g" id="s-healthy">-</div></div>
    <div class="card"><div class="lbl">Connections</div><div class="val a" id="s-conns">-</div></div>
    <div class="card"><div class="lbl">Bandwidth ↑/↓</div><div class="val c" id="s-bw">-</div></div>
  </div>

  <div class="tabs">
    <button class="tab active" onclick="switchTab('instances',this)">Instances</button>
    <button class="tab" onclick="switchTab('graph',this)">Bandwidth</button>
    <button class="tab" onclick="switchTab('config',this)">Config</button>
  </div>

  <div id="tab-instances">
    <div class="panel">
      <table>
        <thead><tr>
          <th>ID</th><th>Domain</th><th>Port</th><th>Mode</th><th>State</th>
          <th>Conns</th><th>Ping</th><th>TX</th><th>RX</th><th></th>
        </tr></thead>
        <tbody id="tbl"></tbody>
      </table>
    </div>
  </div>

  <div id="tab-graph" style="display:none">
    <div class="panel">
      <div class="section-hdr"><h3>Bandwidth (last 24h)</h3></div>
      <canvas id="bw-canvas" height="200"></canvas>
      <div style="display:flex;gap:16px;margin-top:8px;justify-content:center;font-size:11px;color:var(--text2)">
        <span>🟢 Upload (TX)</span><span>🔵 Download (RX)</span>
      </div>
    </div>
  </div>

  <div id="tab-config" style="display:none">
    <div class="panel">
      <div class="section-hdr">
        <h3>Configuration</h3>
        <div style="display:flex;gap:6px">
          <button class="btn pri" onclick="saveConfig()">💾 Save</button>
          <button class="btn grn" onclick="saveAndApply()">🔄 Save & Apply</button>
        </div>
      </div>
      <div class="form-row">
        <div class="form-group"><label>SOCKS Listen</label><input id="cfg-listen" placeholder="0.0.0.0:1080"></div>
        <div class="form-group"><label>Strategy</label>
          <select id="cfg-strat"><option value="round_robin">Round Robin</option><option value="random">Random</option><option value="least_ping">Least Ping</option><option value="least_load">Least Load</option></select>
        </div>
      </div>
      <div class="form-row">
        <div class="form-group"><label>Buffer Size</label><input type="number" id="cfg-buf" placeholder="65536"></div>
        <div class="form-group"><label>Max Connections</label><input type="number" id="cfg-max" placeholder="10000"></div>
      </div>
      <div class="form-row">
        <div class="form-group"><label>Health Interval</label><input id="cfg-hi" placeholder="10s"></div>
        <div class="form-group"><label>Health Target</label><input id="cfg-ht" placeholder="google.com"></div>
      </div>
      <div class="form-group"><label>Instances (JSON)</label><textarea id="cfg-inst"></textarea></div>
    </div>
  </div>

  <div style="text-align:center; padding: 24px; margin-top: 32px; border-top: 1px solid var(--border); font-size: 12px; color: var(--text3);">
    Developed with ❤️ by ParsaKSH - <a href="https://github.com/ParsaKSH/SlipStream-Plus" target="_blank" style="color:var(--accent2); text-decoration:none;">Github Repository</a>
  </div>
</div>
<script>
let CC=null;
function switchTab(t,btn){
  document.querySelectorAll('.tab').forEach(b=>b.classList.remove('active'));
  btn.classList.add('active');
  ['instances','graph','config'].forEach(n=>{
    document.getElementById('tab-'+n).style.display=n===t?'block':'none';
  });
  if(t==='config'&&CC)fillForm(CC);
  if(t==='graph')loadBW();
}
function toast(m,e){const t=document.getElementById('toast');t.textContent=m;t.className='toast show'+(e?' error':'');setTimeout(()=>t.className='toast',3000)}
function fmt(b){if(b<1024)return b+'B';if(b<1048576)return(b/1024).toFixed(1)+'KB';if(b<1073741824)return(b/1048576).toFixed(1)+'MB';return(b/1073741824).toFixed(2)+'GB'}
function esc(s){const d=document.createElement('div');d.textContent=s;return d.innerHTML}

async function fetchStatus(){
  try{
    const r=await fetch('/api/status');
    const d=await r.json();
    const inst=d.instances||[];
    const h=inst.filter(i=>i.state==='healthy').length;
    const c=inst.reduce((s,i)=>s+i.active_conns,0);
    const tx=inst.reduce((s,i)=>s+i.tx_bytes,0);
    const rx=inst.reduce((s,i)=>s+i.rx_bytes,0);
    document.getElementById('s-total').textContent=inst.length;
    document.getElementById('s-healthy').textContent=h;
    document.getElementById('s-conns').textContent=c;
    document.getElementById('s-bw').textContent=fmt(tx)+' / '+fmt(rx);
    document.getElementById('s-bw').style.fontSize='16px';
    const tb=document.getElementById('tbl');
    tb.innerHTML=inst.map(i=>{
      const p=i.last_ping_ms>0?i.last_ping_ms+'ms':'—';
      const m=(i.mode||'socks').toUpperCase();
      const mc=i.mode==='ssh'?'var(--blue)':'var(--accent2)';
      return '<tr>'+
        '<td>#'+i.id+'</td>'+
        '<td style="font-weight:500">'+esc(i.domain)+'</td>'+
        '<td>'+i.port+'</td>'+
        '<td><span style="color:'+mc+';font-size:10px;font-weight:700">'+m+'</span></td>'+
        '<td><span class="sb '+i.state+'">'+i.state+'</span></td>'+
        '<td>'+i.active_conns+'</td>'+
        '<td>'+p+'</td>'+
        '<td style="color:var(--green);font-size:11px">'+fmt(i.tx_bytes)+'</td>'+
        '<td style="color:var(--cyan);font-size:11px">'+fmt(i.rx_bytes)+'</td>'+
        '<td><button class="btn sm red" onclick="restartInst('+i.id+')">↻</button></td>'+
        '</tr>';
    }).join('');
  }catch(e){}
}

async function restartInst(id){
  try{await fetch('/api/instance/'+id+'/restart',{method:'POST'});toast('Instance #'+id+' restarting...')}catch(e){toast('Failed',true)}
}

async function loadCfg(){
  try{const r=await fetch('/api/config');CC=await r.json();fillForm(CC)}catch(e){}
}
function fillForm(c){
  document.getElementById('cfg-listen').value=c.socks?.listen||'';
  document.getElementById('cfg-strat').value=c.strategy||'round_robin';
  document.getElementById('cfg-buf').value=c.socks?.buffer_size||65536;
  document.getElementById('cfg-max').value=c.socks?.max_connections||10000;
  document.getElementById('cfg-hi').value=c.health_check?.interval||'10s';
  document.getElementById('cfg-ht').value=c.health_check?.target||'google.com';
  document.getElementById('cfg-inst').value=JSON.stringify(c.instances||[],null,2);
}

async function saveConfig(){
  try{
    let inst;try{inst=JSON.parse(document.getElementById('cfg-inst').value)}catch(e){toast('Invalid JSON',true);return}
    const cfg={socks:{listen:document.getElementById('cfg-listen').value,buffer_size:parseInt(document.getElementById('cfg-buf').value)||65536,max_connections:parseInt(document.getElementById('cfg-max').value)||10000},slipstream_binary:CC?.slipstream_binary||'',strategy:document.getElementById('cfg-strat').value,health_check:{interval:document.getElementById('cfg-hi').value,target:document.getElementById('cfg-ht').value,timeout:CC?.health_check?.timeout||'5s'},gui:CC?.gui||{},instances:inst};
    const r=await fetch('/api/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(cfg)});
    if(!r.ok){toast(await r.text(),true);return}CC=cfg;toast('Config saved!')
  }catch(e){toast('Save failed',true)}
}

async function saveAndApply(){
  await saveConfig();
  try{
    const r=await fetch('/api/reload',{method:'POST'});
    if(!r.ok){toast(await r.text(),true);return}
    toast('Config saved & applied! Instances reloaded.')
  }catch(e){toast('Reload failed',true)}
}

async function loadBW(){
  try{
    const r=await fetch('/api/bandwidth');
    const data=await r.json();
    drawChart(data);
  }catch(e){}
}

function drawChart(data){
  const canvas=document.getElementById('bw-canvas');
  const ctx=canvas.getContext('2d');
  const dpr=window.devicePixelRatio||1;
  canvas.width=canvas.clientWidth*dpr;
  canvas.height=canvas.clientHeight*dpr;
  ctx.scale(dpr,dpr);
  const W=canvas.clientWidth,H=canvas.clientHeight;
  ctx.clearRect(0,0,W,H);

  if(!data||data.length<2){
    ctx.fillStyle='#5a6478';ctx.font='12px sans-serif';ctx.textAlign='center';
    ctx.fillText('Collecting data...',W/2,H/2);return;
  }

  const maxVal=Math.max(...data.map(d=>Math.max(d.tx,d.rx)),1);
  const pad={t:10,b:24,l:50,r:10};
  const gW=W-pad.l-pad.r,gH=H-pad.t-pad.b;

  // Grid lines
  ctx.strokeStyle='rgba(255,255,255,0.06)';ctx.lineWidth=1;
  for(let i=0;i<5;i++){
    const y=pad.t+gH*i/4;
    ctx.beginPath();ctx.moveTo(pad.l,y);ctx.lineTo(W-pad.r,y);ctx.stroke();
    ctx.fillStyle='#5a6478';ctx.font='9px sans-serif';ctx.textAlign='right';
    ctx.fillText(fmt(maxVal*(1-i/4))+'/s',pad.l-4,y+3);
  }

  function drawLine(color,key){
    ctx.beginPath();ctx.strokeStyle=color;ctx.lineWidth=1.5;
    data.forEach((d,i)=>{
      const x=pad.l+gW*i/(data.length-1);
      const y=pad.t+gH*(1-d[key]/maxVal);
      i===0?ctx.moveTo(x,y):ctx.lineTo(x,y);
    });
    ctx.stroke();
    // Fill
    ctx.lineTo(pad.l+gW,pad.t+gH);ctx.lineTo(pad.l,pad.t+gH);ctx.closePath();
    ctx.fillStyle=color.replace(')',',0.08)').replace('rgb','rgba');ctx.fill();
  }
  drawLine('rgb(34,197,94)','tx');
  drawLine('rgb(6,182,212)','rx');
}

fetchStatus();loadCfg();
setInterval(fetchStatus,2000);
setInterval(()=>{if(document.getElementById('tab-graph').style.display!=='none')loadBW()},10000);
</script>
</body>
</html>`
