"use client";

import { useState } from "react";
import { Boxes } from "lucide-react";
import { cn } from "@/lib/cva.config";
import { logoTreatmentFor } from "@/lib/logoTreatments";
import { resolveModelBrand } from "./modelBrand";

/** 校验目录提供的头像地址，返回 HTTPS 图片或受支持的内联位图；供模型头像使用，未声明或无效时回退到本地资源。 */
function marketAvatar(value: unknown): string | undefined {
  if (typeof value !== "string") return;
  const source = value.trim();
  if (/^data:image\/(?:png|jpeg|webp|gif);base64,[A-Za-z0-9+/]+={0,2}$/i.test(source)) return source;
  if (!/^https:\/\//i.test(source)) return;
  try {
    const url = new URL(source);
    if (!url.username && !url.password) return url.href;
  } catch {
    // 目录字段可能为空或已失效，头像组件仍应提供本地图标。
  }
}

/** 为有相邻模型名称的卡片、展开行和详情显示装饰性头像。
 * model 为目录标识或上游型号，avatar/issuer 来自目录，provider 是旧接口执行标识，className 设置尺寸。图片加载失败时固定占位并降级。 */
export function ModelAvatar({ model, avatar, issuer, provider, className }: {
  model: string;
  avatar?: unknown;
  issuer?: unknown;
  provider?: string | null;
  className?: string;
}) {
  const [failed, setFailed] = useState<ReadonlySet<string>>(() => new Set());
  const artwork = [
    { source: marketAvatar(avatar), kind: "market" },
    { source: resolveModelBrand({ model, issuer, provider })?.logo, kind: "provider" },
  ].find(({ source }) => source && !failed.has(source));
  const source = artwork?.source;
  const treatment = logoTreatmentFor(source ?? "");

  return (
    <span
      aria-hidden="true"
      data-testid="model-avatar"
      data-avatar-kind={artwork?.kind ?? "generic"}
      className={cn("grid size-9 shrink-0 place-items-center rounded-lg border border-border bg-muted/40 text-muted-foreground", className)}
    >
      {source ? (
        // 头像体积小且可能来自目录 CDN，自然加载可直接降级，无需请求图片优化代理。
        // eslint-disable-next-line @next/next/no-img-element
        <img
          key={source}
          src={source}
          alt=""
          loading="lazy"
          decoding="async"
          referrerPolicy="no-referrer"
          className={cn("size-3/5 rounded-sm object-contain", treatment === "invert" && "dark:[filter:brightness(0)_invert(1)]", treatment === "plate" && "dark:bg-logo-surface dark:p-0.5")}
          onError={() => setFailed((previous) => new Set(previous).add(source))}
        />
      ) : <Boxes className="size-4" />}
    </span>
  );
}
