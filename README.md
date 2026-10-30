# TreeRoot 代理与管理后台

管理网站：<https://proxy.treeroot.top>。当前服务器为 `186.244.200.45`，协议为 **VMess + WebSocket + TLS**。支持把链接导入 Mac/手机 Clash 以及 Shadowrocket。

本地维护目录：`/Users/xugenyin/file/treeroot-proxy`。后续修改、更新和迁移以此目录为准。

## 最常用的命令

```bash
cd /Users/xugenyin/file/treeroot-proxy
python3 ops/manage.py status       # 检查服务、数据库和静态资源
python3 ops/manage.py backup       # 完整备份，并下载到本机
python3 ops/manage.py update       # 修改代码后发布，自动先备份
```

本机需要 Python 3.12+ 与系统自带 SSH/SCP；运维脚本没有额外 pip 依赖。更新 DNS 时还需要已登录的阿里云 CLI。生产 FastAPI 依赖固定在 `requirements.lock`。

## 文档入口

| 文件 | 说明 |
| --- | --- |
| [AGENTS.md](AGENTS.md) | 给后续 Codex/开发者的维护规则和部署入口 |
| [服务器信息](docs/SERVER.md) | IP、DNS、端口、服务、目录、权限、SSH 指纹 |
| [更新与备份](docs/DEPLOYMENT.md) | 发布程序、依赖变更、备份和故障恢复 |
| [换服务器](docs/MIGRATION.md) | 备份、恢复新机、测试、切 DNS、回退 |
| `deploy/server.json` | 当前 IP、域名、DNS RecordId、固定软件版本 |
| `private/服务器访问说明.md` | 服务器登录方式与交付时密码；只在本机查看 |
| `private/管理后台登录信息.txt` | 后台初始登录信息；改密后以实际密码为准 |
| `private/订阅链接.txt` | 交付时默认用户的订阅链接；后台可复制各用户的当前链接 |
| `private/backups/` | 含数据库、配置、证书的完整恢复包 |

`private/` 含有可登录服务器或使用代理的资料，已排除 Git 跟踪；完整复制文件夹时仍会复制这些资料。

## 后台使用

- “用户与订阅”：新增、重命名、启用/禁用用户，复制 Clash/Shadowrocket 订阅。
- “重置订阅”：同时更换链接和节点凭据，保留统计；原设备需导入新链接。
- “运行概览”：按用户查看上传/下载、连接数、趋势和常用域名。
- “访问记录”：按用户、域名/IP 查看最近 7 天的连接明细。
- “管理设置”：修改管理员密码。

统计按代理身份计算；共享订阅的设备统计会合并。流量来自 Xray 用户计数器，域名来自连接日志，不解密 HTTPS 正文、密码或完整 URL。域名列表提供连接次数，没有每域名字节统计。累计流量及域名汇总保留，分钟流量明细保存 31 天，连接明细保存 7 天。

已有 IP 订阅暂时兼容，更新订阅后节点指向域名。**换服务器前应把客户端订阅链接也改成域名链接**，因为停掉旧 IP 后，旧 IP 订阅地址就无法更新了。

## 项目结构与验证

`app.py` 是 FastAPI 后端，`index.html` / `app.js` / `style.css` 是前端；`backup.py` 负责每日 SQLite 备份。`ops/` 提供 SSH 更新、完整备份和新机恢复，`tests/` 检查发布隔离及迁移数据保留。

```bash
python3 -m unittest discover -s tests -v
```

2026-10-30 已完成线上完整备份、备份哈希与 SQLite 校验，以及同版本真实更新：用户身份保留、流量不减少、Xray/Caddy 不重启。新机迁移脚本尚未在另一台 VPS 实机演练，迁移时仍须按文档先验证新机。

## 电脑出口

管理后台新增“电脑出口”：添加当前电脑、下载 10 分钟有效的一次性配对包，解压并双击 `连接.command`（macOS）或 `连接.cmd`（Windows）；也可运行 `python3 connector.py install --setup setup.json`。要求 Python 3.10+ 和 OpenSSH 客户端。macOS 登录后由 LaunchAgent 自动启动；Windows 使用当前用户登录任务。连接器自动重连，不修改系统代理。

服务器通过加密 SSH 反向隧道访问本机认证代理；平台 TLS 不解密，目标只允许公网 TCP 80/443，不可访问电脑局域网。点击“检测出口 IP”可确认实际公网地址。若本机 VPN/TUN 接管了连接器流量，平台会看到 VPN 的出口，需调整本机网络策略。

复制“应用连接码”到 SocialCrawlerHub 的登录设置，绑定后新账号使用电脑出口；已有账号需点击“应用当前电脑出口”。电脑离线时已绑定任务报错，不回退服务器 IP。连接码相当于该出口的授权凭据，请妥善保管。配对私钥与配置保存于 `~/.treeroot-connector/设备编号/`。卸载命令为 `python3 connector.py uninstall --id 设备编号`，只撤销自动启动并停止任务，保留私钥便于恢复；也应在面板停用出口。

设备支持删除与恢复。“删除”立即撤销配对和新请求授权，记录移至“已删除设备”；恢复后保持停用，需重新配对。首次安装连接器后，登录系统自动建立 SSH 加密连接；页面的“启用并自动连接”恢复已安装连接器的连接。浏览器不能直接运行本机程序，因此新电脑第一次仍需双击下载包中的连接文件。
