// 临时工具：用**生产实现**（schoolauth.JiaowuSource）端到端验证，
// 而不是复刻一份逻辑。这样验证的就是将要上线的那段代码。
//
// 用法：go run ./tools/jiaowu-probe2 -tgt "<go-admin-tgt>"
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"time"

	"nongyu-backend-plus/internal/school/schoolauth"
)

// realDoer 是 platform.HTTPDoer 的真实实现（生产用出站闸口，这里直接用 http.Client）。
type realDoer struct{ c *http.Client }

func (d realDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	return d.c.Do(req.WithContext(ctx))
}

func main() {
	tgt := flag.String("tgt", "", "go-admin-tgt 的值")
	flag.Parse()
	if *tgt == "" {
		fmt.Println("需要 -tgt")
		return
	}

	src, err := schoolauth.NewJiaowuSource(schoolauth.JiaowuOptions{
		Doer: realDoer{c: &http.Client{
			Timeout: 60 * time.Second,
			// 关键：禁用自动跳转，让 JiaowuSource 自己跟随并收集每跳的 Set-Cookie。
			// 否则 Go 会自己跟完，中间跳的 ASPSESSIONID 就被丢了，
			// 实现收到的是最终响应（200、无 cookie），于是误判「未下发会话 cookie」。
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}},
		AuthBaseURL:   "https://auth.sicau.edu.cn",
		JiaowuBaseURL: "https://jiaowu.sicau.edu.cn",
	})
	if err != nil {
		fmt.Println("构造失败：", err)
		return
	}

	fmt.Println("=== 用生产实现取学籍 ===")
	raw, err := src.FetchAcademic(context.Background(), schoolauth.Credential{
		RawCookie: "go-admin-tgt=" + *tgt,
	})
	if err != nil {
		fmt.Println("❌ 失败：", err)
		return
	}
	fmt.Println("✅ 成功")
	fmt.Printf("  专业=%s\n", raw.Major)
	fmt.Printf("  班级=%s\n", raw.ClassName)
	fmt.Printf("  年级=%s\n", raw.Grade)
	if raw.RefreshedTGT != "" {
		fmt.Printf("  换票带回新 tgt（长度 %d）—— 可用于续期\n", len(raw.RefreshedTGT))
	}
}
