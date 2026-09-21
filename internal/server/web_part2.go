package server

const indexHTML2 = `<script>
async function load(){
  const seq=document.getElementById("seqSel").value.trim();
  STATE=await api("/api/state");
  VIEW=await api("/api/issues"+(seq?("?seq="+seq):""));
  renderState(); renderIssues(); renderKeys(); renderRename(); renderBatch(); renderRuns(); renderProofList();
}
function renderState(){
  const head=document.getElementById("headsnap");
  head.innerHTML="快照 <span class='pill'>"+esc(VIEW.id)+"</span> seq="+VIEW.seq+
    " 解析器="+esc(VIEW.parser_version)+" 基准="+esc(VIEW.baseline)+
    (VIEW.head?"":" <span class='tag t-warn'>历史快照</span>")+
    (VIEW.run_status==="invalid"?(" <span class='tag t-bad'>校验结果已失效: "+esc(VIEW.run_invalid_reason)+"</span>"):"");
  if(STATE.recovery && !STATE.recovery.healthy){
    banner("warn","存储恢复告警（只读）: "+STATE.recovery.notes.join("; "));
  }
  document.getElementById("snapshots").textContent="最近快照 seq: "+(STATE.seq);
  const l=document.getElementById("langs"); let h="";
  STATE.languages.forEach(ls=>{
    h+="<div style='margin:6px 0'><b>"+esc(ls.name)+"</b> "+
      (ls.is_baseline?"<span class='tag t-mut'>基准</span>":"")+
      " <span class='pill'>"+fp8(ls.current_fp)+"</span><div style='margin-top:3px'>";
    ls.imports.forEach((im,i)=>{
      h+="<a class='small' href='/api/raw?sha="+encodeURIComponent(im.raw.blob_sha)+
        "' download>下载第"+(i+1)+"次原文</a> <span class='pill'>"+fp8(im.raw.blob_sha)+
        "</span><br>";
    });
    h+="</div></div>";
  });
  l.innerHTML=h;
}
function renderKeys(){
  const k=document.getElementById("keys");
  const names=new Set();
  VIEW.issues.forEach(i=>names.add(i.issue.key));
  k.innerHTML="问题涉及的 key（点击查看）: "+[...names].slice(0,40)
    .map(n=>"<a href='#' onclick='pickKey(\""+esc(n)+"\");return false'>"+esc(n)+"</a>").join(" · ");
}
function pickKey(k){document.getElementById("keyInput").value=k;showKey();}
async function showKey(){
  const key=document.getElementById("keyInput").value.trim();
  if(!key)return;
  const seq=document.getElementById("seqSel").value.trim();
  const kv=await api("/api/key?key="+encodeURIComponent(key)+(seq?("&seq="+seq):""));
  let h="<table><tr><th>语言</th><th>版本</th><th>结构</th></tr>";
  kv.languages.forEach(ls=>{
    if(!ls.message){h+="<tr><td>"+esc(ls.language)+"</td><td>"+fp8(ls.version)+"</td><td><span class='tag t-bad'>缺项</span></td></tr>";return;}
    const m=ls.message;
    let ast=m.ast||{};
    let body="<div class='mono'>"+esc(m.text)+"</div>";
    if(m.context)body+="<div class='mut'>ctx: "+esc(m.context)+"</div>";
    if(ast.error)body+="<div class='tag t-bad'>解析错误: "+esc(ast.error)+"</div>";
    body+="<div class='small'>占位符: "+esc((ast.params||[]).map(p=>p.name+":"+p.type).join(", ")||"-")+"</div>";
    (ast.plurals||[]).forEach(pl=>body+="<div class='small'>复数 "+esc(pl.name)+": "+esc((pl.categories||[]).join(", "))+"</div>");
    body+="<div class='small'>标签: "+esc((ast.tags||[]).join(", ")||"-")+"</div>";
    if(ast.unbalanced&&ast.unbalanced.length)body+="<div class='tag t-bad'>标签不平衡: "+esc(JSON.stringify(ast.unbalanced))+"</div>";
    if(ls.found_at&&ls.found_at!==key)body+="<div class='tag t-warn'>沿用自旧 key: "+esc(ls.found_at)+"</div>";
    h+="<tr><td>"+esc(ls.language)+"</td><td><span class='pill'>"+fp8(ls.version)+"</span></td><td>"+body+"</td></tr>";
  });
  h+="</table>";
  document.getElementById("keyDetail").innerHTML=h;
}
async function doImport(){
  const f=document.getElementById("file").files[0];
  if(!f){banner("err","请选择文件");return;}
  const lang=document.getElementById("lang").value.trim();
  const op=document.getElementById("importOp").value.trim()||uid("imp");
  document.getElementById("importOp").value=op;
  const fd=new FormData();
  fd.append("language",lang);
  fd.append("baseline",document.getElementById("baseline").checked?"true":"false");
  fd.append("file",f);
  try{
    const r=await api("/api/import?op="+encodeURIComponent(op),{method:"POST",body:fd});
    document.getElementById("importMsg").innerHTML=
      "<span class='tag "+(r.new_version?"t-ok":"t-mut")+"'>"+
      (r.new_version?"生成新版本":"内容版本相同（未生成新版本）")+"</span> "+
      (r.idempotent_replay?"<span class='tag t-warn'>幂等重放</span>":"")+
      " fp=<span class='pill'>"+fp8(r.fingerprint)+"</span> 快照 "+esc(r.snapshot_id)+
      (r.proposal?" <span class='tag t-warn'>产生待确认改名</span>":"");
    banner("ok","导入成功: "+lang);
    document.getElementById("seqSel").value="";
    await load();
  }catch(e){banner("err","导入失败: "+e.message);}
}
function setAsOffset(){
  const base=Math.floor(Date.now()/1000);
  document.getElementById("asOf").value=base+parseInt(document.getElementById("exSeconds").value||"60",10);
}
</script>`
