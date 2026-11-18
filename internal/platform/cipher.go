package platform

// Cipher 的生产实现：AES-256-GCM 认证加密（原 caps_mock.go 的 MockCipher 段）。
//
// ⚠️ 历史命名陷阱：本文件里的类型叫 MockCipher，但它是**真实的生产加密实现**，
// 不是内存桩。名字来自骨架期，为避免波及大量既有调用而保留，同时用类型别名
// AESCipher 提供一眼可辨的真名。
//
// **新代码一律用 NewAESCipher / AESCipher**，不要用 NewMockCipher / MockCipher。
// 之所以把它从 caps_mock.go 里单独拆出来：那个文件名会让人以为整份文件都是测试桩，
// 而实际上这里装着 tgt、身份证号、密码镜像的加密实现。

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"strings"
)

const (
	// 格式：[magic(4) | nonce(12) | GCM 密文+tag(n)]
	aeadMagic   = "NGY2"
	aeadHeadLen = 4 + 12 // magic + GCM 标准 nonce 长度
	aeadKeyLen  = 32     // AES-256
	// 开发占位密钥（与 config.go 的 placeholderTicketKey 同值）。
	// 只用来「识别并拒绝」显式传入的弱密钥，**绝不再作为加密密钥回落到它**。
	// 历史实现：空密钥 → 回落到这个源码里公开的字符串，任何读源码的人都能
	// 用同一常量构造 Cipher 解密 tgt / 身份证密文。现改为弱密钥一律换成本进程
	// 随机临时密钥（见 ephemeralCipherKey），并 WARN 提示配置缺失。
	defaultCipherKey = "dev-only-placeholder-key-change-me"
)

var errInvalidSealed = errors.New("platform: 无效的 sealed 数据（格式错误、密钥不符或已被篡改）")

// MockCipher 是 Cipher 的实现：AES-256-GCM 认证加密。
// 名字沿用骨架期类型名以免改动装配层与既有调用方，但算法已是生产级 AEAD。
//
// AESCipher 是 MockCipher 的**真实实现名**：本类型实为生产级 AES-256-GCM，
// 不是内存桩。命名误导来自骨架期，改名会波及大量既有调用与测试，故用类型别名
// 提供一个一眼可辨的真名，装配层新代码应优先用 NewAESCipher。
// 这是 #21 / Spec M9 的处置：命名可区分，且零破坏。
type AESCipher = MockCipher

// NewAESCipher 构造真实 AES-256-GCM 加密器（与 NewMockCipher 同一实现）。
func NewAESCipher(key string) *AESCipher { return NewMockCipher(key) }

// 密钥来自配置注入，接口本身不暴露密钥；每次 Seal 用 crypto/rand 生成新 nonce，
// 因此同一明文两次封装产出不同密文。密文自带认证标签，篡改/错误密钥/截断一律拒绝。
type MockCipher struct {
	key []byte
}

// ephemeralCipherKey 是「缺配置时」的本进程随机临时密钥。
//
// 为什么是随机而不是源码常量：历史实现把空密钥回落到 defaultCipherKey（一个
// 公开在源码里的字符串），攻击者只要读源码就能构造同样的 Cipher 解密 tgt /
// 身份证号密文。改成 crypto/rand 生成的进程级随机密钥后：
//   - 同一进程内多个空密钥 Cipher 共享它 → 既有「空密钥能往返」的契约不破；
//   - 但外部任何人（含读源码者）无法预知/重建它 → 公开常量不再能解密；
//   - 进程重启即变 → 用弱密钥落库的密文在重启后解不开，这是**刻意**的
//     fail-closed：逼迫部署方显式配置真实 TICKET_KEY，而不是依赖隐蔽默认值。
//
// 装配层/配置层仍应把「prod 必须用强密钥」作为硬校验（config.Load 已做），
// 本层只负责「即使配置漏了，也不退化成全世界都知道的密钥」。
var ephemeralCipherKey = func() []byte {
	b := make([]byte, aeadKeyLen)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败属极端异常；不回落到任何可预测值，直接拒绝启动更安全。
		panic("platform: 无法生成临时 Cipher 密钥：" + err.Error())
	}
	return b
}()

// NewMockCipher 用配置里的密钥构造：SHA-256 派生出固定 32 字节 AES 密钥。
//
// 空密钥 / 仅由占位常量或空基串派生的弱密钥一律不回落到公开常量，改用
// sha256(本进程随机盐 || key) 派生并 WARN。这样：
//   - 外部攻击者即便读源码、知道 key 字符串（"" / 占位值 / ":idcard" 等），
//     也因不知随机盐而无法派生出同一把 AES 密钥，解不开 tgt / 身份证密文；
//   - 同一进程内「同一个弱 key 字符串」仍派生出同一把密钥，Seal/Open 往返不破
//     （不破坏既有单测与骨架装配：如 school/jiaowu.New 的 NewMockCipher("")）；
//   - 不同弱 key 之间仍保持域分隔（":tgt" 与 ":idcard" 密钥不同），
//     app.go 的派生用法语义不变；
//   - 进程重启盐变化 → 弱密钥落库密文重启后不可解，这是刻意的 fail-closed，
//     逼迫部署方显式配置真实 TICKET_KEY，而不是依赖隐蔽默认值；
//   - 生产强密钥路径完全不受影响（weakCipherKey 返回 false 走原 sha256(key)）。
func NewMockCipher(key string) *MockCipher {
	if weakCipherKey(key) {
		log.Printf("WARN platform: Cipher 收到空/占位密钥，已改用本进程随机盐派生临时密钥；请显式配置 TICKET_KEY（缺配置时密文在进程重启后不可解）")
		buf := make([]byte, 0, len(ephemeralCipherKey)+len(key))
		buf = append(buf, ephemeralCipherKey...)
		buf = append(buf, key...)
		sum := sha256.Sum256(buf)
		return &MockCipher{key: sum[:]}
	}
	sum := sha256.Sum256([]byte(key))
	return &MockCipher{key: sum[:]}
}

// weakCipherKey 判断是否为「绝不能拿来派生真实密钥」的弱输入：
//   - 空 / 纯空白；
//   - 等于公开的 defaultCipherKey 本身；
//   - 以 defaultCipherKey 为前缀再加域分隔派生（如 "<占位值>:tgt"、
//     "<占位值>:idcard"），这些是 app.go 在 TicketKey 为占位值时拼出的形态；
//   - 以 ":" 开头的域派生串（app.go 在 TicketKey 为空时拼出的 ":tgt"、
//     ":idcard"、":password"）——基串为空，同样可被任何人照源码复现。
//
// prod 由 config.Load 拦住，但 staging/dev 或漏配场景仍会走到这里，必须一并挡掉。
func weakCipherKey(key string) bool {
	if strings.TrimSpace(key) == "" {
		return true
	}
	if key == defaultCipherKey || strings.HasPrefix(key, defaultCipherKey+":") {
		return true
	}
	return strings.HasPrefix(key, ":")
}

// Seal 实现 Cipher：随机 nonce + AES-GCM 封装，返回 [magic|nonce|ciphertext+tag]。
func (c *MockCipher) Seal(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("platform: 生成 nonce 失败：%w", err)
	}
	out := make([]byte, 0, aeadHeadLen+len(plaintext)+gcm.Overhead())
	out = append(out, aeadMagic...)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, []byte(aeadMagic))
	return out, nil
}

// Open 实现 Cipher：校验格式/密钥/完整性后还原明文。
func (c *MockCipher) Open(sealed []byte) ([]byte, error) {
	if len(sealed) < aeadHeadLen {
		return nil, errInvalidSealed
	}
	if string(sealed[:4]) != aeadMagic {
		return nil, errInvalidSealed
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := sealed[4:aeadHeadLen]
	ct := sealed[aeadHeadLen:]
	plain, err := gcm.Open(nil, nonce, ct, []byte(aeadMagic))
	if err != nil {
		return nil, errInvalidSealed
	}
	return plain, nil
}
