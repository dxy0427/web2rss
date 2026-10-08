# web2rss

影视资源 RSS 服务：抓取资源站，输出带磁力/种子 enclosure 的 RSS 订阅源，供 RSS 阅读器订阅追更。

## 部署

Docker（推荐）：

```bash
docker run -d \
  --network app \
  -p 127.0.0.1:8888:8888 \
  --memory 1g \
  --cpus 1 \
  --log-opt max-size=10m \
  --log-opt max-file=3 \
  --name web2rss \
  --restart=unless-stopped \
  ghcr.io/dxy0427/web2rss:latest
```

有 mukaku VIP 账号时追加一行：`-e MUKAKU_ACCESS_TOKEN=你的token`

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
- **mukaku**（不太灵影视）：源站种子已 VIP 化，通过公开种子流按影片匹配取回。剧集返回最近 `eps` 个集数（各清晰度版本）；电影返回最近一批。更早的历史种子需配置 `MUKAKU_ACCESS_TOKEN`（登录后取 Local Storage 的 token 值）。

## 环境变量

| 变量 | 默认 | 说明 |
| ---- | ---- | ---- |
| `PORT` | `8888` | 监听端口 |
| `CACHE_EXPIRATION_MINUTES` | `15` | feed 缓存时长（分钟） |
| `SCRAPE_TIMEOUT_SEC` | `20` | 单请求超时（秒） |
| `RETRY_MAX` | `2` | 重试次数 |
| `RETRY_INTERVAL_SEC` | `1` | 重试间隔基数 |
| `MAX_CONCURRENCY` | 不限 | 并发抓取上限（内部另有更小上限） |
| `MUKAKU_ACCESS_TOKEN` | 空 | mukaku VIP 登录令牌（可选） |

## 注意

- 防封：mukaku 内置节流、并发限制和页面共享缓存，订阅 10~20 部没有压力；数量更多时优先调大 `CACHE_EXPIRATION_MINUTES`，并避免同时集中添加订阅。
- 6v 影片页路径需取自 hao6v.org（旧版镜像的 ID 与路径不通用）。
- 抓取失败只缓存 30 秒，源站恢复后自动重试；中断的残缺结果不会被长缓存。

## 配合 ani-rss 使用

四站输出均为标准 RSS（条目带磁力或种子 enclosure），可直接作为 ani-rss 的订阅地址。各站标题风格不同，按需在订阅里配置"自定义集数规则"：

| 站点 | 标题样式 | 建议集数正则 |
| ---- | ---- | ---- |
| mukaku | `遮天.年番1[第178-179集][无字片源]...` | `第(\d+)(?:-\d+)?集`（单集/区间通用） |
| btbtla | `武神主宰[第614-628集][无字片源]...` | `第(\d+)(?:-\d+)?集`（取合集首集） |
| 1lou | `繁花[第01-06集][国语配音]...` | `第(\d+)(?:-\d+)?集`（取合集首集） |
| 6v123 | `遮天 动画版 01-03.1080pHD...` | ` (\d+)(?:-\d+)?\.`（空格+集数+点，区间通用） |

提示：

- **6v123 单片订阅**首次会返回该页全部历史磁力（长篇连载可达数百条），建议开启 ani-rss 的"只下载最新集"，或用"匹配"正则限定集数范围
- **mukaku** 用 `?eps=` 控制返回的最近集数，追更建议默认 5；集数标签跨集重复时（补档）会多出几条，用"排除"正则过滤即可
- **1lou** 的 enclosure 是站内 .torrent 直链（无需登录），其余三站为磁力链接
- **1lou 搜索订阅**返回的是关键词下的多部资源，用"匹配"正则锁定其中一部
