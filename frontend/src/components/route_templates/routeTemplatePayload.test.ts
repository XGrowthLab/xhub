import { afterEach, describe, expect, it, vi } from "vitest";
import { createRouteTemplateCall, updateRouteTemplateCall } from "@/components/networking";
import { setActiveLocale } from "@/i18n";
import { deleteRefusalText, saveRouteTemplate, summarizeTemplate, toTemplateRows } from "./routeTemplatePayload";

vi.mock("@/components/networking", () => ({
  createRouteTemplateCall: vi.fn(),
  updateRouteTemplateCall: vi.fn(),
  getRouteTemplatesCall: vi.fn(),
  getRouteTemplateUsageCall: vi.fn(),
  deleteRouteTemplateCall: vi.fn(),
}));
afterEach(() => {
  setActiveLocale("en");
  vi.clearAllMocks();
});

/**
 * The route template library's view logic.
 *
 * These are the parts worth asserting without rendering: what a template's row
 * says, and what a delete refusal means. Both turn a server answer into something
 * an operator reads, so a mistake here is a wrong statement rather than a broken
 * layout.
 */

const labels = {
  models: (value: number) => value + " models",
  retries: (value: number) => `${value} retries`,
  timeout: (seconds: number) => `${seconds}s`,
  fallbacks: (value: number) => `${value} fallbacks`,
  groups: (value: number) => `${value} groups`,
  none: "defaults",
};

describe("saveRouteTemplate", () => {
  /** 用隔离的创建与更新响应验证保存返回可绑定 ID，mock 由测试清理。 */
  it("returns the saved identity", async () => {
    vi.mocked(createRouteTemplateCall).mockResolvedValueOnce({ id: "new" });
    await expect(saveRouteTemplate("token", { name: "Example", body: {} })).resolves.toBe("new");
    await expect(saveRouteTemplate("token", { id: "old", name: "Example", body: {} })).resolves.toBe("old");
    expect(updateRouteTemplateCall).toHaveBeenCalledWith("token", "old", { name: "Example", body: {} });
  });
  /** 前置中文语言和空白名称；验证本地校验返回准确中文且不调用后台；恢复语言并清空 mock，无测试数据。 */
  it("localizes the required-name failure before creating", async () => {
    setActiveLocale("zh-CN");
    await expect(saveRouteTemplate("token", { name: "   ", body: {} })).rejects.toThrow("名称为必填项");
    expect(createRouteTemplateCall).not.toHaveBeenCalled();
  });
});

describe("toTemplateRows", () => {
  it("carries the usage count onto the row", () => {
    const rows = toTemplateRows([
      { id: "a", name: "A", body: {}, used_by: 3 },
      { id: "b", name: "B", body: {}, used_by: 0 },
    ]);
    expect(rows.map((row) => row.usedBy)).toEqual([3, 0]);
  });

  it("treats a missing count as none rather than as unknown", () => {
    // The list endpoint always sends it, but a row without it must not render as
    // "unknown" — the delete button reads this number to decide.
    const rows = toTemplateRows([{ id: "a", name: "A", body: {} }]);
    expect(rows[0].usedBy).toBe(0);
  });
});

describe("summarizeTemplate", () => {
  /** 验证模型规则数量和失败策略摘要，输入为新文档，无外部清理。 */
  it("describes model rules and retry policy", () => {
    expect(summarizeTemplate({}, labels)).toBe("defaults · 1 retries · 60s");
    expect(
      summarizeTemplate(
        {
          model_routes: [
            { model: "a", strategy: "random" },
            { model: "b", strategy: "least-busy" },
          ],
          retry_policy: { max_attempts: 5, timeout_seconds: 90 },
        },
        labels,
      ),
    ).toBe("2 models · 0 groups · 0 fallbacks · 5 retries · 90s");
    expect(
      summarizeTemplate({ model_routes: [], retry_policy: { max_attempts: 1, timeout_seconds: 60 } }, labels),
    ).toContain("defaults");
  });

  /** 前置仅含路由组、显式禁用项及旧数据；验证所有有效规则计数，无外部状态。 */
  it("includes fallback-only and group-only configuration", () => {
    const body = {
      routing_groups: [{ group_name: "g" }],
      fallbacks: [{ g: [] }],
      context_window_fallbacks: [{ g: ["backup"] }],
      content_policy_fallbacks: null,
    };
    expect(summarizeTemplate(body, labels)).toBe("0 models · 1 groups · 2 fallbacks · 1 retries · 60s");
  });
});

describe("deleteRefusalText", () => {
  it("names the scopes when the server refused for that reason", () => {
    const error = {
      body: {
        error: {
          code: "template_in_use",
          used_by: [
            { scope_type: "team", scope_id: "t1", name: "Core" },
            { scope_type: "organization", scope_id: "o1", name: "Acme" },
          ],
        },
      },
    };
    const got = deleteRefusalText(error, { inUse: (n) => `${n} still use it`, failed: "failed" });
    expect(got.message).toBe("2 still use it");
    expect(got.usage.map((u) => u.name)).toEqual(["Core", "Acme"]);
  });

  it("does not invent a cause when the failure was something else", () => {
    // A 500 or a network error must not be reported as "still in use": that
    // would send the operator looking for scopes that are not there.
    const got = deleteRefusalText(new Error("boom"), { inUse: (n) => `${n} in use`, failed: "failed" });
    expect(got.message).toBe("failed");
    expect(got.usage).toEqual([]);
  });

  it("treats a refusal with no list as a refusal with no names", () => {
    const got = deleteRefusalText(
      { body: { error: { code: "template_in_use" } } },
      { inUse: (n) => `${n} in use`, failed: "failed" },
    );
    expect(got.message).toBe("0 in use");
    expect(got.usage).toEqual([]);
  });

  it("survives a body that is not the shape it expects", () => {
    expect(deleteRefusalText({ body: "not json" }, { inUse: () => "in use", failed: "failed" }).message).toBe("failed");
    expect(deleteRefusalText(undefined, { inUse: () => "in use", failed: "failed" }).message).toBe("failed");
  });
});
