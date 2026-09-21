package server

const indexHTML3 = `<script>
function renderIssues(){
  document.getElementById("issueCounts").innerHTML=
    "<b>"+VIEW.unexempted_count+"</b> 未豁免 / 共 "+VIEW.total_issues+
    " <span class='mut'>(快照 "+esc(VIEW.id)+")</span>";
  const tb=document.querySelector("#issueTable tbody"); tb.innerHTML="";
  VIEW.issues.forEach(iv=>{
    const tr=document.createElement("tr");
    const active=iv.exempt_active;
    let ex=iv.exemption?('<span class="tag '+(active?'t-ok':'t-bad')+'">'+
      (active?("豁免至 "+new Date(iv.exemption.deadline*1000).toLocaleString()):"已过期/撤销")+
      '</span><div class="mut">'+esc(iv.exemption.reason)+"</div>"):"";
    tr.innerHTML='<td><input type="checkbox" '+(SELECTED.has(iv.issue.id)?"checked":"")+
      ' data-id="'+esc(iv.issue.id)+'"></td>'+
      '<td><span class="issuecode">'+esc(iv.issue.code)+"</span></td>"+
      "<td>"+esc(iv.issue.language)+"</td>"+
      '<td><a href="#" onclick="pickKey(\''+esc(iv.issue.key)+'\');return false">'+esc(iv.issue.key)+"</a></td>"+
      "<td class='mut'>"+esc(iv.issue.detail||"")+"</td><td>"+ex+"</td>";
    tb.appendChild(tr);
  });
  tb.querySelectorAll("input[data-id]").forEach(cb=>cb.onchange=()=>{
    const id=cb.getAttribute("data-id");
    if(cb.checked)SELECTED.add(id);else SELECTED.delete(id);
  });
}
async function mutateJSON(path,body,op){
  const o=op||uid("op");
  return api(path+"?op="+encodeURIComponent(o),{method:"POST",
    headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
}
function baseReq(){return {base_seq:VIEW.seq};}
async function exemptSelected(){
  const reason=document.getElementById("exReason").value.trim();
  if(!reason){banner("err","需要豁免理由");return;}
  const secs=parseInt(document.getElementById("exSeconds").value||"60",10);
  const deadline=Math.floor(Date.now()/1000)+secs;
  let ok=0,fail=0;
  for(const id of SELECTED){
    try{await mutateJSON("/api/exemption",Object.assign(baseReq(),{
      issue_id:id,reason:reason,deadline:deadline}));ok++;}
    catch(e){fail++;banner("err","豁免冲突: "+e.message);}
  }
  if(ok)banner("ok","已豁免 "+ok+" 项"+(fail?("，"+fail+" 项冲突"):""));
  SELECTED.clear(); await load();
}
async function revokeSelected(){
  for(const id of SELECTED){
    try{await mutateJSON("/api/exemption",Object.assign(baseReq(),{issue_id:id,revoke:true}));}
    catch(e){banner("err",e.message);}
  }
  SELECTED.clear(); await load();
}
function renderRename(){
  const el=document.getElementById("renameArea");
  const props=VIEW.proposals.filter(p=>!p.resolved);
  if(!props.length){el.textContent="没有待确认的改名。";return;}
  let h="";
  props.forEach(p=>{
    h+="<div style='border-top:1px solid #eee;padding-top:8px;margin-top:8px'>"+
      "<div class='mut'>提案 "+esc(p.id.slice(0,18))+" 映射版本 "+
      "<span class='pill'>"+fp8(STATE.mapping_sig)+"</span></div>"+
      "<div>旧-only: "+p.old_only.map(esc).join(", ")+"</div>"+
      "<div>新-only: "+p.new_only.map(esc).join(", ")+"</div>"+
      "<div id='map_"+esc(p.id)+"'></div>"+
      "<button class='ghost' onclick='submitRename(\""+esc(p.id)+"\")'>确认映射</button></div>";
    // rows generated after insert
    setTimeout(()=>buildMapRows(p),0);
  });
  el.innerHTML=h;
}
function buildMapRows(p){
  const box=document.getElementById("map_"+p.id); if(!box)return;
  box.innerHTML=p.old_only.map((old,i)=>
    '<div class="maprow"><input value="'+esc(old)+'" data-old="'+esc(old)+'">'+
    '<span>→</span><select data-new><option value="">（不映射=删除）</option>'+
    p.new_only.map(nn=>'<option value="'+esc(nn)+'"'+(i<p.new_only.length&&nn===p.new_only[i]?' selected':'')+'>'+esc(nn)+"</option>").join("")+
    "</select></div>").join("");
}
async function submitRename(pid){
  const box=document.getElementById("map_"+pid);
  const edges=[];
  box.querySelectorAll(".maprow").forEach(row=>{
    const old=row.querySelector("input[data-old]").value;
    const nw=row.querySelector("select[data-new]").value;
    if(nw)edges.push({old_key:old,new_key:nw});
  });
  try{
    const r=await mutateJSON("/api/rename",{
      proposal_id:pid,edges:edges,base_seq:VIEW.seq,mapping_sig:STATE.mapping_sig});
    banner("ok","改名已确认，新映射版本 "+fp8(r.mapping_sig));
    await load();
  }catch(e){banner("err","改名被拒绝: "+e.message);}
}
function renderBatch(){
  let h="<label>选择要修正的语言（默认选中所有非基准）</label><div>";
  const langs=STATE.languages.filter(l=>!l.is_baseline);
  langs.forEach(l=>{
    h+='<label class="row"><input type="checkbox" class="bl" data-lang="'+esc(l.name)+
      '" data-fp="'+esc(l.current_fp)+'" checked style="width:auto"> '+esc(l.name)+
      " 当前 <span class='pill'>"+fp8(l.current_fp)+"</span></label>";
  });
  h+="</div>";
  document.getElementById("batchLangs").innerHTML=h;
}
function collectBatch(){
  const langs=[...document.querySelectorAll(".bl:checked")].map(cb=>({
    language:cb.getAttribute("data-lang"),expected_fp:cb.getAttribute("data-fp")}));
  return {langs:langs,base_seq:VIEW.seq,mapping_sig:STATE.mapping_sig};
}
async function runBatch(retry){
  const opEl=document.getElementById("batchOp");
  if(!retry){const op=opEl.value.trim()||uid("fix");opEl.value=op;}
  const op=opEl.value.trim()||uid("fix");
  const out=document.getElementById("batchOut");
  try{
    const r=await mutateJSON("/api/batch",collectBatch(),op);
    out.hidden=false;
    out.textContent=JSON.stringify(r.batch.langs.map(l=>({lang:l.language,status:l.status,
      reason:l.reason||"",applied:fp8(l.applied_fp)})),null,2);
    const applied=r.batch.langs.filter(l=>l.status==="applied"||l.status==="already-applied").length;
    const conflicts=r.batch.langs.filter(l=>l.status==="conflict").length;
    banner(applied?"ok":"err","批量修正完成："+applied+" 成功，"+conflicts+" 冲突"+
      (r.idempotent_replay?"（同标识重放，未重复应用）":""));
    await load();
  }catch(e){banner("err","批量请求冲突: "+e.message);}
}
async function genProof(sameKey){
  const opEl=document.getElementById("proofOp");
  if(!sameKey){opEl.value=uid("proof");}
  const op=opEl.value.trim()||uid("proof");
  const asOf=document.getElementById("asOf").value.trim();
  const seq=document.getElementById("seqSel").value.trim();
  const body={seq:seq?parseInt(seq,10):0,as_of:asOf?parseInt(asOf,10):0};
  try{
    const r=await mutateJSON("/api/proof",body,op);
    const out=document.getElementById("proofOut");
    out.hidden=false; out.textContent=(typeof r.bytes==="string"?r.bytes:JSON.stringify(r.bytes,null,2));
    banner("ok","证明 "+esc(r.record.id)+" 快照="+esc(r.record.document.snapshot_id)+
      (r.record.current?" <当前状态>":" <历史状态>")+(r.idempotent_replay?" 字节一致重放":""));
    await load();
  }catch(e){banner("err","证明失败: "+e.message);}
}
function renderProofList(){
  let h="";
  STATE.proofs.forEach(p=>{
    h+='<div><a href="/api/proof/file?id='+encodeURIComponent(p.id)+'" download>'+esc(p.id)+
      "</a> seq="+p.seq+" asOf="+p.as_of+" "+
      '<span class="tag '+(p.current?"t-ok":"t-warn")+'">'+(p.current?"当前":"历史")+"</span> "+
      '<a href="#" onclick="viewProof(\''+esc(p.id)+'\');return false">查看</a></div>';
  });
  document.getElementById("proofList").innerHTML=h;
}
async function viewProof(id){
  const j=await api("/api/proof?id="+encodeURIComponent(id));
  document.getElementById("proofOut").hidden=false;
  document.getElementById("proofOut").textContent=JSON.stringify(j,null,2);
}
async function renderRuns(){
  const j=await api("/api/runs");
  let h="<table><tr><th>run</th><th>seq</th><th>解析器</th><th>基准fp</th><th>问题</th><th>状态</th></tr>";
  j.runs.forEach(r=>{
    h+="<tr><td class='mono'>"+esc(r.id.slice(0,14))+"</td><td>"+r.seq+"</td><td>"+esc(r.parser_version)+
      "</td><td>"+fp8(r.baseline_fp)+"</td><td>"+r.issue_count+"</td>"+
      '<td><span class="tag '+(r.status==="current"?"t-ok":"t-bad")+'">'+esc(r.status)+"</span> "+
      (r.invalid_reason?'<span class="mut">'+esc(r.invalid_reason)+"</span>":"")+
      ' <a href="#" onclick="document.getElementById(\'seqSel\').value='+r.seq+';load();return false">重放</a></td></tr>';
  });
  document.getElementById("runs").innerHTML=h+"</table>";
}
load().catch(e=>banner("err",e.message));
</script>`
