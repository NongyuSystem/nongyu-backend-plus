package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/platform"
)

// Options 是构造 Service 的依赖。
type Options struct {
	// Repo 必填（元数据持久化）。
	Repo Repo
	// ObjectStore 包体存对象存储；nil 时上传与下载都返回明确错误（不是零字节）。
	ObjectStore platform.ObjectStore
	// Clock 缺省用真实时钟。
	Clock platform.Clock
	// Logger 可选；nil 时丢弃日志。
	Logger *slog.Logger
}

// Service 是应用发布业务逻辑。不感知 HTTP、不直接碰 SQL / S3 SDK。
type Service struct {
	repo   Repo
	store  platform.ObjectStore
	clock  platform.Clock
	logger *slog.Logger
}

// New 构造 Service。
func New(opts Options) *Service {
	if opts.Repo == nil {
		panic("release: Options.Repo 必填")
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

// Upload 登记一个安装包，返回登记后的记录。
//
// 顺序是刻意的（失败不动已发布的记录）：
//  1. 格式校验（applicationId / versionCode / versionName / minSupported）；
//  2. 查重——已存在直接 409，**不写对象存储**；
//  3. 写对象存储（唯一 key），算整包 sha256；
//  4. 写元数据（唯一约束兜并发竞态）；登记失败则尽力回滚刚写的对象。
//
// 注意本方法**不解析、不校验安装包内容**：这不是遗漏，是维护者在 Spec #80
// 里明确接受的边界（服务端不是安装包的可信判定方）。
func (s *Service) Upload(ctx context.Context, in UploadInput) (*Release, error) {
	if s.store == nil {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "对象存储未配置，暂不可上传安装包")
	}

	appID := in.AppID
	if err := ValidateApplicationID(appID); err != nil {
		return nil, err
	}
	if err := ValidateVersionCode(in.VersionCode); err != nil {
		return nil, err
	}
	if err := ValidateVersionName(in.VersionName); err != nil {
		return nil, err
	}
	if err := ValidateMinSupported(in.MinSupportedVersionCode, in.VersionCode); err != nil {
		return nil, err
	}
	if len(in.PackBytes) == 0 {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "安装包为空")
	}
	if int64(len(in.PackBytes)) > MaxPackBytes {
		return nil, apperr.Errorf(apperr.CodePayloadTooLarge, "安装包超过 %d 字节", MaxPackBytes)
	}

	// 先查重：重复上传必须在这里就失败，**绝不能先 Put 再回滚**。
	// 皮肤包域曾用「Put → Create 失败 → Delete」的顺序配确定性 key，
	// 结果把线上已生效那版的包体一起删掉了（真实集成测试抓到的 bug）。
	if _, err := s.repo.GetByRef(ctx, appID, in.VersionCode); err == nil {
		return nil, apperr.Errorf(apperr.CodeConflict,
			"应用 %s 的版本 %d 已存在且不可覆盖", appID, in.VersionCode)
	} else if !errors.Is(err, ErrReleaseNotFound) {
		return nil, fmt.Errorf("release: 检查版本是否已存在失败：%w", err)
	}

	sum := sha256.Sum256(in.PackBytes)
	sha := hex.EncodeToString(sum[:])
	size := int64(len(in.PackBytes))

	id, err := NewObjectID()
	if err != nil {
		return nil, err
	}
	key := ObjectKey(appID, in.VersionCode, id)

	if _, err := s.store.Put(ctx, key, bytes.NewReader(in.PackBytes), size, ApkContentType); err != nil {
		s.logger.Error("安装包写入对象存储失败", "key", key, "err", err)
		return nil, fmt.Errorf("release: 写入对象存储失败：%w", err)
	}

	rec := &Release{
		AppID:                   appID,
		VersionCode:             in.VersionCode,
		VersionName:             in.VersionName,
		MinSdk:                  in.MinSdk,
		MinSupportedVersionCode: in.MinSupportedVersionCode,
		Changelog:               in.Changelog,
		ObjectKey:               key,
		SHA256:                  sha,
		Size:                    size,
		CreatedBy:               in.CreatedBy,
		CreatedAt:               s.clock.Now(),
	}
	if err := s.repo.Create(ctx, rec); err != nil {
		// 登记失败：尽力回滚刚写的**唯一**对象（不会碰到任何已发布版本）。
		if derr := s.store.Delete(ctx, key); derr != nil {
			s.logger.Warn("安装包登记失败后的对象回滚也失败", "key", key, "deleteErr", derr, "createErr", err)
		}
		if errors.Is(err, ErrReleaseExists) {
			return nil, apperr.Errorf(apperr.CodeConflict,
				"应用 %s 的版本 %d 已存在且不可覆盖", appID, in.VersionCode)
		}
		return nil, fmt.Errorf("release: 登记发布元数据失败：%w", err)
	}

	s.logger.Info("安装包已发布", "appId", appID, "versionCode", in.VersionCode,
		"versionName", in.VersionName, "size", size, "sha256", sha)
	return rec, nil
}

// Latest 返回该应用的最新版本；尚无任何版本时返回 ErrNoRelease。
func (s *Service) Latest(ctx context.Context, appID string) (*Release, error) {
	if err := ValidateApplicationID(appID); err != nil {
		return nil, err
	}
	rec, err := s.repo.Latest(ctx, appID)
	if err != nil {
		if errors.Is(err, ErrNoRelease) {
			return nil, err
		}
		return nil, fmt.Errorf("release: 取最新版本失败：%w", err)
	}
	return rec, nil
}

// Get 按版本码取一个已发布的版本；不存在返回 ErrReleaseNotFound。
func (s *Service) Get(ctx context.Context, appID string, versionCode int64) (*Release, error) {
	if err := ValidateApplicationID(appID); err != nil {
		return nil, err
	}
	if err := ValidateVersionCode(versionCode); err != nil {
		return nil, err
	}
	rec, err := s.repo.GetByRef(ctx, appID, versionCode)
	if err != nil {
		if errors.Is(err, ErrReleaseNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("release: 取版本失败：%w", err)
	}
	return rec, nil
}

// OpenPack 打开一个安装包的只读流（后端流式代理对象存储）。
//
// 调用方**必须** Close 返回的流。整包不读进内存：安装包可达数百 MB，
// 一次性读入会把并发下载变成内存放大攻击面。
func (s *Service) OpenPack(ctx context.Context, rec *Release) (io.ReadCloser, error) {
	if s.store == nil {
		return nil, apperr.Errorf(apperr.CodeValidationFailed, "对象存储未配置，暂不可下载安装包")
	}
	rc, err := s.store.Get(ctx, rec.ObjectKey)
	if err != nil {
		if errors.Is(err, platform.ErrObjectNotFound) {
			// 元数据在库、对象却没了：这是数据不一致，不能报成「版本不存在」
			// （那会让运维以为版本码写错了）。用 not_found 码但消息点明真相。
			return nil, apperr.Errorf(apperr.CodeNotFound,
				"应用 %s 版本 %d 的安装包对象缺失（元数据存在，存储中不存在）", rec.AppID, rec.VersionCode)
		}
		return nil, fmt.Errorf("release: 读取安装包失败：%w", err)
	}
	return rc, nil
}
