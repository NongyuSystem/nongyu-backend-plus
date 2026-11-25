package classroom

import (
	"errors"
	"math"
	"testing"
)

// yaanCal 是雅安的标定参数，逐值抄自 yaan_px2ll.py。
//
// 这里刻意**再抄一遍常量**而不是从数据库或其他 Go 常量引用：
// 这是回归锚，防的是「有人改了换算系数的小数点」。抄错一位，
// 下面与 py 脚本实算值的比对立刻失败。
func yaanCal() Calibration {
	return Calibration{
		AR:   0.19050900468989643,
		AI:   0.0002290787340230358,
		TX0:  -1509.247805327019,
		TY0:  525.6390234504005,
		LNG0: 102.995025,
		LAT0: 29.978648,
		KX:   96426.68366048264,
		KY:   110540.0,
	}
}

// TestCalibration_ConstantsMatchReferenceScript 校验常量与参考脚本一致。
//
// 期望值由 `python yaan_px2ll.py` 实算得出（保留 6 位小数）：
//
//	左上 (102.979373, 29.983403)
//	右上 (103.004046, 29.983429)
//	右下 (103.004059, 29.973802)
//	左下 (102.979387, 29.973776)
//	中心 (102.991716, 29.978603)
func TestCalibration_ConstantsMatchReferenceScript(t *testing.T) {
	cal := yaanCal()
	const eps = 1e-6

	cases := []struct {
		name    string
		px, py  float64
		wantLng float64
		wantLat float64
	}{
		{"左上", 0, 0, 102.979373, 29.983403},
		{"右上", 12488, 0, 103.004046, 29.983429},
		{"右下", 12488, 5586, 103.004059, 29.973802},
		{"左下", 0, 5586, 102.979387, 29.973776},
		{"中心", 6244, 2793, 102.991716, 29.978603},
	}
	for _, c := range cases {
		lng, lat := cal.Px2LL(c.px, c.py)
		if math.Abs(lng-c.wantLng) > eps || math.Abs(lat-c.wantLat) > eps {
			t.Errorf("%s: 期望 (%.6f, %.6f)，实际 (%.6f, %.6f)",
				c.name, c.wantLng, c.wantLat, lng, lat)
		}
	}
}

// TestCalibration_RoundTrip 校验 px->ll->px 往返一致。
//
// 往返是最强的自洽性检查：正逆变换若有一处符号抄错，
// 往返结果会明显偏移，而单看正变换可能看不出问题。
func TestCalibration_RoundTrip(t *testing.T) {
	cal := yaanCal()
	const eps = 1e-6
	samples := [][2]float64{
		{0, 0}, {12488, 0}, {0, 5586}, {12488, 5586},
		{6244, 2793}, {4301.17, 1484.14}, {100, 5000}, {12000, 200},
	}
	for _, s := range samples {
		lng, lat := cal.Px2LL(s[0], s[1])
		px, py := cal.LL2Px(lng, lat)
		if math.Abs(px-s[0]) > eps || math.Abs(py-s[1]) > eps {
			t.Errorf("往返不一致: 原 (%.2f, %.2f) -> (%.6f, %.6f) -> (%.2f, %.2f)",
				s[0], s[1], lng, lat, px, py)
		}
	}
}

// TestCalibration_ZeroScaleIsInvalid 确认零尺度参数被视为不可用。
//
// KX/KY 为 0 会让 x/KX 无穷大，det 为 0 会直接除零。
// 这类参数是「有值但不可用」，必须与「未标定」一样被拒绝，
// 而不是产出 Inf/NaN 坐标污染数据。
func TestCalibration_ZeroScaleIsInvalid(t *testing.T) {
	zeroKX := yaanCal()
	zeroKX.KX = 0
	if zeroKX.valid() {
		t.Error("KX=0 应视为不可用")
	}

	zeroRot := yaanCal()
	zeroRot.AR, zeroRot.AI = 0, 0
	if zeroRot.valid() {
		t.Error("AR=AI=0 会让 det=0，应视为不可用")
	}
}

// TestMap_CalibratedAndUploaded 校验两个状态各自独立。
//
// 「已登记」与「已上传」是两件事：雅安在迁移里只种参数、不带图字节，
// 此时 Calibrated=true 而 Uploaded=false。混为一谈会让 GET 返回 500。
func TestMap_CalibratedAndUploaded(t *testing.T) {
	cal := yaanCal()

	seeded := &Map{Campus: CampusYaan, Cal: &cal}
	if !seeded.Calibrated() {
		t.Error("种了参数的雅安应 calibrated=true")
	}
	if seeded.Uploaded() {
		t.Error("没有 object_key 时应 uploaded=false")
	}

	uploaded := &Map{Campus: CampusYaan, Cal: &cal, ObjectKey: "classroom/maps/yaan-x.gif",
		Width: 12488, Height: 5586}
	if !uploaded.Calibrated() || !uploaded.Uploaded() {
		t.Error("有参数有 object_key 时应两者都为 true")
	}

	bare := &Map{Campus: CampusChengdu}
	if bare.Calibrated() {
		t.Error("成都未标定，calibrated 应为 false")
	}
}

// TestMap_ValidatePixel_NotCalibrated 是本功能最关键的一条：
// 未标定的校区传像素必须显式失败，绝不退回雅安参数。
func TestMap_ValidatePixel_NotCalibrated(t *testing.T) {
	m := &Map{Campus: CampusChengdu, ObjectKey: "classroom/maps/chengdu-x.png",
		Width: 1000, Height: 800}

	_, _, err := m.ValidatePixel(100, 100)
	if !errors.Is(err, ErrNotCalibrated) {
		t.Fatalf("成都未标定应返回 ErrNotCalibrated，实际 %v", err)
	}
}

// TestMap_ValidatePixel_NotUploaded 确认无图时不能用像素。
func TestMap_ValidatePixel_NotUploaded(t *testing.T) {
	cal := yaanCal()
	m := &Map{Campus: CampusYaan, Cal: &cal} // 有参数但没图

	_, _, err := m.ValidatePixel(100, 100)
	if err == nil {
		t.Fatal("未上传底图时传像素应报错")
	}
	if errors.Is(err, ErrNotCalibrated) {
		t.Error("这是「未上传」不是「未标定」，错误语义应区分")
	}
}

// TestMap_ValidatePixel_OutOfRange 校验越界像素被拒。
func TestMap_ValidatePixel_OutOfRange(t *testing.T) {
	cal := yaanCal()
	m := &Map{Campus: CampusYaan, Cal: &cal, ObjectKey: "k", Width: 12488, Height: 5586}

	bad := [][2]float64{
		{-1, 100}, {100, -1},
		{12489, 100}, {100, 5587},
	}
	for _, p := range bad {
		if _, _, err := m.ValidatePixel(p[0], p[1]); err == nil {
			t.Errorf("越界像素 (%.0f, %.0f) 应被拒", p[0], p[1])
		}
	}

	// 边界值本身合法。
	ok := [][2]float64{{0, 0}, {12488, 5586}, {12488, 0}, {0, 5586}}
	for _, p := range ok {
		if _, _, err := m.ValidatePixel(p[0], p[1]); err != nil {
			t.Errorf("边界像素 (%.0f, %.0f) 应合法，实际 %v", p[0], p[1], err)
		}
	}
}

// TestMap_ValidatePixel_DerivesGeo 确认像素会派生出正确的经纬度。
func TestMap_ValidatePixel_DerivesGeo(t *testing.T) {
	cal := yaanCal()
	m := &Map{Campus: CampusYaan, Cal: &cal, ObjectKey: "k", Width: 12488, Height: 5586}

	lng, lat, err := m.ValidatePixel(6244, 2793)
	if err != nil {
		t.Fatalf("中心点应合法: %v", err)
	}
	const eps = 1e-6
	if math.Abs(lng-102.991716) > eps || math.Abs(lat-29.978603) > eps {
		t.Errorf("中心点经纬度错：期望 (102.991716, 29.978603)，实际 (%.6f, %.6f)", lng, lat)
	}
}

// TestMap_Corners 校验四角与中心。
func TestMap_Corners(t *testing.T) {
	cal := yaanCal()
	m := &Map{Campus: CampusYaan, Cal: &cal, ObjectKey: "k", Width: 12488, Height: 5586}

	corners := m.Corners()
	if len(corners) != 5 {
		t.Fatalf("应返回 4 角 + 中心共 5 项，实际 %d", len(corners))
	}
	lt, ok := corners["leftTop"]
	if !ok {
		t.Fatal("缺 leftTop")
	}
	const eps = 1e-6
	if math.Abs(lt.Lat-29.983403) > eps || math.Abs(lt.Lng-102.979373) > eps {
		t.Errorf("左上角错：期望 (102.979373, 29.983403)，实际 (%.6f, %.6f)", lt.Lng, lt.Lat)
	}

	// 未标定时没有角落经纬度。
	bare := &Map{Campus: CampusChengdu, ObjectKey: "k", Width: 100, Height: 100}
	if bare.Corners() != nil {
		t.Error("未标定时 corners 应为 nil")
	}
}

// TestParseCampus 校验校区枚举是封闭集合。
func TestParseCampus(t *testing.T) {
	for _, raw := range []string{"yaan", "chengdu", "dujiangyan"} {
		c, ok := ParseCampus(raw)
		if !ok {
			t.Errorf("%q 应被接受", raw)
		}
		if c.Label() == string(c) {
			t.Errorf("%q 缺中文标签", raw)
		}
	}
	for _, raw := range []string{"", "0", "1", "2", "雅安", "YAAN", "shanghai"} {
		if _, ok := ParseCampus(raw); ok {
			t.Errorf("%q 不应被接受（校区是封闭集合）", raw)
		}
	}
}
