"use strict";

const state = { view: null, snapshot: "", issues: null };

const $ = (id) => document.getElementById(id);

function toast(msg, kind = "info") {
  const box = $("toast");
  const el = document.createElement("div");
  el.className = "toast-item " + kind;
  el.textContent = msg;
  box.appendChild(el);
  setTimeout(() => el.remove(), 5200);
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[c]);
}

async function api(path, opts = {}) {
  const res = await fetch(path, opts);
  let body = null;
  try { body = await res.json(); } catch (_) {}
  if (!res.ok) {
    const detail = body && (body.detail || body.error) ? (body.detail || body.error) : ("HTTP " + res.status);
    const err = new Error(detail);
    err.body = body;
    err.status = res.status;
    throw err;
  }
  return body;
}

function short(v, n = 12) {
  v = String(v ?? "");
  return v.length > n ? v.slice(0, n) + "…" : v;
}

async function refresh() {
  try {
    state.view = await api("/api/state");
    state.snapshot = state.view.pin.snapshot;
    renderAll();
  } catch (e) {
    toast("加载状态失败: " + e.message, "bad");
  }
}

function renderAll() {
  const v = state.view;
  $("snapshot").textContent = short(v.pin.snapshot, 18) + "  (seq " + v.pin.seq + ")";
  document.querySelectorAll('input[name=expected_snapshot], #batch-snap').forEach((i) => { i.value = v.pin.snapshot; });
  renderCatalogs();
  renderUploads();
  renderRenames();
  renderExemptions();
  renderLangSelect();
  renderCerts();
  renderNotes();
}

function renderNotes() {
  const host = $("catalogs");
  const notes = state.view.recovery_notes || [];
  const existing = document.getElementById("notes-block");
  if (existing) existing.remove();
  if (notes.length) {
    const div = document.createElement("div");
    div.id = "notes-block";
    div.className = "notes";
    div.innerHTML = "启动恢复报告：<br>" + notes.map(esc).join("<br>");
    host.prepend(div);
  }
}

function renderCatalogs() {
  const host = $("catalogs");
  const v = state.view;
  const rows = [];
  if (v.baseline) {
    rows.push(`<tr><td><span class="tag info">基准</span></td><td class="badge-lang">${esc(v.baseline.language)}</td><td title="${esc(v.baseline.version)}">${short(v.baseline.version, 14)}</td><td>${esc(v.baseline.filename || "")}</td><td>${new Date(v.baseline.imported).toLocaleString()}</td></tr>`);
  }
  (v.languages || []).forEach((l) => {
    rows.push(`<tr><td><span class="tag ok">目标</span></td><td class="badge-lang">${esc(l.language)}</td><td title="${esc(l.version)}">${short(l.version, 14)}</td><td>${esc(l.filename || "")}</td><td>${new Date(l.imported).toLocaleString()}</td></tr>`);
  });
  host.innerHTML = `<table><thead><tr><th>角色</th><th>语言</th><th>内容版本(指纹)</th><th>文件</th><th>导入时间</th></tr></thead><tbody>${rows.join("")}</tbody></table>` + hostNotes();
}
function hostNotes() {
  const notes = state.view.recovery_notes || [];
  if (!notes.length) return "";
  return `<div class="notes" id="notes-block" style="margin-top:8px">启动恢复报告：<br>${notes.map(esc).join("<br>")}</div>`;
}

function renderUploads() {
  const tb = $("uploads-table").querySelector("tbody");
  tb.innerHTML = "";
  api("/api/uploads").then((u) => {
    (u.uploads || []).forEach((up) => {
      const tr = document.createElement("tr");
      tr.innerHTML = `<td>${new Date(up.at).toLocaleString()}</td><td>${esc(up.role)}</td><td class="badge-lang">${esc(up.language)}</td><td title="${esc(up.version)}">${short(up.version, 12)}</td><td>${esc(up.filename || "")}</td>
      <td><a class="small" href="/api/uploads/${encodeURIComponent(up.id)}/raw">下载原文</a></td>`;
      tb.appendChild(tr);
    });
  }).catch(() => {});
}

function renderRenames() {
  const rs = state.view.renames || [];
  $("renames").innerHTML = rs.length
    ? `<table><thead><tr><th>旧 key</th><th></th><th>新 key</th><th>确认时间</th></tr></thead><tbody>${
        rs.map((r) => `<tr><td>${esc(r.old_key)}</td><td>→</td><td>${esc(r.new_key)}</td><td>${new Date(r.confirmed_at).toLocaleString()}</td></tr>`).join("")
      }</tbody></table>`
    : `<p class="muted">尚无改名映射。基准改名不会自动删除/新增，必须在此显式确认。</p>`;
}

function renderExemptions() {
  api("/api/exemptions").then((r) => {
    const exs = r.exemptions || [];
    $("exemptions").innerHTML = exs.length
      ? `<table><thead><tr><th>问题指纹</th><th>理由</th><th>到期</th><th></th></tr></thead><tbody>${
          exs.map((e) => `<tr><td title="${esc(e.issue_key)}">${short(e.issue_key, 16)}</td><td>${esc(e.reason)}</td><td>${e.expires_at ? new Date(e.expires_at).toLocaleString() : "永久"}</td><td><button class="small secondary" data-revoke="${esc(e.issue_key)}">撤回</button></td></tr>`).join("")
        }</tbody></table>`
      : `<p class="muted">尚无豁免。可在问题列表对单条问题设置带期限的豁免。</p>`;
    document.querySelectorAll("[data-revoke]").forEach((b) => {
      b.onclick = () => revokeExempt(b.getAttribute("data-revoke"));
    });
  }).catch(() => {});
}

function renderLangSelect() {
  const sel = $("issue-lang");
  const cur = sel.value;
  const langs = (state.view.languages || []).map((l) => l.language);
  sel.innerHTML = langs.map((l) => `<option value="${esc(l)}">${esc(l)}</option>`).join("");
  if (langs.includes(cur)) sel.value = cur;
}

function issueLabel(code) {
  return ({
    missing_key: "缺项", extra_key: "多项", placeholder_mismatch: "占位符集合不一致",
    type_drift: "占位符类型漂移", plural_categories: "复数类别缺失",
    tags_unbalanced: "富文本标签不平衡", icu_syntax: "ICU 语法",
  })[code] || code;
}

async function loadIssues() {
  const lang = $("issue-lang").value;
  if (!lang) { toast("先导入一个目标语言", "bad"); return; }
  try {
    state.issues = await api(`/api/issues/${encodeURIComponent(lang)}`);
    renderIssues();
  } catch (e) { toast("校验失败: " + e.message, "bad"); }
}

function renderIssues() {
  const r = state.issues;
  const meta = $("val-meta");
  meta.textContent = `结果 ${short(r.validation_id, 16)} · 解析器 ${esc(r.pin.parser)} · 基准 ${short(r.pin.baseline_version, 10)} · 未豁免 ${r.unexempted_count} / 已豁免 ${r.exempted_count}`;
  const inv = $("val-invalid");
  if (r.current) {
    inv.classList.add("hidden");
  } else {
    inv.classList.remove("hidden");
    inv.textContent = "该结果已失效（" + r.invalid_reason + "）。仍可查看固定的旧结果，但基于它的写操作必须依据当前状态重新校验。";
  }
  const tb = $("issues-table").querySelector("tbody");
  tb.innerHTML = "";
  if (!r.issues.length) {
    tb.innerHTML = `<tr><td colspan="6" class="muted">没有发现问题</td></tr>`;
    return;
  }
  r.issues.forEach((iv) => {
    const tr = document.createElement("tr");
    const exp = iv.expected && iv.expected.length ? `期望 [${iv.expected.map(esc).join(", ")}]` : "";
    tr.innerHTML = `<td>${esc(iv.key)}</td>
      <td><span class="tag ${iv.exempted ? "warn" : "bad"}">${esc(issueLabel(iv.code))}</span></td>
      <td>${esc(iv.detail)}</td>
      <td class="muted">${exp} ${iv.actual && iv.actual.length ? "实际 [" + iv.actual.map(esc).join(", ") + "]" : ""}</td>
      <td>${iv.exempted ? `<span class="tag warn">已豁免${iv.exemption.expires_at ? " 至 " + new Date(iv.exemption.expires_at).toLocaleString() : ""}</span>` : ""}</td>
      <td>${iv.exempted
        ? `<button class="small secondary" data-revokeissue="${esc(iv.id)}">撤回</button>`
        : `<button class="small" data-issue='${esc(JSON.stringify({ key: iv.id, text: iv.key + " / " + issueLabel(iv.code) }))}'>豁免…</button>`}</td>`;
    tb.appendChild(tr);
  });
  tb.querySelectorAll("[data-issue]").forEach((b) => {
    b.onclick = () => openExempt(JSON.parse(b.getAttribute("data-issue")));
  });
  tb.querySelectorAll("[data-revokeissue]").forEach((b) => {
    b.onclick = () => revokeExempt(b.getAttribute("data-revokeissue"));
  });
}

function openExempt(info) {
  const dlg = $("exempt-dialog");
  $("exempt-issue").textContent = info.text + "  (" + short(info.key, 20) + ")";
  const f = $("form-exempt");
  f.issue_key.value = info.key;
  f.expected_snapshot.value = state.snapshot;
  f.op_id.value = "ex-" + cryptoId();
  f.reason.value = "";
  f.expires_at.value = "";
  dlg.showModal();
}

function cryptoId() {
  if (window.crypto && crypto.randomUUID) return crypto.randomUUID();
  return "id-" + Math.random().toString(16).slice(2) + Date.now().toString(16);
}

async function revokeExempt(issueKey) {
  try {
    await api("/api/exemptions/revoke", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ issue_key: issueKey, expected_snapshot: state.snapshot, op_id: "rv-" + cryptoId() }),
    });
    toast("豁免已撤回", "ok");
    await refresh();
    if (state.issues) await loadIssues();
  } catch (e) { toast("撤回失败: " + e.message, "bad"); }
}

function renderStructure(msg) {
  if (!msg) return `<span class="muted">（该语言缺少此 key）</span>`;
  const ph = (msg.parsed.placeholders || []).map((p) => {
    const cases = p.cases ? `<div class="muted">分支: ${Object.keys(p.cases).map(esc).join(", ")}</div>` : "";
    return `<li><code>${esc(p.name)}</code> ${p.type ? "[" + esc(p.type) + "]" : ""}${cases}</li>`;
  }).join("");
  const tags = (msg.parsed.tags || []).map(esc).join(", ");
  const errs = (msg.parsed.parse_errors || []);
  return `<div class="kv">版本 <code>${esc(msg.version)}</code></div>
    <div class="raw-pattern">${esc(msg.raw)}</div>
    ${msg.context ? `<div class="muted">上下文: ${esc(msg.context)}</div>` : ""}
    <div class="struct"><b>占位符</b><ul>${ph || "<span class=muted>无</span>"}</ul>
    <b>标签</b>: ${tags ? esc(tags) : '<span class="muted">无</span>'}
    ${errs.length ? `<div class="conflict">${errs.map(esc).join("<br>")}</div>` : ""}</div>`;
}

async function showKey() {
  const key = $("key-input").value.trim();
  if (!key) return;
  try {
    const v = await api(`/api/keys/${encodeURIComponent(key)}`);
    const host = $("key-view");
    const targetRows = Object.keys(v.targets || {}).sort().map((l) =>
      `<div class="msg-box"><h4><span class="badge-lang">${esc(l)}</span></h4>${renderStructure(v.targets[l])}</div>`
    ).join("");
    host.innerHTML = `<div class="muted">快照 ${short(v.pin.snapshot, 18)}${(v.renamed_from || []).length ? " · 由 " + v.renamed_from.map(esc).join(" → ") + " 沿用" : ""}</div>
      <div class="msg-box"><h4>基准 ${v.baseline ? esc(v.baseline.language) : ""}</h4>${v.baseline ? renderStructure(v.baseline) : '<span class="muted">基准中不存在此 key</span>'}</div>
      ${targetRows || '<p class="muted">目标语言均无此 key</p>'}`;
  } catch (e) { toast("查看失败: " + e.message, "bad"); }
}

async function doImport(ev) {
  ev.preventDefault();
  const f = ev.target;
  const fd = new FormData();
  fd.append("role", f.role.value);
  fd.append("language", f.language.value.trim());
  fd.append("op_id", f.op_id.value.trim());
  fd.append("expected_snapshot", state.snapshot);
  fd.append("file", f.file.files[0]);
  try {
    const res = await api("/api/import", { method: "POST", body: fd });
    $("import-result").textContent = JSON.stringify(res, null, 2);
    toast(res.reused_content ? "内容版本未变化（原文已保存，可下载）" : "已生成新内容版本", "ok");
    await refresh();
  } catch (e) {
    $("import-result").textContent = JSON.stringify(e.body || e.message, null, 2);
    toast("导入失败: " + e.message, "bad");
  }
}

async function doRename(ev) {
  ev.preventDefault();
  const f = ev.target;
  try {
    const res = await api("/api/renames", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        old_key: f.old_key.value, new_key: f.new_key.value,
        from_version: f.from_version.value, expected_snapshot: f.expected_snapshot.value,
        op_id: f.op_id.value || undefined,
      }),
    });
    toast("改名映射已确认", "ok");
    $("form-rename").reset();
    await refresh();
  } catch (e) {
    toast("改名被拒绝: " + e.message, "bad");
    if (e.status === 409 && e.body && e.body.current) {
      toast("当前快照: " + short(e.body.current.snapshot, 18) + "，旧确认未套用", "info");
    }
  }
}

async function submitExempt(ev) {
  ev.preventDefault();
  const f = ev.target;
  const body = {
    issue_key: f.issue_key.value, reason: f.reason.value,
    expected_snapshot: f.expected_snapshot.value, op_id: f.op_id.value,
  };
  if (f.expires_at.value) body.expires_at = new Date(f.expires_at.value).toISOString();
  try {
    await api("/api/exemptions", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    toast("豁免已保存", "ok");
    await refresh();
    if (state.issues) await loadIssues();
  } catch (e) { toast("保存豁免失败: " + e.message, "bad"); }
}

function addBatchRow(language = "", base = "", content = "{}") {
  const tb = $("batch-items").querySelector("tbody");
  const tr = document.createElement("tr");
  tr.innerHTML = `<td><input class="b-lang" value="${esc(language)}"></td>
    <td><input class="b-base" value="${esc(base)}" placeholder="当前版本指纹"></td>
    <td><textarea class="batch b-content">${esc(content)}</textarea></td>`;
  tb.appendChild(tr);
}

async function submitBatch() {
  const items = [...$("batch-items").querySelectorAll("tr")].map((tr) => ({
    language: tr.querySelector(".b-lang").value.trim(),
    base_version: tr.querySelector(".b-base").value.trim(),
    content: tr.querySelector(".b-content").value,
  })).filter((x) => x.language);
  try {
    const res = await api("/api/batch-fixes", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ items, op_id: $("batch-op").value.trim() || undefined, expected_snapshot: state.snapshot }),
    });
    $("batch-result").textContent = JSON.stringify(res, null, 2);
    const failed = res.items.filter((i) => i.status !== "success");
    if (!failed.length) toast("批量修正全部成功", "ok");
    else toast(`部分成功：${res.items.length - failed.length} 成功，${failed.length} 冲突（已保留成功项）`, "bad");
    await refresh();
  } catch (e) {
    $("batch-result").textContent = JSON.stringify(e.body || e.message, null, 2);
    toast("批量修正被拒绝: " + e.message, "bad");
  }
}

function renderCerts() {
  const tb = $("certs-table").querySelector("tbody");
  tb.innerHTML = "";
  (state.view.certs || []).forEach((c) => {
    const tr = document.createElement("tr");
    tr.innerHTML = `<td title="${esc(c.id)}"><a href="#" data-cert="${esc(c.id)}">${short(c.id, 18)}</a></td>
      <td title="${esc(c.snapshot)}">${short(c.snapshot, 14)}</td>
      <td>${new Date(c.created_at).toLocaleString()}</td>
      <td><span class="tag ${c.current ? "ok" : "info"}">${c.current ? "对应当前状态" : "历史证明"}</span></td>
      <td><a href="/api/certs/${encodeURIComponent(c.id)}/download">下载</a></td>`;
    tb.appendChild(tr);
  });
  tb.querySelectorAll("[data-cert]").forEach((a) => {
    a.onclick = async (ev) => { ev.preventDefault(); await showCert(a.getAttribute("data-cert")); };
  });
}

async function genCert() {
  try {
    const res = await api("/api/certs", { method: "POST" });
    $("cert-view").textContent = JSON.stringify(res.cert, null, 2);
    $("cert-status").textContent = (res.replayed ? "重放既有证明（字节一致）" : "已生成新证明") +
      " · 快照 " + short(res.snapshot.snapshot, 16) + " · 未豁免 " + res.cert.unexempted_issue_count;
    toast("发布证明已就绪", "ok");
    await refresh();
  } catch (e) {
    toast("生成证明失败: " + e.message, "bad");
  }
}

async function showCert(id) {
  try {
    const res = await api(`/api/certs/${encodeURIComponent(id)}`);
    $("cert-view").textContent = JSON.stringify(res.cert, null, 2);
    $("cert-status").textContent = (res.current ? "对应当前状态" : "历史证明") + " · 快照 " + short(res.snapshot, 16);
  } catch (e) { toast("读取证明失败: " + e.message, "bad"); }
}

async function loadDemo() {
  try {
    const res = await api("/api/demo-data", { method: "POST" });
    toast("演示数据已导入（en/ja/fr）", "ok");
    await refresh();
    $("issue-lang").value = "ja";
  } catch (e) { toast("演示数据失败: " + e.message, "bad"); }
}

function bind() {
  $("btn-refresh").onclick = refresh;
  $("btn-demo").onclick = loadDemo;
  $("form-import").onsubmit = doImport;
  $("btn-issues").onclick = loadIssues;
  $("btn-key").onclick = showKey;
  $("key-input").addEventListener("keydown", (e) => { if (e.key === "Enter") showKey(); });
  $("form-rename").onsubmit = doRename;
  $("form-exempt").onsubmit = submitExempt;
  $("btn-batch-add").onclick = () => addBatchRow();
  $("btn-batch").onclick = submitBatch;
  $("btn-cert").onclick = genCert;
  addBatchRow();
}

document.addEventListener("DOMContentLoaded", async () => {
  bind();
  await refresh();
});
