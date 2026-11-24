import { afterEach, vi } from "vitest";
import { cleanup } from "@testing-library/react";

afterEach(() => {
  cleanup();
  localStorage.clear();
  location.hash = "";
});
Object.defineProperty(window, "matchMedia", {
  configurable: true,
  value: (query: string) => ({
    matches: query === "(prefers-reduced-motion: reduce)",
    addEventListener() {},
    removeEventListener() {},
  }),
});
Object.defineProperty(globalThis, "ResizeObserver", {
  value: class {
    observe() {}
    disconnect() {}
    unobserve() {}
  },
});
Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
  value: function () {
    this.setAttribute("open", "");
    this.querySelector("[autofocus]")?.focus();
  },
});
Object.defineProperty(HTMLDialogElement.prototype, "close", {
  value: function () {
    this.removeAttribute("open");
  },
});
Object.defineProperty(HTMLElement.prototype, "scrollTo", { value: vi.fn() });
window.echarts = {
  init: () => ({ setOption: vi.fn(), resize: vi.fn(), dispose: vi.fn() }),
};
