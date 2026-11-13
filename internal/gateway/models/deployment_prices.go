package models

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"sort"
	"time"

	"github.com/sunqirui1987/xhub/internal/catalog"
	"github.com/sunqirui1987/xhub/internal/config"
	"github.com/sunqirui1987/xhub/internal/util"
)

// deploymentPrice 描述一个部署的当前费率和市场候选，不返回凭据或上游连接秘密。
type deploymentPrice struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Base     string         `json:"base_model"`
	Manual   bool           `json:"manual"`
	Current  []catalog.Rate `json:"current"`
	Proposed []catalog.Rate `json:"proposed"`
	Changed  bool           `json:"changed"`
	Reason   string         `json:"reason"`
	Version  string         `json:"version"`
}

// priceVersion 对部署和候选生成不可逆版本号；参数可以含秘密但输出不含原文。
// 预览与提交共同使用；配置或价格发生变化时必须重新确认，无持久化副作用。
func priceVersion(values ...any) string {
	raw, _ := json.Marshal(values)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// effectiveRates 按已有计价优先级提取价格，参数为部署；返回费率，不修改计费规则。
// 显式费率优先，再读平铺单价，最后读已接受目录；手工缺价不回落市场。
func effectiveRates(dep config.ModelEntry) []catalog.Rate {
	if rates, ok := catalog.DecodeRates(dep.LiteLLMParams["rates"]); ok {
		return rates
	}
	rates := catalog.RatesFromFlat(func(key string) (float64, bool) { return numberField(dep.LiteLLMParams[key]) })
	if len(rates) > 0 || dep.ModelInfo["pricing_source"] == "manual" {
		return rates
	}
	id := str(dep.ModelInfo["base_model"])
	if id == "" {
		id = dep.ParamString("model", dep.ModelName)
	}
	row, ok := catalog.ModelRow(id)
	if !ok {
		row, _ = catalog.ModelRow(dep.ModelName)
	}
	if rates, ok := catalog.DecodeRates(row["rates"]); ok {
		return rates
	}
	return catalog.RatesFromFlat(func(key string) (float64, bool) { return numberField(row[key]) })
}

// pricePreview 比较部署现价和指定市场快照，返回可确认状态及版本号。
// 来源下架、无明确绑定或复杂计价只允许查看，不能快捷覆盖；YAML 部署由配置文件维护。
func pricePreview(dep config.ModelEntry, doc catalog.PriceDocument) deploymentPrice {
	p := deploymentPrice{ID: str(dep.ModelInfo["id"]), Name: dep.ModelName, Base: str(dep.ModelInfo["base_model"]), Manual: dep.ModelInfo["pricing_source"] == "manual", Current: effectiveRates(dep)}
	row, found := doc.Models[p.Base]
	switch {
	case !modelIsDB(dep):
		p.Reason = "config_model"
	case !found:
		p.Reason = "unmapped"
	case row["feed_unavailable"] == true || row["private"] == true:
		p.Reason = "delisted"
	default:
		if retired, err := time.Parse(time.RFC3339, str(row["retirement_at"])); err == nil && !retired.After(time.Now()) {
			p.Reason = "delisted"
		} else {
			p.Proposed, p.Reason = catalog.MarketRates(row)
		}
	}
	p.Changed = p.Reason == "" && !samePrices(p.Current, p.Proposed)
	p.Version = priceVersion(dep, p.Current, p.Proposed, p.Reason)
	return p
}

// samePrices 仅比较实际计价维度、金额及单位；忽略展示标签变化和数组顺序。
// 参数为两份费率，返回是否等价；用于调价预览，无副作用。
func samePrices(a, b []catalog.Rate) bool {
	clean := func(in []catalog.Rate) []catalog.Rate {
		out := append([]catalog.Rate(nil), in...)
		for i := range out {
			out[i].Label = ""
			out[i].SourceKey = ""
			out[i].UnitSize = 1
		}
		sort.Slice(out, func(i, j int) bool { return priceVersion(out[i]) < priceVersion(out[j]) })
		return out
	}
	return reflect.DeepEqual(clean(a), clean(b))
}

// DeploymentPrices 返回管理权限可见的部署价格对比；输入宿主与 HTTP 上下文，输出快照。
// 使用实际数据库部署而不是可能过时的表单；存储故障返回 500，未授权由既有权限层拒绝。
func DeploymentPrices(s Host, w http.ResponseWriter, r *http.Request) {
	if s.RequireManage(w, r) == nil {
		return
	}
	if s.RecordStore() == nil {
		util.WriteError(w, 503, "unavailable", "model store unavailable")
		return
	}
	doc, err := readMarket(r.Context(), s)
	if err != nil {
		util.WriteError(w, 500, "internal", "market catalog unavailable")
		return
	}
	rows, err := s.RecordStore().ListProxyModels()
	if err != nil {
		util.WriteError(w, 500, "internal", "model store unavailable")
		return
	}
	out := make([]deploymentPrice, 0, len(rows))
	for _, row := range rows {
		info := maps.Clone(row.Info)
		if info == nil {
			info = map[string]any{}
		}
		info["id"] = row.ID
		info["db_model"] = true
		out = append(out, pricePreview(config.ModelEntry{ModelName: row.ModelName, LiteLLMParams: row.Params, ModelInfo: info}, doc))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	util.WriteJSON(w, 200, map[string]any{"deployments": out, "generated_at": doc.GeneratedAt})
}

var errPriceConflict = errors.New("price or deployment changed; reload preview")

// ApplyDeploymentPrices 在管理员确认后原子应用选定价格；body 仅接受 ID 和预览版本，绝不信任客户端金额。
// 输入宿主和 HTTP 请求，返回更新数量；任何冲突/不可用条目使整批回滚，权限预算和其他部署字段不变。
func ApplyDeploymentPrices(s Host, w http.ResponseWriter, r *http.Request) {
	if s.RequireManage(w, r) == nil {
		return
	}
	var body struct {
		Items []struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"items"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body) != nil || len(body.Items) == 0 || len(body.Items) > 500 {
		util.WriteError(w, 400, "invalid_request", "select 1 to 500 deployments")
		return
	}
	seen := map[string]bool{}
	for _, item := range body.Items {
		if item.ID == "" || item.Version == "" || seen[item.ID] {
			util.WriteError(w, 400, "invalid_request", "unique deployment IDs and versions required")
			return
		}
		seen[item.ID] = true
	}
	if s.RecordStore() == nil {
		util.WriteError(w, 503, "unavailable", "model store unavailable")
		return
	}
	s.LockModels()
	defer s.UnlockModels()
	changed := map[string]config.ModelEntry{}
	err := s.RecordStore().MarketTransaction(r.Context(), func(tx *sql.Tx) error {
		doc, err := marketDocument(r.Context(), tx)
		if err != nil {
			return err
		}
		sort.Slice(body.Items, func(i, j int) bool { return body.Items[i].ID < body.Items[j].ID })
		for _, item := range body.Items {
			var name, paramsRaw, infoRaw string
			err = tx.QueryRowContext(r.Context(), `SELECT model_name,litellm_params_json,model_info_json FROM proxy_models WHERE id=$1 FOR UPDATE`, item.ID).Scan(&name, &paramsRaw, &infoRaw)
			if errors.Is(err, sql.ErrNoRows) {
				return errPriceConflict
			}
			if err != nil {
				return err
			}
			dep := config.ModelEntry{ModelName: name}
			if err = json.Unmarshal([]byte(paramsRaw), &dep.LiteLLMParams); err != nil {
				return err
			}
			if err = json.Unmarshal([]byte(infoRaw), &dep.ModelInfo); err != nil {
				return err
			}
			if dep.ModelInfo == nil {
				dep.ModelInfo = map[string]any{}
			}
			dep.ModelInfo["id"] = item.ID
			dep.ModelInfo["db_model"] = true
			preview := pricePreview(dep, doc)
			if preview.Version != item.Version || preview.Reason != "" {
				return errPriceConflict
			}
			if dep.LiteLLMParams == nil {
				dep.LiteLLMParams = map[string]any{}
			}
			dep.LiteLLMParams["rates"] = preview.Proposed
			// 平铺旧价清除，避免管理表单显示另一套数字；全部候选维度保存为费率表。
			for _, field := range rateFields {
				delete(dep.LiteLLMParams, field)
			}
			dep.ModelInfo["market_price_confirmed_at"] = time.Now().UTC().Format(time.RFC3339)
			dep.ModelInfo["market_price_revision"] = priceVersion(preview.Proposed)
			params, _ := json.Marshal(dep.LiteLLMParams)
			info, _ := json.Marshal(dep.ModelInfo)
			if _, err = tx.ExecContext(r.Context(), `UPDATE proxy_models SET litellm_params_json=$1, model_info_json=$2,updated_at=$3 WHERE id=$4`, string(params), string(info), time.Now().UTC().Format(time.RFC3339), item.ID); err != nil {
				return err
			}
			changed[item.ID] = dep
		}
		return nil
	})
	if errors.Is(err, errPriceConflict) {
		util.WriteError(w, 409, "price_conflict", err.Error())
		return
	}
	if err != nil {
		util.WriteError(w, 500, "internal", "price update failed")
		return
	}
	list := append([]config.ModelEntry(nil), (*s.ModelTable())...)
	for i, dep := range list {
		if newer, ok := changed[str(dep.ModelInfo["id"])]; ok {
			list[i] = newer
		}
	}
	*s.ModelTable() = list
	util.WriteJSON(w, 200, map[string]any{"updated": len(changed)})
}

// bindInitialMarketPrice 在管理员创建目录定价部署时保存当前市场费率快照。
// 参数为上下文、宿主和待创建字段；返回校验错误，旧手工目录沿用原规则，不改现有部署。
func bindInitialMarketPrice(ctx context.Context, s Host, params, info map[string]any) error {
	id := str(info["base_model"])
	if id == "" || info["pricing_source"] == "manual" {
		return nil
	}
	doc, err := readMarket(ctx, s)
	if err != nil {
		return err
	}
	row, ok := doc.Models[id]
	if !ok {
		// 人工维护的本地目录不属于市场快照，沿用已有计价目录的创建行为。
		if _, exists := catalog.ModelRow(id); exists {
			return nil
		}
		return errors.New("pricing model does not exist")
	}
	// 旧目录条目没有完整市场规则时，保留既有手工配置契约。
	if row["pricing_rules_v2"] == nil {
		return nil
	}
	rates, reason := catalog.MarketRates(row)
	if reason != "" {
		// 已有复杂目录维持原创建契约；新来源型号必须人工配置，不能猜取首档价格。
		if _, exists := catalog.ModelRow(id); exists {
			return nil
		}
		return errors.New("market pricing requires manual configuration")
	}
	params["rates"] = rates
	info["market_price_revision"] = priceVersion(rates)
	return nil
}
