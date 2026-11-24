import { renderWithProviders as render, screen } from "../../../../../../tests/test-utils";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { userGetInfoV2, type UserInfoV2Response } from "@/components/networking";
import UserAccountEditDialog, { unwrapUserInfo } from "./UserAccountEditDialog";

vi.mock("@/components/networking", () => ({ userGetInfoV2: vi.fn() }));
vi.mock("./UserAccountEditor", () => ({
  default: ({ user, onCancel }: { user: { user_id: string }; onCancel: () => void }) => (
    <button onClick={onCancel}>{user.user_id}</button>
  ),
}));

const account = { user_id: "account-one", user_alias: "测试别名" } as UserInfoV2Response;

/** 用户快照转换可识别当前和历史形态；前置本地数据，检查缺少 ID 时拒绝返回，测试没有外部写入。 */
it("extracts direct and wrapped account snapshots", () => {
  expect(unwrapUserInfo(account)).toBe(account);
  expect(unwrapUserInfo({ user_info: account })).toBe(account);
  expect(unwrapUserInfo({ user_id: undefined })).toBeNull();
  expect(unwrapUserInfo({ user_info: { user_id: "" } })).toBeNull();
  expect(unwrapUserInfo(null)).toBeNull();
});

describe("UserAccountEditDialog", () => {
  const props = { userId: "account-one", accessToken: "test-token", onClose: vi.fn(), onSaved: vi.fn() };

  /** 列表选择账户后读取快照并交给表单；前置成功请求，校验取消只关闭弹窗，替身及 DOM 由框架清理。 */
  it("loads an account on demand and closes on cancel", async () => {
    vi.mocked(userGetInfoV2).mockResolvedValueOnce(account);
    render(<UserAccountEditDialog {...props} />);
    expect(screen.getByRole("status")).toHaveTextContent(/loading/i);
    await userEvent.click(await screen.findByRole("button", { name: "account-one" }));
    expect(userGetInfoV2).toHaveBeenCalledWith("test-token", "account-one");
    expect(props.onClose).toHaveBeenCalledOnce();
    expect(props.onSaved).not.toHaveBeenCalled();
  });

  /** 获取不到账号时给出弹窗内的可访问错误；前置读取失败，确认无表单、没有保存回调，框架清理 DOM。 */
  it("renders a load failure without mounting an editor", async () => {
    vi.mocked(userGetInfoV2).mockRejectedValueOnce(new Error("unavailable"));
    render(<UserAccountEditDialog {...props} />);
    expect(await screen.findByRole("alert")).toHaveTextContent(/user not found/i);
    expect(screen.queryByRole("button", { name: "account-one" })).not.toBeInTheDocument();
  });
});
