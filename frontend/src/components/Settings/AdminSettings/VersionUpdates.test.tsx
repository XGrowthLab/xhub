import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { I18nProvider } from "@/i18n";
import { ApiError } from "@/lib/http/client";
import { submitVersionUpdate, versionUpdateStatus, type VersionUpdateStatus } from "@/lib/http/versionUpdates";
import VersionUpdates from "./VersionUpdates";

vi.mock("@/lib/http/versionUpdates", () => ({
  versionUpdateStatus: vi.fn(),
  submitVersionUpdate: vi.fn(),
}));
beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
});
afterEach(cleanup);

const target = { version: "v0.1.0-beta.2", revision: "a".repeat(40), compatible: true, prerelease: true, notes: "" };
const installed: VersionUpdateStatus = { enabled: true, stage: "idle", channel: "beta", current: "v0.1.0-beta.1", candidate: target };

/** 未启用时只展示说明；禁止用户误以为可以升级，卸载后清理轮询。 */
it("keeps the disabled installation read only", async () => {
  vi.mocked(versionUpdateStatus).mockResolvedValue({ enabled: false });
  render(<I18nProvider initialLocale="en"><VersionUpdates /></I18nProvider>);
  expect(await screen.findByText("The host update service is not enabled for this installation.")).toBeVisible();
  expect(screen.queryByRole("button", { name: "Check for updates" })).not.toBeInTheDocument();
  expect(submitVersionUpdate).not.toHaveBeenCalled();
});

/** 主机允许后从中文界面提交测试渠道并展示新状态，刷新间隔和模拟调用在卸载时清理。 */
it("switches to the beta channel with an authorized host", async () => {
  const stable: VersionUpdateStatus = { enabled: true, stage: "idle", channel: "stable", channel_switch_enabled: true };
  vi.mocked(versionUpdateStatus).mockResolvedValue(stable);
  vi.mocked(submitVersionUpdate).mockResolvedValueOnce({ ...stable, channel: "beta" });
  const user = userEvent.setup();
  render(<I18nProvider initialLocale="zh-CN"><VersionUpdates /></I18nProvider>);
  await user.click(await screen.findByRole("button", { name: "切换到测试版渠道" }));
  expect(await screen.findByText("测试版渠道", { exact: true })).toBeVisible();
  expect(screen.getByRole("button", { name: "切换到正式版渠道" })).toBeEnabled();
  expect(submitVersionUpdate).toHaveBeenCalledExactlyOnceWith("channel", { channel: "beta" });
});

/** 英文界面在主机锁定时不展示按钮，策略变更导致的拒绝会给出明确提示，结束停止轮询。 */
it("reports a host that locks the release channel", async () => {
  vi.mocked(versionUpdateStatus).mockResolvedValueOnce(installed);
  const user = userEvent.setup();
  const first = render(<I18nProvider initialLocale="en"><VersionUpdates /></I18nProvider>);
  expect(await screen.findByText("Beta channel", { exact: true })).toBeVisible();
  expect(screen.queryByRole("button", { name: "Switch to stable channel" })).not.toBeInTheDocument();
  first.unmount();

  vi.mocked(versionUpdateStatus).mockResolvedValue({ ...installed, channel_switch_enabled: true });
  vi.mocked(submitVersionUpdate).mockRejectedValueOnce(new ApiError("host policy changed", 403, { error: { type: "update_channel_locked" } }));
  render(<I18nProvider initialLocale="en"><VersionUpdates /></I18nProvider>);
  await user.click(await screen.findByRole("button", { name: "Switch to stable channel" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("The host configuration controls this instance’s release channel.");
  expect(screen.getByText("Beta channel", { exact: true })).toBeVisible();
});

/** 中文确认框传递当前已选版本和摘要。主机返回冲突时显示翻译，按钮不自动重试。 */
it("submits the selected revision once and shows a localized conflict", async () => {
  vi.mocked(versionUpdateStatus).mockResolvedValue(installed);
  vi.mocked(submitVersionUpdate).mockRejectedValueOnce(new ApiError("internal detail", 409, {}));
  const user = userEvent.setup();
  render(<I18nProvider initialLocale="zh-CN"><VersionUpdates /></I18nProvider>);
  await user.click(await screen.findByRole("button", { name: "立即更新" }));
  const dialog = screen.getByRole("dialog", { name: "更新到 v0.1.0-beta.2" });
  await user.click(within(dialog).getByRole("button", { name: "确认执行" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("正在执行其他操作，或目标版本已变化");
  expect(submitVersionUpdate).toHaveBeenCalledExactlyOnceWith("apply", { version: target.version, revision: target.revision });
});

/** 数据库标识不匹配时候选说明可见，升级按钮不可操作；卸载清理读状态。 */
it("renders a blocked candidate without an actionable update", async () => {
  const blocked = { ...installed, candidate: { ...target, compatible: false, notes: "Changes the database schema." } };
  vi.mocked(versionUpdateStatus).mockResolvedValue(blocked);
  const user = userEvent.setup();
  render(<I18nProvider initialLocale="en"><VersionUpdates /></I18nProvider>);
  expect(await screen.findByText(/The database compatibility identifiers differ/)).toBeVisible();
  expect(screen.getByRole("button", { name: "Update now" })).toBeDisabled();
  await user.click(screen.getByText("View release notes"));
  expect(screen.getByText(blocked.candidate.notes)).toBeVisible();
});

/** 读取有未恢复事务的状态时展示故障及版本，阻止检查、更新和回退。卸载后停止轮询。 */
it("prevents new operations while an incomplete deployment requires recovery", async () => {
  const failed: VersionUpdateStatus = {
    ...installed, stage: "failed", previous: "v0.0.9", error: "recovery_required", recovery_required: true, channel_switch_enabled: true,
  };
  vi.mocked(versionUpdateStatus).mockResolvedValue(failed);
  render(<I18nProvider initialLocale="en"><VersionUpdates /></I18nProvider>);
  expect(await screen.findByRole("alert")).toHaveTextContent("The original version could not be restored.");
  for (const name of ["Check for updates", "Update now", "Roll back", "Switch to stable channel"]) {
    expect(screen.getByRole("button", { name })).toBeDisabled();
  }
});
