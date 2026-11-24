import { afterEach, expect, it, vi } from "vitest";
import { submitVersionUpdate, versionUpdateStatus } from "./versionUpdates";

const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));
vi.mock("./client", () => ({ createApiClient: () => ({ get, post }) }));
vi.mock("./runtime", () => ({
  getAuthHeaderName: () => "Authorization",
  getAuthToken: () => "fixture-session",
  getRequestBaseUrl: () => "/gw",
}));

afterEach(() => vi.resetAllMocks());

/** 前置已登录管理员，读取已禁用状态也应使用管理入口和会话凭据；完成后清空模拟调用。 */
it("reads the update state through the authenticated management route", async () => {
  get.mockResolvedValue({ enabled: false });
  await expect(versionUpdateStatus()).resolves.toEqual({ enabled: false });
  expect(get).toHaveBeenCalledExactlyOnceWith("/admin/version-update", { accessToken: "fixture-session" });
});

/** 管理操作只提交已选版本，网络中断时返回原错误且不重试；只访问模拟客户端。 */
it("does not retry an operation when the update connection closes", async () => {
  const error = new Error("connection lost");
  const target = { version: "v0.1.0-beta.1" };
  post.mockRejectedValue(error);
  await expect(submitVersionUpdate("rollback", target)).rejects.toBe(error);
  expect(post).toHaveBeenCalledExactlyOnceWith("/admin/version-update/rollback", {
    accessToken: "fixture-session", body: target,
  });
});

/** 平台管理员保存渠道时只提交选项和既有会话，成功返回主机确认的状态，结束清空模拟客户端。 */
it("submits a channel to the authenticated version update endpoint", async () => {
  const status = { enabled: true, channel: "beta", channel_switch_enabled: true };
  post.mockResolvedValue(status);
  await expect(submitVersionUpdate("channel", { channel: "beta" })).resolves.toEqual(status);
  expect(post).toHaveBeenCalledExactlyOnceWith("/admin/version-update/channel", {
    accessToken: "fixture-session", body: { channel: "beta" },
  });
});
