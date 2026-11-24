import {
  createContext,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type ReactNode,
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
};
const UI = createContext<{
  notify: (message: string, error?: boolean) => void;
  confirm: (object: string, action: string) => Promise<boolean>;
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
    <UI.Provider
      value={{
        theme,
        notify: (message, error = false) => setNotice({ message, error }),
        confirm: (object, action) =>
          new Promise((resolve) => setPrompt({ object, action, resolve })),
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
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "subtle" | "danger" | "text";
  busy?: boolean;
}) {
  return (
    <button
      type="button"
      className={`button ${variant} ${className}`}
      disabled={disabled || busy}
      aria-busy={busy || undefined}
      {...props}
    >
      {busy && <LoaderCircle className="spin" size={16} aria-hidden="true" />}
      {children}
    </button>
  );
}
export function IconButton({
  label,
  children,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <button
      type="button"
      className="icon-button"
      aria-label={label}
      title={label}
      {...props}
    >
      {children}
    </button>
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
  return <div className="empty">{children}</div>;
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
        <meter min="0" max="100" value={progress} aria-label={label} />
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
    <select
      aria-label={label}
      value={value}
      onChange={(event) => onChange(event.target.value)}
    >
      <option value="">全部用户</option>
      {users.map((user) => (
        <option key={user.id} value={user.id}>
          {user.name}
        </option>
      ))}
    </select>
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
  return (
    <nav className="pagination" aria-label="分页">
      <span className="pagination-summary">
        共 {number(total)} {cards ? "位用户" : "条"}
      </span>
      <div className="pagination-controls">
        <select
          aria-label="每页数量"
          value={size}
          onChange={(event) => onSize(Number(event.target.value))}
        >
          {(cards ? [6, 12, 24] : [25, 50, 100]).map((value) => (
            <option key={value} value={value}>
              {value} / 页
            </option>
          ))}
        </select>
        <IconButton
          label="上一页"
          disabled={page <= 1}
          onClick={() => onPage(page - 1)}
        >
          <ChevronLeft size={18} />
        </IconButton>
        <span className="page-count" aria-live="polite">
          {page} / {pages || 1}
        </span>
        <IconButton
          label="下一页"
          disabled={page >= pages}
          onClick={() => onPage(page + 1)}
        >
          <ChevronRight size={18} />
        </IconButton>
      </div>
    </nav>
  );
}
export function useCardPage<T>(items: T[], scope: string) {
  const [paging, setPaging] = useState({ page: 1, size: 6, scope });
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
  const ref = useRef<HTMLDialogElement>(null);
  useLayoutEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const dialog = ref.current!;
    dialog.showModal();
    document.body.classList.add("modal-open");
    return () => {
      dialog.close();
      if (!document.querySelector("dialog[open]"))
        document.body.classList.remove("modal-open");
      previous?.focus({ preventScroll: true });
    };
  }, []);
  return (
    <dialog
      ref={ref}
      className={wide ? "modal modal-wide" : "modal"}
      aria-label={label}
      aria-busy={busy || undefined}
      onCancel={(event) => {
        event.preventDefault();
        if (!busy) onClose();
      }}
    >
      <div className="modal-top">
        <IconButton label="关闭" disabled={busy} onClick={onClose}>
          <X size={20} />
        </IconButton>
      </div>
      {children}
    </dialog>
  );
}
export function CopyField({ label, value }: { label: string; value: string }) {
  const input = useRef<HTMLInputElement>(null);
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
        <input ref={input} readOnly value={value} />
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
}: {
  points: Point[];
  granularity?: string;
  daySelected?: boolean;
  label: string;
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
          name: "下载",
          type: "line",
          smooth: 0.25,
          showSymbol: false,
          data: points.map((point) => point.download),
          lineStyle: { width: 2.5 },
          areaStyle: { opacity: 0.06 },
        },
        {
          name: "上传",
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
  }, [points, granularity, daySelected, theme]);
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
