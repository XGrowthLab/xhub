import { useT } from "@/i18n";
export type ModelViewType = "groups" | "individual";

const MODEL_VIEW_OPTIONS: readonly { value: ModelViewType; labelKey: string }[] = [
  { value: "groups", labelKey: "Public Model Name" },
  { value: "individual", labelKey: "Litellm Model Name" },
];

interface ModelViewToggleProps {
  value: ModelViewType;
  onChange: (value: ModelViewType) => void;
}

/** 切换排行按公开名或实际模型聚合；value 为当前模式，onChange 通知父级，返回可键盘操作的按钮组。
 * 组内标签不拆行，容器不足时由父级整组换行，不更改用量数据和聚合规则。 */
export default function ModelViewToggle({ value, onChange }: ModelViewToggleProps) {
  const t = useT();
  return (
    <div className="flex w-fit max-w-full shrink-0 flex-wrap gap-1 rounded-lg bg-muted p-1">
      {MODEL_VIEW_OPTIONS.map((option) => (
        <button
          key={option.value}
          aria-pressed={value === option.value}
          className={`shrink-0 whitespace-nowrap px-3 py-1 text-sm rounded-md transition-colors ${
            value === option.value ? "bg-card shadow-xs text-foreground" : "text-muted-foreground hover:text-foreground"
          }`}
          onClick={() => onChange(option.value)}
        >
          {t(option.labelKey)}
        </button>
      ))}
    </div>
  );
}
