import {
  createContext,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type ReactNode,
  type Ref,
} from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertCircle,
  ArrowDown,
  ArrowUp,
  Check,
  ChevronLeft,
  ChevronRight,
  Copy,
  LoaderCircle,
  X,
  type LucideIcon,
} from "lucide-react";
import {
  Button as AntButton,
  ConfigProvider,
  Empty as AntEmpty,
  Input,
  Modal as AntModal,
  Pagination as AntPagination,
  Progress,
  Select as AntSelect,
  Table,
  theme as antTheme,
  type InputRef,
  type TableColumnsType,
} from "antd";
import zhCN from "antd/locale/zh_CN";
import type { TableRef } from "antd/es/table";
import { api } from "./api";
import { bytes, number, trendDate } from "./lib";
import type { Domain, Point, User } from "./types";

export function useRemote<T>(
  key: string[],
  path: string,
  active = true,
  interval = 15000,
) {
  return useQuery<T>({
    queryKey: key,
    queryFn: ({ signal }) =>
      api<T>(path, {
        signal: AbortSignal.any([signal, AbortSignal.timeout(10000)]),
      }),
    enabled: active,
    refetchInterval: active ? interval : false,
  });
}

type Prompt = {
  object: string;
  action: string;
  resolve: (value: boolean) => void;
  detail?: string;
};
const UI = createContext<{
  notify: (message: string, error?: boolean) => void;
  confirm: (
    object: string,
    action: string,
    detail?: string,
  ) => Promise<boolean>;
  theme: string;
}>({ notify: () => {}, confirm: async () => false, theme: "light" });
export const useUI = () => useContext(UI);

export function UIProvider({
  children,
  theme,
}: {
  children: ReactNode;
  theme: string;
}) {
  const [notice, setNotice] = useState<{
    message: string;
    error: boolean;
  } | null>(null);
  const [prompt, setPrompt] = useState<Prompt | null>(null);
  useEffect(() => {
    if (!notice) return;
    const id = setTimeout(() => setNotice(null), 5000);
    return () => clearTimeout(id);
  }, [notice]);
  const answer = (value: boolean) => {
    prompt?.resolve(value);
    setPrompt(null);
  };
  return (
    <ConfigProvider
      locale={zhCN}
      button={{ autoInsertSpace: false }}
      theme={{
        algorithm:
          theme === "dark" ? antTheme.darkAlgorithm : antTheme.defaultAlgorithm,
        token: {
          colorPrimary: "#465fff",
          colorInfo: "#465fff",
          colorError: "#d92d20",
          colorBgContainer: theme === "dark" ? "#1d2939" : "#ffffff",
          colorBgElevated: theme === "dark" ? "#1d2939" : "#ffffff",
          colorBgLayout: theme === "dark" ? "#101828" : "#f9fafb",
          colorText: theme === "dark" ? "#f2f4f7" : "#1d2939",
          colorTextSecondary: theme === "dark" ? "#b0b8c7" : "#667085",
          colorBorder: theme === "dark" ? "#344054" : "#e4e7ec",
          fontFamily:
            '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif',
          fontSize: 13,
          borderRadius: 9,
          controlHeight: 42,
        },
        components: {
          Table: {
            headerBg: theme === "dark" ? "#253245" : "#f9fafb",
            cellPaddingBlockMD: 16,
          },
          Segmented: { trackBg: theme === "dark" ? "#253245" : "#eef1f6" },
        },
      }}
    >
      <UI.Provider
        value={{
          theme,
          notify: (message, error = false) => setNotice({ message, error }),
          confirm: (object, action, detail) =>
            new Promise((resolve) =>
              setPrompt({ object, action, detail, resolve }),
            ),
        }}
      >
        {children}
        {notice && (
          <div
            className={`toast ${notice.error ? "toast-error" : ""}`}
            role={notice.error ? "alert" : "status"}
          >
            {notice.error ? <AlertCircle size={20} /> : <Check size={20} />}
            <span>{notice.message}</span>
            <IconButton label="关闭通知" onClick={() => setNotice(null)}>
              <X size={16} />
            </IconButton>
          </div>
        )}
        {prompt && (
          <Modal
            label={`${prompt.action}「${prompt.object}」`}
            onClose={() => answer(false)}
          >
            <div className="confirm-symbol">
              <AlertCircle size={28} />
            </div>
            <dl className="confirm-fields">
              <div>
                <dt>操作</dt>
                <dd>{prompt.action}</dd>
              </div>
              <div>
                <dt>对象</dt>
                <dd>{prompt.object}</dd>
              </div>
              {prompt.detail && (
                <div>
                  <dt>处理结果</dt>
                  <dd>{prompt.detail}</dd>
                </div>
              )}
            </dl>
            <div className="modal-actions">
              <Button autoFocus onClick={() => answer(false)}>
                取消
              </Button>
              <Button variant="danger" onClick={() => answer(true)}>
                {prompt.action}
              </Button>
            </div>
          </Modal>
        )}
      </UI.Provider>
    </ConfigProvider>
  );
}

export function useTask() {
  const [pending, setPending] = useState("");
  const locked = useRef(false);
  const { notify } = useUI();
  async function run<T>(label: string, work: () => Promise<T>) {
    if (locked.current) return;
    locked.current = true;
    setPending(label);
    try {
      return await work();
    } catch (error) {
      notify(error instanceof Error ? error.message : "操作失败，请重试", true);
    } finally {
      locked.current = false;
      setPending("");
    }
  }
  return { pending, run };
}

export function Button({
  children,
  variant = "subtle",
  busy,
  className = "",
  disabled,
  type = "button",
  ...props
}: Omit<ButtonHTMLAttributes<HTMLButtonElement>, "color"> & {
  variant?: "primary" | "subtle" | "danger" | "text";
  busy?: boolean;
}) {
  return (
    <AntButton
      htmlType={type}
      type={
        variant === "primary" || variant === "danger"
          ? "primary"
          : variant === "text"
            ? "text"
            : "default"
      }
      danger={variant === "danger"}
      className={`tree-button ${variant} ${className}`}
      loading={busy}
      disabled={disabled || busy}
      aria-busy={busy || undefined}
      {...props}
    >
      {children}
    </AntButton>
  );
}
export function IconButton({
  label,
  children,
  className = "",
  type = "button",
  ...props
}: Omit<ButtonHTMLAttributes<HTMLButtonElement>, "color"> & { label: string }) {
  return (
    <AntButton
      htmlType={type}
      className={`icon-button ${className}`}
      aria-label={label}
      title={label}
      {...props}
    >
      {children}
    </AntButton>
  );
}
export function Badge({
  children,
  tone = "success",
}: {
  children: ReactNode;
  tone?: "success" | "neutral" | "warning" | "danger";
}) {
  return (
    <span className={`badge ${tone}`}>
      <i aria-hidden="true" />
      {children}
    </span>
  );
}
export function Empty({ children = "暂无数据" }: { children?: ReactNode }) {
  return (
    <AntEmpty
      className="empty"
      image={AntEmpty.PRESENTED_IMAGE_SIMPLE}
      description={children}
    />
  );
}
export function Feedback({
  loading,
  error,
  retry,
}: {
  loading?: boolean;
  error?: Error | null;
  retry?: () => void;
}) {
  if (error)
    return (
      <div className="feedback error" role="alert">
        <AlertCircle size={17} />
        <span>{error.message}</span>
        {retry && (
          <Button variant="text" onClick={retry}>
            重试
          </Button>
        )}
      </div>
    );
  if (loading)
    return (
      <div className="feedback" role="status">
        <LoaderCircle className="spin" size={16} />
        正在加载
      </div>
    );
  return null;
}
export function Metric({
  icon: Icon,
  label,
  value,
  note,
  progress,
  color = "brand",
}: {
  icon: LucideIcon;
  label: string;
  value: ReactNode;
  note?: ReactNode;
  progress?: number | null;
  color?: string;
}) {
  return (
    <article className="metric" aria-label={label}>
      <div className="metric-top">
        <span className={`metric-icon ${color}`}>
          <Icon size={22} strokeWidth={1.7} aria-hidden="true" />
        </span>
        <span className="metric-label">{label}</span>
      </div>
      <div className="metric-value">{value}</div>
      {progress != null && (
        <Progress
          className="resource-progress"
          percent={progress}
          showInfo={false}
          size="small"
          aria-label={label}
          strokeColor="var(--brand)"
          railColor="var(--track)"
        />
      )}
      {note && <div className="metric-note">{note}</div>}
    </article>
  );
}
export function Transfer({
  upload,
  download,
}: {
  upload: number;
  download: number;
}) {
  return (
    <span className="transfer">
      <span>
        <ArrowUp size={12} aria-hidden="true" />
        {bytes(upload)}
      </span>
      <span>
        <ArrowDown size={12} aria-hidden="true" />
        {bytes(download)}
      </span>
    </span>
  );
}
export type SelectOption = { value: string; label: string; disabled?: boolean };
export function SelectField({
  label,
  value,
  onChange,
  options,
  disabled,
  searchable = false,
  id,
  className = "",
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  options: SelectOption[];
  disabled?: boolean;
  searchable?: boolean;
  id?: string;
  className?: string;
}) {
  return (
    <AntSelect
      id={id}
      aria-label={label}
      className={`tree-select ${className}`}
      value={value}
      onChange={onChange}
      options={options}
      disabled={disabled}
      showSearch={searchable ? { optionFilterProp: "label" } : false}
      virtual={false}
      listHeight={280}
      notFoundContent={<Empty>暂无匹配项</Empty>}
    />
  );
}
export function UserSelect({
  users,
  value,
  onChange,
  label = "筛选用户",
}: {
  users: User[];
  value: string;
  onChange: (value: string) => void;
  label?: string;
}) {
  return (
    <SelectField
      label={label}
      value={value}
      onChange={onChange}
      searchable
      options={[
        { value: "", label: "全部用户" },
        ...users.map((user) => ({
          value: String(user.id),
          label: user.name + (user.deleted ? "（已删除）" : ""),
        })),
      ]}
    />
  );
}
export function Pagination({
  page,
  size,
  total,
  pages,
  onPage,
  onSize,
  cards = false,
}: {
  page: number;
  size: number;
  total: number;
  pages: number;
  onPage: (value: number) => void;
  onSize: (value: number) => void;
  cards?: boolean;
}) {
  if (!total) return null;
  return (
    <nav className="pagination" aria-label="分页">
      <span className="pagination-summary">
        共 {number(total)} {cards ? "位用户" : "条"}
      </span>
      <div className="pagination-controls">
        <SelectField
          label="每页数量"
          value={String(size)}
          onChange={(value) => onSize(Number(value))}
          options={(cards ? [6, 12, 24] : [25, 50, 100]).map((value) => ({
            value: String(value),
            label: `${value} / 页`,
          }))}
        />
        <AntPagination
          current={page}
          pageSize={size}
          total={total}
          onChange={onPage}
          showSizeChanger={false}
          simple={{ readOnly: true }}
          itemRender={(_, type, original) =>
            type === "prev" || type === "next" ? (
              <IconButton
                label={type === "prev" ? "上一页" : "下一页"}
                disabled={type === "prev" ? page <= 1 : page >= pages}
              >
                {type === "prev" ? (
                  <ChevronLeft size={17} />
                ) : (
                  <ChevronRight size={17} />
                )}
              </IconButton>
            ) : (
              original
            )
          }
        />
      </div>
    </nav>
  );
}
export function DataTable<T extends object>({
  columns,
  data,
  rowKey,
  loading,
  empty = "暂无数据",
  height,
  tableRef,
}: {
  columns: TableColumnsType<T>;
  data: T[];
  rowKey: string;
  loading?: boolean;
  empty?: ReactNode;
  height?: string | number;
  tableRef?: Ref<TableRef>;
}) {
  return (
    <Table<T>
      ref={tableRef}
      className="tree-table"
      columns={columns}
      dataSource={data}
      rowKey={rowKey}
      pagination={false}
      size="middle"
      loading={loading}
      scroll={{ x: "max-content", ...(height ? { y: height } : {}) }}
      locale={{ emptyText: <Empty>{empty}</Empty> }}
    />
  );
}
export function useCardPage<T>(items: T[], scope: string) {
  const [paging, setPaging] = useState({ page: 1, size: 12, scope });
  const pages = Math.max(1, Math.ceil(items.length / paging.size));
  const page = paging.scope === scope ? Math.min(paging.page, pages) : 1;
  return {
    items: items.slice((page - 1) * paging.size, page * paging.size),
    props: {
      page,
      size: paging.size,
      pages,
      total: items.length,
      cards: true,
      onPage: (value: number) => setPaging({ ...paging, page: value, scope }),
      onSize: (value: number) => setPaging({ page: 1, size: value, scope }),
    },
  };
}
export function Modal({
  label,
  children,
  onClose,
  busy = false,
  wide = false,
}: {
  label: string;
  children: ReactNode;
  onClose: () => void;
  busy?: boolean;
  wide?: boolean;
}) {
  const trigger = useRef(document.activeElement as HTMLElement | null);
  useLayoutEffect(
    () => () => {
      const previous = trigger.current;
      queueMicrotask(() => {
        if (previous?.isConnected) previous.focus({ preventScroll: true });
      });
    },
    [],
  );
  return (
    <AntModal
      className="tree-modal"
      open
      centered
      // Ant Design 6 forwards aria props to its outer wrapper, so name the actual dialog panel.
      panelRef={(node) => {
        const dialog = node?.closest('[role="dialog"]');
        dialog?.setAttribute("aria-label", label);
        dialog?.setAttribute("aria-busy", String(busy));
      }}
      title={null}
      footer={null}
      width={wide ? 900 : 480}
      mask={{ closable: false }}
      keyboard={!busy}
      closable={{ disabled: busy, "aria-label": "关闭" }}
      closeIcon={<X size={19} />}
      onCancel={() => {
        if (!busy) onClose();
      }}
      destroyOnHidden
    >
      {children}
    </AntModal>
  );
}
export function CopyField({ label, value }: { label: string; value: string }) {
  const input = useRef<InputRef>(null);
  const { notify } = useUI();
  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      notify("已复制");
    } catch {
      input.current?.focus();
      input.current?.select();
      notify("请复制已选中的内容");
    }
  }
  return (
    <label className="field">
      <span>{label}</span>
      <div className="copy-field">
        <Input ref={input} readOnly value={value} />
        <Button onClick={copy} aria-label={`复制 ${label}`}>
          <Copy size={16} />
          复制
        </Button>
      </div>
    </label>
  );
}

type ChartInstance = {
  setOption: (option: object, replace?: boolean) => void;
  resize: () => void;
  dispose: () => void;
};
declare global {
  interface Window {
    echarts?: {
      init: (
        element: HTMLElement,
        theme: null,
        options: object,
      ) => ChartInstance;
    };
  }
}
export function TrafficChart({
  points,
  granularity = "hour",
  daySelected = false,
  label,
  seriesNames,
}: {
  points: Point[];
  granularity?: string;
  daySelected?: boolean;
  label: string;
  seriesNames?: [string,string];
}) {
  const ref = useRef<HTMLDivElement>(null);
  const { theme } = useUI();
  useEffect(() => {
    if (!window.echarts || !ref.current) return;
    const dark = theme === "dark";
    const text = dark ? "#98a2b3" : "#667085";
    const grid = dark ? "#293347" : "#eef1f6";
    const chart = window.echarts.init(ref.current, null, { renderer: "svg" });
    chart.setOption({
      animation: !matchMedia("(prefers-reduced-motion: reduce)").matches,
      color: ["#465fff", "#12b76a"],
      textStyle: { fontFamily: "inherit" },
      grid: { left: 8, right: 18, top: 36, bottom: 12, containLabel: true },
      legend: {
        top: 0,
        right: 12,
        itemWidth: 14,
        itemHeight: 7,
        icon: "roundRect",
        textStyle: { color: text, fontSize: 11 },
      },
      tooltip: {
        trigger: "axis",
        confine: true,
        backgroundColor: dark ? "#1d2939" : "#fff",
        borderColor: grid,
        textStyle: { color: dark ? "#f2f4f7" : "#344054", fontSize: 12 },
        valueFormatter: bytes,
        axisPointer: {
          type: "line",
          lineStyle: { color: "#a4bcfd", type: "dashed" },
        },
      },
      xAxis: {
        type: "category",
        boundaryGap: false,
        data: points.map((point) => String(point.ts)),
        axisLine: { lineStyle: { color: grid } },
        axisTick: { show: false },
        axisLabel: {
          color: text,
          fontSize: 10,
          hideOverlap: true,
          formatter: (value: string) =>
            trendDate(Number(value), granularity, false, daySelected),
        },
        axisPointer: {
          label: {
            formatter: ({ value }: { value: string }) =>
              trendDate(Number(value), granularity, true),
          },
        },
      },
      yAxis: {
        type: "value",
        min: 0,
        axisLabel: { color: text, fontSize: 10, formatter: bytes },
        splitLine: { lineStyle: { color: grid, type: "dashed" } },
      },
      series: [
        {
          name: seriesNames?.[0] || "下载",
          type: "line",
          smooth: 0.25,
          showSymbol: false,
          data: points.map((point) => point.download),
          lineStyle: { width: 2.5 },
          areaStyle: { opacity: 0.06 },
        },
        {
          name: seriesNames?.[1] || "上传",
          type: "line",
          smooth: 0.25,
          showSymbol: false,
          data: points.map((point) => point.upload),
          lineStyle: { width: 2 },
          areaStyle: { opacity: 0.03 },
        },
      ],
    });
    const observer = new ResizeObserver(() => {
      if (ref.current?.clientWidth) chart.resize();
    });
    observer.observe(ref.current);
    return () => {
      observer.disconnect();
      chart.dispose();
    };
  }, [points, granularity, daySelected, theme, seriesNames?.[0], seriesNames?.[1]]);
  return (
    <div ref={ref} className="traffic-chart" role="img" aria-label={label} />
  );
}
export function DomainRanking({
  domains,
  egress = false,
}: {
  domains: Domain[];
  egress?: boolean;
}) {
  const top = domains[0]?.connections || 1;
  return (
    <div className="domain-list" aria-label="常用访问目标">
      {domains.length ? (
        domains.map((item, i) => (
          <div key={item.domain} className="domain-row">
            <span className="domain-rank">
              {String(i + 1).padStart(2, "0")}
            </span>
            <div className="domain-content">
              <div className="domain-name">
                <span title={item.domain}>{item.domain}</span>
                <span>
                  {number(item.connections)} 次
                  {egress && (
                    <small>
                      {bytes((item.upload || 0) + (item.download || 0))}
                    </small>
                  )}
                </span>
              </div>
              <div className="domain-track">
                <div style={{ width: `${(item.connections / top) * 100}%` }} />
              </div>
            </div>
          </div>
        ))
      ) : (
        <Empty>暂无访问目标</Empty>
      )}
    </div>
  );
}
export function RefreshAll() {
  const client = useQueryClient();
  const { run, pending } = useTask();
  return (
    <IconButton
      label="刷新数据"
      disabled={!!pending}
      onClick={() =>
        run("refresh", () =>
          client.invalidateQueries({
            predicate: (query) => query.queryKey[0] !== "session",
          }),
        )
      }
    >
      <LoaderCircle size={19} className={pending ? "spin" : ""} />
    </IconButton>
  );
}
