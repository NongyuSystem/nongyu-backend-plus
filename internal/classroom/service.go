package classroom

import (
	"context"
	"fmt"
	"strings"
	"time"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/platform"
)

// 字段上限。
const (
	MaxDisplayNameLen = 64
	MaxAddressLen     = 256
	MaxNoteLen        = 512
	MaxRouteTitleLen  = 64
	MaxRouteDescLen   = 1024
	MaxRoutePointNote = 128
	// MaxRoutePoints 是单条路线的点数上限。
	//
	// 「点状路线」是为手工录入少量关键点设计的（门口/拐角/电梯）。
	// 若有人想录几百个点，那应该用真正的 GIS 数据而不是这个接口。
	MaxRoutePoints = 64
)

// Options 是构造 Service 的依赖。
type Options struct {
	// Repo 必填。
	Repo Repo
	// Clock 可为 nil，默认真实时钟。
	Clock platform.Clock
}

// Service 是教室楼栋业务逻辑。不感知 HTTP。
type Service struct {
	repo  Repo
	clock platform.Clock

	// 底图能力（可选）。经 BindMaps 注入，两者都非 nil 才启用；
	// 未注入时楼栋查询等既有功能照常工作（见 mapsReady）。
	mapRepo  MapRepo
	mapStore MapObjectStore
}

// New 构造 Service。
func New(opts Options) *Service {
	if opts.Repo == nil {
		panic("classroom: Options.Repo 必填")
	}
	if opts.Clock == nil {
		opts.Clock = platform.NewRealClock()
	}
	return &Service{repo: opts.Repo, clock: opts.Clock}
}

// ---------- 读者侧 ----------

// Lookup 按教室名查楼栋。
//
// 这是本功能的主入口：用户输入/传入教务里的教室名（`10-B213`），
// 得到楼栋（十教B 栋）及其坐标与进入路线。
//
// 三种结果要区分清楚（前端提示语不同）：
//  1. 无法识别出楼栋键 → BuildingKey 为空，前端提示「教室名无法识别」；
//  2. 识别出键但楼栋未录入 → Building 为 nil，提示「该楼栋暂未录入地址」；
//  3. 命中 → 带上楼栋与路线。
func (s *Service) Lookup(ctx context.Context, roomName string) (LookupResult, error) {
	res := LookupResult{Input: roomName}

	key, ok := ParseKey(roomName)
	if !ok {
		return res, nil // 情况 1：不是错误，只是查不到
	}
	res.BuildingKey = key
	res.RoomNo = ParseRoomNo(roomName)

	b, err := s.repo.FindBuildingByKey(ctx, key)
	if err != nil {
		if isNotFound(err) {
			return res, nil // 情况 2：键对了但没录入
		}
		return res, err
	}

	// 详情场景：带出路线（一次性查，避免前端再发一轮请求）。
	routes, err := s.repo.ListRoutes(ctx, b.ID, false)
	if err != nil {
		return res, err
	}
	b.Routes = routes
	res.Building = b
	return res, nil
}

// ListBuildings 返回楼栋列表（读者侧，只含启用的）。
func (s *Service) ListBuildings(ctx context.Context, campus string) ([]*Building, error) {
	return s.repo.ListBuildings(ctx, campus, false)
}

// GetBuilding 返回单个楼栋详情（含路线）。
func (s *Service) GetBuilding(ctx context.Context, id int64) (*Building, error) {
	if id <= 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "楼栋 id 非法")
	}
	b, err := s.repo.FindBuildingByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !b.Enabled {
		// 对读者而言停用等同于不存在（不泄露「这栋楼被停用了」）。
		return nil, apperr.Errorf(apperr.CodeNotFound, "楼栋不存在")
	}
	routes, err := s.repo.ListRoutes(ctx, b.ID, false)
	if err != nil {
		return nil, err
	}
	b.Routes = routes
	return b, nil
}

// ---------- 管理端：楼栋 ----------

// BuildingInput 是管理端创建/更新楼栋的入参。
type BuildingInput struct {
	BuildingNo  string
	Zone        string
	DisplayName string
	Aliases     []string
	Campus      string
	// Lat/Lng 必须成对出现（只填一个会被拒）。
	Lat *float64
	Lng *float64
	// MapPx/MapPy 是底图像素坐标，必须成对出现。
	//
	// 维护者口径（2026-11-25）：「管理员只用像素点位置，给用户的是两个都要」。
	// 因此管理端**新建/更新一律传像素**，后端按 Campus 底图的换算参数
	// 算出 Lat/Lng 一并存下；读者侧两个都下发。
	//
	// 字段本身可为 nil（维护者：「允许无像素，给其他未来接口兼容」）——
	// 「必须有」是写入门槛，不是「列非空约束」。
	MapPx *float64
	MapPy *float64
	// 注：Lat/Lng 仍保留在入参里，但**已不再是录入途径**——
	// 维护者决定「不兼容旧数据，强制传像素」，经纬度恒由像素派生。
	// 保留字段是为了不改动 handler 的请求类型解码结构，写入时会被派生值覆盖。
	AddressText string
	Note        string
	Enabled     *bool
}

// CreateBuilding 新建楼栋。
func (s *Service) CreateBuilding(ctx context.Context, in BuildingInput) (*Building, error) {
	b, err := s.buildFromInput(ctx, in, nil)
	if err != nil {
		return nil, err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	b.Enabled = enabled
	now := s.clock.Now()
	b.CreatedAt, b.UpdatedAt = now, now

	if err := s.repo.CreateBuilding(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

// UpdateBuilding 更新楼栋。
//
// **不允许改楼栋键**（楼栋号 + 分区）：键变了，已有教室名就会指向别的楼，
// 或者指向空。若确实要改，应删掉重建（管理端会二次确认）。
func (s *Service) UpdateBuilding(ctx context.Context, id int64, in BuildingInput) (*Building, error) {
	old, err := s.repo.FindBuildingByID(ctx, id)
	if err != nil {
		return nil, err
	}
	b, err := s.buildFromInput(ctx, in, old)
	if err != nil {
		return nil, err
	}
	b.ID = id
	b.Key = old.Key // 键不可改
	b.CreatedAt = old.CreatedAt
	b.UpdatedAt = s.clock.Now()
	if in.Enabled != nil {
		b.Enabled = *in.Enabled
	} else {
		b.Enabled = old.Enabled
	}

	if err := s.repo.UpdateBuilding(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

// buildFromInput 把入参校验并转成实体。old 为 nil 表示新建。
func (s *Service) buildFromInput(ctx context.Context, in BuildingInput, old *Building) (*Building, error) {
	key, ok := NormalizeKey(in.BuildingNo, in.Zone)
	if !ok {
		return nil, apperr.Errorf(apperr.CodeValidationFailed,
			"楼栋号/分区非法（楼栋号需含数字，分区为单个字母）")
	}

	name := strings.TrimSpace(in.DisplayName)
	if name == "" || len(name) > MaxDisplayNameLen {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "展示名不能为空且不超过 %d 字符", MaxDisplayNameLen)
	}
	if len(in.AddressText) > MaxAddressLen {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "地址超长（上限 %d）", MaxAddressLen)
	}
	if len(in.Note) > MaxNoteLen {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "备注超长（上限 %d）", MaxNoteLen)
	}

	// 坐标必须成对，且落在合法区间。
	if (in.Lat == nil) != (in.Lng == nil) {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "经纬度必须同时提供")
	}
	if (in.MapPx == nil) != (in.MapPy == nil) {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "像素坐标 mapPx/mapPy 必须同时提供")
	}

	campus := strings.TrimSpace(in.Campus)
	if campus != "" {
		if _, ok := ParseCampus(campus); !ok {
			return nil, apperr.Errorf(apperr.CodeValidationFailed,
				"校区非法：%q（可选 yaan/chengdu/dujiangyan）", campus)
		}
	}

	var mapPx, mapPy *float64
	var lat, lng *float64

	if in.MapPx == nil || in.MapPy == nil {
		return nil, apperr.Errorf(apperr.CodeValidationFailed,
			"必须提供像素坐标 mapPx/mapPy（不支持只传经纬度）")
	}
	// 像素必须有校区：没有校区就无像素坐标系可言。
	if campus == "" {
		return nil, apperr.Errorf(apperr.CodeValidationFailed,
			"必须指定校区 campus（可选 yaan/chengdu/dujiangyan）")
	}

	// 像素是唯一事实源，经纬度是派生值。
	//
	// 即使调用方同时传了 lat/lng，也一律以像素换算结果覆盖：同一行存在
	// 两套互相矛盾的坐标时，读者无法判断该信哪个，数据即不可信。
	derivedLng, derivedLat, err := s.deriveGeoFromPixel(ctx, Campus(campus), *in.MapPx, *in.MapPy)
	if err != nil {
		return nil, err
	}
	mapPx, mapPy = in.MapPx, in.MapPy
	lng, lat = &derivedLng, &derivedLat

	b := &Building{
		Key: key,
		// No/Zone 从归一化键反推，保证与 Key 严格一致。
		No:          strings.SplitN(key, "-", 2)[0],
		DisplayName: name,
		Aliases:     trimAll(in.Aliases),
		Campus:      campus,
		Lat:         lat,
		Lng:         lng,
		MapPx:       mapPx,
		MapPy:       mapPy,
		AddressText: strings.TrimSpace(in.AddressText),
		Note:        strings.TrimSpace(in.Note),
	}
	if parts := strings.SplitN(key, "-", 2); len(parts) == 2 {
		b.Zone = parts[1]
	}
	return b, nil
}

// deriveGeoFromPixel 用该校区底图的换算参数把像素派生成经纬度。
//
// 这是「管理员只用像素」的核心：像素是唯一事实源，经纬度是派生物。
// 四条失败路径都必须显式报错、绝不退回雅安参数（规格 §5 第 3 条）：
//  1. 底图能力未启用；
//  2. 该校区没有底图行 / 未标定参数（成都、都江堰当前就是这种）；
//  3. 未上传图片字节（无尺寸则无从判越界）；
//  4. 像素越界。
func (s *Service) deriveGeoFromPixel(
	ctx context.Context, campus Campus, px, py float64,
) (lng, lat float64, err error) {
	if err := s.mapsReady(); err != nil {
		return 0, 0, err
	}
	m, err := s.mapRepo.FindMapByCampus(ctx, campus)
	if err != nil {
		if isNotFound(err) {
			return 0, 0, apperr.Errorf(apperr.CodeValidationFailed,
				"%s 校区尚未登记地图底图，无法按像素定位", campus.Label())
		}
		return 0, 0, err
	}
	lng, lat, verr := m.ValidatePixel(px, py)
	if verr != nil {
		return 0, 0, apperr.Errorf(apperr.CodeValidationFailed, "%v", verr)
	}
	return lng, lat, nil
}

// AdminListBuildings 管理端列表（含停用）。
func (s *Service) AdminListBuildings(ctx context.Context, campus string) ([]*Building, error) {
	return s.repo.ListBuildings(ctx, campus, true)
}

// AdminGetBuilding 管理端详情（含停用）。
func (s *Service) AdminGetBuilding(ctx context.Context, id int64) (*Building, error) {
	if id <= 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "楼栋 id 非法")
	}
	b, err := s.repo.FindBuildingByID(ctx, id)
	if err != nil {
		return nil, err
	}
	routes, err := s.repo.ListRoutes(ctx, b.ID, true)
	if err != nil {
		return nil, err
	}
	b.Routes = routes
	return b, nil
}

// DeleteBuilding 删除楼栋（级联删路线）。
func (s *Service) DeleteBuilding(ctx context.Context, id int64) error {
	if id <= 0 {
		return apperr.Errorf(apperr.CodeValidationFailed, "楼栋 id 非法")
	}
	return s.repo.DeleteBuilding(ctx, id)
}

// ---------- 管理端：路线 ----------

// RouteInput 是路线的创建/更新入参。
type RouteInput struct {
	Title       string
	Description string
	StartLabel  string
	Recommended bool
	SortOrder   int
	Enabled     *bool
	Points      []RoutePointInput
}

// RoutePointInput 是路线上的一个点。
//
// 维护者决定「不兼容旧数据，强制传像素」：每个点**必须**给 MapPx/MapPy，
// 经纬度由所属楼栋校区的底图换算参数派生（见 BuildingInput 的同款口径）。
//
// 不在这里放 campus：路线属于楼栋，校区由楼栋决定，冗余一列会引入
// 「楼栋在雅安、点却在成都图上」这种无意义状态。
type RoutePointInput struct {
	// Lat/Lng 已不是录入途径（恒由像素派生），保留以兼容请求体解码结构。
	Lat   float64
	Lng   float64
	MapPx *float64
	MapPy *float64
	Label string
}

// CreateRoute 新建路线。
func (s *Service) CreateRoute(ctx context.Context, buildingID int64, in RouteInput) (*Route, error) {
	b, err := s.findBuilding(ctx, buildingID)
	if err != nil {
		return nil, err
	}
	r, err := s.routeFromInput(ctx, in, nil, b.Campus)
	if err != nil {
		return nil, err
	}
	r.BuildingID = buildingID
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	r.Enabled = enabled
	now := s.clock.Now()
	r.CreatedAt, r.UpdatedAt = now, now

	if err := s.repo.CreateRoute(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

// UpdateRoute 更新路线（整体替换点集合）。
func (s *Service) UpdateRoute(ctx context.Context, routeID int64, in RouteInput) (*Route, error) {
	old, err := s.repo.FindRoute(ctx, routeID)
	if err != nil {
		return nil, err
	}
	// 取所属楼栋：路线点的像素坐标系由楼栋校区决定。
	b, err := s.findBuilding(ctx, old.BuildingID)
	if err != nil {
		return nil, err
	}
	r, err := s.routeFromInput(ctx, in, old, b.Campus)
	if err != nil {
		return nil, err
	}
	r.ID = old.ID
	r.BuildingID = old.BuildingID
	r.CreatedAt = old.CreatedAt
	r.UpdatedAt = s.clock.Now()
	if in.Enabled != nil {
		r.Enabled = *in.Enabled
	} else {
		r.Enabled = old.Enabled
	}

	if err := s.repo.UpdateRoute(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

// routeFromInput 校验入参并转成实体。
//
// campus 来自**所属楼栋**（不是入参）：路线点的像素坐标系由楼栋所在校区决定。
func (s *Service) routeFromInput(
	ctx context.Context, in RouteInput, old *Route, campus string,
) (*Route, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" || len(title) > MaxRouteTitleLen {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "路线标题不能为空且不超过 %d 字符", MaxRouteTitleLen)
	}
	if len(in.Description) > MaxRouteDescLen {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "路线说明超长（上限 %d）", MaxRouteDescLen)
	}
	if len(in.Points) > MaxRoutePoints {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "路线点数过多（上限 %d）", MaxRoutePoints)
	}

	pts := make([]RoutePoint, 0, len(in.Points))
	for i, p := range in.Points {
		if p.MapPx == nil || p.MapPy == nil {
			return nil, apperr.Errorf(apperr.CodeValidationFailed,
				"第 %d 个点必须提供像素坐标 mapPx/mapPy", i+1)
		}
		// 传了像素：按楼栋校区换算。楼栋未设校区则无法换算。
		if campus == "" {
			return nil, apperr.Errorf(apperr.CodeValidationFailed,
				"所属楼栋未设置校区，第 %d 个点无法按像素定位", i+1)
		}
		c, ok := ParseCampus(campus)
		if !ok {
			return nil, apperr.Errorf(apperr.CodeValidationFailed,
				"所属楼栋的校区 %q 非法", campus)
		}
		lng, lat, err := s.deriveGeoFromPixel(ctx, c, *p.MapPx, *p.MapPy)
		if err != nil {
			return nil, fmt.Errorf("第 %d 个点：%w", i+1, err)
		}

		if len(p.Label) > MaxRoutePointNote {
			return nil, apperr.Errorf(apperr.CodeValidationFailed, "第 %d 个点的说明超长", i+1)
		}
		// seq 由服务端按数组顺序生成，**不接受客户端自报**：
		// 让客户端定 seq 会出现重复/跳号，前端连线就乱了。
		pts = append(pts, RoutePoint{
			Seq: i + 1, Lat: lat, Lng: lng,
			MapPx: p.MapPx, MapPy: p.MapPy,
			Label: strings.TrimSpace(p.Label),
		})
	}

	return &Route{
		Title:       title,
		Description: strings.TrimSpace(in.Description),
		StartLabel:  strings.TrimSpace(in.StartLabel),
		Recommended: in.Recommended,
		SortOrder:   in.SortOrder,
		Points:      pts,
	}, nil
}

// DeleteRoute 删除路线。
func (s *Service) DeleteRoute(ctx context.Context, routeID int64) error {
	if routeID <= 0 {
		return apperr.Errorf(apperr.CodeValidationFailed, "路线 id 非法")
	}
	return s.repo.DeleteRoute(ctx, routeID)
}

// findBuilding 取楼栋实体（供需要其校区等字段的调用方使用）。
//
// 与 ensureBuilding 的区别：后者只验证存在性，前者要拿到实体。
func (s *Service) findBuilding(ctx context.Context, id int64) (*Building, error) {
	if id <= 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "楼栋 id 非法")
	}
	return s.repo.FindBuildingByID(ctx, id)
}

func (s *Service) ensureBuilding(ctx context.Context, id int64) error {
	if id <= 0 {
		return apperr.Errorf(apperr.CodeValidationFailed, "楼栋 id 非法")
	}
	if _, err := s.repo.FindBuildingByID(ctx, id); err != nil {
		return err
	}
	return nil
}

// ---------- 工具 ----------

// isNotFound 判断是否是「找不到」类错误。
//
// 用 apperr.CodeOf 判定而不是字符串比较：错误信息会变，错误码不会。
func isNotFound(err error) bool {
	code, ok := apperr.CodeOf(err)
	return ok && code == apperr.CodeNotFound
}

func trimAll(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if v := strings.TrimSpace(s); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// 编译期确认时钟接口被用到（避免 import 冗余告警）。
var _ = time.Now
