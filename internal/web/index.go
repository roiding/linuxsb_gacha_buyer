package web

// indexHTML 控制台单页（数据全部走 /api/*）。
const indexHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="dark">
<title>Gacha Buyer 控制台</title>
<link rel="stylesheet" href="/static/style.css">
</head>
<body>
<div class="app-shell">
  <aside class="sidebar">
    <div class="brand"><span class="brand-mark">G</span><div><strong>Gacha Buyer</strong><small>称号市场控制台</small></div></div>
    <nav class="tabs" aria-label="主导航">
      <button data-tab="dash" class="active"><span>◫</span>仪表盘</button>
      <button data-tab="market"><span>◇</span>在售快照</button>
      <button data-tab="records"><span>≡</span>购买记录</button>
      <button data-tab="accounts"><span>◎</span>账号管理</button>
      <button data-tab="transfers"><span>↗</span>归集记录</button>
      <button data-tab="settings"><span>⚙</span>采购设置</button>
    </nav>
    <div class="sidebar-note"><span class="status-dot"></span><div><b>本机运行</b><small>数据仅存于 SQLite</small></div></div>
  </aside>

  <div class="workspace">
    <header class="topbar">
      <div><p class="eyebrow">LINUX.SB AUTOMATION</p><h1>称号采购与积分归集</h1></div>
      <div id="engine-state" class="pill">正在连接…</div>
    </header>

    <main>
      <section id="tab-dash" class="tab active">
        <div class="hero">
          <div><p class="eyebrow">采购引擎</p><h2>市场状态总览</h2><p>按限价自动扫描在售称号，并通过预算护栏控制总花费。</p></div>
          <div class="hero-actions">
            <button id="btn-start" class="primary">启动收购</button>
            <button id="btn-stop">停止</button>
            <button id="btn-scan">立即扫描</button>
          </div>
        </div>
        <div class="metrics">
          <article class="metric"><span>当前积分</span><b id="points">—</b><small>主账号可用余额</small></article>
          <article class="metric"><span>预算使用</span><b id="budget-used">—</b><small>上限 <i id="budget-max">—</i> 积分</small></article>
          <article class="metric"><span>在售条数</span><b id="listing-count">—</b><small>最近一次快照</small></article>
          <article class="metric"><span>成交笔数</span><b id="buy-ok">—</b><small>真实采购成功</small></article>
          <article class="metric"><span>最近扫描</span><b id="last-scan" class="metric-time">—</b><small>成功抓取市场时间</small></article>
        </div>
        <div class="panel guard-panel">
          <div><span class="panel-icon">◉</span><div><h3>安全护栏</h3><p id="rules-brief" class="hint">正在读取规则…</p></div></div>
          <label class="toggle-line"><input type="checkbox" id="dry-run-toggle"><span>Dry-run</span><small>只记录，不真实下单或打赏</small></label>
        </div>
        <pre id="err-box" class="err" hidden></pre>
      </section>

      <section id="tab-market" class="tab">
        <div class="section-head"><div><p class="eyebrow">LATEST SCAN</p><h2>在售快照</h2><p class="hint">展示最近一次成功扫描的新版市场卡片。</p></div></div>
        <div class="table-card"><div class="table-wrap"><table id="market-table">
          <thead><tr><th>称号</th><th>稀有度</th><th>单价</th><th>剩余</th><th>采购判定</th></tr></thead><tbody></tbody>
        </table></div></div>
      </section>

      <section id="tab-records" class="tab">
        <div class="section-head"><div><p class="eyebrow">PURCHASE HISTORY</p><h2>购买记录</h2><p class="hint">真实成交共 <b id="ok-count">0</b> 笔，累计花费 <b id="total-spent">0</b> 积分。</p></div></div>
        <div class="table-card"><div class="table-wrap"><table id="records-table">
          <thead><tr><th>时间</th><th>称号</th><th>稀有度</th><th>单价 × 数量</th><th>花费</th><th>结果</th></tr></thead><tbody></tbody>
        </table></div></div>
      </section>

      <section id="tab-accounts" class="tab">
        <div class="section-head">
          <div><p class="eyebrow">ACCOUNT VAULT</p><h2>账号管理</h2><p class="hint">账号、密码和会话保存在本机 SQLite，密码不会回显到页面。</p></div>
          <button id="btn-patrol" class="primary">巡检全部账号</button>
        </div>
        <article class="panel account-panel">
          <div class="panel-head"><div><span class="section-kicker">MAIN</span><h3>主账号</h3><p class="hint">唯一采购账号，同时接收小号打赏。</p></div></div>
          <div class="table-wrap"><table id="main-acct-table"><thead><tr><th>账号</th><th>状态</th><th>UID</th><th>最近在线</th><th>说明</th><th>操作</th></tr></thead><tbody></tbody></table></div>
          <form id="main-acct-form" class="inline-form">
            <label>用户名 / 邮箱<input name="main_username" autocomplete="off" placeholder="不修改可留空"></label>
            <label>新密码<input name="main_password" type="password" autocomplete="new-password" placeholder="不修改可留空"></label>
            <button type="submit" class="primary">保存主号</button><span class="form-msg"></span>
          </form>
        </article>
        <article class="panel account-panel">
          <div class="panel-head"><div><span class="section-kicker">SUB ACCOUNTS</span><h3>签到小号</h3><p class="hint">每日访问触发签到，之后向主号帖子打赏；每号每日最多一次，单次最多 99。</p></div></div>
          <div class="table-wrap"><table id="sub-acct-table"><thead><tr><th>账号</th><th>备注</th><th>启用</th><th>状态</th><th>UID</th><th>最近在线</th><th>说明</th><th>操作</th></tr></thead><tbody></tbody></table></div>
          <details><summary>添加一个小号</summary>
            <form id="sub-acct-form" class="inline-form">
              <label>用户名 / 邮箱<input name="sub_username" autocomplete="off" required></label>
              <label>密码<input name="sub_password" type="password" autocomplete="new-password" required></label>
              <label>备注<input name="sub_note" placeholder="例如：签到号 1"></label>
              <button type="submit" class="primary">添加小号</button>
            </form>
          </details>
        </article>
        <article class="panel account-panel">
          <div class="panel-head"><div><span class="section-kicker">DAILY TRANSFER</span><h3>每日积分归集</h3><p class="hint">固定帖子 ID 为 0 时，从主号已发布主题中随机选择。</p></div><button id="btn-collector-run">立即执行一轮</button></div>
          <form id="collector-form" class="inline-form">
            <label>固定帖子 ID<input name="topic_id" type="number" min="0" class="num" value="0"></label>
            <label>小号保留积分<input name="keep" type="number" min="0" class="num"></label>
            <label>每日执行时刻<input name="at_hour" type="number" min="0" max="23" class="num"></label>
            <label>打赏备注<input name="tip_message" placeholder="可留空"></label>
            <button type="submit" class="primary">保存归集设置</button>
          </form>
          <p class="hint schedule-line" id="collector-status"></p>
        </article>
      </section>

      <section id="tab-transfers" class="tab">
        <div class="section-head"><div><p class="eyebrow">TRANSFER HISTORY</p><h2>归集记录</h2><p class="hint">记录签到、余额、帖子与打赏结果。</p></div></div>
        <div class="table-card"><div class="table-wrap"><table id="transfers-table">
          <thead><tr><th>时间</th><th>小号</th><th>签到</th><th>余额</th><th>打赏金额</th><th>帖子</th><th>结果</th></tr></thead><tbody></tbody>
        </table></div></div>
      </section>

      <section id="tab-settings" class="tab">
        <div class="section-head"><div><p class="eyebrow">BUYER POLICY</p><h2>采购设置</h2><p class="hint">账号请在“账号管理”维护；这里仅配置采购策略。</p></div></div>
        <form id="cfg-form" class="settings-grid">
          <fieldset><legend>稀有度限价</legend><p class="hint">0 表示不采购该稀有度。</p>
            <div class="field-grid"><label>SR 上限<input name="sr" type="number" min="0" class="num"></label><label>R 上限<input name="r" type="number" min="0" class="num"></label><label>N 上限<input name="n" type="number" min="0" class="num"></label><label>SSR 上限<input name="ssr" type="number" min="0" class="num"></label><label>UR 上限<input name="ur" type="number" min="0" class="num"></label></div>
          </fieldset>
          <fieldset><legend>预算与节奏</legend><p class="hint">限制单轮采购规模和市场请求频率。</p>
            <div class="field-grid"><label>总花费上限<input name="max_spend" type="number" min="0" class="num"></label><label>扫描间隔（秒）<input name="scan_sec" type="number" min="30" class="num"></label><label>单条最多购买<input name="max_buy_once" type="number" min="1" class="num"></label><label>每轮最多成交条数<input name="max_listings" type="number" min="1" class="num"></label></div>
          </fieldset>
          <div class="settings-actions"><button type="submit" class="primary">保存采购设置</button><span id="save-msg"></span></div>
        </form>
      </section>
    </main>
    <footer>Gacha Buyer · 独立工具 · SQLite 本地持久化</footer>
  </div>
</div>
<div id="toast" class="toast" role="status" aria-live="polite"></div>
<script src="/static/app.js"></script>
</body>
</html>`
