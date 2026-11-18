import { render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { translate, type Locale } from "@/i18n/translate";
import { DirectoryFormSection } from "./DirectoryFormSection";

let language: Locale = "en";
vi.mock("@/i18n", async (load) => ({
  ...await load<typeof import("@/i18n")>(),
  useT: () => (key: string) => translate(language, key),
}));

describe("DirectoryFormSection", () => {
  for (const section of ["identity", "access", "limits"] as const) {
    /** 目录表单切换语言时保持输入内容和禁用状态；用真实词典验证分组名称，渲染器自动清理。 */
    it(`keeps fields intact across languages in ${section}`, () => {
      const key = section === "access" ? "directoryForm.modelAccess" : `directoryForm.${section}`;
      const fields = <DirectoryFormSection section={section}><input aria-label="test" defaultValue="用户输入" disabled /></DirectoryFormSection>;
      language = "en";
      const view = render(fields);
      expect(within(screen.getByRole("group", { name: translate(language, key) })).getByRole("textbox")).toBeDisabled();
      language = "zh-CN";
      view.rerender(<DirectoryFormSection section={section}><input aria-label="test" defaultValue="用户输入" disabled /></DirectoryFormSection>);
      expect(within(screen.getByRole("group", { name: translate(language, key) })).getByRole("textbox")).toHaveValue("用户输入");
    });
  }
});
