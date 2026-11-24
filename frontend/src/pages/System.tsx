import { Cpu, HardDrive, MemoryStick, Timer } from "lucide-react";
import { Badge, Empty, Feedback, Metric, useRemote } from "../components";
import { bytes, number } from "../lib";
import type { Resources, Server } from "../types";

const services: Record<string, string> = {
  "xray.service": "Xray 代理",
  "caddy.service": "Caddy HTTPS",
  "treeroot-panel.service": "TreeRoot 面板",
  "treeroot-limits.service": "账号流量限制",
  "treeroot-backup.timer": "每日备份定时器",
  "socialcrawlerhub.service": "SocialCrawlerHub 网站",
  "socialcrawlerhub-browser.service": "SocialCrawlerHub 浏览器",
  "mysql.service": "MySQL 数据库",
};
function uptime(value?: number | null) {
  if (value == null) return "—";
  if (value >= 86400)
    return (
      <>
        {Math.floor(value / 86400)}
        <small>天</small> {Math.floor((value % 86400) / 3600)}
        <small>小时</small>
      </>
    );
  if (value >= 3600)
    return (
      <>
        {Math.floor(value / 3600)}
        <small>小时</small> {Math.floor((value % 3600) / 60)}
        <small>分</small>
      </>
    );
  return (
    <>
      {Math.floor(value / 60)}
      <small>分钟</small>
    </>
  );
}
export function SystemPage({ active }: { active: boolean }) {
  const resources = useRemote<Resources>(
    ["resources"],
    "/resources",
    active,
    5000,
  );
  const server = useRemote<Server>(["server"], "/server", active);
  const data = resources.data,
    info = server.data;
  const crawler = info?.services.find(
    (item) => item.name === "socialcrawlerhub.service",
  );
  const fields = info
    ? [
        ["主机名", info.hostname],
        ["操作系统", info.os],
        ["内核版本", info.kernel],
        ["系统架构", info.architecture],
        ["CPU 型号", info.cpu_model],
        ["服务器时区", info.timezone],
        ["网站域名", info.domain],
        ["代理协议", info.protocol],
        ["SocialCrawlerHub 网站", "https://get.treeroot.top"],
        ["SocialCrawlerHub 目录", crawler?.working_directory],
        ["运行时", info.versions?.go || info.python],
        ["系统进程数", number(info.processes)],
        ["数据库大小", bytes(info.database_bytes)],
      ]
    : [];
  return (
    <div className="page-stack">
      <Feedback
        loading={resources.isPending}
        error={resources.error}
        retry={() => resources.refetch()}
      />
      <div className="resource-grid">
        <Metric
          icon={Cpu}
          label="CPU 使用率"
          value={
            data?.cpu_percent == null ? (
              "—"
            ) : (
              <>
                {data.cpu_percent.toFixed(1)}
                <small>%</small>
              </>
            )
          }
          progress={data?.cpu_percent}
          note={
            <>
              {data?.cpu_cores ? `${data.cpu_cores} 核` : "—"}
              <span className="note-separator" />
              {data?.load_average
                ? `负载 ${data.load_average[0].toFixed(2)}`
                : "等待采样"}
            </>
          }
        />
        <Metric
          icon={MemoryStick}
          label="内存使用率"
          value={
            data?.memory ? (
              <>
                {data.memory.percent.toFixed(1)}
                <small>%</small>
              </>
            ) : (
              "—"
            )
          }
          progress={data?.memory?.percent}
          note={
            data?.memory
              ? `${bytes(data.memory.used)} / ${bytes(data.memory.total)}`
              : "等待采样"
          }
          color="purple"
        />
        <Metric
          icon={HardDrive}
          label="磁盘使用率"
          value={
            data?.disk ? (
              <>
                {data.disk.percent.toFixed(1)}
                <small>%</small>
              </>
            ) : (
              "—"
            )
          }
          progress={data?.disk?.percent}
          note={
            data?.disk
              ? `${bytes(data.disk.used)} / ${bytes(data.disk.total)}`
              : "等待采样"
          }
          color="green"
        />
        <Metric
          icon={Timer}
          label="持续运行"
          value={uptime(data?.uptime_seconds)}
          note={
            data?.sampled_at
              ? new Date(data.sampled_at * 1000).toLocaleTimeString("zh-CN", {
                  hour12: false,
                })
              : "等待采样"
          }
          color="orange"
        />
      </div>
      <Feedback
        loading={server.isPending}
        error={server.error}
        retry={() => server.refetch()}
      />
      <div className="service-grid" aria-label="服务运行状态">
        {info?.services.map((item) => (
          <article key={item.name} className="service-card">
            <div className="service-top">
              <span className="service-name">
                {services[item.name] || item.name}
              </span>
              <Badge tone={item.state === "active" ? "success" : "warning"}>
                {item.state === "active" ? "运行中" : item.state || "未知"}
              </Badge>
            </div>
            <div className="service-stats">
              <div>
                <span>进程</span>
                <b>{item.pid || "—"}</b>
              </div>
              <div>
                <span>内存</span>
                <b>{bytes(item.memory)}</b>
              </div>
            </div>
          </article>
        ))}
        {info && !info.services.length && <Empty>暂时无法读取服务状态</Empty>}
      </div>
      <article className="panel" aria-label="服务器信息">
        <dl className="server-details">
          {fields.map(([label, value]) => (
            <div key={label} className="server-detail">
              <dt>{label}</dt>
              <dd>{value ?? "暂不可用"}</dd>
            </div>
          ))}
        </dl>
        {!info && !server.isPending && <Empty>服务器信息暂不可用</Empty>}
      </article>
    </div>
  );
}
