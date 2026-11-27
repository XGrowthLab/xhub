"use client";
import { useT } from "@/i18n";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import DeploymentIdentity from "./DeploymentIdentity";
import { Input } from "@/components/ui/input";
import { strategyLabels } from "./allocationPolicy";
import type { ModelRoutingRule, SplitDeployment } from "./templateForm";

/** ModelRoutingFields 编辑模板的按模型负载均衡规则；参数为规则、部署目录和更新回调，
 * 返回可访问的受控表单；添加时使用 simple-shuffle，切换按权重分流先继承，显式编辑才保存自定义分配。 */
export default function ModelRoutingFields({
  rules,
  deployments,
  onChange,
}: {
  rules: ModelRoutingRule[];
  deployments: SplitDeployment[];
  onChange: (rules: ModelRoutingRule[]) => void;
}) {
  const t = useT();
  const [model, setModel] = useState("");
  const configured = new Set(rules.map((rule) => rule.model));
  const names = [...new Set(deployments.map((row) => row.model_name))].filter((name) => !configured.has(name)).sort();

  return (
    <section className="space-y-4" aria-label={t("pages.routeTemplates.editor.modelRules")}>
      <div>
        <h3 className="font-semibold">{t("pages.routeTemplates.editor.modelRulesTitle")}</h3>
        <p className="text-sm text-muted-foreground">{t("pages.routeTemplates.editor.modelHint")}</p>
      </div>
      <div className="flex flex-wrap gap-2">
        <select
          aria-label={t("pages.routeTemplates.editor.publicModel")}
          className="rounded border bg-background p-2"
          value={model}
          onChange={(event) => setModel(event.target.value)}
        >
          <option value="">{t("pages.routeTemplates.editor.chooseModel")}</option>
          {names.map((name) => (
            <option key={name} value={name}>
              {name}
            </option>
          ))}
        </select>
        <Button
          type="button"
          disabled={!model}
          onClick={() => {
            onChange([...rules, { model, strategy: "simple-shuffle" }]);
            setModel("");
          }}
        >
          {t("pages.routeTemplates.editor.addModel")}
        </Button>
      </div>
      {rules.map((rule, index) => {
        const modelDeployments = deployments.filter((row) => row.model_name === rule.model && row.deployment_id);
        const allocations = rule.allocations;
        const weights = [
          ...new Set([
            ...modelDeployments.map((row) => row.deployment_id!),
            ...(allocations ?? []).map((row) => row.deployment_id),
          ]),
        ].map((id) => ({
          id,
          weight: allocations?.find((row) => row.deployment_id === id)?.weight ?? 1,
        }));
        const total = weights.reduce((sum, row) => sum + row.weight, 0);
        const showShares = total > 0 && Number.isFinite(total) && weights.every((row) => row.weight >= 0);
        return (
          <section
            key={rule.model}
            className="flex flex-wrap items-center justify-between gap-4 rounded-lg border p-4"
            aria-label={t("pages.routeTemplates.editor.ruleFor", { model: rule.model })}
          >
            <div>
              <h4 className="font-semibold">{rule.model}</h4>
              {rule.strategy === "traffic-split" && (
                <p className="text-sm text-muted-foreground">{t("pages.routeTemplates.editor.inheritOrOverride")}</p>
              )}
            </div>
            <div className="flex flex-wrap items-center gap-3">
              <label className="flex items-center gap-2">
                {t("pages.routeTemplates.editor.routingLogic")}
                <select
                  aria-label={t("pages.routeTemplates.editor.logicFor", { model: rule.model })}
                  className="rounded border bg-background p-2"
                  value={rule.strategy}
                  onChange={(event) =>
                    onChange(
                      rules.map((row, i) => (i === index ? { model: row.model, strategy: event.target.value } : row)),
                    )
                  }
                >
                  {Object.entries(strategyLabels).map(([id, label]) => (
                    <option key={id} value={id}>
                      {t(label)}
                    </option>
                  ))}
                </select>
              </label>
              <Button type="button" variant="ghost" onClick={() => onChange(rules.filter((_, i) => i !== index))}>
                {t("pages.routeTemplates.editor.deleteRule")}
              </Button>
            </div>
            {rule.strategy === "traffic-split" && (
              <div className="w-full space-y-3 border-t pt-4">
                <label className="flex flex-wrap items-center gap-3">
                  {t("pages.routeTemplates.editor.weightSource")}
                  <select
                    aria-label={t("pages.routeTemplates.editor.sourceFor", { model: rule.model })}
                    className="rounded border bg-background p-2"
                    value={rule.allocations === undefined ? "inherit" : "custom"}
                    onChange={(e) =>
                      onChange(
                        rules.map((r, i) => {
                          if (i !== index) return r;
                          if (e.target.value === "inherit") return { model: r.model, strategy: r.strategy };
                          return {
                            ...r,
                            allocations: modelDeployments.map((d) => ({ deployment_id: d.deployment_id!, weight: 1 })),
                          };
                        }),
                      )
                    }
                  >
                    <option value="custom">{t("pages.routeTemplates.editor.customWeights")}</option>
                    <option value="inherit">{t("pages.routeTemplates.editor.inheritWeights")}</option>
                  </select>
                </label>
                {rule.allocations !== undefined ? (
                  <>
                    <p className="text-sm text-muted-foreground">{t("pages.routeTemplates.editor.customWeightHint")}</p>
                    {weights.map(({ id, weight }) => (
                      <div key={id} className="flex flex-wrap items-center justify-between gap-3 rounded border p-3">
                        <DeploymentIdentity id={id} deployment={deployments.find((d) => d.deployment_id === id)} />
                        <div className="flex items-center gap-3">
                          {showShares && (
                            <span className="text-sm tabular-nums text-muted-foreground">
                              {t("pages.routeTemplates.editor.configuredShare", {
                                ratio: ((weight / total) * 100).toFixed(1),
                              })}
                            </span>
                          )}
                          <Input
                            type="number"
                            min="0"
                            step="any"
                            className="w-28 shrink-0"
                            aria-label={t("pages.routeTemplates.editor.templateWeight", { id })}
                            value={Number.isNaN(weight) ? "" : weight}
                            onChange={(e) =>
                              onChange(
                                rules.map((r, i) =>
                                  i !== index
                                    ? r
                                    : {
                                        ...r,
                                        allocations: [
                                          ...r.allocations!.filter((a) => a.deployment_id !== id),
                                          {
                                            deployment_id: id,
                                            weight: e.target.value === "" ? NaN : Number(e.target.value),
                                          },
                                        ],
                                      },
                                ),
                              )
                            }
                          />
                        </div>
                      </div>
                    ))}
                  </>
                ) : (
                  <p className="text-sm text-muted-foreground">{t("pages.routeTemplates.editor.inheritHint")}</p>
                )}
              </div>
            )}
          </section>
        );
      })}
      {rules.length === 0 && (
        <p className="rounded-md border border-dashed p-4 text-sm text-muted-foreground">
          {t("pages.routeTemplates.editor.emptyRules")}
        </p>
      )}
    </section>
  );
}
