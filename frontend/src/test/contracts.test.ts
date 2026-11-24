import { describe, expect, it } from "vitest";
import { limitsBody, period, proxyQuery, trendDate } from "../lib";
import { recordQuery } from "../pages/Records";

describe("existing API query contracts", () => {
  it("sends only valid egress periods and omits unknown record anchors", () => {
    for (const value of ["", "undefined", "NaN", "最近 7 天", "24"])
      expect(period(value)).toBe("24");
    expect(period("168")).toBe("168");
    const filters = {
      user_id: "",
      device_id: "test-device",
      kind: "request",
      outcome: "",
      search: "",
      hours: "24",
    };
    const first = new URLSearchParams(recordQuery(filters, 1, 25, null, true));
    expect(first.has("start")).toBe(false);
    expect(first.has("before_id")).toBe(false);
    expect(first.get("device_id")).toBe("test-device");
    const next = new URLSearchParams(
      recordQuery({ ...filters, start: 123 }, 2, 50, 900, true),
    );
    expect(next.get("start")).toBe("123");
    expect(next.get("before_id")).toBe("900");
    expect(next.get("page_size")).toBe("50");
  });
  it("keeps user and timezone in calendar requests and formats the complete tooltip date", () => {
    const params = new URLSearchParams(
      proxyQuery("7", { year: "2026", month: "11", day: "1" }),
    );
    expect(params.get("user_id")).toBe("7");
    expect(params.get("year")).toBe("2026");
    expect(params.get("month")).toBe("11");
    expect(params.get("day")).toBe("1");
    expect(params.get("time_zone")).toBeTruthy();
    expect(trendDate(1793462400, "hour", true)).toContain("2026");
    expect(trendDate(1793462400, "hour", false, true)).toMatch(/\d{2}:\d{2}/);
  });
  it("preserves binary GB and decimal Mbps limits, clears to zero, and rejects invalid amounts", () => {
    expect(
      limitsBody({ total_bytes: "2.5", daily_bytes: "1", speed_bps: ".8" }),
    ).toEqual({
      total_bytes: 2684354560,
      daily_bytes: 1073741824,
      speed_bps: 800000,
    });
    expect(
      limitsBody({ total_bytes: "", daily_bytes: "0", speed_bps: "" }),
    ).toEqual({ total_bytes: 0, daily_bytes: 0, speed_bps: 0 });
    for (const raw of [
      "-1",
      "Infinity",
      "NaN",
      ".00000000001",
      "9007199254740991",
    ])
      expect(() => limitsBody({ total_bytes: raw })).toThrow();
  });
});
