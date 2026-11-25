package classroom

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// 本文件是「校区底图 + 像素坐标」的模型与坐标换算。
// 规格见 docs/specs/spec-教室地图底图与像素坐标.md。

// Campus 是校区受控枚举（规格 §6.2）。
//
// 为什么用字符串而不是维护者最初提的数字 0/1/2：数字在库、日志、API 里都不可读，
// 排错时无法一眼确认；且数字一旦写进客户端就永久成为协议的一部分，改不动。
// 字符串枚举的可读性收益大过「省几个字节」。
type Campus string

const (
	CampusYaan       Campus = "yaan"
	CampusChengdu    Campus = "chengdu"
	CampusDujiangyan Campus = "dujiangyan"
)

// CampusLabel 返回中文显示名。
func (c Campus) Label() string {
	switch c {
	case CampusYaan:
		return "雅安"
	case CampusChengdu:
		return "成都"
	case CampusDujiangyan:
		return "都江堰"
	default:
		return string(c)
	}
}

// ParseCampus 把外部字符串解析成受控取值。
//
// 只认三个确定值：底图的「属于哪张图」必须是封闭集合，否则客户端的
// 像素坐标会落到一张根本不存在的图上。
func ParseCampus(raw string) (Campus, bool) {
	switch Campus(raw) {
	case CampusYaan, CampusChengdu, CampusDujiangyan:
		return Campus(raw), true
	default:
		return "", false
	}
}

// AllCampuses 返回全部合法校区（供列表与校验使用）。
func AllCampuses() []Campus {
	return []Campus{CampusYaan, CampusChengdu, CampusDujiangyan}
}

// ErrNotCalibrated 表示该校区尚未标定像素↔经纬度换算参数。
//
// 这是本功能最重要的一个错误：成都与都江堰目前没有标定参数，
// 传像素必须显式失败，绝不退回雅安参数或猜一个——那会静默产出错误坐标，
// 比报错危险得多。
var ErrNotCalibrated = errors.New("classroom: 该校区尚未标定像素-地理换算参数")

// Calibration 是像素 -> 经纬度的相似变换参数（Procrustes，复数形式）。
//
//	x   = AR*Px + AI*Py + TX0
//	y   = AI*Px - AR*Py + TY0
//	lng = LNG0 + x / KX
//	lat = LAT0 + y / KY
//
// AR/AI 是「旋转 + 尺度」的复数分量，TX0/TY0 是平移，LNG0/LAT0 是基准点，
// KX/KY 是该纬度下 1 经度、1 纬度对应的米数。
//
// 参数存数据库（classroom_map 表）而不是写死代码：只有雅安做过人工标定，
// 硬编码会逼实现者给未标定的校区伪造公式。
type Calibration struct {
	AR   float64
	AI   float64
	TX0  float64
	TY0  float64
	LNG0 float64
	LAT0 float64
	KX   float64
	KY   float64
}

// valid 判断参数是否可用于换算。
//
// 逆变换的分母是 det = -(AR² + AI²)，为 0 会直接除零；
// KX/KY 为 0 会让 x/ KX 无穷大。这类参数是「有值但不可用」，
// 与「全空=未标定」不同，但对外都应表现为不可换算。
func (c Calibration) valid() bool {
	if c.KX == 0 || c.KY == 0 {
		return false
	}
	return c.AR != 0 || c.AI != 0
}

// Px2LL 把图像像素转成 GCJ-02 经纬度。
//
// 像素语义：原点在图片左上角，px 向右递增，py 向下递增（规格 §4.1）。
// 这与部分地图 SDK 的「左下原点」相反，调用方需自行翻转。
func (c Calibration) Px2LL(px, py float64) (lng, lat float64) {
	x := c.AR*px + c.AI*py + c.TX0
	y := c.AI*px - c.AR*py + c.TY0
	return c.LNG0 + x/c.KX, c.LAT0 + y/c.KY
}

// LL2Px 是 Px2LL 的逆：(经度, 纬度) -> 图像像素。
func (c Calibration) LL2Px(lng, lat float64) (px, py float64) {
	x := (lng - c.LNG0) * c.KX
	y := (lat - c.LAT0) * c.KY
	det := -(c.AR*c.AR + c.AI*c.AI)
	if det == 0 {
		return math.NaN(), math.NaN()
	}
	px = (-c.AR*(x-c.TX0) - c.AI*(y-c.TY0)) / det
	py = (-c.AI*(x-c.TX0) + c.AR*(y-c.TY0)) / det
	return px, py
}

// Map 是一个校区的底图（规格 §6.1）。
type Map struct {
	ID     int64
	Campus Campus

	// ObjectKey 为空串表示「已登记但尚未上传图片字节」。
	// 雅安在迁移里只种换算参数，图要经管理端上传，就是这个状态。
	ObjectKey   string
	ContentType string
	SizeBytes   int64
	ETag        string

	// Width/Height 由上传时解码图片字节得出（不信客户端声明）。
	Width  int
	Height int

	// CoordSystem 是底层坐标系标识，固定 GCJ-02（高德）。
	// 显式下发而不是靠文档口头约定：GCJ-02 与 WGS84 叠加会有数十到数百米偏移。
	CoordSystem string

	// Cal 为 nil 表示该校区尚未标定，此时不能做像素换算。
	Cal *Calibration
	// CalibrationNote 是标定说明（控制点来源、残差、日期）。
	CalibrationNote string

	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Uploaded 判断该校区是否已上传底图字节。
func (m *Map) Uploaded() bool { return m != nil && m.ObjectKey != "" }

// Calibrated 判断该校区是否有可用的换算参数。
func (m *Map) Calibrated() bool { return m != nil && m.Cal != nil && m.Cal.valid() }

// Corner 是底图四角之一（像素 + 对应经纬度）。
type Corner struct {
	PX  float64
	PY  float64
	Lat float64
	Lng float64
}

// Corners 返回底图四角与中心的像素及经纬度。
//
// 由后端算好下发，避免每个客户端各写一遍换算——换算参数抄错一位，
// 客户端看到的是「点掉到湖里」，而不是一个显眼的错误。
//
// 未标定时返回 nil（没有换算就无所谓角落经纬度）。
func (m *Map) Corners() map[string]Corner {
	if !m.Calibrated() || m.Width <= 0 || m.Height <= 0 {
		return nil
	}
	w := float64(m.Width)
	h := float64(m.Height)
	points := map[string][2]float64{
		"leftTop":     {0, 0},
		"rightTop":    {w, 0},
		"rightBottom": {w, h},
		"leftBottom":  {0, h},
		"center":      {w / 2, h / 2},
	}
	out := make(map[string]Corner, len(points))
	for name, p := range points {
		lng, lat := m.Cal.Px2LL(p[0], p[1])
		out[name] = Corner{PX: p[0], PY: p[1], Lat: lat, Lng: lng}
	}
	return out
}

// ValidatePixel 校验像素是否落在该底图范围内，并返回换算出的经纬度。
//
// 这是「管理员传像素」的唯一入口校验，把三件事一次做完：
//  1. 该校区的图已上传（没有图就无所谓像素坐标系）；
//  2. 该校区已标定参数（否则无法派生经纬度）；
//  3. 像素非负且不越界（越界说明传的是别的图、或客户端缩放没还原）。
func (m *Map) ValidatePixel(px, py float64) (lng, lat float64, err error) {
	if m == nil || !m.Uploaded() {
		return 0, 0, fmt.Errorf("该校区尚未上传地图底图，无法使用像素坐标")
	}
	if !m.Calibrated() {
		return 0, 0, fmt.Errorf("%w：%s", ErrNotCalibrated, m.Campus.Label())
	}
	if px < 0 || py < 0 || px > float64(m.Width) || py > float64(m.Height) {
		return 0, 0, fmt.Errorf(
			"像素坐标 (%.1f, %.1f) 超出底图范围 %d×%d",
			px, py, m.Width, m.Height)
	}
	lng, lat = m.Cal.Px2LL(px, py)
	return lng, lat, nil
}
