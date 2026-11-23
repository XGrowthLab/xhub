// Package prefs registers settings routes. The rule that a database key overrides YAML lives in the handlers, not in this registration.
package prefs

import (
	"net/http"
	"os"
	"sync"

	"github.com/sunqirui1987/xhub/internal/gateway/versionupdate"
	"github.com/sunqirui1987/xhub/internal/util"
)

var logTraceOnceMount sync.Once

// Module 注册通用设置及管理员版本操作。h 为设置宿主；返回用于启动挂载的模块。
// 未配置主机 socket 时版本操作保持禁用。
func Module(h Host) util.Module {
	logTraceOnceMount.Do(func() { util.Trace("enter prefs.Module") })

	traceModule("prefs")
	updateSocket := os.Getenv("XHUB_UPDATE_SOCKET")
	return util.Bind("prefs", func(reg util.Registrar) {
		versionupdate.Mount(reg, h.RequireManage, updateSocket)
		reg.Handle("GET /get/config/callbacks", func(w http.ResponseWriter, r *http.Request) { Callbacks(h, w, r) })
		reg.Handle("GET /config/list", func(w http.ResponseWriter, r *http.Request) { List(h, w, r) })
		reg.Handle("POST /config/update", func(w http.ResponseWriter, r *http.Request) { Update(h, w, r) })
		reg.Handle("POST /config/field/update", func(w http.ResponseWriter, r *http.Request) { FieldUpdate(h, w, r) })
		reg.Handle("POST /config/field/delete", func(w http.ResponseWriter, r *http.Request) { FieldDelete(h, w, r) })
	})
}
