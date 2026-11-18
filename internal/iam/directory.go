package iam

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"xorm.io/xorm"
)

// DirectoryConnection 是单组织权威目录；TokenHash 只用于服务端验证，不能回显。
type DirectoryConnection struct {
	ID             string  `xorm:"pk 'id'" json:"id"`
	OrganizationID string  `xorm:"'organization_id'" json:"organization_id"`
	Name           string  `json:"name"`
	Enabled        bool    `json:"enabled"`
	SCIMConfigured bool    `xorm:"'scim_configured'" json:"scim_configured"`
	TokenHash      string  `xorm:"'token_hash'" json:"-"`
	DefaultTeamID  *string `xorm:"'default_team_id'" json:"default_team_id"`
	Revision       int64   `json:"revision"`
}

// TableName 返回连接表名；由 xorm 调用，无参数、副作用或异常。
func (DirectoryConnection) TableName() string { return "directory_connections" }

// DirectoryUser 关联企业身份和专属本地普通用户；人工空归属用 Mode 区分。
type DirectoryUser struct {
	ID                string  `xorm:"pk 'id'" json:"id"`
	ConnectionID      string  `xorm:"'connection_id'" json:"connection_id"`
	ExternalID        string  `xorm:"'external_id'" json:"external_id"`
	UserID            string  `xorm:"'user_id'" json:"user_id"`
	Active            bool    `json:"active"`
	Mode              string  `json:"mode"`
	ManualTeamID      *string `xorm:"'manual_team_id'" json:"manual_team_id"`
	SelectedMappingID string  `xorm:"'selected_mapping_id'" json:"selected_mapping_id"`
	AssignmentReason  string  `xorm:"'assignment_reason'" json:"assignment_reason"`
}

// TableName 返回身份表名；由 xorm 调用，无参数、副作用或异常。
func (DirectoryUser) TableName() string { return "directory_users" }

// DirectoryGroup 保存企业组，独立于本地计费团队，成员只引用同连接身份。
type DirectoryGroup struct {
	ID           string `xorm:"pk 'id'" json:"id"`
	ConnectionID string `xorm:"'connection_id'" json:"connection_id"`
	ExternalID   string `xorm:"'external_id'" json:"external_id"`
	Name         string `json:"name"`
}

// TableName 返回组表名；由 xorm 调用，无参数、副作用或异常。
func (DirectoryGroup) TableName() string { return "directory_groups" }

// DirectoryMapping 显式将一个企业组映射为同组织团队，数字越小越优先。
type DirectoryMapping struct {
	ID           string `xorm:"pk 'id'" json:"id"`
	ConnectionID string `xorm:"'connection_id'" json:"connection_id"`
	GroupID      string `xorm:"'group_id'" json:"group_id"`
	TeamID       string `xorm:"'team_id'" json:"team_id"`
	Priority     int    `json:"priority"`
	Enabled      bool   `json:"enabled"`
}

// TableName 返回映射表名；由 xorm 调用，无参数、副作用或异常。
func (DirectoryMapping) TableName() string { return "directory_team_mappings" }

// CreateDirectoryConnection 创建组织连接并返回一次性同步凭据；参数 by 是已授权管理主体。
// orgID/name 指定目标与名称；可复用 SSO 锚点，返回连接、一次性 token 或错误，不修改 SSO 与成员。
func (db *DB) CreateDirectoryConnection(ctx context.Context, by Actor, orgID, name string) (*DirectoryConnection, string, error) {
	if strings.TrimSpace(name) == "" {
		return nil, "", ErrInvalid
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	c := DirectoryConnection{ID: newID(), OrganizationID: orgID, Name: strings.TrimSpace(name), TokenHash: HashKey(token), Revision: 1, SCIMConfigured: true}
	err := db.tx(ctx, func(s *xorm.Session) error {
		var org Organization
		if err := get(s.SQL("SELECT * FROM organizations WHERE id=? FOR UPDATE", orgID), &org); err != nil {
			return err
		}
		if org.Status != StatusActive {
			return ErrInactive
		}
		var existing DirectoryConnection
		found, err := s.SQL("SELECT * FROM directory_connections WHERE organization_id=? FOR UPDATE", orgID).Get(&existing)
		if err != nil {
			return err
		}
		if found {
			if existing.SCIMConfigured {
				return ErrConflict
			}
			// 复用组织身份锚点但只开通 SCIM，绝不覆盖已保存的 SSO。
			c.ID = existing.ID
			c.Revision = existing.Revision + 1
			c.DefaultTeamID = existing.DefaultTeamID
			if _, err := s.Exec("UPDATE directory_connections SET name=?, token_hash=?, scim_configured=true, enabled=false, revision=revision+1 WHERE id=?", c.Name, c.TokenHash, c.ID); err != nil {
				return err
			}
		} else if _, err := s.Insert(&c); err != nil {
			return err
		}
		return writeAudit(s, by, Audit{Action: "directory.create", ObjectType: "directory_connection", ObjectID: c.ID})
	})
	if err != nil {
		return nil, "", err
	}
	return &c, token, nil
}

// DirectoryConnections 返回所有连接且结构不含可序列化秘密；供平台管理员列表使用。
// 参数 ctx 限制查询生命周期；返回有序列表或存储错误，无副作用。
func (db *DB) DirectoryConnections(ctx context.Context) ([]DirectoryConnection, error) {
	out := []DirectoryConnection{}
	err := db.Engine.Context(ctx).Where("scim_configured=true").Asc("id").Find(&out)
	return out, err
}

// DeleteDirectoryConnection 解除企业目录连接；ctx/by/id 为生命周期、管理主体与连接标识，返回不存在或事务错误。
// 管理员删除入口调用；只撤销 SCIM 和组映射，保留 SSO、稳定用户标识和本地业务数据。
// 保留身份锚点支持重新接入；任何失败（含审计失败）全部回滚，不按邮箱重新合并身份。
func (db *DB) DeleteDirectoryConnection(ctx context.Context, by Actor, id string) error {
	return db.tx(ctx, func(s *xorm.Session) error {
		if err := get(s.SQL("SELECT * FROM directory_connections WHERE id = ? AND scim_configured=true FOR UPDATE", id), new(DirectoryConnection)); err != nil {
			return err
		}
		// 稳定身份不属于同步开关，不能因删除 SCIM 破坏 SSO 身份绑定。
		for _, table := range []string{"directory_team_mappings", "directory_group_members", "directory_groups"} {
			if _, err := s.Exec("DELETE FROM "+table+" WHERE connection_id = ?", id); err != nil {
				return err
			}
		}
		if _, err := s.Exec("UPDATE directory_connections SET scim_configured=false, enabled=false, token_hash=?, default_team_id=NULL, revision=revision+1 WHERE id=?", HashKey(newID()), id); err != nil {
			return err
		}
		return writeAudit(s, by, Audit{Action: "directory.delete", ObjectType: "directory_connection", ObjectID: id})
	})
}

// DirectoryConnectionByID 读取连接；供管理及登录使用，调用方必须控制秘密暴露与权限。
// 参数 id 是本地连接标识；返回记录或不存在/数据库错误，无副作用。
func (db *DB) DirectoryConnectionByID(ctx context.Context, id string) (*DirectoryConnection, error) {
	s := db.session(ctx)
	defer s.Close()
	var c DirectoryConnection
	err := get(s.ID(id), &c)
	return &c, err
}

// AuthenticateDirectory 校验连接范围 Bearer 凭据与启用状态；供 SCIM 中间件使用。
// 参数 id/token 为请求身份，返回连接或统一未授权错误，不泄漏存在性，不改变状态。
func (db *DB) AuthenticateDirectory(ctx context.Context, id, token string) (*DirectoryConnection, error) {
	if token == "" {
		return nil, ErrInactive
	}
	s := db.session(ctx)
	defer s.Close()
	var c DirectoryConnection
	if err := get(s.Where("id = ? AND token_hash = ? AND enabled = true AND scim_configured = true", id, HashKey(token)), &c); err != nil {
		return nil, ErrInactive
	}
	var o Organization
	if err := get(s.ID(c.OrganizationID), &o); err != nil || o.Status != StatusActive {
		return nil, ErrInactive
	}
	return &c, nil
}

// ConfigureDirectory 修改启用状态和默认团队并事务重算失效归属；参数 by 为管理主体。
// 目标团队必须活跃且同组织，空串清除默认；返回错误，失败全部回滚并写成功审计。
func (db *DB) ConfigureDirectory(ctx context.Context, by Actor, id string, enabled bool, fallback string) error {
	return db.tx(ctx, func(s *xorm.Session) error {
		var c DirectoryConnection
		if err := get(s.SQL("SELECT * FROM directory_connections WHERE id = ? FOR UPDATE", id), &c); err != nil {
			return err
		}
		if !c.SCIMConfigured {
			return ErrNotFound
		}
		if fallback != "" {
			if err := directoryTeam(s, c.OrganizationID, fallback); err != nil {
				return err
			}
		}
		var value any
		if fallback != "" {
			value = fallback
		}
		if _, err := s.Exec("UPDATE directory_connections SET enabled = ?, default_team_id = ?, revision = revision + 1 WHERE id = ?", enabled, value, id); err != nil {
			return err
		}
		c.DefaultTeamID = nil
		if fallback != "" {
			c.DefaultTeamID = &fallback
		}
		if err := reconcileDirectory(ctx, s, c, ""); err != nil {
			return err
		}
		return writeAudit(s, by, Audit{Action: "directory.configure", ObjectType: "directory_connection", ObjectID: id})
	})
}

// ProvisionDirectoryUser 创建或按稳定 externalID 更新专属普通用户；供 SCIM 用户写入调用。
// email/name/active 为目录管理字段，不合并已有同邮箱账户，不授予预算、密码或管理员。
// 返回身份和用户；停用同时撤销会话及个人密钥，任何失败回滚。
func (db *DB) ProvisionDirectoryUser(ctx context.Context, connectionID, externalID, email, name string, active bool) (*DirectoryUser, *User, error) {
	return db.provisionDirectoryUser(ctx, connectionID, externalID, email, name, active, nil)
}

// provisionDirectoryUser 在连接锁内应用可选原子变更；其他参数与公开同步方法相同。
// mutate 用于 SCIM PATCH，输入当前快照，返回完整目标资源；校验或写入失败整笔回滚。
func (db *DB) provisionDirectoryUser(ctx context.Context, connectionID, externalID, email, name string, active bool, mutate func(*xorm.Session) (string, string, string, bool, error)) (*DirectoryUser, *User, error) {
	if mutate == nil {
		if strings.TrimSpace(externalID) == "" || !strings.Contains(normEmail(email), "@") {
			return nil, nil, ErrInvalid
		}
	}
	var d DirectoryUser
	var u User
	err := db.tx(ctx, func(s *xorm.Session) error {
		var c DirectoryConnection
		if err := get(s.SQL("SELECT * FROM directory_connections WHERE id = ? AND scim_configured=true FOR UPDATE", connectionID), &c); err != nil {
			return err
		}
		if mutate != nil {
			var err error
			externalID, email, name, active, err = mutate(s)
			if err != nil {
				return err
			}
			if externalID == "" || !strings.Contains(normEmail(email), "@") {
				return ErrInvalid
			}
		}
		found, err := s.Where("connection_id = ? AND external_id = ?", connectionID, externalID).Get(&d)
		if err != nil {
			return err
		}
		status := StatusActive
		if !active {
			status = StatusDisabled
		}
		if !found {
			u = User{ID: newID(), Email: normEmail(email), Name: name, Role: RoleUser, Status: status, SessionVersion: 1}
			zero := 0.0
			u.MaxBudget = &zero
			if _, err := s.Insert(&u); err != nil {
				return err
			}
			d = DirectoryUser{ID: newID(), ConnectionID: connectionID, ExternalID: externalID, UserID: u.ID, Active: active, Mode: "auto"}
			if _, err := s.Insert(&d); err != nil {
				return err
			}
		} else {
			if err := get(s.ID(d.UserID), &u); err != nil {
				return err
			}
			if u.Role != RoleUser {
				return ErrInvalid
			}
			if _, err := s.Exec("UPDATE users SET email = ?, name = ?, status = ?, session_version = session_version + ? WHERE id = ?", normEmail(email), name, status, boolInt(d.Active != active), u.ID); err != nil {
				return err
			}
			if !active {
				if _, err := revokePersonalKeys(ctx, s, "user_id = ?", u.ID); err != nil {
					return err
				}
			}
			if _, err := s.Exec("UPDATE directory_users SET active = ? WHERE id = ?", active, d.ID); err != nil {
				return err
			}
			d.Active = active
			u.Email, u.Name, u.Status = normEmail(email), name, status
		}
		if err := reconcileDirectory(ctx, s, c, ""); err != nil {
			return err
		}
		return writeAudit(s, Actor{Kind: "system"}, Audit{Action: "directory.user.sync", ObjectType: "user", ObjectID: u.ID, Detail: map[string]any{"object_name": u.Name}})
	})
	return &d, &u, err
}

// boolInt 将状态变化转为会话版本增量；参数为变化标记，返回 0/1，无副作用。
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// ReplaceDirectoryGroup 以同连接用户标识替换一个组的完整成员集；供 SCIM PUT/POST 调用。
// 只替换该组，不推断其他组被删除；未知或跨连接成员导致全部回滚，返回组或错误。
func (db *DB) ReplaceDirectoryGroup(ctx context.Context, connectionID, externalID, name string, members []string) (*DirectoryGroup, error) {
	return db.replaceDirectoryGroup(ctx, connectionID, externalID, name, members, nil)
}

// replaceDirectoryGroup 在连接锁内应用组变更；mutate 提供 PATCH 读改写，其他参数与公开方法一致。
// 返回提交后的组或错误，成员校验、归属重算与审计均在同一事务内执行。
func (db *DB) replaceDirectoryGroup(ctx context.Context, connectionID, externalID, name string, members []string, mutate func(*xorm.Session) (string, string, []string, error)) (*DirectoryGroup, error) {
	if mutate == nil {
		if externalID == "" || strings.TrimSpace(name) == "" || len(members) > 10000 {
			return nil, ErrInvalid
		}
	}
	var g DirectoryGroup
	err := db.tx(ctx, func(s *xorm.Session) error {
		var c DirectoryConnection
		if err := get(s.SQL("SELECT * FROM directory_connections WHERE id = ? AND scim_configured=true FOR UPDATE", connectionID), &c); err != nil {
			return err
		}
		if mutate != nil {
			var err error
			externalID, name, members, err = mutate(s)
			if err != nil {
				return err
			}
			if externalID == "" || strings.TrimSpace(name) == "" || len(members) > 10000 {
				return ErrInvalid
			}
		}
		found, err := s.Where("connection_id = ? AND external_id = ?", connectionID, externalID).Get(&g)
		if err != nil {
			return err
		}
		if !found {
			g = DirectoryGroup{ID: newID(), ConnectionID: connectionID, ExternalID: externalID, Name: name}
			if _, err := s.Insert(&g); err != nil {
				return err
			}
		} else {
			g.Name = name
			if _, err := s.ID(g.ID).Cols("name").Update(&g); err != nil {
				return err
			}
		}
		if _, err := s.Exec("DELETE FROM directory_group_members WHERE group_id = ?", g.ID); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, id := range members {
			if seen[id] {
				continue
			}
			seen[id] = true
			var d DirectoryUser
			if err := get(s.Where("id = ? AND connection_id = ?", id, connectionID), &d); err != nil {
				return err
			}
			if _, err := s.Exec("INSERT INTO directory_group_members(connection_id,group_id,directory_user_id) VALUES(?,?,?)", connectionID, g.ID, id); err != nil {
				return err
			}
		}
		if err := reconcileDirectory(ctx, s, c, ""); err != nil {
			return err
		}
		if _, err := s.Exec("UPDATE directory_connections SET revision = revision + 1 WHERE id = ?", connectionID); err != nil {
			return err
		}
		return writeAudit(s, Actor{Kind: "system"}, Audit{Action: "directory.group.sync", ObjectType: "directory_group", ObjectID: g.ID, Detail: map[string]any{"object_name": g.Name}})
	})
	return &g, err
}

// SetDirectoryMapping 保存显式组映射并重算；参数映射包含连接、组、团队、优先级和启用状态。
// by 为管理员主体；拒绝跨组织映射，保留有效已有归属，不覆盖人工来源；返回错误并原子回滚。
func (db *DB) SetDirectoryMapping(ctx context.Context, by Actor, m DirectoryMapping) error {
	if m.Priority < 0 {
		return ErrInvalid
	}
	return db.tx(ctx, func(s *xorm.Session) error {
		var c DirectoryConnection
		if err := get(s.SQL("SELECT * FROM directory_connections WHERE id = ? AND scim_configured=true FOR UPDATE", m.ConnectionID), &c); err != nil {
			return err
		}
		var g DirectoryGroup
		if err := get(s.Where("id = ? AND connection_id = ?", m.GroupID, m.ConnectionID), &g); err != nil {
			return err
		}
		if err := directoryTeam(s, c.OrganizationID, m.TeamID); err != nil {
			return err
		}
		var old DirectoryMapping
		found, err := s.Where("group_id = ?", m.GroupID).Get(&old)
		if err != nil {
			return err
		}
		if found {
			m.ID = old.ID
			_, err = s.ID(m.ID).AllCols().Update(&m)
		} else {
			m.ID = newID()
			_, err = s.Insert(&m)
		}
		if err != nil {
			return err
		}
		if _, err := s.Exec("UPDATE directory_connections SET revision = revision + 1 WHERE id = ?", c.ID); err != nil {
			return err
		}
		if err := reconcileDirectory(ctx, s, c, ""); err != nil {
			return err
		}
		return writeAudit(s, by, Audit{Action: "directory.mapping.set", ObjectType: "directory_mapping", ObjectID: m.ID})
	})
}

// OverrideDirectoryTeam 设置人工固定团队或恢复自动；manual 空团队表示明确禁止自动分配。
// 参数 userID 为本地用户，teamID 必须同组织且活跃；返回错误，迁移与密钥撤销原子执行。
func (db *DB) OverrideDirectoryTeam(ctx context.Context, by Actor, userID, mode, teamID string) error {
	if mode != "auto" && mode != "manual" {
		return ErrInvalid
	}
	return db.tx(ctx, func(s *xorm.Session) error {
		var d DirectoryUser
		if err := get(s.Where("user_id = ?", userID), &d); err != nil {
			return err
		}
		var c DirectoryConnection
		if err := get(s.SQL("SELECT * FROM directory_connections WHERE id = ? AND scim_configured=true FOR UPDATE", d.ConnectionID), &c); err != nil {
			return err
		}
		// 连接锁可能等待其他人工编辑；拿锁后重新读取，避免使用等待前的过时归属状态。
		if err := get(s.Where("user_id = ? AND connection_id = ?", userID, c.ID), &d); err != nil {
			return err
		}
		if mode == "manual" && teamID != "" {
			if err := directoryTeam(s, c.OrganizationID, teamID); err != nil {
				return err
			}
		}
		var value any
		if mode == "manual" && teamID != "" {
			value = teamID
		}
		if _, err := s.Exec("UPDATE directory_users SET mode = ?, manual_team_id = ? WHERE id = ?", mode, value, d.ID); err != nil {
			return err
		}
		if err := reconcileDirectory(ctx, s, c, d.UserID); err != nil {
			return err
		}
		return writeAudit(s, by, Audit{Action: "directory.assignment.override", ObjectType: "user", ObjectID: userID})
	})
}

// directoryTeam 验证连接目标的组织与启用状态；参数为事务/组织/团队，返回错误，无副作用。
func directoryTeam(s *xorm.Session, orgID, teamID string) error {
	var t Team
	if err := get(s.ID(teamID), &t); err != nil {
		return err
	}
	if t.OrganizationID != orgID {
		return ErrInvalid
	}
	if t.Status != StatusActive {
		return ErrInactive
	}
	return nil
}

// reconcileDirectory 在管理写事务中按完整已知组关系计算有效团队并维护既有额度与最后管理员约束。
// 参数 c 为连接，forceUserID 仅强制重算指定用户，避免一次人工操作迁移其他用户；失败回滚。
func reconcileDirectory(ctx context.Context, s *xorm.Session, c DirectoryConnection, forceUserID string) error {
	var users []DirectoryUser
	if err := s.Where("connection_id = ?", c.ID).Find(&users); err != nil {
		return err
	}
	fallback := ""
	if c.DefaultTeamID != nil && directoryTeam(s, c.OrganizationID, *c.DefaultTeamID) == nil {
		fallback = *c.DefaultTeamID
	}
	for _, d := range users {
		var memberships []TeamMembership
		if err := s.Where("user_id = ?", d.UserID).Find(&memberships); err != nil {
			return err
		}
		current := DirectoryAssignment{Mode: d.Mode}
		if len(memberships) > 0 {
			current.TeamID = memberships[0].TeamID
		}
		if d.Mode == "manual" {
			current.TeamID = ""
			if d.ManualTeamID != nil && directoryTeam(s, c.OrganizationID, *d.ManualTeamID) == nil {
				current.TeamID = *d.ManualTeamID
			}
		}
		var candidates []DirectoryCandidate
		if err := s.SQL(`SELECT m.id AS mapping_id,m.team_id,m.priority FROM directory_team_mappings m JOIN directory_group_members gm ON gm.group_id=m.group_id JOIN teams t ON t.id=m.team_id WHERE gm.directory_user_id=? AND m.connection_id=? AND m.enabled=true AND t.status='active' AND t.organization_id=?`, d.ID, c.ID, c.OrganizationID).Find(&candidates); err != nil {
			return err
		}
		next, err := SelectDirectoryTeam(current, candidates, fallback, forceUserID == d.UserID)
		if err != nil {
			return err
		}
		old := ""
		if len(memberships) > 0 {
			old = memberships[0].TeamID
		}
		if old != next.TeamID {
			for _, membership := range memberships {
				if membership.Role == TeamAdmin {
					if err := keepTeamAdmin(ctx, s, membership.TeamID, d.UserID); err != nil {
						return err
					}
				}
				if _, err := revokePersonalKeys(ctx, s, "user_id = ?", d.UserID); err != nil {
					return err
				}
			}
			if _, err := s.Where("user_id = ?", d.UserID).Delete(&TeamMembership{}); err != nil {
				return err
			}
			if next.TeamID != "" {
				if _, err := s.Insert(&TeamMembership{TeamID: next.TeamID, UserID: d.UserID, Role: TeamMember}); err != nil {
					return err
				}
			}
			if err := validateQuota(s, "user", d.UserID); err != nil {
				return err
			}
			if err := writeAudit(s, Actor{Kind: "system"}, Audit{Action: "directory.team.assign", ObjectType: "user", ObjectID: d.UserID, TeamID: next.TeamID, Detail: map[string]any{"previous_team_id": old, "reason": next.Reason}}); err != nil {
				return err
			}
		}
		if _, err := s.Exec("UPDATE directory_users SET selected_mapping_id = ?, assignment_reason = ? WHERE id = ?", next.MappingID, next.Reason, d.ID); err != nil {
			return err
		}
	}
	return nil
}
