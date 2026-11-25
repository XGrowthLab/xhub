import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@/i18n";
import RouteTemplateJsonGuide, { ROUTE_TEMPLATE_EXAMPLE } from "./RouteTemplateJsonGuide";
import { parseDocument } from "./templateForm";

const renderGuide = (locale: "en" | "zh-CN" = "zh-CN") =>
  render(<I18nProvider initialLocale={locale}><RouteTemplateJsonGuide /></I18nProvider>);

describe("RouteTemplateJsonGuide", () => {
  /** 目的：验证严格顶层字段在中英文界面一致；前置静态指南；结果不把废弃字段当成可导入字段，DOM 自动清理。 */
  it.each([
    ["zh-CN", "JSON 指南分区", "字段"],
    ["en", "JSON guide sections", "Fields"],
  ] as const)("renders the strict contract in %s", (locale, tabList, tab) => {
    renderGuide(locale);
    expect(within(screen.getByRole("tablist", { name: tabList })).getByRole("tab", { name: tab, selected: true })).toBeVisible();
    const fields = screen.getAllByRole("term").map((term) => term.textContent);
    expect(fields).toEqual(expect.arrayContaining(["model_routes", "retry_policy", "routing_groups", "fallbacks"]));
    expect(fields).not.toContain("num_retries");
  });

  /** 目的：验证公开示例可被前端严格解析且旧字段被拒绝；前置共享 JSON；纯函数无外部数据与清理。 */
  it("parses the shared example and rejects legacy fields", () => {
    expect(parseDocument(JSON.stringify(ROUTE_TEMPLATE_EXAMPLE))).toMatchObject({ ok: true });
    expect(parseDocument(JSON.stringify({ ...ROUTE_TEMPLATE_EXAMPLE, num_retries: 1 }))).toEqual({ ok: false });
    expect(parseDocument(JSON.stringify({ ...ROUTE_TEMPLATE_EXAMPLE, stream_timeout: 120 }))).toEqual({ ok: false });
  });

  /** 目的：验证中英文复制成功与失败反馈；前置可控剪贴板；示例正文保持完整，测试后恢复方法。 */
  it.each([
    ["zh-CN", "导入示例", "完整路由模板正文", "复制示例", "已复制", "复制失败，请手动选择代码复制。"],
    ["en", "Import example", "Complete route-template document", "Copy example", "Copied", "Copy failed. Select the code and copy it manually."],
  ] as const)("reports clipboard outcomes in %s", async (locale, tab, region, copy, copied, failed) => {
    const user = userEvent.setup();
    const write = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValueOnce().mockRejectedValueOnce(new Error("denied"));
    try {
      renderGuide(locale);
      await user.click(screen.getByRole("tab", { name: tab }));
      const example = screen.getByRole("region", { name: region });
      await user.click(within(example).getByRole("button", { name: copy }));
      expect(write).toHaveBeenLastCalledWith(JSON.stringify(ROUTE_TEMPLATE_EXAMPLE, null, 2));
      expect(within(example).getByRole("status")).toHaveTextContent(copied);
      await user.click(within(example).getByRole("button", { name: copy }));
      expect(within(example).getByRole("status")).toHaveTextContent(failed);
    } finally {
      write.mockRestore();
    }
  });
});
