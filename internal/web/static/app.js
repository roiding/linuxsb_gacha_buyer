/* gacha-buyer 控制台前端逻辑 */
const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => document.querySelectorAll(sel);

// ---- Tab 切换 ----
$$(".tabs button").forEach(btn => {
  btn.addEventListener("click", () => {
    $$(".tabs button").forEach(b => b.classList.remove("active"));
    $$(".tab").forEach(t => t.classList.remove("active"));
    btn.classList.add("active");
    $("#tab-" + btn.dataset.tab).classList.add("active");
  });
});

async function api(path, opts) {
  const r = await fetch(path, opts);
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data.error || ("HTTP " + r.status));
  return data;
}

let toastTimer;
function notify(message, error = false) {
  const el = $("#toast");
  el.textContent = message;
  el.className = "toast show" + (error ? " error" : "");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.className = "toast"; }, 2800);
}

async function withBusy(button, task) {
  const label = button.textContent;
  button.disabled = true;
  button.textContent = "处理中…";
  try { return await task(); }
  finally { button.disabled = false; button.textContent = label; }
}

function rarityBadge(r) {
  if (!r) return "-";
  return `<span class="r-${r}">${r.toUpperCase()}</span>`;
}

// ---- 状态轮询 ----
async function refreshStatus() {
  try {
    const s = await api("/api/status");
    const pill = $("#engine-state");
    pill.textContent = s.running ? "运行中" + (s.dry_run ? "（dry-run）" : "") : "已停止";
    pill.className = "pill " + (s.running ? "on" : "off");
    $("#points").textContent = s.points > 0 ? s.points.toLocaleString() : "-";
    $("#min-balance").textContent = s.min_balance;
    $("#listing-count").textContent = (s.listings || []).length;
    $("#buy-ok").textContent = s.buy_ok;
    $("#last-scan").textContent = s.last_scan_at || "-";
    const rules = s.rules || {};
    $("#rules-brief").textContent =
      `SR≤${rules.sr ?? 0} / R≤${rules.r ?? 0} / N≤${rules.n ?? 0}` +
      `（SSR 按名称定向，UR≤${rules.ur ?? 0}），余额保护线 ${s.min_balance} 分` +
      (s.dry_run ? "，当前 dry-run" : "");
    const errBox = $("#err-box");
    if (s.last_error) { errBox.hidden = false; errBox.textContent = "最近错误：" + s.last_error; }
    else errBox.hidden = true;

    renderMarket(s.listings || [], s.rules || {}, s.ssr_prices || {});
    // 设置页 dry-run 开关同步
    $("#dry-run-toggle").checked = !!s.dry_run;
  } catch (e) { console.error(e); }
}

function renderMarket(listings, rules, ssrPrices) {
  const tb = $("#market-table tbody");
  tb.innerHTML = "";
  if (!listings.length) {
    tb.innerHTML = `<tr><td colspan="5" class="empty">尚无快照。点击“立即扫一轮”后会显示最新市场数据。</td></tr>`;
    return;
  }
  for (const l of listings) {
    const limit = l.rarity === "ssr" ? (ssrPrices[l.name] ?? 0) : (rules[l.rarity] ?? 0);
    const hit = limit > 0 && l.price <= limit && l.remain > 0;
    const tr = document.createElement("tr");
    tr.innerHTML = `<td>${l.emoji || ""} ${esc(l.name)}</td>
      <td>${rarityBadge(l.rarity)}</td>
      <td>${l.price}</td><td>${l.remain}</td>
      <td class="${hit ? "hit" : "miss"}">${hit ? `✓ 收（限 ≤${limit}）` : "—"}</td>`;
    tb.appendChild(tr);
  }
}

async function refreshRecords() {
  try {
    const d = await api("/api/purchases?limit=300");
    $("#total-spent").textContent = d.total_spent;
    $("#ok-count").textContent = d.ok_count;
    const tb = $("#records-table tbody");
    tb.innerHTML = "";
    for (const p of d.records || []) {
      const cls = !p.ok ? "fail" : (p.dry_run ? "dry" : "hit");
      const label = p.dry_run ? "[dry] 将购买" : (p.ok ? "✓ 成交" : "✗ 失败");
      const tr = document.createElement("tr");
      tr.innerHTML = `<td>${p.time.replace("T", " ").slice(0, 19)}</td>
        <td>${esc(p.name)}</td><td>${rarityBadge(p.rarity)}</td>
        <td>${p.price} × ${p.qty}</td><td>${p.cost}</td>
        <td class="${cls}">${label}${p.message ? " · " + esc(p.message) : ""}</td>`;
      tb.appendChild(tr);
    }
  } catch (e) { console.error(e); }
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g,
    c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

// ---- 引擎控制 ----
$("#btn-start").addEventListener("click", async () => {
  try { await api("/api/engine/start", { method: "POST" }); refreshStatus(); }
  catch (e) { alert(e.message); }
});
$("#btn-stop").addEventListener("click", async () => {
  try { await api("/api/engine/stop", { method: "POST" }); refreshStatus(); }
  catch (e) { alert(e.message); }
});
$("#btn-scan").addEventListener("click", async () => {
  try {
    const d = await api("/api/engine/scan", { method: "POST" });
    if (!d.ok && d.error) alert(d.error);
    refreshStatus();
  } catch (e) { alert(e.message); }
});
$("#dry-run-toggle").addEventListener("change", async (ev) => {
  try {
    await api("/api/config", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ dry_run: ev.target.checked }),
    });
    refreshStatus();
  } catch (e) { alert(e.message); ev.target.checked = !ev.target.checked; }
});

// ---- 设置 ----
async function loadConfig() {
  try {
    const c = await api("/api/config");
    const f = $("#cfg-form");
    f.sr.value = c.rules.sr; f.r.value = c.rules.r; f.n.value = c.rules.n;
    f.ur.value = c.rules.ur;
    f.min_balance.value = c.min_balance;
    f.scan_sec.value = c.scan_sec;
  } catch (e) { console.error(e); }
}

$("#cfg-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {
    rules: { sr: +f.sr.value, r: +f.r.value, n: +f.n.value, ur: +f.ur.value },
    min_balance: +f.min_balance.value,
    scan_sec: +f.scan_sec.value,
  };
  try {
    await api("/api/config", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    $("#save-msg").textContent = "已保存 ✓";
    setTimeout(() => $("#save-msg").textContent = "", 2500);
    refreshStatus();
  } catch (e) { alert(e.message); }
});

// ---- SSR 定向收集 ----
let ssrTitles = [];
async function loadCatalog() {
  const box = $("#ssr-catalog");
  box.innerHTML = `<p class="hint">正在读取 linux.sb 称号目录…</p>`;
  try {
    const d = await api("/api/catalog");
    ssrTitles = d.titles || [];
    const prices = d.prices || {};
    box.innerHTML = "";
    if (!ssrTitles.length) {
      box.innerHTML = `<p class="empty">目录中没有发现 SSR。</p>`;
      return;
    }
    for (const t of ssrTitles) {
      const label = document.createElement("label");
      label.className = "ssr-card";
      label.innerHTML = `<span class="ssr-title"><b>${esc(t.emoji || "")}</b> ${esc(t.name)}</span><span class="ssr-rarity">SSR</span><input class="num" type="number" min="1" data-ssr-name="${esc(t.name)}" value="${prices[t.name] || ""}" placeholder="不收购">`;
      box.appendChild(label);
    }
  } catch (e) {
    box.innerHTML = `<p class="fail">${esc(e.message)}</p>`;
  }
}

$("#btn-load-catalog").addEventListener("click", async (ev) => {
  try { await withBusy(ev.currentTarget, loadCatalog); }
  catch (e) { notify(e.message, true); }
});
$("#btn-save-ssr").addEventListener("click", async (ev) => {
  const prices = {};
  $("#ssr-catalog").querySelectorAll("[data-ssr-name]").forEach(input => {
    const n = Number(input.value);
    if (n > 0) prices[input.dataset.ssrName] = n;
  });
  try {
    await withBusy(ev.currentTarget, () => api("/api/config", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ssr_prices: prices }) }));
    $("#ssr-save-msg").textContent = "已保存 ✓";
    notify("SSR 定向价格已保存");
    refreshStatus();
    setTimeout(() => $("#ssr-save-msg").textContent = "", 2500);
  } catch (e) { notify(e.message, true); }
});

// ---- 账号管理 ----
const statusLabel = { ok: "✓ 正常", expired: "◌ 掉线", error: "✗ 异常" };
const statusClass = { ok: "hit", expired: "dry", error: "fail" };
function statusBadge(st) {
  return `<span class="${statusClass[st] || ""}">${statusLabel[st] || st || "未登录"}</span>`;
}

async function refreshAccounts() {
  try {
    const d = await api("/api/accounts");
    const mainTb = $("#main-acct-table tbody");
    const subTb = $("#sub-acct-table tbody");
    mainTb.innerHTML = ""; subTb.innerHTML = "";
    for (const a of d.accounts || []) {
      const ops = [];
      if (a.status !== "ok") ops.push(`<button data-recover="${a.id}" data-label="${esc(a.username)}" class="small">恢复</button>`);
      if (a.status === "ok") ops.push(`<button data-logout="${a.id}" data-label="${esc(a.username)}" class="small">退出</button>`);
      if (a.role === "main") {
        mainTb.insertAdjacentHTML("beforeend", `<tr><td>${esc(a.username)}</td><td>${statusBadge(a.status)}</td>
          <td>${a.uid || "-"}</td><td>${esc(a.last_seen || "-")}</td><td class="hint">${esc(a.message || "")}</td><td>${ops.join(" ")}</td></tr>`);
      } else {
        subTb.insertAdjacentHTML("beforeend", `<tr><td>${esc(a.username)}</td><td>${esc(a.note || "")}</td>
          <td><button data-toggle="${a.id}" class="switch ${a.enabled ? "on" : ""}">${a.enabled ? "启用" : "停用"}</button></td>
          <td>${statusBadge(a.status)}</td><td>${a.uid || "-"}</td><td>${esc(a.last_seen || "-")}</td>
          <td class="hint">${esc(a.message || "")}</td><td>${ops.join(" ")} <button data-delete="${a.id}" data-label="${esc(a.username)}" class="small danger">删除</button></td></tr>`);
      }
    }
    if (!mainTb.children.length) mainTb.innerHTML = `<tr><td colspan="6" class="empty">尚未设置主号，请使用下方表单添加。</td></tr>`;
    if (!subTb.children.length) subTb.innerHTML = `<tr><td colspan="8" class="empty">尚未添加小号。</td></tr>`;
    const col = d.collector || {};
    const f = $("#collector-form");
    f.topic_id.value = col.topic_id || 0;
    f.keep.value = col.keep ?? 5;
    f.at_hour.value = col.at_hour ?? 9;
    f.tip_message.value = col.message || "";
  } catch (e) { console.error(e); }
}

async function accountAction(path, id) {
  return api(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: +id }) });
}

document.addEventListener("click", async (ev) => {
  const rec = ev.target.closest("[data-recover]");
  if (rec) {
    if (!confirm(`确认重新登录并恢复 ${rec.dataset.label}？`)) return;
    try { const d = await accountAction("/api/accounts/recover", rec.dataset.recover); alert(d.ok ? "已恢复 ✓" : d.error); refreshAccounts(); } catch (e) { alert(e.message); }
    return;
  }
  const lo = ev.target.closest("[data-logout]");
  if (lo) {
    if (!confirm(`确认让 ${lo.dataset.label} 退出站点登录？`)) return;
    try { const d = await accountAction("/api/accounts/logout", lo.dataset.logout); alert(d.ok ? "已退出 ✓" : d.error); refreshAccounts(); } catch (e) { alert(e.message); }
    return;
  }
  const toggle = ev.target.closest("[data-toggle]");
  if (toggle) {
    try { await api("/api/accounts", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action: "toggle", id: +toggle.dataset.toggle }) }); refreshAccounts(); } catch (e) { alert(e.message); }
    return;
  }
  const del = ev.target.closest("[data-delete]");
  if (del) {
    if (!confirm(`确认删除小号 ${del.dataset.label}？此操作不会删除站点账号。`)) return;
    try { await api("/api/accounts", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action: "delete", id: +del.dataset.delete }) }); refreshAccounts(); } catch (e) { alert(e.message); }
  }
});

$("#btn-patrol").addEventListener("click", async () => {
  try { await api("/api/accounts/patrol", { method: "POST" }); setTimeout(refreshAccounts, 3000); setTimeout(refreshAccounts, 8000); } catch (e) { alert(e.message); }
});

$("#main-acct-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  if (!f.main_username.value.trim() && !f.main_password.value) { alert("请至少填写一项要修改的主号信息"); return; }
  const body = {};
  if (f.main_username.value.trim()) body.main_username = f.main_username.value.trim();
  if (f.main_password.value) body.main_password = f.main_password.value;
  try {
    await api("/api/accounts", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    f.reset(); f.querySelector(".form-msg").textContent = "已保存 ✓"; refreshAccounts();
  } catch (e) { alert(e.message); }
});

$("#sub-acct-form").addEventListener("submit", async (ev) => {
  ev.preventDefault(); const f = ev.target;
  try {
    await api("/api/accounts", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action: "add", sub: { username: f.sub_username.value.trim(), password: f.sub_password.value, note: f.sub_note.value.trim(), enabled: true } }) });
    f.reset(); refreshAccounts();
  } catch (e) { alert(e.message); }
});

$("#collector-form").addEventListener("submit", async (ev) => {
  ev.preventDefault(); const f = ev.target;
  try {
    await api("/api/accounts", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ collector: { topic_id: +f.topic_id.value || 0, keep: +f.keep.value, at_hour: +f.at_hour.value, message: f.tip_message.value } }) });
    alert("归集设置已保存 ✓"); refreshAccounts();
  } catch (e) { alert(e.message); }
});

async function refreshTransfers() {
  try {
    const d = await api("/api/transfers");
    const s = d.status || {};
    $("#collector-status").textContent =
      (s.running ? `调度中 · 下次 ${s.next_run || "-"}` : "调度未运行") +
      (s.last_run ? ` · 上次执行 ${s.last_run}` : "");
    const tb = $("#transfers-table tbody");
    tb.innerHTML = "";
    for (const t of d.transfers || []) {
      const cls = !t.ok ? "fail" : (t.dry_run ? "dry" : "hit");
      const tr = document.createElement("tr");
      tr.innerHTML = `<td>${(t.time || "").replace("T", " ").slice(0, 19)}</td>
        <td>${esc(t.sub)}</td><td>${t.check_in ? "✓" : "-"}</td>
        <td>${t.balance}</td><td>${t.tip_amount || "-"}</td>
        <td>${t.topic_id ? "#" + t.topic_id : "-"}</td>
        <td class="${cls}">${t.dry_run ? "[dry] " : ""}${esc(t.message || "")}</td>`;
      tb.appendChild(tr);
    }
  } catch (e) { console.error(e); }
}

$("#btn-collector-run").addEventListener("click", async () => {
  try {
    await api("/api/collector/run", { method: "POST" });
    setTimeout(refreshTransfers, 5000);
    setTimeout(refreshTransfers, 20000);
  } catch (e) { alert(e.message); }
});

refreshStatus();
refreshRecords();
refreshAccounts();
refreshTransfers();
loadConfig();
setInterval(refreshStatus, 8000);
setInterval(refreshRecords, 15000);
setInterval(refreshAccounts, 15000);
setInterval(refreshTransfers, 15000);
