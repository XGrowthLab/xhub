package iam

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/sunqirui1987/xhub/internal/util"
	"xorm.io/xorm"
)

var logTraceOnceRouteTemplate sync.Once

// ErrRouteTemplateBindingConflict 表示目标已被重新绑定，调用方须先重新读取再确认。
var ErrRouteTemplateBindingConflict = errors.New("route template binding changed")

// TemplateOwner is who a router template belongs to. Both empty is a platform
// template; it is not an error, it is the default level.
type TemplateOwner struct {
	OrgID  string
	TeamID string
}

// RouteTemplate 保存一份具名路由模板；Body 为 JSON 文档，由 router 校验并编译。
// 组织、团队和密钥可绑定整份模板，未绑定时使用内置默认值；不复制全局执行设置。
// OrganizationID/TeamID 标识可见范围，管理接口负责授权，读取本结构不产生副作用。
type RouteTemplate struct {
	ID   string `xorm:"pk 'id'" json:"id"`
	Name string `xorm:"'name'" json:"name"`
	Body string `xorm:"'body'" json:"body"`
	// Ownership. Both nil is a platform template, which every organization can
	// read and select. Only OrganizationID is a template for that organization
	// and the teams beneath it. Both is a template for that one team.
	//
	// Pointers rather than strings because the columns are nullable foreign keys:
	// an empty string is not a valid organization id, so inserting one is an FK
	// violation rather than "unowned". Every other nullable column in this schema
	// is read the same way.
	//
	// Ownership decides who may edit; it does not decide who the settings apply
	// to. A team that selects a sibling team's template routes by that template's
	// contents, and the label on the request says it came from the sibling - who
	// wrote it and where it takes effect are separate questions.
	OrganizationID *string `xorm:"'organization_id'" json:"organization_id,omitempty"`
	TeamID         *string `xorm:"'team_id'" json:"team_id,omitempty"`
	CreatedAt      Time    `xorm:"created 'created_at'" json:"created_at"`
	UpdatedAt      Time    `xorm:"updated 'updated_at'" json:"-"`
}

// TableName tells xorm which table these rows live in.
// 参数：无。
// 返回 string（string）：表名 route_templates。
// 调用：xorm 在映射这张表时。
// 测试：无直接单测
func (RouteTemplate) TableName() string { return "route_templates" }

// Settings parses the body into a router settings document. A body that does not
// parse is reported as an empty document rather than failing the caller: the
// alternative is a single malformed row taking down every request that inherits
// it, and an empty document is exactly the platform behaviour.
// 参数：无。
// 返回 map[string]any（map[string]any）：解析出来的设置。解析失败时为空表。
// 调用：gateway 解析模板时。
// 测试：route_template_test.go
func (t RouteTemplate) Settings() map[string]any {
	trimmed := strings.TrimSpace(t.Body)
	if trimmed == "" {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		util.Error("route template body is not readable id=%s err=%v", t.ID, err)
		return map[string]any{}
	}
	return out
}

// ---------- templates ----------

// ListRouteTemplates returns every template by name.
// 参数 ctx（context.Context）：上下文，取消时停止。
// 返回 []RouteTemplate（[]RouteTemplate）：模板列表；error（error）：失败原因，nil 表示这一步成功。
// 调用：gateway/route_template.go
// 测试：route_template_test.go
func (db *DB) ListRouteTemplates(ctx context.Context) ([]RouteTemplate, error) {
	logTraceOnceRouteTemplate.Do(func() { util.Trace("enter iam.ListRouteTemplates") })

	s := db.session(ctx)
	defer s.Close()
	var rows []RouteTemplate
	if err := s.OrderBy("name ASC").Find(&rows); err != nil {
		return nil, mapErr(err)
	}
	return rows, nil
}

// GetRouteTemplate loads one template by id.
// 参数 ctx（context.Context）：上下文，取消时停止；id（string）：模板 id。
// 返回 *RouteTemplate（*RouteTemplate）：这一行，没找到时为 nil；error（error）：失败原因，nil 表示这一步成功。
// 调用：gateway/route_template.go、gateway 解析模板时。
// 测试：route_template_test.go
func (db *DB) GetRouteTemplate(ctx context.Context, id string) (*RouteTemplate, error) {
	s := db.session(ctx)
	defer s.Close()
	var row RouteTemplate
	ok, err := s.ID(id).Get(&row)
	if err != nil {
		return nil, mapErr(err)
	}
	if !ok {
		return nil, nil
	}
	return &row, nil
}

// CreateRouteTemplate inserts a template. The name must be free.
// 参数 ctx（context.Context）：上下文，取消时停止；by（Actor）：执行这次修改的操作者，写进审计；name（string）：模板名，空名会被拒绝；body（string）：一份 路由模板文档。
// 返回 *RouteTemplate（*RouteTemplate）：新插入的那一行；error（error）：失败原因，nil 表示这一步成功。
// 调用：gateway/route_template.go
// 参数 owner（TemplateOwner）：这份模板的归属，平台模板传两个空值；name（string）：模板名；body（string）：一份 路由模板文档。
// 返回 *RouteTemplate（*RouteTemplate）：新插入的那一行；error（error）：失败原因，nil 表示这一步成功。
// 调用：gateway/route_template.go
// 测试：route_template_test.go
func (db *DB) CreateRouteTemplate(ctx context.Context, by Actor, owner TemplateOwner, name, body string) (*RouteTemplate, error) {
	var out *RouteTemplate
	err := db.tx(ctx, func(s *xorm.Session) error {
		row := RouteTemplate{ID: newID(), Name: strings.TrimSpace(name), Body: body}
		if owner.OrgID != "" {
			row.OrganizationID = &owner.OrgID
		}
		if owner.TeamID != "" {
			row.TeamID = &owner.TeamID
		}
		if _, err := s.Insert(&row); err != nil {
			return err
		}
		out = &row
		return writeAudit(s, by, Audit{Action: "route_template.create", ObjectType: "route_template", ObjectID: row.ID, Detail: map[string]any{"object_name": row.Name}})
	})
	return out, err
}

// UpdateRouteTemplate replaces a template's name and body.
// 参数 ctx（context.Context）：上下文，取消时停止；by（Actor）：执行这次修改的操作者，写进审计；id（string）：模板 id；name（string）：新名字；body（string）：新的一份 路由模板文档。
// 返回 *RouteTemplate（*RouteTemplate）：更新后的那一行，不存在时为 nil；error（error）：失败原因，nil 表示这一步成功。
// 调用：gateway/route_template.go
// 测试：route_template_test.go
func (db *DB) UpdateRouteTemplate(ctx context.Context, by Actor, id, name, body string) (*RouteTemplate, error) {
	var out *RouteTemplate
	err := db.tx(ctx, func(s *xorm.Session) error {
		var row RouteTemplate
		ok, err := s.ID(id).Get(&row)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		previousName := row.Name
		settingsChanged := row.Body != body
		row.Body = body
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			row.Name = trimmed
		}
		if _, err := s.ID(id).Cols("name", "body", "updated_at").Update(&row); err != nil {
			return err
		}
		out = &row
		return writeAudit(s, by, Audit{Action: "route_template.update", ObjectType: "route_template", ObjectID: id, Detail: map[string]any{"object_name": row.Name, "previous_name": previousName, "settings_changed": settingsChanged}})
	})
	return out, err
}

// DeleteRouteTemplate removes a template.
//
// Refusing while a scope still selects it is enforced by the caller, which reads
// the usage list first and reports where. Doing it here as well would put a
// user-facing explanation inside the store.
// 参数 ctx（context.Context）：上下文，取消时停止；by（Actor）：执行这次修改的操作者，写进审计；id（string）：模板 id。
// 返回 error（error）：失败原因。不存在时报错，nil 表示这一步成功。
// 调用：gateway/route_template.go
// 测试：route_template_test.go
func (db *DB) DeleteRouteTemplate(ctx context.Context, by Actor, id string) error {
	return db.tx(ctx, func(s *xorm.Session) error {
		var row RouteTemplate
		ok, err := s.ID(id).Get(&row)
		if err != nil {
			return err
		}
		if !ok {
			return ErrRouteTemplateMissing
		}
		if _, err := s.ID(id).Delete(&RouteTemplate{}); err != nil {
			return err
		}
		return writeAudit(s, by, Audit{Action: "route_template.delete", ObjectType: "route_template", ObjectID: id, Detail: map[string]any{"object_name": row.Name}})
	})
}

// TemplateUsage is one scope that selects a template, as reported by the usage
// listing and by the refusal that blocks deleting a template still in use.
type TemplateUsage struct {
	ScopeType string `json:"scope_type"` // organization | team | key
	ScopeID   string `json:"scope_id"`
	Name      string `json:"name"`
}

// RouteTemplateUsage lists every scope currently selecting a template.
//
// It is three queries rather than one join because the three scopes live in
// three tables with no common parent beyond the id. Each is indexed on the
// column, so this is cheap even though it looks like a fan-out.
// 参数 ctx（context.Context）：上下文，取消时停止；id（string）：模板 id。
// 返回 []TemplateUsage（[]TemplateUsage）：正在选用它的组织、团队和密钥；error（error）：失败原因，nil 表示这一步成功。
// 调用：gateway/route_template.go
// 测试：route_template_test.go
func (db *DB) RouteTemplateUsage(ctx context.Context, id string) ([]TemplateUsage, error) {
	if strings.TrimSpace(id) == "" {
		return nil, nil
	}
	s := db.session(ctx)
	defer s.Close()

	out := make([]TemplateUsage, 0, 4)
	var orgs []struct {
		ID   string `xorm:"'id'"`
		Name string `xorm:"'name'"`
	}
	if err := s.Table("organizations").Where("route_template_id = ?", id).Select("id, name").Find(&orgs); err != nil {
		return nil, mapErr(err)
	}
	for _, row := range orgs {
		out = append(out, TemplateUsage{ScopeType: "organization", ScopeID: row.ID, Name: row.Name})
	}

	var teams []struct {
		ID   string `xorm:"'id'"`
		Name string `xorm:"'name'"`
	}
	if err := s.Table("teams").Where("route_template_id = ?", id).Select("id, name").Find(&teams); err != nil {
		return nil, mapErr(err)
	}
	for _, row := range teams {
		out = append(out, TemplateUsage{ScopeType: "team", ScopeID: row.ID, Name: row.Name})
	}

	var keys []struct {
		ID   string `xorm:"'id'"`
		Name string `xorm:"'name'"`
	}
	if err := s.Table("api_keys").Where("route_template_id = ?", id).Select("id, name").Find(&keys); err != nil {
		return nil, mapErr(err)
	}
	for _, row := range keys {
		out = append(out, TemplateUsage{ScopeType: "key", ScopeID: row.ID, Name: row.Name})
	}
	return out, nil
}

// RouteTemplateUsageCounts returns how many scopes select each template, keyed by
// template id. The list screen shows the count, and computing it here keeps that
// screen from running one usage query per row.
// 参数 ctx（context.Context）：上下文，取消时停止。
// 返回 map[string]int（map[string]int）：模板 id 到选用它的范围个数；error（error）：失败原因，nil 表示这一步成功。
// 调用：gateway/route_template.go
// 测试：route_template_test.go
func (db *DB) RouteTemplateUsageCounts(ctx context.Context) (map[string]int, error) {
	s := db.session(ctx)
	defer s.Close()
	out := map[string]int{}
	for _, table := range []string{"organizations", "teams", "api_keys"} {
		// No SELECT alias here either: the struct tag names the column, and an
		// alias would make xorm look for a field by that name instead and come
		// back empty.
		var rows []struct {
			TemplateID *string `xorm:"'route_template_id'"`
		}
		err := s.Table(table).
			Where("route_template_id IS NOT NULL AND route_template_id <> ''").
			Select("route_template_id").Find(&rows)
		if err != nil {
			return nil, mapErr(err)
		}
		for _, row := range rows {
			if row.TemplateID != nil && *row.TemplateID != "" {
				out[*row.TemplateID]++
			}
		}
	}
	return out, nil
}

// SetScopeRouteTemplate points one scope at a template, or clears it when id is
// empty. Clearing is what puts the scope back on inheritance.
//
// The scope is addressed as a table plus a column-safe id rather than through
// three separate functions, because the only difference between them is the
// table name and an id column that is spelled the same in all three.
// 参数 ctx（context.Context）：上下文，取消时停止；by（Actor）：执行这次修改的操作者，写进审计；scope（string）：organizations、teams 或 api_keys；scopeID（string）：那个范围内的一行；templateID（string）：选用的模板，空串表示清除选择。
// 返回 error（error）：失败原因。范围名不认识、或范围不存在时报错，nil 表示这一步成功。
// 调用：gateway/route_template.go
// 测试：route_template_test.go
func (db *DB) SetScopeRouteTemplate(ctx context.Context, by Actor, scope, scopeID, templateID string) error {
	return db.setScopeRouteTemplate(ctx, by, scope, scopeID, templateID, nil)
}

// CompareAndSetScopeRouteTemplate 在直接绑定仍等于 expected 时更新绑定并写入审计。
// ctx、by、scope、scopeID、templateID 的含义同 SetScopeRouteTemplate，expected 为空允许从继承状态更新。
// 供绑定确认流程调用，返回冲突、范围不存在或写入错误；冲突时不会写绑定或审计。
func (db *DB) CompareAndSetScopeRouteTemplate(ctx context.Context, by Actor, scope, scopeID, templateID, expected string) error {
	return db.setScopeRouteTemplate(ctx, by, scope, scopeID, templateID, &expected)
}

// setScopeRouteTemplate 在同一事务内写入指定范围的模板与审计记录。
// ctx 和 by 提供上下文与操作者，scope、scopeID、templateID 指定目标；expected 可选，指定客户端已确认的直接绑定。
// 供普通和条件绑定共用，返回数据库错误或绑定状态冲突。
func (db *DB) setScopeRouteTemplate(ctx context.Context, by Actor, scope, scopeID, templateID string, expected *string) error {
	table, ok := scopeTable(scope)
	if !ok {
		return ErrUnknownScope
	}
	if strings.TrimSpace(scopeID) == "" {
		return ErrUnknownScope
	}
	return db.tx(ctx, func(s *xorm.Session) error {
		var value any
		if trimmed := strings.TrimSpace(templateID); trimmed != "" {
			exists, err := s.ID(trimmed).Exist(&RouteTemplate{})
			if err != nil {
				return err
			}
			if !exists {
				return ErrRouteTemplateMissing
			}
			value = trimmed
		}
		query := "UPDATE " + table + " SET route_template_id = ?, updated_at = now() WHERE id = ?"
		args := []any{value, scopeID}
		if expected != nil {
			// 比较和写入在一条语句里完成，旧客户端的无条件写入也不能插入两步之间。
			query += " AND COALESCE(route_template_id, '') = ?"
			args = append(args, strings.TrimSpace(*expected))
		}
		affected, err := s.Exec(append([]any{query}, args...)...)
		if err != nil {
			return mapErr(err)
		}
		n, err := affected.RowsAffected()
		if err != nil {
			return mapErr(err)
		}
		if n == 0 {
			if expected != nil {
				exists, err := s.Table(table).Where("id = ?", scopeID).Exist()
				if err != nil {
					return mapErr(err)
				}
				if exists {
					return ErrRouteTemplateBindingConflict
				}
			}
			return ErrUnknownScope
		}
		return writeAudit(s, by, Audit{Action: "route_template.bind", ObjectType: table, ObjectID: scopeID})
	})
}

// scopeTable maps the scope name used on the wire to its table. A name that is
// not one of the three is rejected rather than interpolated: the table name goes
// into SQL, so it must come from this list and never from the request.
// 参数 scope（string）：范围名。
// 返回 string（string）：表名；bool（bool）：这个名字被承认时为真。
// 调用：SetScopeRouteTemplate、ScopeRouteTemplate。
// 测试：route_template_test.go
func scopeTable(scope string) (string, bool) {
	switch scope {
	case "organization":
		return "organizations", true
	case "team":
		return "teams", true
	case "key":
		return "api_keys", true
	default:
		return "", false
	}
}

// ScopeRouteTemplate reads one scope's selected template id. An empty string
// means the scope selects nothing and inherits.
// 参数 ctx（context.Context）：上下文，取消时停止；scope（string）：organizations、teams 或 api_keys；scopeID（string）：那个范围内的一行。
// 返回 string（string）：选用的模板 id，没有时为空串；error（error）：失败原因，nil 表示这一步成功。
// 调用：gateway 解析模板时。
// 测试：route_template_test.go
func (db *DB) ScopeRouteTemplate(ctx context.Context, scope, scopeID string) (string, error) {
	table, ok := scopeTable(scope)
	if !ok || strings.TrimSpace(scopeID) == "" {
		return "", nil
	}
	s := db.session(ctx)
	defer s.Close()
	// The column is nullable, so it has to be read into a pointer. Scanning NULL
	// into a string is an error in some drivers and silently empty in others, and
	// the empty case is exactly the "inherits" state that must not be mistaken
	// for a broken read.
	//
	// No SELECT alias: the struct tag already names the column, and an alias
	// makes xorm look for a field of that name instead, which silently yields
	// nothing.
	var rows []struct {
		TemplateID *string `xorm:"'route_template_id'"`
	}
	err := s.Table(table).Where("id = ?", scopeID).Select("route_template_id").Find(&rows)
	if err != nil {
		return "", mapErr(err)
	}
	if len(rows) == 0 || rows[0].TemplateID == nil {
		return "", nil
	}
	return strings.TrimSpace(*rows[0].TemplateID), nil
}

// SelectRouteTemplate 选择从窄到宽的首个非空模板 ID，并返回其来源。
// 参数 ids 按 key、team、organization 排列；不足三项视为未绑定，多余项忽略。
// 运行时和控制台共用；不查询模板内容，选中的模板被删除时调用方回到模型默认，不能改选父级。
func SelectRouteTemplate(ids ...string) (string, string) {
	for i, scope := range []string{"key", "team", "organization"} {
		if i < len(ids) {
			if id := strings.TrimSpace(ids[i]); id != "" {
				return id, scope
			}
		}
	}
	return "", "builtin"
}
