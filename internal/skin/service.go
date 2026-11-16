package skin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/platform"
)

// Options 是构造 Service 的依赖。
type Options struct {
	// Repo 必填（元数据持久化）。
	Repo Repo
	// ObjectStore 必填（包体 zip 存 MinIO / 内存实现）。nil 时所有读写返回 500。
	ObjectStore platform.ObjectStore
	// Clock 缺省用真实时钟。
	Clock platform.Clock
	// Logger 可选；nil 时丢弃日志。
	Logger *slog.Logger
}

// Service 是皮肤包业务逻辑。不感知 HTTP、不直接碰 SQL / S3 SDK。
type Service struct {
	repo   Repo
	store  platform.ObjectStore
	clock  platform.Clock
	logger *slog.Logger
}

// New 构造 Service。
func New(opts Options) *Service {
	if opts.Repo == nil {
		panic("skin: Options.Repo 必填")
	}
	if opts.Clock == nil {
		opts.Clock = platform.NewRealClock()
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{
		repo: opts.Repo, store: opts.ObjectStore, clock: opts.Clock,
		logger: opts.Logger,
	}
}

// UploadInput 是一次上传的输入（包体已在 handler 读入内存并受体积限）。
type UploadInput struct {
	PackBytes []byte // zip 字节
	Version   string // 版本目录名（B 端显式给出，对应客户端 filesDir/skins/{packId}/{version}）
	CreatedBy string // 操作者（admin 会话用户名/ID，由 handler 从已认证身份注入）
	Activate  bool   // 是否上传后立即把全局单指针切到这版
}

// Upload 校验并登记一个皮肤包，返回包元数据。
//
// 顺序是刻意的（失败不动已生效的包，对齐客户端「只在结论 OK 时调 install」）：
//  1. VerifyPack（纯内存校验：清单、路径白名单、逐文件 sha256、zip 炸弹）；
//  2. 写对象存储（MinIO.Put）；**写成功才**登记元数据；
//  3. 登记成功且 Activate=true 才切全局单指针。
//
// 任何一步失败都返回带码领域错误；已写入 MinIO 但登记失败时，本方法会尽力
// Delete 掉刚写的对象，避免留下「库里查不到、对象存储里却有个孤儿 zip」。
// 这是旁路清理，失败只记日志，不改变主结果。
func (s *Service) Upload(ctx context.Context, in UploadInput) (*Pack, error) {
	if s.store == nil {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "对象存储未配置，暂不可上传皮肤包")
	}
	version := strings.TrimSpace(in.Version)
	if !IsSafeSegment(version) {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "version 缺失或含非法字符（只许字母数字与 -_.）")
	}

	vr, err := VerifyPack(VerifyInput{PackBytes: in.PackBytes})
	if err != nil {
		return nil, err
	}
	m := vr.Manifest

	// 先查版本是否已存在：重复上传必须在这里就失败，**绝不能先 Put 再回滚**。
	// 早先「Put → Create 失败 → Delete」的实现会因为对象 key 确定性派生而把
	// 线上已生效那版的包体一起删掉（集成测试实测抓到的 bug）。
	// 现在的顺序保证：重复上传既不写 MinIO、也不碰既有对象。
	if _, err := s.repo.GetByRef(ctx, m.PackID, version); err == nil {
		return nil, apperr.Errorf(apperr.CodeConflict, "皮肤包 %s/%s 已存在且不可覆盖", m.PackID, version)
	} else if !errors.Is(err, ErrVersionNotFound) {
		return nil, fmt.Errorf("skin: 检查版本是否已存在失败：%w", err)
	}

	// 每次上传用唯一对象 key：即便并发同版本上传，落败那笔的回滚也只会删
	// 自己刚写的对象，不会碰到胜出者的包体。
	id, err := NewObjectID()
	if err != nil {
		return nil, fmt.Errorf("skin: 生成对象 key 失败：%w", err)
	}
	key := ObjectKey(m.PackID, version, id)

	if _, err := s.store.Put(ctx, key, bytes.NewReader(in.PackBytes), vr.PackSize, "application/zip"); err != nil {
		s.logger.Error("皮肤包写入对象存储失败", "key", key, "err", err)
		return nil, fmt.Errorf("skin: 写入对象存储失败：%w", err)
	}

	now := s.clock.Now()
	p := &Pack{
		PackID:        m.PackID,
		Version:       version,
		SchemaVersion: m.SchemaVersion,
		MinAppVersion: m.MinAppVersion,
		DisplayName:   m.DisplayName,
		ObjectKey:     key,
		PackSHA256:    vr.PackSHA256,
		PackSize:      vr.PackSize,
		ManifestJSON:  vr.ManifestJSON,
		CreatedAt:     now,
		CreatedBy:     in.CreatedBy,
	}
	if err := s.repo.Create(ctx, p); err != nil {
		// 登记失败：尽力回滚刚写的**唯一**对象（不会影响任何已登记版本）。
		if derr := s.store.Delete(ctx, key); derr != nil {
			s.logger.Warn("皮肤包登记失败后的对象回滚也失败", "key", key, "deleteErr", derr, "createErr", err)
		}
		if errors.Is(err, ErrVersionExists) {
			// 并发竞态：另一笔先登记成功，本笔按冲突返回。
			return nil, apperr.Errorf(apperr.CodeConflict, "皮肤包 %s/%s 已存在且不可覆盖", m.PackID, version)
		}
		return nil, fmt.Errorf("skin: 登记皮肤包元数据失败：%w", err)
	}

	if in.Activate {
		if err := s.repo.Activate(ctx, m.PackID, version); err != nil {
			// 登记已成功、指针没切上：不回滚包（它仍可作为候选被后续 Activate 选中），
			// 只把错误如实报出，由调用方决定是否重试激活。
			s.logger.Error("皮肤包上传成功但激活失败", "packId", m.PackID, "version", version, "err", err)
			return nil, fmt.Errorf("skin: 皮肤包已登记但激活失败：%w", err)
		}
	}
	s.logger.Info("皮肤包已上传", "packId", m.PackID, "version", version,
		"size", vr.PackSize, "sha256", vr.PackSHA256, "activate", in.Activate)
	return p, nil
}

// Catalog 是「目录接口」的响应（客户端首帧后拉取的就是它）。
//
// 对齐 ADR-0003 / 0004「全局单指针：服务端只提供当前皮肤的
// {packId, version, url, sha256}，所有用户同一套」。这里额外把完整 manifest
// 原文带出，客户端拿一次目录就能同时得到「包 URL + 清单」，无需两次请求。
// 没有已激活包时 HasPack=false、各字段为零值：客户端必须据此回落内置默认皮肤，
// 绝不能编造一个包。
type Catalog struct {
	HasPack       bool            `json:"hasPack"`
	PackID        string          `json:"packId,omitempty"`
	Version       string          `json:"version,omitempty"`
	SchemaVersion int             `json:"schemaVersion,omitempty"`
	MinAppVersion int             `json:"minAppVersion,omitempty"`
	DisplayName   string          `json:"displayName,omitempty"`
	PackSHA256    string          `json:"packSha256,omitempty"`
	PackSize      int64           `json:"packSize,omitempty"`
	Manifest      json.RawMessage `json:"manifest,omitempty"`
	DownloadURL   string          `json:"downloadUrl,omitempty"`
	UpdatedAt     *time.Time      `json:"updatedAt,omitempty"`
}

// Catalog 返回当前生效皮肤包的目录视图。
// DownloadURL 指向本服务的下载端点（后端流式代理），不是对象存储的预签名 URL：
// 这样客户端走同一套已签名/已鉴权的通道，且与「首帧后拉取」这条链一致。
func (s *Service) Catalog(ctx context.Context, basePath string) (*Catalog, error) {
	cur, err := s.repo.Current(ctx)
	if err != nil {
		if errors.Is(err, ErrNoCurrentPointer) {
			return &Catalog{HasPack: false}, nil
		}
		return nil, fmt.Errorf("skin: 读取当前皮肤指针失败：%w", err)
	}
	t := cur.UpdatedAt
	return &Catalog{
		HasPack:       true,
		PackID:        cur.PackID,
		Version:       cur.Version,
		SchemaVersion: cur.SchemaVersion,
		MinAppVersion: cur.MinAppVersion,
		DisplayName:   cur.DisplayName,
		PackSHA256:    cur.PackSHA256,
		PackSize:      cur.PackSize,
		Manifest:      json.RawMessage(cur.ManifestJSON),
		DownloadURL:   fmt.Sprintf("%s/%s/%s/pack", strings.TrimRight(basePath, "/"), cur.PackID, cur.Version),
		UpdatedAt:     &t,
	}, nil
}

// List 返回已登记皮肤包列表（管理端用），最多 limit 条。
func (s *Service) List(ctx context.Context, limit int) ([]CatalogEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	packs, err := s.repo.List(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("skin: 列出皮肤包失败：%w", err)
	}
	out := make([]CatalogEntry, 0, len(packs))
	for _, p := range packs {
		out = append(out, MarshalCatalogEntry(p))
	}
	return out, nil
}

// Detail 返回单个包的目录级详情（不含对象 key 等内部字段）。
func (s *Service) Detail(ctx context.Context, packID, version string) (*CatalogEntry, error) {
	p, err := s.repo.GetByRef(ctx, packID, version)
	if err != nil {
		if errors.Is(err, ErrVersionNotFound) {
			return nil, apperr.Errorf(apperr.CodeNotFound, "皮肤包 %s/%s 不存在", packID, version)
		}
		return nil, fmt.Errorf("skin: 读取皮肤包失败：%w", err)
	}
	e := MarshalCatalogEntry(p)
	return &e, nil
}

// Download 打开一个包体的只读流（后端流式代理 MinIO）。
//
// 返回 (meta, io.ReadCloser, error)；调用方**必须** Close。
// 先查元数据再取对象：不存在 / 非法引用一律 404，绝不把对象存储的内部错误
// 泄漏成 4xx；对象缺失（库里有、MinIO 里没了）记日志并映射成 404。
func (s *Service) Download(ctx context.Context, packID, version string) (*Pack, io.ReadCloser, error) {
	if !IsSafeSegment(packID) || !IsSafeSegment(version) {
		return nil, nil, apperr.Errorf(apperr.CodeValidationFailed, "packId / version 非法")
	}
	p, err := s.repo.GetByRef(ctx, packID, version)
	if err != nil {
		if errors.Is(err, ErrVersionNotFound) {
			return nil, nil, apperr.Errorf(apperr.CodeNotFound, "皮肤包 %s/%s 不存在", packID, version)
		}
		return nil, nil, fmt.Errorf("skin: 读取皮肤包失败：%w", err)
	}
	if s.store == nil {
		return nil, nil, apperr.Errorf(apperr.CodeValidationFailed, "对象存储未配置，暂不可下载皮肤包")
	}
	rc, err := s.store.Get(ctx, p.ObjectKey)
	if err != nil {
		if errors.Is(err, platform.ErrObjectNotFound) {
			s.logger.Error("皮肤包元数据存在但对象缺失", "packId", packID, "version", version, "key", p.ObjectKey)
			return nil, nil, apperr.Errorf(apperr.CodeNotFound, "皮肤包 %s/%s 的包体不存在", packID, version)
		}
		return nil, nil, fmt.Errorf("skin: 读取皮肤包对象失败：%w", err)
	}
	return p, rc, nil
}

// Activate 把全局单指针切到某个已登记的包（管理端「发布 / 回滚」入口）。
// 回滚就是把指针指回上一版（ADR-0003），不需要重新上传。
func (s *Service) Activate(ctx context.Context, packID, version string) error {
	if !IsSafeSegment(packID) || !IsSafeSegment(version) {
		return apperr.Errorf(apperr.CodeValidationFailed, "packId / version 非法")
	}
	if err := s.repo.Activate(ctx, packID, version); err != nil {
		if errors.Is(err, ErrVersionNotFound) {
			return apperr.Errorf(apperr.CodeNotFound, "皮肤包 %s/%s 不存在", packID, version)
		}
		return fmt.Errorf("skin: 激活皮肤包失败：%w", err)
	}
	s.logger.Info("皮肤包已激活（全局单指针切换）", "packId", packID, "version", version)
	return nil
}
