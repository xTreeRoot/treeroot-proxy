# 生产服务器信息

核对日期：2026-10-30。以下是实际检查结果；配置可能随后续维护变化，以线上与最新备份为准。

## 基础资料

| 项目 | 值 |
| --- | --- |
| 管理网站、订阅域名、TLS SNI | `https://proxy.treeroot.top` |
| 公网 IPv4 | `186.244.200.45` |
| SSH | `root@186.244.200.45`，TCP 22 |
| 主机名 | `ser288843446808` |
| 系统 | Ubuntu 24.04.1 LTS，x86_64 |
| 当前内核 | `6.8.0-48-generic` |
| CPU / 内存 / 根盘 | 2 vCPU / 3916 MiB / 39 GiB，交付时可用约 35 GiB |
| CPU 型号 | Intel Xeon Gold 6133 @ 2.50 GHz，虚拟机 |
| 网卡、私网 | `ens17`，`10.0.90.2/24`；公网地址经供应商映射，不直接显示在网卡上 |
| 时区、时间同步 | `Etc/UTC`，NTP 同步正常 |
| Xray / Caddy | `26.10.29` / `2.11.7` |
| 代理协议 | VMess + WebSocket + TLS，443 |
| UFW | 交付检查时 inactive；供应商侧防火墙设置未提供 |
| 服务器供应商、控制台、订单、续费日 | 用户尚未提供，不能从 IP 推断 |

服务器已安装更新版内核但尚未重启。后续若安排系统重启，应先备份，并在重启后核对代理、后台和定时器。

## 端口和请求流向

| 端口 | 监听 | 用途 |
| --- | --- | --- |
| TCP 22 | 公网 | SSH |
| TCP 80 | 公网 | Caddy HTTPS 跳转及 ACME HTTP 验证 |
| TCP 443 | 公网 | Caddy HTTPS：管理网站、订阅、WebSocket 代理 |
| TCP 10001 | `127.0.0.1` | Xray VMess WebSocket 入站 |
| TCP 10085 | `127.0.0.1` | Xray Handler/Stats/Logger gRPC API |
| TCP 19991 | `127.0.0.1` | uvicorn / FastAPI |
| TCP 2019 | `127.0.0.1` | Caddy 本机管理接口 |

Caddy 按 WebSocket 路径把代理转发至 Xray，其余域名请求交给 FastAPI。具体路径含在私密配置快照中。保留的 IP 站点只处理代理及原始用户旧订阅，不提供公开后台；迁移不应继续依赖旧 IP 链接。

TLS 由 Caddy 向 Let's Encrypt 申请和自动续期；ACME 配置禁用了 TLS-ALPN challenge，所以必须保持公网 80 可达。Caddy 证书及 ACME 账号状态位于 `/var/lib/caddy`。当前仅启用 HTTP/1.1、HTTP/2，正常方案不依赖 UDP 443。

## DNS

| 项目 | 值 |
| --- | --- |
| DNS 平台 | 阿里云云解析 |
| 域名 / 主机记录 | `treeroot.top` / `proxy` |
| 类型 / 值 / 线路 / TTL | A / `186.244.200.45` / default / 600 秒 |
| RecordId | `2116072018348275712` |
| NS | `dns3.hichina.com`、`dns4.hichina.com` |
| 本机 Alibaba CLI profile | `treeroot-dns`，OAuth |

只允许针对这个子域名维护解析，保留根域名及其他记录。脚本使用官方 `alidns UpdateDomainRecord`。OAuth 授权属于当前 Mac 的阿里云 CLI，全局凭据不随项目迁移；过期时运行 `aliyun configure --profile treeroot-dns --mode OAuth` 重新授权。

```bash
aliyun alidns DescribeDomainRecordInfo --profile treeroot-dns --RecordId 2116072018348275712
```

## 文件、用户和权限

| 内容 | 位置 | 属主 / 权限 |
| --- | --- | --- |
| 应用 | `/opt/treeroot` | 源文件 `root:root 644` |
| Python venv | `/opt/treeroot/venv` | 服务以 `treeroot` 读取运行 |
| 面板配置 | `/etc/treeroot/config.json` | `root:treeroot 640` |
| 数据目录 | `/var/lib/treeroot` | `treeroot:treeroot 700` |
| SQLite | `/var/lib/treeroot/panel.sqlite3` | `treeroot:treeroot 600`，WAL 模式 |
| Xray 配置目录 | `/usr/local/etc/xray` | `treeroot:xray 2750` |
| Xray 配置 | `/usr/local/etc/xray/config.json` | `treeroot:xray 640` |
| 访问日志目录 | `/var/log/treeroot` | `xray:treeroot 2750` |
| 访问日志 | `/var/log/treeroot/access.log` | `xray:treeroot 640` |
| Caddy 配置 | `/etc/caddy/Caddyfile` | root 管理 |
| Caddy 证书和状态 | `/var/lib/caddy` | `caddy:caddy` |
| 日志轮转 | `/etc/logrotate.d/treeroot` | root 管理 |
| 每日数据库备份 | `/var/lib/treeroot/backups` | `treeroot`，文件 600，最近 7 份 |
| 完整运维备份 | `/var/backups/treeroot` | root，目录 700 / 文件 600 |
| 运维助手 | `/root/treeroot-ops` | root 私有，经 SSH 更新 |

`treeroot` 需要附加组 `xray`，以便校验、原子更新 Xray 配置。Xray 负责写日志，面板只读取日志。不要通过放宽为 777 解决权限问题。

## systemd

所有项目单元位于 `/etc/systemd/system/`，开机自动启动。

| 单元 | 身份 | 内容 |
| --- | --- | --- |
| `xray.service` | `xray:xray` | `/usr/local/bin/xray run -config /usr/local/etc/xray/config.json` |
| `caddy.service` | `caddy:caddy` | `/usr/local/bin/caddy run --config /etc/caddy/Caddyfile` |
| `treeroot-panel.service` | `treeroot:treeroot`，附加组 xray | venv 中 uvicorn，监听 `127.0.0.1:19991` |
| `treeroot-backup.service` | `treeroot:treeroot` | 运行 `/opt/treeroot/backup.py` |
| `treeroot-backup.timer` | systemd | 每日 UTC 00:00 后随机延迟最多 300 秒，约北京时间 08:00–08:05；错过后补跑 |

`xray.service` 的 Description 仍是历史文字“Personal VLESS REALITY proxy”，实际运行的是 VMess WS。判断协议应看真实配置和订阅。

## SSH 和私密资料

已核验的 ED25519 主机指纹：

```text
SHA256:QuItjScoojydH/jwDVsK0xmjtyACgUcZl9kgY29Az2c
```

本项目 SSH 私钥为 `private/ssh/id_ed25519`，公钥已加入服务器 `/root/.ssh/authorized_keys`；保留原有授权条目。`private/ssh/known_hosts` 固定已验证主机密钥。服务器 `/etc/ssh/sshd_config.d/00-treeroot-maintenance.conf` 启用 `PubkeyAuthentication yes`，原来的密码登录与 root 登录保持开启。

需要交互维护时，从项目目录执行：

```bash
ssh -i private/ssh/id_ed25519 -o IdentitiesOnly=yes \
  -o UserKnownHostsFile=private/ssh/known_hosts \
  -o StrictHostKeyChecking=yes -p 22 root@186.244.200.45
```

密码和订阅见 `private/`，不在本文件列出。`private/snapshots/production-20261030/` 保存交付时完整面板/Xray/Caddy 配置、systemd 单元、logrotate 和文件哈希清单；更新后的状态以最新完整备份为准。
