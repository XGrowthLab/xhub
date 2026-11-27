import { describe, expect, it } from "vitest";
import { requestLogStatus } from "./taskStatus";

describe("task lifecycle status", () => {
  /** 前置纯展示函数；验证正常生命周期、旧日志与失败优先级，无 DOM 或数据需要清理。 */
  it("distinguishes lifecycle states and retains legacy/error behavior", () => {
    for (const [status, label] of Object.entries({ executing: "Executing", polling: "Polling", completed: "Completed", failed: "Failure", success: "Success", unknown: "Success" })) {
      expect(requestLogStatus({ status }).label).toBe(label);
    }
    expect(requestLogStatus({}).label).toBe("Success");
    expect(requestLogStatus({ status: "completed", error: "bad" }).tone).toBe("error");
  });

  /** 前置服务端分级、历史状态和未成功的 HTTP 状态；验证警告与错误的颜色及翻译键，纯函数无需清理。 */
  it("distinguishes warnings and service errors even when legacy status is absent", () => {
    expect(requestLogStatus({ severity: "warning" })).toEqual({ tone: "warning", label: "logsUX.severityWarning" });
    expect(requestLogStatus({ status: "success", metadata: { http_status: 401 } }).tone).toBe("warning");
    expect(requestLogStatus({ severity: "error", metadata: { http_status: 499 } })).toEqual({ tone: "error", label: "logsUX.severityError" });
  });
});
