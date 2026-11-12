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

// Upload 处理一次附件上传（后端中转，**流式**，不落本地盘）。
//
// 为什么不用 Request.ParseMultipartForm：它会把整个文件先读进内存、超过
// maxMemory 时还会落到 os.CreateTemp 临时文件——既不满足「流式转发」也不满足
// 「不落本地盘」。这里改用 MultipartReader 逐部件流式读：只从第一个 file 部件
// 取前 512 字节嗅探格式，随后把「已读部分 + 剩余」拼成一个 reader 直接喂给
// ObjectStore.Put，全程只有 512 字节缓冲。
//
// 体积约束两道闸：
//
//	① MaxBytesReader 包住整个请求体（含 multipart 边界），超限读取时抛
//	   *http.MaxBytesError → attachment_too_large；
//	② 流式读取时用 io.LimitReader 限制单文件字节不超过 MaxAttachmentBytes+1，
//	   一旦读超就判 attachment_too_large，挡住「多部件里塞超大文件」。
func (s *Service) Upload(ctx context.Context, r *http.Request) (UploadResult, error) {
	if s.store == nil {
		return UploadResult{}, apperr.Errorf(apperr.CodeValidationFailed, "对象存储未配置，暂不可上传附件")
	}

	// 先包 MaxBytesReader：整个请求体（含 multipart 边界）都受此限。
	r.Body = http.MaxBytesReader(nil, r.Body, MaxAttachmentBytes+maxMultipartOverhead)

	mr, err := r.MultipartReader()
	if err != nil {
		return UploadResult{}, apperr.Errorf(apperr.CodeUnsupportedMedia, "请求不是 multipart/form-data")
	}

	// 只认第一个名为 file 的部件；其余忽略（单文件端点）。
	var file io.Reader
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			return UploadResult{}, tooLargeOrWrap(apperr.Errorf(apperr.CodeValidationFailed, "multipart 读取失败"))
		}
		if part.FormName() == "file" {
			file = part
			break
		}
		_ = part.Close()
	}
	if file == nil {
		return UploadResult{}, apperr.Errorf(apperr.CodeValidationFailed, "缺少 file 字段")
	}

	// 单文件硬限用第二道 MaxBytesReader（限 5MB，比外层更紧）：
	// 超过时读会返回 *http.MaxBytesError，由 tooLargeOrWrap 映射成
	// attachment_too_large。用 MaxBytesReader 而不是 LimitReader，是为了让
	// 「超限」成为可判定的错误而不是静默截断（LimitReader 会悄悄丢字节）。
	partBody := http.MaxBytesReader(nil, io.NopCloser(file), MaxAttachmentBytes)

	head := make([]byte, sniffHeaderLen)
	n, rerr := io.ReadFull(partBody, head)
	if rerr != nil && !errors.Is(rerr, io.ErrUnexpectedEOF) && !errors.Is(rerr, io.EOF) {
		return UploadResult{}, tooLargeOrWrap(fmt.Errorf("post: 读取附件失败: %w", rerr))
	}
	head = head[:n]
	if n == 0 {
		return UploadResult{}, apperr.Errorf(apperr.CodeValidationFailed, "附件为空")
	}

	ext, mediaType, err := SniffMediaType(head)
	if err != nil {
		return UploadResult{}, err
	}

	key, err := newObjectKey(s.clock.Now(), ext)
	if err != nil {
		return UploadResult{}, fmt.Errorf("post: 生成对象 key 失败: %w", err)
	}
	// 把 head 拼回 + 剩余受限流，一次性流式交给 ObjectStore。
	// size 传 -1：让 Put 以实际读到的字节数为准（契约允许）；体积由
	// partBody 这道 MaxBytesReader 保证，超限会在 Put 读取时返回错误。
	body := io.MultiReader(bytes.NewReader(head), partBody)
	if _, err := s.store.Put(ctx, key, body, -1, mediaType); err != nil {
		return UploadResult{}, tooLargeOrWrap(err)
	}
	return UploadResult{Key: key}, nil
}
