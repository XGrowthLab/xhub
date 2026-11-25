import useAuthorized from "@/app/(dashboard)/hooks/useAuthorized";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { KeyResponse } from "../../../key_team_helpers/key_list";
import * as transformKeyInfo from "../../../key_team_helpers/transform_key_info";
import * as networking from "../../../networking";
import TopKeyView from "./TopKeyView";
import { I18nProvider } from "@/i18n";

vi.mock("@/app/(dashboard)/hooks/useAuthorized", () => ({
  __esModule: true,
  default: vi.fn(),
}));

vi.mock("../../../networking", () => ({
  keyInfoV1Call: vi.fn(),
}));

vi.mock("../../../key_team_helpers/transform_key_info", () => ({
  transformKeyInfo: vi.fn(),
}));

vi.mock("../../../templates/key_info_view", () => ({
  default: ({ keyId, onClose }: { keyId: string; onClose: () => void }) => (
    <div data-testid="key-info-view">
      <div>Key Info View for {keyId}</div>
      <button onClick={onClose}>Close</button>
    </div>
  ),
}));

describe("TopKeyView", () => {
  const mockUseAuthorized = vi.mocked(useAuthorized);
  const mockKeyInfoV1Call = vi.mocked(networking.keyInfoV1Call);
  const mockTransformKeyInfo = vi.mocked(transformKeyInfo.transformKeyInfo);

  const mockAuth = {
    token: "mock-token",
    accessToken: "test-token",
    userId: "user-1",
    userEmail: "user@example.com",
    userRole: "admin",
    premiumUser: true,
    disabledPersonalKeyCreation: false,
    showSSOBanner: false,
  };

  const mockSetTopKeysLimit = vi.fn();

  const baseProps = {
    topKeys: [],
    teams: null,
    showTags: false,
    topKeysLimit: 5,
    setTopKeysLimit: mockSetTopKeysLimit,
  };

  beforeEach(() => {
    mockUseAuthorized.mockReturnValue(mockAuth);
    mockSetTopKeysLimit.mockClear();
    mockKeyInfoV1Call.mockClear();
    mockTransformKeyInfo.mockClear();
  });

  it("should render", () => {
    render(<TopKeyView {...baseProps} />);
    expect(screen.getByRole("button", { name: "Table" })).toBeInTheDocument();
  });

  it("should display table view button", () => {
    render(<TopKeyView {...baseProps} />);
    expect(screen.getByRole("button", { name: "Table" })).toBeInTheDocument();
  });

  it("should display chart view button", () => {
    render(<TopKeyView {...baseProps} />);
    expect(screen.getByRole("button", { name: "Chart" })).toBeInTheDocument();
  });

  it("should display base table column headers", () => {
    render(<TopKeyView {...baseProps} />);
    expect(screen.getByText("Key ID")).toBeInTheDocument();
    expect(screen.getByText("Recorded name")).toBeInTheDocument();
    expect(screen.getByText("Spend (USD)")).toBeInTheDocument();
  });

  it("should display Tags column when showTags is true", () => {
    render(<TopKeyView {...baseProps} showTags={true} />);
    expect(screen.getByText("Tags")).toBeInTheDocument();
  });

  it("should not display Tags column when showTags is false", () => {
    render(<TopKeyView {...baseProps} showTags={false} />);
    expect(screen.queryByText("Tags")).not.toBeInTheDocument();
  });

  it("should display key information in table view", () => {
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 100,
            tags: [
              { tag: "tag-1", usage: 50 },
              { tag: "tag-2", usage: 30 },
            ],
          },
        ]}
        showTags={true}
      />,
    );
    expect(screen.getByText("Test Key")).toBeInTheDocument();
    expect(screen.getByText(/tag-1/)).toBeInTheDocument();
    expect(screen.getByText(/tag-2/)).toBeInTheDocument();
    expect(screen.getByText("$100.00")).toBeInTheDocument();
  });

  it("should switch to chart view when chart view button is clicked", async () => {
    const user = userEvent.setup();
    render(<TopKeyView {...baseProps} />);

    const chartViewButton = screen.getByRole("button", { name: "Chart" });
    await user.click(chartViewButton);

    expect(chartViewButton).toHaveClass("bg-info/15");
  });

  /** 前置历史别名和仍可访问的关联密钥；从榜单按键打开详情，组件和请求模拟由测试框架清理。 */
  it("renders the exact recorded alias and opens key details from the ranking", async () => {
    const mockKeyInfo = { key: "info" };
    const mockTransformedData = { transformed: "data" } as unknown as KeyResponse;
    mockKeyInfoV1Call.mockResolvedValue(mockKeyInfo);
    mockTransformKeyInfo.mockReturnValue(mockTransformedData);

    const user = userEvent.setup();
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "A Very Long Key Alias",
            spend: 100,
          },
        ]}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Chart" }));

    const row = screen.getByRole("button", { name: /^A Very Long Key Alias/ });
    expect(within(row).getByText("$100.00")).toBeVisible();
    row.focus();
    await user.keyboard("{Enter}");

    await waitFor(() => {
      expect(mockKeyInfoV1Call).toHaveBeenCalledWith("test-token", "key-123");
    });

    await waitFor(() => {
      expect(screen.getByTestId("key-info-view")).toBeInTheDocument();
    });
    expect(screen.getByText("Key Info View for key-123")).toBeInTheDocument();
  });

  it("should switch to table view when table view button is clicked", async () => {
    const user = userEvent.setup();
    render(<TopKeyView {...baseProps} />);

    const chartViewButton = screen.getByRole("button", { name: "Chart" });
    const tableViewButton = screen.getByRole("button", { name: "Table" });

    await user.click(chartViewButton);
    await user.click(tableViewButton);

    expect(tableViewButton).toHaveClass("bg-info/15");
  });

  it("should call setTopKeysLimit when limit is changed via the segmented control", async () => {
    const user = userEvent.setup();
    render(<TopKeyView {...baseProps} />);

    await user.click(screen.getByRole("radio", { name: "10" }));

    expect(mockSetTopKeysLimit).toHaveBeenCalledWith(10);
  });

  it("should display truncated key ID in table", () => {
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "sk-1234567890abcdef",
            key_alias: "Test Key",
            spend: 100,
          },
        ]}
      />,
    );
    const keyId = screen.getByText("sk-1234567890abcdef");
    expect(keyId).toBeInTheDocument();
    expect(keyId).toHaveClass("truncate");
  });

  it("should display dash for missing key alias", () => {
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "",
            spend: 100,
          },
        ]}
      />,
    );
    expect(screen.getByText("Unnamed key")).toBeInTheDocument();
  });

  it("should format spend values with two decimal places", () => {
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 123.456,
          },
        ]}
      />,
    );
    expect(screen.getByText("$123.46")).toBeInTheDocument();
  });

  it("should display sub-cent spend as < $0.01", () => {
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 0.004,
          },
        ]}
      />,
    );
    expect(screen.getByText("< $0.01")).toBeInTheDocument();
  });

  it("should display zero spend as a dash", () => {
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 0,
          },
        ]}
      />,
    );
    expect(screen.getByText("-")).toBeInTheDocument();
    expect(screen.queryByText("$0.00")).not.toBeInTheDocument();
  });

  it("should display dash for empty tags", () => {
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 100,
            tags: [],
          },
        ]}
        showTags={true}
      />,
    );
    expect(screen.getAllByText("-").length).toBeGreaterThan(0);
  });

  it("should display dash for missing tags", () => {
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 100,
          },
        ]}
        showTags={true}
      />,
    );
    expect(screen.getAllByText("-").length).toBeGreaterThan(0);
  });

  it("should display first two tags by default and show expand button", () => {
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 100,
            tags: [
              { tag: "tag-1", usage: 50 },
              { tag: "tag-2", usage: 30 },
              { tag: "tag-3", usage: 20 },
            ],
          },
        ]}
        showTags={true}
      />,
    );
    expect(screen.getByText(/tag-1/)).toBeInTheDocument();
    expect(screen.getByText(/tag-2/)).toBeInTheDocument();
    expect(screen.queryByText(/tag-3/)).not.toBeInTheDocument();
  });

  it("should expand tags when expand button is clicked", async () => {
    const user = userEvent.setup();
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 100,
            tags: [
              { tag: "tag-1", usage: 50 },
              { tag: "tag-2", usage: 30 },
              { tag: "tag-3", usage: 20 },
            ],
          },
        ]}
        showTags={true}
      />,
    );

    const expandButton = screen.getByTitle("Show all tags");
    await user.click(expandButton);

    expect(screen.getByText(/tag-3/)).toBeInTheDocument();
  });

  it("should collapse tags when collapse button is clicked", async () => {
    const user = userEvent.setup();
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 100,
            tags: [
              { tag: "tag-1", usage: 50 },
              { tag: "tag-2", usage: 30 },
              { tag: "tag-3", usage: 20 },
            ],
          },
        ]}
        showTags={true}
      />,
    );

    const expandButton = screen.getByTitle("Show all tags");
    await user.click(expandButton);

    expect(screen.getByText(/tag-3/)).toBeInTheDocument();

    const collapseButton = screen.getByTitle("Show fewer tags");
    await user.click(collapseButton);

    expect(screen.queryByText(/tag-3/)).not.toBeInTheDocument();
  });

  it("should open modal when key ID is clicked", async () => {
    const mockKeyInfo = { key: "info" };
    const mockTransformedData = { transformed: "data" } as unknown as KeyResponse;
    mockKeyInfoV1Call.mockResolvedValue(mockKeyInfo);
    mockTransformKeyInfo.mockReturnValue(mockTransformedData);

    const user = userEvent.setup();
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 100,
          },
        ]}
      />,
    );

    const keyIdButton = screen.getByText("key-123").closest("button");
    if (keyIdButton) {
      await user.click(keyIdButton);
    }

    await waitFor(() => {
      expect(mockKeyInfoV1Call).toHaveBeenCalledWith("test-token", "key-123");
    });

    await waitFor(() => {
      expect(screen.getByTestId("key-info-view")).toBeInTheDocument();
    });
  });

  it("should close modal when close button is clicked", async () => {
    const mockKeyInfo = { key: "info" };
    const mockTransformedData = { transformed: "data" } as unknown as KeyResponse;
    mockKeyInfoV1Call.mockResolvedValue(mockKeyInfo);
    mockTransformKeyInfo.mockReturnValue(mockTransformedData);

    const user = userEvent.setup();
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 100,
          },
        ]}
      />,
    );

    const keyIdButton = screen.getByText("key-123").closest("button");
    if (keyIdButton) {
      await user.click(keyIdButton);
    }

    await waitFor(() => {
      expect(screen.getByTestId("key-info-view")).toBeInTheDocument();
    });

    const closeButton = screen.getByLabelText("Close");
    await user.click(closeButton);

    await waitFor(() => {
      expect(screen.queryByTestId("key-info-view")).not.toBeInTheDocument();
    });
  });

  it("should close modal when escape key is pressed", async () => {
    const mockKeyInfo = { key: "info" };
    const mockTransformedData = { transformed: "data" } as unknown as KeyResponse;
    mockKeyInfoV1Call.mockResolvedValue(mockKeyInfo);
    mockTransformKeyInfo.mockReturnValue(mockTransformedData);

    const user = userEvent.setup();
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 100,
          },
        ]}
      />,
    );

    const keyIdButton = screen.getByText("key-123").closest("button");
    if (keyIdButton) {
      await user.click(keyIdButton);
    }

    await waitFor(() => {
      expect(screen.getByTestId("key-info-view")).toBeInTheDocument();
    });

    await user.keyboard("{Escape}");

    await waitFor(() => {
      expect(screen.queryByTestId("key-info-view")).not.toBeInTheDocument();
    });
  });

  it("should close modal when clicking outside modal", async () => {
    const mockKeyInfo = { key: "info" };
    const mockTransformedData = { transformed: "data" } as unknown as KeyResponse;
    mockKeyInfoV1Call.mockResolvedValue(mockKeyInfo);
    mockTransformKeyInfo.mockReturnValue(mockTransformedData);

    const user = userEvent.setup();
    const { container } = render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 100,
          },
        ]}
      />,
    );

    const keyIdButton = screen.getByText("key-123").closest("button");
    if (keyIdButton) {
      await user.click(keyIdButton);
    }

    await waitFor(() => {
      expect(screen.getByTestId("key-info-view")).toBeInTheDocument();
    });

    const modalBackdrop = container.querySelector(".fixed.inset-0");
    if (modalBackdrop) {
      await user.click(modalBackdrop);
    }

    await waitFor(() => {
      expect(screen.queryByTestId("key-info-view")).not.toBeInTheDocument();
    });
  });

  it("should not open modal when accessToken is missing", async () => {
    mockUseAuthorized.mockReturnValue({
      ...mockAuth,
      accessToken: "",
    });

    const user = userEvent.setup();
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 100,
          },
        ]}
      />,
    );

    const keyIdButton = screen.getByText("key-123").closest("button");
    if (keyIdButton) {
      await user.click(keyIdButton);
    }

    await waitFor(() => {
      expect(mockKeyInfoV1Call).not.toHaveBeenCalled();
    });

    expect(screen.queryByTestId("key-info-view")).not.toBeInTheDocument();
  });

  it("should handle error when fetching key info", async () => {
    const consoleErrorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    mockKeyInfoV1Call.mockRejectedValue(new Error("Network error"));

    const user = userEvent.setup();
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 100,
          },
        ]}
      />,
    );

    const keyIdButton = screen.getByText("key-123").closest("button");
    if (keyIdButton) {
      await user.click(keyIdButton);
    }

    await waitFor(() => {
      expect(mockKeyInfoV1Call).toHaveBeenCalled();
    });

    await waitFor(() => {
      expect(consoleErrorSpy).toHaveBeenCalled();
    });

    consoleErrorSpy.mockRestore();
  });

  it("should sort tags by usage descending", () => {
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "Test Key",
            spend: 100,
            tags: [
              { tag: "tag-low", usage: 10 },
              { tag: "tag-high", usage: 50 },
              { tag: "tag-medium", usage: 30 },
            ],
          },
        ]}
        showTags={true}
      />,
    );

    const tagElements = screen.getAllByText(/tag-/);
    const tagTexts = tagElements.map((el) => el.textContent);
    // Tags are truncated to 7 characters + "...", so "tag-high" becomes "tag-hig..."
    expect(tagTexts[0]).toMatch(/^tag-hig/);
    expect(tagTexts[1]).toMatch(/^tag-med/);
  });

  it("should handle empty key list", () => {
    render(<TopKeyView {...baseProps} topKeys={[]} />);
    expect(screen.getByText("Key ID")).toBeInTheDocument();
    expect(screen.getByText("Recorded name")).toBeInTheDocument();
  });

  it("should display full key alias in table view", () => {
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: "This is a very long key alias",
            spend: 100,
          },
        ]}
      />,
    );
    expect(screen.getByText("This is a very long key alias")).toBeInTheDocument();
  });

  it("should handle keys with no alias", () => {
    render(
      <TopKeyView
        {...baseProps}
        topKeys={[
          {
            api_key: "key-123",
            key_alias: null,
            spend: 100,
          },
        ]}
      />,
    );
    expect(screen.getByText("Unnamed key")).toBeInTheDocument();
  });
  /** 双语历史排行保留已删除测试密钥名称与费用，空 ID 明确未关联且点击不发请求；每例由测试环境清理 DOM 和网络替身。 */
  it.each(["en", "zh-CN"] as const)("explains historical names and unlinked usage in %s", async (locale) => {
    render(<I18nProvider initialLocale={locale}><TopKeyView {...baseProps} topKeys={[
      { api_key: "historic-id", key_alias: "codex-model-smoke", spend: 1.25 },
      { api_key: "", key_alias: null, spend: 0.25 },
    ]} /></I18nProvider>);
    expect(screen.getByText("codex-model-smoke")).toBeVisible();
    expect(screen.getByText("$1.25")).toBeVisible();
    const summary = screen.getByText(locale === "en" ? "Names recorded with requests" : "请求发生时的名称");
    await userEvent.click(summary);
    expect(screen.getByText(locale === "en" ? /This is usage history, not your current key list/ : /这里展示历史用量，不是当前密钥列表/)).toBeVisible();
    await userEvent.click(screen.getAllByText(locale === "en" ? "No linked key" : "未关联密钥")[0]);
    expect(mockKeyInfoV1Call).not.toHaveBeenCalled();
  });

  /** 历史密钥详情读取失败时保留费用并提供可读原因，不把网络错误误报成删除；无真实网络，afterEach 清理。 */
  it("explains unavailable key details without discarding recorded spend", async () => {
    mockKeyInfoV1Call.mockRejectedValueOnce(new Error("not found"));
    render(<TopKeyView {...baseProps} topKeys={[{ api_key: "historic-id", key_alias: "old-test", spend: 1.25 }]} />);
    await userEvent.click(screen.getByText("historic-id"));
    expect(await screen.findByRole("status")).toHaveTextContent("Historical usage is still included");
    expect(screen.getByText("$1.25")).toBeVisible();
  });

});
