let csrf = "";
export const setCSRF = (value: string) => {
  csrf = value;
};

export async function request(path: string, options: RequestInit = {}) {
  const response = await fetch("/api" + path, {
    ...options,
    credentials: "same-origin",
    signal:
      options.signal ?? AbortSignal.timeout(options.method ? 60000 : 10000),
    headers: {
      "Content-Type": "application/json",
      "X-CSRF-Token": csrf,
      ...options.headers,
    },
  });
  if (response.status === 401 && path !== "/login")
    window.dispatchEvent(new Event("treeroot:unauthorized"));
  return response;
}

export async function api<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const response = await request(path, options);
  const data = await response.json().catch(() => {
    throw new Error("服务器暂时无法响应");
  });
  if (!response.ok) {
    const detail = Array.isArray(data.detail)
      ? data.detail.map((item: { msg: string }) => item.msg).join("；")
      : data.detail;
    throw new Error(detail || "请求失败，请稍后重试");
  }
  return data as T;
}

export const write = <T>(path: string, method: string, body = {}) =>
  api<T>(path, { method, body: JSON.stringify(body) });

export async function downloadConnector(id: string) {
  const response = await request(`/egress/${encodeURIComponent(id)}/setup`, {
    method: "POST",
    body: "{}",
  });
  if (!response.ok)
    throw new Error((await response.json()).detail || "下载失败");
  const url = URL.createObjectURL(await response.blob());
  const link = document.createElement("a");
  link.href = url;
  link.download = "TreeRoot-Connector.zip";
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
