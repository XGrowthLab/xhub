import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { UsageViewSelect } from "./UsageViewSelect";
import { translate } from "@/i18n/translate";

const offers = (label: string) => screen.queryByRole("tab", { name: label }) != null;

describe("UsageViewSelect", () => {
  const mockOnChange = vi.fn();

  beforeEach(() => {
    mockOnChange.mockClear();
  });

  /** 普通用户只有个人范围时不显示无效切换器；既不扩权也不触发范围变化，DOM 自动清理。 */
  it("hides a single-option selector without changing scope", () => {
    render(<UsageViewSelect value="my-usage" onChange={mockOnChange} userRole="Internal User" />);
    expect(screen.queryByRole("tablist")).not.toBeInTheDocument();
    expect(mockOnChange).not.toHaveBeenCalled();
  });

  it("should call onChange when value changes", async () => {
    const user = userEvent.setup();
    render(<UsageViewSelect value="global" onChange={mockOnChange} userRole="Admin" />);

    await user.click(screen.getByRole("tab", { name: translate("en", "pages.usage.team") }));

    expect(mockOnChange).toHaveBeenCalled();
    expect(mockOnChange.mock.calls[0][0]).toBe("team");
  });

  /** 前置管理员或普通用户；验证客户和标签入口均已删除，参数为角色，无返回值；组件自动卸载，无数据写入。 */
  it.each(["Admin", "Internal User"])("removed dimensions are unavailable to %s", (userRole) => {
    render(<UsageViewSelect value="global" onChange={mockOnChange} userRole={userRole} />);
    expect(offers(translate("en", "pages.usage.customer"))).toBe(false);
    expect(offers(translate("en", "pages.usage.tag"))).toBe(false);
  });

  it.each(["pages.usage.organization"] as const)("should show %s to an admin", async (key) => {
    const user = userEvent.setup();
    render(<UsageViewSelect value="global" onChange={mockOnChange} userRole="Admin" />);

    expect(offers(translate("en", key))).toBe(true);
    void user;
  });

  it.each(["pages.usage.organization"] as const)(
    "should hide %s from an internal user",
    async (key) => {
    const user = userEvent.setup();
    render(
      <UsageViewSelect value="global" onChange={mockOnChange} userRole="Internal User" />,
    );

    expect(offers(translate("en", key))).toBe(false);
    void user;
  });

  // An org admin's session role is "Internal User" — org-admin-ness lives in the
  // membership table — so the two rows above cannot tell them apart from a plain
  // internal user. Organization Usage must open for them, and only that option:
  // the proxy serves them /organization/daily/activity scoped to the orgs they
  // administer, but still refuses the agent usage route.
  it.each([["pages.usage.organization", true]] as const)("should offer %s to an org admin: %s", async (key, expected) => {
    const user = userEvent.setup();
    render(
      <UsageViewSelect value="global" onChange={mockOnChange} userRole="Internal User" isOrgAdmin={true} />,
    );

    expect(offers(translate("en", key))).toBe(expected);
    void user;
  });

  it("offers team and user filters to a team administrator, and not the organization", async () => {
    render(
      <UsageViewSelect value="team" onChange={mockOnChange} userRole="Internal User" isTeamAdmin />,
    );
    expect(offers(translate("en", "pages.usage.team"))).toBe(true);
    expect(offers(translate("en", "pages.usage.user"))).toBe(true);
    expect(offers(translate("en", "pages.usage.organization"))).toBe(false);
  });

  it("hides team and user filters from a plain member", async () => {
    render(<UsageViewSelect value="my-usage" onChange={mockOnChange} userRole="Internal User" />);
    expect(offers(translate("en", "pages.usage.team"))).toBe(false);
    expect(offers(translate("en", "pages.usage.user"))).toBe(false);
    expect(offers(translate("en", "pages.usage.yours"))).toBe(false);
  });

  it("shows only the localized view name on the closed control", async () => {
    const { setActiveLocale } = await import("@/i18n/runtime");
    setActiveLocale("zh-CN");
    render(<UsageViewSelect value="global" onChange={mockOnChange} userRole="Admin" />);
    const selected = screen.getByRole("tab", { selected: true });
    expect(selected).toHaveTextContent("全局用量");
    expect(selected.textContent ?? "").not.toMatch(/(^|\s)global(\s|$)/);
    setActiveLocale("en");
  });

  it("uses Simplified Chinese labels when locale is zh-CN", async () => {
    const { setActiveLocale } = await import("@/i18n/runtime");
    setActiveLocale("zh-CN");
    const user = userEvent.setup();
    render(<UsageViewSelect value="global" onChange={mockOnChange} userRole="Admin" />);
    expect(screen.getByRole("tablist", { name: translate("zh-CN", "pages.usage.viewTitle") })).toBeInTheDocument();
    expect(screen.queryByText(translate("en", "pages.usage.viewTitle"))).not.toBeInTheDocument();
    expect(offers(translate("zh-CN", "pages.usage.global"))).toBe(true);
    expect(offers(translate("en", "pages.usage.global"))).toBe(false);
    void user;
    setActiveLocale("en");
  });
});
