package classroom

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"  // 注册 GIF 解码器，供 DecodeConfig 嗅探尺寸
	_ "image/jpeg" // 同上
	_ "image/png"  // 同上
	"io"
	"sort"
	"strings"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/platform"
)

// 底图上传的上限与允许类型（规格 §7.3）。
const (
	// MaxMapBytes 是底图字节上限。底图是静态资产，雅安那张约 2.8 MB，
	// 给足余量到 16 MiB；仍设上限，防止误传几百 MB 的文件撑爆存储与内存。
	MaxMapBytes = 16 << 20

	// maxMapDimension 是单边像素上限。超过这个量级的图不是校区底图
	// （更像卫星影像），而超宽图会让前端 canvas 渲染显著变慢。
	maxMapDimension = 30000
)

// 允许的底图 MIME 类型。
var allowedMapTypes = map[string]string{
	"image/gif":  ".gif",
	"image/png":  ".png",
	"image/jpeg": ".jpg",
}

// MapRepo 是底图的持久化接口（实现放 internal/classroom/store）。
type MapRepo interface {
	// FindMapByCampus 按校区查底图。不存在返回 not_found。
	FindMapByCampus(ctx context.Context, campus Campus) (*Map, error)
	// ListMaps 返回全部底图，按校区名升序（管理端列表用）。
	ListMaps(ctx context.Context) ([]*Map, error)
	// UpsertMapImage 写入/替换某校区的图片元信息。
	//
	// 只更新「图」相关列（object_key/content_type/size/etag/width/height），
	// **不动换算参数**：换图不等于重新标定，标定是独立的人工过程。
	UpsertMapImage(ctx context.Context, m *Map) error
	// SetCalibration 写入某校区的换算参数（运维标定后用）。
	SetCalibration(ctx context.Context, campus Campus, cal *Calibration, note string) error
}

// MapOptions 是底图功能的依赖。
//
// 用 mapRepo 与 store 分开注入而不是塞进主 Options：底图是可选能力，
// 没配对象存储时楼栋查询仍应可用（与二课域「依赖缺失不瘫核心功能」同一思路）。
type MapOptions struct {
	Repo  MapRepo
	Store MapObjectStore
}

// MapObjectStore 是底图所需的对象存储最小面。
//
// 只声明用到的方法，不依赖 platform.ObjectStore 全量接口——与本包既有风格一致
// （classroom 域此前没有对象存储依赖，引入时保持最小）。
// 返回类型直接用 platform.ObjectInfo：再定义一份同形结构只会多一层无意义的转换。
type MapObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (platform.ObjectInfo, error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// BindMaps 注入底图依赖。两者都注入后底图能力才启用。
func (s *Service) BindMaps(opts MapOptions) {
	s.mapRepo = opts.Repo
	s.mapStore = opts.Store
}

// mapsReady 判断底图能力是否可用。
//
// 用 CodeUnsupportedMedia 而不是自造一个「不可用」码：装配层没注入对象存储时，
// 底图能力在本进程内确实不存在，语义上最接近 415（该能力不支持）。
// 不新增错误码的好处是错误码表保持封闭，客户端不必为一次性场景扩展枚举。
func (s *Service) mapsReady() error {
	if s.mapRepo == nil || s.mapStore == nil {
		return apperr.Errorf(apperr.CodeUnsupportedMedia, "地图底图能力未启用")
	}
	return nil
}

// GetMap 返回某校区底图的元信息（规格 §7.2）。
func (s *Service) GetMap(ctx context.Context, rawCampus string) (*Map, error) {
	campus, ok := ParseCampus(rawCampus)
	if !ok {
		return nil, apperr.Errorf(apperr.CodeValidationFailed,
			"校区非法：%q（可选 yaan/chengdu/dujiangyan）", rawCampus)
	}
	if err := s.mapsReady(); err != nil {
		return nil, err
	}
	m, err := s.mapRepo.FindMapByCampus(ctx, campus)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// OpenMapImage 返回底图字节流（供 handler 流式代理，规格 §7.1）。
//
// 已登记但未上传图片字节时返回 not_found，而不是空流：
// 「有这个校区但还没传图」对客户端等价于「没有图」。
func (s *Service) OpenMapImage(ctx context.Context, rawCampus string) (*Map, io.ReadCloser, error) {
	m, err := s.GetMap(ctx, rawCampus)
	if err != nil {
		return nil, nil, err
	}
	if !m.Uploaded() {
		return nil, nil, apperr.Errorf(apperr.CodeNotFound, "该校区暂无地图")
	}
	rc, err := s.mapStore.Get(ctx, m.ObjectKey)
	if err != nil {
		return nil, nil, err
	}
	return m, rc, nil
}

// ListMaps 返回全部底图（管理端）。
func (s *Service) ListMaps(ctx context.Context) ([]*Map, error) {
	if err := s.mapsReady(); err != nil {
		return nil, err
	}
	return s.mapRepo.ListMaps(ctx)
}

// MapUploadInput 是上传底图的入参。
type MapUploadInput struct {
	Campus string
	// FileName 仅用于取扩展名与日志，不参与任何校验判断。
	FileName string
	Body     []byte
}

// UploadMap 校验并上传底图（规格 §7.3）。
//
// 三条硬约束，都是为了让「系统里那张图」与「坐标换算用的尺寸」永远一致：
//  1. 类型按**魔数嗅探**判定，不信客户端声明的 Content-Type；
//  2. 宽高由**解码图片字节**得出，不信客户端声明——尺寸错会让所有像素
//     坐标系统性偏移，且几乎无法察觉；
//  3. 换图时删旧对象，避免存储泄漏。
func (s *Service) UploadMap(ctx context.Context, in MapUploadInput) (*Map, error) {
	campus, ok := ParseCampus(in.Campus)
	if !ok {
		return nil, apperr.Errorf(apperr.CodeValidationFailed,
			"校区非法：%q（可选 yaan/chengdu/dujiangyan）", in.Campus)
	}
	if err := s.mapsReady(); err != nil {
		return nil, err
	}
	if len(in.Body) == 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "上传内容为空")
	}
	if len(in.Body) > MaxMapBytes {
		return nil, apperr.Errorf(apperr.CodeValidationFailed,
			"底图超过 %d MiB 上限（实际 %d 字节）", MaxMapBytes>>20, len(in.Body))
	}

	contentType, width, height, err := sniffMapImage(in.Body)
	if err != nil {
		return nil, err
	}

	// 先取旧行，换图时才知道要删哪个对象。
	old, ferr := s.mapRepo.FindMapByCampus(ctx, campus)
	if ferr != nil && !isNotFound(ferr) {
		return nil, ferr
	}

	key := mapObjectKey(campus, contentType, in.Body)
	info, err := s.mapStore.Put(ctx, key, bytes.NewReader(in.Body), int64(len(in.Body)), contentType)
	if err != nil {
		return nil, err
	}

	m := &Map{
		Campus:      campus,
		ObjectKey:   key,
		ContentType: contentType,
		SizeBytes:   int64(len(in.Body)),
		ETag:        info.ETag,
		Width:       width,
		Height:      height,
		CoordSystem: "GCJ-02",
	}
	if old != nil {
		// 保留既有换算参数与标定说明：换图不等于重新标定。
		m.ID = old.ID
		m.Cal = old.Cal
		m.CalibrationNote = old.CalibrationNote
		m.Enabled = old.Enabled
	} else {
		m.Enabled = true
	}

	if err := s.mapRepo.UpsertMapImage(ctx, m); err != nil {
		return nil, err
	}

	// 旧对象在数据库更新成功之后才删：先删后写若写失败，会出现
	// 「数据库指向已删除对象」的坏状态；先写后删则最坏只是留一个孤儿对象。
	if old != nil && old.ObjectKey != "" && old.ObjectKey != key {
		// 删失败不影响本次上传结果（对象已就绪），只记日志级别的静默失败。
		_ = s.mapStore.Delete(ctx, old.ObjectKey)
	}

	// 重新读一次，拿到数据库里的最终参数（UpsertMapImage 不改参数，
	// 但可能是别处刚标定过）。
	if fresh, err := s.mapRepo.FindMapByCampus(ctx, campus); err == nil {
		return fresh, nil
	}
	return m, nil
}

// SetMapCalibration 由运维/管理端写入换算参数（规格 §6.1）。
//
// 参数来源只能是人工标定（控制点拟合）。这里只做形状校验，不猜值：
// 零尺度会让逆变换除零，负米数没有物理意义。
func (s *Service) SetMapCalibration(
	ctx context.Context, rawCampus string, cal Calibration, note string,
) (*Map, error) {
	campus, ok := ParseCampus(rawCampus)
	if !ok {
		return nil, apperr.Errorf(apperr.CodeValidationFailed,
			"校区非法：%q", rawCampus)
	}
	if err := s.mapsReady(); err != nil {
		return nil, err
	}
	if !cal.valid() {
		return nil, apperr.Errorf(apperr.CodeValidationFailed,
			"换算参数不可用：KX/KY 与 AR/AI 不能同时为 0")
	}
	if cal.KX <= 0 || cal.KY <= 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed,
			"KX/KY 必须是正数（该纬度下 1 度对应的米数）")
	}
	if cal.LAT0 < -90 || cal.LAT0 > 90 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "LAT0 超出 [-90, 90]")
	}
	if cal.LNG0 < -180 || cal.LNG0 > 180 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "LNG0 超出 [-180, 180]")
	}

	if err := s.mapRepo.SetCalibration(ctx, campus, &cal, note); err != nil {
		return nil, err
	}
	return s.mapRepo.FindMapByCampus(ctx, campus)
}

// ---------- 工具 ----------

// sniffMapImage 按魔数判定图片类型并解码宽高。
//
// 用 image.DecodeConfig 而不是只看扩展名或客户端 Content-Type：
// 前者可伪造，后者客户端说了算。DecodeConfig 只读头部，不做完整解码，
// 对 16 MiB 的上限来说开销可忽略。
func sniffMapImage(body []byte) (contentType string, width, height int, err error) {
	cfg, format, derr := image.DecodeConfig(bytes.NewReader(body))
	if derr != nil {
		return "", 0, 0, apperr.Errorf(apperr.CodeValidationFailed,
			"无法识别的图片格式（支持 gif/png/jpeg）")
	}
	switch format {
	case "gif":
		contentType = "image/gif"
	case "png":
		contentType = "image/png"
	case "jpeg":
		contentType = "image/jpeg"
	default:
		return "", 0, 0, apperr.Errorf(apperr.CodeValidationFailed,
			"不支持的图片格式 %q（支持 gif/png/jpeg）", format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return "", 0, 0, apperr.Errorf(apperr.CodeValidationFailed, "图片尺寸非法")
	}
	if cfg.Width > maxMapDimension || cfg.Height > maxMapDimension {
		return "", 0, 0, apperr.Errorf(apperr.CodeValidationFailed,
			"图片尺寸 %d×%d 超过单边 %d 像素上限", cfg.Width, cfg.Height, maxMapDimension)
	}
	return contentType, cfg.Width, cfg.Height, nil
}

// mapObjectKey 生成含内容指纹的对象键。
//
// 为什么要带指纹：换图后键必须变化，客户端带 ?v=<objectKey> 的强缓存
// 才会自然失效。若沿用固定键（如 classroom/maps/yaan.gif），客户端会
// 一直看到旧图——这是最难排查的一类缓存问题。
func mapObjectKey(campus Campus, contentType string, body []byte) string {
	sum := sha256Hex(body)
	ext := allowedMapTypes[contentType]
	if ext == "" {
		ext = ".bin"
	}
	return fmt.Sprintf("classroom/maps/%s-%s%s", campus, sum[:12], ext)
}

// MapSubmissionHint 汇总一个校区的可用性，供管理端列表与错误提示。
func MapSubmissionHint(m *Map) string {
	switch {
	case m == nil:
		return "未登记"
	case !m.Uploaded() && !m.Calibrated():
		return "未上传、未标定"
	case !m.Uploaded():
		return "未上传（换算参数已就绪）"
	case !m.Calibrated():
		return "已上传（尚未标定，不能用像素）"
	default:
		return "可上传与管理点位"
	}
}

// SortedCampuses 返回按名称升序的校区切片（稳定输出，便于测试与前端渲染）。
func SortedCampuses() []Campus {
	out := AllCampuses()
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// normalizeCampusInput 把输入去空白并转小写。
//
// 只做**去空白与大小写**，不做语义猜测（不把「雅安」映射成 yaan）：
// 校区标识一旦开始容忍别名，就会有人传「雅安市」，最终无法收敛成封闭集合。
func normalizeCampusInput(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// sha256Hex 返回 body 的 sha256 十六进制小写串。
func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
