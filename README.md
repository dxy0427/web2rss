# web2rss

影视资源 RSS 服务：抓取资源站，输出带磁力/种子 enclosure 的 RSS 订阅源，供 RSS 阅读器订阅追更。

## 部署

Docker（推荐）：

```bash
docker run -d \
  --name web2rss \
  --restart=unless-stopped \
  -p 8888:8888 \
  -v web2rss-data:/data \
  --memory 1g \
  --cpus 1 \
  --log-opt max-size=10m \
  --log-opt max-file=3 \
  ghcr.io/dxy0427/web2rss:latest
```

- 局域网直接访问用 `-p 8888:8888`；只给本机或反向代理访问时改成 `-p 127.0.0.1:8888:8888`
- 镜像仅提供 `linux/amd64`，ARM 设备（如部分群晖）需自行 `docker build`
- `-v web2rss-data:/data` 用于持久保存 mukaku 续期令牌，**建议保留**，否则重建容器后需重新续期

有 mukaku VIP 账号时追加：`-e MUKAKU_USERNAME=账号 -e MUKAKU_PASSWORD=密码`（可用一键续期，见下文《mukaku 会员令牌》）；或直接给令牌 `-e MUKAKU_ACCESS_TOKEN=你的token`

公网访问建议经反向代理（Caddy/Nginx/Cloudflare Tunnel）并配置鉴权，不要直接把 8888 暴露到公网；反代读超时建议不小于 180 秒（游客模式抓取较慢时避免截断）。

源码运行：

```bash
go build -o web2rss . && PORT=8888 ./web2rss
```

## 路由

| 站点 | 路由 | 参数 |
| ---- | ---- | ---- |
| BT之家 1LOU | `/rss/1lou/{关键词或帖子ID}` | `limit` 结果条数，默认 20 上限 50 |
| 6v电影网 | `/rss/6v123/{分类、片名或影片ID}` | `limit` 条目数，默认 25 上限 50 |
| 6v电影网 | `/rss/6v123/search/{关键词}` | 同上 |
| 6v电影网 | `/rss/6v123/detail/{影片页路径}` | 无 |
| btbtla | `/rss/btbtla/{资源ID或名称}` | 无 |
| mukaku | `/rss/mukaku/{idcode或名称}` | `eps` 最近集数，默认 5 上限 10 |

示例：

```
/rss/1lou/繁花                          # 1lou 关键词搜索，返回各版本种子包
/rss/1lou/643346                        # 1lou 订阅单帖
/rss/6v123                              # 6v 首页最新
/rss/6v123/donghuapian                  # 6v 分类（动画片）
/rss/6v123/遮天                          # 6v 按片名订阅（自动搜索取第一个结果）
/rss/6v123/15719                         # 6v 按影片 ID 订阅（自动探测分类）
/rss/6v123/search/完美世界               # 6v 站内搜索
/rss/6v123/detail/donghuapian/15719.html  # 6v 订阅单片（路径取自影片页 URL，可省略 / 和 .html）
/rss/btbtla/繁花                        # btbtla 按名称
/rss/mukaku/遮天?eps=10                 # mukaku 最近 10 集
```

## 各站说明

- **1lou**：搜索返回各帖子种子包（一个种子可能含多集或全集）；enclosure 为站内 .torrent 直链，无种子时回退正文磁力。帖子页有限速，个别请求会挂起重试。
- **6v123**：抓 hao6v.org（新版）。**片名订阅**（`/rss/6v123/遮天`）自动搜索取第一个结果；**ID 订阅**（`/rss/6v123/15719`）自动探测分类路径；两者均返回该页**全部磁力**。分类/搜索每个影片页最多取最新 20 条。英文片名若站点搜不到（标题多为中文），请用中文片名。常用分类：`donghuapian` 动画片、`dianshiju` 电视剧、`xijupian` 喜剧片、`dongzuopian` 动作片、`aiqingpian` 爱情片、`kehuanpian` 科幻片、`kongbupian` 恐怖片、`juqingpian` 剧情片、`zhanzhengpian` 战争片、`jilupian` 纪录片，子分类按 URL 填（如 `dianshiju/oumeiju`）。
- **btbtla**：单片返回该影片页的资源（页面最新在前，最多 50 条）；磁力在各自的下载子页里，需逐条抓取，源站对请求速率敏感，已内置节流与小并发。
- **mukaku**（不太灵影视）：源站种子已 VIP 化。未配置令牌时按公开种子流匹配取回（剧集为最近 `eps` 个集数）；配置 `MUKAKU_ACCESS_TOKEN`（见下文《mukaku 会员令牌》）后直接返回完整种子列表；令牌失效时自动回退游客模式，不影响出 feed。

## mukaku 会员令牌

配置后可直接读取完整种子列表（全部历史集数），并在启动时自动校验会员状态。两种方式：

**方式一：账号密码 + 一键续期（推荐）**

1. 启动容器时添加 `-e MUKAKU_USERNAME=账号 -e MUKAKU_PASSWORD=密码`；建议同时加 `-v web2rss-data:/data`（持久保存续期结果，容器重建也不丢）
2. 令牌缺失或过期时，浏览器打开 `http://服务地址:8888/mukaku/renew`
3. 按提示输入图中验证码，点“一键续期”即可自动登录换新令牌，立即生效、无需重启

**方式二：手动粘贴令牌**

1. 浏览器登录 https://web5.mukaku.com
2. 按 F12 → Application（应用）→ Storage → Local Storage → 选中 https://web5.mukaku.com
3. 找到键 `token`，复制它的值（三段式 JWT 字符串）
4. 启动容器时加 `-e MUKAKU_ACCESS_TOKEN=复制的值`（已在运行的容器需删除后用新参数重建）
5. 确认生效：`docker logs web2rss` 启动日志应出现 `[mukaku] 令牌有效期至 2026-xx-xx（剩余 N 天）` 和 `[mukaku] access_token 校验成功：账号 xxx，VIP：是（到期 ...）`

说明：

- 站点登录带图形验证码（服务端强制校验），**无法完全自动续期**；一键续期页是验证码机制下最省事的方式
- 续期页本身不含登录凭据，但能触发用已配置账号登录；公网部署时建议连同整站一起加访问控制
- 令牌是 JWT、自带有过期时间：启动时会解析并打印有效期；剩 2 天内日志会定期提醒续期
- 令牌无效或过期时服务不会中断：自动按游客模式抓取；令牌被拒后 1 小时内不再重复尝试，过后自动重试
- 配置令牌后长篇剧集会返回全部历史集数，ani-rss 订阅建议开启“只下载最新集”

## 环境变量

| 变量 | 默认 | 说明 |
| ---- | ---- | ---- |
| `PORT` | `8888` | 监听端口 |
| `CACHE_EXPIRATION_MINUTES` | `15` | feed 缓存时长（分钟） |
| `SCRAPE_TIMEOUT_SEC` | `20` | 单请求超时（秒） |
| `RETRY_MAX` | `2` | 重试次数 |
| `RETRY_INTERVAL_SEC` | `1` | 重试间隔基数 |
| `MAX_CONCURRENCY` | 不限 | 并发抓取上限（内部另有更小上限） |
| `MUKAKU_ACCESS_TOKEN` | 空 | mukaku 会员令牌（JWT），配置后返回完整种子并自动校验，获取方式见《mukaku 会员令牌》 |
| `MUKAKU_USERNAME` | 空 | mukaku 账号（可选，配合一键续期页使用） |
| `MUKAKU_PASSWORD` | 空 | mukaku 密码（可选，配合一键续期页使用） |

## 注意

- **凭据只放在运行环境**：账号密码、令牌均通过环境变量或 `/data` 传入，不要写进代码或提交到仓库；密码含 `$` 时 `docker run` 用单引号包裹，Compose 中写成 `$$`
- 防封：mukaku 内置节流、并发限制和页面共享缓存，订阅 10~20 部没有压力；数量更多时优先调大 `CACHE_EXPIRATION_MINUTES`，并避免同时集中添加订阅。
- 6v 影片页路径需取自 hao6v.org（旧版镜像的 ID 与路径不通用）。
- 抓取失败只缓存 30 秒，源站恢复后自动重试；中断的残缺结果不会被长缓存。

## 配合 ani-rss 使用

四站输出均为标准 RSS（条目带磁力或种子 enclosure），可直接作为 ani-rss 的订阅地址。

**推荐：订阅的"自定义集数规则"统一填下面这条（正则匹配组设为 2）**，覆盖四站全部标题格式及常见动漫命名（单集/区间合集/`[01]`/` - 01`/`【12.5】`等），并排除体积后缀（如 `0.34GB`）误匹配：

```
(.*|\[.*])(( - |Vol |[Ee][Pp]?)\d+(\.5)?|【\d+(\.5)?】|\[\d+(\.5)?( ?[vV]\d)?( ?END)?( ?完)?]|第\d+(\.5)?(?:-\d+(\.5)?)?[话話集]( - END)?| \d+(\.5)?(?:-\d+(\.5)?)?\.(?!\d* ?[GKM]i?B)|^\[TOC].* \d+)
```

提示：

- **6v123 单片订阅**首次会返回该页全部历史磁力（长篇连载可达数百条），建议开启 ani-rss 的"只下载最新集"，或用"匹配"正则限定集数范围
- **mukaku** 用 `?eps=` 控制返回的最近集数，追更建议默认 5；集数标签跨集重复时（补档）会多出几条，用"排除"正则过滤即可
- **1lou** 的 enclosure 是站内 .torrent 直链（无需登录），其余三站为磁力链接
- **1lou 搜索订阅**返回的是关键词下的多部资源，用"匹配"正则锁定其中一部
