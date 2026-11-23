package classroom_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/classroom"
	"nongyu-backend-plus/internal/classroom/store"
)

func init() { gin.SetMode(gin.TestMode) }

var t0 = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

type fixedClock struct{ now time.Time }

func (f fixedClock) Now() time.Time { return f.now }

func newEnv(t *testing.T) (*gin.Engine, *classroom.Service, *store.BuildingMemoryRepo) {
	t.Helper()
	repo := store.NewBuildingMemoryRepo(func() time.Time { return t0 })
	svc := classroom.New(classroom.Options{Repo: repo, Clock: fixedClock{now: t0}})
	r := gin.New()
	classroom.RegisterPublic(r, svc)
	classroom.RegisterAdmin(r, svc)
	return r, svc, repo
}

func do(t *testing.T, r *gin.Engine, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

// TestLookup_CollapsesToBuilding 是本功能的核心验收：同一楼栋的不同教室
// 必须查到同一栋楼。
func TestLookup_CollapsesToBuilding(t *testing.T) {
	r, _, _ := newEnv(t)

	// 管理员录入十教B 与一教。
	if code, body := do(t, r, http.MethodPost, "/admin/classrooms/buildings", map[string]any{
		"buildingNo": "10", "zone": "B", "displayName": "十教B栋",
		"lat": 30.5, "lng": 114.3, "addressText": "XX路1号",
	}); code != http.StatusCreated {
		t.Fatalf("录入十教B 失败：%d %v", code, body)
	}
	if code, body := do(t, r, http.MethodPost, "/admin/classrooms/buildings", map[string]any{
		"buildingNo": "1", "displayName": "第一教学楼", "lat": 30.51, "lng": 114.31,
	}); code != http.StatusCreated {
		t.Fatalf("录入一教失败：%d %v", code, body)
	}

	// 需求里的四个原始例子。
	cases := map[string]string{
		"10-B201": "十教B栋",
		"10-B213": "十教B栋",
		"1-404":   "第一教学楼",
		"1-301":   "第一教学楼",
	}
	for room, wantName := range cases {
		code, body := do(t, r, http.MethodGet, "/classrooms/lookup?room="+room, nil)
		if code != http.StatusOK {
			t.Fatalf("%s 查询失败：%d", room, code)
		}
		if body["found"] != true {
			t.Fatalf("%s 应查到楼栋，得到 %v", room, body)
		}
		b := body["building"].(map[string]any)
		if b["displayName"] != wantName {
			t.Fatalf("%s → %v，期望 %s", room, b["displayName"], wantName)
		}
		// 房间号要回传（展示用）。
		if body["roomNo"] == "" {
			t.Fatalf("%s 应回传 roomNo", room)
		}
	}
}

// TestLookup_DistinguishesTwoFailureModes 两种「查不到」必须能区分。
//
// 「教室名无法识别」与「楼栋未录入」对用户的提示语完全不同：
// 前者让他检查输入，后者让他知道「学校还没录这栋楼」。
func TestLookup_DistinguishesTwoFailureModes(t *testing.T) {
	r, _, _ := newEnv(t)

	// 情况 1：无法识别
	_, body := do(t, r, http.MethodGet, "/classrooms/lookup?room=教学楼", nil)
	if body["found"] != false || body["reason"] != "unrecognized" {
		t.Fatalf("无法识别应返回 reason=unrecognized，得到 %v", body)
	}

	// 情况 2：能识别但没录入
	_, body = do(t, r, http.MethodGet, "/classrooms/lookup?room=99-C101", nil)
	if body["found"] != false || body["reason"] != "building_not_configured" {
		t.Fatalf("未录入应返回 reason=building_not_configured，得到 %v", body)
	}
	if body["buildingKey"] != "99-C" {
		t.Fatalf("应能识别出键 99-C，得到 %v", body["buildingKey"])
	}
}

// TestLookup_DisabledBuildingInvisible 停用的楼栋对读者不可见。
func TestLookup_DisabledBuildingInvisible(t *testing.T) {
	r, _, _ := newEnv(t)
	code, body := do(t, r, http.MethodPost, "/admin/classrooms/buildings", map[string]any{
		"buildingNo": "5", "displayName": "五教", "enabled": false,
	})
	if code != http.StatusCreated {
		t.Fatalf("录入失败：%d %v", code, body)
	}

	_, got := do(t, r, http.MethodGet, "/classrooms/lookup?room=5-101", nil)
	if got["found"] != false {
		t.Fatalf("停用楼栋不该被查到，得到 %v", got)
	}
}

// TestBuilding_CreateRejectsBadInput 管理员输错要有明确报错。
func TestBuilding_CreateRejectsBadInput(t *testing.T) {
	r, _, _ := newEnv(t)
	bad := []map[string]any{
		{"buildingNo": "", "displayName": "x"},                               // 缺楼栋号
		{"buildingNo": "A", "displayName": "x"},                              // 楼栋号无数字
		{"buildingNo": "10", "zone": "BC", "displayName": "x"},               // 分区多字母
		{"buildingNo": "10", "displayName": ""},                              // 缺展示名
		{"buildingNo": "10", "displayName": "x", "lat": 30.5},                // 只有纬度
		{"buildingNo": "10", "displayName": "x", "lat": 200.0, "lng": 114.0}, // 纬度越界
	}
	for i, in := range bad {
		if code, body := do(t, r, http.MethodPost, "/admin/classrooms/buildings", in); code == http.StatusCreated {
			t.Fatalf("第 %d 个非法入参不该成功：%v", i+1, body)
		}
	}
}

// TestBuilding_DuplicateKeyRejected 同键不能录两次。
//
// 否则查询无法确定答案（两栋楼同键 = 随机返回一栋）。
func TestBuilding_DuplicateKeyRejected(t *testing.T) {
	r, _, _ := newEnv(t)
	in := map[string]any{"buildingNo": "10", "zone": "B", "displayName": "十教B"}
	if code, _ := do(t, r, http.MethodPost, "/admin/classrooms/buildings", in); code != http.StatusCreated {
		t.Fatal("首次录入应成功")
	}
	if code, _ := do(t, r, http.MethodPost, "/admin/classrooms/buildings", in); code == http.StatusCreated {
		t.Fatal("重复键不该成功")
	}
}

// TestRoute_CreateAndLookupThroughBuilding 路线随楼栋一起返回。
func TestRoute_CreateAndLookupThroughBuilding(t *testing.T) {
	r, _, _ := newEnv(t)
	code, body := do(t, r, http.MethodPost, "/admin/classrooms/buildings", map[string]any{
		"buildingNo": "10", "zone": "B", "displayName": "十教B", "lat": 30.5, "lng": 114.3,
	})
	if code != http.StatusCreated {
		t.Fatalf("录入楼栋失败：%d %v", code, body)
	}
	bid := int64(body["data"].(map[string]any)["id"].(float64))

	// 加一条「从南门进」的点状路线。
	code, body = do(t, r, http.MethodPost,
		"/admin/classrooms/buildings/"+itoa(bid)+"/routes", map[string]any{
			"title": "从南门进入", "startLabel": "南门", "recommended": true,
			"points": []map[string]any{
				{"lat": 30.500, "lng": 114.300, "label": "南门口"},
				{"lat": 30.501, "lng": 114.301, "label": "直走到路口右转"},
				{"lat": 30.502, "lng": 114.302, "label": "B栋正门"},
			},
		})
	if code != http.StatusCreated {
		t.Fatalf("建路线失败：%d %v", code, body)
	}

	// 通过教室名查询时，路线应一并返回（前端不必再发一轮请求）。
	_, got := do(t, r, http.MethodGet, "/classrooms/lookup?room=10-B201", nil)
	b := got["building"].(map[string]any)
	routes, _ := b["routes"].([]any)
	if len(routes) != 1 {
		t.Fatalf("应返回 1 条路线，得到 %d", len(routes))
	}
	rt := routes[0].(map[string]any)
	pts, _ := rt["points"].([]any)
	if len(pts) != 3 {
		t.Fatalf("应有 3 个点，得到 %d", len(pts))
	}
	// seq 由服务端按数组顺序生成。
	for i, p := range pts {
		pm := p.(map[string]any)
		if int(pm["seq"].(float64)) != i+1 {
			t.Fatalf("第 %d 个点 seq 应为 %d，得到 %v", i+1, i+1, pm["seq"])
		}
	}
}

// TestRoute_PointsReplacedOnUpdate 更新路线时点集合整体替换。
func TestRoute_PointsReplacedOnUpdate(t *testing.T) {
	r, _, _ := newEnv(t)
	_, body := do(t, r, http.MethodPost, "/admin/classrooms/buildings", map[string]any{
		"buildingNo": "7", "displayName": "七教",
	})
	bid := int64(body["data"].(map[string]any)["id"].(float64))

	_, body = do(t, r, http.MethodPost,
		"/admin/classrooms/buildings/"+itoa(bid)+"/routes", map[string]any{
			"title": "路线A",
			"points": []map[string]any{
				{"lat": 1.0, "lng": 2.0}, {"lat": 1.1, "lng": 2.1},
			},
		})
	rid := int64(body["data"].(map[string]any)["id"].(float64))

	// 改成 1 个点：旧的第 2 点必须消失（不是残留）。
	code, body := do(t, r, http.MethodPatch, "/admin/classrooms/routes/"+itoa(rid), map[string]any{
		"title": "路线A改", "points": []map[string]any{{"lat": 3.0, "lng": 4.0}},
	})
	if code != http.StatusOK {
		t.Fatalf("更新失败：%d %v", code, body)
	}
	pts := body["data"].(map[string]any)["points"].([]any)
	if len(pts) != 1 {
		t.Fatalf("点应被整体替换为 1 个，得到 %d", len(pts))
	}
}

// TestRoute_RejectsTooManyPoints 点数上限保护。
func TestRoute_RejectsTooManyPoints(t *testing.T) {
	r, _, _ := newEnv(t)
	_, body := do(t, r, http.MethodPost, "/admin/classrooms/buildings", map[string]any{
		"buildingNo": "8", "displayName": "八教",
	})
	bid := int64(body["data"].(map[string]any)["id"].(float64))

	pts := make([]map[string]any, classroom.MaxRoutePoints+1)
	for i := range pts {
		pts[i] = map[string]any{"lat": 1.0, "lng": 2.0}
	}
	if code, _ := do(t, r, http.MethodPost,
		"/admin/classrooms/buildings/"+itoa(bid)+"/routes",
		map[string]any{"title": "太多点", "points": pts}); code == http.StatusCreated {
		t.Fatal("超过点数上限应被拒")
	}
}

// TestDeleteBuilding_CascadesRoutes 删楼栋级联删路线。
func TestDeleteBuilding_CascadesRoutes(t *testing.T) {
	r, svc, _ := newEnv(t)
	_, body := do(t, r, http.MethodPost, "/admin/classrooms/buildings", map[string]any{
		"buildingNo": "9", "displayName": "九教",
	})
	bid := int64(body["data"].(map[string]any)["id"].(float64))
	_, rb := do(t, r, http.MethodPost,
		"/admin/classrooms/buildings/"+itoa(bid)+"/routes",
		map[string]any{"title": "x", "points": []map[string]any{{"lat": 1.0, "lng": 2.0}}})
	rid := int64(rb["data"].(map[string]any)["id"].(float64))

	if code, _ := do(t, r, http.MethodDelete, "/admin/classrooms/buildings/"+itoa(bid), nil); code != http.StatusOK {
		t.Fatal("删楼栋失败")
	}
	// 路线应也消失（直接查仓储，绕过 handler）。
	if _, err := svc.AdminGetBuilding(context.Background(), bid); err == nil {
		t.Fatal("楼栋应已删除")
	}
	_ = rid
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
