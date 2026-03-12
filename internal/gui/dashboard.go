package gui

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
header{display:flex;justify-content:space-between;align-items:center;margin-bottom:24px;padding:16px 24px;background:var(--glass);border:1px solid var(--border);border-radius:var(--r);flex-wrap:wrap;gap:10px}
header h1{font-size:20px;font-weight:700;background:linear-gradient(135deg,var(--accent),var(--cyan));-webkit-background-clip:text;-webkit-text-fill-color:transparent}
header .sub{font-size:12px;color:var(--text3);margin-top:2px}
.hdr-btns{display:flex;gap:6px;align-items:center}
.badge{display:inline-flex;align-items:center;gap:5px;padding:5px 12px;border-radius:16px;font-size:11px;font-weight:600}
.badge.on{background:rgba(34,197,94,0.12);color:var(--green);border:1px solid rgba(34,197,94,0.2)}
.dot{width:6px;height:6px;border-radius:50%;background:currentColor;animation:pulse 2s infinite}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.3}}
.grid4{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:14px;margin-bottom:24px}
.card{padding:16px 20px;background:var(--glass);border:1px solid var(--border);border-radius:var(--r);transition:border-color .2s}
.card:hover{border-color:rgba(124,108,255,0.2)}
.card .lbl{font-size:11px;color:var(--text3);text-transform:uppercase;letter-spacing:.8px;margin-bottom:6px}
.card .val{font-size:24px;font-weight:700;letter-spacing:-.5px}
.card .val.a{color:var(--accent2)}.card .val.g{color:var(--green)}.card .val.c{color:var(--cyan)}
.tabs{display:flex;gap:3px;margin-bottom:18px;background:var(--bg2);border-radius:var(--rs);padding:3px;border:1px solid var(--border);flex-wrap:wrap}
.tab{padding:7px 18px;border:none;background:none;color:var(--text3);cursor:pointer;border-radius:6px;font:500 12px inherit;transition:.2s}
.tab.active{background:var(--accent);color:#fff}
.tab:hover:not(.active){color:var(--text);background:var(--bg3)}
.panel{padding:20px;background:var(--glass);border:1px solid var(--border);border-radius:var(--r)}
.tbl-wrap{overflow-x:auto;-webkit-overflow-scrolling:touch}
table{width:100%;border-collapse:collapse;min-width:600px}
th{padding:10px 14px;text-align:left;font-size:10px;font-weight:600;color:var(--text3);text-transform:uppercase;letter-spacing:.8px;border-bottom:1px solid var(--border);white-space:nowrap}
td{padding:10px 14px;font-size:12px;border-bottom:1px solid var(--border);white-space:nowrap}
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
.btn.grn{border-color:rgba(34,197,94,0.3);color:var(--green)}.btn.grn:hover{background:rgba(34,197,94,0.1)}
.btn.red{border-color:rgba(239,68,68,0.3);color:var(--red)}.btn.red:hover{background:rgba(239,68,68,0.1)}
.btn.sm{padding:4px 10px;font-size:10px}
.btn.warn{background:var(--yellow);border-color:var(--yellow);color:#000}.btn.warn:hover{opacity:.85}
.ico{width:14px;height:14px;vertical-align:middle;fill:none;stroke:currentColor;stroke-width:2;stroke-linecap:round;stroke-linejoin:round}
.btn .ico{width:12px;height:12px;margin-right:2px}
.form-group{margin-bottom:14px}
.form-group label{display:block;font-size:11px;color:var(--text2);margin-bottom:4px;font-weight:500}
.form-group input,.form-group select,.form-group textarea{width:100%;padding:8px 12px;background:var(--bg2);border:1px solid var(--border);border-radius:var(--rs);color:var(--text);font:13px inherit}
.form-group input:focus,.form-group select:focus,.form-group textarea:focus{outline:none;border-color:var(--accent)}
.form-group textarea{min-height:120px;font:11px 'SF Mono',Monaco,Consolas,monospace;resize:vertical}
.form-row{display:grid;grid-template-columns:1fr 1fr;gap:12px}
.form-row3{display:grid;grid-template-columns:1fr 1fr 1fr;gap:12px}
canvas{width:100%;height:200px;border-radius:var(--rs);background:var(--bg2);border:1px solid var(--border)}
.toast{position:fixed;bottom:20px;right:20px;padding:10px 18px;background:var(--green);color:#fff;border-radius:var(--rs);font:500 12px inherit;opacity:0;transform:translateY(8px);transition:.3s;pointer-events:none;z-index:99}
.toast.show{opacity:1;transform:translateY(0)}.toast.error{background:var(--red)}
.section-hdr{display:flex;justify-content:space-between;align-items:center;margin-bottom:12px;flex-wrap:wrap;gap:8px}
.section-hdr h3{font-size:14px;font-weight:600}
.modal-bg{display:none;position:fixed;top:0;left:0;width:100%;height:100%;background:rgba(0,0,0,0.6);z-index:50;align-items:center;justify-content:center}
.modal-bg.show{display:flex}
.modal{background:var(--bg2);border:1px solid var(--border);border-radius:var(--r);padding:24px;max-width:500px;width:90%;max-height:85vh;overflow-y:auto}
.modal h3{font-size:16px;margin-bottom:16px}
@media(max-width:768px){.grid4{grid-template-columns:1fr 1fr}.form-row,.form-row3{grid-template-columns:1fr}}
</style>
</head>
<body>
<div id="toast" class="toast"></div>
<div id="modal-user" class="modal-bg" onclick="if(event.target===this)closeModal('modal-user')">
  <div class="modal">
    <h3 id="mu-title">Add User</h3>
    <input type="hidden" id="mu-mode" value="add">
    <div class="form-row">
      <div class="form-group"><label>Username</label><input id="mu-user" placeholder="username"></div>
      <div class="form-group"><label>Password</label><input id="mu-pass" placeholder="password"></div>
    </div>
    <div class="form-row">
      <div class="form-group"><label>Bandwidth Limit</label><input type="number" id="mu-bw" placeholder="0 = unlimited"></div>
      <div class="form-group"><label>Unit</label><select id="mu-bwu"><option value="kbit">Kbit</option><option value="mbit">Mbit</option></select></div>
    </div>
    <div class="form-row">
      <div class="form-group"><label>Data Limit</label><input type="number" id="mu-dl" placeholder="0 = unlimited"></div>
      <div class="form-group"><label>Unit</label><select id="mu-dlu"><option value="gb">GB</option><option value="mb">MB</option></select></div>
    </div>
    <div class="form-group"><label>IP Limit (concurrent)</label><input type="number" id="mu-ip" placeholder="0 = unlimited"></div>
    <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
      <button class="btn" onclick="closeModal('modal-user')">Cancel</button>
      <button class="btn pri" onclick="submitUser()">Save</button>
    </div>
  </div>
</div>
<div id="modal-inst" class="modal-bg" onclick="if(event.target===this)closeModal('modal-inst')">
  <div class="modal">
    <h3 id="mi-title">Add Instance</h3>
    <input type="hidden" id="mi-mode" value="add">
    <input type="hidden" id="mi-idx" value="-1">
    <div class="form-row">
      <div class="form-group"><label>Domain</label><input id="mi-domain" placeholder="example.com"></div>
      <div class="form-group"><label>Resolver</label><input id="mi-resolver" placeholder="1.1.1.1"></div>
    </div>
    <div class="form-row3">
      <div class="form-group"><label>Port (or range)</label><input id="mi-port" placeholder="17001 or 17001-17004"></div>
      <div class="form-group"><label>Replicas</label><input type="number" id="mi-replicas" placeholder="1"></div>
      <div class="form-group"><label>Mode</label><select id="mi-imode"><option value="socks">SOCKS</option><option value="ssh">SSH</option></select></div>
    </div>
    <div class="form-row">
      <div class="form-group"><label>Authoritative</label><select id="mi-auth"><option value="false">No</option><option value="true">Yes</option></select></div>
      <div class="form-group"><label>Cert (optional)</label><input id="mi-cert" placeholder="path/to/cert"></div>
    </div>
    <div id="mi-ssh-fields" style="display:none">
      <div class="form-row3">
        <div class="form-group"><label>SSH Port</label><input type="number" id="mi-sshport" placeholder="22"></div>
        <div class="form-group"><label>SSH User</label><input id="mi-sshuser" placeholder="root"></div>
        <div class="form-group"><label>SSH Password</label><input id="mi-sshpass" placeholder="password"></div>
      </div>
      <div class="form-group"><label>SSH Key (path)</label><input id="mi-sshkey" placeholder="/path/to/key"></div>
    </div>
    <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
      <button class="btn" onclick="closeModal('modal-inst')">Cancel</button>
      <button class="btn pri" onclick="submitInstance()">Save</button>
    </div>
  </div>
</div>
<div class="container">
  <header>
    <div><h1>SlipstreamPlus</h1><div class="sub">Multi-threaded DNS Tunnel Load Balancer</div></div>
    <div class="hdr-btns">
      <div class="badge on"><span class="dot"></span> Running</div>
      <button class="btn warn" onclick="fullRestart()"><svg class="ico" viewBox="0 0 24 24"><path d="M1 4v6h6M23 20v-6h-6"/><path d="M20.49 9A9 9 0 0 0 5.64 5.64L1 10m22 4l-4.64 4.36A9 9 0 0 1 3.51 15"/></svg> Restart</button>
    </div>
  </header>
  <div class="grid4">
    <div class="card"><div class="lbl">Instances</div><div class="val a" id="s-total">-</div></div>
    <div class="card"><div class="lbl">Healthy</div><div class="val g" id="s-healthy">-</div></div>
    <div class="card"><div class="lbl">Connections</div><div class="val a" id="s-conns">-</div></div>
    <div class="card"><div class="lbl">Bandwidth ↑/↓</div><div class="val c" id="s-bw">-</div></div>
  </div>
  <div class="tabs">
    <button class="tab active" onclick="switchTab('instances',this)">Instances</button>
    <button class="tab" onclick="switchTab('users',this)">Users</button>
    <button class="tab" onclick="switchTab('graph',this)">Bandwidth</button>
    <button class="tab" onclick="switchTab('config',this)">Config</button>
  </div>
  <div id="tab-instances">
    <div class="panel">
      <div class="section-hdr">
        <h3>Instances</h3>
        <button class="btn grn" onclick="openAddInst()">+ Add Instance</button>
      </div>
      <div class="tbl-wrap">
      <table><thead><tr>
        <th>ID</th><th>Domain</th><th>Port</th><th>Mode</th><th>State</th>
        <th>Conns</th><th>Ping</th><th>TX</th><th>RX</th><th>Actions</th>
      </tr></thead><tbody id="tbl"></tbody></table>
      </div>
    </div>
  </div>
  <div id="tab-users" style="display:none">
    <div class="panel">
      <div class="section-hdr">
        <h3>Users</h3>
        <div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap">
          <input id="usr-search" placeholder="Search users..." oninput="filterUsers()" style="padding:5px 10px;background:var(--bg2);border:1px solid var(--border);border-radius:var(--rs);color:var(--text);font:12px inherit;width:160px">
          <button class="btn grn" onclick="openAddUser()">+ Add User</button>
        </div>
      </div>
      <div class="tbl-wrap">
      <table><thead><tr>
        <th>Username</th><th>BW Limit</th><th>Data Limit</th><th>Used</th><th>IP Limit</th><th>Active IPs</th><th>Actions</th>
      </tr></thead><tbody id="usr-tbl"></tbody></table>
      </div>
      <div id="no-users" style="text-align:center;padding:20px;color:var(--text3);font-size:12px">No users configured (auth disabled)</div>
    </div>
  </div>
  <div id="tab-graph" style="display:none">
    <div class="panel">
      <div class="section-hdr"><h3>Bandwidth (last 24h)</h3></div>
      <canvas id="bw-canvas" height="200"></canvas>
      <div style="display:flex;gap:16px;margin-top:8px;justify-content:center;font-size:11px;color:var(--text2)">
        <span style="color:var(--green)">● Upload (TX)</span><span style="color:var(--cyan)">● Download (RX)</span>
      </div>
    </div>
  </div>
  <div id="tab-config" style="display:none">
    <div class="panel">
      <div class="section-hdr">
        <h3>Configuration</h3>
        <div style="display:flex;gap:6px;flex-wrap:wrap">
          <button class="btn pri" onclick="saveConfig()"><svg class="ico" viewBox="0 0 24 24"><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/><polyline points="17 21 17 13 7 13 7 21"/><polyline points="7 3 7 8 15 8"/></svg> Save</button>
          <button class="btn grn" onclick="saveAndApply()"><svg class="ico" viewBox="0 0 24 24"><path d="M1 4v6h6M23 20v-6h-6"/><path d="M20.49 9A9 9 0 0 0 5.64 5.64L1 10m22 4l-4.64 4.36A9 9 0 0 1 3.51 15"/></svg> Save &amp; Apply</button>
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
        <div class="form-group"><label>Health Interval</label><input id="cfg-hi" placeholder="30s"></div>
        <div class="form-group"><label>Health Timeout</label><input id="cfg-ht" placeholder="30s"></div>
      </div>
    </div>
  </div>
  <div style="text-align:center;padding:24px;margin-top:32px;border-top:1px solid var(--border);font-size:12px;color:var(--text3)">
    Developed with ❤️ by ParsaKSH - <a href="https://github.com/ParsaKSH/SlipStream-Plus" target="_blank" style="color:var(--accent2);text-decoration:none">Github Repository</a>
  </div>
</div>
<script>
let CC=null;
const TABS=['instances','users','graph','config'];
function switchTab(t,btn){
  document.querySelectorAll('.tab').forEach(b=>b.classList.remove('active'));
  btn.classList.add('active');
  TABS.forEach(n=>document.getElementById('tab-'+n).style.display=n===t?'block':'none');
  if(t==='config'&&CC)fillForm(CC);
  if(t==='graph')loadBW();
  if(t==='users')fetchUsers();
}
function toast(m,e){const t=document.getElementById('toast');t.textContent=m;t.className='toast show'+(e?' error':'');setTimeout(()=>t.className='toast',3000)}
function fmt(b){if(b<1024)return b+'B';if(b<1048576)return(b/1024).toFixed(1)+'KB';if(b<1073741824)return(b/1048576).toFixed(1)+'MB';return(b/1073741824).toFixed(2)+'GB'}
function esc(s){const d=document.createElement('div');d.textContent=s;return d.innerHTML}
function openModal(id){document.getElementById(id).classList.add('show')}
function closeModal(id){document.getElementById(id).classList.remove('show')}

// ─── Status ───
async function fetchStatus(){
  try{
    const r=await fetch('/api/status');const d=await r.json();
    const inst=d.instances||[];
    document.getElementById('s-total').textContent=inst.length;
    document.getElementById('s-healthy').textContent=inst.filter(i=>i.state==='healthy').length;
    document.getElementById('s-conns').textContent=inst.reduce((s,i)=>s+i.active_conns,0);
    const tx=inst.reduce((s,i)=>s+i.tx_bytes,0),rx=inst.reduce((s,i)=>s+i.rx_bytes,0);
    const bw=document.getElementById('s-bw');bw.textContent=fmt(tx)+' / '+fmt(rx);bw.style.fontSize='16px';
    document.getElementById('tbl').innerHTML=inst.map((i,idx)=>{
      const p=i.last_ping_ms>0?i.last_ping_ms+'ms':'—';
      const m=(i.mode||'socks').toUpperCase();
      const mc=i.mode==='ssh'?'var(--blue)':'var(--accent2)';
      return '<tr>'+
        '<td>#'+i.id+'</td>'+
        '<td style="font-weight:500">'+esc(i.domain)+'</td>'+
        '<td>'+i.port+'</td>'+
        '<td><span style="color:'+mc+';font-size:10px;font-weight:700">'+m+'</span></td>'+
        '<td><span class="sb '+i.state+'">'+i.state+'</span></td>'+
        '<td>'+i.active_conns+'</td><td>'+p+'</td>'+
        '<td style="color:var(--green);font-size:11px">'+fmt(i.tx_bytes)+'</td>'+
        '<td style="color:var(--cyan);font-size:11px">'+fmt(i.rx_bytes)+'</td>'+
        '<td style="display:flex;gap:4px">'+
          '<button class="btn sm" onclick="editInst('+idx+')" title="Edit"><svg class="ico" viewBox="0 0 24 24"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg></button>'+
          '<button class="btn sm red" onclick="deleteInst('+idx+')" title="Delete"><svg class="ico" viewBox="0 0 24 24"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg></button>'+
          '<button class="btn sm" onclick="restartInst('+i.id+')" title="Restart"><svg class="ico" viewBox="0 0 24 24"><path d="M1 4v6h6"/><path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10"/></svg></button>'+
        '</td></tr>';
    }).join('');
  }catch(e){}
}

// ─── Users ───
let cachedUsers=[];
function renderUsers(data){
  const tb=document.getElementById('usr-tbl'),nu=document.getElementById('no-users');
  if(!data||data.length===0){tb.innerHTML='';nu.style.display='block';return}
  nu.style.display='none';
  const q=(document.getElementById('usr-search').value||'').toLowerCase();
  const filtered=q?data.filter(u=>u.username.toLowerCase().includes(q)):data;
  tb.innerHTML=filtered.map(u=>{
    const bw=u.bandwidth_limit>0?(u.bandwidth_limit+' '+u.bandwidth_unit):'∞';
    const dl=u.data_limit>0?(u.data_limit+' '+u.data_unit):'∞';
    const ip=u.ip_limit>0?u.ip_limit:'∞';
    const pct=u.data_limit>0?Math.round(u.data_used_bytes/({gb:1073741824,mb:1048576}[u.data_unit]||1)/u.data_limit*100):0;
    const bar=u.data_limit>0?'<div style="width:60px;height:4px;background:var(--bg4);border-radius:2px;margin-top:2px"><div style="width:'+Math.min(pct,100)+'%;height:100%;background:'+(pct>90?'var(--red)':pct>70?'var(--yellow)':'var(--green)')+';border-radius:2px"></div></div>':'';
    return '<tr>'+
      '<td style="font-weight:600;color:var(--accent2)">'+esc(u.username)+'</td>'+
      '<td>'+bw+'</td><td>'+dl+'</td>'+
      '<td>'+fmt(u.data_used_bytes)+bar+'</td>'+
      '<td>'+ip+'</td><td>'+u.active_ips+'</td>'+
      '<td style="display:flex;gap:4px">'+
        '<button class="btn sm" onclick="editUser(\''+esc(u.username)+'\')" title="Edit"><svg class="ico" viewBox="0 0 24 24"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg></button>'+
        '<button class="btn sm red" onclick="deleteUser(\''+esc(u.username)+'\')" title="Delete"><svg class="ico" viewBox="0 0 24 24"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg></button>'+
        '<button class="btn sm" onclick="resetUser(\''+esc(u.username)+'\')" title="Reset Data"><svg class="ico" viewBox="0 0 24 24"><path d="M1 4v6h6"/><path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10"/></svg></button>'+
      '</td></tr>';
  }).join('');
}
async function fetchUsers(){
  try{const r=await fetch('/api/users');cachedUsers=await r.json();renderUsers(cachedUsers)}catch(e){}
}
function filterUsers(){renderUsers(cachedUsers)}

// ─── User CRUD ───
function openAddUser(){
  document.getElementById('mu-title').textContent='Add User';
  document.getElementById('mu-mode').value='add';
  ['mu-user','mu-pass','mu-bw','mu-dl','mu-ip'].forEach(id=>document.getElementById(id).value='');
  document.getElementById('mu-user').disabled=false;
  document.getElementById('mu-bwu').value='kbit';
  document.getElementById('mu-dlu').value='gb';
  openModal('modal-user');
}
function editUser(username){
  if(!CC)return;
  const u=(CC.socks?.users||[]).find(u=>u.username===username);
  if(!u)return;
  document.getElementById('mu-title').textContent='Edit User';
  document.getElementById('mu-mode').value='edit';
  document.getElementById('mu-user').value=u.username;document.getElementById('mu-user').disabled=true;
  document.getElementById('mu-pass').value=u.password;
  document.getElementById('mu-bw').value=u.bandwidth_limit||'';
  document.getElementById('mu-bwu').value=u.bandwidth_unit||'kbit';
  document.getElementById('mu-dl').value=u.data_limit||'';
  document.getElementById('mu-dlu').value=u.data_unit||'gb';
  document.getElementById('mu-ip').value=u.ip_limit||'';
  openModal('modal-user');
}
async function submitUser(){
  const mode=document.getElementById('mu-mode').value;
  const uc={username:document.getElementById('mu-user').value,password:document.getElementById('mu-pass').value,
    bandwidth_limit:parseInt(document.getElementById('mu-bw').value)||0,bandwidth_unit:document.getElementById('mu-bwu').value,
    data_limit:parseInt(document.getElementById('mu-dl').value)||0,data_unit:document.getElementById('mu-dlu').value,
    ip_limit:parseInt(document.getElementById('mu-ip').value)||0};
  if(!uc.username||!uc.password){toast('Username & password required',true);return}
  try{
    const url=mode==='add'?'/api/users':'/api/users/'+uc.username+'/edit';
    const r=await fetch(url,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(uc)});
    if(!r.ok){toast(await r.text(),true);return}
    closeModal('modal-user');toast(mode==='add'?'User added':'User updated');loadCfg();fetchUsers();
  }catch(e){toast('Failed',true)}
}
async function deleteUser(username){
  if(!confirm('Delete user '+username+'?'))return;
  try{
    const r=await fetch('/api/users/'+username+'/delete',{method:'POST'});
    if(!r.ok){toast(await r.text(),true);return}
    toast('User deleted');loadCfg();fetchUsers();
  }catch(e){toast('Failed',true)}
}
async function resetUser(username){
  try{const r=await fetch('/api/users/'+username+'/reset',{method:'POST'});if(r.ok){toast('Data reset');fetchUsers()}}catch(e){toast('Failed',true)}
}

// ─── Instance CRUD ───
function showSSHFields(){
  document.getElementById('mi-ssh-fields').style.display=document.getElementById('mi-imode').value==='ssh'?'block':'none';
}
function openAddInst(){
  document.getElementById('mi-title').textContent='Add Instance';
  document.getElementById('mi-mode').value='add';
  document.getElementById('mi-idx').value='-1';
  ['mi-domain','mi-resolver','mi-port','mi-replicas','mi-cert','mi-sshport','mi-sshuser','mi-sshpass','mi-sshkey'].forEach(id=>document.getElementById(id).value='');
  document.getElementById('mi-imode').value='socks';
  document.getElementById('mi-auth').value='false';
  document.getElementById('mi-imode').onchange=showSSHFields;
  showSSHFields();
  openModal('modal-inst');
}
function editInst(idx){
  if(!CC||!CC.instances[idx])return;
  const inst=CC.instances[idx];
  document.getElementById('mi-title').textContent='Edit Instance';
  document.getElementById('mi-mode').value='edit';
  document.getElementById('mi-idx').value=idx;
  document.getElementById('mi-domain').value=inst.domain||'';
  document.getElementById('mi-resolver').value=inst.resolver||'';
  // port might be number or string
  const p=inst.port;document.getElementById('mi-port').value=typeof p==='number'?p:(typeof p==='string'?p:JSON.stringify(p));
  document.getElementById('mi-replicas').value=inst.replicas||1;
  document.getElementById('mi-imode').value=inst.mode||'socks';
  document.getElementById('mi-auth').value=inst.authoritative?'true':'false';
  document.getElementById('mi-cert').value=inst.cert||'';
  document.getElementById('mi-sshport').value=inst.ssh_port||'';
  document.getElementById('mi-sshuser').value=inst.ssh_user||'';
  document.getElementById('mi-sshpass').value=inst.ssh_password||'';
  document.getElementById('mi-sshkey').value=inst.ssh_key||'';
  document.getElementById('mi-imode').onchange=showSSHFields;
  showSSHFields();
  openModal('modal-inst');
}
async function submitInstance(){
  const mode=document.getElementById('mi-mode').value;
  const idx=parseInt(document.getElementById('mi-idx').value);
  let portVal=document.getElementById('mi-port').value.trim();
  // Try to parse as number, otherwise keep as string
  const portNum=parseInt(portVal);
  const port=(!isNaN(portNum)&&portNum.toString()===portVal)?portNum:portVal;
  const inst={domain:document.getElementById('mi-domain').value,resolver:document.getElementById('mi-resolver').value,
    port:port,replicas:parseInt(document.getElementById('mi-replicas').value)||1,
    mode:document.getElementById('mi-imode').value,
    authoritative:document.getElementById('mi-auth').value==='true',
    cert:document.getElementById('mi-cert').value||undefined};
  if(inst.mode==='ssh'){
    inst.ssh_port=parseInt(document.getElementById('mi-sshport').value)||22;
    inst.ssh_user=document.getElementById('mi-sshuser').value;
    inst.ssh_password=document.getElementById('mi-sshpass').value;
    inst.ssh_key=document.getElementById('mi-sshkey').value;
  }
  if(!inst.domain||!inst.resolver||!inst.port){toast('Domain, resolver, and port are required',true);return}
  if(!CC)await loadCfg();
  const instances=CC.instances||[];
  if(mode==='add'){instances.push(inst)}else{instances[idx]=inst}
  CC.instances=instances;
  try{
    const r=await fetch('/api/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(CC)});
    if(!r.ok){toast(await r.text(),true);return}
    closeModal('modal-inst');toast(mode==='add'?'Instance added - Save & Apply to activate':'Instance updated - Save & Apply to activate');loadCfg();
  }catch(e){toast('Failed',true)}
}
async function deleteInst(idx){
  if(!CC||!confirm('Delete this instance?'))return;
  CC.instances.splice(idx,1);
  try{
    const r=await fetch('/api/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(CC)});
    if(!r.ok){toast(await r.text(),true);return}
    toast('Instance deleted - Save & Apply to activate');loadCfg();
  }catch(e){toast('Failed',true)}
}
async function restartInst(id){
  try{await fetch('/api/instance/'+id+'/restart',{method:'POST'});toast('Instance #'+id+' restarting...')}catch(e){toast('Failed',true)}
}

// ─── Config ───
async function loadCfg(){try{const r=await fetch('/api/config');CC=await r.json()}catch(e){}}
function fillForm(c){
  document.getElementById('cfg-listen').value=c.socks?.listen||'';
  document.getElementById('cfg-strat').value=c.strategy||'round_robin';
  document.getElementById('cfg-buf').value=c.socks?.buffer_size||65536;
  document.getElementById('cfg-max').value=c.socks?.max_connections||10000;
  document.getElementById('cfg-hi').value=c.health_check?.interval||'30s';
  document.getElementById('cfg-ht').value=c.health_check?.timeout||'30s';
}
async function saveConfig(){
  try{
    if(!CC)await loadCfg();
    CC.socks.listen=document.getElementById('cfg-listen').value;
    CC.socks.buffer_size=parseInt(document.getElementById('cfg-buf').value)||65536;
    CC.socks.max_connections=parseInt(document.getElementById('cfg-max').value)||10000;
    CC.strategy=document.getElementById('cfg-strat').value;
    CC.health_check.interval=document.getElementById('cfg-hi').value;
    CC.health_check.timeout=document.getElementById('cfg-ht').value;
    const r=await fetch('/api/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(CC)});
    if(!r.ok){toast(await r.text(),true);return}toast('Config saved!')
  }catch(e){toast('Save failed',true)}
}
async function saveAndApply(){
  await saveConfig();
  try{await fetch('/api/restart',{method:'POST'});toast('Config saved & restarting...');setTimeout(()=>location.reload(),2000)}catch(e){toast('Restart failed',true)}
}
async function fullRestart(){
  if(!confirm('Full restart? All connections will be dropped.'))return;
  try{await fetch('/api/restart',{method:'POST'});toast('Restarting...');setTimeout(()=>location.reload(),2000)}catch(e){toast('Failed',true)}
}

// ─── Bandwidth Chart ───
async function loadBW(){try{const r=await fetch('/api/bandwidth');drawChart(await r.json())}catch(e){}}
function drawChart(data){
  const canvas=document.getElementById('bw-canvas'),ctx=canvas.getContext('2d');
  const dpr=window.devicePixelRatio||1;canvas.width=canvas.clientWidth*dpr;canvas.height=canvas.clientHeight*dpr;
  ctx.scale(dpr,dpr);const W=canvas.clientWidth,H=canvas.clientHeight;ctx.clearRect(0,0,W,H);
  if(!data||data.length<2){ctx.fillStyle='#5a6478';ctx.font='12px sans-serif';ctx.textAlign='center';ctx.fillText('Collecting data...',W/2,H/2);return}
  const maxVal=Math.max(...data.map(d=>Math.max(d.tx,d.rx)),1);
  const pad={t:10,b:24,l:50,r:10},gW=W-pad.l-pad.r,gH=H-pad.t-pad.b;
  ctx.strokeStyle='rgba(255,255,255,0.06)';ctx.lineWidth=1;
  for(let i=0;i<5;i++){const y=pad.t+gH*i/4;ctx.beginPath();ctx.moveTo(pad.l,y);ctx.lineTo(W-pad.r,y);ctx.stroke();
    ctx.fillStyle='#5a6478';ctx.font='9px sans-serif';ctx.textAlign='right';ctx.fillText(fmt(maxVal*(1-i/4))+'/s',pad.l-4,y+3)}
  function drawLine(c,k){ctx.beginPath();ctx.strokeStyle=c;ctx.lineWidth=1.5;
    data.forEach((d,i)=>{const x=pad.l+gW*i/(data.length-1),y=pad.t+gH*(1-d[k]/maxVal);i===0?ctx.moveTo(x,y):ctx.lineTo(x,y)});
    ctx.stroke();ctx.lineTo(pad.l+gW,pad.t+gH);ctx.lineTo(pad.l,pad.t+gH);ctx.closePath();
    ctx.fillStyle=c.replace(')',',0.08)').replace('rgb','rgba');ctx.fill()}
  drawLine('rgb(34,197,94)','tx');drawLine('rgb(6,182,212)','rx');
}

fetchStatus();loadCfg();
setInterval(fetchStatus,2000);
setInterval(()=>{if(document.getElementById('tab-graph').style.display!=='none')loadBW()},10000);
setInterval(()=>{if(document.getElementById('tab-users').style.display!=='none')fetchUsers()},3000);
</script>
</body>
</html>`
