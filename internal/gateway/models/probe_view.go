package models

import "github.com/sunqirui1987/xhub/internal/config"

// probeViews 仅为有探测证明的部署复核实时凭据；参数为宿主及请求私有目录，返回去除失效证明的目录。
// 调用模型发现与管理列表，不改变普通模型读取行为、不把水合的密钥带到公开结果；失败时仅撤销额外协议展示。
func probeViews(h Host, list []config.ModelEntry) []config.ModelEntry {
	resolver, ok := h.(interface {
		AttachCredential(config.ModelEntry) (config.ModelEntry, error)
	})
	if !ok {
		return list
	}
	for i, m := range list {
		if m.ModelInfo["protocol_probe"] == nil {
			continue
		}
		resolved, err := resolver.AttachCredential(m)
		copied := config.CloneModels([]config.ModelEntry{m})[0]
		if err != nil || resolved.ModelInfo["protocol_probe"] == nil {
			delete(copied.ModelInfo, "protocol_probe")
		}
		list[i] = copied
	}
	return list
}
