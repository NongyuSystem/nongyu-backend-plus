package main

// 临时工具：生成一枚 HMAC 自包含 token，供端到端验证使用。
// 用法：go run ./tools/gentoken -key <AUTH_KEY> -user <userID>

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"time"
)

func main() {
	key := flag.String("key", "", "AUTH_KEY")
	user := flag.String("user", "e2e-user", "userID")
	ttl := flag.Duration("ttl", time.Hour, "有效期")
	flag.Parse()

	claims, _ := json.Marshal(map[string]any{
		"sub": *user,
		"exp": time.Now().Add(*ttl).Unix(),
	})
	payload := base64.RawURLEncoding.EncodeToString(claims)
	m := hmac.New(sha256.New, []byte(*key))
	m.Write([]byte(payload))
	fmt.Printf("%s.%s\n", payload, base64.RawURLEncoding.EncodeToString(m.Sum(nil)))
}
