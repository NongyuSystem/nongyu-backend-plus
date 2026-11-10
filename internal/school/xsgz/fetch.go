package xsgz

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"nongyu-backend-plus/internal/platform"
)

// FetchToken 用统一的 go-admin-tgt 换出学工系统的 access token。
//
// 这是「一次登录、后端全权取数」的关键一环：客户端只需交一张 tgt，
// 后端就能自己换出取数所需的 token，**不需要浏览器、不需要 WebView**。
//
// 两步（2026-11-10 实测）：
//
//	① GET  auth/api/v1/cas/login?appid=ywtb  + Cookie: go-admin-tgt
//	   → 解密得 {st, users[0].id}
//	② POST xsgz /gateway/xg-agg-pc/cas/login  form: ticket=<st>:<userId>
//	   → {code:"0", data:{token:"<608 字符>"}}
//
// 注意 `H-Zove-ClientType: PC` 是 ② 的必需头，漏了会报「客户端类型不能够为空」。
func FetchToken(ctx context.Context, doer platform.HTTPDoer, authBase, tgt string, decrypt func(requestID, data string) ([]byte, error)) (string, error) {
	if doer == nil {
		return "", fmt.Errorf("xsgz: FetchToken 需要 HTTPDoer")
	}
	if strings.TrimSpace(tgt) == "" {
		return "", fmt.Errorf("xsgz: 缺少 %s", goAdminTgtCookie)
	}
	if decrypt == nil {
		return "", fmt.Errorf("xsgz: FetchToken 需要解密函数")
	}
	if authBase == "" {
		authBase = "https://auth.sicau.edu.cn"
	}

	// ① tgt → st + userId
	u := strings.TrimRight(authBase, "/") + "/api/v1/cas/login?appid=ywtb"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Cookie", goAdminTgtCookie+"="+strings.TrimSpace(tgt))
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120")

	resp, err := doer.Do(ctx, req)
	if err != nil {
		return "", fmt.Errorf("xsgz: 换票请求失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", err
	}
	var env struct {
		Code      int    `json:"code"`
		Msg       string `json:"msg"`
		RequestID string `json:"requestId"`
		Data      string `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("xsgz: 换票响应解析失败：%w", err)
	}
	if env.Code != 200 || env.Data == "" {
		return "", fmt.Errorf("xsgz: 换票失败（code=%d %s）", env.Code, env.Msg)
	}
	plain, err := decrypt(env.RequestID, env.Data)
	if err != nil {
		return "", fmt.Errorf("xsgz: 换票明文解密失败：%w", err)
	}
	var ti struct {
		ST    string `json:"st"`
		Users []struct {
			ID json.Number `json:"id"`
		} `json:"users"`
	}
	if err := json.Unmarshal(plain, &ti); err != nil {
		return "", fmt.Errorf("xsgz: 换票明文解析失败：%w", err)
	}
	if ti.ST == "" || len(ti.Users) == 0 {
		return "", fmt.Errorf("xsgz: 换票响应缺少 st 或 users")
	}
	userID := ti.Users[0].ID.String()

	// ② st:userId → token
	form := url.Values{}
	form.Set("ticket", ti.ST+":"+userID)
	req2, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://"+host+aggGateway+"/cas/login", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.Header.Set("H-Zove-Source", "20100")
	req2.Header.Set("H-Zove-ClientType", "PC")
	req2.Header.Set("H-Zove-TenantId", tenant)
	req2.Header.Set("H-Zove-AppKey", aggAppKey)
	req2.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120")

	resp2, err := doer.Do(ctx, req2)
	if err != nil {
		return "", fmt.Errorf("xsgz: 换 token 请求失败：%w", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	raw2, err := io.ReadAll(io.LimitReader(resp2.Body, maxBody))
	if err != nil {
		return "", err
	}
	if resp2.StatusCode != http.StatusOK {
		return "", fmt.Errorf("xsgz: 换 token 返回 %d", resp2.StatusCode)
	}
	var e2 struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw2, &e2); err != nil {
		return "", fmt.Errorf("xsgz: 换 token 响应解析失败：%w", err)
	}
	if e2.Code != "0" || e2.Data.Token == "" {
		return "", fmt.Errorf("xsgz: 换 token 失败（%s）", e2.Message)
	}
	return e2.Data.Token, nil
}

// EnterHall 是取 detail **之前必须完成的两步**（2026-11-10 实测）。
//
// 为什么需要它：拿到 token 后直接打 student/detail 会返回
// 500「验证权限异常」，但**同一个 token 只要先走这两个接口就能 200**。
// 说明 detail 端点校验的不是 token 有效性，而是 token 的「大厅上下文」。
//
//	① POST user/checkUserAndGetUserLevel → 拿到 levelNo（如 XS）与 units（学院号）
//	② POST user/enterHall                → 用上面的值进入大厅，建立上下文
//
// 两步都对 token 幂等：重复调用不会出错，所以每次取数前都走一遍最稳。
func EnterHall(ctx context.Context, doer platform.HTTPDoer, baseURL, token string) error {
	if doer == nil {
		return fmt.Errorf("xsgz: EnterHall 需要 HTTPDoer")
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("xsgz: EnterHall 缺少 token")
	}
	if baseURL == "" {
		baseURL = "https://" + host
	}
	base := strings.TrimRight(baseURL, "/") + aggGateway

	// 这两步走主应用网关（agg），Source 是 20100（不是 detail 的 20000）。
	post := func(path, body string) (json.RawMessage, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("accept", "application/json, text/plain, */*")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120")
		req.Header.Set("H-Zove-Token", strings.TrimSpace(token))
		req.Header.Set("H-Zove-Source", "20100")
		req.Header.Set("H-Zove-ClientType", "PC")
		req.Header.Set("H-Zove-TenantId", tenant)
		req.Header.Set("H-Zove-AppKey", aggAppKey)

		resp, err := doer.Do(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("xsgz: %s 请求失败：%w", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("xsgz: %s 返回 %d", path, resp.StatusCode)
		}
		var env struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("xsgz: %s 响应解析失败：%w", path, err)
		}
		if env.Code != "0" {
			return nil, fmt.Errorf("xsgz: %s 业务码 %s（%s）", path, env.Code, env.Message)
		}
		return env.Data, nil
	}

	// ① 取级别与单位
	data, err := post("/user/checkUserAndGetUserLevel", "")
	if err != nil {
		return err
	}
	var lv struct {
		LoginLevels []struct {
			LevelNo string `json:"levelNo"`
			Units   []struct {
				UnitType int    `json:"unitType"`
				UnitNo   string `json:"unitNo"`
			} `json:"units"`
		} `json:"loginLevels"`
	}
	if err := json.Unmarshal(data, &lv); err != nil {
		return fmt.Errorf("xsgz: 解析身份级别失败：%w", err)
	}
	levelNo, unitNo, unitType := "XS", "", 2
	if len(lv.LoginLevels) > 0 {
		levelNo = lv.LoginLevels[0].LevelNo
		if len(lv.LoginLevels[0].Units) > 0 {
			unitNo = lv.LoginLevels[0].Units[0].UnitNo
			unitType = lv.LoginLevels[0].Units[0].UnitType
		}
	}

	// ② 进入大厅（这一步才建立 detail 所需的上下文）
	hallBody, err := json.Marshal(map[string]any{
		"departNo":       unitNo,
		"userStateLevel": levelNo,
		"userType":       unitType,
		"accessSource":   "30100",
	})
	if err != nil {
		return err
	}
	if _, err := post("/user/enterHall", string(hallBody)); err != nil {
		return err
	}
	return nil
}

// headers 构造 detail 端点要求的请求头（2026-11-10 用浏览器完整 curl 实测确认）。
//
// 三处极易写错、写错就 403「API 接口资源未授权」或 500「验证权限异常」：
//   - H-Zove-Source 必须是 "20000"（子应用），不是主应用的 "20100"
//   - H-Zove-AppKey 必须是 "zovecenter-saip-xg-app-student"（子应用），不是 agg-pc
//   - H-Zove-UserLevel 必须带（学生为 "XS"），漏了就 403
func headers(token, userLevel string) map[string]string {
	if strings.TrimSpace(userLevel) == "" {
		userLevel = "XS"
	}
	return map[string]string{
		"H-Zove-Token":     strings.TrimSpace(token),
		"H-Zove-Source":    "20000",
		"H-Zove-TenantId":  tenant,
		"H-Zove-AppKey":    appKey,
		"H-Zove-UserLevel": userLevel,
	}
}

// FetchDetail 取学工系统的完整学生档案（一次请求多字段）。
//
// **必须按顺序走三步，缺任一都会 500「验证权限异常」**：
//
//	① POST user/checkUserAndGetUserLevel  取身份级别与单位
//	② POST user/enterHall                 进入大厅（升级 token 的权限上下文）
//	③ GET  student/detail/<学号>           这才拿得到 13 组档案
//
// 最容易踩的坑：拿到 token 就直接打 detail 会 500，而**同一个 token
// 只要先 enterHall 一次就能 200**。这是本函数把前两步内置进来的原因——
// 让调用方不必知道这个顺序。
func FetchDetail(ctx context.Context, doer platform.HTTPDoer, baseURL, token, studentNo string) (StudentDetail, error) {
	if doer == nil {
		return StudentDetail{}, fmt.Errorf("xsgz: FetchDetail 需要 HTTPDoer")
	}
	if strings.TrimSpace(token) == "" {
		return StudentDetail{}, fmt.Errorf("xsgz: 缺少 access token")
	}
	if strings.TrimSpace(studentNo) == "" {
		return StudentDetail{}, fmt.Errorf("xsgz: 缺少学号")
	}
	// 学号只允许数字，防止路径注入（这是拼进 URL 的唯一外部输入）。
	for _, r := range studentNo {
		if r < '0' || r > '9' {
			return StudentDetail{}, fmt.Errorf("xsgz: 学号只能包含数字")
		}
	}
	if baseURL == "" {
		baseURL = "https://" + host
	}

	// ①② 升级权限上下文（缺这两步，detail 必然 500）。
	if err := EnterHall(ctx, doer, baseURL, token); err != nil {
		return StudentDetail{}, err
	}

	u := strings.TrimRight(baseURL, "/") + detailGateway + "/student/detail/" + studentNo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return StudentDetail{}, err
	}
	req.Header.Set("accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120")
	req.Header.Set("Referer", strings.TrimRight(baseURL, "/")+"/student/personal/info")
	for k, v := range headers(token, "XS") {
		req.Header.Set(k, v)
	}

	resp, err := doer.Do(ctx, req)
	if err != nil {
		return StudentDetail{}, fmt.Errorf("xsgz: 取档案请求失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return StudentDetail{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return StudentDetail{}, fmt.Errorf("xsgz: 返回 %d（token 可能已失效或权限不足）", resp.StatusCode)
	}

	var env struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return StudentDetail{}, fmt.Errorf("xsgz: 响应解析失败：%w", err)
	}
	if env.Code != "0" {
		return StudentDetail{}, fmt.Errorf("xsgz: 业务码 %s（%s）", env.Code, env.Message)
	}
	return parseDetail(env.Data)
}
