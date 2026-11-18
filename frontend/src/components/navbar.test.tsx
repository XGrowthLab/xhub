import userEvent from "@testing-library/user-event";
import React from "react";
import { describe, expect, it, vi, afterEach } from "vitest";
import { renderWithProviders, screen, waitFor } from "../../tests/test-utils";
import Navbar from "./navbar";

// Mock the hooks and utilities
vi.mock("@/components/networking", () => ({
  getProxyBaseUrl: vi.fn(() => "http://localhost:4000"),
  serverRootPath: "",
}));

vi.mock("@/app/(dashboard)/hooks/useDisableBouncingIcon", () => ({
  useDisableBouncingIcon: () => false,
}));

vi.mock("./Navbar/BlogDropdown/BlogDropdown", () => ({
  BlogDropdown: () => <div data-testid="blog-dropdown">Blog</div>,
}));

const mockUserDropdownData = vi.hoisted(() => ({
  current: () => ({
    userId: "test-user",
    userEmail: "test@example.com",
    userRole: "Admin",
    premiumUser: false,
  }),
}));

vi.mock("./Navbar/UserDropdown/UserDropdown", async (importOriginal) => {
  const React = await import("react");
  const { useState } = React;
  const localStorageUtils = await import("@/utils/localStorageUtils");
  return {
    default: function MockUserDropdown({ onLogout }: { onLogout: () => void }) {
      const { userId, userEmail, userRole, premiumUser } = mockUserDropdownData.current();
      const [open, setOpen] = useState(false);
      return (
        <div>
          <button type="button" aria-label="Open account menu" onClick={() => setOpen(!open)}>
            Account
          </button>
          {open && (
            <div data-testid="user-dropdown-content">
              <span>{userId}</span>
              <span>{userRole}</span>
              <span>{userEmail}</span>
              {premiumUser && <span>Premium</span>}
              <button type="button" onClick={() => onLogout()}>
                Logout
              </button>
              <label>
                <input
                  type="checkbox"
                  aria-label="Toggle hide new feature indicators"
                  onChange={(e) => {
                    if (e.target.checked) {
                      localStorageUtils.setLocalStorageItem("disableShowNewBadge", "true");
                      localStorageUtils.emitLocalStorageChange("disableShowNewBadge");
                    }
                  }}
                />
                Toggle hide new feature indicators
              </label>
            </div>
          )}
        </div>
      );
    },
  };
});

vi.mock("@/utils/proxyUtils", () => ({
  fetchProxySettings: vi.fn().mockResolvedValue({
    PROXY_BASE_URL: "",
    PROXY_LOGOUT_URL: "https://example.com/logout",
  }),
}));

// Mock CommunityEngagementButtons component
vi.mock("./Navbar/CommunityEngagementButtons/CommunityEngagementButtons", () => ({
  CommunityEngagementButtons: () => null,
}));

// Create mock functions that can be controlled in tests
let mockUseThemeImpl = () => ({ logoUrl: null as string | null });
let mockUseHealthReadinessDetailsImpl = () => ({ data: null as any });
let mockGetLocalStorageItemImpl = (key: string) => null as string | null;
const mockUseAuthorizedImpl = () => ({
  userId: "test-user",
  userEmail: "test@example.com",
  userRole: "Admin",
  premiumUser: false,
});

const useHealthReadinessDetailsSpy = vi.hoisted(() => vi.fn());

vi.mock("@/contexts/ThemeContext", () => ({
  useTheme: () => mockUseThemeImpl(),
}));

vi.mock("@/app/(dashboard)/hooks/healthReadiness/useHealthReadinessDetails", () => ({
  useHealthReadinessDetails: (accessToken: string | null | undefined) => {
    useHealthReadinessDetailsSpy(accessToken);
    return mockUseHealthReadinessDetailsImpl();
  },
}));

vi.mock("@/app/(dashboard)/hooks/useAuthorized", () => ({
  default: () => mockUseAuthorizedImpl(),
}));

vi.mock("@/utils/localStorageUtils", () => ({
  LOCAL_STORAGE_EVENT: "local-storage-change",
  getLocalStorageItem: (key: string) => mockGetLocalStorageItemImpl(key),
  setLocalStorageItem: vi.fn(),
  removeLocalStorageItem: vi.fn(),
  emitLocalStorageChange: vi.fn(),
}));

vi.mock("@/utils/cookieUtils", () => ({
  clearTokenCookies: vi.fn(),
}));

// Mock window.location.href for logout testing
Object.defineProperty(window, "location", {
  value: { href: "" },
  writable: true,
});

describe("Navbar", () => {
  afterEach(() => { vi.unstubAllGlobals(); });
  const defaultProps = {
    accessToken: "test-token",
    isPublicPage: false,
  };

  it("should render without crashing", () => {
    renderWithProviders(<Navbar {...defaultProps} />);

    // 当前导航栏以视图切换器作为已登录入口，通知按钮已从产品中移除。
    expect(screen.getByRole("button", { name: /AI Gateway/i })).toBeInTheDocument();
    expect(screen.queryByText("Docs")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /open account menu/i })).toBeInTheDocument();
  });

  it("should link the product name to the UI home route rather than the proxy origin", () => {
    renderWithProviders(<Navbar {...defaultProps} />);

    expect(screen.getByRole("link", { name: /xhub/i })).toHaveAttribute("href", "/ui");
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
  });

  it("should display user information in dropdown", async () => {
    const user = userEvent.setup();
    renderWithProviders(<Navbar {...defaultProps} />);

    await user.click(screen.getByRole("button", { name: /open account menu/i }));

    await waitFor(() => {
      expect(screen.getByText("test-user")).toBeInTheDocument();
    });
    expect(screen.getByText("Admin")).toBeInTheDocument();
    expect(screen.getByText("test@example.com")).toBeInTheDocument();
  });

  it("should show sidebar toggle button when onToggleSidebar is provided", () => {
    const mockToggle = vi.fn();
    renderWithProviders(<Navbar {...defaultProps} onToggleSidebar={mockToggle} />);

    const toggleButton = screen.getByTitle("Collapse sidebar");
    expect(toggleButton).toBeInTheDocument();
  });

  it("should call onToggleSidebar when sidebar button is clicked", async () => {
    const mockToggle = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(<Navbar {...defaultProps} onToggleSidebar={mockToggle} />);

    const toggleButton = screen.getByTitle("Collapse sidebar");
    await user.click(toggleButton);

    expect(mockToggle).toHaveBeenCalledTimes(1);
  });

  it("should show premium user badge when premiumUser is true", async () => {
    const user = userEvent.setup();
    const originalCurrent = mockUserDropdownData.current;
    mockUserDropdownData.current = () => ({
      userId: "test-user",
      userEmail: "test@example.com",
      userRole: "Admin",
      premiumUser: true,
    });
    renderWithProviders(<Navbar {...defaultProps} />);

    await user.click(screen.getByRole("button", { name: /open account menu/i }));

    await waitFor(() => {
      expect(screen.getByText("Premium")).toBeInTheDocument();
    });

    // Reset mock
    mockUserDropdownData.current = originalCurrent;
  });

  /** 品牌导航不展示内部构建标识；健康接口即使返回开发版本也不影响产品名称，夹具随后还原。 */
  it("keeps deployment versions out of the product brand", () => {
    mockUseHealthReadinessDetailsImpl = () => ({ data: { litellm_version: "xhub-dev" } });

    renderWithProviders(<Navbar {...defaultProps} />);

    expect(screen.queryByText("vxhub-dev")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "XHub home" })).toHaveTextContent("XHub");

    // Reset mock
    mockUseHealthReadinessDetailsImpl = () => ({ data: null });
  });

  /** 导航品牌不再为版本展示请求健康详情，避免普通用户无谓请求；测试不连接外部服务。 */
  it("does not request readiness metadata just to render the brand", () => {
    useHealthReadinessDetailsSpy.mockClear();

    renderWithProviders(<Navbar {...defaultProps} accessToken="my-token" />);

    expect(useHealthReadinessDetailsSpy).not.toHaveBeenCalled();
  });

  /** 未登录也不读取构建详情；空令牌只影响已有账户行为，结束由框架清理。 */
  it("does not request readiness metadata for a signed-out visitor", () => {
    useHealthReadinessDetailsSpy.mockClear();

    renderWithProviders(<Navbar {...defaultProps} accessToken={null} />);

    expect(useHealthReadinessDetailsSpy).not.toHaveBeenCalled();
  });

  it("should use custom logo from theme context", () => {
    mockUseThemeImpl = () => ({ logoUrl: "https://example.com/custom-logo.png" });

    renderWithProviders(<Navbar {...defaultProps} />);

    const logoImg = screen.getByAltText("XHub");
    expect(logoImg).toHaveAttribute("src", "https://example.com/custom-logo.png");

    // Reset mock
    mockUseThemeImpl = () => ({ logoUrl: null });
  });

  it("should hide authenticated controls on public pages", () => {
    const publicPageProps = { ...defaultProps, isPublicPage: true };
    renderWithProviders(<Navbar {...publicPageProps} />);

    expect(screen.queryByRole("button", { name: /open account menu/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /AI Gateway/i })).not.toBeInTheDocument();
  });

  it("should handle hide new feature indicators toggle", async () => {
    const user = userEvent.setup();

    // Initially disabled
    mockGetLocalStorageItemImpl = (key: string) => {
      if (key === "disableShowNewBadge") return "false";
      return null;
    };

    renderWithProviders(<Navbar {...defaultProps} />);

    await user.click(screen.getByRole("button", { name: /open account menu/i }));

    await waitFor(() => {
      expect(screen.getByText("test-user")).toBeInTheDocument();
    });

    // Find and click the toggle switch
    const toggleSwitch = screen.getByLabelText("Toggle hide new feature indicators");
    await user.click(toggleSwitch);

    // The functions are mocked globally, so we can check if they were called
    // by accessing them through the mock registry
    const localStorageUtils = vi.mocked(await import("@/utils/localStorageUtils"));
    expect(localStorageUtils.setLocalStorageItem).toHaveBeenCalledWith("disableShowNewBadge", "true");
    expect(localStorageUtils.emitLocalStorageChange).toHaveBeenCalledWith("disableShowNewBadge");

    // Reset mock
    mockGetLocalStorageItemImpl = (key: string) => null;
  });

  /** 已登录用户从导航栏退出，真实请求撤销当前会话后清理并跳转；mock 随用例恢复，无业务数据。 */
  it("should revoke the session before clearing credentials", async () => {
    const request = vi.fn().mockResolvedValue(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", request);
    const user = userEvent.setup();

    renderWithProviders(<Navbar {...defaultProps} />);

    await user.click(screen.getByRole("button", { name: /open account menu/i }));

    await waitFor(() => {
      expect(screen.getByText("test-user")).toBeInTheDocument();
    });

    // Click logout
    await user.click(screen.getByText("Logout"));

    const cookieUtils = vi.mocked(await import("@/utils/cookieUtils"));
    await waitFor(() => expect(cookieUtils.clearTokenCookies).toHaveBeenCalled());
    expect(request).toHaveBeenCalledWith("http://localhost:4000/auth/logout", {
      method: "POST", headers: { Authorization: "Bearer test-token" },
    });
    await waitFor(() => {
      expect(window.location.href).toBe("https://example.com/logout");
    });
  });

  it("should not render dark mode toggle slider", () => {
    renderWithProviders(<Navbar {...defaultProps} />);

    // DO NOT RENDER THIS UNTIL ALL COMPONENTS ARE CONFIRMED TO SUPPORT DARK MODE STYLES. IT IS AN ISSUE IF THIS TEST FAILS.
    expect(screen.queryByTestId("dark-mode-toggle")).not.toBeInTheDocument();
  });
});
