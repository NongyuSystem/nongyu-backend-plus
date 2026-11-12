package post

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"nongyu-backend-plus/internal/apperr"
)

const (
	// maxMultipartOverhead 是 multipart 边界/头部允许的额外字节。
	// MaxBytesReader 限的是**整个请求体**（含多部件包装），所以上限要在
	// 5MB 之上留出这段开销，否则合法的单张 5MB 图片会被边界挤掉。
	maxMultipartOverhead = 64 << 10
	// sniffHeaderLen 是内容嗅探需要的头部字节数。stdlib 的 webp 签名
	// （RIFF....WEBPVP）需要前 14 字节，512 足够覆盖所有白名单格式。
	sniffHeaderLen = 512
)

// SniffMediaType 按内容嗅探图片格式，返回 (规范扩展名, contentType)。
//
// 只看传入的头部字节，绝不看扩展名或客户端声明的 Content-Type —— 那两者
// 可任意伪造，文件头骗不了人。返回 unsupported_media_type 表示不在白名单。
func SniffMediaType(header []byte) (string, string, error) {
	if len(header) > sniffHeaderLen {
		header = header[:sniffHeaderLen]
	}
	ct := http.DetectContentType(header)
	ext, err := ValidateMediaType(ct)
	if err != nil {
		return "", ct, err
	}
	return ext, ct, nil
}

// ObjectKey 按模板 posts/{yyyy}/{MM}/{uuid}.{ext} 生成对象 key。
//
// 用 UTC 取年月，保证同一时刻跨部署一致；拆成纯函数让 key 模板可被单测锁定。
func ObjectKey(now time.Time, id, ext string) string {
	n := now.UTC()
	return fmt.Sprintf("%s/%04d/%02d/%s.%s", objectKeyPrefix, n.Year(), int(n.Month()), id, ext)
}

// randomHexID 用 crypto/rand 生成 UUID v4 形状的十六进制串。
//
// 仓里没有 google/uuid 依赖，也不为一次 key 生成新增依赖；16 字节随机 +
// 版本号/变体位，形状与 uuid 一致，满足模板里的 {uuid}。
func randomHexID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

// newObjectKey 生成一个完整的附件对象 key。
func newObjectKey(now time.Time, ext string) (string, error) {
	id, err := randomHexID()
	if err != nil {
		return "", err
	}
	return ObjectKey(now, id, ext), nil
}

// UploadResult 是一次上传的产物。
type UploadResult struct {
	Key string `json:"key"`
}

// tooLargeOrWrap 把读/写过程中冒出的 *http.MaxBytesError 统一映射成
// attachment_too_large（413），其它内部错误原样上抛（由 writeError 走 500）。
func tooLargeOrWrap(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return apperr.Errorf(apperr.CodeAttachmentTooLarge, "附件超过 %d 字节", MaxAttachmentBytes)
	}
	return err
}

// Upload 处理一次附件上传（后端中转，流式，不落本地盘）。
//
// 体积约束两道闸：
//  ① MaxBytesReader 包住整个请求体（含 multipart 边界），超限读取时抛
//     *http.MaxBytesError → attachment_too_large；
//  ② 对 fileHeader.Size 与 MaxAttachmentBytes 比对，挡住「声明超限」。
//
// 之后先嗅探前 512 字节判格式（内容嗅探，非扩展名），再把「已读部分 +
// 剩余」拼回一个 reader 流式转发到 ObjectStore，全程不落本地盘、不整体入内存。
func (s *Service) Upload(ctx context.Context, r *http.Request) (UploadResult, error) {
	if s.store == nil {
		return UploadResult{}, apperr.Errorf(apperr.CodeValidationFailed, "对象存储未配置，暂不可上传附件")
	}

	// 先包 MaxBytesReader：整个请求体（含 multipart 边界）都受此限。
	r.Body = http.MaxBytesReader(nil, r.Body, MaxAttachmentBytes+maxMultipartOverhead)
	const maxMemory = maxMultipartOverhead
	if err := r.ParseMultipartForm(maxMemory); err != nil {
		// 超限可能发生在 ParseMultipartForm 读边界/部件时；先按原始错误判定，
		// 不能先包成别的错误再判断，否则 *MaxBytesError 类型会丢。
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return UploadResult{}, apperr.Errorf(apperr.CodeAttachmentTooLarge, "附件超过 %d 字节", MaxAttachmentBytes)
		}
		return UploadResult{}, apperr.Errorf(apperr.CodeValidationFailed, "multipart 请求非法")
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, hdr, err := r.FormFile("file")
	if err != nil {
		return UploadResult{}, apperr.Errorf(apperr.CodeValidationFailed, "缺少 file 字段")
	}
	defer file.Close()

	// 单文件大小上限（Header.Size 是 multipart 声明的字节数）。
	if err := ValidateAttachmentSize(hdr.Size); err != nil {
		return UploadResult{}, err
	}
	if hdr.Size <= 0 {
		return UploadResult{}, apperr.Errorf(apperr.CodeValidationFailed, "附件为空")
	}

	// 嗅探头部，再拼回完整流（不整体入内存）。
	head := make([]byte, sniffHeaderLen)
	n, rerr := io.ReadFull(file, head)
	if rerr != nil && !errors.Is(rerr, io.ErrUnexpectedEOF) && !errors.Is(rerr, io.EOF) {
		return UploadResult{}, tooLargeOrWrap(fmt.Errorf("post: 读取附件失败: %w", rerr))
	}
	head = head[:n]

	ext, mediaType, err := SniffMediaType(head)
	if err != nil {
		return UploadResult{}, err
	}

	body := io.MultiReader(bytes.NewReader(head), file)
	key, err := newObjectKey(s.clock.Now(), ext)
	if err != nil {
		return UploadResult{}, fmt.Errorf("post: 生成对象 key 失败: %w", err)
	}
	if _, err := s.store.Put(ctx, key, body, hdr.Size, mediaType); err != nil {
		return UploadResult{}, tooLargeOrWrap(err)
	}
	return UploadResult{Key: key}, nil
}
