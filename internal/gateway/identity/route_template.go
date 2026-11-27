package identity

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/sunqirui1987/xhub/internal/router"
	"net/http"
	"strings"

	"github.com/sunqirui1987/xhub/internal/authz"
	"github.com/sunqirui1987/xhub/internal/gateway/templateauth"
	"github.com/sunqirui1987/xhub/internal/iam"
	"github.com/sunqirui1987/xhub/internal/util"
)

// Router templates are named bundles of router settings that an organization, a
// team or a key selects from. The handlers live here rather than with the rest of
// the router settings because everything they need is here: who is calling, the
// one authorization decision per object, the identity store, and the error
// writers that turn a store failure into a response.
//
// IAM stores the documents and router compiles execution policies. This file
// manages which named document a scope points at.

// templateView is one template as the console reads it. Body is handed over as
// parsed JSON rather than as the stored string so the console does not have to
// parse it a second time in a different language.
type templateView struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Body      map[string]any `json:"body"`
	UpdatedAt string         `json:"updated_at"`
	// UsedBy counts the scopes selecting this template. The list screen shows it,
	// and a non-zero value is what makes the delete button refuse.
	UsedBy int `json:"used_by"`
	// Writable says whether the caller may edit this one. A template a sibling
	// team built is visible and selectable but not editable, and the console
	// needs to know which is which rather than showing an edit button that fails.
	Writable bool `json:"writable"`
	// Ownership, so the console can group the list without inferring it.
	OrgID  string `json:"organization_id,omitempty"`
	TeamID string `json:"team_id,omitempty"`
}

// RouteTemplateList returns every template with its usage count.
// 参数 g（Gate）：带当前操作者的鉴权守卫。允许时返回 nil，拒绝时返回禁止或未找到；w（http.ResponseWriter）：调用方的 HTTP 响应，状态和正文写在这里；r（*http.Request）：入站 HTTP 请求。
// 返回：无。状态码和正文写进调用方的响应。
// 调用：gateway/identity/mount.go
// 测试：route_template_test.go
func RouteTemplateList(g Gate, w http.ResponseWriter, r *http.Request) {
	p := g.RequireUser(w, r)
	if p == nil {
		return
	}
	rows, err := g.Identity().ListRouteTemplates(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	counts, err := g.Identity().RouteTemplateUsageCounts(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// One row at a time through the same rule that governs selecting one, rather
	// than a query filter that would have to restate the visibility rule in SQL.
	// The two would drift, and the drift would be a tenant reading another
	// tenant's configuration.
	out := make([]templateView, 0, len(rows))
	for _, row := range rows {
		visible := authz.Object{
			Type:   authz.ObjectRouteTemplate,
			ID:     row.ID,
			OrgID:  deref(row.OrganizationID),
			TeamID: deref(row.TeamID),
		}
		if err := g.Authorize(r, p, authz.ActionRouteTemplateRead, visible); err != nil {
			continue
		}
		out = append(out, templateView{
			ID:        row.ID,
			Name:      row.Name,
			Body:      row.Settings(),
			UpdatedAt: row.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			UsedBy:    counts[row.ID],
			Writable:  g.Authorize(r, p, authz.ActionRouteTemplateWrite, visible) == nil,
			OrgID:     deref(row.OrganizationID),
			TeamID:    deref(row.TeamID),
		})
	}
	util.WriteJSON(w, 200, map[string]any{"data": out})
}

// RouteTemplateGet returns one template.
// 参数 g（Gate）：带当前操作者的鉴权守卫。允许时返回 nil，拒绝时返回禁止或未找到；w（http.ResponseWriter）：调用方的 HTTP 响应，状态和正文写在这里；r（*http.Request）：入站 HTTP 请求。
// 返回：无。状态码和正文写进调用方的响应。
// 调用：gateway/identity/mount.go
// 测试：route_template_test.go
func RouteTemplateGet(g Gate, w http.ResponseWriter, r *http.Request) {
	if g.RequireManage(w, r) == nil {
		return
	}
	id := r.PathValue("template_id")
	row, err := g.Identity().GetRouteTemplate(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if row == nil {
		util.WriteError(w, 404, "not_found", "route template not found")
		return
	}
	util.WriteJSON(w, 200, templateView{
		ID:        row.ID,
		Name:      row.Name,
		Body:      row.Settings(),
		UpdatedAt: row.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
}

// RouteTemplateCreate inserts a template.
// 参数 g（Gate）：带当前操作者的鉴权守卫。允许时返回 nil，拒绝时返回禁止或未找到；w（http.ResponseWriter）：调用方的 HTTP 响应，状态和正文写在这里；r（*http.Request）：入站 HTTP 请求。
// 返回：无。状态码和正文写进调用方的响应。
// 调用：gateway/identity/mount.go
// 测试：route_template_test.go
func RouteTemplateCreate(g Gate, w http.ResponseWriter, r *http.Request) {
	p := g.RequireManage(w, r)
	if p == nil {
		return
	}
	body := readMap(r)
	name := strings.TrimSpace(str(body["name"]))
	if name == "" {
		util.WriteError(w, 400, "invalid_request", "name required")
		return
	}
	// A new template starts as a complete document seeded from the platform
	// default, not as an empty one.
	//
	// Resolution replaces rather than merges, so a template that omits a key
	// means that key's zero value - one attempt, a zero timeout. Seeding makes every
	// template in the table a working configuration, and an operator who wants
	// one attempt can still delete the line.
	encoded, err := encodeTemplateBody(seedFromBuiltin(body["body"]))
	if err != nil {
		util.WriteError(w, 400, "invalid_request", err.Error())
		return
	}
	if err := validateTemplateModelRouting(encoded); err != nil {
		util.WriteError(w, 400, "invalid_request", err.Error())
		return
	}
	// Who this template belongs to. An absent pair means a platform template,
	// which is what the current create path produces and what the existing
	// tests exercise; an organization or team makes it theirs.
	owner := iam.TemplateOwner{OrgID: strings.TrimSpace(str(body["organization_id"])),
		TeamID: strings.TrimSpace(str(body["team_id"]))}
	if owner.TeamID != "" && owner.OrgID == "" {
		// A team template with no organization is unreachable by every rule that
		// reads the organization first, and the database refuses it too. Answering
		// here gives the caller a reason instead of a 500.
		util.WriteError(w, 400, "invalid_request", "organization_id is required when team_id is given")
		return
	}
	if err := g.Authorize(r, p, authz.ActionRouteTemplateWrite, authz.Object{
		Type: authz.ObjectRouteTemplate, OrgID: owner.OrgID, TeamID: owner.TeamID,
	}); err != nil {
		g.WriteAuthz(w, r, err)
		return
	}
	if catalog, ok := g.(interface{ ValidateRoutingTemplate(string) error }); ok {
		if err := catalog.ValidateRoutingTemplate(encoded); err != nil {
			util.WriteError(w, 400, "invalid_request", err.Error())
			return
		}
	}
	row, err := g.Identity().CreateRouteTemplate(r.Context(), actorOf(p), owner, name, encoded)
	if err != nil {
		g.WriteIAMError(w, r, err)
		return
	}
	util.WriteJSON(w, 200, map[string]any{"id": row.ID, "name": row.Name, "status": "success"})
}

// RouteTemplateUpdate replaces a template's name and body.
// 参数 g（Gate）：带当前操作者的鉴权守卫。允许时返回 nil，拒绝时返回禁止或未找到；w（http.ResponseWriter）：调用方的 HTTP 响应，状态和正文写在这里；r（*http.Request）：入站 HTTP 请求。
// 返回：无。状态码和正文写进调用方的响应。
// 调用：gateway/identity/mount.go
// 测试：route_template_test.go
func RouteTemplateUpdate(g Gate, w http.ResponseWriter, r *http.Request) {
	p := g.RequireUser(w, r)
	if p == nil {
		return
	}
	id := r.PathValue("template_id")
	existing, err := g.Identity().GetRouteTemplate(ctx0(r), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if existing == nil {
		util.WriteError(w, 404, "not_found", "route template not found")
		return
	}
	// Editing is the owner's alone. A sibling team's template is readable and
	// selectable; it is not yours to change.
	if err := g.Authorize(r, p, authz.ActionRouteTemplateWrite, authz.Object{
		Type: authz.ObjectRouteTemplate, ID: existing.ID,
		OrgID: deref(existing.OrganizationID), TeamID: deref(existing.TeamID),
	}); err != nil {
		g.WriteAuthz(w, r, err)
		return
	}
	body := readMap(r)
	encoded, err := encodeTemplateBody(body["body"])
	if err != nil {
		util.WriteError(w, 400, "invalid_request", err.Error())
		return
	}
	if err := validateTemplateModelRouting(encoded); err != nil {
		util.WriteError(w, 400, "invalid_request", err.Error())
		return
	}
	if catalog, ok := g.(interface{ ValidateRoutingTemplate(string) error }); ok {
		if err := catalog.ValidateRoutingTemplate(encoded); err != nil {
			util.WriteError(w, 400, "invalid_request", err.Error())
			return
		}
	}
	row, err := g.Identity().UpdateRouteTemplate(r.Context(), actorOf(p), id, str(body["name"]), encoded)
	if err != nil {
		g.WriteIAMError(w, r, err)
		return
	}
	if row == nil {
		util.WriteError(w, 404, "not_found", "route template not found")
		return
	}
	util.WriteJSON(w, 200, map[string]any{"id": row.ID, "name": row.Name, "status": "success"})
}

// RouteTemplateDelete removes a template, refusing while any scope still
// selects it.
//
// The refusal carries the scopes that hold it because "in use" alone leaves the
// operator to go find them, and the difference between "I removed something
// nobody used" and "I changed three teams' behaviour" is the whole reason to
// refuse in the first place.
//
// 参数 g（Gate）：带当前操作者的鉴权守卫。允许时返回 nil，拒绝时返回禁止或未找到；w（http.ResponseWriter）：调用方的 HTTP 响应，状态和正文写在这里；r（*http.Request）：入站 HTTP 请求。
// 返回：无。状态码和正文写进调用方的响应。
// 调用：gateway/identity/mount.go
// 测试：route_template_test.go
func RouteTemplateDelete(g Gate, w http.ResponseWriter, r *http.Request) {
	p := g.RequireUser(w, r)
	if p == nil {
		return
	}
	id := r.PathValue("template_id")
	existing, err := g.Identity().GetRouteTemplate(ctx0(r), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if existing == nil {
		util.WriteError(w, 404, "not_found", "route template not found")
		return
	}
	if err := g.Authorize(r, p, authz.ActionRouteTemplateWrite, authz.Object{
		Type: authz.ObjectRouteTemplate, ID: existing.ID,
		OrgID: deref(existing.OrganizationID), TeamID: deref(existing.TeamID),
	}); err != nil {
		g.WriteAuthz(w, r, err)
		return
	}
	usage, err := g.Identity().RouteTemplateUsage(ctx0(r), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(usage) > 0 {
		util.WriteJSON(w, 409, map[string]any{
			"error": map[string]any{
				"code":    "template_in_use",
				"message": "this template is still selected by an organization, a team or a key",
				"used_by": usage,
			},
		})
		return
	}
	if err := g.Identity().DeleteRouteTemplate(ctx0(r), actorOf(p), id); err != nil {
		g.WriteIAMError(w, r, err)
		return
	}
	util.WriteJSON(w, 200, map[string]any{"status": "success"})
}

// RouteTemplateUsage lists the scopes selecting one template. It is the same list
// the delete refusal carries, for an operator who wants to look before deleting.
// 参数 g（Gate）：带当前操作者的鉴权守卫。允许时返回 nil，拒绝时返回禁止或未找到；w（http.ResponseWriter）：调用方的 HTTP 响应，状态和正文写在这里；r（*http.Request）：入站 HTTP 请求。
// 返回：无。状态码和正文写进调用方的响应。
// 调用：gateway/identity/mount.go
// 测试：route_template_test.go
func RouteTemplateUsage(g Gate, w http.ResponseWriter, r *http.Request) {
	if g.RequireManage(w, r) == nil {
		return
	}
	usage, err := g.Identity().RouteTemplateUsage(ctx0(r), r.PathValue("template_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	util.WriteJSON(w, 200, map[string]any{"data": usage})
}

// RouteTemplateBinding 读取或修改组织、团队、密钥的直接绑定。
// 参数 g 和 r 提供身份库、授权及范围信息，w 接收结果。写入可带 expected_route_template_id，
// 使控制台确认期间被修改的绑定返回 409。旧调用可省略该字段，读取及生效优先级维持原契约。
//
// Selecting is narrower than editing: an organization administrator may point
// their own organization, a team administrator their own team, and a key's owner
// their own key. That is why each branch asks for the write action on the scope's
// own object rather than for the platform administrator gate. An empty template
// id clears the selection, which is how a scope goes back to inheriting.
//
// 参数 g（Gate）：带当前操作者的鉴权守卫。允许时返回 nil，拒绝时返回禁止或未找到；w（http.ResponseWriter）：调用方的 HTTP 响应，状态和正文写在这里；r（*http.Request）：入站 HTTP 请求。
// 返回：无。状态码和正文写进调用方的响应。
// 调用：gateway/identity/mount.go
// 测试：route_template_test.go
func RouteTemplateBinding(g Gate, w http.ResponseWriter, r *http.Request) {
	p := g.RequireUser(w, r)
	if p == nil {
		return
	}
	body := readMap(r)
	// A read addresses the scope in the query string and a write in the body,
	// because a GET carries no body. One handler serves both so the two cannot
	// drift apart in which scope names they accept.
	scope := strings.TrimSpace(str(body["scope"]))
	scopeID := strings.TrimSpace(str(body["scope_id"]))
	if scope == "" {
		scope = strings.TrimSpace(r.URL.Query().Get("scope"))
	}
	if scopeID == "" {
		scopeID = strings.TrimSpace(r.URL.Query().Get("scope_id"))
	}
	action, obj, ok := bindingTarget(scope, scopeID)
	if !ok {
		util.WriteError(w, 400, "invalid_request", "scope must be organization, team or key and scope_id is required")
		return
	}
	if err := g.Authorize(r, p, action, obj); err != nil {
		g.WriteAuthz(w, r, err)
		return
	}

	templateID := strings.TrimSpace(str(body["route_template_id"]))
	// Two decisions, not one. The one above says the caller may point this scope;
	// this one says the template they named is one they may use.
	//
	// It is the read rule rather than a separate visibility rule, because
	// visibility and selectability are deliberately the same thing here: a
	// template is usable by exactly the scopes whose members can see it. Keeping
	// them one rule means the console's list and the data plane's check cannot
	// disagree about what a caller is allowed to pick.
	//
	// Without this, a team administrator could aim their own team at another
	// organization's configuration and then route by it. It was harmless while
	// only a platform administrator could author a template, and becomes a
	// cross-tenant hole the moment organizations author their own.
	//
	// An invisible template answers 404, so the caller cannot tell "not yours"
	// from "does not exist".
	if err := templateauth.Selection(g, r, p, templateID); err != nil {
		g.WriteAuthz(w, r, err)
		return
	}
	if r.Method == http.MethodGet {
		current, err := g.Identity().ScopeRouteTemplate(ctx0(r), scope, scopeID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resolved, err := resolveRouteTemplate(g, r, scope, scopeID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		inherited := routeTemplateResolution{ScopeType: router.BuiltinSource}
		if parent, parentID, parentErr := parentScope(g.Identity(), r, scope, scopeID); parentErr != nil {
			http.Error(w, parentErr.Error(), http.StatusInternalServerError)
			return
		} else if parentID != "" {
			inherited, err = resolveRouteTemplate(g, r, parent, parentID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		util.WriteJSON(w, 200, map[string]any{
			"scope": scope, "scope_id": scopeID,
			// selected is what this scope chose; effective is what it will
			// actually route by, which differs when it chose nothing and
			// inherited from the level above.
			"route_template_id": current,
			"effective":         resolved,
			"inherited":         inherited,
		})
		return
	}

	var writeErr error
	if raw, present := body["expected_route_template_id"]; present {
		expected, valid := raw.(string)
		if !valid {
			util.WriteError(w, 400, "invalid_request", "expected_route_template_id must be a string")
			return
		}
		writeErr = g.Identity().CompareAndSetScopeRouteTemplate(ctx0(r), actorOf(p), scope, scopeID, templateID, expected)
	} else {
		writeErr = g.Identity().SetScopeRouteTemplate(ctx0(r), actorOf(p), scope, scopeID, templateID)
	}
	if errors.Is(writeErr, iam.ErrRouteTemplateBindingConflict) {
		util.WriteError(w, http.StatusConflict, "route_template_binding_conflict", "the scope now selects a different route template")
		return
	}
	if writeErr != nil {
		g.WriteIAMError(w, r, writeErr)
		return
	}
	util.Info("route template bound scope=%s id=%s template=%s", scope, scopeID, templateID)
	resolved, err := resolveRouteTemplate(g, r, scope, scopeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	util.WriteJSON(w, 200, map[string]any{
		"status": "success", "scope": scope, "scope_id": scopeID,
		// The effective template is what the scope will actually route by, which
		// is not the same as what it selected: a scope that selects nothing
		// inherits from the level above it.
		"effective": resolved,
	})
}

// bindingTarget maps a scope name onto the object the caller is pointing, and the
// action that governs pointing it.
//
// The action is selection rather than the scope's own write action. Selecting is
// narrower: it picks among configurations somebody already approved, and it
// applies to exactly the scopes the caller already belongs to, so a member may do
// it for their own team while still being unable to change the team's budget, its
// model ceiling or its routing template's contents.
//
// An unknown scope is rejected rather than defaulted: the name
// reaches SQL in the store, and a wrong default would let one scope write
// another's selection.
// 参数 scope（string）：organization、team 或 key；scopeID（string）：那个范围内的一行。
// 返回 authz.Action（authz.Action）：要判定的写动作；authz.Object（authz.Object）：要判定的对象；bool（bool）：范围名被承认且 id 非空时为真。
// 调用：RouteTemplateBinding。
// 测试：route_template_test.go
func bindingTarget(scope, scopeID string) (authz.Action, authz.Object, bool) {
	if scopeID == "" {
		return "", authz.Object{}, false
	}
	switch scope {
	case "organization":
		return authz.ActionRouteTemplateSelect, authz.Object{Type: authz.ObjectOrg, ID: scopeID}, true
	case "team":
		return authz.ActionRouteTemplateSelect, authz.Object{Type: authz.ObjectTeam, ID: scopeID}, true
	case "key":
		return authz.ActionRouteTemplateSelect, authz.Object{Type: authz.ObjectKey, ID: scopeID}, true
	default:
		return "", authz.Object{}, false
	}
}

// routeTemplateResolution is what a scope will actually route by, and where that
// came from. The console shows both: "inherits Cheap first (from Organization)"
// is the answer to "why is this team's traffic split this way", and without the
// origin a scope that selects nothing looks the same as one that selected
// something.
type routeTemplateResolution struct {
	TemplateID string `json:"template_id,omitempty"`
	Name       string `json:"name,omitempty"`
	ScopeType  string `json:"scope_type"` // organization | team | key | platform
	ScopeID    string `json:"scope_id,omitempty"`
}

// resolveRouteTemplate 为控制台返回范围的生效模板与来源。
// 参数 g/r 为身份库及请求上下文，scope/scopeID 为 key、team 或 organization 起点；返回解析结果或数据库错误。
// 每层只读取一次绑定，与请求共用 IAM 的选择规则；悬空模板回到内置设置，不保存缓存或错误侧通道。
func resolveRouteTemplate(g Gate, r *http.Request, scope, scopeID string) (routeTemplateResolution, error) {
	ids := make([]string, 3)
	scopeIDs := map[string]string{}
	for at, id := scope, scopeID; id != ""; {
		selected, err := g.Identity().ScopeRouteTemplate(ctx0(r), at, id)
		if err != nil {
			return routeTemplateResolution{}, err
		}
		for i, name := range []string{"key", "team", "organization"} {
			if at == name {
				ids[i] = selected
				scopeIDs[name] = id
			}
		}
		if strings.TrimSpace(selected) != "" {
			break
		}
		at, id, err = parentScope(g.Identity(), r, at, id)
		if err != nil {
			return routeTemplateResolution{}, err
		}
	}
	selected, source := iam.SelectRouteTemplate(ids...)
	if selected != "" {
		row, err := g.Identity().GetRouteTemplate(ctx0(r), selected)
		if err != nil {
			return routeTemplateResolution{}, err
		}
		if row != nil {
			return routeTemplateResolution{TemplateID: row.ID, Name: row.Name, ScopeType: source, ScopeID: scopeIDs[source]}, nil
		}
	}
	return routeTemplateResolution{ScopeType: router.BuiltinSource}, nil
}

// parentScope walks one step outward: key to team, team to organization,
// organization to nothing.
// 参数 db（*iam.DB）：身份库；r（*http.Request）：入站 HTTP 请求；scope（string）：当前范围；scopeID（string）：当前范围内的那一行。
// 返回 string（string）：上一层范围；string（string）：上一层的那一行。
// 调用：resolveRouteTemplate。
// 测试：route_template_test.go
func parentScope(db *iam.DB, r *http.Request, scope, scopeID string) (string, string, error) {
	switch scope {
	case "key":
		key, err := db.GetKey(ctx0(r), scopeID)
		if err != nil {
			return "", "", err
		}
		if key == nil {
			return "", "", nil
		}
		// A key belonging to nobody but an organization still has a team; a key
		// with no team at all stops here.
		return "team", key.TeamID, nil
	case "team":
		team, err := db.GetTeam(ctx0(r), scopeID)
		if err != nil {
			return "", "", err
		}
		if team == nil {
			return "", "", nil
		}
		return "organization", team.OrganizationID, nil
	default:
		return "", "", nil
	}
}

// seedFromBuiltin 为新模板提供完整的新格式骨架，避免表单和 JSON 依赖任何全局路由配置。
// 调用方提供正文时按新格式字段覆盖骨架；后续严格校验要求 model_routes 与 retry_policy 完整有效。
// 参数 supplied（any）：调用方提交的正文，可能缺失。
// 返回 map[string]any（map[string]any）：要存进库的完整文档。
// 调用：RouteTemplateCreate。
// 测试：route_template_test.go
func seedFromBuiltin(supplied any) map[string]any {
	seed := router.BuiltinDocument()
	override, ok := supplied.(map[string]any)
	if !ok {
		// An empty or absent body keeps the seed as it stands. RouteTemplateUpdate
		// takes the body as written, so this only touches creation.
		return seed
	}
	for key, value := range override {
		seed[key] = value
	}
	return seed
}

// encodeTemplateBody turns the request's body field into the stored string. An
// absent body is an empty document rather than an error: a template whose
// settings are not filled in yet is a name the operator is about to use.
// 参数 raw（any）：请求里的 body 字段，可能是一份设置对象，也可能缺失。
// 返回 string（string）：要存进库的 JSON；error（error）：格式不对时报错。
// 调用：RouteTemplateCreate、RouteTemplateUpdate。
// 测试：route_template_test.go
func encodeTemplateBody(raw any) (string, error) {
	switch v := raw.(type) {
	case nil:
		return "{}", nil
	case map[string]any:
		out, err := json.Marshal(v)
		if err != nil {
			return "", errors.New("body is not encodable")
		}
		return string(out), nil
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return "{}", nil
		}
		// It has to be an object: an array or a scalar would parse and then
		// behave as an empty document at request time, which hides the mistake.
		probe := map[string]any{}
		if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
			return "", errors.New("body must be a JSON object")
		}
		return trimmed, nil
	default:
		return "", errors.New("body must be a JSON object")
	}
}

// validateTemplateModelRouting checks a serialized template's per-model routing rules.
// 参数 encoded（string）：待校验的 JSON 正文。
// 返回 error：格式或策略无效时返回错误。
// 调用：模板创建与更新处理器。
// 测试：regression/route_template_config_test.go
func validateTemplateModelRouting(encoded string) error {
	document := map[string]any{}
	if err := json.Unmarshal([]byte(encoded), &document); err != nil {
		return errors.New("body must be a JSON object")
	}
	return router.ValidateRouteTemplateDocument(document)
}

// ctx0 is the request context, or the background context when the request has
// none. Handlers here read after the response is decided, and a cancelled
// request context must not turn a completed write into a reported failure.
// 参数 r（*http.Request）：入站 HTTP 请求。
// 返回 context.Context（context.Context）：可用的上下文。
// 调用：本文件所有 handler。
// 测试：无直接单测
func ctx0(r *http.Request) context.Context {
	if r == nil {
		return context.Background()
	}
	return r.Context()
}
