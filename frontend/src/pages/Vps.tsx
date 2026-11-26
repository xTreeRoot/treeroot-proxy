import { Input, Space } from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { write } from "../api";
import { Button, Feedback, useRemote, useTask, useUI } from "../components";
import type { Server } from "../types";

type Template = { source: string; revision: string };
type Validation = { rules: number; fallback: string };
const endpoint = "/vps/subscription";

export function VpsPage({ active }: { active: boolean }) {
  const config = useRemote<Template>(["vps-subscription"], endpoint, active);
  const server = useRemote<Server>(["server"], "/server", active);
  const cache = useQueryClient();
  const { pending, run } = useTask();
  const ui = useUI();
  const [draft, setDraft] = useState<Template | null>(null);
  const [validation, setValidation] = useState<Validation | null>(null);
  const editor = draft ?? config.data;
  const fields = [
    ["订阅域名", server.data?.domain],
    ["代理协议", server.data?.protocol],
    ...[["xray.service", "Xray"], ["caddy.service", "Caddy"]].map(([unit, label]) => {
      const service = server.data?.services.find((s) => s.name === unit);
      return [label, service?.state === "active" ? "运行中" : service?.state];
    }),
  ];
  return (
    <div className="page-stack">
      <Feedback loading={server.isPending} error={server.error} retry={() => server.refetch()} />
      <article className="panel" aria-label="VPS 服务">
        <dl className="server-details">
          {fields.map(([label, value]) => <div key={label} className="server-detail"><dt>{label}</dt><dd>{value ?? "—"}</dd></div>)}
        </dl>
      </article>
      <Feedback loading={config.isPending} error={config.error} retry={() => config.refetch()} />
      {editor && <article className="panel vps-editor">
        <label className="field">
          <span>Clash 订阅模板 YAML</span>
          <Input.TextArea className="home-rule-source" rows={18} spellCheck={false}
            autoComplete="off" value={editor.source} disabled={!!pending}
            onChange={(e) => {
              setDraft({ ...editor, source: e.target.value });
              setValidation(null);
            }} />
        </label>
        <Space wrap>
          <Button disabled={!!pending} onClick={() => run("validate", async () => {
            setValidation(null);
            setValidation(await write<Validation>(endpoint + "/preview", "POST", editor));
          })}>校验规则</Button>
          <Button variant="primary" busy={pending === "save"} disabled={!draft || !!pending}
            onClick={() => run("save", async () => {
              if (!await ui.confirm("所有 Clash 用户的订阅", "发布配置", "用户下次刷新订阅时生效")) return;
              const saved = await write<Template>(endpoint, "PUT", editor);
              await cache.cancelQueries({queryKey: ["vps-subscription"]});
              cache.setQueryData(["vps-subscription"], saved);
              setDraft(null);
              setValidation(null);
              ui.notify("配置已发布");
            })}>发布配置</Button>
          <Button disabled={!!pending} onClick={() => run("reload", async () => {
            if (draft && !await ui.confirm("未发布的订阅模板", "放弃修改并重新载入")) return;
            await config.refetch({ throwOnError: true });
            setDraft(null);
            setValidation(null);
          })}>重新载入</Button>
        </Space>
        {validation && <span role="status">校验通过 · {validation.rules} 条规则 · {validation.fallback}</span>}
      </article>}
    </div>
  );
}
