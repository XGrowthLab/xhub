package execution

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	dpdiagnostics "github.com/sunqirui1987/xhub/internal/dataplane/diagnostics"
	llmdialogue "github.com/sunqirui1987/xhub/internal/llm/dialogue"
	"github.com/sunqirui1987/xhub/internal/util"
)

// writeProtocolValidation 返回入站协议校验错误；source 为登记的入口协议，err 为解析或历史恢复错误。
// 仅在外发前调用。字段未知时保留已有校验说明和阶段，仍记录到调用日志。
func writeProtocolValidation(w http.ResponseWriter, path, source string, err error) {
	field, detail := "", err.Error()
	var fieldError *llmdialogue.RequestFieldError
	if errors.As(err, &fieldError) {
		field, detail = fieldError.Field, fieldError.Message
	}
	problems := []dpdiagnostics.ProtocolProblem{dpdiagnostics.NewProtocolProblem(field, "", detail)}
	writeProtocolRejection(w, path, http.StatusBadRequest, "unsupported_capability", "request_validation", source, "", problems, nil)
}

// writeProtocolRejection 记录并返回外发前的兼容拒绝。problems 至少含一项，来自校验或匹配候选。
// routing 为已有内部快照，只用于日志。状态码及原生错误包络沿用调用方选择。
func writeProtocolRejection(w http.ResponseWriter, path string, status int, kind, stage, source, reason string, problems []dpdiagnostics.ProtocolProblem, routing map[string]any) {
	diagnostic := &dpdiagnostics.ProtocolRejection{Stage: stage, Source: source, Problems: problems}
	dpdiagnostics.RecordEarlyFailure(w, &dpdiagnostics.UpstreamDiagnostics{Protocol: diagnostic, Routing: routing})
	var message strings.Builder
	fmt.Fprintf(&message, "XHub blocked the %s request before sending it upstream", source)
	if reason != "" {
		message.WriteString(". " + reason)
	}
	field := problems[0].Field
	for _, problem := range problems {
		message.WriteString(". ")
		if problem.Target != "" {
			message.WriteString("Target protocol " + problem.Target + ". ")
		}
		if problem.Field != "" {
			message.WriteString(problem.Field + ": ")
		}
		message.WriteString(problem.Details)
		if field != problem.Field {
			// 多个字段均写入 message；只有唯一字段时才设置 OpenAI 的单字段 param。
			field = ""
		}
	}
	w.Header().Set("X-XHub-Error-Stage", stage)
	util.WriteTypedFieldError(w, path, status, kind, message.String(), field)
}
