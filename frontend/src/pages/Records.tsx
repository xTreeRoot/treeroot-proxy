import type { TableRef } from "antd/es/table";
import { Input, type TableColumnsType } from "antd";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { Search, RotateCcw } from "lucide-react";
import {
  Badge,
  Button,
  Feedback,
  Pagination,
  UserSelect,
  SelectField,
  DataTable,
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
  const table = useRef<TableRef>(null);
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
              <SelectField
                label="筛选电脑"
                value={draft.device_id}
                onChange={(value) => update("device_id", value)}
                searchable
                options={[
                  { value: "", label: "全部电脑" },
                  ...devices.map((device) => ({
                    value: device.id,
                    label:
                      device.name +
                      (deletedDevices.data?.some(
                        (item) => item.id === device.id,
                      )
                        ? "（已删除）"
                        : ""),
                  })),
                ]}
              />
            </label>
            <label className="field">
              <span>记录类型</span>
              <SelectField
                label="记录类型"
                value={draft.kind}
                onChange={(value) => update("kind", value)}
                options={[
                  { value: "", label: "全部类型" },
                  { value: "request", label: "出口访问" },
                  { value: "device", label: "设备事件" },
                ]}
              />
            </label>
            <label className="field">
              <span>结果</span>
              <SelectField
                label="结果"
                value={draft.outcome}
                onChange={(value) => update("outcome", value)}
                options={[
                  { value: "", label: "全部结果" },
                  { value: "success", label: "成功" },
                  { value: "failed", label: "失败" },
                ]}
              />
            </label>
            <label className="field">
              <span>统计范围</span>
              <SelectField
                label="统计范围"
                value={draft.hours}
                onChange={(value) => update("hours", value)}
                options={[
                  { value: "168", label: "最近 7 天" },
                  { value: "24", label: "最近 24 小时" },
                ]}
              />
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
          <Input
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
        className="record-table-container"
        aria-label={egress ? "电脑出口详细记录表格" : "访问记录表格"}
        aria-busy={query.isFetching}
      >
        <DataTable<RecordItem>
          tableRef={table}
          rowKey="id"
          data={data?.items || []}
          columns={recordColumns(egress)}
          loading={query.isPending}
          height="min(62dvh, 520px)"
          empty={query.error ? "记录暂不可用" : "暂无匹配记录"}
        />
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
function recordColumns(egress: boolean): TableColumnsType<RecordItem> {
  const columns: TableColumnsType<RecordItem> = [
    {
      title: "连接时间",
      key: "time",
      width: 150,
      render: (_, item) => date(item.ts),
    },
    {
      title: egress ? "电脑" : "用户",
      dataIndex: "name",
      key: "name",
      width: 140,
      className: "table-identity",
    },
  ];
  if (egress)
    columns.push(
      {
        title: "记录类型",
        key: "kind",
        width: 100,
        render: (_, item) =>
          item.kind === "request" ? "出口访问" : "设备事件",
      },
      {
        title: "事件",
        key: "action",
        width: 120,
        render: (_, item) => eventNames[item.action || ""] || item.action,
      },
    );
  columns.push(
    {
      title: "目标域名 / IP",
      dataIndex: "domain",
      key: "domain",
      width: 220,
      ellipsis: true,
      render: (value) => value || "—",
    },
    {
      title: "端口",
      key: "port",
      width: 80,
      render: (_, item) => item.port || "—",
    },
  );
  if (!egress)
    columns.push({
      title: "传输",
      key: "network",
      width: 90,
      render: (_, item) => (
        <span className="tag">{item.network?.toUpperCase()}</span>
      ),
    });
  else
    columns.push(
      {
        title: "方法",
        key: "method",
        width: 100,
        render: (_, item) => item.method || "—",
      },
      {
        title: "结果",
        key: "outcome",
        width: 110,
        render: (_, item) => (
          <Badge tone={item.outcome === "success" ? "success" : "danger"}>
            {item.outcome === "success"
              ? item.kind === "request"
                ? "已连接"
                : "成功"
              : "失败"}
          </Badge>
        ),
      },
      {
        title: "上传",
        key: "upload",
        width: 110,
        align: "right",
        render: (_, item) =>
          item.kind === "request" ? bytes(item.upload) : "—",
      },
      {
        title: "下载",
        key: "download",
        width: 110,
        align: "right",
        render: (_, item) =>
          item.kind === "request" ? bytes(item.download) : "—",
      },
      {
        title: "耗时",
        key: "duration",
        width: 100,
        align: "right",
        render: (_, item) =>
          item.kind !== "request"
            ? "—"
            : (item.duration_ms || 0) < 1000
              ? number(item.duration_ms || 0) + " ms"
              : ((item.duration_ms || 0) / 1000).toFixed(2) + " s",
      },
      {
        title: "最近检测出口 IP",
        key: "ip",
        width: 160,
        render: (_, item) => item.exit_ip || "—",
      },
      {
        title: "详情",
        key: "detail",
        width: 260,
        className: "detail-cell",
        render: (_, item) => details[item.detail || ""] || item.detail || "—",
      },
    );
  return columns;
}
