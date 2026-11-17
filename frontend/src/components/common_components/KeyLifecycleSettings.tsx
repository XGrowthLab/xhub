import React from "react";
import { CircleHelp } from "lucide-react";

import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useT } from "@/i18n";

interface KeyLifecycleSettingsProps {
  value?: string;
  onChange?: (value: string) => void;
  autoRotationEnabled: boolean;
  onAutoRotationChange: (enabled: boolean) => void;
  rotationInterval: string;
  onRotationIntervalChange: (interval: string) => void;
  isCreateMode?: boolean;
  neverExpire?: boolean;
  onNeverExpireChange?: (checked: boolean) => void;
  id?: string;
}

/** 为期限和轮换说明渲染提示图标；参数为已翻译说明，返回可访问提示，无写入副作用。 */
const hintIcon = (hint: string): React.ReactNode => (
  <Tooltip>
    <TooltipTrigger
      render={<CircleHelp className="size-3.5 shrink-0 cursor-help text-muted-foreground" />}
      aria-label={hint}
    />
    <TooltipContent>{hint}</TooltipContent>
  </Tooltip>
);

/** 渲染共享的密钥生命周期控件；参数及回调控制期限与永不过期，返回表单片段；编辑留空保留期限，自动轮换仅显示不支持说明。 */
const KeyLifecycleSettings: React.FC<KeyLifecycleSettingsProps> = ({
  value,
  onChange,
  isCreateMode = false,
  neverExpire = false,
  onNeverExpireChange,
  id,
}) => {
  const t = useT();
  const durationId = id ?? "key-lifecycle-duration";

  /** 同步永不过期选项；参数为选中状态，返回无，勾选时清除相对期限输入。 */
  const handleNeverExpireChange = (checked: boolean) => {
    onNeverExpireChange?.(checked);
    if (checked) {
      onChange?.("");
    }
  };

  return (
    <TooltipProvider>
      <div className="space-y-6">
        <div className="space-y-4">
          <div className="space-y-2">
            <div className="flex items-center space-x-1 text-sm font-medium text-foreground">
              <label htmlFor={durationId}>{t("keyEditor.expire")}</label>
              {hintIcon(t("keyEditor.expiryHint"))}
              {!isCreateMode && onNeverExpireChange && (
                <span className="ml-2 flex items-center gap-2 text-sm font-normal text-muted-foreground">
                  <Checkbox
                    id={`${durationId}-never-expire`}
                    checked={neverExpire}
                    onCheckedChange={handleNeverExpireChange}
                  />
                  <label htmlFor={`${durationId}-never-expire`} className="cursor-pointer">
                    {t("keyEditor.neverExpire")}
                  </label>
                </span>
              )}
            </div>
            <Input
              id={durationId}
              value={value ?? ""}
              onChange={(event) => onChange?.(event.target.value)}
              placeholder={isCreateMode ? t("keyEditor.createPlaceholder") : t("keyEditor.editPlaceholder")}
              className="w-full"
              disabled={!isCreateMode && neverExpire}
            />
          </div>
        </div>

        <p className="text-sm text-muted-foreground">{t("keyEditor.rotationUnsupported")}</p>
      </div>
    </TooltipProvider>
  );
};

export default KeyLifecycleSettings;
