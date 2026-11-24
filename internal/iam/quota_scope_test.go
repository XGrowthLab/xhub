package iam

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestQuotaAndRatesScope 验证请求内共用额度及速率归属；前置两个隔离组织和一个个人，验证兄弟保留及跨组织隔离。
// 参数 t 为测试上下文，无返回；testDB 自动清理 schema，不修改共享数据或分钟计数。
func TestQuotaAndRatesScope(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()
	actor := Actor{}
	org, err := db.CreateOrg(ctx, actor, "caller", quotaLimit(100))
	if err != nil {
		t.Fatal(err)
	}
	team, err := db.CreateTeam(ctx, actor, TeamInput{OrganizationID: org.ID, Name: "caller"})
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := db.CreateTeam(ctx, actor, TeamInput{OrganizationID: org.ID, Name: "reserved", MaxBudget: quotaLimit(100)})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateOrg(ctx, actor, "unrelated", nil)
	if err != nil {
		t.Fatal(err)
	}
	otherTeam, err := db.CreateTeam(ctx, actor, TeamInput{OrganizationID: other.ID, Name: "unrelated"})
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, actor, UserInput{Email: "scope@example.com", Password: "password123", TeamID: team.ID})
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := db.CreateKey(ctx, actor, KeyInput{OwnerType: OwnerPersonal, UserID: user.ID})
	if err != nil {
		t.Fatal(err)
	}
	hotIDs := map[string]bool{}
	tid, oid, scope, empty, err := db.QuotaAndRates(ctx, "key", key.ID, func(kind, id string, _ ...time.Time) float64 { hotIDs[kind+":"+id] = true; return 0 })
	if err != nil || tid != team.ID || oid != org.ID || scope != "org" || len(empty.Nodes) != 0 {
		t.Fatalf("兄弟固定额度应阻止共享调用并不生成速率计划: team=%s org=%s scope=%s plan=%+v err=%v", tid, oid, scope, empty, err)
	}
	if !hotIDs["team:"+sibling.ID] || hotIDs["org:"+other.ID] || hotIDs["team:"+otherTeam.ID] {
		t.Fatalf("热消费读取范围错误: %+v", hotIDs)
	}
	// 释放兄弟保留后，合并读取与独立速率计划必须一致。
	if _, err := db.Engine.Exec("UPDATE teams SET max_budget = NULL WHERE id = ?", sibling.ID); err != nil {
		t.Fatal(err)
	}
	_, _, scope, plan, err := db.QuotaAndRates(ctx, "key", key.ID, nil)
	want, wantErr := db.RatePlan(ctx, "key", key.ID)
	if err != nil || wantErr != nil || scope != "" || !reflect.DeepEqual(plan, want) {
		t.Fatalf("共享读取计划不一致: scope=%s plan=%+v want=%+v errors=%v/%v", scope, plan, want, err, wantErr)
	}
	personal, err := db.CreateUser(ctx, actor, UserInput{Email: "personal-scope@example.com", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	personalKey, _, err := db.CreateKey(ctx, actor, KeyInput{OwnerType: OwnerPersonal, UserID: personal.ID})
	if err != nil {
		t.Fatal(err)
	}
	hotIDs = map[string]bool{}
	tid, oid, scope, plan, err = db.QuotaAndRates(ctx, "user", personal.ID, func(kind, id string, _ ...time.Time) float64 { hotIDs[kind+":"+id] = true; return 0 })
	if err != nil || tid != "" || oid != "" || scope != "" || len(plan.Nodes) != 2 || len(hotIDs) != 1 || !hotIDs["user:"+personal.ID] || hotIDs["key:"+personalKey.ID] {
		t.Fatalf("无团队个人必须只读取自己及个人密钥: %s %s %s %+v %+v %v", tid, oid, scope, plan, hotIDs, err)
	}
	for _, tc := range []struct {
		kind, id string
		want     error
	}{{"key", "missing", ErrNotFound}, {"user", "missing", ErrNotFound}, {"org", org.ID, ErrInvalid}} {
		_, _, _, _, err := db.QuotaAndRates(ctx, tc.kind, tc.id, nil)
		if !errors.Is(err, tc.want) {
			t.Fatalf("非法主体 %s/%s: got=%v want=%v", tc.kind, tc.id, err, tc.want)
		}
	}
}

// TestQuotaSnapshotSingleRead 验证真实 PostgreSQL 单次读取的节点、父子关系、额度、周期及速率与管理端一致。
// 参数 t 提供隔离 schema；修改团队限额后复读应立即生效，模拟历史多团队数据时必须拒绝，由 testDB 清理全部数据。
func TestQuotaSnapshotSingleRead(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()
	org, err := db.CreateOrg(ctx, Actor{}, "snapshot", quotaLimit(250))
	if err != nil {
		t.Fatal(err)
	}
	rpm, tpm := 17, 301
	team, err := db.CreateTeam(ctx, Actor{}, TeamInput{OrganizationID: org.ID, Name: "primary", RPMLimit: &rpm, TPMLimit: &tpm})
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := db.CreateTeam(ctx, Actor{}, TeamInput{OrganizationID: org.ID, Name: "sibling", MaxBudget: quotaLimit(20)})
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUser(ctx, Actor{}, UserInput{Email: "snapshot@example.invalid", Password: "password123", TeamID: team.ID})
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := db.CreateKey(ctx, Actor{}, KeyInput{OwnerType: OwnerPersonal, UserID: user.ID, MaxBudget: quotaLimit(14)})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := db.Engine.Exec("UPDATE users SET budget_period_start = ?, spend = 1.25 WHERE id = ?", start, user.ID); err != nil {
		t.Fatal(err)
	}
	s := db.session(ctx)
	defer s.Close()
	want, err := quotaTree(s)
	if err != nil {
		t.Fatal(err)
	}
	hook := &pendingSQLHook{}
	db.Engine.AddHook(hook)
	for _, subject := range []struct{ kind, id string }{{"key", key.ID}, {"user", user.ID}} {
		before := hook.count.Load()
		got, err := quotaTreeFor(s, subject.kind, subject.id)
		if err != nil || len(got) != len(want) || hook.count.Load()-before != 1 {
			t.Fatalf("%s 额度树应一次读全同组织节点：nodes=%d/%d sql=%d err=%v", subject.kind, len(got), len(want), hook.count.Load()-before, err)
		}
		for id, expected := range want {
			if got[id] == nil {
				t.Fatalf("额度树缺少节点 %s", id)
			}
			actual, reference := *got[id], *expected
			if len(actual.Children) != len(reference.Children) {
				t.Fatalf("%s 的直属子节点数错误：%d/%d", id, len(actual.Children), len(reference.Children))
			}
			actual.Children, reference.Children = nil, nil
			if !reflect.DeepEqual(actual, reference) {
				t.Fatalf("%s 的额度、速率、周期或父级不一致：got=%+v want=%+v", id, actual, reference)
			}
		}
	}
	if _, err := db.Engine.Exec("UPDATE teams SET max_budget = 0 WHERE id = ?", team.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckSoftBudgetBalance(ctx, "key", key.ID, nil); !errors.Is(err, ErrBudgetUnavailable) {
		t.Fatalf("下一次出站必须读取修改后的团队预算，实际错误 %v", err)
	}
	// 仅在本测试的独立 schema 中构造升级前可能遗留的双重归属，两个入口都应拒绝。
	if _, err := db.Engine.Exec("ALTER TABLE team_members DROP CONSTRAINT team_members_user_id_key"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine.Exec("INSERT INTO team_members(team_id, user_id) VALUES (?, ?)", sibling.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	for _, subject := range []struct{ kind, id string }{{"user", user.ID}, {"key", key.ID}} {
		if _, err := quotaTreeFor(s, subject.kind, subject.id); !errors.Is(err, ErrMultipleTeams) {
			t.Fatalf("%s 多团队归属必须拒绝：%v", subject.kind, err)
		}
	}
}
