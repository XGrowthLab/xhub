package deployment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"strings"

	"github.com/sunqirui1987/xhub/internal/config"
	pc "github.com/sunqirui1987/xhub/internal/provider/contract"
	"github.com/sunqirui1987/xhub/internal/provider/registry"
)

// ProbeSignature 绑定影响原生出站的型号、地址、凭据和所选传输；参数为原始或水合部署，返回不可逆摘要。
// 价格、公开别名及展示字段不触发额外供应商调用；只在服务端保存，不向页面输出摘要。
func ProbeSignature(m config.ModelEntry) string {
	values := map[string]any{"schema_version": 1, "transport": m.ModelInfo["transport"], "catalog_id": m.ModelInfo["catalog_id"]}
	for _, k := range []string{"model", "api_base", "api_key", "custom_llm_provider", "litellm_credential_name"} {
		values[k] = m.ParamString(k, "")
	}
	b, _ := json.Marshal(values)
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}

// ProbeRecord 读取服务器写入的探测记录；参数部署，返回记录或 nil；不修改输入。
func ProbeRecord(m config.ModelEntry) map[string]any {
	v, _ := m.ModelInfo["protocol_probe"].(map[string]any)
	return v
}

// ValidatedProbeView 在解析实际凭据后清除失效验证；参数 raw/attached 分别是保存值与解析结果。
// 返回新模型元数据副本，供 UI 目录和出站前复核；地址、密钥、型号改变后立即拒绝旧验证，不依赖后台扫描时间。
func ValidatedProbeView(raw, attached config.ModelEntry) config.ModelEntry {
	record := ProbeRecord(raw)
	if record == nil {
		return attached
	}
	if record["config_signature"] == ProbeSignature(raw) && record["connection_signature"] == ProbeSignature(attached) {
		return attached
	}
	attached.ModelInfo = maps.Clone(attached.ModelInfo)
	delete(attached.ModelInfo, "protocol_probe")
	return attached
}

// verifiedTransport 只补充当前连接已通过整套检查的三种对话透传执行器；不改变普通接口选用的 transport。
// 参数部署与入口 ID，返回额外执行器和是否可作为候选。原始签名用于无密钥目录的候选筛选；实际出站必须先用 ValidatedProbeView 同时校验原始与水合签名。
// UI 目录也先复核凭据；禁止客户端写入 protocol_probe，不能把此函数单独当作凭据验证。
func verifiedTransport(m config.ModelEntry, endpoint string) (pc.Transport, bool) {
	record := ProbeRecord(m)
	if record == nil || !strings.HasPrefix(endpoint, "bypass:") {
		return pc.Transport{}, false
	}
	sig := ProbeSignature(m)
	if record["config_signature"] != sig && record["connection_signature"] != sig {
		return pc.Transport{}, false
	}
	valid, _ := record["verified"].(map[string]any)
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic-messages"} {
		if endpoint != "bypass:"+protocol || valid[protocol] != true {
			continue
		}
		for _, t := range registry.Transports() {
			if t.EndpointID == endpoint && t.Protocol == protocol {
				return t, true
			}
		}
	}
	return pc.Transport{}, false
}
