import { useEffect, useRef, useState, type FormEvent } from "react";
import { Search, RotateCcw } from "lucide-react";
import {
  Badge,
  Button,
  Empty,
  Feedback,
  Pagination,
  UserSelect,
  useRemote,
} from "../components";
import { bytes, date, number, period } from "../lib";
import type {
  Device,
  RecordItem,
  RecordPage,
  RecordScope,
  User,
} from "../types";

const eventNames: Record<string, string> = {
  created: "添加电脑",
  setup_downloaded: "下载连接器",
  paired: "完成配对",
  connected: "连接器上线",
  disconnected: "心跳中断",
  enabled: "启用出口",
  disabled: "停用出口",
  deleted: "删除电脑",
  restored: "恢复电脑",
  ip_test: "检测出口 IP",
  connection: "代理连接",
};
const details: Record<string, string> = {
  invalid_request: "请求格式无效",
  target_rejected: "目标地址或端口被拒绝",
  target_blacklisted: "目标主机在当前出口黑名单中",
  connection_failed: "网络连接失败",
  timeout: "连接超时",
  interrupted: "连接中断",
};
type Filters = {
  user_id: string;
  device_id: string;
  search: string;
  kind: string;
  outcome: string;
  hours: string;
  start?: number;
};
const defaults: Filters = {
  user_id: "",
  device_id: "",
  search: "",
  kind: "",
  outcome: "",
  hours: "168",
};
export function recordQuery(
  filters: Filters,
  page: number,
  size: number,
  anchor: number | null,
  egress: boolean,
) {
  const params = new URLSearchParams({
    page: String(page),
    page_size: String(size),
  });
  for (const key of egress
    ? (["device_id", "kind", "outcome", "search"] as const)
    : (["user_id", "search"] as const))
    if (filters[key]) params.set(key, filters[key]);
  if (egress && Number.isSafeInteger(filters.start))
    params.set("start", String(filters.start));
  if (page > 1 && anchor) params.set("before_id", String(anchor));
  return params.toString();
}
export function RecordsPage({
  active,
  users,
  egress = false,
  scope,
}: {
  active: boolean;
  users: User[];
  egress?: boolean;
  scope?: RecordScope | null;
}) {
  const [draft, setDraft] = useState<Filters>(defaults),
    [filters, setFilters] = useState<Filters>(defaults);
  const [paging, setPaging] = useState<{
    page: number;
    size: number;
    anchor: number | null;
  }>({ page: 1, size: 25, anchor: null });
  const table = useRef<HTMLDivElement>(null);
  const activeDevices = useRemote<Device[]>(
    ["devices", "active"],
    "/egress",
    active && egress,
  );
  const deletedDevices = useRemote<Device[]>(
    ["devices", "deleted"],
    "/egress?deleted=true",
    active && egress,
  );
  useEffect(() => {
    if (!scope || !egress) return;
    const next = {
      ...defaults,
      device_id: scope.device_id,
      hours: period(scope.hours),
      kind: "request",
      ...(Number.isSafeInteger(scope.start) ? { start: scope.start } : {}),
    };
    setDraft(next);
    setFilters(next);
    setPaging((previous) => ({ ...previous, page: 1, anchor: null }));
  }, [scope, egress]);
  const params = recordQuery(
    filters,
    paging.page,
    paging.size,
    paging.anchor,
    egress,
  );
  const query = useRemote<RecordPage>(
    [egress ? "egress-records" : "activity", params],
    (egress ? "/egress/records?" : "/activity?") + params,
    active,
  );
  const data = query.data;
  useEffect(() => {
    if (data && data.page !== paging.page)
      setPaging((previous) => ({ ...previous, page: data.page }));
  }, [data, paging.page]);
  const update = (key: keyof Filters, value: string) =>
    setDraft((previous) => ({ ...previous, [key]: value }));
  function search(event?: FormEvent, reset = false) {
    event?.preventDefault();
    const next = reset
      ? { ...defaults }
      : { ...draft, search: draft.search.trim() };
    delete next.start;
    if (egress && !reset && period(next.hours) === "24")
      next.start = Math.floor(Date.now() / 1000) - 86400;
    if (reset) setDraft(next);
    setFilters(next);
    setPaging((previous) => ({ ...previous, page: 1, anchor: null }));
    table.current?.scrollTo({ top: 0 });
  }
  function turn(page: number) {
    setPaging((previous) => ({
      ...previous,
      page,
      anchor: page === 1 ? null : previous.anchor || data?.before_id || null,
    }));
    table.current?.scrollTo({ top: 0 });
  }
  const devices = [
    ...(activeDevices.data || []),
    ...(deletedDevices.data || []),
  ];
  return (
    <article className="panel records-panel">
      <form
        className="search-form"
        role="search"
        aria-label={egress ? "电脑出口记录搜索" : "访问记录搜索"}
        onSubmit={search}
      >
        {egress ? (
          <>
            <label className="field">
              <span>电脑</span>
              <select
                aria-label="筛选电脑"
                value={draft.device_id}
                onChange={(e) => update("device_id", e.target.value)}
              >
                <option value="">全部电脑</option>
                {devices.map((device) => (
                  <option key={device.id} value={device.id}>
                    {device.name}
                    {deletedDevices.data?.some((item) => item.id === device.id)
                      ? "（已删除）"
                      : ""}
                  </option>
                ))}
              </select>
            </label>
            <label className="field">
              <span>记录类型</span>
              <select
                value={draft.kind}
                onChange={(e) => update("kind", e.target.value)}
              >
                <option value="">全部类型</option>
                <option value="request">出口访问</option>
                <option value="device">设备事件</option>
              </select>
            </label>
            <label className="field">
              <span>结果</span>
              <select
                value={draft.outcome}
                onChange={(e) => update("outcome", e.target.value)}
              >
                <option value="">全部结果</option>
                <option value="success">成功</option>
                <option value="failed">失败</option>
              </select>
            </label>
            <label className="field">
              <span>统计范围</span>
              <select
                value={draft.hours}
                onChange={(e) => update("hours", e.target.value)}
              >
                <option value="168">最近 7 天</option>
                <option value="24">最近 24 小时</option>
              </select>
            </label>
          </>
        ) : (
          <label className="field">
            <span>用户</span>
            <UserSelect
              users={users}
              label="筛选用户"
              value={draft.user_id}
              onChange={(value) => update("user_id", value)}
            />
          </label>
        )}
        <label className="field search-domain">
          <span>域名 / IP</span>
          <input
            type="search"
            value={draft.search}
            maxLength={253}
            onChange={(e) => update("search", e.target.value)}
          />
        </label>
        <div className="search-actions">
          <Button variant="primary" type="submit" busy={query.isFetching}>
            <Search size={16} />
            搜索
          </Button>
          <Button onClick={() => search(undefined, true)}>
            <RotateCcw size={15} />
            重置
          </Button>
        </div>
      </form>
      <Feedback
        loading={query.isPending}
        error={query.error}
        retry={() => query.refetch()}
      />
      {(activeDevices.error || deletedDevices.error) && egress && (
        <Feedback
          error={activeDevices.error || deletedDevices.error}
          retry={() => {
            activeDevices.refetch();
            deletedDevices.refetch();
          }}
        />
      )}
      <div
        ref={table}
        className="table-scroll record-scroll"
        tabIndex={0}
        aria-label={egress ? "电脑出口详细记录表格" : "访问记录表格"}
        aria-busy={query.isFetching}
      >
        <table>
          <thead>
            <tr>
              <th>连接时间</th>
              <th>{egress ? "电脑" : "用户"}</th>
              {egress && (
                <>
                  <th>记录类型</th>
                  <th>事件</th>
                </>
              )}
              <th>目标域名 / IP</th>
              <th>端口</th>
              {egress ? (
                <>
                  <th>方法</th>
                  <th>结果</th>
                  <th>上传</th>
                  <th>下载</th>
                  <th>耗时</th>
                  <th>最近检测出口 IP</th>
                  <th>详情</th>
                </>
              ) : (
                <th>传输</th>
              )}
            </tr>
          </thead>
          <tbody>
            {data?.items.map((item) => (
              <RecordRow key={item.id} item={item} egress={egress} />
            ))}
          </tbody>
        </table>
        {!query.isPending && !data?.items.length && (
          <Empty>{query.error ? "记录暂不可用" : "暂无匹配记录"}</Empty>
        )}
      </div>
      <Pagination
        page={data?.page || paging.page}
        size={paging.size}
        total={data?.total || 0}
        pages={data?.pages || 1}
        onPage={turn}
        onSize={(size) => {
          setPaging({ page: 1, size, anchor: null });
          table.current?.scrollTo({ top: 0 });
        }}
      />
    </article>
  );
}
function RecordRow({ item, egress }: { item: RecordItem; egress: boolean }) {
  const request = item.kind === "request";
  return (
    <tr>
      <td className="numeric">{date(item.ts)}</td>
      <td className="table-identity">{item.name}</td>
      {egress && (
        <>
          <td>{request ? "出口访问" : "设备事件"}</td>
          <td>{eventNames[item.action || ""] || item.action}</td>
        </>
      )}
      <td className="domain-cell" title={item.domain}>
        {item.domain || "—"}
      </td>
      <td className="numeric">{item.port || "—"}</td>
      {egress ? (
        <>
          <td>{item.method || "—"}</td>
          <td>
            <Badge tone={item.outcome === "success" ? "success" : "danger"}>
              {item.outcome === "success"
                ? request
                  ? "已连接"
                  : "成功"
                : "失败"}
            </Badge>
          </td>
          <td className="numeric">{request ? bytes(item.upload) : "—"}</td>
          <td className="numeric">{request ? bytes(item.download) : "—"}</td>
          <td className="numeric">
            {request
              ? (item.duration_ms || 0) < 1000
                ? number(item.duration_ms || 0) + " ms"
                : ((item.duration_ms || 0) / 1000).toFixed(2) + " s"
              : "—"}
          </td>
          <td>{item.exit_ip || "—"}</td>
          <td className="detail-cell">
            {details[item.detail || ""] || item.detail || "—"}
          </td>
        </>
      ) : (
        <td>
          <span className="tag">{item.network?.toUpperCase()}</span>
        </td>
      )}
    </tr>
  );
}
