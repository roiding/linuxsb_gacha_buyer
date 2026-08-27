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

// ---- 记录分页 ----
const PAGE_SIZE = 50;
let recordsPage = 1;
let transfersPage = 1;

// renderPager 更新分页条：隐藏/显示、页码信息、按钮禁用状态。
function renderPager(pagerId, infoId, d) {
  const pager = $(pagerId), info = $(infoId);
  const total = d.total || 0;
  const totalPages = d.total_pages || 0;
  const page = d.page || 1;
  if (!total) { pager.hidden = true; return; }
  pager.hidden = false;
  info.textContent = `第 ${page} / ${totalPages} 页 · 共 ${total} 条`;
  const prev = pager.querySelector('[data-act="prev"]');
  const next = pager.querySelector('[data-act="next"]');
  prev.disabled = page <= 1;
  next.disabled = page >= totalPages;
}

document.addEventListener("click", (ev) => {
  const btn = ev.target.closest("[data-pager]");
  if (!btn) return;
  const dir = btn.dataset.act === "next" ? 1 : -1;
  if (btn.dataset.pager === "records") {
    recordsPage = Math.max(1, recordsPage + dir);
    refreshRecords();
  } else if (btn.dataset.pager === "transfers") {
    transfersPage = Math.max(1, transfersPage + dir);
    refreshTransfers();
  }
});

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
      `（UR≤${rules.ur ?? 0}），余额保护线 ${s.min_balance} 分` +
      (s.targets ? `，定向 ${Object.keys(s.targets).length} 条` : "") +
      (s.dry_run ? "，当前 dry-run" : "");
    const errBox = $("#err-box");
    if (s.last_error) { errBox.hidden = false; errBox.textContent = "最近错误：" + s.last_error; }
    else errBox.hidden = true;

    renderMarket(s.listings || [], s.rules || {}, s.ssr_prices || {}, s.targets || {});
    // 设置页 dry-run 开关同步
    $("#dry-run-toggle").checked = !!s.dry_run;
  } catch (e) { console.error(e); }
}

function renderMarket(listings, rules, ssrPrices, targets) {
  const tb = $("#market-table tbody");
  tb.innerHTML = "";
  if (!listings.length) {
    tb.innerHTML = `<tr><td colspan="5" class="empty">尚无快照。点击“立即扫一轮”后会显示最新市场数据。</td></tr>`;
    return;
  }
  for (const l of listings) {
    const targeted = (targets && targets[l.name] && targets[l.name].price) || 0;
    const limit = targeted || (l.rarity === "ssr" ? (ssrPrices[l.name] ?? 0) : (rules[l.rarity] ?? 0));
    const hit = limit > 0 && l.price <= limit && l.remain > 0;
    const tr = document.createElement("tr");
    tr.innerHTML = `<td>${l.emoji || ""} ${esc(l.name)}</td>
      <td>${rarityBadge(l.rarity)}</td>
      <td>${l.price}</td><td>${l.remain}</td>
      <td class="${hit ? "hit" : "miss"}">${hit ? `✓ 收（限 ≤${limit}）` : "—"}</td>`;
      tb.appendChild(tr);
    }
}

// ---- 批量上架 / 下架 ----
let bulkBusy = false;

async function withBulkBusy(button, task) {
  if (bulkBusy) return null;
  const panel = $(".bulk-panel");
  const controls = panel.querySelectorAll("input, select, button");
  const label = button.textContent;
  bulkBusy = true;
  panel.classList.add("is-busy");
  controls.forEach(control => { control.disabled = true; });
  button.textContent = "处理中…";
  try {
    return await task();
  } finally {
    controls.forEach(control => { control.disabled = false; });
    button.textContent = label;
    panel.classList.remove("is-busy");
    bulkBusy = false;
  }
}

function renderBulkResult(res) {
  const el = $("#bulk-result");
  if (!res) return;
  el.hidden = false;
  const items = res.items || [];
  if (!items.length) {
    el.innerHTML = `<div class="bulk-summary"><strong>没有可执行的操作</strong><span>无匹配分类的可出售称号，或当前没有在售挂牌。</span></div>`;
    return;
  }
  const lines = items.map(it => {
    const name = esc(it.name || "未命名项目");
    const price = it.price ? ` @${esc(it.price)}` : "";
    const message = esc(it.message || "");
    return `<div class="bulk-item ${it.ok ? "ok" : "fail"}">${it.ok ? "✓" : "✗"} ${name}${price} <span>· ${message}</span></div>`;
  }).join("");
  el.innerHTML = `<div class="bulk-summary"><strong>处理完成</strong><span class="bulk-success">成功 ${res.success || 0}</span><span class="bulk-failed">失败 ${res.failed || 0}</span><span>共 ${items.length} 项 · ${esc(fmtTime(new Date().toISOString()))}</span></div><div class="bulk-items">${lines}</div>`;
}

function refreshMarketAfterBulk() {
  refreshStatus();
  setTimeout(refreshStatus, 3000);
}

$("#bulk-publish-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const rarities = Array.from(f.rarities.options).filter(o => o.selected).map(o => o.value);
  const unitPrice = +f.unit_price.value || 0;
  if (!rarities.length) { notify("请至少选择一个稀有度分类", true); return; }
  if (unitPrice <= 0) { notify("请填写统一单价", true); f.unit_price.focus(); return; }
  const btn = f.querySelector("button[type=submit]");
  try {
    const res = await withBulkBusy(btn, () => api("/api/market/publish", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ rarities, unit_price: unitPrice, duration_hours: +f.duration_hours.value || 24 }),
    }));
    renderBulkResult(res);
    notify(`批量上架完成：成功 ${res.success || 0}，失败 ${res.failed || 0}`);
  } catch (e) { notify(e.message, true); }
  refreshMarketAfterBulk();
});

$("#btn-bulk-cancel").addEventListener("click", async () => {
  if (bulkBusy || !confirm("确定撤回当前主号全部在售挂牌吗？仓库中的称号不会被删除。")) return;
  const btn = $("#btn-bulk-cancel");
  try {
    const res = await withBulkBusy(btn, () => api("/api/market/cancel", { method: "POST" }));
    renderBulkResult(res);
    notify(`批量下架完成：成功 ${res.success || 0}，失败 ${res.failed || 0}`);
  } catch (e) { notify(e.message, true); }
  refreshMarketAfterBulk();
});

async function refreshRecords() {
  try {
    const d = await api(`/api/purchases?page=${recordsPage}&page_size=${PAGE_SIZE}`);
    // 记录只增不减，若新数据把当前页挤到末页之后，回到最后一页重取
    if (d.total_pages > 0 && recordsPage > d.total_pages) {
      recordsPage = d.total_pages;
      return refreshRecords();
    }
    $("#total-spent").textContent = d.total_spent;
    $("#ok-count").textContent = d.ok_count;
    renderPager("records-pager", "records-pager-info", d);
    const tb = $("#records-table tbody");
    tb.innerHTML = "";
    if (!(d.records || []).length) {
      tb.innerHTML = `<tr><td colspan="6" class="empty">暂无购买记录。</td></tr>`;
    }
    for (const p of d.records || []) {
      const confirmed = !!p.confirmed || (!Object.prototype.hasOwnProperty.call(p, "confirmed") && !!p.ok && !p.dry_run);
      const submitted = !!p.submitted || confirmed || !!p.dry_run;
      const cls = p.dry_run ? "dry" : (confirmed ? "hit" : (submitted ? "dry" : "fail"));
      const label = p.dry_run ? "[dry] 仅模拟" : (confirmed ? "✓ 已确认成交" : (submitted ? "↗ 已提交，未确认" : "✗ 未成交"));
      const time = fmtTime(p.time);
      const tr = document.createElement("tr");
      tr.innerHTML = `<td>${esc(time)}</td>
        <td>${esc(p.name || "-")}</td><td>${rarityBadge(p.rarity)}</td>
        <td>${p.price ?? 0} × ${p.qty ?? 0}</td><td>${p.cost ?? 0}</td>
        <td class="${cls}">${label}${p.message ? " · " + esc(p.message) : ""}</td>`;
      tb.appendChild(tr);
    }
  } catch (e) { console.error(e); }
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g,
    c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

// fmtTime 把后端返回的 ISO 时间（UTC）显示为北京时间（Asia/Shanghai）。
function fmtTime(iso) {
  if (!iso) return "-";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return String(iso);
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false,
  }).formatToParts(d);
  const get = (t) => (parts.find(p => p.type === t) || {}).value || "";
  return `${get("year")}-${get("month")}-${get("day")} ${get("hour")}:${get("minute")}:${get("second")}`;
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
    f.scan_mode.value = c.scan_mode || "";
  } catch (e) { console.error(e); }
}

$("#cfg-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const body = {
    rules: { sr: +f.sr.value, r: +f.r.value, n: +f.n.value, ur: +f.ur.value },
    min_balance: +f.min_balance.value,
    scan_sec: +f.scan_sec.value,
    scan_mode: f.scan_mode.value,
  };
  try {
    await api("/api/config", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    $("#save-msg").textContent = "已保存 ✓";
    setTimeout(() => $("#save-msg").textContent = "", 2500);
    refreshStatus();
  } catch (e) { alert(e.message); }
});

// ---- 定向收购 ----
let targetTitles = [];
let targetRules = {}; // name -> {price, max}

function buildTargetRow(t) {
  const rule = targetRules[t.name] || {};
  const tr = document.createElement("tr");
  tr.dataset.rarity = t.rarity || "";
  tr.dataset.name = t.name;
  tr.innerHTML = `
    <td class="target-title">${esc(t.emoji || "")} ${esc(t.name)}</td>
    <td>${rarityBadge(t.rarity)}</td>
    <td><input class="num target-price" type="number" min="0" value="${rule.price || ""}" placeholder="不收"></td>
    <td><input class="num target-max" type="number" min="0" value="${rule.max || ""}" placeholder="不限"></td>`;
  return tr;
}

function applyTargetFilter() {
  const rarity = $("#target-rarity-group .active").dataset.rarity;
  const q = ($("#target-search").value || "").trim().toLowerCase();
  $$("#target-table tbody tr").forEach(tr => {
    const matchR = !rarity || tr.dataset.rarity === rarity;
    const matchQ = !q || (tr.dataset.name || "").toLowerCase().includes(q);
    tr.style.display = (matchR && matchQ) ? "" : "none";
  });
}

async function loadCatalog() {
  const tb = $("#target-table tbody");
  tb.innerHTML = `<tr><td colspan="4" class="empty">正在读取 linux.sb 称号目录…</td></tr>`;
  try {
    const d = await api("/api/catalog");
    targetTitles = d.titles || [];
    targetRules = d.targets || {};
    tb.innerHTML = "";
    if (!targetTitles.length) {
      tb.innerHTML = `<tr><td colspan="4" class="empty">目录中没有发现称号。</td></tr>`;
      return;
    }
    for (const t of targetTitles) tb.appendChild(buildTargetRow(t));
    applyTargetFilter();
  } catch (e) {
    tb.innerHTML = `<tr><td colspan="4" class="fail">${esc(e.message)}</td></tr>`;
  }
}

$("#btn-load-catalog").addEventListener("click", async (ev) => {
  try { await withBusy(ev.currentTarget, loadCatalog); }
  catch (e) { notify(e.message, true); }
});
$("#target-rarity-group").addEventListener("click", (ev) => {
  const btn = ev.target.closest("button");
  if (!btn) return;
  $$("#target-rarity-group button").forEach(b => b.classList.remove("active"));
  btn.classList.add("active");
  applyTargetFilter();
});
$("#target-search").addEventListener("input", applyTargetFilter);
$("#btn-save-targets").addEventListener("click", async (ev) => {
  const targets = {};
  $$("#target-table tbody tr").forEach(tr => {
    const name = tr.dataset.name;
    if (!name) return;
    const price = Number(tr.querySelector(".target-price").value) || 0;
    const max = Number(tr.querySelector(".target-max").value) || 0;
    if (price > 0 || max > 0) targets[name] = { price, max };
  });
  try {
    await withBusy(ev.currentTarget, () => api("/api/config", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ targets }) }));
    $("#target-save-msg").textContent = "已保存 ✓";
    notify(`定向收购已保存（${Object.keys(targets).length} 条规则）`);
    refreshStatus();
    setTimeout(() => $("#target-save-msg").textContent = "", 2500);
  } catch (e) { notify(e.message, true); }
});

let lotteryDirty = false;

async function refreshLottery() {
  try {
    const d = await api("/api/lottery");
    const f = $("#lottery-form");
    if (!lotteryDirty) {
      f.lottery_url.value = d.url || "";
      f.lottery_messages.value = (d.messages || []).join("\n");
    }
    const status = $("#lottery-status");
    status.textContent = `${d.running ? "任务运行中" : "任务已停止"} · 已启用小号 ${d.enabled_subs || 0} 个` + (d.dry_run ? " · 当前 dry-run，只记录不发帖" : " · 真实模式会向目标帖发帖");
    const tb = $("#lottery-table tbody");
    tb.innerHTML = "";
    for (const x of d.records || []) {
      const cls = x.dry_run ? "dry" : (x.confirmed ? "hit" : (x.submitted ? "dry" : "fail"));
      const label = x.dry_run ? "[dry] 仅记录" : (x.confirmed ? `✓ 已确认 #${x.reply_id || ""}` : (x.submitted ? "↗ 已提交，未确认" : "✗ 未提交"));
      const time = fmtTime(x.time);
      tb.insertAdjacentHTML("beforeend", `<tr><td>${esc(time)}</td><td>${esc(x.sub || "-")}</td><td>#${x.topic_id || "-"}</td><td class="hint">${esc(x.content || "")}</td><td>${x.captcha ? "✓" : "-"}</td><td class="${cls}">${label} · ${esc(x.message || "")}</td></tr>`);
    }
  } catch (e) { console.error(e); }
}

$("#lottery-form").addEventListener("input", () => { lotteryDirty = true; });

$("#lottery-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const f = ev.target;
  const messages = f.lottery_messages.value.split(/\r?\n/).map(x => x.trim()).filter(Boolean);
  try {
    await api("/api/lottery", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ url: f.lottery_url.value.trim(), messages }) });
    lotteryDirty = false;
    $("#lottery-save-msg").textContent = "已保存 ✓";
    notify("抽奖回复设置已保存");
    setTimeout(() => $("#lottery-save-msg").textContent = "", 2500);
    refreshLottery();
  } catch (e) { notify(e.message, true); }
});

$("#btn-lottery-run").addEventListener("click", async (ev) => {
  const f = $("#lottery-form");
  const enabled = $("#lottery-status").textContent.match(/已启用小号 (\d+) 个/);
  const count = enabled ? enabled[1] : "若干";
  if (!f.lottery_url.value.trim()) { notify("请先保存抽奖帖 URL", true); return; }
  if (!confirm(`确认让 ${count} 个已启用小号依次回复此抽奖帖？`)) return;
  try {
    await withBusy(ev.currentTarget, () => api("/api/lottery/run", { method: "POST" }));
    notify("抽奖回复任务已开始");
    refreshLottery();
  } catch (e) { notify(e.message, true); }
});

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
    f.min_tip.value = col.min_tip ?? 1;
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
    await api("/api/accounts", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ collector: { topic_id: +f.topic_id.value || 0, keep: +f.keep.value, min_tip: +f.min_tip.value || 1, message: f.tip_message.value } }) });
    alert("归集设置已保存 ✓"); refreshAccounts();
  } catch (e) { alert(e.message); }
});

async function refreshTransfers() {
  try {
    const d = await api(`/api/transfers?page=${transfersPage}&page_size=${PAGE_SIZE}`);
    if (d.total_pages > 0 && transfersPage > d.total_pages) {
      transfersPage = d.total_pages;
      return refreshTransfers();
    }
    const s = d.status || {};
    const plans = s.plans || [];
    const done = plans.filter(p => p.status === "completed").length;
    $("#collector-status").textContent =
      (s.running ? "调度中" : "调度未运行") +
      (s.next_run ? ` · 下次 ${s.next_run}` : "") +
      (s.last_run ? ` · 上次执行 ${s.last_run}` : "") +
      ` · 已排 ${plans.length} 号 / 完成 ${done}`;
    const ptb = $("#collector-plans-table tbody");
    ptb.innerHTML = "";
    if (!plans.length) {
      ptb.innerHTML = `<tr><td colspan="5" class="empty">今天还没有生成计划，启动引擎或保存归集设置后生成。</td></tr>`;
    } else {
      const planStatus = { planned: "⏳ 待执行", running: "▶ 执行中", retry: "↻ 稍后重试", completed: "✓ 已完成" };
      for (const p of plans) {
        const tr = document.createElement("tr");
        tr.innerHTML = `<td>${esc(p.account)}</td><td>${esc(p.planned_at)}</td>
          <td>${esc(p.started_at || "-")}</td><td>${esc(p.completed_at || "-")}</td>
          <td class="${p.status === "completed" ? "hit" : (p.status === "retry" ? "dry" : "")}">${planStatus[p.status] || esc(p.status)}</td>`;
        ptb.appendChild(tr);
      }
    }
    const tb = $("#transfers-table tbody");
    tb.innerHTML = "";
    const gt = $("#gacha-table tbody");
    gt.innerHTML = "";
    if (!(d.gacha || []).length) {
      gt.innerHTML = `<tr><td colspan="5" class="empty">今日抽卡记录会随归集任务生成。</td></tr>`;
    } else {
      for (const g of d.gacha || []) {
        const tr = document.createElement("tr");
        const got = g.drawn ? esc(g.drawn) : (g.ok ? "空包" : "-");
        const gift = g.gifted ? "✓ 已赠送" : (g.drawn && g.ok ? "⏳ 待赠送" : "-");
        tr.innerHTML = `<td>${fmtTime(g.time)}</td>
          <td>${esc(g.sub)}</td><td>${got}</td>
          <td class="${g.gifted ? "hit" : ""}">${gift}${g.gift_target ? " → " + esc(g.gift_target) : ""}</td>
          <td>${esc(g.message || "")}</td>`;
        gt.appendChild(tr);
      }
    }
    for (const t of d.transfers || []) {
      const cls = t.pending ? "dry" : (t.confirmed ? "hit" : (t.submitted ? "dry" : (t.ok ? "hit" : "fail")));
      const label = t.dry_run ? "[dry] 仅记录" : (t.pending ? "⏳ 已提交，待核验" : (!t.retryable && !t.ok ? "⚠ 硬条件未满足，本日不重试" : (t.confirmed ? "✓ 已确认" : (t.submitted ? "↗ 已提交，未确认" : (t.ok ? "— 无需归集" : "✗ 未成功")))));
      const verify = t.balance_before || t.balance_after ? ` · 余额 ${t.balance_before || "-"}→${t.balance_after || "-"}` : "";
      const tr = document.createElement("tr");
      tr.innerHTML = `<td>${fmtTime(t.time)}</td>
        <td>${esc(t.sub)}</td><td>${t.check_in ? "✓" : "-"}</td>
        <td>${t.balance}</td><td>${t.tip_amount || "-"}</td>
        <td>${t.topic_id ? "#" + t.topic_id : "-"}</td>
        <td class="${cls}">${label}${verify} · ${esc(t.message || "")}</td>`;
      tb.appendChild(tr);
    }
    if (!(d.transfers || []).length) {
      tb.innerHTML = `<tr><td colspan="7" class="empty">暂无归集记录。</td></tr>`;
    }
    renderPager("transfers-pager", "transfers-pager-info", d);
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
refreshLottery();
loadConfig();
setInterval(refreshStatus, 8000);
setInterval(refreshRecords, 15000);
setInterval(refreshAccounts, 15000);
setInterval(refreshTransfers, 15000);
setInterval(refreshLottery, 15000);
