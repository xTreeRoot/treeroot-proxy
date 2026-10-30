# 后面换服务器怎么做

目标是换 IP 而继续使用 `proxy.treeroot.top`，保留用户、订阅 token、节点 UUID、管理员密码、累计流量和已入库的访问历史。**只复制程序或只改 DNS 都不够。**

下面命令在 Mac 的本项目目录执行；标明“服务器内”的除外。涉及交互密码的步骤在终端里执行，不需要把新密码发到聊天。

## 1. 准备新机器和客户端

- 选择干净的 **Ubuntu 24.04 x86_64 / amd64** VPS，有公网 IPv4 和 root SSH。ARM 或其他发行版需要另行适配。
- 供应商安全组及机器防火墙放行 TCP **22、80、443**，保证 GitHub、Ubuntu 软件源和 Python 包下载可用。不要开放 10001/10085/19991/2019。
- 保留当前域名。先在所有 Clash/小火箭设备中，把旧 IP 订阅换成后台复制的 **`https://proxy.treeroot.top/sub/...` 域名订阅**，刷新后确认节点的服务器地址也是 `proxy.treeroot.top`。
- 不要重置用户订阅。重置会改变 UUID/token，已有设备必须重新导入。
- 保存本项目整个目录，尤其 `private/`；保留旧服务器直至新机验证完成。

以下示例的 `203.0.113.10` 是文档示例，必须换成新服务器真实公网 IPv4：

```bash
cd /Users/xugenyin/file/treeroot-proxy
NEW_IP="203.0.113.10"
python3 ops/manage.py --host "$NEW_IP" authorize
```

首次连接时，先在供应商控制台核对新机的 SSH 指纹，然后接受 SSH 提示并输入新机 root 密码。脚本安装本项目公钥、启用公钥登录并测试登录；保留原密码登录。若新机没有 `python3`，先通过供应商控制台或 SSH 安装：`apt-get update && apt-get install -y python3`。

此时 `deploy/server.json` 仍指向旧机，不要提前改。

## 2. 在线备份，恢复到新机预验证

```bash
python3 ops/manage.py backup
```

把输出的 `local_backup` 路径填入下面变量：

```bash
BACKUP="/Users/xugenyin/file/treeroot-proxy/private/backups/treeroot-实际时间.tar.gz"
python3 ops/manage.py --host "$NEW_IP" bootstrap --backup "$BACKUP"
python3 ops/manage.py --host "$NEW_IP" status
```

恢复脚本安装固定版本 Xray/Caddy、系统 venv 支持与锁定 Python 依赖；恢复配置、证书、DB、用户及服务。已有数据库时默认拒绝覆盖。恢复不改 DNS。

脚本保留 UUID/token 和历史，只清理机器相关的 Xray 计数基线、日志游标、旧登录会话及登录失败记录；因此迁移后管理员重新登录即可。新机开始空 access.log，防止旧日志重复入库。

## 3. DNS 切换前验证新机

Caddy 的域名证书状态也已恢复，所以在 DNS 仍指向旧机时，可以直接测试新 IP 的域名 HTTPS：

```bash
curl --resolve "proxy.treeroot.top:443:$NEW_IP" -fsS \
  https://proxy.treeroot.top/api/me
curl --resolve "proxy.treeroot.top:443:$NEW_IP" -fsS -D - -o /dev/null \
  https://proxy.treeroot.top/assets/style.css
curl --resolve "proxy.treeroot.top:443:$NEW_IP" -fsS -D - -o /dev/null \
  https://proxy.treeroot.top/assets/app.js
```

应该看到未登录 JSON、CSS/JS 返回 200 且类型正确。不要加 `-k` 跳过证书检查；证书失效时应先修复签发/证书再切流量。

再复制一份实际节点用于测试：只把该测试节点的 `server` 改为新 IP，TLS `servername` / SNI 和 WebSocket `Host` 仍保持 `proxy.treeroot.top`，WebSocket 路径和 UUID 保持原值。通过这份节点访问 Google 并核对出口为新 IP；用 `--resolve` 拉取原用户域名订阅时也应保留原凭据。正式配置继续保留域名。

仅在服务器运行 curl 不是代理验证。预验证阶段不要在新机做正式用户新增、重置等管理操作；随后最终恢复会覆盖新机预验证数据。

## 4. 最终同步：安排维护窗口

预验证通过后，暂停代理使用和后台用户修改，再对**旧机**执行：

```bash
python3 ops/manage.py backup --stop-services
```

这个命令关闭旧机公网入口和面板、采集最后一轮统计、停止 Xray，生成最终快照。**命令成功后旧机服务保持停止，开始维护窗口。** 服务器端备份失败会尽力恢复之前运行的服务；如果本机下载断线，旧服务可能仍是停止状态，必须检查。

把这次输出的最新 `local_backup` 作为最终包覆盖新机的预验证数据：

```bash
FINAL_BACKUP="/Users/xugenyin/file/treeroot-proxy/private/backups/treeroot-最终实际时间.tar.gz"
python3 ops/manage.py --host "$NEW_IP" bootstrap --backup "$FINAL_BACKUP" --replace
python3 ops/manage.py --host "$NEW_IP" status
```

`--replace` 会先备份新机当前状态，再恢复最终包。它只用于这里明确要替换的预验证实例，不能随意覆盖有新业务数据的服务器。

重复第 3 步 HTTPS 和实际代理检查。最终包会覆盖新机预验证产生的临时统计，这是预期行为。不要使用较早的在线备份替代最终包。

## 5. 修改域名解析，确认后更新维护目标

```bash
python3 ops/manage.py dns --ip "$NEW_IP"
```

这只更新阿里云现有 `proxy` A 记录，RecordId `2116072018348275712`。不会改根域名，也不会修改本地 `deploy/server.json`。

CLI 授权失效时在这台 Mac 重新授权：

```bash
aliyun configure --profile treeroot-dns --mode OAuth
```

也可在阿里云「云解析 DNS → treeroot.top」编辑现有 `proxy` A 记录为新 IP，保持默认线路和 TTL 600。不要新增重复 A 记录。

核对 DNS 与公开网站：

```bash
aliyun alidns DescribeDomainRecordInfo --profile treeroot-dns --RecordId 2116072018348275712
dig +short proxy.treeroot.top A
curl -fsS https://proxy.treeroot.top/api/me
```

TTL 为 600 秒，实际设备及运营商可能缓存更久；等待后刷新订阅并断开重连客户端。不要为了强制切换而重置订阅。

确认服务已切至新机后，打开 `deploy/server.json`，把 **`host` 改成新 IP**。若新机 SSH 端口不同同时调整 `ssh_port`，并将真实资料更新进 `docs/SERVER.md` 和私密访问文档。

```bash
python3 ops/manage.py status
python3 ops/manage.py backup
```

现在不带 `--host` 的维护命令应访问新机。保持域名和凭据不变的设备通常不用重新导入；若有缓存，刷新订阅与重连即可。

## 6. 最后逐项确认

- HTTPS 证书正常、后台能登录、CSS/JS 无 404/MIME 错误。
- 原有用户、订阅链接和累计流量存在，启用/禁用状态正确。
- Mac Clash、手机 Clash/Shadowrocket 实际可用，出口是新 IP。
- 按用户产生的流量和域名记录继续增加，没有重复计数。
- `treeroot-backup.timer` 正常，新机完整备份已下载。
- 所有维护命令指向新机；旧机和最终备份保留一段观察期。

不要自动退订或删除旧 VPS。整个迁移存在维护窗口，DNS 缓存期间旧 IP 停止服务的设备会暂时断开。

## 迁移中止或失败

**尚未对新机产生正式变更时**，可以先恢复旧机服务，再把 DNS 指回旧 IP。旧服务器内执行：

```bash
systemctl start xray caddy treeroot-panel
systemctl start treeroot-backup.timer
```

Mac 上恢复 DNS：

```bash
python3 ops/manage.py dns --ip 186.244.200.45
```

若已经改过 `deploy/server.json`，同时改回旧 IP。用 SSH 连接旧机时按 `docs/SERVER.md` 的命令，不能误连到新机。

**新机已经有新用户、重置订阅或新增统计时**，不能直接切回旧 DB，否则会丢状态。先对新机做最终停服备份，再把新机最新恢复包还原到旧机（`--host 旧IP bootstrap --backup 最新包 --replace`），验证后再改回 DNS。这是一次反向迁移，需要同样的维护和验证步骤。

如果旧服务器已经丢失，只能用本机 `private/backups/` 中最新完整包恢复；该包之后尚未备份的变更无法找回。每日 SQLite 备份不包含证书和完整配置，不能代替完整恢复包。

## 验证范围

当前已验证线上完整备份、同版本更新和迁移数据转换，尚无另一台新 VPS 可实机演练新机安装。购买新机后按本流程先预验证，再执行最终停服与 DNS 切换。
