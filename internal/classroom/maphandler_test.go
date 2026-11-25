package classroom_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/classroom"
	"nongyu-backend-plus/internal/classroom/store"
	"nongyu-backend-plus/internal/platform"
)

// 本文件是校区底图的 HTTP 集成测（规格 §9 层二）。

// makeGIF 生成一张 w×h 的最小 GIF（用于上传测试）。
//
// 用真 GIF 而不是伪字节：上传路径按**魔数嗅探**判类型，
// 伪字节会被拒，测不到正常分支。
func makeGIF(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, w, h), []color.Color{
		color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255},
	})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("生成 GIF 失败: %v", err)
	}
	return buf.Bytes()
}

// uploadMap 以 multipart 上传底图。
func uploadMap(t *testing.T, r *gin.Engine, campus string, body []byte, filename string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	if campus != "" {
		_ = w.WriteField("campus", campus)
	}
	if body != nil {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition",
			`form-data; name="file"; filename="`+filename+`"`)
		h.Set("Content-Type", "application/octet-stream")
		part, err := w.CreatePart(h)
		if err != nil {
			t.Fatalf("构造 multipart 失败: %v", err)
		}
		if _, err := part.Write(body); err != nil {
			t.Fatalf("写入 multipart 失败: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/classrooms/maps", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	out := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

// newMapEnv 构造只含地图能力的测试环境（不含预置底图）。
func newMapEnv(t *testing.T) (*gin.Engine, *store.MapMemoryRepo, *platform.MemoryObjectStore) {
	t.Helper()
	repo := store.NewBuildingMemoryRepo(func() time.Time { return t0 })
	mapRepo := store.NewMapMemoryRepo(func() time.Time { return t0 })
	objStore := platform.NewMemoryObjectStore()

	svc := classroom.New(classroom.Options{Repo: repo, Clock: fixedClock{now: t0}})
	svc.BindMaps(classroom.MapOptions{Repo: mapRepo, Store: objStore})

	r := gin.New()
	// 楼栋管理端点也要注册：像素派生的失败路径发生在「录入楼栋」这一步，
	// 只挂地图路由就测不到本功能最关键的那条约束。
	classroom.RegisterAdmin(r, svc)
	classroom.RegisterMapRoutes(r, svc)
	classroom.RegisterMapAdmin(r, svc)
	return r, mapRepo, objStore
}

// TestMap_UploadThenMetaAndImage 是本功能的主链路：
// 上传 → 取 meta（尺寸由解码得出）→ 取图字节。
func TestMap_UploadThenMetaAndImage(t *testing.T) {
	r, _, _ := newMapEnv(t)
	raw := makeGIF(t, 40, 30)

	code, body := uploadMap(t, r, "chengdu", raw, "cd.gif")
	if code != http.StatusOK {
		t.Fatalf("上传应成功，得到 %d %v", code, body)
	}

	data := body["data"].(map[string]any)
	// 尺寸必须来自解码，而不是客户端声明。
	if int(data["width"].(float64)) != 40 || int(data["height"].(float64)) != 30 {
		t.Errorf("尺寸应由解码得出 40×30，实际 %v×%v", data["width"], data["height"])
	}
	if data["uploaded"] != true {
		t.Error("上传后 uploaded 应为 true")
	}
	// 成都没有换算参数 → calibrated=false。
	if data["calibrated"] != false {
		t.Error("成都未标定，calibrated 应为 false")
	}
	if data["calibration"] != nil {
		t.Error("未标定时 calibration 应为 null")
	}

	// 取 meta。
	code, meta := do(t, r, http.MethodGet, "/classrooms/maps/chengdu/meta", nil)
	if code != http.StatusOK {
		t.Fatalf("meta 应 200，得到 %d", code)
	}
	if meta["data"].(map[string]any)["campusLabel"] != "成都" {
		t.Error("campusLabel 应为中文「成都」")
	}

	// 取图。
	req := httptest.NewRequest(http.MethodGet, "/classrooms/maps/chengdu", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("取图应 200，得到 %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/gif" {
		t.Errorf("Content-Type 应为 image/gif，实际 %q", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), raw) {
		t.Error("下发的字节应与上传的一致")
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("应带 ETag（内容指纹），便于客户端构造带版本的 URL")
	}
}

// TestMap_CalibratedFlagAndCorners 校验雅安的已标定状态与四角。
func TestMap_CalibratedFlagAndCorners(t *testing.T) {
	r, mapRepo, _ := newMapEnv(t)

	if err := mapRepo.UpsertMapImage(context.Background(), &classroom.Map{
		Campus: classroom.CampusYaan, ObjectKey: "k.gif", ContentType: "image/gif",
		SizeBytes: 100, Width: 12488, Height: 5586, CoordSystem: "GCJ-02", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := mapRepo.SetCalibration(context.Background(), classroom.CampusYaan,
		&yaanCalFake, "测试"); err != nil {
		t.Fatal(err)
	}

	code, body := do(t, r, http.MethodGet, "/classrooms/maps/yaan/meta", nil)
	if code != http.StatusOK {
		t.Fatalf("meta 应 200，得到 %d", code)
	}
	data := body["data"].(map[string]any)
	if data["calibrated"] != true {
		t.Fatal("雅安已标定，calibrated 应为 true")
	}
	if data["coordSystem"] != "GCJ-02" {
		t.Error("必须显式下发坐标系标识，避免客户端叠加 WGS84 底图时静默偏移")
	}

	cal := data["calibration"].(map[string]any)
	if cal["ar"].(float64) == 0 {
		t.Error("换算参数应下发")
	}

	corners := data["corners"].(map[string]any)
	if len(corners) != 5 {
		t.Fatalf("应下发 4 角 + 中心，实际 %d", len(corners))
	}
	lt := corners["leftTop"].(map[string]any)
	// 与参考脚本实算值一致（保留 6 位小数）。
	if d := lt["lng"].(float64) - 102.979373; d > 1e-6 || d < -1e-6 {
		t.Errorf("左上角经度错：%v", lt["lng"])
	}
}

// TestMap_NotFoundBeforeUpload 未上传的校区取图应 404。
func TestMap_NotFoundBeforeUpload(t *testing.T) {
	r, _, _ := newMapEnv(t)

	code, body := do(t, r, http.MethodGet, "/classrooms/maps/dujiangyan", nil)
	if code != http.StatusNotFound {
		t.Fatalf("未上传应 404，得到 %d %v", code, body)
	}
}

// TestMap_IllegalCampusRejected 校区是封闭集合。
func TestMap_IllegalCampusRejected(t *testing.T) {
	r, _, _ := newMapEnv(t)

	for _, bad := range []string{"shanghai", "0", "1", "2", "雅安"} {
		code, _ := do(t, r, http.MethodGet, "/classrooms/maps/"+bad+"/meta", nil)
		if code != http.StatusBadRequest {
			t.Errorf("校区 %q 应 400，得到 %d", bad, code)
		}
	}
}

// TestMap_RejectsNonImage 伪装成图片的文本必须被拒（魔数嗅探）。
func TestMap_RejectsNonImage(t *testing.T) {
	r, _, _ := newMapEnv(t)

	code, body := uploadMap(t, r, "yaan", []byte("this is not an image"), "fake.gif")
	if code != http.StatusBadRequest {
		t.Fatalf("非图片应 400，得到 %d %v", code, body)
	}
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "图片格式") {
		t.Errorf("错误信息应说明格式问题，实际 %q", msg)
	}
}

// TestMap_ReplaceDeletesOldObject 换图应删掉旧对象，避免存储泄漏。
func TestMap_ReplaceDeletesOldObject(t *testing.T) {
	r, mapRepo, objStore := newMapEnv(t)

	if code, body := uploadMap(t, r, "chengdu", makeGIF(t, 10, 10), "a.gif"); code != http.StatusOK {
		t.Fatalf("首次上传失败 %d %v", code, body)
	}
	m1, err := mapRepo.FindMapByCampus(context.Background(), classroom.CampusChengdu)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := m1.ObjectKey

	// 换一张不同内容的图（内容指纹不同 → key 不同）。
	if code, body := uploadMap(t, r, "chengdu", makeGIF(t, 20, 20), "b.gif"); code != http.StatusOK {
		t.Fatalf("二次上传失败 %d %v", code, body)
	}
	m2, err := mapRepo.FindMapByCampus(context.Background(), classroom.CampusChengdu)
	if err != nil {
		t.Fatal(err)
	}
	if m2.ObjectKey == oldKey {
		t.Error("换图后 object_key 必须变化（含内容指纹），否则客户端强缓存拿不到新图")
	}
	if m2.Width != 20 || m2.Height != 20 {
		t.Errorf("尺寸应更新为 20×20，实际 %d×%d", m2.Width, m2.Height)
	}

	// 旧对象应已被删除。
	if _, err := objStore.Get(context.Background(), oldKey); err == nil {
		t.Error("旧对象应被删除，避免存储泄漏")
	}
}

// TestMap_ReplaceKeepsCalibration 换图不等于重新标定。
//
// 若换图顺手把参数清空，会让本来已标定的校区突然无法用像素定位，
// 而这种退化在客户端表现为「坐标没了」，很难追溯到「有人换了张图」。
func TestMap_ReplaceKeepsCalibration(t *testing.T) {
	r, mapRepo, _ := newMapEnv(t)

	if err := mapRepo.SetCalibration(context.Background(), classroom.CampusYaan,
		&yaanCalFake, "原始标定"); err != nil {
		t.Fatal(err)
	}
	if code, body := uploadMap(t, r, "yaan", makeGIF(t, 8, 8), "y.gif"); code != http.StatusOK {
		t.Fatalf("上传失败 %d %v", code, body)
	}

	m, err := mapRepo.FindMapByCampus(context.Background(), classroom.CampusYaan)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Calibrated() {
		t.Fatal("换图后换算参数应保留")
	}
	if m.CalibrationNote != "原始标定" {
		t.Errorf("标定说明应保留，实际 %q", m.CalibrationNote)
	}
}

// TestMap_UploadWithoutCampus 缺校区应 400。
func TestMap_UploadWithoutCampus(t *testing.T) {
	r, _, _ := newMapEnv(t)

	code, body := uploadMap(t, r, "", makeGIF(t, 10, 10), "a.gif")
	if code != http.StatusBadRequest {
		t.Fatalf("缺校区应 400，得到 %d %v", code, body)
	}
}

// TestMap_UploadWithoutFile 缺文件应 400。
func TestMap_UploadWithoutFile(t *testing.T) {
	r, _, _ := newMapEnv(t)

	code, body := uploadMap(t, r, "yaan", nil, "")
	if code != http.StatusBadRequest {
		t.Fatalf("缺文件应 400，得到 %d %v", code, body)
	}
}

// TestMap_BuildingPixelRejectedWhenUncalibrated 是本功能最关键的一条：
// 未标定的校区传像素必须显式失败，绝不退回雅安参数。
func TestMap_BuildingPixelRejectedWhenUncalibrated(t *testing.T) {
	r, _, _ := newMapEnv(t)

	// 成都有图但没有换算参数。
	if code, body := uploadMap(t, r, "chengdu", makeGIF(t, 100, 100), "cd.gif"); code != http.StatusOK {
		t.Fatalf("上传失败 %d %v", code, body)
	}

	code, body := do(t, r, http.MethodPost, "/admin/classrooms/buildings", map[string]any{
		"buildingNo": "1", "displayName": "一教",
		"campus": "chengdu", "mapPx": 10, "mapPy": 20,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("未标定校区传像素应 400，得到 %d %v", code, body)
	}
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "标定") {
		t.Errorf("错误信息应说明尚未标定，实际 %q", msg)
	}
}

// TestMap_BuildingPixelOutOfRange 像素越界应被拒。
func TestMap_BuildingPixelOutOfRange(t *testing.T) {
	r, _, _ := newMapEnv(t)

	// 预置一张小图（100×80）与参数。
	if code, body := uploadMap(t, r, "yaan", makeGIF(t, 100, 80), "y.gif"); code != http.StatusOK {
		t.Fatalf("上传失败 %d %v", code, body)
	}
	// 通过管理端点写参数。
	calBody := map[string]any{
		"ar": yaanCalFake.AR, "ai": yaanCalFake.AI,
		"tx0": yaanCalFake.TX0, "ty0": yaanCalFake.TY0,
		"lng0": yaanCalFake.LNG0, "lat0": yaanCalFake.LAT0,
		"kx": yaanCalFake.KX, "ky": yaanCalFake.KY,
		"note": "测试",
	}
	if code, body := do(t, r, http.MethodPut,
		"/admin/classrooms/maps/yaan/calibration", calBody); code != http.StatusOK {
		t.Fatalf("写参数失败 %d %v", code, body)
	}

	code, body := do(t, r, http.MethodPost, "/admin/classrooms/buildings", map[string]any{
		"buildingNo": "1", "displayName": "一教",
		"campus": "yaan", "mapPx": 9999, "mapPy": 20,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("越界像素应 400，得到 %d %v", code, body)
	}
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "超出") {
		t.Errorf("错误信息应说明越界，实际 %q", msg)
	}
}

// TestMap_BuildingRequiresPixel 强制像素：只传经纬度必须被拒。
//
// 这是维护者 2026-11-25 的决定「不兼容旧数据，强制要传」。
func TestMap_BuildingRequiresPixel(t *testing.T) {
	r, _, _ := newMapEnv(t)

	code, body := do(t, r, http.MethodPost, "/admin/classrooms/buildings", map[string]any{
		"buildingNo": "1", "displayName": "一教",
		"campus": "yaan", "lat": 29.98, "lng": 102.99,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("只传经纬度应 400，得到 %d %v", code, body)
	}
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "像素") {
		t.Errorf("错误信息应说明需要像素，实际 %q", msg)
	}
}

// TestMap_ListIncludesMissingCampuses 管理端列表要列出还没传图的校区。
//
// 管理员需要知道「成都还没传图」，而不是只看已有的行去猜缺了什么。
func TestMap_ListIncludesMissingCampuses(t *testing.T) {
	r, _, _ := newMapEnv(t)

	if code, body := uploadMap(t, r, "yaan", makeGIF(t, 10, 10), "y.gif"); code != http.StatusOK {
		t.Fatalf("上传失败 %d %v", code, body)
	}

	code, body := do(t, r, http.MethodGet, "/admin/classrooms/maps", nil)
	if code != http.StatusOK {
		t.Fatalf("列表应 200，得到 %d", code)
	}
	items := body["data"].([]any)
	if len(items) != 3 {
		t.Fatalf("应列出全部 3 个校区，实际 %d", len(items))
	}

	seen := map[string]bool{}
	for _, it := range items {
		seen[it.(map[string]any)["campus"].(string)] = true
	}
	for _, c := range []string{"yaan", "chengdu", "dujiangyan"} {
		if !seen[c] {
			t.Errorf("缺校区 %q", c)
		}
	}
}
