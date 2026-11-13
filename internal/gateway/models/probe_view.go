package models

import (
	"net/http"

	"github.com/sunqirui1987/xhub/internal/config"
	pd "github.com/sunqirui1987/xhub/internal/provider/deployment"
	"github.com/sunqirui1987/xhub/internal/util"
)

// probeViews 仅为有探测证明的部署复核实时凭据；参数为宿主及请求私有目录，返回去除失效证明的目录。
// 调用模型发现与管理列表，不改变普通模型读取行为、不把水合的密钥带到公开结果；失败时仅撤销额外协议展示。
func probeViews(h Host, list []config.ModelEntry) []config.ModelEntry {
	if batch, ok := h.(interface {
		ProtocolProbeViews([]config.ModelEntry) []config.ModelEntry
	}); ok {
		return batch.ProtocolProbeViews(list)
	}
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
		resolved = pd.ValidatedProbeView(m, resolved)
		copied := config.CloneModels([]config.ModelEntry{m})[0]
		if err != nil || resolved.ModelInfo["protocol_probe"] == nil {
			delete(copied.ModelInfo, "protocol_probe")
		}
		list[i] = copied
	}
	return list
}

// ProbeProtocol 将已注册 HTTP 入口交给宿主的管理员探测服务，权限及真实调用仍由宿主校验。
// 参数 h/w/r 为模型宿主、响应和请求；无返回值。未配置服务时返回 503，不伪造成功或发起外部请求。
func ProbeProtocol(h Host, w http.ResponseWriter, r *http.Request) {
	if runner, ok := h.(interface {
		ProtocolProbe(http.ResponseWriter, *http.Request)
	}); ok {
		runner.ProtocolProbe(w, r)
	} else {
		util.WriteError(w, 503, "protocol_probe_unavailable", "protocol probe unavailable")
	}
}
