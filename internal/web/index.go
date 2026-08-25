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
      <button data-tab="ssr"><span>✦</span>SSR定向</button>
      <button data-tab="lottery"><span>✎</span>抽奖回复</button>
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
          <div><p class="eyebrow">采购引擎</p><h2>市场状态总览</h2><p>按限价自动扫描在售称号，并在每次下单前保留账号保护余额。</p></div>

          <div class="hero-actions">
            <button id="btn-start" class="primary">启动收购</button>
            <button id="btn-stop">停止</button>
            <button id="btn-scan">立即扫描</button>
          </div>
        </div>
        <div class="metrics">
          <article class="metric"><span>当前积分</span><b id="points">—</b><small>主账号可用余额</small></article>
          <article class="metric"><span>余额保护线</span><b id="min-balance">—</b><small>购买后至少保留</small></article>
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
	          <div class="panel-head"><div><span class="section-kicker">DAILY TRANSFER</span><h3>每日积分归集</h3><p class="hint">每个小号在当天 0–24 点各有一个独立随机时刻，分散执行；重启会补跑当天未完成的小号。</p></div><button id="btn-collector-run">立即执行一轮</button></div>

          <form id="collector-form" class="inline-form">
            <label>固定帖子 ID<input name="topic_id" type="number" min="0" class="num" value="0"></label>
            <label>小号保留积分<input name="keep" type="number" min="0" class="num"></label>
            <label>单次最低打赏<input name="min_tip" type="number" min="1" class="num"></label>
            <label>打赏备注<input name="tip_message" placeholder="可留空"></label>
            <button type="submit" class="primary">保存归集设置</button>
          </form>
          <p class="hint schedule-line" id="collector-status"></p>
          <div class="table-wrap"><table id="collector-plans-table">
            <thead><tr><th>小号</th><th>计划时刻</th><th>开始</th><th>完成</th><th>状态</th></tr></thead><tbody></tbody>
          </table></div>
        </article>
      </section>

      <section id="tab-transfers" class="tab">
        <div class="section-head"><div><p class="eyebrow">TRANSFER HISTORY</p><h2>归集记录</h2><p class="hint">记录签到、余额、帖子与打赏结果。</p></div></div>
        <div class="section-head compact"><div><p class="eyebrow">DAILY FREE GACHA</p><h2>每日免费一抽</h2><p class="hint">随归集任务执行：小号签到后抽一次每日免费卡池，抽到的称号自动赠送给主号（可赠送数=持有数−佩戴中的 1 张）；空包仅记录。</p></div></div>
        <div class="table-card"><div class="table-wrap"><table id="gacha-table">
          <thead><tr><th>时间</th><th>小号</th><th>所得</th><th>赠送</th><th>说明</th></tr></thead><tbody></tbody>
        </table></div></div>
        <div class="table-card"><div class="table-wrap"><table id="transfers-table">
          <thead><tr><th>时间</th><th>小号</th><th>签到</th><th>余额</th><th>打赏金额</th><th>帖子</th><th>结果</th></tr></thead><tbody></tbody>
        </table></div></div>
      </section>

      <section id="tab-settings" class="tab">
        <div class="section-head"><div><p class="eyebrow">BUYER POLICY</p><h2>采购设置</h2><p class="hint">账号请在“账号管理”维护；这里仅配置采购策略。</p></div></div>
        <form id="cfg-form" class="settings-grid">
          <fieldset><legend>稀有度限价</legend><p class="hint">0 表示不采购该稀有度。</p>
            <div class="field-grid"><label>SR 上限<input name="sr" type="number" min="0" class="num"></label><label>R 上限<input name="r" type="number" min="0" class="num"></label><label>N 上限<input name="n" type="number" min="0" class="num"></label><label>UR 上限<input name="ur" type="number" min="0" class="num"></label></div>
          </fieldset>
          <fieldset><legend>余额与节奏</legend><p class="hint">不限制总采购额度和每轮数量，只保留账号余额保护线。</p>
            <div class="field-grid"><label>余额保护线<input name="min_balance" type="number" min="0" class="num"></label><label>扫描间隔（秒）<input name="scan_sec" type="number" min="30" class="num"></label></div>
          </fieldset>
          <div class="settings-actions"><button type="submit" class="primary">保存采购设置</button><span id="save-msg"></span></div>
        </form>
      </section>

      <section id="tab-ssr" class="tab">
        <div class="section-head"><div><p class="eyebrow">TARGETED COLLECTION</p><h2>SSR 定向收集</h2><p class="hint">从 linux.sb 称号目录读取全部 SSR；逐个填写最高收购价，留空表示不收购。</p></div><button id="btn-load-catalog" class="primary">刷新 SSR 目录</button></div>
        <div class="panel"><div id="ssr-catalog" class="ssr-grid"><p class="empty">点击“刷新 SSR 目录”获取称号列表。</p></div><div class="settings-actions"><button id="btn-save-ssr" class="primary">保存 SSR 价格</button><span id="ssr-save-msg"></span></div></div>
      </section>
      <section id="tab-lottery" class="tab">
        <div class="section-head"><div><p class="eyebrow">LOTTERY REPLIES</p><h2>抽奖帖小号回复</h2><p class="hint">保存目标帖子和回复语料后，由全部已启用小号依次随机回复；只有点击“立即执行”才会发帖。</p></div><button id="btn-lottery-run" class="primary">立即执行全部小号</button></div>
        <div class="panel lottery-panel">
          <form id="lottery-form" class="lottery-form">
            <label>抽奖帖 URL<input name="lottery_url" type="url" placeholder="https://linux.sb/topic/123" required></label>
            <label>回复语料库 <small>每行一条，至少 5 个字；同一轮优先使用不同文案</small><textarea name="lottery_messages" rows="15" required></textarea></label>
            <div class="settings-actions"><button type="submit" class="primary">保存抽奖回复设置</button><span id="lottery-save-msg"></span></div>
          </form>
          <p class="hint schedule-line" id="lottery-status">正在读取状态…</p>
        </div>
        <div class="section-head compact"><div><p class="eyebrow">REPLY HISTORY</p><h2>回复记录</h2><p class="hint">“已提交”不等于成功；只有站点返回有效 replyid 才标记为确认成功。</p></div></div>
        <div class="table-card"><div class="table-wrap"><table id="lottery-table">
          <thead><tr><th>时间</th><th>小号</th><th>帖子</th><th>语料</th><th>验证码</th><th>结果</th></tr></thead><tbody></tbody>
        </table></div></div>
      </section>
    </main>
    <footer>Gacha Buyer · 独立工具 · SQLite 本地持久化</footer>
  </div>
</div>
<div id="toast" class="toast" role="status" aria-live="polite"></div>
<script src="/static/app.js"></script>
</body>
</html>`
