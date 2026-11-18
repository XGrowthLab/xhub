import { useI18n } from "@/i18n";
import type { MyModelCard, ModelPriceRate } from "./grantedModelCards";

const sides = ["input", "output", "cache_read", "cache_write", "batch_input", "batch_output"];
const variants = ["uncached", "cached", "1h", "30m", "12h", "search"];
const windows = ["peak", "offpeak"];
const units = ["token", "query", "second", "picture"];

/** 展示当前部署已确认的完整价格；model 为授权接口响应，返回分档或费率表。
 * 明确显示上下文边界、缓存期限、时段及搜索按次价格；不查询管理接口或最新市场价，不修改任何配置。
 */
export function ModelPriceDetails({ model }: { model: MyModelCard }) {
  const { t, locale } = useI18n();
  /** 渲染本档全部计价项；rates 为已确认快照，缺失或非法金额显示缺价，不伪造为免费。 */
  function ratesTable(rates: ModelPriceRate[], caption: string) {
    return (
      <div className="overflow-x-auto rounded-lg border">
        <table className="w-full text-left text-sm">
          <caption className="sr-only">{caption}</caption>
          <thead className="bg-muted/50">
            <tr>
              <th className="p-3">{t("myModels.item")}</th>
              <th className="p-3 text-right">{t("myModels.rate")}</th>
            </tr>
          </thead>
          <tbody>
            {rates.map((rate, index) => (
              <tr key={index} className="border-t">
                <td className="p-3">
                  {sides.includes(rate.side) ? t("marketSync.side." + rate.side) : rate.side}
                  {rate.variant && (
                    <span className="ml-2 text-muted-foreground">
                      {variants.includes(rate.variant) ? t("marketSync.variant." + rate.variant) : rate.variant}
                    </span>
                  )}
                  {rate.window !== "all" && (
                    <span className="ml-2 text-muted-foreground">
                      {windows.includes(rate.window) ? t("marketSync.window." + rate.window) : rate.window}
                    </span>
                  )}
                </td>
                <td className="whitespace-nowrap p-3 text-right tabular-nums">
                  {Number.isFinite(rate.usd) &&
                  rate.usd >= 0 &&
                  Number.isFinite(rate.usd * (rate.measure === "token" ? 1_000_000 : 1))
                    ? "$" +
                      new Intl.NumberFormat("en-US", { maximumSignificantDigits: 8 }).format(
                        rate.usd * (rate.measure === "token" ? 1_000_000 : 1),
                      )
                    : t("Price unavailable")}
                  <span className="ml-1 text-muted-foreground">
                    / {units.includes(rate.measure) ? t("marketSync.unit." + rate.measure) : rate.measure}
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    );
  }
  if (model.price_tiers?.length)
    return (
      <div className="space-y-4">
        <p className="text-sm text-muted-foreground">{t("consoleUi.tierPriceHelp")}</p>
        {model.price_tiers.map((tier, index) => {
          const title = t("marketSync.inputTier", {
            lower: tier.input_range[0].toLocaleString(locale),
            upper: tier.input_range[1].toLocaleString(locale),
          });
          return (
            <section key={index} aria-label={title} className="space-y-2">
              <h3 className="text-sm font-medium">{title}</h3>
              {ratesTable(tier.rates, title)}
            </section>
          );
        })}
      </div>
    );
  return ratesTable(model.rates ?? [], t("myModels.pricing"));
}
