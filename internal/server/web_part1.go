package server

const indexHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>消息目录校验</title>
<style>
:root{--bd:#d9dee7;--mut:#6b7280;--ok:#15803d;--bad:#b91c1c;--warn:#b45309;--blue:#1d4ed8;}
*{box-sizing:border-box}
body{font-family:-apple-system,"PingFang SC","Segoe UI",sans-serif;margin:0;color:#111827;background:#f7f8fa}
header{background:#0f172a;color:#fff;padding:14px 20px}
header h1{margin:0;font-size:20px}
header .snap{font-size:12px;color:#cbd5e1;margin-top:4px}
main{display:grid;grid-template-columns:340px 1fr;gap:16px;padding:16px}
.card{background:#fff;border:1px solid var(--bd);border-radius:8px;padding:14px;margin-bottom:14px}
.card h2{font-size:15px;margin:0 0 10px}
label{font-size:12px;color:var(--mut);display:block;margin:6px 0 2px}
input,select,textarea{width:100%;padding:6px 8px;border:1px solid var(--bd);border-radius:6px;font-size:13px;font-family:inherit}
button{background:var(--blue);color:#fff;border:0;border-radius:6px;padding:7px 12px;font-size:13px;cursor:pointer;margin-top:8px}
button.ghost{background:#eef2ff;color:var(--blue)}
button.danger{background:#fee2e2;color:var(--bad)}
button:disabled{opacity:.5}
table{border-collapse:collapse;width:100%;font-size:12.5px}
th,td{border-bottom:1px solid #eef0f4;padding:6px 8px;text-align:left;vertical-align:top}
th{background:#f1f5f9;position:sticky;top:0}
.tag{display:inline-block;padding:1px 7px;border-radius:999px;font-size:11px}
.t-bad{background:#fee2e2;color:var(--bad)}
.t-ok{background:#dcfce7;color:var(--ok)}
.t-warn{background:#fef3c7;color:var(--warn)}
.t-mut{background:#e5e7eb;color:#374151}
.mut{color:var(--mut);font-size:12px}
.mono{font-family:ui-monospace,Menlo,monospace}
.row{display:flex;gap:8px;align-items:center}
.pill{font-family:ui-monospace,Menlo,monospace;font-size:11px;background:#f1f5f9;border:1px solid var(--bd);padding:1px 6px;border-radius:6px}
pre{background:#0b1020;color:#dbeafe;padding:10px;border-radius:8px;overflow:auto;font-size:12px;max-height:340px}
.small{font-size:12px}
.banner{padding:8px 12px;border-radius:6px;margin-bottom:10px;font-size:13px}
.banner.err{background:#fee2e2;color:var(--bad)}
.banner.ok{background:#dcfce7;color:var(--ok)}
.banner.warn{background:#fef3c7;color:var(--warn)}
.maprow{display:grid;grid-template-columns:1fr 24px 1fr;gap:6px;align-items:center;margin-bottom:6px}
.issuecode{font-family:ui-monospace,Menlo,monospace;font-size:11px}
</style>
</head>
<body>
<header>
  <h1>消息目录校验</h1>
  <div class="snap" id="headsnap">未加载</div>
</header>
<main>
<div>
  <div class="card">
    <h2>1. 导入目录</h2>
    <label>语言代码（如 en / ja）</label>
    <input id="lang" value="en"/>
    <label>消息文件（JSON）</label>
    <input id="file" type="file" accept=".json,application/json"/>
    <label class="row"><input type="checkbox" id="baseline" style="width:auto"/> 作为基准语言</label>
    <label>操作标识（可留空，自动生成）</label>
    <input id="importOp" placeholder="Idempotency-Key"/>
    <button onclick="doImport()">导入</button>
    <div id="importMsg" class="small" style="margin-top:8px"></div>
  </div>

  <div class="card">
    <h2>语言与版本</h2>
    <div id="langs"></div>
  </div>

  <div class="card">
    <h2>确认改名</h2>
    <div id="renameArea" class="small">没有待确认的改名。</div>
  </div>

  <div class="card">
    <h2>受控时钟 / 快照</h2>
    <label>评估时间 asOf（unix 秒，留空=快照时间）</label>
    <div class="row">
      <input id="asOf" placeholder="1700000000"/>
      <button class="ghost" onclick="setAsOffset()">+60s</button>
    </div>
    <label>查看快照 seq（空=最新）</label>
    <div class="row">
      <input id="seqSel" placeholder="latest"/>
      <button class="ghost" onclick="load()">查看</button>
    </div>
    <div id="snapshots" class="mut" style="margin-top:6px"></div>
  </div>
</div>

<div>
  <div id="banner"></div>
  <div class="card">
    <h2>2. 并排结构（按 key）</h2>
    <div class="row">
      <input id="keyInput" placeholder="输入或选择 key"/>
      <button onclick="showKey()">查看结构</button>
    </div>
    <div id="keys" class="mut" style="margin:6px 0"></div>
    <div id="keyDetail"></div>
  </div>

  <div class="card">
    <h2>3. 问题与豁免</h2>
    <div class="row">
      <span id="issueCounts" class="small"></span>
      <span style="flex:1"></span>
      <input id="exReason" placeholder="豁免理由" style="max-width:240px"/>
      <input id="exSeconds" type="number" placeholder="有效秒数" style="max-width:110px" value="60"/>
      <button class="ghost" onclick="exemptSelected()">豁免选中</button>
      <button class="danger" onclick="revokeSelected()">撤销</button>
    </div>
    <div style="margin-top:8px;max-height:330px;overflow:auto">
      <table id="issueTable"><thead><tr>
        <th></th><th>代码</th><th>语言</th><th>key</th><th>详情</th><th>豁免</th>
      </tr></thead><tbody></tbody></table>
    </div>
  </div>

  <div class="card">
    <h2>4. 批量修正（按已确认改名更新目标语言）</h2>
    <div id="batchLangs"></div>
    <div class="row">
      <input id="batchOp" placeholder="操作标识（留空自动生成）"/>
      <button onclick="runBatch()">提交批量修正</button>
      <button class="ghost" onclick="runBatch(true)">用同标识重试</button>
    </div>
    <pre id="batchOut" hidden></pre>
  </div>

  <div class="card">
    <h2>5. 发布证明</h2>
    <div class="row">
      <input id="proofOp" placeholder="操作标识（留空自动生成）"/>
      <button onclick="genProof()">生成证明</button>
      <button class="ghost" onclick="genProof(true)">同标识重复生成</button>
    </div>
    <pre id="proofOut" hidden></pre>
    <div id="proofList" class="small" style="margin-top:8px"></div>
  </div>

  <div class="card">
    <h2>校验运行（旧结果可重放）</h2>
    <div id="runs"></div>
  </div>
</div>
</main>
<script>
let STATE=null, VIEW=null, SELECTED=new Set(), LAST={};
function uid(p){return p+"-"+Date.now().toString(36)+"-"+Math.random().toString(36).slice(2,7);}
function api(path,opts){opts=opts||{};opts.headers=opts.headers||{};return fetch(path,opts).then(async r=>{
  const t=await r.text(); let j; try{j=t?JSON.parse(t):{};}catch(e){j={raw:t};}
  if(!r.ok) throw new Error(j.error||("HTTP "+r.status)); return j;});}
function esc(s){return String(s==null?"":s).replace(/[&<>"]/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c]));}
function fp8(s){return s?String(s).slice(0,8):"-";}
function banner(kind,msg){const b=document.getElementById("banner");
  b.innerHTML='<div class="banner '+kind+'">'+esc(msg)+'</div>';}
</script>
` + indexHTML2 + indexHTML3 + `
</body></html>`
