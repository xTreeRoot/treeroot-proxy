import { Input } from "antd";
import { useEffect, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  Archive,
  Download,
  Globe,
  KeyRound,
  Monitor,
  Plus,
  ShieldBan,
  Trash2,
  Undo2,
} from "lucide-react";
import { api, downloadConnector, write } from "../api";
import {
  Badge,
  Button,
  CopyField,
  DataTable,
  Empty,
  Feedback,
  Modal,
  useRemote,
  useTask,
  useUI,
} from "../components";
import { date } from "../lib";
import type { BlacklistRule, Device } from "../types";

async function browserInfo() {
  const info: Record<string, unknown> = {
    userAgent: navigator.userAgent,
    platform: navigator.platform,
    language: navigator.language,
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    width: screen.width,
    height: screen.height,
  };
  const extended = navigator as Navigator & {
    userAgentData?: {
      getHighEntropyValues: (
        hints: string[],
      ) => Promise<Record<string, string>>;
    };
  };
  if (extended.userAgentData) {
    try {
      const hints = await extended.userAgentData.getHighEntropyValues([
        "platformVersion",
        "architecture",
        "bitness",
      ]);
      Object.assign(info, {
        platformName: hints.platform,
        platformVersion: hints.platformVersion,
        architecture: hints.architecture,
        bitness: hints.bitness,
      });
    } catch {
      /* Basic browser information remains available. */
    }
  }
  return info;
}
export function EgressPage({ active }: { active: boolean }) {
  const [trash, setTrash] = useState(false),
    [add, setAdd] = useState(false),
    [blacklist, setBlacklist] = useState(false);
  const [name, setName] = useState(""),
    [code, setCode] = useState<{ name: string; code: string } | null>(null);
  const query = useRemote<Device[]>(
    ["devices", trash ? "deleted" : "active"],
    "/egress" + (trash ? "?deleted=true" : ""),
    active,
  );
  const version = useRemote<{ version: string }>(
    ["driver-version"],
    "/egress/version",
    active,
    60000,
  );
  const client = useQueryClient();
  const { pending, run } = useTask();
  const { notify, confirm } = useUI();
  const refresh = () =>
    client.invalidateQueries({
      predicate: (query) =>
        ["devices", "egress-overview"].includes(String(query.queryKey[0])),
    });
  async function action(device: Device, kind: string) {
    if (kind === "delete" && !(await confirm(device.name, "删除电脑"))) return;
    run(kind + device.id, async () => {
      const path = "/egress/" + encodeURIComponent(device.id);
      if (kind === "delete") {
        await write(path, "DELETE");
        notify("电脑已删除");
      }
      if (kind === "restore") {
        await write(path + "/restore", "POST");
        notify("电脑已恢复，当前已停用");
      }
      if (kind === "toggle") {
        await write(path, "PATCH", { enabled: !device.enabled });
        notify(device.enabled ? "电脑出口已停用" : "电脑出口已启用");
      }
      if (kind === "test") {
        const data = await write<{ ip: string }>(path + "/test", "POST");
        notify("电脑出口 IP：" + data.ip);
      }
      if (kind === "code") {
        const result = await api<{ code: string }>(path + "/access-code");
        setCode({ name: device.name, code: result.code });
      }
      if (kind === "setup") {
        await downloadConnector(device.id);
        notify("连接包已下载");
      }
      await refresh();
    });
  }
  function submit(event: FormEvent) {
    event.preventDefault();
    run("add", async () => {
      await write("/egress", "POST", { name, browser: await browserInfo() });
      setAdd(false);
      await refresh();
      notify("电脑已添加");
    });
  }
  return (
    <div className="page-stack">
      <div className="toolbar">
        <span className="driver-version">
          <Monitor size={17} aria-hidden="true" />
          当前驱动版本{" "}
          <b>
            {version.data?.version || (version.error ? "暂不可用" : "加载中")}
          </b>
        </span>
        <div className="toolbar-group">
          <Button onClick={() => setBlacklist(true)}>
            <ShieldBan size={16} />
            黑名单管理
          </Button>
          <Button
            className={trash ? "selected" : ""}
            aria-pressed={trash}
            onClick={() => setTrash(!trash)}
          >
            <Archive size={16} />
            {trash ? "返回电脑出口" : "已删除设备"}
          </Button>
          {!trash && (
            <Button
              variant="primary"
              onClick={() => {
                setName(
                  navigator.platform.includes("Mac") ? "我的 Mac" : "我的电脑",
                );
                setAdd(true);
              }}
            >
              <Plus size={17} />
              添加这台电脑
            </Button>
          )}
        </div>
      </div>
      <Feedback
        loading={query.isPending}
        error={query.error}
        retry={() => query.refetch()}
      />
      <div className="device-grid">
        {query.data?.map((device) => (
          <article className="device-card" key={device.id}>
            <div className="device-head">
              <span className="device-icon">
                <Monitor size={25} strokeWidth={1.6} />
              </span>
              <div className="device-name">
                <strong>{device.name}</strong>
                <span>
                  {device.browser.platformName ||
                    device.browser.platform ||
                    "待识别"}{" "}
                  {device.browser.platformVersion}
                </span>
              </div>
              <Badge
                tone={
                  trash
                    ? "neutral"
                    : device.online
                      ? "success"
                      : !device.enabled
                        ? "neutral"
                        : "warning"
                }
              >
                {trash
                  ? "已删除"
                  : device.online
                    ? "已连接"
                    : !device.enabled
                      ? "已停用"
                      : device.paired
                        ? "等待连接"
                        : "待安装"}
              </Badge>
            </div>
            <dl className="device-details">
              <div>
                <dt>出口 IP</dt>
                <dd>{device.last_ip || "待检测"}</dd>
              </div>
              <div>
                <dt>系统驱动</dt>
                <dd>{device.driver_version || "未上报"}</dd>
              </div>
              <div>
                <dt>最近在线</dt>
                <dd>{date(device.last_seen, true)}</dd>
              </div>
            </dl>
            <div className="device-actions">
              {trash ? (
                <Button
                  disabled={!!pending}
                  busy={pending === "restore" + device.id}
                  onClick={() => action(device, "restore")}
                >
                  <Undo2 size={15} />
                  恢复设备
                </Button>
              ) : (
                <>
                  <Button
                    disabled={!device.enabled || !!pending}
                    busy={pending === "setup" + device.id}
                    variant="primary"
                    onClick={() => action(device, "setup")}
                  >
                    <Download size={15} />
                    {device.paired ? "下载连接器" : "下载配对包"}
                  </Button>
                  <Button
                    disabled={!device.online || !!pending}
                    busy={pending === "test" + device.id}
                    onClick={() => action(device, "test")}
                  >
                    <Globe size={15} />
                    检测出口 IP
                  </Button>
                  <Button
                    disabled={!!pending}
                    onClick={() => action(device, "code")}
                  >
                    <KeyRound size={15} />
                    应用连接码
                  </Button>
                </>
              )}
            </div>
            {!trash && (
              <div className="device-footer">
                <Button
                  variant="text"
                  disabled={!!pending}
                  busy={pending === "toggle" + device.id}
                  onClick={() => action(device, "toggle")}
                >
                  {device.enabled ? "停用" : "启用并自动连接"}
                </Button>
                <Button
                  variant="text"
                  className="danger-text"
                  disabled={!!pending}
                  busy={pending === "delete" + device.id}
                  onClick={() => action(device, "delete")}
                >
                  <Trash2 size={14} />
                  删除
                </Button>
              </div>
            )}
          </article>
        ))}
      </div>
      {!query.isPending && !query.error && !query.data?.length && (
        <article className="panel">
          <Empty>{trash ? "暂无已删除设备" : "暂无电脑"}</Empty>
        </article>
      )}
      {add && (
        <Modal
          label="添加这台电脑"
          onClose={() => setAdd(false)}
          busy={!!pending}
        >
          <form onSubmit={submit}>
            <label className="field">
              <span>电脑名称</span>
              <Input
                autoFocus
                required
                maxLength={64}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            <div className="modal-actions">
              <Button disabled={!!pending} onClick={() => setAdd(false)}>
                取消
              </Button>
              <Button variant="primary" type="submit" busy={!!pending}>
                添加电脑
              </Button>
            </div>
          </form>
        </Modal>
      )}
      {code && (
        <Modal
          label={code.name + " · 应用连接码"}
          onClose={() => setCode(null)}
        >
          <div className="modal-identity">
            <span className="avatar">
              <KeyRound size={20} />
            </span>
            <strong>{code.name}</strong>
          </div>
          <CopyField label="应用连接码" value={code.code} />
        </Modal>
      )}
      {blacklist && <BlacklistModal close={() => setBlacklist(false)} />}
    </div>
  );
}
function BlacklistModal({ close }: { close: () => void }) {
  const query = useRemote<BlacklistRule[]>(["blacklist"], "/egress/blacklist");
  const [host, setHost] = useState(""),
    [edit, setEdit] = useState<number | null>(null);
  const { run, pending } = useTask(),
    { notify, confirm } = useUI();
  const client = useQueryClient();
  const reset = () => {
    setHost("");
    setEdit(null);
  };
  const refresh = () => client.invalidateQueries({ queryKey: ["blacklist"] });
  useEffect(() => {
    if (edit != null) document.getElementById("blacklist-host")?.focus();
  }, [edit]);
  function submit(event: FormEvent) {
    event.preventDefault();
    run("save", async () => {
      await write(
        "/egress/blacklist" + (edit == null ? "" : "/" + edit),
        edit == null ? "POST" : "PATCH",
        { host: host.trim() },
      );
      reset();
      await refresh();
      notify("黑名单规则已保存");
    });
  }
  async function remove(rule: BlacklistRule) {
    if (!(await confirm(rule.host, "删除黑名单规则"))) return;
    run("delete", async () => {
      await write("/egress/blacklist/" + rule.id, "DELETE");
      if (edit === rule.id) reset();
      await refresh();
      notify("黑名单规则已删除");
    });
  }
  return (
    <Modal label="出口黑名单" onClose={close} busy={!!pending} wide>
      <form className="blacklist-form" onSubmit={submit}>
        <label className="field">
          <span>精确主机</span>
          <Input
            id="blacklist-host"
            autoFocus
            required
            maxLength={253}
            value={host}
            onChange={(e) => setHost(e.target.value)}
          />
        </label>
        <Button
          type="submit"
          variant="primary"
          busy={pending === "save"}
          disabled={!!pending}
        >
          {edit == null ? "添加规则" : "保存修改"}
        </Button>
        {edit != null && (
          <Button disabled={!!pending} onClick={reset}>
            取消编辑
          </Button>
        )}
      </form>
      <Feedback
        loading={query.isPending}
        error={query.error}
        retry={() => query.refetch()}
      />
      <div className="rule-count">
        {query.data?.filter((rule) => rule.enabled).length || 0} 条启用 ·{" "}
        {query.data?.length || 0} 条规则
      </div>
      <div className="blacklist-table">
        <DataTable<BlacklistRule>
          rowKey="id"
          data={query.data || []}
          loading={query.isPending}
          height="45dvh"
          empty="暂无黑名单规则"
          columns={[
            {
              title: "精确主机",
              dataIndex: "host",
              key: "host",
              className: "table-identity",
            },
            {
              title: "状态",
              key: "state",
              render: (_, rule) => (
                <Badge tone={rule.enabled ? "success" : "neutral"}>
                  {rule.enabled ? "已启用" : "已停用"}
                </Badge>
              ),
            },
            {
              title: "更新时间",
              key: "updated",
              render: (_, rule) => date(rule.updated),
            },
            {
              title: "操作",
              key: "actions",
              render: (_, rule) => (
                <div className="table-actions">
                  <Button
                    variant="text"
                    disabled={!!pending}
                    onClick={() => {
                      setEdit(rule.id);
                      setHost(rule.host);
                    }}
                  >
                    编辑
                  </Button>
                  <Button
                    variant="text"
                    disabled={!!pending}
                    onClick={() =>
                      run("toggle", async () => {
                        await write("/egress/blacklist/" + rule.id, "PATCH", {
                          enabled: !rule.enabled,
                        });
                        await refresh();
                      })
                    }
                  >
                    {rule.enabled ? "停用" : "启用"}
                  </Button>
                  <Button
                    variant="text"
                    className="danger-text"
                    disabled={!!pending}
                    onClick={() => remove(rule)}
                  >
                    删除
                  </Button>
                </div>
              ),
            },
          ]}
        />
      </div>
    </Modal>
  );
}
