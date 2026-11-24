package iam

import (
	"context"
	_ "embed"
	"errors"
	"math"
	"time"

	"xorm.io/xorm"

	"github.com/sunqirui1987/xhub/internal/live"
)

// ErrQuotaAllocation 表示已消费与下级保留额度超过父级；管理接口返回 400 并回滚配置。
var ErrQuotaAllocation = errors.New("quota allocation exceeds available parent budget")

// ErrQuotaUnavailable 表示待结算消费不可读，网关应返回 503，不能当作零费用继续调用。
var ErrQuotaUnavailable = errors.New("pending quota usage is unavailable")

// ErrMultipleTeams 表示账号存在多个团队，必须先解除多余归属，不能猜测计费路径。
var ErrMultipleTeams = errors.New("a person may belong to only one team")

// quotaNode 是额度树节点；null 共享父级，固定额度保留未消费部分，历史消费留在父级。
type quotaNode struct {
	ID           string `xorm:"'id'"`
	Parent, Kind string
	HotID        string
	RPM          *int     `xorm:"'rpm_limit'"`
	TPM          *int     `xorm:"'tpm_limit'"`
	Limit        *float64 `xorm:"'max_budget'"`
	Spend        float64
	PeriodStart  *time.Time
	Children     []*quotaNode `xorm:"-"`
}

// quotaUnused 计算组织、团队、个人仍需由父级保留的金额；Key 仅校验单项金额，不预留。参数 n 是树节点；返回未消费保留金额或非法分配错误。
// 调用：单项及批量管理写入；共享节点递归传递下级保留金额，固定节点只允许下级保留其非负剩余额度；历史软预算超支不作为分配错误。
func quotaUnused(n *quotaNode) (float64, error) {
	// Key 金额仅限制自身消费；软预算可能已超支，但不能因此锁住个人的其他入口。
	if n.Kind == "key" {
		if n.Limit != nil && (math.IsNaN(*n.Limit) || math.IsInf(*n.Limit, 0) || *n.Limit < 0) {
			return 0, ErrQuotaAllocation
		}
		return 0, nil
	}
	unused := 0.0
	for _, c := range n.Children {
		amount, err := quotaUnused(c)
		if err != nil {
			return 0, err
		}
		unused += amount
	}
	if n.Limit == nil {
		return unused, nil
	}
	if math.IsNaN(*n.Limit) || math.IsInf(*n.Limit, 0) || *n.Limit < 0 {
		return 0, ErrQuotaAllocation
	}
	// 软预算允许在途请求完成后超支。负余额没有可预留金额，但不应阻塞无关兄弟节点的管理操作。
	// 仍按真实消费扣减父级余额，不能把历史超支抹掉或继续为下级分配正额度。
	remaining := math.Max(0, *n.Limit-n.Spend)
	if unused > remaining+1e-9 {
		return 0, ErrQuotaAllocation
	}
	return remaining, nil
}

// quotaTree 从会话 s 读取全局额度树，返回节点索引或数据库错误，供管理端校验分配使用。
// 所有密钥沿用户的唯一团队消费，项目另设上限；请求准入使用 quotaTreeFor 限定读取范围。
func quotaTree(s *xorm.Session) (map[string]*quotaNode, error) {
	nodes := map[string]*quotaNode{}
	var orgs []Organization
	var teams []Team
	var users []User
	var keys []Key
	var members []TeamMembership
	if err := s.Find(&orgs); err != nil {
		return nil, err
	}
	if err := s.Find(&teams); err != nil {
		return nil, err
	}
	if err := s.Find(&users); err != nil {
		return nil, err
	}
	if err := s.Find(&keys); err != nil {
		return nil, err
	}
	if err := s.Find(&members); err != nil {
		return nil, err
	}
	parents := map[string]string{}
	for _, m := range members {
		if parents[m.UserID] != "" {
			parents[m.UserID] = "ambiguous"
		} else {
			parents[m.UserID] = "team:" + m.TeamID
		}
	}
	for _, o := range orgs {
		nodes["org:"+o.ID] = &quotaNode{ID: o.ID, Kind: "org", PeriodStart: o.BudgetPeriodStart, Limit: o.MaxBudget, Spend: o.Spend, RPM: o.RPMLimit, TPM: o.TPMLimit}
	}
	for _, t := range teams {
		nodes["team:"+t.ID] = &quotaNode{ID: t.ID, Kind: "team", Parent: "org:" + t.OrganizationID, Limit: t.MaxBudget, Spend: t.Spend, RPM: t.RPMLimit, TPM: t.TPMLimit}
	}
	for _, u := range users {
		nodes["user:"+u.ID] = &quotaNode{ID: u.ID, Kind: "user", PeriodStart: u.BudgetPeriodStart, Parent: parents[u.ID], Limit: u.MaxBudget, Spend: u.Spend, RPM: u.RPMLimit, TPM: u.TPMLimit}
	}
	for _, k := range keys {
		uid := ""
		if k.UserID != nil {
			uid = *k.UserID
		}
		parent := ""
		if uid != "" {
			parent = "user:" + uid
		} else {
			return nil, ErrInvalid
		}
		nodes["key:"+k.ID] = &quotaNode{ID: k.ID, Kind: "key", HotID: k.ID, Parent: parent, Limit: k.MaxBudget, Spend: k.Spend, RPM: k.RPMLimit, TPM: k.TPMLimit}
	}
	return linkQuotaNodes(nodes), nil
}

// scopedQuotaSQL 只投影运行时额度与速率字段，在一次数据库读取中取得主体和同组织的兄弟节点。
//
//go:embed quota_scoped.sql
var scopedQuotaSQL string

// quotaTreeFor 在一个数据库语句快照内读取 user/key 所属组织的额度树，供预算、限流和预占使用。
// s 是调用方会话，每次调用均重新读取。无团队用户只取本人；归属缺失、非法或多团队均返回原错误。
// 空 kind 沿用管理端完整额度树，用于组织分配校验。
func quotaTreeFor(s *xorm.Session, kind, id string) (map[string]*quotaNode, error) {
	if kind == "" {
		return quotaTree(s)
	}
	if kind != "user" && kind != "key" {
		return nil, ErrInvalid
	}
	var rows []quotaNode
	if err := s.SQL(scopedQuotaSQL, kind, id).Find(&rows); err != nil {
		return nil, err
	}
	nodes := make(map[string]*quotaNode, len(rows))
	for i := range rows {
		n := &rows[i]
		if n.Kind == "scope" {
			// 定位结果单独返回；无团队是合法归属，不能和失效密钥混为一谈。
			switch n.Parent {
			case "missing":
				return nil, ErrNotFound
			case "multiple":
				return nil, ErrMultipleTeams
			case "":
				continue
			default:
				return nil, ErrInvalid
			}
		}
		if n.Kind == "key" {
			n.HotID = n.ID
		}
		nodes[n.Kind+":"+n.ID] = n
	}
	return linkQuotaNodes(nodes), nil
}

// linkQuotaNodes 把新读取的节点挂到直属父级，返回同一索引，供管理端和运行时共用。
// nodes 必须尚未链接，未知或不唯一的父级不挂接，后续按原规则验证归属。
func linkQuotaNodes(nodes map[string]*quotaNode) map[string]*quotaNode {
	for _, n := range nodes {
		if p := nodes[n.Parent]; p != nil {
			p.Children = append(p.Children, n)
		}
	}
	return nodes
}

// validateQuota 校验指定节点及祖先。参数 kind/id 标识修改对象；返回分配错误，事务调用方负责回滚。
// 只验证受影响路径；直接修改对象仍不得低于自身消费与预占，递归兄弟节点允许历史软预算超支。
func validateQuota(s *xorm.Session, kind, id string) error {
	nodes, err := quotaTree(s)
	if err != nil {
		return err
	}
	n := nodes[kind+":"+id]
	if n == nil {
		return ErrNotFound
	}
	if err := applyBudgetHolds(s, nodes); err != nil {
		return err
	}
	// 保留直接修改对象的降额约束；仅递归扫描到的历史超支不阻塞此次分配。
	if kind != "key" && n.Limit != nil && n.Spend > *n.Limit+1e-9 {
		return ErrQuotaAllocation
	}
	for n != nil {
		if n.Parent == "ambiguous" {
			return ErrMultipleTeams
		}
		if _, err := rateAllocated(n, false); err != nil {
			return err
		}
		if _, err := rateAllocated(n, true); err != nil {
			return err
		}
		// 创建或编辑 Key 不改变祖先金额分配，即使个人余额耗尽也允许管理 Key；调用准入仍检查余额。
		if kind != "key" || n.Kind == "key" {
			if _, err := quotaUnused(n); err != nil {
				return err
			}
		}
		if n.Parent != "" && nodes[n.Parent] == nil {
			return ErrInvalid
		}
		n = nodes[n.Parent]
	}
	return nil
}

// singleTeamMember 拒绝第二个团队归属。参数 s/userID/teamID 为事务与身份；返回归属或数据库错误。
// 调用：创建团队、添加成员；已有本团队关系由原唯一约束处理，不自动移动消费历史。
func singleTeamMember(s *xorm.Session, userID, teamID string) error {
	var members []TeamMembership
	if err := s.Where("user_id = ?", userID).Find(&members); err != nil {
		return err
	}
	for _, m := range members {
		if m.TeamID != teamID {
			return ErrMultipleTeams
		}
	}
	return nil
}

// QuotaPath 检查调用节点及祖先的共享余额。参数 ctx/kind/id/hot 为上下文、主体与未落库消费读取器。
// 返回消费团队、组织、耗尽层级及错误；固定孩子保留额度，仅共享孩子受父级共享池限制。
// hot 的可选时间参数用于组织及个人各自的周期起点，其他节点保留累计消费语义。
// 调用：会话与密钥检查；检查前补执行到期组织及个人周期，失败拒绝准入；无法约束供应商最终计价超过最后余额的情况。
func (db *DB) QuotaPath(ctx context.Context, kind, id string, hot func(string, string, ...time.Time) float64) (string, string, string, error) {
	if err := db.ResetDueBudgets(ctx, time.Now()); err != nil {
		return "", "", "", err
	}
	s := db.session(ctx)
	defer s.Close()
	nodes, err := quotaTreeFor(s, kind, id)
	if err != nil {
		return "", "", "", err
	}
	return quotaPath(nodes, kind+":"+id, hot)
}

// QuotaAndRates 为首次准入共用一次归属树读取，避免预算和分钟计划分别扫描同一组织。
// 参数 ctx/kind/id/hot 与 QuotaPath 相同；返回计费团队、组织、耗尽层级、速率计划及读取错误。
// 网关首次准入调用；先补执行到期组织及个人周期，再读取预算及热消费，不占分钟容量，后续复核仍读取最新预算。
func (db *DB) QuotaAndRates(ctx context.Context, kind, id string, hot func(string, string, ...time.Time) float64) (string, string, string, live.RatePlan, error) {
	if err := db.ResetDueBudgets(ctx, time.Now()); err != nil {
		return "", "", "", live.RatePlan{}, err
	}
	s := db.session(ctx)
	defer s.Close()
	nodes, err := quotaTreeFor(s, kind, id)
	if err != nil {
		return "", "", "", live.RatePlan{}, err
	}
	team, org, scope, err := quotaPath(nodes, kind+":"+id, hot)
	if err != nil || scope != "" {
		return team, org, scope, live.RatePlan{}, err
	}
	plan, err := buildRatePlan(nodes, kind+":"+id)
	return team, org, scope, plan, err
}

// quotaPath 检查已读取的归属树及热消费，参数 target 为层级前缀 ID，hot 可空。
// 返回计费归属及耗尽层级；供 QuotaPath/QuotaAndRates 共用，不更新数据库或速率计数。
// 组织及个人各自传入持久化周期起点，只计本层当前周期待入库费用；其他层级保持累计额度与保留额规则。
func quotaPath(nodes map[string]*quotaNode, target string, hot func(string, string, ...time.Time) float64) (string, string, string, error) {
	n := nodes[target]
	if n == nil {
		return "", "", "", ErrNotFound
	}
	// 热账单同时影响父级消费和子级未用保留额，避免重复占用。
	if hot != nil {
		for _, node := range nodes {
			// 只有目标固定 Key 需要自身热费用；兄弟 Key 不预留，读取失败不能阻塞其他入口。
			if node.Kind == "key" && (node.Limit == nil || node != n) {
				continue
			}
			ref := node.ID
			if node.Kind == "key" {
				ref = node.HotID
			}
			if (node.Kind == "org" || node.Kind == "user") && node.PeriodStart != nil {
				node.Spend += hot(node.Kind, ref, *node.PeriodStart)
			} else {
				node.Spend += hot(node.Kind, ref)
			}
			if math.IsNaN(node.Spend) || math.IsInf(node.Spend, 0) {
				return "", "", "", ErrQuotaUnavailable
			}
		}
	}
	team, org, scope := "", "", ""
	var child *quotaNode
	credit := 0.0
	for n != nil {
		if n.Parent == "ambiguous" {
			return "", "", "", ErrMultipleTeams
		}
		if n.Kind == "team" {
			team = n.ID
		}
		if n.Kind == "org" {
			org = n.ID
		}
		spent := n.Spend
		if n.Limit != nil {
			reserved := 0.0
			for _, c := range n.Children {
				amount := quotaReserved(c)
				// 固定后代的保留权穿过共享层，不能被自己的保留额再次拦截。
				if c == child {
					amount = math.Max(0, amount-credit)
				}
				reserved += amount
			}
			// 软预算只在实际可用余额非正时停止，不用预占误差阈值吞掉微小正余额。
			if spent+reserved >= *n.Limit && scope == "" {
				scope = n.Kind
			}
			// 只有真正预留资金的组织/团队/个人能抵扣祖先预留；Key 上限不产生可转移权益。
			if n.Kind != "key" {
				credit = math.Max(0, *n.Limit-spent)
			} else {
				credit = 0
			}
		}
		if n.Parent != "" && nodes[n.Parent] == nil {
			return team, org, "", ErrInvalid
		}
		child = n
		n = nodes[n.Parent]
	}
	return team, org, scope, nil
}

// quotaReserved 计算运行时未使用的保留金额；参数 n 含热消费，返回非负金额。
// 调用：QuotaPath；固定额度耗尽后的保留额为零，消费本身仍在祖先账单中。
func quotaReserved(n *quotaNode) float64 {
	// Key 不向个人或更高层保留未使用额度，控制台与兄弟 Key 可共享实际余额。
	if n.Kind == "key" {
		return 0
	}
	if n.Limit != nil {
		return math.Max(0, *n.Limit-n.Spend)
	}
	amount := 0.0
	for _, c := range n.Children {
		amount += quotaReserved(c)
	}
	return amount
}

// rateAllocated 计算固定 RPM 或 TPM 分配；参数 n 为节点、tokens 选择维度，返回向父级占用的容量或超配错误。
// 调用：所有额度树写入；共享层透传下级分配，固定层保留自身容量，整数相加不允许超过父级。
func rateAllocated(n *quotaNode, tokens bool) (int64, error) {
	allocated := int64(0)
	for _, c := range n.Children {
		value, err := rateAllocated(c, tokens)
		if err != nil {
			return 0, err
		}
		allocated += value
	}
	limit := n.RPM
	if tokens {
		limit = n.TPM
	}
	if limit == nil {
		return allocated, nil
	}
	if err := ValidateRates(limit); err != nil {
		return 0, err
	}
	if allocated > int64(*limit) {
		return 0, ErrRateAllocation
	}
	return int64(*limit), nil
}
