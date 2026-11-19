"use client";

import { useEffect, useState } from "react";
import { useFieldArray, useWatch, type UseFormReturn } from "react-hook-form";
import { ChevronDown, CircleAlert, Minus, Plus } from "lucide-react";

import useAuthorized from "@/app/(dashboard)/hooks/useAuthorized";
import { ALL_TEAM_MODELS, type ProjectFormValues, type ProjectSubmitValues } from "./projectFormSchema";
import { useTeams } from "@/app/(dashboard)/hooks/teams/useTeams";
import { Team } from "@/components/key_team_helpers/key_list";
import { fetchTeamModels } from "@/components/organisms/create_key_button";
import { getModelDisplayName } from "@/components/key_team_helpers/fetch_available_models_team_key";
import { getGuardrailsList } from "@/components/networking";
import { TagsInput } from "@/app/(dashboard)/guardrails/_components/content_filter/TagsInput";
import { Alert, AlertTitle } from "@/components/shared/Alert";
import { SearchSelect } from "@/components/shared/SearchSelect";
import { DirectoryFormSection } from "@/components/shared/DirectoryFormSection";
import { FormField } from "@/components/shared/form/FormField";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupInput, InputGroupText } from "@/components/ui/input-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { useT } from "@/i18n";

const toOptionalNumber = (raw: string): number | undefined => {
  if (raw.trim() === "") return undefined;
  const parsed = Number(raw);
  return Number.isNaN(parsed) ? undefined : parsed;
};

interface ProjectBaseFormProps {
  form: UseFormReturn<ProjectFormValues, unknown, ProjectSubmitValues>;
  advancedOpen: boolean;
  onAdvancedOpenChange: (open: boolean) => void;
}

export type { ProjectFormValues } from "./projectFormSchema";

/** 为新建与编辑项目提供共用字段。form 持有提交值与错误，advancedOpen 和回调控制高级设置；
 * 仅在已选择团队时允许选模型。分组保留字段注册和现有权限、预算提交契约。 */
export function ProjectBaseForm({ form, advancedOpen, onAdvancedOpenChange }: ProjectBaseFormProps) {
  const t = useT();
  const { accessToken, userId, userRole } = useAuthorized();
  const { data: teams } = useTeams();

  const [selectedTeam, setSelectedTeam] = useState<Team | null>(null);
  const [modelsToPick, setModelsToPick] = useState<string[]>([]);
  const [guardrailsList, setGuardrailsList] = useState<string[]>([]);

  const modelLimits = useFieldArray({ control: form.control, name: "modelLimits" });
  const metadata = useFieldArray({ control: form.control, name: "metadata" });
  const emptyModelLimit: NonNullable<ProjectFormValues["modelLimits"]>[number] = {
    model: "",
    tpm: undefined,
    rpm: undefined,
    itpm: undefined,
    otpm: undefined,
  };

  const teamIdValue = useWatch({ control: form.control, name: "team_id" });
  const isBlocked = useWatch({ control: form.control, name: "isBlocked" });

  useEffect(() => {
    const fetchGuardrails = async () => {
      if (!accessToken) return;
      try {
        const response = await getGuardrailsList(accessToken);
        const names = response.guardrails.map((g: { guardrail_name: string }) => g.guardrail_name);
        setGuardrailsList(names);
      } catch (error) {
        console.error("Failed to fetch guardrails:", error);
      }
    };
    fetchGuardrails();
  }, [accessToken]);

  useEffect(() => {
    if (teamIdValue && teams) {
      const team = teams.find((t) => t.team_id === teamIdValue) ?? null;
      if (team && team.team_id !== selectedTeam?.team_id) {
        setSelectedTeam(team);
      }
    }
  }, [teamIdValue, teams, selectedTeam?.team_id]);

  useEffect(() => {
    if (userId && userRole && accessToken && selectedTeam) {
      fetchTeamModels(userId, userRole, accessToken, selectedTeam.team_id).then((models) => {
        const allModels = Array.from(new Set([...(selectedTeam.models ?? []), ...models]));
        setModelsToPick(allModels);
      });
    } else {
      setModelsToPick([]);
    }
  }, [selectedTeam, accessToken, userId, userRole]);

  const handleTeamChange = (teamId: string | null) => {
    const team = teams?.find((t) => t.team_id === teamId) ?? null;
    setSelectedTeam(team);
    form.setValue("models", []);
  };

  const teamOptions = (teams ?? []).map((team) => ({
    value: team.team_id,
    label: team.team_alias || team.team_id,
    sublabel: team.team_id,
  }));

  const modelOptions = [
    { value: ALL_TEAM_MODELS, label: t("All Team Models") },
    ...modelsToPick.map((model) => ({ value: model, label: getModelDisplayName(model) })),
  ];
  const modelsPlaceholder = selectedTeam ? t("Select models") : t("pages.projects.teamFirst");

  return (
    <div className="mt-4 space-y-4">
      <DirectoryFormSection section="identity">
        <div className="grid grid-cols-1 gap-6 sm:grid-cols-2">
          <FormField control={form.control} name="project_alias" label={t("Project Name")}>
            {({ ref, ...field }) => (
              <Input {...field} value={field.value ?? ""} ref={ref} placeholder={t("e.g. Customer Support Bot")} />
            )}
          </FormField>

          <FormField control={form.control} name="team_id" label={t("Team")}>
            {({ id, value, onChange, ref: _ref, ...field }) => (
              <SearchSelect
                {...field}
                inputId={id}
                options={teamOptions}
                value={value}
                onValueChange={(next) => {
                  onChange(next);
                  handleTeamChange(next);
                }}
                placeholder={t("Search or select a team")}
                allowClear
              />
            )}
          </FormField>
        </div>

        <FormField control={form.control} name="description" label={t("Description")}>
          {({ ref, ...field }) => (
            <Textarea
              {...field}
              value={field.value ?? ""}
              ref={ref}
              rows={3}
              placeholder={t("Describe the purpose of this project")}
            />
          )}
        </FormField>

      </DirectoryFormSection>
      <DirectoryFormSection section="access">
        <FormField
          control={form.control}
          name="models"
          label={t("Allowed Models (scoped to selected team's models)")}
        >
          {({ id, value, onChange, "aria-invalid": ariaInvalid, "aria-describedby": ariaDescribedBy }) => (
            <Select
              multiple
              items={modelOptions}
              value={value}
              onValueChange={(next: string[]) => onChange(next.includes(ALL_TEAM_MODELS) ? [ALL_TEAM_MODELS] : next)}
              disabled={!selectedTeam}
            >
              <SelectTrigger id={id} aria-invalid={ariaInvalid} aria-describedby={ariaDescribedBy} className="w-full">
                <SelectValue placeholder={modelsPlaceholder}>
                  {(selected: string[]) =>
                    selected.length === 0
                      ? modelsPlaceholder
                      : modelOptions
                          .filter((option) => selected.includes(option.value))
                          .map((option) => option.label)
                          .join(", ")
                  }
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {modelOptions.map((option) => (
                  <SelectItem key={option.value} value={option.value} title={option.label}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </FormField>

      </DirectoryFormSection>
      <DirectoryFormSection section="limits">
        <FormField control={form.control} name="max_budget" label={t("Max Budget (USD)")}>
          {({ ref, value, onChange, ...field }) => (
            <InputGroup>
              <InputGroupAddon>
                <InputGroupText>$</InputGroupText>
              </InputGroupAddon>
              <InputGroupInput
                {...field}
                ref={ref}
                type="number"
                min={0}
                placeholder="0.00"
                value={Number.isNaN(value) ? "" : value ?? ""}
                onInput={(event) => {
                  if (event.currentTarget.validity.badInput || Number.isNaN(value)) {
                    onChange(
                      event.currentTarget.validity.badInput
                        ? Number.NaN
                        : toOptionalNumber(event.currentTarget.value) ?? null,
                    );
                  }
                }}
                onChange={(event) =>
                  onChange(event.target.validity.badInput ? Number.NaN : toOptionalNumber(event.target.value) ?? null)
                }
              />
            </InputGroup>
          )}
        </FormField>
      </DirectoryFormSection>

      <Collapsible
        open={advancedOpen}
        onOpenChange={onAdvancedOpenChange}
        className="rounded-xl border border-border bg-card"
      >
        <CollapsibleTrigger
          render={
            <button type="button" className="flex w-full items-center gap-2 px-4 py-3 text-left">
              <ChevronDown
                className={`size-4 text-muted-foreground transition-transform ${advancedOpen ? "" : "-rotate-90"}`}
              />
              <span className="text-sm font-semibold text-foreground">{t("Advanced Settings")}</span>
            </button>
          }
        />
        <CollapsibleContent className="px-4 pb-4">
          <FormField control={form.control} name="isBlocked" label={t("Block Project")}>
            {({ id, value, onChange, ref: _ref, ...field }) => (
              <Switch {...field} id={id} checked={value} onCheckedChange={onChange} />
            )}
          </FormField>

          {isBlocked ? (
            <Alert variant="warning" className="mt-3">
              <CircleAlert />
              <AlertTitle>{t("All API requests using keys under this project will be rejected.")}</AlertTitle>
            </Alert>
          ) : null}

          <Separator className="my-4" />

          <FormField
            control={form.control}
            name="guardrails"
            label={t("Guardrails")}
            description={t("Select existing guardrails or enter new ones")}
          >
            {({ id, value, onChange }) => (
              <TagsInput
                id={id}
                value={value ?? []}
                onValueChange={onChange}
                options={guardrailsList.map((name) => ({ label: name, value: name }))}
                placeholder={t("Select or enter guardrails")}
              />
            )}
          </FormField>

          <Separator className="my-4" />

          <p className="mb-3 text-sm font-semibold text-foreground">{t("Model-Specific Limits")}</p>
          {modelLimits.fields.map((field, index) => (
            <div
              key={field.id}
              className="mb-2 grid grid-cols-1 items-start gap-2 sm:grid-cols-2 xl:grid-cols-[minmax(0,2fr)_repeat(4,minmax(0,1fr))_auto]"
            >
              <FormField control={form.control} name={`modelLimits.${index}.model`} label={t("Model")}>
                {({ ref, ...control }) => (
                  <Input {...control} value={control.value ?? ""} ref={ref} placeholder={t("Model name (e.g. gpt-4)")} />
                )}
              </FormField>
              <FormField control={form.control} name={`modelLimits.${index}.tpm`} label={t("TPM Limit")}>
                {({ ref, value, onChange, ...control }) => (
                  <Input
                    {...control}
                    ref={ref}
                    type="number"
                    min={0}
                    placeholder={t("TPM Limit")}
                    value={value ?? ""}
                    onChange={(event) => onChange(toOptionalNumber(event.target.value))}
                  />
                )}
              </FormField>
              <FormField control={form.control} name={`modelLimits.${index}.rpm`} label={t("RPM Limit")}>
                {({ ref, value, onChange, ...control }) => (
                  <Input
                    {...control}
                    ref={ref}
                    type="number"
                    min={0}
                    placeholder={t("RPM Limit")}
                    value={value ?? ""}
                    onChange={(event) => onChange(toOptionalNumber(event.target.value))}
                  />
                )}
              </FormField>
              <FormField control={form.control} name={`modelLimits.${index}.itpm`} label={t("Input TPM Limit")}>
                {({ ref, value, onChange, ...control }) => (
                  <Input
                    {...control}
                    ref={ref}
                    type="number"
                    min={0}
                    placeholder={t("Input TPM Limit")}
                    value={value ?? ""}
                    onChange={(event) => onChange(toOptionalNumber(event.target.value))}
                  />
                )}
              </FormField>
              <FormField control={form.control} name={`modelLimits.${index}.otpm`} label={t("Output TPM Limit")}>
                {({ ref, value, onChange, ...control }) => (
                  <Input
                    {...control}
                    ref={ref}
                    type="number"
                    min={0}
                    placeholder={t("Output TPM Limit")}
                    value={value ?? ""}
                    onChange={(event) => onChange(toOptionalNumber(event.target.value))}
                  />
                )}
              </FormField>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                className="mt-1 text-destructive"
                onClick={() => modelLimits.remove(index)}
                aria-label={t("Remove model limit {value0}", { value0: (index + 1) })}
              >
                <Minus />
              </Button>
            </div>
          ))}
          <Button
            type="button"
            variant="outline"
            className="w-full border-dashed"
            onClick={() => modelLimits.append(emptyModelLimit)}
          >
            <Plus />
            {t("Add Model Limit")}
          </Button>

          <Separator className="my-4" />

          <p className="mb-3 text-sm font-semibold text-foreground">{t("Metadata")}</p>
          {metadata.fields.map((field, index) => (
            <div key={field.id} className="mb-2 flex items-start gap-2">
              <FormField control={form.control} name={`metadata.${index}.key`}>
                {({ ref, ...control }) => (
                  <Input {...control} value={control.value ?? ""} ref={ref} placeholder={t("Key")} />
                )}
              </FormField>
              <FormField control={form.control} name={`metadata.${index}.value`}>
                {({ ref, ...control }) => (
                  <Input {...control} value={control.value ?? ""} ref={ref} placeholder={t("Value")} />
                )}
              </FormField>
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                className="mt-1 text-destructive"
                onClick={() => metadata.remove(index)}
                aria-label={t("Remove metadata pair {value0}", { value0: (index + 1) })}
              >
                <Minus />
              </Button>
            </div>
          ))}
          <Button
            type="button"
            variant="outline"
            className="w-full border-dashed"
            onClick={() => metadata.append({ key: "", value: "" })}
          >
            <Plus />
            {t("Add Key-Value Pair")}
          </Button>
        </CollapsibleContent>
      </Collapsible>
    </div>
  );
}
