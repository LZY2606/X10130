let STATE=null, CURRENT_REPORT=null, CURRENT_TUPLE=null;
const $=id=>document.getElementById(id);
function toast(msg,kind='ok'){const d=document.createElement('div');d.className='t-'+kind;d.textContent=msg;$('toast').appendChild(d);setTimeout(()=>d.remove(),6000);}
function b64(s){return btoa(unescape(encodeURIComponent(s)));}
async function api(path,opts){
  const r=await fetch(path,opts);
  let j=null; try{j=await r.json();}catch(e){}
  if(!r.ok||(j&&j.error)){const e=new Error(j?j.error:('HTTP '+r.status));e.status=r.status;e.body=j;throw e;}
  return j;
}
function post(path,obj,opId){
  const headers={'Content-Type':'application/json'};
  if(opId)headers['X-Op-Id']=opId;
  return api(path,{method:'POST',headers,body:JSON.stringify(obj)});
}
function opOr(elId){const v=$(elId).value.trim();if(v)return v;const id='op-'+crypto.randomUUID();$(elId).value=id;return id;}
function esc(s){return String(s==null?'':s).replace(/[&<>]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;'}[c]));}
function shortId(v){return v?v.slice(0,12):'—';}

function readFile(f){return new Promise((res,rej)=>{const fr=new FileReader();fr.onload=()=>res(fr.result);fr.onerror=rej;fr.readAsText(f);});}

async function doImport(){
  const lang=$('impLang').value.trim(); const f=$('impFile').files[0];
  if(!lang||!f){toast('语言和文件必填','err');return;}
  const text=await readFile(f);
  try{
    const r=await post('/api/import',{language:lang,content:b64(text),filename:f.name},opOr('impOp'));
    toast('已导入 '+lang+' 版本 '+shortId(r.contentVersion)+(r.versionChanged?'（新版本）':'（版本不变）'));
    if(r.pendingRename){toast('检测到改名待确认：移除 '+r.pendingRename.removed.length+' / 新增 '+r.pendingRename.added.length,'warn');}
    await loadState();
  }catch(e){toast('导入失败：'+e.message,'err');}
}

async function loadState(){
  STATE=await api('/api/state');
  $('snapLine').textContent='快照 '+STATE.snapshotId+' · seq '+STATE.seq+' · 解析器 '+STATE.parserVersion+' · 映射 v'+STATE.mappingVersion+' · '+STATE.now;
  $('stateBox').innerHTML=kv([
    ['基准语言',STATE.baselineLanguage||'—'],['快照',STATE.snapshotId],['状态序号',STATE.seq],
    ['映射版本',STATE.mappingVersion],['解析器版本',STATE.parserVersion],['当前时间',STATE.now],
  ]);
  $('degradeBox').textContent=STATE.degraded?('完整性告警：'+STATE.integrityNote):'';
  const tb=$('langTable').querySelector('tbody');tb.innerHTML='';
  const sel=$('issueLang');const cur=sel.value;sel.innerHTML='';
  for(const l of STATE.languages){
    tb.innerHTML+='<tr><td>'+esc(l.language)+'</td><td>'+(l.isBaseline?'<span class="badge b-info">基准</span>':'')+'</td>'+
      '<td class="mono">'+shortId(l.contentVersion)+'</td><td class="mono">'+esc(l.uploadId)+'</td><td class="mut">'+esc(l.importedAt)+'</td>'+
      '<td><a href="/api/raw?lang='+encodeURIComponent(l.language)+'"><button>原始文件</button></a> '+
      '<button onclick="viewLangIssues(\''+esc(l.language)+'\')">问题</button> '+
      '<button onclick="$(\'cmpKey\').value=\'\';$(\'cmpKey\').placeholder=\'选择 key 后查看\'">—</button></td></tr>';
    sel.innerHTML+='<option value="'+esc(l.language)+'">'+esc(l.language)+'</option>';
  }
  if(cur)sel.value=cur;
  renderRename();
  renderBatchTargets();
  loadIssues();loadTuples();
}
function kv(pairs){return pairs.map(([k,v])=>'<span class="mut">'+k+'</span><span>'+esc(v)+'</span>').join('');}

function renderRename(){
  const box=$('renameBox');
  const p=STATE.pendingRename;
  if(!p){box.innerHTML='<span class="mut">当前没有待确认改名。</span>';return;}
  let rows='';
  for(const old of p.removed){
    const sug=p.suggested[old]||'';
    rows+='<tr><td class="mono">'+esc(old)+'</td><td>→</td><td><select data-old="'+esc(old)+'" class="newpick">'+
      '<option value="">（不映射 / 保持缺失）</option>'+
      p.added.map(n=>'<option value="'+esc(n)+'"'+(n===sug?' selected':'')+'>'+esc(n)+'</option>').join('')+
      '</select></td></tr>';
  }
  box.innerHTML='<p class="mut">旧版本 '+shortId(p.oldVersion)+' → 新版本 '+shortId(p.newVersion)+'</p>'+
    '<table><thead><tr><th>旧 key</th><th></th><th>新 key</th></tr></thead><tbody>'+rows+'</tbody></table>'+
    '<div class="row" style="margin-top:8px"><input id="renOp" placeholder="rename opId（留空自动）" style="width:240px">'+
    '<button class="primary" onclick="confirmRename(\''+esc(p.newVersion)+'\')">确认所选映射</button></div>';
}
async function confirmRename(newVersion){
  const edges={};
  document.querySelectorAll('.newpick').forEach(sel=>{if(sel.value)edges[sel.dataset.old]=sel.value;});
  if(!Object.keys(edges).length){toast('未选择任何映射','warn');return;}
  try{
    const r=await post('/api/rename/confirm',{baseVersion:newVersion,edges},opOr('renOp'));
    toast('映射已确认，映射版本 v'+r.mappingVersion);
    await loadState();
  }catch(e){toast('改名确认冲突：'+e.message,'err');}
}

async function viewLangIssues(lang){$('issueLang').value=lang;loadIssues();}
async function loadIssues(){
  const lang=$('issueLang').value;if(!lang)return;
  try{
    const r=await api('/api/issues?lang='+encodeURIComponent(lang));
    CURRENT_REPORT=r;CURRENT_TUPLE=r.tupleSha;
    $('issueSnap').textContent='快照 '+r.snapshotId+' · tuple '+shortId(r.tupleSha)+(r.current?'':' · 已失效');
    renderIssues(r.issues);
  }catch(e){toast(e.message,'err');}
}
function renderIssues(issues){
  const tb=$('issueTable').querySelector('tbody');tb.innerHTML='';
  for(const i of issues){
    const badge=i.severity==='error'?'b-error':'b-warn';
    let ex='';
    if(i.exemptionActive)ex='<span class="badge b-ok">已豁免</span><div class="mut">'+esc(i.exemptionDeadline||'无期限')+'</div>';
    else if(i.exempted)ex='<span class="badge b-warn">已过期</span>';
    tb.innerHTML+='<tr><td><span class="badge '+badge+'">'+esc(i.code)+'</span></td>'+
      '<td class="mut">'+esc(i.code)+'</td><td class="mono">'+esc(i.key)+'</td><td class="mono">'+esc(i.placeholder||'')+'</td>'+
      '<td>'+esc(i.message)+'</td><td class="mono mut">'+esc(i.expected||'')+' / '+esc(i.actual||'')+'</td>'+
      '<td>'+ex+'<div class="mut" style="max-width:200px">'+esc(i.exemptionReason||'')+'</div></td>'+
      '<td><button onclick="exemptIssue(\''+i.id+'\')">豁免</button> '+
      '<button onclick="$(\'cmpKey\').value='+JSON.stringify(i.key)+';compare()">查看</button></td></tr>';
  }
}
async function exemptIssue(id){
  if(!CURRENT_REPORT)return;
  const reason=prompt('豁免理由：');if(!reason)return;
  const deadline=prompt('期限 RFC3339（留空表示无期限，例如 2026-10-01T00:00:00Z）：','')||'';
  const opId='op-ex-'+crypto.randomUUID();
  try{
    await post('/api/exemption',{
      issueId:id,reason,deadline,language:CURRENT_REPORT.language,
      baseVersion:CURRENT_REPORT.baselineVersion,targetVersion:CURRENT_REPORT.targetVersion,
      mappingVersion:CURRENT_REPORT.mappingVersion,
    },opId);
    toast('豁免已保存（op '+opId.slice(0,18)+'）');loadIssues();
  }catch(e){toast('豁免冲突：'+e.message,'err');}
}

async function compare(){
  const key=$('cmpKey').value.trim();if(!key){toast('输入 key','warn');return;}
  const r=await api('/api/compare?key='+encodeURIComponent(key));
  $('cmpSnap').textContent='快照 '+r.snapshotId+' · 映射 v'+r.mappingVersion+' · 根 key '+r.resolvedRoot;
  let html='';
  for(const lang of Object.keys(r.languages).sort()){
    const e=r.languages[lang];
    let body;
    if(e.parseError)body='<p class="err">'+esc(e.parseError)+'</p>';
    else body='<pre>'+esc(JSON.stringify(e.parsed,null,2))+'</pre>';
    html+='<div class="card"><h2>'+esc(lang)+' <span class="mono mut">'+esc(e.key)+'</span></h2>'+
      '<div class="mut">原文</div><pre>'+esc(e.text)+'</pre>'+(e.context?'<div class="mut">上下文：'+esc(e.context)+'</div>':'')+body+'</div>';
  }
  $('cmpBox').innerHTML=html;
}

// Batch target rows derived from current languages, editable JSON.
function renderBatchTargets(){
  const box=$('batchBox');if(!STATE)return;
  let html='<table><thead><tr><th>语言</th><th>依据版本</th><th>修正内容（JSON）</th></tr></thead><tbody>';
  for(const l of STATE.languages){
    if(l.isBaseline)continue;
    html+='<tr data-lang="'+esc(l.language)+'"><td>'+esc(l.language)+'</td>'+
      '<td><input class="bver" value="'+esc(l.contentVersion)+'" size=14></td>'+
      '<td><textarea class="bcontent" placeholder=\'{"key":"..."}\'></textarea></td></tr>';
  }
  html+='</tbody></table>';box.innerHTML=html;
}
async function submitBatch(){
  const items=[];
  document.querySelectorAll('#batchBox tr[data-lang]').forEach(tr=>{
    const content=tr.querySelector('.bcontent').value.trim();
    if(content)items.push({language:tr.dataset.lang,baseVersion:tr.querySelector('.bver').value.trim(),content:b64(content)});
  });
  if(!items.length){toast('至少填写一条修正','warn');return;}
  const opId=opOr('batchOp');
  try{
    const r=await post('/api/batch',{items},opId);
    $('batchResult').textContent=JSON.stringify(r,null,2);
    if(r.conflicted&&r.conflicted.length)toast('部分成功：冲突 '+r.conflicted.join(', ')+'；成功已保留：'+(r.succeeded||[]).join(', '),'warn');
    else toast('批量修正全部成功（op '+opId.slice(0,14)+'，重放不产生新版本）');
    await loadState();
  }catch(e){
    $('batchResult').textContent=e.message+'\n'+(e.body?JSON.stringify(e.body,null,2):'');
    toast('批量冲突：'+e.message,'err');
  }
}

async function genProof(){
  try{
    const r=await post('/api/proof',{},'proof-'+crypto.randomUUID());
    $('proofBox').textContent=JSON.stringify(r.proof,null,2);
    $('proofDl').href='/api/proof?download=1';
    toast('证明已生成：'+r.meta.proofId+(r.meta.current?'（当前状态）':'（历史状态）'));
  }catch(e){toast(e.message,'err');}
}

async function queryOp(){
  const id=$('opQuery').value.trim();if(!id)return;
  try{const r=await api('/api/op?id='+encodeURIComponent(id));$('opBox').textContent=JSON.stringify(r,null,2);}
  catch(e){$('opBox').textContent='未找到：'+id;}
}
async function loadTuples(){
  const ts=await api('/api/tuples');
  $('tupleBox').innerHTML='<table><thead><tr><th>语言</th><th>tuple</th><th>基准</th><th>目标</th><th>映射</th><th></th></tr></thead><tbody>'+
  ts.map(t=>'<tr><td>'+esc(t.language)+'</td><td class="mono">'+shortId(t.tupleSha)+'</td><td class="mono">'+shortId(t.baselineVersion)+'</td>'+
    '<td class="mono">'+shortId(t.targetVersion)+'</td><td>v'+t.mappingVersion+'</td>'+
    '<td><button onclick="replayOne(\''+t.tupleSha+'\')">重放</button></td></tr>').join('')+'</tbody></table>';
}
async function replayOne(tuple){
  const r=await api('/api/replay?tuple='+encodeURIComponent(tuple));
  if(!r.current){toast('该结果依据已变化：'+r.invalidReason+'（仍可查看，写操作需重新校验）','warn');}
  $('issueSnap').textContent='重放 tuple '+shortId(tuple)+(r.current?' · 当前':' · 历史/失效');
  renderIssues(r.report.issues);
  CURRENT_REPORT=r.report;CURRENT_TUPLE=tuple;
}
async function openReplay(){if(CURRENT_TUPLE)replayOne(CURRENT_TUPLE);}

async function setClock(){
  const v=$('clock').value.trim();
  const r=await post('/api/clock',{now:v},'clock-set');
  toast('时间源：'+r.mode+' '+r.now);loadState();
}
async function resetClock(){
  const r=await post('/api/clock',{},'clock-reset');$('clock').value='';
  toast('时间源：'+r.mode+' '+r.now);loadState();
}

loadState();
