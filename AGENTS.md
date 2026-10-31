# TreeRoot 项目维护规则

本文件供后续接手的 Codex 或开发者使用。项目交付于 2026-10-30。

## 用户约束

- 禁止创建、使用 Git worktree。
- 仅在 `main` 开发并推送远程主分支，不创建或保留其他分支。
- 禁止在 Mac 的其他应用内使用 Computer Use；只能在 Codex 内使用。
- 只修改本项目，不操作 `/Users/xugenyin/file` 下的其他项目。
- 查阅现有代码和真实配置，复用已有接口；不要猜接口、初始化已有生产数据或盲目重构。
- 明确且已获授权的维护工作应完成并验证；关键业务意图确实不明确时再向用户确认。

## 先读这些文件

- `README.md`：项目入口和常用命令。
- `docs/SERVER.md`：服务器、DNS、路径、端口和服务清单。
- `docs/DEPLOYMENT.md`：正常更新、备份、回退和故障检查。
- `docs/MIGRATION.md`：迁移服务器完整流程。
- `deploy/server.json`：当前维护目标和固定版本下载信息；迁移后更新 `host`。

当前本地项目路径是 `/Users/xugenyin/file/treeroot-proxy`。早期工作目录里的副本只是历史交付文件，后续以本项目为准。不要依赖早期目录的 SSH ControlMaster、临时文件或绝对路径。

## 当前生产架构

- 网站：`https://proxy.treeroot.top`。
- 服务器：`186.244.200.45`，SSH `root:22`，Ubuntu 24.04 x86_64。
- **实际代理协议为 VMess + WebSocket + TLS，公网端口 443。**
- `xray.service` 的旧 Description 写着 VLESS REALITY，这是历史名称，不能据此生成客户端配置。
- Caddy 终止 TLS，把 WebSocket 转到 `127.0.0.1:10001`，把管理网站和订阅转到 `127.0.0.1:19991`。
- FastAPI + SQLite 提供管理、订阅、统计；HTML/CSS/JS 无前端构建步骤。
- Xray 的 Handler/Stats/Logger API 为 `127.0.0.1:10085`，Caddy admin 为 `127.0.0.1:2019`；均不可暴露到公网。
- 后台不是多管理员系统。每个“用户”是一个独立代理身份，统计按该身份汇总。

## 生产文件

| 内容 | 路径 |
| --- | --- |
| 程序和虚拟环境 | `/opt/treeroot`、`/opt/treeroot/venv` |
| 面板配置 | `/etc/treeroot/config.json` |
| 数据库 | `/var/lib/treeroot/panel.sqlite3` |
| Xray 配置 | `/usr/local/etc/xray/config.json` |
| Caddy 配置及证书状态 | `/etc/caddy/Caddyfile`、`/var/lib/caddy` |
| 访问日志 | `/var/log/treeroot/access.log` |
| 每日数据库备份，保留 7 份 | `/var/lib/treeroot/backups` |
| 完整运维备份 | `/var/backups/treeroot` |
| 运维辅助脚本 | `/root/treeroot-ops` |
| systemd 单元 | `/etc/systemd/system/` 下的 xray、caddy、treeroot-panel、treeroot-backup 服务和 timer |

详细属主、权限和 DNS RecordId 在 `docs/SERVER.md`。完整配置快照保存在本地 `private/snapshots/production-20261030/`，后续变更以最新完整备份中的配置为准。

## 更新程序的标准流程

在本项目目录运行：

```bash
cd /Users/xugenyin/file/treeroot-proxy
python3 ops/manage.py status
python3 -m unittest discover -s tests -v
python3 ops/manage.py --dry-run update
python3 ops/manage.py update
python3 ops/manage.py status
```

`update` 自动创建完整备份、发布白名单源文件、只重启面板并检查资源 MIME；成功后把备份下载到 `private/backups/`。它不改 DNS，不覆盖数据库、生产配置、证书或 SSH 信息，不重启 Xray/Caddy。源文件发布失败会恢复原源文件；数据库结构变更不在自动回退范围内。

- 发布白名单以 `ops/remote.py` 的 `FILES` 为准，包括面板前后端、`egress.py`、`egress.js`、`egress.css`、`connector.py`、`egress_proxy.py`、图表库、备份脚本和锁定依赖。
- 本地 `ops/`、`docs/`、`AGENTS.md` 和 `private/` 不进入 Web 程序发布包。运维脚本经 SSH 单独上传到 root 私有目录。
- 更新必须保留用户 ID、UUID、token、启用状态、管理员密码、累计流量和域名历史。
- 不要重新运行旧的初始部署脚本，不要调用初始化逻辑替代迁移，不要清空 DB 后重新生成用户。
- `update` 要求服务器已安装依赖与 `requirements.lock` 一致。修改依赖前按部署文档准备并验证独立虚拟环境，不能顺便升级生产依赖。
- 改表必须单独设计兼容迁移、备份和回退，验证后再部署；不要把源代码回退当作数据库回退。
- 改 Xray/Caddy/systemd 配置需先备份，使用官方配置校验命令，再 reload/restart，并再次做真实代理测试。

## 必须保留的行为

- 菜单分为“运行概览”“出口流量大盘”“代理流量大盘”。运行概览只放服务器资源、主机/系统/运行时信息、项目服务状态和网卡累计收发；不再混放两个流量大盘。
- 出口大盘六张主卡片为累计流量、最近 24 小时流量、启用电脑数、24 小时连接数、实时上传、实时下载，另有电脑筛选、趋势、目标排名与用量明细。选择 7 天只改变趋势和用量范围，24 小时卡片始终使用最近 24 小时。
- 出口累计数存入新增 `egress_totals`，按设备与事件 ID 检查点在同一事务增量记账，清理七天明细前先同步。首次上线只回填数据库里仍保留的连接记录，无法补回已删除的历史；之后累计数长期保留，设备删除也保留历史。
- 新连接器每 5 秒上报进程累计字节与单调时钟，`/api/egress/rates` 使用差分计算正在传输连接的速率。15 秒未更新、重启、计数回退均重新建立基线；仅统计启用且在线的电脑。旧连接器兼容，但需要重新下载安装才能显示真实实时速率；部分电脑未上报时必须说明覆盖数量。
- 出口累计/24 小时流量在连接结束并上报后入账，离线重试去重。实时速率和已完成连接的字节汇总存在自然时间差，不能把完成记录的平均值冒充实时速率。
- `/api/server` 必须登录，读取真实系统信息并缓存；不可暴露配置、订阅凭据或私钥。

- 订阅节点地址使用域名 `proxy.treeroot.top`；Clash 与 Shadowrocket 各有订阅格式。
- 重置订阅同时轮换链接 token 和代理 UUID，保留历史统计。
- 默认用户“我的设备”和交付时另一真实用户“舅舅”必须保留；不得拿真实用户做破坏性测试。
- 普通 `/api/me` 未登录时返回 200 和 `authenticated:false`，需要登录的接口仍返回 401。
- 旧链接兼容使用明确的 `/{token}/clash.yaml` 与 `/{token}/shadowrocket` 路由。**不要恢复 `/{token}/{kind}` 这种宽匹配，会截获 `/assets/app.js` 与 CSS。**
- 验证 JS/CSS 返回 200 且 MIME 正确，不能只检查首页状态码。
- 统计采集读取 Xray 增量计数和访问日志；重启/迁移必须处理计数基线和日志游标，避免重复计数。
- 域名访问数量代表连接次数，不等于页面浏览量。当前不统计每个域名的字节数，不解密 HTTPS 正文。

## 源码 Release 与服务器清理

- 用户所说的 Release 是代码库 `releases/` 内打包好的源码备份，不是 GitHub Releases，也不是含生产数据的完整运维备份。本地仓库和服务器 `/root/treeroot-ops/releases` 各只保留最新两个版本。
- 完成功能、测试和文档后先提交源码，再运行 `python3 ops/releases.py pack vYYYY.MM.DD.N --ref 源码提交`，随后提交生成的两个压缩包、校验文件及 `manifest.json`。包内记录来源提交及逐文件哈希，排除 `private/`、Git、虚拟环境与 `releases/` 自身。
- `python3 ops/releases.py list` 查看版本；`python3 ops/releases.py sync` 同步两个已验证源码包到服务器私有目录。打包会先验证保留的两版，再删除更老的源码包。源码备份包含文档和运维脚本，不能直接当成 `manage.py update` 的运行文件白名单包。
- 同步后运行 `python3 ops/manage.py cleanup` 检查，再运行 `python3 ops/manage.py cleanup --apply` 清理。必须先验证服务器上的两份源码包；只删除本项目旧的发布/布局临时包以及 apt/pip 可重建缓存。保留其他项目、当前程序/虚拟环境、配置、证书、数据库、原始访问日志、每日 DB 备份及 `/var/backups/treeroot` 完整运维备份。
- 本次累计表是兼容新增：部署前备份，并在生产 DB 副本上验证重复初始化、累计数与身份不变。代码回退时保留 `egress_totals` 和最新 DB，重新升级会补记检查点之后尚在保留期内的记录；若旧版长期运行至记录过期，无法恢复其间已删除的明细。

## 凭据和 SSH

- 私密资料仅放 `private/`，已加入 `.gitignore`；目录 700，密钥、配置快照、凭据和备份文件 600。
- `private/服务器访问说明.md`、`private/管理后台登录信息.txt` 和 `private/订阅链接.txt` 保存交付时访问资料。
- `private/ssh/id_ed25519` 是本项目运维私钥，`known_hosts` 保存已验证主机密钥；脚本使用严格主机校验。
- 生产已启用 SSH 公钥登录，同时保留原密码登录；不要因换服务器而静默关闭现有登录方式。
- 新机器通过 `python3 ops/manage.py --host 新IP authorize` 接入；交互核对供应商提供的 SSH 指纹并输入新机器密码。
- 发现主机密钥变更，先核实是否重装或 IP 重用；不要自动删除 known_hosts 或关闭校验。
- 阿里云 CLI profile 为 `treeroot-dns`，OAuth 凭据属于本机全局配置，不得复制进项目或发布包。
- 不在普通文档、聊天输出、提交、截图中展示密码、UUID、订阅 token、私钥或完整生产配置。

## 换服务器

按 `docs/MIGRATION.md` 执行：先在线备份与新机预验证，再最终停用旧入口并备份、替换新机状态、验证、切换原 A 记录，最后修改 `deploy/server.json`。保留域名和 DB 内身份才能保留订阅。

`backup --stop-services` 会停止旧服务并在成功时保持停止，仅用于最终迁移。不要把它用于日常更新。迁移中止时按文档恢复旧机。迁移有维护窗口，不承诺零停机。

只修改 `proxy.treeroot.top` 的现有 A 记录，不改根域名或其他 DNS。没有用户明确要求，不删除或退订旧服务器。

## 验证要求

- 运维变更：运行 `tests/test_ops.py`，验证备份哈希、SQLite 完整性和发布包无私密文件。
- 普通发布：服务状态、公开 HTTPS、静态资源 MIME、数据库状态、现有身份保留、累计数不减少。
- 修改代理、订阅或网络配置：通过实际 Clash/兼容内核测试出口，不能仅凭“服务器 curl 能访问 Google”宣布客户端可用。
- UI 改动：检查控制台、资源请求和移动端布局；可在 Codex 内浏览，或使用服务器的无头浏览器。
- 初始交付已验证真实线上备份和同版本更新；新 VPS 恢复脚本只做了数据转换和运维测试，尚未在另一台全新 VPS 实机演练。后续不能据此声称迁移已实机验证。
