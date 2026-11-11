# 更新、备份与故障处理

所有本机命令从 `/Users/xugenyin/file/treeroot-proxy` 执行。`ops/manage.py` 使用项目内密钥与 known_hosts，不依赖早期工作目录。默认目标来自 `deploy/server.json`；`--host`、`--dry-run` 必须写在子命令之前。

## 正常修改代码后发布

```bash
cd /Users/xugenyin/file/treeroot-proxy
python3 ops/manage.py status
python3 -m unittest discover -s tests -v
python3 ops/manage.py --dry-run update
python3 ops/manage.py update
python3 ops/manage.py status
```

一次 `update` 会完成：

1. 按 `ops/remote.py` 的 `FILES` 白名单打包运行文件，拒绝符号链接与额外文件；验证 Python 语法及已安装依赖版本。
2. 在旧版本运行时创建包含源码、SQLite、配置、证书和服务单元的完整备份。
3. 停止面板，替换源文件，再启动面板。Xray 与 Caddy 继续运行。
4. 重试检查 `/api/me`、CSS 和 JS 的 HTTP 状态及 MIME。
5. 发布失败恢复旧源文件；成功后把完整备份下载至本机 `private/backups/` 并验证 SHA256。

发布包不会包含 `private/`、数据库、生产配置或运维文档，也不会改 DNS。只有修改文档或 `ops/` 时，不需要为了文档发布应用；运维助手会在下一次调用时经 SSH 上传。

**数据库结构变更需要专门迁移方案。** 源文件自动回退无法撤销已经执行的数据库写入。普通发布也不安装/升级依赖。

## 发布后怎么确认

```bash
curl -fsS https://proxy.treeroot.top/api/me
curl -fsS -D - -o /dev/null https://proxy.treeroot.top/assets/style.css
curl -fsS -D - -o /dev/null https://proxy.treeroot.top/assets/app.js
```

未登录时 `/api/me` 应为 `{"authenticated":false}`；CSS 应为 `text/css`，JS（含 `echarts.min.js`）应包含 `javascript`。ECharts 5.6.0 随应用本地发布，无外部 CDN 请求。在后台确认原用户仍在、累计流量继续增长。涉及代理或订阅变更时，通过真实 Clash/小火箭访问网站，并检查出口和对应用户统计。只有服务器 curl 成功不足以证明客户端代理正常。

## 完整备份

```bash
python3 ops/manage.py backup
```

输出中的 `local_backup` 为本机恢复包，`archive` 为服务器副本；旁边的 `.sha256` 用于校验。可以在本机运行：

```bash
cd private/backups
shasum -a 256 -c treeroot-实际时间.tar.gz.sha256
```

完整恢复包包括：

- 当前生产源码与锁定依赖清单；不含 venv 和可执行二进制。
- SQLite 一致性副本，包含管理员密码哈希、代理 UUID/token、用户、累计统计和已入库的访问记录。
- 面板配置、Xray 配置、Caddyfile、Caddy 证书及 ACME 状态。
- 项目 systemd 单元、logrotate、版本与文件 SHA256 清单。

它不包括整个系统镜像、供应商设置、SSH 主机私钥、`authorized_keys`、阿里云 CLI 凭据、原始 access.log、旧的每日备份。恢复时二进制按 `deploy/server.json` 的官方地址重新下载并校验；SSH 使用新机自己的主机密钥，运维公钥由 `authorize` 单独安装。

日常在线备份允许继续使用代理；最后几秒尚未采集的流量可能不在该快照里。最终迁移用停服备份完成最后采集。两者不能混用。

另有服务器每日 SQLite 备份，位置 `/var/lib/treeroot/backups`，保留 7 份。它只备份数据库，单独使用不足以完整恢复服务。完整运维备份保存在 `/var/backups/treeroot`，不自动清理；维护时检查磁盘并按所需保留期整理。

把整个项目（包括 `private/`）另存至可信备份介质，才能在 Mac 或旧 VPS 丢失后恢复。`private/` 未加密，仅通过本机文件权限保护。

## 依赖、配置或系统服务变更

`requirements.lock` 固定生产版本；更新器发现缺失或不同版本会在发布前拒绝继续。若确实需要变更：

1. 先完整备份，记录当前源码及 venv，列出新增依赖和版本。
2. 在独立测试环境验证功能和数据兼容性；不要让两个采集器同时写同一个生产 DB。
3. 新建单独的生产候选 venv，例如 `/opt/treeroot/venv-next`，安装新 lock 并核对版本。Ubuntu 缺少 venv 支持时先安装 `python3-venv`。
4. 在维护窗口只停面板，将原 venv 改名保留为回退目录，在最终路径 `/opt/treeroot/venv` 重建环境并安装已验证的 lock，再发布源代码；确认服务启动和实际统计。可提前下载所需 wheel 以缩短维护时间。
5. 失败时恢复原源码和原 venv；数据库若有迁移，使用已设计并演练的回退办法。

不能直接把候选 venv 移到其他路径后继续用其中的 pip/uvicorn 脚本，因为脚本 shebang 含绝对路径。回退时先恢复原 venv 的原路径，再启动服务。

配置变更时先保存原文件并校验：

```bash
# 以下命令在服务器 root SSH 会话内运行
/usr/local/bin/xray run -test -config /usr/local/etc/xray/config.json
/usr/local/bin/caddy validate --config /etc/caddy/Caddyfile
sshd -t
```

注意：Xray 配置校验可能尝试打开日志，若是权限验证应以 xray 身份运行并核对日志权限。更改 systemd 单元后要 `systemctl daemon-reload`；只改 Caddyfile 可 `systemctl reload caddy`。Xray 重启前应先完成统计采集，并验证重启后的计数基线。

## 更新失败如何处理

更新器在源文件替换或健康检查失败时会恢复原源文件并启动面板。备份仍保留在服务器 `/var/backups/treeroot`。如果失败发生在下载备份阶段，服务器更新可能已成功，先运行 `status` 和检查源码，不要反复盲目重发。

进入 SSH 后检查：

```bash
systemctl status treeroot-panel xray caddy --no-pager
journalctl -u treeroot-panel -n 80 --no-pager
journalctl -u xray -n 50 --no-pager
journalctl -u caddy -n 50 --no-pager
df -h /
```

如果新功能上线后需要手动退回旧源文件，且没有改依赖或数据库结构，先备份当前状态，再在服务器 root 会话执行下面的白名单恢复。`BACKUP` 必须替换为已经验证的旧恢复包：

```bash
python3 /root/treeroot-ops/remote.py backup --public-ip 186.244.200.45
BACKUP="/var/backups/treeroot/treeroot-旧版本实际时间.tar.gz"
systemctl stop treeroot-panel
tar -xzf "$BACKUP" -C /opt/treeroot --strip-components=1 \
  app/app.py app/app.js app/index.html app/style.css app/echarts.min.js app/backup.py \
  app/subscription-template.json app/requirements.txt app/requirements.lock
systemctl start treeroot-panel
python3 /root/treeroot-ops/remote.py status
```

引入 ECharts 之前的旧备份没有 `app/echarts.min.js`，恢复这类备份时从命令中去掉该文件参数。恢复脚本也兼容这类旧备份。

这只恢复代码，保留最新用户和统计。若涉及 DB/配置完整恢复，使用迁移文档的 `bootstrap --replace`，不要直接把运行中的 SQLite 文件覆盖。

## 交付验证记录

2026-10-30：线上备份已下载、34 个归档文件哈希及 SQLite 完整性通过；实际执行过同版本 `update`，前后两个真实用户的身份摘要一致、流量不减少，Xray/Caddy PID 未变化，面板 PID 正常变化。运维测试还覆盖压缩包越界/链接拒绝、发布包排除秘密、迁移保留身份历史、停服备份异常恢复等行为。

新服务器脚本尚未在另一台全新 VPS 实机演练，因此必须保留迁移前预验证步骤。

2026-11-01 手机端出口统计参数修复：前端请求只发送 `24` 或 `168`，选择值为空或异常时恢复最近 24 小时；后端兼容旧页面传入的空 `hours`，其他非法范围返回中文提示。详情跳转在汇总尚未加载时省略未知的 `start`，避免把空串作为整数提交。发布时同步更新 `egress.js` 的资源版本，避免手机继续命中旧缓存；不修改数据库结构或连接器。

## 用户使用时间与代理趋势筛选

2026-11-09：用户列表与代理大盘用户卡片展示最近一次代理连接时间，复用长期保留的 `domains.last_seen`，不增加数据库字段。年月日筛选在原 `/api/overview` 上增加可选参数及 IANA 时区；年按月、月按日、日按小时，补齐无流量时段，跨日 X 轴显示日期，悬浮显示完整日期。24 小时流量和连接数独立于所选趋势范围。

本次没有依赖或数据库结构变更，按正常 `update` 备份发布；回退源码即可，保留最新数据库。趋势仍受现有 31 天明细保留期限制。验证覆盖自然月与日边界、用户筛选、闰日、夏令时、快速切换筛选的旧响应、320/390 像素布局、控制台及静态资源 MIME。

## BBRx 网络拥塞控制

2026-11-11 起使用独立拥塞控制模块 `bbrx`，上游为
<https://github.com/nivrrex/bbrx>，固定源码提交与逐文件 SHA256 见
`deploy/bbrx.json`。这是基于 BBR v1 的外部内核模块，不是内核自带的 `bbr`。
当前已在 Ubuntu 24.04 的 `6.8.0-48-generic` 编译和加载验证；上游列明的验证内核为
6.6/6.12，不能据此承诺其他内核可用。保留 `fq_codel` 和其他 TCP 参数，只切换拥塞控制。

运维脚本为 `ops/bbrx.py`，在服务器 `/root/treeroot-ops/` 单独运行，不进 Web 发布白名单。
它从固定提交下载 Makefile、源码与许可证，先校验哈希，再针对当前运行内核编译，
校验模块名、vermagic 和模块 SHA256；不执行上游安装脚本，不换内核，也不重启代理服务。
生产安装了 `gcc-13`、`make`，使用现有内核头文件；没有升级原有软件包。

初次部署顺序（本机上传脚本使用 `ops/manage.py` 的严格 SSH 配置）：

1. `python3 ops/manage.py backup`，验证完整包 SHA256、包内逐文件哈希和 SQLite 完整性。
2. 将 `ops/bbrx.py`、`ops/remote.py` 和 `deploy/bbrx.json` 分别上传为服务器
   `/root/treeroot-ops/bbrx.py`、`remote.py`、`bbrx.json`。
3. 服务器执行以下命令，替换实际构建记录、完整备份和试运行快照路径：

```bash
python3 /root/treeroot-ops/bbrx.py build
python3 /root/treeroot-ops/bbrx.py trial --build /root/treeroot-ops/bbrx/运行内核/源码提交/build.json --backup /var/backups/treeroot/完整备份.tar.gz
python3 /root/treeroot-ops/bbrx.py status
# 通过原有 VMess 节点从客户端实测出口、网页和下载，确认改善后：
python3 /root/treeroot-ops/bbrx.py confirm --snapshot /var/backups/treeroot/bbrx-时间戳/state.json
systemctl is-enabled treeroot-bbrx
systemctl is-active treeroot-bbrx
```

`trial` 先保存原算法、原持久化文件和服务状态，并建立 10 分钟自动回退定时器，
再加载模块和切换；加载失败立即回退。`confirm` 才写入并启用
`/etc/systemd/system/treeroot-bbrx.service`，确认成功后撤销定时器。
单次构建配置使用 `bbrx_floor_mbps=1`，降低对原本只有几 Mbps 线路的启动过冲；
不启用上游默认关闭的补偿开关。构建目录包含锁定源码、许可证、模块和 `build.json`。

开机服务先检查当前内核和模块哈希，再加载模块并设置算法。**内核升级后必须重编译**；
若模块不匹配、丢失或加载失败，保留正常内核默认算法，不阻断网站和代理启动。
已安装但尚未启动的其他内核未经过 BBRx 验证；不要为了启用 BBRx 重启服务器。
当前不使用 DKMS，也不向通用 sysctl 文件写入一个可能在新内核不存在的算法。

手动回退（使用同一次试运行返回的快照）：

```bash
python3 /root/treeroot-ops/bbrx.py rollback --snapshot /var/backups/treeroot/bbrx-时间戳/state.json
```

回退恢复原默认算法和启动配置，保留最新数据库、用户、日志及内核模块。
不强制卸载模块或断开用户连接：**切换仅影响新建 TCP 连接**，旧连接继续使用原算法，
客户端断开重连后才全部应用新算法。完整备份额外保存 `network/` 下的脚本、锁定配置、
安装记录和启动单元用于审计；迁移脚本不会把内核专属模块或服务盲目恢复到新机器，
需在新机另行构建和验证。

BBRx 只能缓解丢包下的吞吐下降，不会修复运营商线路或消除物理往返延迟。
性能记录见 `docs/NETWORK-20261111.md`，不能把单次测速当作长期带宽保证。

## 单账号额度与带宽限制

`limits.py` 使用 Python 标准库提供仅监听本机的认证 SOCKS5 TCP/UDP 中继，不增加生产 pip 依赖。Xray 保留 VMess/WebSocket/TLS 入站、原凭据与统计，按入站用户转入各自 SOCKS 出站；既有 API 路由与私网拦截优先，末尾兜底拒绝未知用户，限流服务不可用时不会回退直连。

新增 `user_limits`、`limit_usage`、`limit_daily` 三表，不修改用户/管理员/设备原表。初始化可重复执行；首次启用在最终统计采集后建立累计与北京时间当日基线。后续账本按中继实际授予的上传与下载字节累计，避免把 Xray 已预读但尚未转发的缓冲重复计额。设置有限额度时先提交账本再转发，因此异常退出不会补回已授予额度；极少量已记账但尚未送达的字节可能计入额度。历史累计统计仍使用原有 Xray 计数，不被替换或清零。

首次接入：先在生产 DB 副本验证初始化两次、身份和累计数不变；正常 `manage.py update` 自动创建完整备份并发布新表与代码，随后单独上传 `ops/limits_setup.py` 至 `/root/treeroot-ops/`，用 `/opt/treeroot/venv/bin/python /root/treeroot-ops/limits_setup.py --backup 完整备份绝对路径` 安装服务。脚本验证备份逐文件哈希、停止面板采集最终统计、校验候选 Xray 配置、启用单账号路由及 RoutingService，再启动面板与限流服务。**首次需要短暂重启 Xray，现有连接会重连。** Caddy、证书、DNS 和订阅身份不变。脚本异常恢复旧 Xray 配置与服务状态。

后续 `update` 仅在 `limits.py` 内容变化时重启限流服务；重启会使代理连接重连，等待新一轮统计采样后开始接入。总额度包含原累计，日额度北京时间零点恢复，限速为全部连接和上下行合计的 Mbps；单位转换为十进制 Mbps、二进制 GB，未设置为不限。设置保存后约一秒生效，手动停用、重置订阅、额度耗尽会关闭中继已有连接。限速采用共享调度，TCP 允许一个短调度片、UDP 允许一个数据报的短暂突发。

仅回退面板代码仍会保留限流路由。若整体撤销功能，应先完整备份、停止面板和限流服务、最终采集 Xray 统计，将 Xray 中 `treeroot-limit-` 开头的出站/路由删除并还原升级前配置规则（当前用户凭据仍从最新数据库生成），校验后重启 Xray，删除 `state.limits_active` 使以后重新接入重新核对基线，停用限流服务后再恢复面板。保留三张新增表、最新用户与累计，不回灌旧数据库。迁移备份包含限流单元，恢复旧版备份时允许该单元与源码缺失；新机恢复后验证其监听、额度与真实 Clash/兼容客户端。

## 电脑出口部署与回退

电脑出口是独立的新增 `egress_devices` 表，初始化执行 `CREATE TABLE IF NOT EXISTS`；已有初版出口表只增加 `deleted` 列用于可恢复删除，不会迁移或清空既有表。旧版源代码可继续使用原来的用户和统计；回退代码时可保留新增表，勿用旧 DB 覆盖最新业务数据。

出口详细记录另行新增 `egress_events` 表和索引，全部通过 `CREATE TABLE/INDEX IF NOT EXISTS` 创建，不改已有表及身份。上线前按正常更新流程完整备份；已验证重复初始化保留设备凭据、用户身份和累计数。回退时恢复旧版源码即可，保留新增表和最新数据库，不删除记录表、不回灌旧数据库。新版心跳兼容旧连接器的空 JSON 请求；访问明细需用户重新下载连接器安装，旧连接器不受此次面板发布影响。设备事件上线后开始记录，历史数据不能追溯生成。

出口流量大盘独立于代理流量大盘与运行概览。新增 `egress_totals` 累计表，不修改原表/身份；首次初始化从仍保留的请求记录回填。心跳入库与累计检查点更新位于同一事务，七天明细清理前先同步累计；重复初始化、心跳重试和进程重启不会重复记账。上线前完整备份，并在生产 DB 副本验证迁移两次及明细清理。回退代码时保留新增表和最新 DB；再次升级会补记旧代码运行期间检查点之后的记录，但无法补回超过保留期已删除的记录。

累计、24 小时卡片和趋势/目标排名使用同一读取快照；选择 7 天仅改变趋势与用量，24 小时卡片不变。实时速率复用心跳增加可选采样字段，不增依赖：新连接器每 5 秒报告活动连接的进程累计字节，服务器按单调时间差计算速率，15 秒过期。旧连接器仍可使用，但需重新下载安装才会显示真实实时速率；部分未上报设备会在界面说明。面板更新本身不会修改电脑上已安装的连接器。

首次配置：把 `ops/egress_setup.py` 单独上传到 root 私有目录，执行 `python3 /root/treeroot-ops/egress_setup.py --host 186.244.200.45`。脚本创建专用无 shell 用户、受限 SSH 反向转发、公钥查询器、只读共享组，并在验证 sshd 后 reload。面板和 SocialCrawlerHub 重启后才取得新组权限。只绑定回环端口 21000–21999，不开放公网代理端口，不改已有管理员登录方式。

更新器会将登记表、SSH 配置片段和公钥查询器一并备份。迁移新服务器后需要重新执行配置脚本，写入新服务器 IP 和实际 SSH 主机公钥；随后在电脑重新下载连接器并配对。客户端私钥始终保存在用户电脑，不在服务器备份内。旧备份缺少此功能文件时仍可恢复旧界面。

回退出口服务时先在面板停用电脑，再停止本机连接器。保留新增 DB 表和备份；移除 `/etc/ssh/sshd_config.d/40-treeroot-egress.conf` 前先备份并运行 `sshd -t`，再 reload ssh。不要取消或替换现有运维登录凭据。

## 仓库源码备份包（Release）

这里的 Release 指仓库 `releases/` 中的打包源码，不创建 GitHub Release；与含生产数据的完整运维备份分开。仅在 `main` 开发和提交，不创建 worktree 或其他分支。

```bash
# 先完成测试和源码提交，替换为未使用的版本号
python3 ops/releases.py pack v2026.10.31.2 --ref HEAD
python3 ops/releases.py list
# 提交 releases/ 中两个源码包、SHA256 与 manifest.json 后同步
python3 ops/releases.py sync
python3 ops/manage.py cleanup
python3 ops/manage.py cleanup --apply
python3 ops/manage.py status
```

第一次应为升级前的源码提交和当前源码提交各打包一版；之后每次只需打包新版本。打包按版本号保留最新两份，校验两份完整包后再删除旧包。包内 `release-manifest.json` 记录来源提交与所有文件哈希；排除私密文件、虚拟环境、Git 和递归打包 `releases/`。源码包包含文档、测试和运维工具，不能直接传给运行文件白名单更新器；需要恢复时先在本项目校验/解包，再按正常 `update` 流程发布。

服务器保存相同两版于 `/root/treeroot-ops/releases`（目录 700、文件 600）。`sync` 上传后，由 `cleanup` 校验包哈希、逐文件哈希与两版清单。清理仅删除本项目过时源码包和布局临时包、apt 下载缓存、root pip 缓存；保留其他项目、生产程序/依赖、数据库/日志、配置/证书、每日 DB 备份和 `/var/backups/treeroot` 完整备份。先检查清单，执行后再验证服务。
