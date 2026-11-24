import { useState, type FormEvent } from "react";
import { LockKeyhole } from "lucide-react";
import { write } from "../api";
import { Button, useTask, useUI } from "../components";

export function SettingsPage() {
  const [current, setCurrent] = useState(""),
    [password, setPassword] = useState("");
  const { pending, run } = useTask(),
    { notify } = useUI();
  function submit(event: FormEvent) {
    event.preventDefault();
    run("password", async () => {
      await write("/password", "POST", { current, password });
      setCurrent("");
      setPassword("");
      notify("管理员密码已更新");
    });
  }
  return (
    <article className="panel password-panel">
      <div className="password-symbol" aria-hidden="true">
        <LockKeyhole size={24} />
      </div>
      <form onSubmit={submit}>
        <label className="field">
          <span>原密码</span>
          <input
            type="password"
            required
            autoComplete="current-password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
          />
        </label>
        <label className="field">
          <span>新密码</span>
          <input
            type="password"
            required
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        <Button type="submit" variant="primary" busy={!!pending}>
          更新密码
        </Button>
      </form>
    </article>
  );
}
