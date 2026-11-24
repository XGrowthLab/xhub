import { createApiClient } from "./client";
import { getAuthHeaderName, getAuthToken, getRequestBaseUrl } from "./runtime";

const client = createApiClient({ getBaseUrl: getRequestBaseUrl, getAuthHeaderName });
const endpoint = "/admin/version-update";

export type UpdateAction = "check" | "apply" | "rollback" | "channel";
export type UpdateChannel = "stable" | "beta";
export type UpdateTarget = { version?: string; revision?: string; channel?: UpdateChannel };
export type UpdateStage = "idle" | "checking" | "pulling" | "switching" | "recovering" | "succeeded" | "failed";
export type UpdateFailure = "interrupted" | "rolled_back" | "recovery_required" | "check_failed" | "prepare_failed" | "invalid_target";

export interface VersionUpdateStatus {
  enabled: boolean;
  stage?: UpdateStage;
  channel?: UpdateChannel;
  channel_switch_enabled?: boolean;
  current?: string;
  previous?: string;
  candidate?: { version: string; revision: string; prerelease: boolean; compatible: boolean; notes: string };
  error?: UpdateFailure;
  recovery_required?: boolean;
  checked?: number;
}

/** 读取当前升级状态。无参数，返回版本及阶段；管理员页轮询调用，不在浏览器保存部署凭据。 */
export function versionUpdateStatus(): Promise<VersionUpdateStatus> {
  return client.get(endpoint, { accessToken: getAuthToken() });
}

/** 提交检查、镜像操作或渠道设置。action 决定固定路径，target 保存选择；失败不自动重试。 */
export function submitVersionUpdate(action: UpdateAction, target: UpdateTarget): Promise<VersionUpdateStatus> {
  return client.post(endpoint + "/" + action, { accessToken: getAuthToken(), body: target });
}
