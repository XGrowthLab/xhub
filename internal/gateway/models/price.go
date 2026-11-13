// Package models manages the price catalog: the embedded price baseline plus
// the rows and suppliers an operator adds from the console. The stored overrides
// are laid over the embedded file at startup, so editing a price never rewrites
// the generated document.
package models

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/sunqirui1987/xhub/internal/catalog"
	"github.com/sunqirui1987/xhub/internal/util"
)

const (
	// priceModelNS holds one row per hand-entered or edited model, keyed by id.
	priceModelNS    = "price_model"
	priceSnapshotNS = "price_snapshot"
	priceListingNS  = "price_listing"
	// priceProviderNS holds one row per hand-added supplier, keyed by slug.
	priceProviderNS = "price_provider"
	// priceRemovedProviderNS 保存内置供应商的当前删除状态，防止重启后重新出现。
	priceRemovedProviderNS = "price_removed_provider"
)

var logTraceOncePrice sync.Once

// rateFields are the per-token rates the console edits. They are stored per
// token, which is the unit the gateway bills in.
var rateFields = []string{
	"input_cost_per_token",
	"output_cost_per_token",
	"input_cost_per_token_peak",
	"output_cost_per_token_peak",
	"cache_read_input_token_cost",
	"cache_creation_input_token_cost",
	"input_cost_per_image",
	"output_cost_per_image",
	"input_cost_per_second",
	"output_cost_per_second",
	"search_context_cost_per_query",
}

// textFields are the descriptive fields carried on a price row.
var textFields = []string{
	"display_name",
	"description",
	"upstream_model",
	"supplier_name",
	"mode",
	// 控制台价格下拉只写入当前执行类型 ID，不接受旧手工字段别名。
	"endpoint_id",
	"source",
	"max_input_tokens",
	"max_output_tokens",
	"avatar",
	"pricing_page_url",
	"retirement_at",
	"release_at",
}

// flagFields are the capability booleans the console shows as badges.
var flagFields = []string{
	"supports_function_calling",
	"supports_response_schema",
	"supports_reasoning",
	"supports_vision",
	"supports_prompt_caching",
}

// LoadPriceOverrides applies the stored catalog edits over the embedded
// baseline. It runs at startup, before the process serves traffic.
// 参数 s（Host）：能读框架记录和模型表的宿主。
// 返回：无。把库里保存的价格改动并进内存价格表。
// 调用：gateway.New。
// 测试：无直接单测
func LoadPriceOverrides(s Host) {
	logTraceOncePrice.Do(func() { util.Trace("enter models.LoadPriceOverrides") })

	st := s.RecordStore()
	if st == nil {
		return
	}
	// 启动恢复已接受计费基线，再叠加手工价格；独立市场快照不能自动调价。
	if saved, err := st.ListConfig(priceSnapshotNS); err == nil {
		if raw, ok := saved["catalog"]; ok {
			bytes, _ := json.Marshal(raw)
			var doc catalog.PriceDocument
			if json.Unmarshal(bytes, &doc) == nil {
				_, _ = catalog.ApplyDocument(doc)
			}
		}
	}
	rows, err := st.ListConfig(priceModelNS)
	if err != nil {
		util.Error("price overrides unreadable: %v", err)
		return
	}
	for id, raw := range rows {
		if row, ok := raw.(map[string]any); ok {
			// 价格覆盖不能带回旧可售状态、价格档位或能力；这些字段始终取最新市场基线。
			merged := map[string]any{}
			for key, value := range row {
				merged[key] = value
			}
			if base, ok := catalog.BaselineModel(id); ok {
				for _, key := range []string{"market_catalog", "private", "feed_unavailable", "retirement_at", "pricing_rules_v2", "input_modalities", "output_modalities", "architecture", "rank", "features", "hot_tags", "support_api_protocols", "model_doc_url", "integration_doc_url"} {
					delete(merged, key)
					if value, exists := base[key]; exists {
						merged[key] = value
					}
				}
			}
			catalog.SetModel(id, merged)
		}
	}
	providers, err := st.ListConfig(priceProviderNS)
	if err != nil {
		util.Error("price providers unreadable: %v", err)
		return
	}
	for slug, raw := range providers {
		if row, ok := raw.(map[string]any); ok {
			catalog.SetProvider(slug, row)
		}
	}
	// A hand-added supplier that the operator removed is not stored again, so
	// only the embedded ones need the provider tombstone.
	if removed, err := st.ListConfig(priceRemovedProviderNS); err == nil {
		for slug, raw := range removed {
			if gone, ok := raw.(bool); ok && gone {
				catalog.RemoveProvider(slug)
			}
		}
	}
}

// PriceList 返回管理员模型广场资料、独立下架状态和本地价格覆盖；参数为宿主及 HTTP 上下文。
// 每日市场快照不写回计费目录，人工新增项始终保留；数据库读取失败返回 500。
// 调用：GET /price/catalog；真实来源刷新、人工编辑和下架流程由 regression/E2E 覆盖。
func PriceList(s Host, w http.ResponseWriter, r *http.Request) {
	if s.RequireManage(w, r) == nil {
		return
	}
	doc, err := readMarket(r.Context(), s)
	if err != nil {
		util.WriteError(w, 500, "internal", "market catalog unavailable")
		return
	}
	rows := doc.Models
	for id, row := range catalog.CostMap() {
		if _, ok := rows[id]; !ok {
			rows[id] = row
		}
	}
	overrides := storedKeys(s, priceModelNS)
	listings := map[string]any{}
	if s.RecordStore() != nil {
		listings, _ = s.RecordStore().ListConfig(priceListingNS)
	}
	models := make([]map[string]any, 0, len(rows))
	for id, row := range rows {
		entry := make(map[string]any, len(row)+3)
		for k, v := range row {
			entry[k] = v
		}
		// 人工价格仍供编辑器读取；完整市场规则保持来源数据，快捷调价直接读独立快照。
		if overrides[id] {
			if local, ok := catalog.ModelRow(id); ok {
				for _, key := range append(append([]string{}, rateFields...), "rates") {
					delete(entry, key)
					if value, exists := local[key]; exists {
						entry[key] = value
					}
				}
			}
		}
		entry["id"] = id
		entry["baseline"] = catalog.IsBaseline(id)
		entry["overridden"] = overrides[id]
		entry["delisted"] = listings[id] == true
		models = append(models, entry)
	}
	sort.Slice(models, func(i, j int) bool {
		return str(models[i]["id"]) < str(models[j]["id"])
	})

	providerOverrides := storedKeys(s, priceProviderNS)
	byProvider := map[string]map[string]any{}
	for _, p := range doc.Providers {
		byProvider[str(p["litellm_provider"])] = p
	}
	for _, p := range catalog.Providers() {
		slug := str(p["litellm_provider"])
		if _, exists := byProvider[slug]; !exists || providerOverrides[slug] {
			byProvider[slug] = p
		}
	}
	removedProviders := storedKeys(s, priceRemovedProviderNS)
	out := make([]map[string]any, 0, len(byProvider))
	for _, p := range byProvider {
		slug := str(p["litellm_provider"])
		if removedProviders[slug] {
			continue
		}
		entry := make(map[string]any, len(p)+2)
		for k, v := range p {
			entry[k] = v
		}
		entry["baseline"] = catalog.IsBaselineProvider(slug)
		entry["overridden"] = providerOverrides[slug]
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		return str(out[i]["litellm_provider"]) < str(out[j]["litellm_provider"])
	})

	util.WriteJSON(w, 200, map[string]any{
		"models":       models,
		"providers":    out,
		"source":       doc.Source,
		"generated_at": doc.GeneratedAt,
		"loaded_at":    marketLoadedAt(s),
		"count":        len(models),
	})
}

// UpsertPriceModel adds or edits one model in the price catalog. A row that the
// embedded file already has is overridden, not duplicated.
// 参数 s（Host）：能写框架记录的宿主；w（http.ResponseWriter）：响应写到这；r（*http.Request）：入站请求。
// 返回：无。状态码和正文写进调用方的响应。
// 调用：gateway/models/mount.go
// 测试：无直接单测
func UpsertPriceModel(s Host, w http.ResponseWriter, r *http.Request) {
	if s.RequireManage(w, r) == nil {
		return
	}
	body := readMap(r)
	id := strings.TrimSpace(str(body["id"]))
	if id == "" {
		util.WriteError(w, 400, "invalid_request", "id is required")
		return
	}
	provider := strings.TrimSpace(str(body["litellm_provider"]))
	if provider == "" {
		util.WriteError(w, 400, "invalid_request", "litellm_provider is required")
		return
	}
	row := priceRowFrom(body, provider)
	for _, field := range rateFields {
		v, exists := body[field]
		if !exists || v == nil {
			continue
		}
		if text, ok := v.(string); ok && strings.TrimSpace(text) == "" {
			continue
		}
		if n, valid := numberField(v); !valid || n < 0 {
			util.WriteError(w, 400, "invalid_request", field+" must be a finite non-negative number")
			return
		}
	}

	if err := s.RecordStore().PutConfig(priceModelNS, id, row); err != nil {
		util.WriteError(w, 500, "internal", err.Error())
		return
	}
	catalog.SetModel(id, row)
	util.WriteJSON(w, 200, map[string]any{"status": "success", "id": id})
}

// SetPriceListing 设置本地模型上下架状态；参数为宿主和HTTP请求响应，正文含id及delisted布尔值。
// 返回HTTP结果；管理列表调用。只改变广场展示，不删除价格或禁用已部署接口。
// 未知模型404，错误输入400，数据库写入失败500，无价格清理副作用。
func SetPriceListing(s Host, w http.ResponseWriter, r *http.Request) {
	if s.RequireManage(w, r) == nil {
		return
	}
	body := readMap(r)
	id := strings.TrimSpace(str(body["id"]))
	delisted, valid := body["delisted"].(bool)
	if id == "" || !valid {
		util.WriteError(w, 400, "invalid_request", "id and boolean delisted are required")
		return
	}
	doc, err := readMarket(r.Context(), s)
	if err != nil {
		util.WriteError(w, 500, "internal", "market catalog unavailable")
		return
	}
	if _, ok := doc.Models[id]; !ok {
		// 首次同步后新增的人工目录不在来源快照中，仍应允许管理员隐藏或恢复。
		if _, local := catalog.ModelRow(id); !local {
			util.WriteError(w, 404, "not_found", "model is not in the local catalog")
			return
		}
	}
	if s.RecordStore() == nil {
		util.WriteError(w, 500, "internal", "local catalog store unavailable")
		return
	}
	if err := s.RecordStore().PutConfig(priceListingNS, id, delisted); err != nil {
		util.WriteError(w, 500, "internal", err.Error())
		return
	}
	util.WriteJSON(w, 200, map[string]any{"status": "success", "id": id, "delisted": delisted})
}

// ResetPriceModel drops an override and restores the embedded row.
// 参数 s（Host）：能写框架记录的宿主；w（http.ResponseWriter）：响应写到这；r（*http.Request）：入站请求。
// 返回：无。状态码和正文写进调用方的响应。
// 调用：gateway/models/mount.go
// 测试：无直接单测
func ResetPriceModel(s Host, w http.ResponseWriter, r *http.Request) {
	if s.RequireManage(w, r) == nil {
		return
	}
	id := strings.TrimSpace(str(readMap(r)["id"]))
	if id == "" {
		util.WriteError(w, 400, "invalid_request", "id is required")
		return
	}
	row, ok := catalog.BaselineModel(id)
	if !ok {
		util.WriteError(w, 409, "no_baseline", "custom models have no market baseline; delist instead")
		return
	}
	if s.RecordStore() == nil {
		util.WriteError(w, 500, "internal", "local catalog store unavailable")
		return
	}
	if err := s.RecordStore().DeleteConfig(priceModelNS, id); err != nil {
		util.WriteError(w, 500, "internal", err.Error())
		return
	}
	catalog.SetModel(id, row)
	util.WriteJSON(w, 200, map[string]any{"status": "success", "id": id, "restored": true})
}

// UpsertPriceProvider adds or edits one supplier in the add-model dropdown.
// 参数 s（Host）：能写框架记录的宿主；w（http.ResponseWriter）：响应写到这；r（*http.Request）：入站请求。
// 返回：无。状态码和正文写进调用方的响应。
// 调用：gateway/models/mount.go
// 测试：无直接单测
func UpsertPriceProvider(s Host, w http.ResponseWriter, r *http.Request) {
	if s.RequireManage(w, r) == nil {
		return
	}
	body := readMap(r)
	slug := strings.TrimSpace(str(body["litellm_provider"]))
	if slug == "" {
		util.WriteError(w, 400, "invalid_request", "litellm_provider is required")
		return
	}
	row := providerRowFrom(body, slug)
	if err := s.RecordStore().PutConfig(priceProviderNS, slug, row); err != nil {
		util.WriteError(w, 500, "internal", err.Error())
		return
	}
	if err := s.RecordStore().DeleteConfig(priceRemovedProviderNS, slug); err != nil {
		util.Debug("provider tombstone for %s was already absent", slug)
	}
	catalog.SetProvider(slug, row)
	util.WriteJSON(w, 200, map[string]any{"status": "success", "litellm_provider": slug})
}

// DeletePriceProvider removes one supplier from the dropdown.
// 参数 s（Host）：能写框架记录的宿主；w（http.ResponseWriter）：响应写到这；r（*http.Request）：入站请求。
// 返回：无。状态码和正文写进调用方的响应。
// 调用：gateway/models/mount.go
// 测试：无直接单测
func DeletePriceProvider(s Host, w http.ResponseWriter, r *http.Request) {
	if s.RequireManage(w, r) == nil {
		return
	}
	slug := strings.TrimSpace(str(readMap(r)["litellm_provider"]))
	if slug == "" {
		util.WriteError(w, 400, "invalid_request", "litellm_provider is required")
		return
	}
	if catalog.IsBaselineProvider(slug) {
		if err := s.RecordStore().PutConfig(priceRemovedProviderNS, slug, true); err != nil {
			util.WriteError(w, 500, "internal", err.Error())
			return
		}
	}
	if err := s.RecordStore().DeleteConfig(priceProviderNS, slug); err != nil {
		util.Debug("price provider %s was already absent", slug)
	}
	catalog.RemoveProvider(slug)
	util.WriteJSON(w, 200, map[string]any{"status": "success", "litellm_provider": slug})
}

// storedKeys reads which ids have a row in a namespace. A nil store reads as an
// empty set, which is the same answer as an empty table.
// 参数 s（Host）：能读框架记录的宿主；namespace（string）：要读的命名空间。
// 返回 map[string]bool（map[string]bool）：该命名空间里出现过的键。读不到时为空表。
// 调用：PriceList 标记哪些行是人工覆盖的。
// 测试：无直接单测
func storedKeys(s Host, namespace string) map[string]bool {
	if s.RecordStore() == nil {
		return map[string]bool{}
	}
	cfg, err := s.RecordStore().ListConfig(namespace)
	if err != nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(cfg))
	for key := range cfg {
		out[key] = true
	}
	return out
}

// priceRowFrom builds a price row from the request, keeping only known fields.
// A rate that is absent stays absent so an unpriced model is never billed as
// free; a rate that is present is taken per token exactly as sent.
// 参数 body（map[string]any）：请求正文；provider（string）：这一行归属的供应商标识。
// 返回 map[string]any（map[string]any）：写进价格表的一行。
// 调用：UpsertPriceModel
// 测试：无直接单测
func priceRowFrom(body map[string]any, provider string) map[string]any {
	row := map[string]any{"litellm_provider": provider}
	// Start from whatever is live so editing an override twice keeps the fields
	// the form did not send. The baseline is only the fallback for a first edit.
	if prior, ok := catalog.ModelRow(str(body["id"])); ok {
		for k, v := range prior {
			row[k] = v
		}
		row["litellm_provider"] = provider
	} else if fromFile, ok := catalog.BaselineModel(str(body["id"])); ok {
		for k, v := range fromFile {
			row[k] = v
		}
		row["litellm_provider"] = provider
	}
	for _, field := range textFields {
		if v, ok := body[field]; ok {
			row[field] = v
		}
	}
	for _, field := range rateFields {
		v, ok := body[field]
		if !ok {
			continue
		}
		if n, ok := numberField(v); ok && n >= 0 {
			row[field] = n
			continue
		}
		// A cleared rate is removed rather than set to zero, so the row stops
		// being billable on that side instead of billing nothing.
		delete(row, field)
	}
	reconcilePriceRates(row, body)
	for _, field := range flagFields {
		if v, ok := body[field].(bool); ok {
			row[field] = v
		}
	}
	return row
}

// providerRowFrom builds a dropdown entry from the request. The credential
// fields are the two every supplier needs: an API base and an API key.
// 参数 body（map[string]any）：请求正文；slug（string）：供应商标识。
// 返回 map[string]any（map[string]any）：写进下拉列表的一项。
// 调用：UpsertPriceProvider
// 测试：无直接单测
func providerRowFrom(body map[string]any, slug string) map[string]any {
	display := strings.TrimSpace(str(body["provider_display_name"]))
	if display == "" {
		display = strings.TrimSpace(str(body["provider"]))
	}
	if display == "" {
		display = slug
	}
	apiBase := strings.TrimSpace(str(body["default_api_base"]))
	fields := []any{
		providerField("api_base", "API Base", "text", false, apiBase),
		providerField("api_key", display+" API Key", "password", true, ""),
	}
	if raw, ok := body["credential_fields"].([]any); ok && len(raw) > 0 {
		fields = raw
	}
	return map[string]any{
		"provider":                  display,
		"provider_display_name":     display,
		"litellm_provider":          slug,
		"credential_fields":         fields,
		"default_model_placeholder": str(body["default_model_placeholder"]),
		"default_api_base":          apiBase,
	}
}

// providerField is one credential input on a supplier entry.
// 参数 key（string）：字段名；label（string）：控制台显示的标题；kind（string）：输入类型；required（bool）：是否必填；def（string）：默认值。
// 返回 map[string]any（map[string]any）：一项凭据字段。
// 调用：providerRowFrom
// 测试：无直接单测
func providerField(key, label, kind string, required bool, def string) map[string]any {
	var defaultValue any
	if def != "" {
		defaultValue = def
	}
	return map[string]any{
		"key":           key,
		"label":         label,
		"placeholder":   nil,
		"tooltip":       nil,
		"required":      required,
		"field_type":    kind,
		"options":       nil,
		"default_value": defaultValue,
	}
}

// numberField reads a JSON number. A numeric string is accepted because the
// console posts form values as text.
// 参数 v（any）：从 JSON 里读到的值。控制台把表单数字当成文本提交，所以字符串也要能解析。
// 返回 float64（float64）：解析出的数。不合法时为 0，同时布尔值为假；bool（bool）：值是数字或数字字符串时为真。空串和其他类型为假。
// 调用：读取控制台提交的价格字段。
// 测试：无直接单测
func numberField(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return 0, false
		}
		out, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return out, !math.IsNaN(out) && !math.IsInf(out, 0)
	default:
		return 0, false
	}
}

// Editing flat prices replaces their unqualified rate entries, while preserving
// catalog variants (resolution, thinking, batch) the flat form cannot express.
// 参数 row（map[string]any）：已合并旧目录行和平面字段的新行；patch（map[string]any）：本次请求实际提交的字段。
// 返回：无。只替换 patch 触及的平面费率，并在 peak 变化时修正对应基础费率的窗口。
// 调用：priceRowFrom。
// 测试：price_test.go。
func reconcilePriceRates(row, patch map[string]any) {
	prior, ok := catalog.DecodeRates(row["rates"])
	if !ok {
		return
	}
	type flatSpec struct {
		measure, side, variant, window string
	}
	specs := map[string]flatSpec{
		"input_cost_per_token":            {"token", "input", "uncached", "offpeak"},
		"output_cost_per_token":           {"token", "output", "", "offpeak"},
		"input_cost_per_token_peak":       {"token", "input", "uncached", "peak"},
		"output_cost_per_token_peak":      {"token", "output", "", "peak"},
		"cache_read_input_token_cost":     {"token", "cache_read", "", "all"},
		"cache_creation_input_token_cost": {"token", "cache_write", "", "all"},
		"input_cost_per_image":            {"picture", "input", "", "all"},
		"output_cost_per_image":           {"picture", "output", "", "all"},
		"input_cost_per_second":           {"second", "input", "", "all"},
		"output_cost_per_second":          {"second", "output", "", "all"},
		"search_context_cost_per_query":   {"query", "output", "", "all"},
	}
	touched := map[string]flatSpec{}
	for field, spec := range specs {
		if _, exists := patch[field]; exists {
			touched[field] = spec
		}
	}
	if len(touched) == 0 {
		return
	}

	rates := make([]catalog.Rate, 0, len(prior)+len(touched))
	for _, rate := range prior {
		replace := false
		for _, spec := range touched {
			if rate.Measure != spec.measure || rate.Side != spec.side || rate.Variant != spec.variant {
				continue
			}
			if rate.Window == spec.window || (spec.window == "offpeak" && rate.Window == "all") {
				replace = true
				break
			}
		}
		if !replace {
			rates = append(rates, rate)
		}
	}
	for field, spec := range touched {
		usd, valid := numberField(row[field])
		if !valid || usd < 0 {
			continue
		}
		rates = append(rates, catalog.Rate{
			Measure: spec.measure, UnitSize: 1, Side: spec.side, Variant: spec.variant,
			Window: spec.window, SourceKey: field, USD: usd,
		})
	}

	// A base token price applies all day unless the same side still has a peak
	// price. Adding or clearing only the peak field must therefore adjust the
	// base window without removing any other window or qualified variant.
	for _, side := range []struct{ side, variant string }{{"input", "uncached"}, {"output", ""}} {
		hasPeak := false
		for _, rate := range rates {
			if rate.Measure == "token" && rate.Side == side.side && rate.Variant == side.variant && rate.Window == "peak" {
				hasPeak = true
				break
			}
		}
		for i := range rates {
			rate := &rates[i]
			if rate.Measure != "token" || rate.Side != side.side || rate.Variant != side.variant {
				continue
			}
			if hasPeak && rate.Window == "all" {
				rate.Window = "offpeak"
			} else if !hasPeak && rate.Window == "offpeak" {
				rate.Window = "all"
			}
		}
	}
	row["rates"] = rates
}
