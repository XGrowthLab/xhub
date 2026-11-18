"use client";

import { useLogin } from "@/app/(dashboard)/hooks/login/useLogin";
import { useUIConfig } from "@/app/(dashboard)/hooks/uiConfig/useUIConfig";
import LoadingScreen from "@/components/common_components/LoadingScreen";
import { exchangeLoginCode, switchToWorkerUrl } from "@/components/networking";
import { Alert, AlertDescription, AlertTitle } from "@/components/shared/Alert";
import { PasswordInput } from "@/components/shared/PasswordInput";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { FormField } from "@/components/shared/form/FormField";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { TooltipProvider } from "@/components/ui/tooltip";
import { UiLoadingSpinner } from "@/components/ui/ui-loading-spinner";
import { useZodForm } from "@/lib/forms/useZodForm";
import { clearTokenCookies, getCookieFromDocument } from "@/utils/cookieUtils";
import { isJwtExpired } from "@/utils/jwtUtils";
import { consumeReturnUrl } from "@/utils/returnUrlUtils";
import { ArrowRight, CircleAlert, TriangleAlert } from "lucide-react";
import { useRouter } from "next/navigation";
import { useEffect, useId, useMemo, useState, type CSSProperties, type ReactNode } from "react";
import { z } from "zod/v4";
import { useWorker } from "@/hooks/useWorker";
import { useT } from "@/i18n";
import LanguageSwitcher from "@/components/LanguageSwitcher";
import EnterpriseLogin from "@/components/EnterpriseLogin";
import { XHubMark } from "@/components/console/XHubMark";
import styles from "./login-stage.module.css";

type LoginFormValues = { username: string; password: string };

/** 登录页品牌区；使用与控制台相同的品牌标记，名称由当前语言提供。 */
function LoginLogo() {
  const t = useT();
  return (
    <div data-testid="login-logo" className="relative z-raised inline-flex items-center gap-2.5 text-2xl font-semibold tracking-tight text-white">
      <XHubMark role="img" aria-hidden={undefined} aria-label={t("site.logoAlt")} className="size-9 rounded-xl ring-1 ring-white/20" />
      <span>{t("site.product")}</span>
    </div>
  );
}

/** 登录各状态共用的布局；children 是当前表单或状态提示。装饰线条不进入无障碍树，
 * 窄屏压缩封面以优先展示输入框；正确设置减少动态效果的设备直接显示静止构图。 */
function LoginStage({ children }: { children: ReactNode }) {
  const t = useT();
  return (
    <main data-testid="login-stage" className="h-dvh w-full overflow-y-auto bg-background text-foreground">
      <div className={styles.layout}>
        <section className={styles.cover} aria-labelledby="login-cover-title">
          <LoginLogo />
          <div className={styles.art} aria-hidden="true">
            <div className={styles.ribbon}>
              {Array.from({ length: 28 }, (_, index) => (
                <i key={index} style={{ "--ring": index } as CSSProperties} />
              ))}
            </div>
          </div>
          <div className={styles.caption}>
            <h2 id="login-cover-title">{t("login.brandTitle")}</h2>
            <p>{t("login.brandDescription")}</p>
          </div>
        </section>
        <section aria-labelledby="login-title" className={styles.formPane}>
          <div className={styles.language}><LanguageSwitcher /></div>
          <div data-testid="login-card" className="w-full min-w-0 max-w-sm">
            {children}
            <EnterpriseLogin />
          </div>
        </section>
      </div>
    </main>
  );
}

/** 登录路由的表单主体，无参数；根据网关配置返回加载、停用或交互状态，提交后沿原认证流程跳转。 */
function LoginPageContent() {
  const t = useT();
  const [isLoading, setIsLoading] = useState(true);
  const { data: uiConfig, isLoading: isConfigLoading } = useUIConfig();
  const loginMutation = useLogin();
  const router = useRouter();
  const { workers, selectWorker } = useWorker();
  const [selectedWorkerId, setSelectedWorkerId] = useState<string | null>(null);
  const workerFieldId = useId();
  const loginSchema = useMemo(
    () =>
      z.object({
        username: z.string().min(1, t("login.usernameRequired")),
        password: z.string().min(1, t("login.passwordRequired")),
      }),
    [t],
  );
  const form = useZodForm(loginSchema, { defaultValues: { username: "", password: "" } });

  // Pre-select worker from URL param (e.g. /ui/login?worker=team-b)
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const workerParam = params.get("worker");
    if (workerParam) {
      setSelectedWorkerId(workerParam);
    }
  }, []);

  useEffect(() => {
    if (isConfigLoading) {
      return;
    }

    // Check if admin UI is disabled
    if (uiConfig && uiConfig.admin_ui_disabled) {
      setIsLoading(false);
      return;
    }

    // Cross-origin SSO: worker redirected back with a single-use code.
    // Exchange it for the JWT via the worker's /v3/login/exchange endpoint.
    const params = new URLSearchParams(window.location.search);
    const rawSsoCode = params.get("code");
    // Validate the SSO code is a plausible OAuth authorization code (alphanumeric
    // plus common URL-safe chars) so that arbitrary user input cannot trigger the
    // exchange endpoint.
    const ssoCode = rawSsoCode && /^[a-zA-Z0-9._~+/=-]+$/.test(rawSsoCode) ? rawSsoCode : null;
    if (ssoCode) {
      const rawWorkerUrl = localStorage.getItem("litellm_worker_url");
      // Validate the stored worker URL: only allow http(s) URLs.
      const workerUrl = rawWorkerUrl && /^https?:\/\/.+/.test(rawWorkerUrl) ? rawWorkerUrl : null;
      exchangeLoginCode(ssoCode, workerUrl).then(() => {
        params.delete("code");
        const cleanSearch = params.toString();
        window.history.replaceState(null, "", window.location.pathname + (cleanSearch ? `?${cleanSearch}` : ""));
        router.replace("/ui/?login=success");
      });
      return;
    }

    // If switching workers on a control plane, clear the old token and show login
    const switchingWorker = params.has("worker");
    if (switchingWorker && uiConfig?.is_control_plane) {
      clearTokenCookies();
      setIsLoading(false);
      return;
    }

    const rawToken = getCookieFromDocument("token");
    if (rawToken && !isJwtExpired(rawToken)) {
      // User already logged in - redirect to return URL or default
      const returnUrl = consumeReturnUrl();
      if (returnUrl) {
        router.replace(returnUrl);
      } else {
        router.replace("/ui");
      }
      return;
    }

    setIsLoading(false);
  }, [isConfigLoading, router, uiConfig]);

  const handleSubmit = ({ username, password }: LoginFormValues) => {
    // If a worker is selected, point proxyBaseUrl at it before login
    const selectedWorker = workers.find((w) => w.worker_id === selectedWorkerId);
    if (selectedWorker) {
      switchToWorkerUrl(selectedWorker.url);
    }

    loginMutation.mutate(
      { username, password, useV3: !!selectedWorker },
      {
        onSuccess: (data) => {
          // Update the worker context with the selected worker
          if (selectedWorker) {
            selectWorker(selectedWorker.worker_id);
            // Stay on the CP's UI — proxyBaseUrl already points at the worker
            router.push("/ui/?login=success");
          } else {
            // Normal (non-control-plane) login — follow the server's redirect
            const returnUrl = consumeReturnUrl();
            if (returnUrl) {
              router.push(returnUrl);
            } else {
              router.push(data.redirect_url);
            }
          }
        },
        onError: () => {
          // Reset proxyBaseUrl on login failure
          if (selectedWorker) {
            switchToWorkerUrl(null);
          }
        },
      },
    );
  };

  const error = loginMutation.error instanceof Error ? loginMutation.error.message : null;
  const isLoginLoading = loginMutation.isPending;

  if (isConfigLoading || isLoading) {
    return (
      <LoginStage>
        <div className="flex w-full flex-col gap-6 text-center">
          <h1 id="login-title" className="text-3xl font-semibold text-foreground">{t("login.title")}</h1>
          <p className="text-lg text-muted-foreground">{t("login.subtitle")}</p>
          <LoadingScreen />
        </div>
      </LoginStage>
    );
  }

  // Show disabled message if admin UI is disabled
  if (uiConfig && uiConfig.admin_ui_disabled) {
    return (
      <LoginStage>
        <div className="flex w-full flex-col gap-6">
          <div className="text-start">
            <h1 id="login-title" className="text-3xl font-semibold text-foreground">{t("login.title")}</h1>
          </div>

          <Alert variant="warning">
            <TriangleAlert />
            <AlertTitle>{t("login.adminDisabled")}</AlertTitle>
            <AlertDescription>
              <p className="text-sm">{t("login.adminDisabledBody")}</p>
              <p className="mt-2 text-sm">
                <code className="rounded-sm bg-muted px-1 py-0.5 text-xs">DISABLE_ADMIN_UI=False</code>
              </p>
            </AlertDescription>
          </Alert>
        </div>
      </LoginStage>
    );
  }

  return (
    <LoginStage>
      <TooltipProvider>
        <div className="flex w-full flex-col gap-4">
          <div className="text-start">
            <h1 id="login-title" className="text-3xl font-semibold tracking-tight text-foreground">{t("login.title")}</h1>
            <p className="mt-2 mb-2 text-sm text-muted-foreground">{t("login.subtitle")}</p>
          </div>

          {error && (
            <Alert variant="error">
              <CircleAlert />
              <AlertTitle>{error}</AlertTitle>
            </Alert>
          )}

          <form onSubmit={form.handleSubmit(handleSubmit)}>
            <FieldGroup className="gap-4">
              {uiConfig?.is_control_plane && workers.length > 0 && (
                <Field>
                  <FieldLabel htmlFor={workerFieldId}>{t("login.worker")}</FieldLabel>
                  <Select
                    items={workers.map((worker) => ({ label: worker.name, value: worker.worker_id }))}
                    value={selectedWorkerId}
                    onValueChange={(value: string | null) => setSelectedWorkerId(value)}
                  >
                    <SelectTrigger id={workerFieldId} className="h-10 w-full">
                      <SelectValue placeholder={t("login.workerPlaceholder")} />
                    </SelectTrigger>
                    <SelectContent>
                      {workers.map((worker) => (
                        <SelectItem key={worker.worker_id} value={worker.worker_id}>
                          {worker.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              )}

              <FormField control={form.control} name="username" label={t("login.username")}>
                {({ ref, ...field }) => (
                  <Input
                    {...field}
                    ref={ref}
                    placeholder={t("login.usernamePlaceholder")}
                    autoComplete="username"
                    disabled={isLoginLoading}
                    className="h-11 rounded-md border-input bg-background text-base"
                  />
                )}
              </FormField>

              <FormField control={form.control} name="password" label={t("login.password")}>
                {({ ref, ...field }) => (
                  <PasswordInput
                    {...field}
                    ref={ref}
                    placeholder={t("login.passwordPlaceholder")}
                    autoComplete="current-password"
                    disabled={isLoginLoading}
                    groupClassName="h-11"
                  />
                )}
              </FormField>

              <Button
                type="submit"
                size="lg"
                disabled={isLoginLoading}
                className="h-11 w-full gap-2.5 rounded-md text-base font-semibold"
              >
                {isLoginLoading && <UiLoadingSpinner className="size-4" role="img" aria-label={t("loading")} />}
                {isLoginLoading ? t("login.submitting") : t("login.submit")}
                {!isLoginLoading && <ArrowRight aria-hidden="true" className="size-4" />}
              </Button>

            </FieldGroup>
          </form>
        </div>
      </TooltipProvider>
    </LoginStage>
  );
}

export default function LoginPage() {
  const t = useT();
  return <LoginPageContent />;
}
