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

1. 只打包 9 个运行文件，拒绝符号链接与额外文件；验证 Python 语法及已安装依赖版本。
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
