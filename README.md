# gacha-buyer — linux.sb 称号市场采购与积分归集

这是一个独立于 sniper 的 Go 单二进制工具。配置、主号和小号凭据、登录会话、采购记录与归集记录全部保存在 SQLite 数据库中。

## 功能

- 按稀有度限价自动扫描 `/gacha_market` 并采购（默认 SR≤30、R≤10、N≤4）。
- 总花费上限、单 listing 限量、每轮 listing 数量限制和 dry-run 护栏。
- 一个主号负责采购，多个小号每日访问触发签到后向主号随机帖子打赏；单次最多 99，每日一次，可设置保留积分。
- 登录会话持久化，每 4 小时复用会话或重新登录；异常账号可在 Web 账号管理中人工恢复或退出。
- 最新市场 HTML 结构解析，在售快照、采购记录和归集记录可查询。

## 本机运行

```bash
export PATH="$PATH:/usr/local/go/bin"
go build -o bin/gacha-buyer ./cmd/gacha-buyer
./bin/gacha-buyer --data ./data
```

启动后打开 <http://127.0.0.1:8080>。首次启动会自动创建 `data/gacha-buyer.db`；所有配置在 Web 控制台保存，不需要也不支持 `config.json`。

先在“账号管理”中保存主号，再添加小号。建议保持 dry-run 观察采购记录，确认规则后再进行真实采购。主号密码不会在 GET API 或页面中回显。

## 主要设置

| 设置 | 说明 |
|---|---|
| 稀有度限价 | SR、R、N、SSR、UR 分别配置，0 表示不收购 |
| 总花费上限 | 真实成交累计达到上限后停止采购 |
| dry-run | 只记录拟采购/拟打赏，不向站点提交 |
| topic ID | 0 表示随机选择主号已发布主题；大于 0 表示固定帖子 |
| 保留积分 | 每个小号打赏前保留的积分 |

## Docker 部署

CI（GitHub Actions）在每次推送到 main 后自动构建 `linux/amd64 + linux/arm64` 双架构镜像并发布到 GHCR：

```
ghcr.io/roiding/linuxsb_gacha_buyer:latest
```

### docker compose（推荐）

```bash
mkdir -p /opt/gacha-buyer/data
cd /opt/gacha-buyer
cat > docker-compose.yml <<'EOF'
services:
  gacha-buyer:
    image: ghcr.io/roiding/linuxsb_gacha_buyer:latest
    container_name: gacha-buyer
    restart: unless-stopped
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - ./data:/app/data
    environment:
      - TZ=Asia/Shanghai
EOF
docker compose up -d
```

### docker run

```bash
docker run -d --name gacha-buyer --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v /opt/gacha-buyer/data:/app/data \
  -e TZ=Asia/Shanghai \
  ghcr.io/roiding/linuxsb_gacha_buyer:latest
```

### 数据库挂载目录

**容器内路径固定为 `/app/data`，SQLite 数据库文件是 `/app/data/gacha-buyer.db`。** 把宿主机任意目录挂到 `/app/data` 即可，所有配置、账号会话、购买与归集记录都保存在这一个文件里，容器重建不丢数据。

首次拉取 GHCR 镜像若提示无权限，先 `docker login ghcr.io`（用 GitHub 用户名 + Personal Access Token，需 `read:packages` 权限）；或在包设置里将其改为 public。

自建镜像与上面完全一致：`docker compose up -d --build`（仓库内已带 Dockerfile 与 compose 文件）。

## 备份

停止程序后备份 `data/gacha-buyer.db`；SQLite 的 WAL sidecar 文件也应一并保留，或使用 SQLite 在线备份工具导出。

## 免责声明

仅供个人账号自动化使用，请遵守站点规则与频率限制。
