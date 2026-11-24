import { Input, InputNumber } from "antd";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  Archive,
  ExternalLink,
  Link2,
  Plus,
  Settings2,
  Trash2,
  Undo2,
} from "lucide-react";
import { api, write } from "../api";
import {
  Badge,
  Button,
  CopyField,
  Empty,
  Feedback,
  Modal,
  Pagination,
  UserSelect,
  useCardPage,
  useTask,
  useUI,
  useRemote,
} from "../components";
import { bytes, date, limitsBody, number } from "../lib";
import type { Limits, User } from "../types";

export function UserIdentity({
  user,
  created = false,
}: {
  user: User;
  created?: boolean;
}) {
  return (
    <div className="user-identity">
      <span className="avatar">{user.name.slice(0, 1)}</span>
      <div>
        <strong>{user.name}</strong>
        {created && (
          <span className="user-time">创建：{date(user.created, true)}</span>
        )}
        <span className="user-time">
          上次使用：{user.last_used ? date(user.last_used, true) : "从未使用"}
        </span>
      </div>
    </div>
  );
}
export const UserStatus = ({ user }: { user: User }) => (
  <Badge tone={user.enabled ? "success" : "neutral"}>
    {user.deleted ? "已删除" : user.enabled ? "已启用" : "已禁用"}
  </Badge>
);
function LimitStatus({ limits }: { limits?: Limits }) {
  if (
    !limits ||
    (!limits.total_bytes && !limits.daily_bytes && !limits.speed_bps)
  )
    return null;
  return (
    <div className={`limit-status ${limits.blocked ? "warning" : ""}`}>
      {limits.blocked === "total"
        ? "总流量额度已用尽 · 代理已暂停"
        : limits.blocked === "daily"
          ? "今日额度已用尽 · 北京时间零点恢复"
          : [
              limits.total_bytes ? "总额度 " + bytes(limits.total_bytes) : "",
              limits.daily_bytes ? "每日 " + bytes(limits.daily_bytes) : "",
              limits.speed_bps
                ? "限速 " +
                  (limits.speed_bps / 1e6).toLocaleString("zh-CN", {
                    maximumFractionDigits: 6,
                  }) +
                  " Mbps"
                : "",
            ]
              .filter(Boolean)
              .join(" · ")}
    </div>
  );
}
export function SubscriptionModal({
  user,
  close,
}: {
  user: User;
  close: () => void;
}) {
  return (
    <Modal label={user.name + " · 订阅链接"} onClose={close}>
      <div className="modal-identity">
        <span className="avatar">
          <Link2 size={20} />
        </span>
        <strong>{user.name}</strong>
      </div>
      <CopyField label="Clash" value={user.clash_url} />
      <CopyField label="Shadowrocket" value={user.shadowrocket_url} />
    </Modal>
  );
}
export function UsersPage({
  active,
  users,
  loading,
  error,
  view,
}: {
  active: boolean;
  users: User[];
  loading: boolean;
  error: Error | null;
  view: (id: string) => void;
}) {
  const [filter, setFilter] = useState("");
  const [trash, setTrash] = useState(false);
  const archived = useRemote<User[]>(
    ["users", "deleted"],
    "/users?deleted=true",
    active && trash,
  );
  const list = useRef<HTMLDivElement>(null);
  const displayed = trash ? archived.data || [] : users;
  const listLoading = trash ? archived.isPending : loading;
  const listError = trash ? archived.error : error;
  const [edit, setEdit] = useState<User | "new" | null>(null);
  const [name, setName] = useState("");
  const [subscription, setSubscription] = useState<User | null>(null);
  const [limits, setLimits] = useState<User | null>(null);
  const paging = useCardPage(
    displayed.filter((user) => !filter || String(user.id) === filter),
    `${trash}-${filter}`,
  );
  const client = useQueryClient();
  const { run, pending } = useTask();
  const { confirm, notify } = useUI();
  const refresh = () => client.invalidateQueries({ queryKey: ["users"] });
  function save(event: FormEvent) {
    event.preventDefault();
    run("save", async () => {
      const result = await write<User>(
        edit === "new" ? "/users" : "/users/" + edit!.id,
        edit === "new" ? "POST" : "PATCH",
        { name },
      );
      const fresh = edit === "new";
      setEdit(null);
      await refresh();
      if (fresh) setSubscription(result);
      notify("用户已保存");
    });
  }
  async function reset(user: User) {
    if (!(await confirm(user.name, "重置订阅"))) return;
    run("reset-" + user.id, async () => {
      const result = await write<User>(`/users/${user.id}/reset`, "POST");
      await refresh();
      setSubscription(result);
      notify("订阅已重置");
    });
  }
  async function remove(user: User) {
    if (
      !(await confirm(
        user.name,
        "删除用户",
        "停用代理并移至已删除用户，保留历史流量",
      ))
    )
      return;
    run("delete-" + user.id, async () => {
      await write(`/users/${user.id}`, "DELETE");
      if (filter === String(user.id)) setFilter("");
      await refresh();
      list.current?.focus({ preventScroll: true });
      notify("用户已删除，可从已删除用户恢复");
    });
  }
  function restore(user: User) {
    run("restore-" + user.id, async () => {
      await write(`/users/${user.id}/restore`, "POST");
      if (filter === String(user.id)) setFilter("");
      await refresh();
      list.current?.focus({ preventScroll: true });
      notify("用户已恢复，当前为停用状态");
    });
  }
  return (
    <div className="page-stack" ref={list} tabIndex={-1}>
      <div className="toolbar">
        <UserSelect users={displayed} value={filter} onChange={setFilter} />
        <div className="toolbar-group">
          <Button
            className={trash ? "selected" : ""}
            aria-pressed={trash}
            onClick={() => {
              setTrash(!trash);
              setFilter("");
            }}
          >
            <Archive size={16} />
            {trash ? "返回用户列表" : "已删除用户"}
          </Button>
          {!trash && (
            <Button
              variant="primary"
              onClick={() => {
                setName("");
                setEdit("new");
              }}
            >
              <Plus size={17} />
              新增用户
            </Button>
          )}
        </div>
      </div>
      <Feedback loading={listLoading} error={listError} retry={refresh} />
      <div className="user-grid">
        {paging.items.map((user) => (
          <article key={user.id} className="user-card">
            <div className="user-card-head">
              <UserIdentity user={user} created />
              <UserStatus user={user} />
            </div>
            <div className="user-stats">
              <div>
                <span>累计流量</span>
                <b>{bytes(user.upload + user.download)}</b>
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
            <LimitStatus limits={user.limits} />
            {trash ? (
              <>
                <div className="user-time">
                  删除时间：{date(user.deleted_at, true)}
                </div>
                <div className="user-primary-actions">
                  <Button
                    variant="primary"
                    disabled={!!pending}
                    busy={pending === "restore-" + user.id}
                    onClick={() => restore(user)}
                  >
                    <Undo2 size={15} />
                    恢复用户
                  </Button>
                  <Button onClick={() => view(String(user.id))}>
                    <ExternalLink size={15} />
                    查看统计
                  </Button>
                </div>
              </>
            ) : (
              <>
                <div className="user-primary-actions">
                  <Button
                    variant="primary"
                    onClick={() => setSubscription(user)}
                  >
                    <Link2 size={15} />
                    订阅链接
                  </Button>
                  <Button onClick={() => view(String(user.id))}>
                    <ExternalLink size={15} />
                    查看统计
                  </Button>
                  <Button
                    aria-label={user.name + "的流量限制"}
                    onClick={() => setLimits(user)}
                  >
                    <Settings2 size={15} />
                    流量限制
                  </Button>
                </div>
                <div className="user-secondary-actions">
                  <Button
                    variant="text"
                    onClick={() => {
                      setName(user.name);
                      setEdit(user);
                    }}
                  >
                    重命名
                  </Button>
                  <Button
                    variant="text"
                    disabled={!!pending}
                    busy={pending === "toggle-" + user.id}
                    onClick={() =>
                      run("toggle-" + user.id, async () => {
                        await write(`/users/${user.id}`, "PATCH", {
                          enabled: !user.enabled,
                        });
                        await refresh();
                        notify(user.enabled ? "用户已禁用" : "用户已启用");
                      })
                    }
                  >
                    {user.enabled ? "禁用" : "启用"}
                  </Button>
                  <Button
                    variant="text"
                    disabled={!!pending}
                    busy={pending === "reset-" + user.id}
                    onClick={() => reset(user)}
                  >
                    重置订阅
                  </Button>
                  <Button
                    variant="text"
                    className="danger-text"
                    disabled={!!pending}
                    busy={pending === "delete-" + user.id}
                    onClick={() => remove(user)}
                  >
                    <Trash2 size={13} />
                    删除
                  </Button>
                </div>
              </>
            )}
          </article>
        ))}
      </div>
      {!listLoading && !listError && !paging.items.length && (
        <Empty>{trash ? "暂无已删除用户" : "暂无用户"}</Empty>
      )}
      <Pagination {...paging.props} />
      {edit && (
        <Modal
          label={edit === "new" ? "新增用户" : "重命名用户"}
          onClose={() => setEdit(null)}
          busy={!!pending}
        >
          <form onSubmit={save}>
            <label className="field">
              <span>用户名称</span>
              <Input
                autoFocus
                required
                maxLength={64}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            <div className="modal-actions">
              <Button disabled={!!pending} onClick={() => setEdit(null)}>
                取消
              </Button>
              <Button type="submit" variant="primary" busy={!!pending}>
                保存用户
              </Button>
            </div>
          </form>
        </Modal>
      )}
      {subscription && (
        <SubscriptionModal
          user={subscription}
          close={() => setSubscription(null)}
        />
      )}
      {limits && <LimitsModal user={limits} close={() => setLimits(null)} />}
    </div>
  );
}
function LimitsModal({ user, close }: { user: User; close: () => void }) {
  const [data, setData] = useState<Limits | null>(null);
  const [error, setError] = useState<Error | null>(null);
  const [values, setValues] = useState({
    total_bytes: "",
    daily_bytes: "",
    speed_bps: "",
  });
  const { run, pending } = useTask();
  const { notify } = useUI();
  const client = useQueryClient();
  useEffect(() => {
    const controller = new AbortController();
    api<Limits>(`/users/${user.id}/limits`, {
      signal: AbortSignal.any([controller.signal, AbortSignal.timeout(10000)]),
    })
      .then((value) => {
        if (controller.signal.aborted) return;
        setData(value);
        setValues({
          total_bytes: value.total_bytes
            ? String(Number((value.total_bytes / 1024 ** 3).toPrecision(15)))
            : "",
          daily_bytes: value.daily_bytes
            ? String(Number((value.daily_bytes / 1024 ** 3).toPrecision(15)))
            : "",
          speed_bps: value.speed_bps ? String(value.speed_bps / 1e6) : "",
        });
      })
      .catch((error) => {
        if (!controller.signal.aborted) setError(error);
      });
    return () => controller.abort();
  }, [user.id]);
  function submit(event: FormEvent) {
    event.preventDefault();
    run("limits", async () => {
      const result = await write<Limits>(
        `/users/${user.id}/limits`,
        "PATCH",
        limitsBody(values),
      );
      await client.invalidateQueries({ queryKey: ["users"] });
      close();
      notify(
        result.ready
          ? result.blocked
            ? "限制已保存，该账号额度已用尽"
            : "流量限制已保存"
          : "设置已保存，等待限流服务恢复",
      );
    });
  }
  return (
    <Modal label={user.name + " · 流量限制"} onClose={close} busy={!!pending}>
      <div className="modal-identity">
        <span className="avatar">
          <Settings2 size={20} />
        </span>
        <strong>{user.name}</strong>
      </div>
      <Feedback loading={!data && !error} error={error} />
      {data && (
        <form onSubmit={submit}>
          <div className="limits-usage">
            <div>
              <span>累计已使用</span>
              <b>{bytes(data.total_used)}</b>
            </div>
            <div>
              <span>今日已使用</span>
              <b>{bytes(data.daily_used)}</b>
            </div>
          </div>
          {(["total_bytes", "daily_bytes", "speed_bps"] as const).map((key) => (
            <label className="field" key={key}>
              <span>
                {
                  {
                    total_bytes: "总流量上限（GB）",
                    daily_bytes: "单日流量上限（GB）",
                    speed_bps: "网速上限（Mbps）",
                  }[key]
                }
              </span>
              <InputNumber<string>
                stringMode
                min="0"
                step="0.1"
                max={key === "speed_bps" ? "1000000" : "8388607"}
                inputMode="decimal"
                placeholder="不限"
                value={values[key]}
                onChange={(value) =>
                  setValues((previous) => ({ ...previous, [key]: value ?? "" }))
                }
              />
            </label>
          ))}
          {(!data.ready || data.blocked) && (
            <div className="feedback warning" role="status">
              {!data.ready
                ? "限流服务未就绪"
                : data.blocked === "total"
                  ? "总额度已用尽"
                  : "今日额度已用尽"}
            </div>
          )}
          <div className="modal-actions">
            <Button
              variant="text"
              disabled={!!pending}
              onClick={() =>
                setValues({ total_bytes: "", daily_bytes: "", speed_bps: "" })
              }
            >
              清除全部上限
            </Button>
            <Button disabled={!!pending} onClick={close}>
              取消
            </Button>
            <Button variant="primary" type="submit" busy={!!pending}>
              保存限制
            </Button>
          </div>
        </form>
      )}
    </Modal>
  );
}
