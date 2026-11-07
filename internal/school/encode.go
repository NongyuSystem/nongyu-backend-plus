package school

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"net/url"
	"strings"
)

// WebVPN 地址编码常量，逐字来自
// docs/tech/旧农屿WebVPN可重写契约清单.md §2:77-84。
const (
	webvpnOrigin = "https://webvpn.sicau.edu.cn"
	webvpnHost   = "webvpn.sicau.edu.cn"

	// KEY = IV，16 字节 ASCII（AES-128）。
	webvpnKey = "wrdvpnisthebest!"
	// KEY 的 ASCII hex，路径段固定前缀；缺它网关会 302 到登录页。
	webvpnKeyHex = "77726476706e69737468656265737421"

	blockSize = 16
	// 主机名右侧填充字符 '0' (0x30)，填充到 16 字节对齐。
	padByte = byte('0')
)

// encodeVpnHost 返回 KEY_HEX + host_cipher_hex。
//
// 算法（契约清单 §2:81-82，encodeVpnHost / aesCfb128Encrypt）：
//  1. 主机名 ASCII 字节右侧用 '0' 填充到 16 字节对齐；
//  2. KEY=IV="wrdvpnisthebest!"，AES-128-CFB、segment_size=128
//     （Go 标准库 cipher.NewCFBEncrypter 默认即整块反馈 = CFB-128）；
//  3. 密文只取前 len(host) 字节，转小写 hex。
func encodeVpnHost(host string) (string, error) {
	block, err := aes.NewCipher([]byte(webvpnKey))
	if err != nil {
		// 常量恰为 16 字节，理论不可达；保留错误通道而非吞掉。
		return "", err
	}
	stream := cipher.NewCFBEncrypter(block, []byte(webvpnKey)) // IV = KEY

	paddedLen := ((len(host) + blockSize - 1) / blockSize) * blockSize
	padded := make([]byte, paddedLen)
	copy(padded, host)
	for i := len(host); i < paddedLen; i++ {
		padded[i] = padByte
	}

	out := make([]byte, paddedLen)
	stream.XORKeyStream(out, padded)

	return webvpnKeyHex + hex.EncodeToString(out[:len(host)]), nil
}

// vpnSchemeSegment 构造路径里的 scheme 段。
// 基础形态为 /{scheme}/（http 或 https）。探索笔记与 issue #12 算法条目记录了
// 带端口时的 /{scheme}-{port}/ 形态（如 /https-443/…）；此处按该形态实现，
// 端口从 host 中剥离后只体现在这一段。
func vpnSchemeSegment(scheme, port string) string {
	if port != "" {
		return scheme + "-" + port
	}
	return scheme
}

// Encode 把校内目标的绝对地址改写为 WebVPN 网关编码地址（纯函数）。
//
// 契约（契约清单 §2:83-84，encodeWebVpnUrl）：
//   - 形态：https://webvpn.sicau.edu.cn/{scheme}/{KEY_HEX}{host_cipher_hex}{原 path}?{原 query}
//   - path 与 query 保持明文，片段（#…）丢弃；无 path 补 "/"；
//   - 已是 webvpn 域的地址原样返回，不重复编码；
//   - 非 http(s) / 相对路径 / 空串 / 非法 URL 一律原样返回，不 panic。
//
// 纯函数保证：只读入参、常量与标准库；不发起网络请求、不读写任何全局可变状态、
// 不读配置/环境。对同一输入重复调用结果逐字一致。
//
// 注意白名单边界：本函数是底层编解码器，与旧仓 encodeWebVpnUrl 一样不自行校验
// 目标白名单；「调用方只能传逻辑目标名」的约束由 Resolve / TargetName 与后续
// 子域的调用层把守（见架构落点草图 §4 的 TargetResolver 与 EncodeCodec 分立）。
func Encode(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		// 非法 URL：按「非 http(s) 原样返回」口径处理，不 panic、不联网。
		return rawURL, nil
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return rawURL, nil
	}
	if u.Host == "" {
		return rawURL, nil
	}

	host := u.Hostname() // 已去端口
	if host == "" {
		return rawURL, nil
	}
	// 已是网关域：原样返回。主机名按 RFC 大小写不敏感，用 EqualFold 更稳；
	// 两个基准密文的输入均为小写，不受影响。
	if strings.EqualFold(host, webvpnHost) {
		return rawURL, nil
	}

	segment, err := encodeVpnHost(host)
	if err != nil {
		return "", err
	}

	path := u.EscapedPath() // 保留原始转义，即「path 保持明文」
	if path == "" {
		path = "/"
	}
	query := ""
	if u.RawQuery != "" || u.ForceQuery {
		query = "?" + u.RawQuery
	}

	return webvpnOrigin + "/" + vpnSchemeSegment(scheme, u.Port()) + "/" + segment + path + query, nil
}
