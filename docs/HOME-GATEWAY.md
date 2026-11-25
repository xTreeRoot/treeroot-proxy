# 家庭网关接入

2026-11-25：Go 家庭管理接口和 React/Ant Design 页面已部署到正式后台，Mac 内的 OpenWrt 已通过正式 HTTPS 主动配对。代码只在 main，未创建 worktree。仍有下列实机验收和能力缺口，不能宣称全部交付。

## 实际拓扑

- Mac mini Wi-Fi en1 连接现有上游，默认路由仍为 192.168.1.1；不更改这个上网路径。
- Mac Ethernet en0 为下游，不运行有线 DHCP 默认路由。Go 宿主守护与独立 vmnet 中继连接 OpenWrt LAN 和 en0。
- OpenWrt WAN 为 QEMU user NAT / 10.0.2.15，LAN 为 br-lan / 192.168.77.1/24。
- TP-LINK TL-WDR5620 千兆易展版 Ver 9.0，MAC 尾号 A571。读取本机固件脚本及真实配置后，明确关闭它的 DHCP/自动模式；固件将物理口转为 LAN。AP 实际从 OpenWrt 获取固定保留地址 192.168.77.3，不再为手机做 NAT。
- 手机实际从 OpenWrt 获得独立的 192.168.77.x 地址和 MAC；不是把 AP 当成所有家庭设备。更换 SSID 后手机私有 MAC 发生变化，旧身份的记录保留。
- Wi-Fi 名称 TreeRoot_A571，双频合一，已加密。密码只保存在本地私密文件中。

## 已验证

- 用户在真实手机直接打开 Cloudflare trace，确认出口 186.244.200.45，无需手机设置 SOCKS 或代理。
- 正式后台配对、每 5 秒认证同步、配置下发和版本回报、设备识别与独立 IP/MAC、流量、连接记录、诊断任务均已实际运行。
- 真实手机的 cloudflare.com 来源专属禁止规则已下发并应用；用户确认 trace 无法打开。其他网站及撤回的结果继续记录在后续验收条目中。
- 两个隔离身份 A/B 曾依次验证：A 拒绝 cloudflare.com 和 1.1.1.1、其他网站正常；B 放行。透明 TCP/UDP 出口、外部 DNS 重定向、fake IP 已验证。此项是虚拟实验，不能替代两台真实终端。
- LAN IPv6 明确拒绝转发；没有开放未验证的 IPv6 透明代理。
- TP-LINK 管理协议来自这台固件自身。SSID、密码、双频合一、两频段开关和信道均通过实际接口读取；SSID/加密修改经实际回读确认，用户已用新密码重新连入。
- 网关配置应用前校验 Mihomo 和 nftables、保存原配置，另起持久回退 watchdog。隔离环境已实测配置失败及 agent 崩溃后的自动回退。
- 管理离线时保留最后有效配置。隔离环境实测停止管理通道后 LAN 客户端仍经 VPS 联网，重连后继续上报；正式 HTTPS 已独立接通。
- 隔离网关 guest 关机后由宿主 Go 守护重建，procd 恢复代理和 agent。Mac 整机重启及登录前服务尚未实测。

## 规则高级模式

“家庭网络 → 访问规则 → 高级模式”可粘贴单份 YAML：`prepend`、`append`、`delete` 都是字符串数组。预览通过 Go 解析并校验，预览本身不修改配置；应用仍复用已有配置保存、CSRF、版本冲突与历史撤回接口。

支持 DOMAIN、DOMAIN-SUFFIX、DOMAIN-KEYWORD、IP-CIDR、IP-CIDR6、DST-PORT、NETWORK、MATCH；策略为 DIRECT、REJECT 或已启用用户对应的 VPS 节点名称/编号。未知类型、未知节点、额外参数、额外 YAML 字段或多文档会明确报错，不会静默忽略。

可选择全部设备、单台设备或设备组。delete 精确匹配所选范围内的类型、目标、端口、协议与动作，不删除其他范围规则。重复导入不重复新增。prepend/append 保留各数组顺序；禁止规则始终先于路由选择，以免直连或切节点绕过封禁。

用户提供的五条 DOMAIN-KEYWORD/DOMAIN 直连规则已在独立浏览器验收库完成预览和保存，并在实际运行的 Mihomo v1.19.34 上通过配置校验。未擅自把这些演示规则应用到正式家庭策略。

## 实现入口

- `gopanel/home.go`：新增 home_* 兼容表、原认证/CSRF/有界写队列、一次性配对和撤销、序号确认、事件去重、历史配置和有限任务。
- `gopanel/homegateway/`：结构化校验、规则编译与高级导入、独立 nft 表、来源绑定、DHCP 保留地址、用量账本、连接观察、主动 HTTPS 同步、任务超时和持久回退。
- `gopanel/homeap/`：仅针对已核实 TP-LINK 的私有接口，验证 MAC、登录、无线设置和重启操作；无线更改保存快照并有独立回退进程。
- `gopanel/cmd/home-gateway/`：OpenWrt ARM64 agent；生产程序 `/usr/bin/home-gateway`，配置和状态 `/etc/treeroot-home-production/`，procd 单元 `/etc/init.d/treeroot-home`。旧测试单元 treeroot-home-e2e 已停止并禁用。
- `gopanel/cmd/home-host/`：Mac VM 管理、离线磁盘修复、网络快照/回退、能力探测、物理有线中继。活动运行目录 `private/home-runtime/`，旧文稿目录磁盘只作冷备，不得同时启动。
- `gopanel/cmd/treeroot-ops/`：本轮 Go 发布路径，复用现有 SSH/目录/身份；生产库副本预验证、完整备份、连接器逐字节校验、自动程序回退、数据库与累计数检查、备份下载与逐文件哈希/SQLite 校验。
- `frontend/src/pages/Home.tsx`：家庭页面与 Ant Design 控件。所有家庭弹窗复用 tree-modal 的顶部关闭区，修复配对码输入框和关闭按钮遮挡；桌面及 390px 实测无重叠。

宿主登录服务 `top.treeroot.home-gateway` 管理专用 VM；物理网口中继由独立系统服务管理。不要重新运行旧 Python bootstrap/vm 程序，也不要给 en0 打开 DHCP 抢占 Mac 默认路由。

## 测试和发布

```sh
npm --prefix frontend run build
npm --prefix frontend test
(cd gopanel && go test ./... && go test -race ./...)
(cd gopanel && go build -trimpath -o ../work/treeroot-ops ./cmd/treeroot-ops)
work/treeroot-ops -root "$PWD" -dry-run ship
work/treeroot-ops -root "$PWD" ship
```

12 项前端测试、Go 全量和 race 测试、12 项原运维测试通过。UI 在 Codex 内浏览器验证过设备编辑、规则保存/删除确认与 Esc、记录筛选/分页、高级 YAML 预览/应用、无线弹窗、桌面/手机布局；无控制台错误。浏览器验收库与生产数据库分离。

正式发布前在生产一致副本上校验原身份、会话、统计和额度表指纹不变；发布创建完整运维备份，确认账号和连接器保留、累计数不减少、SQLite 完整性及服务状态。JS/CSS 线上均为 200 且 MIME 正确。最新完整备份已下载到 `private/backups/treeroot-20261125T130225.997543Z.tar.gz`，逐文件哈希和 SQLite 校验通过。源代码回退保留新增 home_* 表及最新数据库。

临时浏览器验收：`cd gopanel && TREEROOT_HOME_UI=1 go test -run '^TestHomeBrowserFixture$' -v -timeout 2h`。只绑定 localhost:19994，使用独立临时库。

## 未完成与边界

- 只有一台真实手机可供测试。两台真实终端同时进行域名/IP 分别拒绝和放行，以及正式通道下重启/断线恢复的完整验收尚未完成。
- 每设备限速/额度尚未完成持久账本及规则重建的实机证明，能力明确为 false，前后端不开放为可用功能。
- 远程自动升级尚未实现可验证的升级/版本回退链路，能力为 false。当前 agent 由本机 SSH 维护更新。
- 访客网络在当前全 LAN/AP 模式未验证，不显示可操作按钮；不承诺硬件未提供的无线能力。
- 同一二层网络的客户端间流量可能在 AP 内交换，不经过网关。随机 MAC 会产生新身份；MAC 冒用需要 AP 准入能力配合。
- DoH/DoT、VPN、ECH 可隐藏域名，不能承诺未知加密流量的精准域名识别。默认不解密 HTTPS。
- Wi-Fi 设置回读可确认 AP 接受配置，不能替代手机重新连入。AP 回退通过有线管理路径运行，物理断电期间无法执行。
- 尚未把任何阶段进展称作全部完成，也未发送“全部完成”邮件。

## 更换光猫

WAN 获取地址/拨号/VLAN 与设备身份、备注、地址绑定、规则和历史分开保存。当前已核实的唯一有线口是 en0，用作 LAN；系统存在的 USB 网卡服务条目不代表实际有可用第二网口。以后改为光猫有线 WAN，应先准备经实测的第二有线口/USB 网卡，并核实光猫路由/桥接、拨号账号、VLAN、IPTV。迁移先保存宿主和 guest 配置、安排独立自动回退，验证 WAN 后再切换；不得清空 SQLite 或重新配对设备来代替迁移。
