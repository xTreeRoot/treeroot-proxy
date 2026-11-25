import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import App from "../App";
import { setCSRF } from "../api";
import type { EgressOverview, User } from "../types";

const now = Math.floor(Date.now() / 1000);
const seedUsers = (): User[] =>
  Array.from({ length: 14 }, (_, i) => ({
    id: i + 1,
    name: `测试用户 ${i + 1}`,
    enabled: 1,
    created: now - 86400,
    last_used: i ? null : now,
    upload: 1024,
    download: 4096,
    domains: 4,
    connections: 9,
    clash_url: "https://test.invalid/sub/test/clash.yaml",
    shadowrocket_url: "https://test.invalid/sub/test/shadowrocket",
  }));
const egress: EgressOverview = {
  total_upload: 100,
  total_download: 200,
  day_upload: 20,
  day_download: 30,
  enabled: 2,
  available: 2,
  day_connections: 30,
  day_succeeded: 29,
  online: 2,
  hours: 24,
  since: 123,
  until: now,
  connections: 30,
  series: [],
  domains: [],
  devices: [],
  device_options: [{ id: "fixture-device", name: "测试电脑", deleted: false }],
  speed_upload: 50,
  speed_download: 60,
};
const clients: QueryClient[] = [];
let authenticated = true,
  users: User[],
  archivedUsers: User[],
  requests: { path: string; options: RequestInit }[],
  overviewDelay: Promise<EgressOverview> | undefined;
const respond = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data), {
    status,
    headers: { "Content-Type": "application/json" },
  });

beforeEach(() => {
  authenticated = true;
  users = seedUsers();
  archivedUsers = [];
  requests = [];
  overviewDelay = undefined;
  setCSRF("");
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, options: RequestInit = {}) => {
      const url = new URL(input, "https://test.invalid");
      const path = url.pathname;
      requests.push({ path: input, options });
      if (path === "/api/me")
        return respond(
          authenticated
            ? { authenticated: true, username: "admin", csrf: "fixture-csrf" }
            : { authenticated: false },
        );
      if (path === "/api/login") {
        authenticated = true;
        return respond({ username: "admin", csrf: "fixture-csrf" });
      }
      if (path === "/api/logout") {
        authenticated = false;
        return respond({ ok: true });
      }
      if (path === "/api/users") {
        if (options.method === "POST") {
          const body = JSON.parse(String(options.body));
          const user = { ...users[0], id: 15, name: body.name };
          users.push(user);
          return respond(user);
        }
        return respond(
          url.searchParams.get("deleted") === "true" ? archivedUsers : users,
        );
      }
      if (/\/users\/\d+\/limits$/.test(path))
        return respond({
          total_bytes: 1610612736,
          daily_bytes: 1073741824,
          speed_bps: 500000,
          total_used: 200,
          daily_used: 100,
          blocked: null,
          ready: true,
        });
      if (/\/users\/\d+\/reset$/.test(path)) return respond(users[0]);
      if (/\/users\/\d+\/restore$/.test(path)) {
        const id = Number(path.split("/")[3]);
        const user = archivedUsers.find((item) => item.id === id)!;
        archivedUsers = archivedUsers.filter((item) => item.id !== id);
        const restored = { ...user, deleted: false, enabled: 0 };
        users.push(restored);
        users.sort((a, b) => a.id - b.id);
        return respond(restored);
      }
      if (/\/users\/\d+$/.test(path)) {
        if (options.method === "DELETE") {
          const id = Number(path.split("/")[3]);
          const user = users.find((item) => item.id === id)!;
          archivedUsers.push({
            ...user,
            enabled: 0,
            deleted: true,
            deleted_at: now,
          });
          users = users.filter((item) => item.id !== id);
          return respond({ ok: true });
        }
        return respond(users[0]);
      }
      if (path === "/api/resources")
        return respond({
          cpu_percent: 12,
          cpu_cores: 2,
          memory: { used: 1e9, total: 4e9, percent: 25 },
          disk: { used: 4e9, total: 40e9, percent: 10 },
          uptime_seconds: 99999,
          sampled_at: now,
        });
      if (path === "/api/server")
        return respond({
          services: [],
          versions: { go: "go1.26" },
          hostname: "fixture",
          domain: "test.invalid",
        });
      if (path === "/api/rates")
        return respond({
          speed_upload: 30,
          speed_download: 40,
          collector: { last_poll: now, last_error: null },
        });
      if (path === "/api/overview")
        return respond({
          upload: 100,
          download: 200,
          day_upload: 20,
          day_download: 30,
          active: 2,
          users: 2,
          connections: 40,
          series: [],
          domains: [],
          trend: {
            granularity: url.searchParams.get("month") ? "day" : "month",
          },
        });
      if (path === "/api/egress/overview") {
        if (url.searchParams.get("device_id") && overviewDelay)
          return respond(await overviewDelay);
        return respond({
          ...egress,
          hours: Number(url.searchParams.get("hours")) || 24,
        });
      }
      if (path === "/api/egress/rates")
        return respond({
          speed_upload: 50,
          speed_download: 60,
          live_devices: 1,
          online_devices: 2,
        });
      if (path === "/api/egress/version")
        return respond({ version: "v2026.11.24.1" });
      if (path === "/api/egress")
        return respond(
          url.searchParams.has("deleted")
            ? []
            : [
                {
                  id: "fixture-device",
                  name: "测试电脑",
                  browser: { platform: "Mac" },
                  enabled: true,
                  online: false,
                  paired: false,
                  last_seen: now,
                },
              ],
        );
      if (path === "/api/egress/blacklist") return respond([]);
      if (path === "/api/activity" || path === "/api/egress/records") {
        const page = Number(url.searchParams.get("page")) || 1,
          size = Number(url.searchParams.get("page_size")) || 25;
        return respond({
          items: Array.from(
            { length: Math.min(size, 62 - (page - 1) * size) },
            (_, i) => ({
              id: 800 - (page - 1) * size - i,
              ts: now,
              name: "测试用户 1",
              domain: "example.org",
              port: 443,
              network: "tcp",
              kind: "request",
              outcome: "success",
              action: "connection",
            }),
          ),
          page,
          page_size: size,
          total: 62,
          pages: Math.ceil(62 / size),
          before_id: 800,
        });
      }
      return respond({ detail: `Unexpected fixture route ${path}` }, 404);
    }),
  );
});
afterEach(async () => {
  for (const client of clients.splice(0)) {
    await client.cancelQueries();
    client.clear();
  }
  vi.unstubAllGlobals();
});
function mount(hash = "overview") {
  location.hash = hash;
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  clients.push(client);
  return render(
    <QueryClientProvider client={client}>
      <App />
    </QueryClientProvider>,
  );
}
const clickNav = (name: string) =>
  userEvent.click(
    within(screen.getByRole("navigation", { name: "主导航" })).getByRole(
      "link",
      { name },
    ),
  );

async function choose(scope: HTMLElement, label: string, option: string) {
  await userEvent.click(within(scope).getByRole("combobox", { name: label }));
  await userEvent.click(await screen.findByRole("option", { name: option }));
}

describe("React page interactions", () => {
  it("waits for login and accepts the existing login response without an authenticated field", async () => {
    authenticated = false;
    mount();
    await screen.findByRole("button", { name: "登录" });
    expect(requests.every((item) => item.path === "/api/me")).toBe(true);
    await userEvent.type(screen.getByLabelText("密码"), "fixture-password");
    await userEvent.click(screen.getByRole("button", { name: "登录" }));
    await screen.findByRole("navigation", { name: "主导航" });
    expect(requests.some((item) => item.path === "/api/resources")).toBe(true);
  });
  it("keeps user filters on return, paginates cards, and saves exact limit units", async () => {
    mount("users");
    const section = await screen.findByRole("region", { name: "vps用户" });
    await within(section).findByText("测试用户 1", { selector: "strong" });
    await choose(section, "每页数量", "6 / 页");
    await userEvent.click(
      within(section).getByRole("button", { name: "下一页" }),
    );
    expect(
      within(section).getByText("测试用户 7", { selector: "strong" }),
    ).toBeTruthy();
    await choose(section, "筛选用户", "测试用户 1");
    await clickNav("运行概览");
    await clickNav("vps用户");
    expect(within(section).getAllByRole("article")).toHaveLength(1);
    expect(
      within(section).getByText("测试用户 1", { selector: "strong" }),
    ).toBeTruthy();
    await userEvent.click(
      within(section).getByRole("button", { name: "测试用户 1的流量限制" }),
    );
    const modal = await screen.findByRole("dialog", {
      name: "测试用户 1 · 流量限制",
    });
    await waitFor(() =>
      expect(
        (within(modal).getByLabelText("总流量上限（GB）") as HTMLInputElement)
          .value,
      ).toBe("1.5"),
    );
    await userEvent.clear(within(modal).getByLabelText("网速上限（Mbps）"));
    await userEvent.type(
      within(modal).getByLabelText("网速上限（Mbps）"),
      ".8",
    );
    await userEvent.click(
      within(modal).getByRole("button", { name: "保存限制" }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const saved = requests.find(
      (item) =>
        item.path === "/api/users/1/limits" && item.options.method === "PATCH",
    )!;
    expect(JSON.parse(String(saved.options.body))).toEqual({
      total_bytes: 1610612736,
      daily_bytes: 1073741824,
      speed_bps: 800000,
    });
    expect(
      (saved.options.headers as Record<string, string>)["X-CSRF-Token"],
    ).toBe("fixture-csrf");
  });
  it("names the reset target, supports cancel and Escape, and escapes user content", async () => {
    users[0].name = "<img src=x onerror=alert(1)>";
    mount("users");
    const section = await screen.findByRole("region", { name: "vps用户" });
    await within(section).findByText(users[0].name, { selector: "strong" });
    const card = within(section)
      .getByText(users[0].name, { selector: "strong" })
      .closest("article")!;
    await userEvent.click(
      within(card).getByRole("button", { name: "重置订阅" }),
    );
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(users[0].name)).toBeTruthy();
    fireEvent.keyDown(dialog, { key: "Escape", code: "Escape", keyCode: 27 });
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() =>
      expect(document.activeElement).toBe(
        within(card).getByRole("button", { name: "重置订阅" }),
      ),
    );
    expect(requests.some((item) => item.path.endsWith("/reset"))).toBe(false);
    await userEvent.click(
      within(card).getByRole("button", { name: "重置订阅" }),
    );
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "取消" }),
    );
    expect(requests.some((item) => item.path.endsWith("/reset"))).toBe(false);
    expect(card.querySelector("img")).toBeNull();
  });
  it("keeps record anchors across pages and resets them when changing filters or size", async () => {
    mount("activity");
    const section = await screen.findByRole("region", { name: "代理流量记录" });
    await waitFor(() =>
      expect(within(section).getAllByText("example.org")).toHaveLength(25),
    );
    await userEvent.click(
      within(section).getByRole("button", { name: "下一页" }),
    );
    await waitFor(() =>
      expect(
        requests.some(
          (item) =>
            item.path.includes("page=2") && item.path.includes("before_id=800"),
        ),
      ).toBe(true),
    );
    await choose(section, "每页数量", "50 / 页");
    await waitFor(() =>
      expect(within(section).getAllByText("example.org")).toHaveLength(50),
    );
    const latest = requests
      .filter((item) => item.path.startsWith("/api/activity"))
      .at(-1)!;
    expect(latest.path).toContain("page=1");
    expect(latest.path).not.toContain("before_id");
    await userEvent.type(
      within(section).getByLabelText("域名 / IP"),
      "example.org",
    );
    await userEvent.click(
      within(section).getByRole("button", { name: "搜索" }),
    );
    await waitFor(() =>
      expect(requests.at(-1)!.path).toContain("search=example.org"),
    );
  });
  it("keeps 24h cards fixed, shows rate coverage, and ignores obsolete device responses", async () => {
    mount("egress-dashboard");
    const section = await screen.findByRole("region", { name: "出口流量大盘" });
    await within(section).findByText("300 B");
    expect(within(section).getAllByText("1 / 2 台上报")).toHaveLength(2);
    await userEvent.click(
      within(section)
        .getByRole("radio", { name: "最近 7 天" })
        .closest("label")!,
    );
    await waitFor(() =>
      expect(requests.some((item) => item.path.includes("hours=168"))).toBe(
        true,
      ),
    );
    await within(section).findByText("50 B");
    let resolve!: (data: EgressOverview) => void;
    overviewDelay = new Promise((value) => {
      resolve = value;
    });
    await choose(section, "筛选电脑", "测试电脑");
    await choose(section, "筛选电脑", "全部电脑");
    resolve({ ...egress, total_download: 999999 });
    await waitFor(() =>
      expect(within(section).getByText("300 B")).toBeTruthy(),
    );
    await userEvent.click(
      within(section).getByRole("button", { name: "查看详细记录 →" }),
    );
    await waitFor(() =>
      expect(
        requests.some(
          (item) =>
            item.path.includes("/api/egress/records?") &&
            item.path.includes("start=123") &&
            item.path.includes("kind=request"),
        ),
      ).toBe(true),
    );
  });
  it("confirms the delete target and hides the archived account while preserving its history", async () => {
    users = users.slice(0, 2);
    mount("users");
    const section = await screen.findByRole("region", { name: "vps用户" });
    const name = await within(section).findByText("测试用户 1", {
      selector: "strong",
    });
    const original = { ...users[0] };
    await userEvent.click(
      within(name.closest("article")!).getByRole("button", { name: "删除" }),
    );
    let dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("测试用户 1")).toBeTruthy();
    await userEvent.click(within(dialog).getByRole("button", { name: "取消" }));
    expect(requests.some((item) => item.options.method === "DELETE")).toBe(
      false,
    );
    await userEvent.click(
      within(name.closest("article")!).getByRole("button", { name: "删除" }),
    );
    dialog = screen.getByRole("dialog");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "删除用户" }),
    );
    await waitFor(() =>
      expect(
        within(section).queryByText("测试用户 1", { selector: "strong" }),
      ).toBeNull(),
    );
    expect(
      requests.find(
        (item) =>
          item.path === "/api/users/1" && item.options.method === "DELETE",
      ),
    ).toBeTruthy();
    expect(archivedUsers).toHaveLength(1);
    expect(archivedUsers[0]).toMatchObject({
      ...original,
      enabled: 0,
      deleted: true,
    });
    expect(within(section).queryByRole("button", { name: "已删除用户" })).toBeNull();
    await clickNav("代理流量大盘");
    const dashboard = screen.getByRole("region", { name: "代理流量大盘" });
    await within(dashboard).findByText("测试用户 2", { selector: "strong" });
    expect(within(dashboard).queryByText("测试用户 1", { selector: "strong" })).toBeNull();
    expect(requests.some((item) => item.path.includes("deleted=true"))).toBe(false);
  });
});
