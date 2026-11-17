package secondclass

// 本文件是 #76「审计流水线验收补齐」的专属测试：在 #73/#75 已落地的实现上，
// 补齐三处缺口——(a) 审计写失败不阻断业务的行为测试；(b) 全部 14 个写类配方端点
// 的审计覆盖与脱敏；(c) 规格 §3.4 盲区固定措辞「须原样进实现与文档」的守卫。
// 不改动任何既有测试与实现，只新增断言。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/school"
)

// failingAuditStore 让 Append 恒返回 error，用来验证「审计是旁路」：
// 写失败绝不能把业务请求拖成 500（规格 §3.4 / GLOSSARY「审计流水」）。
type failingAuditStore struct {
	calls int
	err   error
}

func (f *failingAuditStore) Append(_ context.Context, _ platform.AuditEntry) error {
	f.calls++
	return f.err
}
func (f *failingAuditStore) Flush(context.Context) error { return nil }
func (f *failingAuditStore) Dropped() int64              { return 0 }
func (f *failingAuditStore) Close() error                { return nil }

// newCacheServerWithAudit 装配一个注入指定 AuditStore 的读三端点引擎。
// 与 scaffold 的 newCacheTestServer 同形，只多一个审计依赖注入口。
func newCacheServerWithAudit(t *testing.T, userID string, audit platform.AuditStore) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	clock := newFakeClock()
	cache := newMemCache(func() time.Time { return clock.Now() })
	v, err := NewSchemaValidator()
	if err != nil {
		t.Fatalf("编译 schema 失败: %v", err)
	}
	svc := New(Options{Cache: cache, Schema: v, Clock: clock, Audit: audit})
	engine := gin.New()
	if userID != "" {
		engine.Use(func(c *gin.Context) {
			c.Set(school.CtxUserID, userID)
			c.Next()
		})
	}
	RegisterCache(engine, svc)
	return engine
}

// ---- 验收标准 4：审计写失败不阻断业务 ----

func TestAudit_AppendFailureDoesNotBlockBusiness(t *testing.T) {
	sentinel := errors.New("模拟审计库写入失败")

	t.Run("写类配方端点", func(t *testing.T) {
		audit := &failingAuditStore{err: sentinel}
		engine := newRecipeTestServer(t, "student-1", audit)
		res := doJSON(t, engine, http.MethodGet, "/school/secondclass/recipe/"+writeRecipeIDs[0], "")
		if res.status != http.StatusOK {
			t.Fatalf("审计写失败不应把配方下发拖成 %d: %s", res.status, res.raw)
		}
		if res.body["id"] != writeRecipeIDs[0] {
			t.Errorf("响应体应是完整配方，实际 %s", res.raw)
		}
		if audit.calls == 0 {
			t.Error("审计 Append 未被调用，无法证明写失败被吞")
		}
	})

	t.Run("配方下发-cache_miss", func(t *testing.T) {
		audit := &failingAuditStore{err: sentinel}
		engine := newCacheServerWithAudit(t, "student-1", audit)
		res := doJSON(t, engine, http.MethodGet, cachePath+"secondclassCredit", "")
		if res.status != http.StatusOK || res.body["code"] != "cache_miss" {
			t.Fatalf("审计写失败不应影响配方下发，实际 %d: %s", res.status, res.raw)
		}
		if audit.calls == 0 {
			t.Error("审计 Append 未被调用，无法证明写失败被吞")
		}
	})

	t.Run("副本写入", func(t *testing.T) {
		audit := &failingAuditStore{err: sentinel}
		engine := newCacheServerWithAudit(t, "student-1", audit)
		body, _ := submitBodyFromFixture(t, "1-学期学分.json")
		res := doJSON(t, engine, http.MethodPost, cachePath+"secondclassCredit", string(body))
		if res.status != http.StatusOK {
			t.Fatalf("审计写失败不应把副本写入拖成 %d: %s", res.status, res.raw)
		}
		if audit.calls == 0 {
			t.Error("审计 Append 未被调用，无法证明写失败被吞")
		}
	})

	t.Run("缓存失效", func(t *testing.T) {
		audit := &failingAuditStore{err: sentinel}
		engine := newCacheServerWithAudit(t, "student-1", audit)
		res := doJSON(t, engine, http.MethodDelete, cachePath+"secondclassCredit", "")
		if res.status != http.StatusOK {
			t.Fatalf("审计写失败不应把失效拖成 %d: %s", res.status, res.raw)
		}
		if audit.calls == 0 {
			t.Error("审计 Append 未被调用，无法证明写失败被吞")
		}
	})
}

// ---- 验收标准 1 / 5：全部写类配方端点都记审计，且六字段无敏感值 ----

func TestAudit_AllWriteRecipeEndpointsEmitSpecNameWithoutSecrets(t *testing.T) {
	audit := &recordingAuditStore{}
	engine := newRecipeTestServer(t, "student-1", audit)

	for _, id := range writeRecipeIDs {
		res := doJSON(t, engine, http.MethodGet, "/school/secondclass/recipe/"+id, "")
		if res.status != http.StatusOK {
			t.Errorf("GET %q 应 200，实际 %d: %s", id, res.status, res.raw)
		}
	}

	if len(audit.entries) != len(writeRecipeIDs) {
		t.Fatalf("应恰好记 %d 条审计，实际 %d: %+v", len(writeRecipeIDs), len(audit.entries), audit.entries)
	}
	seen := map[string]int{}
	for _, e := range audit.entries {
		if e.Action != "secondclass.recipe_issue" || e.Result != "ok" {
			t.Errorf("审计 action/result 不符合规格: %+v", e)
		}
		seen[e.Target]++
		// 六字段里绝不混入 token / GPS / 扫码时间戳具体值。
		blob := e.UserID + e.Action + e.Target + e.Result + e.RequestID
		for _, forbidden := range []string{
			"Bearer", "x-access-token:", "1730000000000", "client-long=", "client-lat=",
		} {
			if strings.Contains(blob, forbidden) {
				t.Errorf("审计字段里出现敏感值 %q: %+v", forbidden, e)
			}
		}
		// 响应体（配方里的上游路径/参数）不得进审计：target 只是配方 id，不含斜杠。
		if strings.Contains(e.Target, "/") {
			t.Errorf("审计 target 不应含响应体里的上游路径: %+v", e)
		}
	}
	for _, id := range writeRecipeIDs {
		if seen[id] != 1 {
			t.Errorf("配方 %q 应恰好一条审计，实际 %d", id, seen[id])
		}
	}
}

// ---- 验收标准 3：审计只用六字段、不为二课加字段 ----

func TestAudit_AuditEntryHasExactlySixIdentifierFields(t *testing.T) {
	// 规格 §3.4：「只记 AuditEntry 六字段，不加字段」。期望值来自规格，
	// 不从实现里推导，防止有人给审计条目加字段（尤其是为二课加值字段）。
	typ := reflect.TypeOf(platform.AuditEntry{})
	want := []string{"RequestID", "UserID", "Action", "Target", "Result", "OccurredAt"}
	if typ.NumField() != len(want) {
		t.Fatalf("AuditEntry 应恰好 6 个标识性字段，实际 %d", typ.NumField())
	}
	for i, name := range want {
		if got := typ.Field(i).Name; got != name {
			t.Errorf("AuditEntry 第 %d 个字段应为 %q，实际 %q", i, name, got)
		}
	}
	// 行为化守卫：序列化后恰好六个 key，没有为二课塞进任何额外字段。
	b, err := json.Marshal(platform.AuditEntry{})
	if err != nil {
		t.Fatalf("序列化 AuditEntry 失败: %v", err)
	}
	var keys map[string]any
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatalf("反序列化 AuditEntry 失败: %v", err)
	}
	if len(keys) != 6 {
		t.Errorf("序列化后应恰好六个字段，实际 %d: %s", len(keys), b)
	}
}

// ---- 验收标准 6：盲区固定措辞「须原样进实现与文档」 ----

// blindSpotWording 逐字取自规格 §3.4（docs/specs/spec-学校二课接入.md:81）。
const blindSpotWording = "后端对二课全部请求无观测能力，不代发、不转发、不缓存其响应，无法限流、无法记审计、无法判定失败原因；不得声称已限流或已审计。"

func TestDoc_BlindSpotWordingIsVerbatimInPackageDoc(t *testing.T) {
	// 规格 §3.4 标题即「盲区（须原样进实现与文档）」：这段固定措辞必须逐字
	// 落在包注释里（doc.go），且 audit() 注释明确引用，防止后人删改或弱化。
	raw, err := os.ReadFile("doc.go")
	if err != nil {
		t.Fatalf("读 doc.go 失败: %v", err)
	}
	if !strings.Contains(string(raw), blindSpotWording) {
		t.Errorf("doc.go 包注释缺规格 §3.4 盲区固定措辞原文（须逐字一致）")
	}
	svc, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatalf("读 service.go 失败: %v", err)
	}
	if !strings.Contains(string(svc), "盲区") {
		t.Errorf("service.go 的 audit() 注释应引用规格 §3.4 盲区")
	}
}
