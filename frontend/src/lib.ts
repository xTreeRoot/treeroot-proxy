import type { Trend } from "./types";

export function bytes(value: number | null | undefined) {
  if (value == null || !Number.isFinite(value)) return "—";
  if (value < 1024) return Math.round(value) + " B";
  const unit = Math.min(Math.floor(Math.log(value) / Math.log(1024)), 4);
  return (
    (value / 1024 ** unit).toFixed(unit > 1 ? 2 : 1) +
    " " +
    ["B", "KB", "MB", "GB", "TB"][unit]
  );
}
export const speed = (value: number | null | undefined) =>
  value == null ? "—" : bytes(value) + "/s";
export const number = (value?: number) =>
  value == null ? "—" : value.toLocaleString("zh-CN");
export const date = (value: number | null | undefined, full = false) =>
  value
    ? new Date(value * 1000).toLocaleString("zh-CN", {
        ...(full ? { year: "numeric" } : {}),
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        hour12: false,
      })
    : "—";
export const successRate = (connections: number, succeeded: number) =>
  connections ? ((succeeded / connections) * 100).toFixed(1) + "%" : "—";
export const period = (value: string) => (value === "168" ? "168" : "24");
export function proxyQuery(user: string, trend: Trend) {
  const params = new URLSearchParams({
    time_zone:
      Intl.DateTimeFormat().resolvedOptions().timeZone || "Asia/Shanghai",
  });
  if (user) params.set("user_id", user);
  for (const [key, value] of Object.entries(trend))
    if (value) params.set(key, value);
  return params.toString();
}
export function limitsBody(values: Record<string, string>) {
  return Object.fromEntries(
    Object.entries(values).map(([key, raw]) => {
      const value = Number(raw || 0),
        unit = key === "speed_bps" ? 1e6 : 1024 ** 3,
        amount = Math.round(value * unit);
      if (
        !Number.isFinite(value) ||
        value < 0 ||
        !Number.isSafeInteger(amount) ||
        (value > 0 && amount < 1) ||
        (key === "speed_bps" && value > 1000000)
      )
        throw new Error("请输入有效的非负上限，留空或 0 表示不限");
      return [key, amount];
    }),
  );
}
export function trendDate(
  value: number,
  granularity: string,
  full = false,
  daySelected = false,
) {
  const stamp = new Date(value * 1000);
  if (granularity === "hour" && !full && daySelected)
    return stamp.toLocaleTimeString("zh-CN", {
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    });
  const label = stamp.toLocaleString("zh-CN", {
    month: "2-digit",
    ...(granularity !== "month" ? { day: "2-digit" } : {}),
    ...(granularity === "hour"
      ? { hour: "2-digit", minute: "2-digit", hour12: false }
      : {}),
    ...(full ? { year: "numeric" } : {}),
  });
  return !full && granularity === "hour" ? label.replace(" ", "\n") : label;
}
