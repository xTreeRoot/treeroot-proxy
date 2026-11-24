import {
  ArrowDownToLine,
  ArrowUpFromLine,
  Cable,
  ChartNoAxesCombined,
  Layers3,
  Users,
} from "lucide-react";
import {
  Button,
  DomainRanking,
  Feedback,
  Metric,
  Pagination,
  TrafficChart,
  Transfer,
  UserSelect,
  useCardPage,
  useRemote,
} from "../components";
import { bytes, number, proxyQuery, speed } from "../lib";
import { useState } from "react";
import { UserIdentity, UserStatus } from "./Users";
import type { ProxyOverview, Rates, Trend, User } from "../types";

export function ProxyPage({
  active,
  users,
  filter,
  setFilter,
  manage,
}: {
  active: boolean;
  users: User[];
  filter: string;
  setFilter: (value: string) => void;
  manage: () => void;
}) {
  const [trend, setTrend] = useState<Trend>({ year: "", month: "", day: "" });
  const query = useRemote<ProxyOverview>(
    ["proxy", proxyQuery(filter, trend)],
    "/overview?" + proxyQuery(filter, trend),
    active,
  );
  const live = useRemote<Rates>(
    ["rates", filter],
    "/rates" + (filter ? "?user_id=" + filter : ""),
    active,
    5000,
  );
  const data = query.data,
    rates = live.error ? undefined : live.data;
  const visible = users.filter((user) => !filter || String(user.id) === filter);
  const paging = useCardPage(visible, filter);
  const now = new Date(),
    earliest = new Date(Date.now() - 31 * 86400000);
  const years = Array.from(
    { length: now.getFullYear() - earliest.getFullYear() + 1 },
    (_, i) => now.getFullYear() - i,
  );
  const days = trend.month
    ? new Date(Number(trend.year), Number(trend.month), 0).getDate()
    : 0;
  const range = trend.year
    ? `${trend.year} 年${trend.month ? ` ${trend.month} 月` : ""}${trend.day ? ` ${trend.day} 日` : ""}`
    : "最近 24 小时";
  const change = (key: keyof Trend, value: string) =>
    setTrend((previous) => ({
      ...previous,
      [key]: value,
      ...(key === "year"
        ? { month: "", day: "" }
        : key === "month"
          ? { day: "" }
          : {}),
    }));
  return (
    <div className="page-stack">
      <div className="toolbar">
        <UserSelect users={users} value={filter} onChange={setFilter} />
        {filter && (
          <Button variant="text" onClick={() => setFilter("")}>
            全部用户
          </Button>
        )}
      </div>
      <Feedback
        loading={query.isPending}
        error={query.error}
        retry={() => query.refetch()}
      />
      <div className="metric-grid">
        <Metric
          icon={Layers3}
          label="累计代理流量"
          value={data ? bytes(data.upload + data.download) : "—"}
          note={
            data && <Transfer upload={data.upload} download={data.download} />
          }
        />
        <Metric
          icon={ChartNoAxesCombined}
          label="最近 24 小时"
          value={data ? bytes(data.day_upload + data.day_download) : "—"}
          note={
            data && (
              <Transfer upload={data.day_upload} download={data.day_download} />
            )
          }
          color="purple"
        />
        <Metric
          icon={Users}
          label="启用用户"
          value={
            data ? (
              <>
                {data.active}
                <small>/ {data.users}</small>
              </>
            ) : (
              "—"
            )
          }
          color="green"
        />
        <Metric
          icon={Cable}
          label="24 小时连接数"
          value={number(data?.connections)}
          color="orange"
        />
        <Metric
          icon={ArrowUpFromLine}
          label="实时上传"
          value={speed(rates?.speed_upload)}
          note={
            live.error
              ? "实时速率暂不可用"
              : rates?.speed_upload == null
                ? "等待采样"
                : undefined
          }
        />
        <Metric
          icon={ArrowDownToLine}
          label="实时下载"
          value={speed(rates?.speed_download)}
          note={
            live.error
              ? "实时速率暂不可用"
              : rates?.speed_download == null
                ? "等待采样"
                : undefined
          }
          color="green"
        />
      </div>
      <div className="dashboard-grid">
        <article className="panel chart-panel" aria-label="代理流量趋势">
          <div className="chart-toolbar">
            <div className="date-filters">
              <label>
                年份
                <select
                  aria-label="趋势年份"
                  value={trend.year}
                  onChange={(e) => change("year", e.target.value)}
                >
                  <option value="">最近 24 小时</option>
                  {years.map((year) => (
                    <option key={year} value={year}>
                      {year} 年
                    </option>
                  ))}
                </select>
              </label>
              <label>
                月份
                <select
                  aria-label="趋势月份"
                  value={trend.month}
                  disabled={!trend.year}
                  onChange={(e) => change("month", e.target.value)}
                >
                  <option value="">全年</option>
                  {Array.from({ length: 12 }, (_, i) => (
                    <option key={i} value={i + 1}>
                      {i + 1} 月
                    </option>
                  ))}
                </select>
              </label>
              <label>
                日期
                <select
                  aria-label="趋势日期"
                  value={trend.day}
                  disabled={!trend.month}
                  onChange={(e) => change("day", e.target.value)}
                >
                  <option value="">全月</option>
                  {Array.from({ length: days }, (_, i) => (
                    <option key={i} value={i + 1}>
                      {i + 1} 日
                    </option>
                  ))}
                </select>
              </label>
            </div>
            <Button
              variant="text"
              onClick={() => setTrend({ year: "", month: "", day: "" })}
            >
              重置
            </Button>
          </div>
          {data ? (
            <TrafficChart
              points={data.series}
              granularity={data.trend.granularity}
              daySelected={!!trend.day}
              label={range + "上传和下载流量趋势"}
            />
          ) : (
            <EmptyChart />
          )}
        </article>
        <article className="panel ranking-panel">
          <DomainRanking domains={data?.domains || []} />
        </article>
      </div>
      <article className="panel user-usage-panel" aria-label="用户使用情况">
        <div className="panel-toolbar">
          <Button variant="text" onClick={manage}>
            管理用户 →
          </Button>
        </div>
        <div className="user-grid">
          {paging.items.map((user) => (
            <article key={user.id} className="user-card">
              <div className="user-card-head">
                <UserIdentity user={user} />
                <UserStatus user={user} />
              </div>
              <div className="user-stats">
                <div>
                  <span>上传</span>
                  <b>{bytes(user.upload)}</b>
                </div>
                <div>
                  <span>下载</span>
                  <b>{bytes(user.download)}</b>
                </div>
                <div>
                  <span>访问域名</span>
                  <b>{number(user.domains)}</b>
                </div>
                <div>
                  <span>累计连接</span>
                  <b>{number(user.connections)}</b>
                </div>
              </div>
            </article>
          ))}
        </div>
        <Pagination {...paging.props} />
      </article>
    </div>
  );
}
function EmptyChart() {
  return <div className="traffic-chart empty">等待统计</div>;
}
