import { Segmented } from "antd";
import { useEffect, useState } from "react";
import {
  ArrowDownToLine,
  ArrowUpFromLine,
  Cable,
  ChartNoAxesCombined,
  Layers3,
  Monitor,
} from "lucide-react";
import {
  Badge,
  Button,
  DomainRanking,
  Feedback,
  Metric,
  SelectField,
  DataTable,
  TrafficChart,
  Transfer,
  useRemote,
} from "../components";
import { bytes, date, number, period, speed, successRate } from "../lib";
import type {
  DeviceOption,
  DeviceUsage,
  EgressOverview,
  Rates,
  RecordScope,
} from "../types";

export function EgressDashboard({
  active,
  records,
}: {
  active: boolean;
  records: (scope: Omit<RecordScope, "key">) => void;
}) {
  const [device, setDevice] = useState(""),
    [hours, setHours] = useState("24");
  const [options, setOptions] = useState<DeviceOption[]>([]);
  const params = new URLSearchParams({
    device_id: device,
    hours: period(hours),
  }).toString();
  const query = useRemote<EgressOverview>(
    ["egress-overview", params],
    "/egress/overview?" + params,
    active,
  );
  const live = useRemote<Rates>(
    ["egress-rates", device],
    "/egress/rates?" + new URLSearchParams({ device_id: device }),
    active,
    5000,
  );
  const data = query.data,
    rates = live.error ? undefined : live.data;
  useEffect(() => {
    if (data) setOptions(data.device_options);
  }, [data]);
  const coverage =
    rates &&
    rates.online_devices &&
    (rates.live_devices || 0) < rates.online_devices
      ? `${rates.live_devices || 0} / ${rates.online_devices} 台上报`
      : rates?.speed_upload == null
        ? "等待连接器采样"
        : undefined;
  function openRecords(id: string) {
    records({
      device_id: id,
      hours: period(hours),
      ...(Number.isSafeInteger(data?.since) ? { start: data!.since } : {}),
    });
  }
  return (
    <div className="page-stack">
      <div className="toolbar">
        <div className="toolbar-group">
          <SelectField
            label="筛选电脑"
            value={device}
            onChange={setDevice}
            searchable
            options={[
              { value: "", label: "全部电脑" },
              ...options.map((item) => ({
                value: item.id,
                label: item.name + (item.deleted ? "（已删除）" : ""),
              })),
            ]}
          />
          <Segmented
            className="period-control"
            aria-label="趋势与用量范围"
            value={period(hours)}
            onChange={(value) => setHours(period(String(value)))}
            options={[
              { value: "24", label: "最近 24 小时" },
              { value: "168", label: "最近 7 天" },
            ]}
          />
        </div>
        <Button onClick={() => openRecords(device)}>查看详细记录 →</Button>
      </div>
      <Feedback
        loading={query.isPending}
        error={query.error}
        retry={() => query.refetch()}
      />
      <div className="metric-grid">
        <Metric
          icon={Layers3}
          label="累计出口流量"
          value={data ? bytes(data.total_upload + data.total_download) : "—"}
          note={
            data && (
              <Transfer
                upload={data.total_upload}
                download={data.total_download}
              />
            )
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
          icon={Monitor}
          label="启用电脑"
          value={
            data ? (
              <>
                {data.enabled}
                <small>/ {data.available}</small>
              </>
            ) : (
              "—"
            )
          }
          note={data && `当前在线 ${data.online} 台`}
          color="green"
        />
        <Metric
          icon={Cable}
          label="24 小时连接数"
          value={number(data?.day_connections)}
          note={
            data &&
            `成功率 ${successRate(data.day_connections, data.day_succeeded)} · 失败 ${number(data.day_connections - data.day_succeeded)} 次`
          }
          color="orange"
        />
        <Metric
          icon={ArrowUpFromLine}
          label="实时上传"
          value={speed(rates?.speed_upload)}
          note={live.error ? "实时速率暂不可用" : coverage}
        />
        <Metric
          icon={ArrowDownToLine}
          label="实时下载"
          value={speed(rates?.speed_download)}
          note={live.error ? "实时速率暂不可用" : coverage}
          color="green"
        />
      </div>
      <div className="dashboard-grid">
        <article className="panel chart-panel" aria-label="出口流量趋势">
          <div className="chart-toolbar">
            <span className="range-value">
              {period(hours) === "24" ? "24 小时" : "7 天"} · 每小时
            </span>
            <span className="updated-time">{data && date(data.until)}</span>
          </div>
          {data ? (
            <TrafficChart
              points={data.series}
              label={`电脑出口最近${period(hours) === "24" ? "24 小时" : "7 天"}上传和下载流量趋势`}
            />
          ) : (
            <div className="traffic-chart empty">等待统计</div>
          )}
        </article>
        <article className="panel ranking-panel">
          <DomainRanking domains={data?.domains || []} egress />
        </article>
      </div>
      <article className="panel table-panel" aria-label="各电脑出口流量统计">
        <DataTable<DeviceUsage>
          rowKey="id"
          data={data?.devices || []}
          loading={query.isPending}
          empty="暂无电脑出口用量"
          columns={[
            {
              title: "电脑",
              dataIndex: "name",
              key: "name",
              className: "table-identity",
            },
            {
              title: "当前状态",
              key: "status",
              render: (_, item) => (
                <Badge
                  tone={
                    item.deleted || !item.enabled
                      ? "neutral"
                      : item.online
                        ? "success"
                        : "warning"
                  }
                >
                  {item.deleted
                    ? "已删除"
                    : item.online
                      ? "在线"
                      : item.enabled
                        ? "离线"
                        : "已停用"}
                </Badge>
              ),
            },
            {
              title: "驱动版本",
              key: "version",
              render: (_, item) => item.driver_version || "未上报",
            },
            {
              title: "总流量",
              key: "total",
              align: "right",
              render: (_, item) => bytes(item.upload + item.download),
            },
            {
              title: "上传",
              key: "upload",
              align: "right",
              render: (_, item) => bytes(item.upload),
            },
            {
              title: "下载",
              key: "download",
              align: "right",
              render: (_, item) => bytes(item.download),
            },
            {
              title: "连接数",
              key: "connections",
              align: "right",
              render: (_, item) => number(item.connections),
            },
            {
              title: "成功率",
              key: "success",
              align: "right",
              render: (_, item) =>
                successRate(item.connections, item.succeeded),
            },
            {
              title: "最近连接记录",
              key: "last",
              render: (_, item) => date(item.last_record),
            },
            {
              title: "操作",
              key: "actions",
              render: (_, item) => (
                <Button variant="text" onClick={() => openRecords(item.id)}>
                  查看明细
                </Button>
              ),
            },
          ]}
        />
      </article>
    </div>
  );
}
