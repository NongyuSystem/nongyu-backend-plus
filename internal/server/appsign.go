package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
)

// 安卓请求签名（ADR-0003）。
//
// 这层证明「请求来自持有当前密钥的农屿客户端」，不证明「用户是谁」。
// 用户身份仍由既有 HMAC 会话令牌负责（auth.go / revocable_auth.go），这里不加 JWT。
//
// 无 GMS 的国产机做不到 Play Integrity / Key Attestation 的平台证明，密钥会进 APK、
// 可被提取。设计目标因此是可轮换、可吊销（删掉 kid），不是「密钥不可提取」。
//
// 规范化串（NY1，与 nongyu-app-plus 的 AppRequestSigner 必须逐字节一致）：
//
//	NY1\n
//	<keyId>\n
//	<unixSeconds>\n
//	<nonce>\n
//	<METHOD>\n
//	<escapedPath>\n
//	<rawQuery>\n
//	<lowercase-hex-sha256(body)>\n
//
// 签名 = base64url(HMAC-SHA256(key, canonical))，无 padding。
// 重放防护不由 HMAC 自动提供：±5 分钟时间窗 + nonce 只可用一次。

const (
	HeaderAppKeyID     = "X-Nongyu-Key-Id"
	HeaderAppTimestamp = "X-Nongyu-Timestamp"
	HeaderAppNonce     = "X-Nongyu-Nonce"
	HeaderAppSignature = "X-Nongyu-Signature"

	appSignProtocol = "NY1"
	// appSignWindow 是允许的时钟偏差。客户端与服务端必须用同一个数。
	appSignWindow = 5 * time.Minute
	// nonce 要活过「时间戳仍会被接受」的全部区间，再留一点边界。
	appSignNonceTTL = 2*appSignWindow + 30*time.Second
	// 与教务回传体上限对齐（1 MiB）。更大的请求直接拒绝，避免验签把内存打满。
	appSignMaxBody = 1 << 20

	memoryNonceCap = 100_000
)

// NonceStore 认领一次 nonce。fresh=false 表示窗口内见过（重放）。
// err != nil 表示存储不可用，调用方必须拒绝（fail-closed），不能当成「没见过」。
type NonceStore interface {
	Claim(ctx context.Context, nonce string, ttl time.Duration) (fresh bool, err error)
}

// OnceClaimer 是 Redis 侧的原子占位。与 platform.RedisCache.TryAcquire 分开：
// TryAcquire 在熔断时返回 (false, nil)，验签无法区分「重放」和「存储挂了」。
type OnceClaimer interface {
	ClaimOnce(ctx context.Context, key string, ttl time.Duration) (fresh bool, err error)
}

type redisNonceStore struct{ claimer OnceClaimer }

// NewRedisNonceStore 用外置存储做跨实例的 nonce 去重。
func NewRedisNonceStore(c OnceClaimer) NonceStore {
	return redisNonceStore{claimer: c}
}

func (s redisNonceStore) Claim(ctx context.Context, nonce string, ttl time.Duration) (bool, error) {
	if s.claimer == nil {
		return false, fmt.Errorf("appsign: redis nonce store 未装配")
	}
	return s.claimer.ClaimOnce(ctx, "appsign:nonce:"+nonce, ttl)
}

type memoryNonceStore struct {
	mu    sync.Mutex
	clock platform.Clock
	seen  map[string]time.Time
	cap   int
}

// NewMemoryNonceStore 是单进程去重。多实例部署不能用它做重放防护。
func NewMemoryNonceStore(clock platform.Clock) NonceStore {
	if clock == nil {
		clock = platform.NewRealClock()
	}
	return &memoryNonceStore{clock: clock, seen: make(map[string]time.Time), cap: memoryNonceCap}
}

func (s *memoryNonceStore) Claim(_ context.Context, nonce string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		return false, fmt.Errorf("appsign: nonce ttl 必须为正")
	}
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if exp, ok := s.seen[nonce]; ok && exp.After(now) {
		return false, nil
	}
	if len(s.seen) >= s.cap && !s.sweepLocked(now) {
		return false, fmt.Errorf("appsign: 内存 nonce 存储已满")
	}
	s.seen[nonce] = now.Add(ttl)
	return true, nil
}

func (s *memoryNonceStore) sweepLocked(now time.Time) bool {
	for k, exp := range s.seen {
		if !exp.After(now) {
			delete(s.seen, k)
		}
	}
	return len(s.seen) < s.cap
}

type appSignError struct {
	status int
	code   string
	reason string
	keyID  string
}

func (e *appSignError) Error() string { return e.reason }

// SignAppRequest 按 NY1 计算签名。测试与客户端向量对齐用；生产验签走同一套 canonical。
func SignAppRequest(keyID string, key []byte, unixSeconds int64, nonce, method, path, rawQuery string, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(canonicalAppSign(keyID, strconv.FormatInt(unixSeconds, 10), nonce, method, path, rawQuery, body)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func canonicalAppSign(keyID, timestamp, nonce, method, path, rawQuery string, body []byte) string {
	sum := sha256.Sum256(body)
	return strings.Join([]string{
		appSignProtocol,
		keyID,
		timestamp,
		nonce,
		strings.ToUpper(method),
		path,
		rawQuery,
		hex.EncodeToString(sum[:]),
	}, "\n") + "\n"
}

// IsAppSignExempt 只放过探针。登录、发验证码、业务接口都要签名——
// 那些才是仿造请求的目标。不要和 IsPublicPath 混用：公开≠免签名。
func IsAppSignExempt(path string) bool {
	return path == "/healthz" || path == "/status"
}

// AppSignMiddleware 在用户鉴权之前拒绝未签名请求。
// keys 至少一把；空 map 是装配错误，直接 panic，避免静默放行。
func AppSignMiddleware(keys map[string][]byte, nonces NonceStore, clock platform.Clock, logger *slog.Logger) gin.HandlerFunc {
	copied := copyAppSignKeys(keys)
	if len(copied) == 0 {
		panic("appsign: AppSignMiddleware 需要至少一把密钥")
	}
	if nonces == nil {
		panic("appsign: AppSignMiddleware 需要 NonceStore")
	}
	if clock == nil {
		clock = platform.NewRealClock()
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return func(c *gin.Context) {
		if IsAppSignExempt(c.Request.URL.Path) {
			c.Next()
			return
		}
		if err := verifyAppSign(c, copied, nonces, clock); err != nil {
			var se *appSignError
			if !errors.As(err, &se) {
				se = &appSignError{status: http.StatusUnauthorized, code: "app_sign_invalid", reason: err.Error()}
			}
			logger.Warn("请求签名被拒绝", "path", c.Request.URL.Path, "reason", se.reason, "key_id", se.keyID)
			c.AbortWithStatusJSON(se.status, gin.H{"code": se.code})
			return
		}
		c.Next()
	}
}

func copyAppSignKeys(keys map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(keys))
	for id, key := range keys {
		out[id] = append([]byte(nil), key...)
	}
	return out
}

func verifyAppSign(c *gin.Context, keys map[string][]byte, nonces NonceStore, clock platform.Clock) error {
	keyID := c.GetHeader(HeaderAppKeyID)
	timestamp := c.GetHeader(HeaderAppTimestamp)
	nonce := c.GetHeader(HeaderAppNonce)
	signature := c.GetHeader(HeaderAppSignature)
	if keyID == "" || timestamp == "" || nonce == "" || signature == "" {
		return &appSignError{status: http.StatusUnauthorized, code: "app_sign_invalid", reason: "missing signature headers", keyID: keyID}
	}
	if !validAppSignKeyID(keyID) || !validAppSignNonce(nonce) || !validAppSignTimestamp(timestamp) {
		return &appSignError{status: http.StatusUnauthorized, code: "app_sign_invalid", reason: "malformed signature headers", keyID: keyID}
	}
	key, ok := keys[keyID]
	if !ok {
		return &appSignError{status: http.StatusUnauthorized, code: "app_sign_invalid", reason: "unknown key id", keyID: keyID}
	}
	unixSeconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return &appSignError{status: http.StatusUnauthorized, code: "app_sign_invalid", reason: "bad timestamp", keyID: keyID}
	}
	skew := clock.Now().Sub(time.Unix(unixSeconds, 0))
	if skew < 0 {
		skew = -skew
	}
	if skew > appSignWindow {
		return &appSignError{status: http.StatusUnauthorized, code: "app_sign_invalid", reason: "timestamp outside window", keyID: keyID}
	}

	body, err := readAppSignBody(c)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return &appSignError{status: http.StatusRequestEntityTooLarge, code: "app_sign_too_large", reason: "body too large", keyID: keyID}
		}
		return &appSignError{status: http.StatusBadRequest, code: "app_sign_invalid", reason: "body unreadable", keyID: keyID}
	}

	want, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return &appSignError{status: http.StatusUnauthorized, code: "app_sign_invalid", reason: "bad signature encoding", keyID: keyID}
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(canonicalAppSign(keyID, timestamp, nonce, c.Request.Method, c.Request.URL.EscapedPath(), c.Request.URL.RawQuery, body)))
	if !hmac.Equal(mac.Sum(nil), want) {
		return &appSignError{status: http.StatusUnauthorized, code: "app_sign_invalid", reason: "signature mismatch", keyID: keyID}
	}

	fresh, err := nonces.Claim(c.Request.Context(), nonce, appSignNonceTTL)
	if err != nil {
		return &appSignError{status: http.StatusServiceUnavailable, code: "app_sign_unavailable", reason: "nonce store unavailable", keyID: keyID}
	}
	if !fresh {
		return &appSignError{status: http.StatusUnauthorized, code: "app_sign_invalid", reason: "nonce replayed", keyID: keyID}
	}
	return nil
}

func readAppSignBody(c *gin.Context) ([]byte, error) {
	if c.Request.Body == nil {
		return nil, nil
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, appSignMaxBody)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	c.Request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return body, nil
}

func validAppSignKeyID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validAppSignNonce(nonce string) bool {
	if len(nonce) < 16 || len(nonce) > 128 {
		return false
	}
	for i := 0; i < len(nonce); i++ {
		c := nonce[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

func validAppSignTimestamp(ts string) bool {
	if len(ts) < 1 || len(ts) > 12 {
		return false
	}
	for i := 0; i < len(ts); i++ {
		if ts[i] < '0' || ts[i] > '9' {
			return false
		}
	}
	return true
}
