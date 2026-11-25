import { Button as AntButton, Input } from "antd";
import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  ArrowRight,
  BarChart3,
  ChevronRight,
  ChevronsLeft,
  Globe2,
  LayoutDashboard,
  ListFilter,
  LogOut,
  Menu,
  Monitor,
  Moon,
  Settings,
  Sun,
  Users as UsersIcon,
} from "lucide-react";
import { setCSRF, write } from "./api";
import {
  Badge,
  Button,
  Feedback,
  IconButton,
  RefreshAll,
  UIProvider,
  useRemote,
  useTask,
} from "./components";
import type { Page, Rates, RecordScope, Session, User } from "./types";
import { SystemPage } from "./pages/System";
import { ProxyPage } from "./pages/Proxy";
import { UsersPage } from "./pages/Users";
import { EgressDashboard } from "./pages/EgressDashboard";
import { EgressPage } from "./pages/Egress";
import { RecordsPage } from "./pages/Records";
import { SettingsPage } from "./pages/Settings";
import { HomePage } from "./pages/Home";

export const navigation = [
  { id: "overview", label: "运行概览", icon: LayoutDashboard },
  { id: "egress-dashboard", label: "出口流量大盘", icon: Globe2 },
  { id: "proxy-dashboard", label: "代理流量大盘", icon: BarChart3 },
  { id: "users", label: "用户与订阅", icon: UsersIcon },
  { id: "activity", label: "访问记录", icon: Activity },
  { id: "egress", label: "电脑出口", icon: Monitor },
  { id: "home", label: "家庭网络", icon: Globe2 },
  { id: "egress-records", label: "出口明细", icon: ListFilter },
  { id: "settings", label: "管理设置", icon: Settings },
] as const;
function currentPage(): Page {
  const hash = location.hash.slice(1);
  return navigation.some((item) => item.id === hash)
    ? (hash as Page)
    : "overview";
}
export function Logo({ compact = false }: { compact?: boolean }) {
  return (
    <span className="logo">
      <span className="logo-icon" aria-hidden="true">
        <BarChart3 size={25} strokeWidth={2.5} />
      </span>
      {!compact && <span>TreeRoot</span>}
    </span>
  );
}
export default function App() {
  const [theme, setTheme] = useState(() => {
    try {
      return localStorage.getItem("treeroot-theme") === "dark"
        ? "dark"
        : "light";
    } catch {
      return "light";
    }
  });
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    try {
      localStorage.setItem("treeroot-theme", theme);
    } catch {
      /* Storage may be disabled. */
    }
  }, [theme]);
  return (
    <UIProvider theme={theme}>
      <SessionGate
        theme={theme}
        toggleTheme={() =>
          setTheme((previous) => (previous === "light" ? "dark" : "light"))
        }
      />
    </UIProvider>
  );
}
function SessionGate({
  theme,
  toggleTheme,
}: {
  theme: string;
  toggleTheme: () => void;
}) {
  const client = useQueryClient();
  const query = useRemote<Session>(["session"], "/me", true, 300000);
  useEffect(() => {
    const expire = () => {
      setCSRF("");
      client.cancelQueries();
      client.clear();
      client.setQueryData(["session"], { authenticated: false });
    };
    window.addEventListener("treeroot:unauthorized", expire);
    return () => window.removeEventListener("treeroot:unauthorized", expire);
  }, [client]);
  const enter = (data: Session) => {
    setCSRF(data.csrf || "");
    client.setQueryData(["session"], { ...data, authenticated: true });
  };
  // Set the token before authenticated children mount and begin their queries.
  if (query.data?.authenticated) setCSRF(query.data.csrf || "");
  if (query.isPending || query.error)
    return (
      <div className="boot-screen">
        <Logo />
        <Feedback
          loading={query.isPending}
          error={query.error}
          retry={() => query.refetch()}
        />
      </div>
    );
  return query.data?.authenticated ? (
    <Workspace session={query.data} theme={theme} toggleTheme={toggleTheme} />
  ) : (
    <Login enter={enter} />
  );
}
function Login({ enter }: { enter: (session: Session) => void }) {
  const [username, setUsername] = useState("admin"),
    [password, setPassword] = useState(""),
    [error, setError] = useState(""),
    [pending, setPending] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (pending) return;
    setPending(true);
    setError("");
    try {
      const data = await write<Session>("/login", "POST", {
        username,
        password,
      });
      setPassword("");
      enter(data);
    } catch (error) {
      setError(error instanceof Error ? error.message : "登录失败");
    } finally {
      setPending(false);
    }
  }
  return (
    <div className="login-screen">
      <a className="login-brand" href="#overview" aria-label="TreeRoot 首页">
        <Logo />
      </a>
      <form className="login-card" aria-label="登录 TreeRoot" onSubmit={submit}>
        <div className="login-mark" aria-hidden="true">
          <BarChart3 size={34} />
        </div>
        <label className="field">
          <span>管理员账号</span>
          <Input
            autoComplete="username"
            required
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
        </label>
        <label className="field">
          <span>密码</span>
          <Input.Password
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        {error && (
          <div className="feedback error" role="alert">
            {error}
          </div>
        )}
        <Button variant="primary" type="submit" className="full" busy={pending}>
          登录
          <ArrowRight size={18} />
        </Button>
      </form>
    </div>
  );
}
function Workspace({
  session,
  theme,
  toggleTheme,
}: {
  session: Session;
  theme: string;
  toggleTheme: () => void;
}) {
  const [page, setPage] = useState<Page>(currentPage),
    [open, setOpen] = useState(false),
    [collapsed, setCollapsed] = useState(false),
    [proxyFilter, setProxyFilter] = useState("");
  const [visited, setVisited] = useState<Page[]>([page]);
  useEffect(
    () =>
      setVisited((previous) =>
        previous.includes(page) ? previous : [...previous, page],
      ),
    [page],
  );
  const [mobile, setMobile] = useState(
    () => window.matchMedia("(max-width: 767px)").matches,
  );
  const [recordScope, setRecordScope] = useState<RecordScope | null>(null);
  const wasOpen = useRef(false);
  const main = useRef<HTMLElement>(null),
    menu = useRef<HTMLButtonElement>(null),
    sidebar = useRef<HTMLElement>(null);
  useLayoutEffect(() => {
    if (wasOpen.current && !open) menu.current?.focus();
    wasOpen.current = open;
  }, [open]);
  useEffect(() => {
    main.current?.focus({ preventScroll: true });
  }, [page]);
  const users = useRemote<User[]>(
    ["users"],
    "/users",
    ["users", "proxy-dashboard", "activity"].includes(page),
  );
  const archivedUsers = useRemote<User[]>(
    ["users", "deleted"],
    "/users?deleted=true",
    ["proxy-dashboard", "activity"].includes(page),
  );
  const historyUsers = [...(users.data || []), ...(archivedUsers.data || [])];
  const rates = useRemote<Rates>(["rates", ""], "/rates", true, 5000);
  const client = useQueryClient();
  const { pending, run } = useTask();
  useEffect(() => {
    const media = window.matchMedia("(max-width: 767px)");
    const resize = () => {
      setMobile(media.matches);
      if (!media.matches) setOpen(false);
    };
    media.addEventListener("change", resize);
    return () => media.removeEventListener("change", resize);
  }, []);
  function navigate(value: Page) {
    if (location.hash !== "#" + value) location.hash = value;
    setPage(value);
    setOpen(false);
    main.current?.focus({ preventScroll: true });
  }
  useEffect(() => {
    const change = () => {
      setPage(currentPage());
      setOpen(false);
    };
    window.addEventListener("hashchange", change);
    return () => window.removeEventListener("hashchange", change);
  }, []);
  useEffect(() => {
    document.title =
      "TreeRoot · " + navigation.find((item) => item.id === page)!.label;
  }, [page]);
  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement as HTMLElement;
    sidebar.current
      ?.querySelector<HTMLElement>('[aria-current="page"]')
      ?.focus();
    const keyboard = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setOpen(false);
        menu.current?.focus();
      }
      if (event.key !== "Tab") return;
      const items = sidebar.current?.querySelectorAll<HTMLElement>(
        "a, button:not(:disabled)",
      );
      if (!items?.length) return;
      const first = items[0],
        last = items[items.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      }
      if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.body.classList.add("menu-open");
    document.addEventListener("keydown", keyboard);
    return () => {
      document.body.classList.remove("menu-open");
      document.removeEventListener("keydown", keyboard);
      if (document.activeElement === document.body) previous?.focus();
    };
  }, [open]);
  const collector = rates.data?.collector;
  const healthy =
    !rates.error &&
    collector?.last_poll &&
    Date.now() / 1000 - collector.last_poll < 30 &&
    !collector.last_error;
  async function logout() {
    await write("/logout", "POST");
    setCSRF("");
    await client.cancelQueries();
    client.clear();
    client.setQueryData(["session"], { authenticated: false });
  }
  const nav = navigation.find((item) => item.id === page)!;
  return (
    <div
      className={`app-shell ${collapsed ? "sidebar-collapsed" : ""} ${open ? "sidebar-open" : ""}`}
    >
      <a
        href="#workspace"
        className="skip-link"
        onClick={(e) => {
          e.preventDefault();
          main.current?.focus();
        }}
      >
        跳到内容
      </a>
      {open && (
        <button
          type="button"
          className="sidebar-overlay"
          aria-label="关闭导航"
          onClick={() => {
            setOpen(false);
            menu.current?.focus();
          }}
        />
      )}
      <aside
        ref={sidebar}
        className="sidebar"
        aria-label="主导航区域"
        inert={mobile && !open}
      >
        <a
          className="brand"
          href="#overview"
          aria-label="TreeRoot 首页"
          onClick={(e) => {
            e.preventDefault();
            navigate("overview");
          }}
        >
          <Logo compact={collapsed} />
        </a>
        <nav className="nav-list" aria-label="主导航">
          {navigation.map(({ id, label, icon: Icon }) => (
            <a
              key={id}
              href={"#" + id}
              className={`nav-item ${page === id ? "active" : ""}`}
              aria-current={page === id ? "page" : undefined}
              title={collapsed ? label : undefined}
              onClick={(event) => {
                if (
                  event.metaKey ||
                  event.ctrlKey ||
                  event.shiftKey ||
                  event.altKey
                )
                  return;
                event.preventDefault();
                navigate(id);
              }}
            >
              <Icon size={21} strokeWidth={1.8} aria-hidden="true" />
              <span>{label}</span>
              {page === id && (
                <ChevronRight
                  className="nav-chevron"
                  size={15}
                  aria-hidden="true"
                />
              )}
            </a>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <div className="collector-status">
            <Badge tone={healthy ? "success" : "warning"}>
              {healthy
                ? "统计采集中"
                : rates.isPending
                  ? "连接中"
                  : "等待采集恢复"}
            </Badge>
          </div>
          <div className="account">
            <span className="avatar">
              {session.username?.slice(0, 1).toUpperCase()}
            </span>
            <span className="account-name">{session.username}</span>
            <IconButton
              label="退出登录"
              disabled={!!pending}
              onClick={() => run("logout", logout)}
            >
              <LogOut size={18} />
            </IconButton>
          </div>
        </div>
      </aside>
      <div className="workspace-shell" inert={mobile && open}>
        <header className="topbar">
          <AntButton
            ref={menu}
            className="icon-button mobile-menu"
            htmlType="button"
            aria-label="打开导航"
            aria-expanded={open}
            onClick={() => setOpen(true)}
          >
            <Menu size={21} />
          </AntButton>
          <IconButton
            label={collapsed ? "展开侧栏" : "收起侧栏"}
            className="icon-button desktop-menu"
            onClick={() => setCollapsed(!collapsed)}
          >
            {collapsed ? <Menu size={20} /> : <ChevronsLeft size={20} />}
          </IconButton>
          <nav className="breadcrumb" aria-label="当前位置">
            <a
              href="#overview"
              onClick={(e) => {
                e.preventDefault();
                navigate("overview");
              }}
            >
              TreeRoot
            </a>
            <ChevronRight size={14} aria-hidden="true" />
            <span>{nav.label}</span>
          </nav>
          <div className="topbar-actions">
            <RefreshAll />
            <IconButton
              label={theme === "light" ? "切换深色模式" : "切换浅色模式"}
              onClick={toggleTheme}
            >
              {theme === "light" ? <Moon size={19} /> : <Sun size={19} />}
            </IconButton>
            <span className="topbar-avatar">
              {session.username?.slice(0, 1).toUpperCase()}
            </span>
          </div>
        </header>
        <main ref={main} id="workspace" tabIndex={-1}>
          <section hidden={page !== "home"} aria-label="家庭网络">
            {(page === "home" || visited.includes("home")) && <HomePage active={page === "home"} />}
          </section>
          <section hidden={page !== "overview"} aria-label="运行概览">
            {(page === "overview" || visited.includes("overview")) && (
              <>
                <SystemPage active={page === "overview"} />
              </>
            )}
          </section>
          <section
            hidden={page !== "egress-dashboard"}
            aria-label="出口流量大盘"
          >
            {(page === "egress-dashboard" ||
              visited.includes("egress-dashboard")) && (
              <>
                <EgressDashboard
                  active={page === "egress-dashboard"}
                  records={(scope) => {
                    setRecordScope({ ...scope, key: Date.now() });
                    navigate("egress-records");
                  }}
                />
              </>
            )}
          </section>
          <section
            hidden={page !== "proxy-dashboard"}
            aria-label="代理流量大盘"
          >
            {(page === "proxy-dashboard" ||
              visited.includes("proxy-dashboard")) && (
              <>
                <ProxyPage
                  active={page === "proxy-dashboard"}
                  users={historyUsers}
                  filter={proxyFilter}
                  setFilter={setProxyFilter}
                  manage={() => navigate("users")}
                />
              </>
            )}
          </section>
          <section hidden={page !== "users"} aria-label="用户与订阅">
            {(page === "users" || visited.includes("users")) && (
              <>
                <UsersPage
                  active={page === "users"}
                  users={users.data || []}
                  loading={users.isPending}
                  error={users.error}
                  view={(id) => {
                    setProxyFilter(id);
                    navigate("proxy-dashboard");
                  }}
                />
              </>
            )}
          </section>
          <section hidden={page !== "activity"} aria-label="访问记录">
            {(page === "activity" || visited.includes("activity")) && (
              <>
                <RecordsPage
                  active={page === "activity"}
                  users={historyUsers}
                />
              </>
            )}
          </section>
          <section hidden={page !== "egress"} aria-label="电脑出口">
            {(page === "egress" || visited.includes("egress")) && (
              <>
                <EgressPage active={page === "egress"} />
              </>
            )}
          </section>
          <section
            hidden={page !== "egress-records"}
            aria-label="电脑出口详细记录"
          >
            {(page === "egress-records" ||
              visited.includes("egress-records")) && (
              <>
                <RecordsPage
                  active={page === "egress-records"}
                  users={[]}
                  egress
                  scope={recordScope}
                />
              </>
            )}
          </section>
          <section hidden={page !== "settings"} aria-label="管理设置">
            {(page === "settings" || visited.includes("settings")) && (
              <>
                <SettingsPage />
              </>
            )}
          </section>
        </main>
      </div>
    </div>
  );
}
