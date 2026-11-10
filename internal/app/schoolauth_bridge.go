package app

// 装配层的桥接：把 schoolauth 适配器接成身份域要的 SchoolAuthenticator。
//
// 为什么要桥：两个域各自定义了自己的入参/出参类型
//   - schoolauth.Credential / Identity（传输与解析视角）
//   - login.SchoolCredential / SchoolIdentity（业务视角）
// 谁也不该 import 谁，所以在装配层做这层翻译——这正是 composition root 的职责。

import (
	"context"
	"strconv"

	"nongyu-backend-plus/internal/identity/login"
	"nongyu-backend-plus/internal/school/schoolauth"
)

// schoolauthBridge 实现 login.SchoolAuthenticator。
type schoolauthBridge struct {
	a *schoolauth.Authenticator
}

// Authenticate 把 login 的凭据翻译成 schoolauth 的凭据，调用后把结果翻译回来。
//
// 注意：这里**还不知道农屿 userID**（登录正在确认身份的过程中），
// 因此 tgt 保管此时只能先以「客户端本次交来的」为准；
// 登录成功、有了 userID 之后再落库（由 flow 层在成功路径调用 TGTManager.Save）。
func (b schoolauthBridge) Authenticate(ctx context.Context, in login.SchoolCredential) (login.SchoolIdentity, error) {
	// 凭据形态只有一种：客户端完成学校登录后交来的 go-admin-tgt（及其同域 cookie）。
	// 曾经的 BearerToken 已删除（#28）：真实链路里客户端没有它，
	// 且换票响应本身就带身份，不需要第二条取数路径。
	cred := schoolauth.Credential{
		RawCookie: in.CookieHeader,
		UserID:    in.UserID,
		// 学工系统 token（唯一数据源，#28 后续）。
		XSGZToken: in.XSGZToken,
	}
	// 教务换票需要 go-admin-tgt（auth 域 cookie）。
	if in.GoAdminTgt != "" {
		cred.Cookies = append(cred.Cookies, schoolauth.SessionCookie{
			Name: "go-admin-tgt", Value: in.GoAdminTgt,
		})
	}
	for _, c := range in.Cookies {
		cred.Cookies = append(cred.Cookies, schoolauth.SessionCookie{Name: c.Name, Value: c.Value})
	}

	ident, err := b.a.Authenticate(ctx, cred)
	if err != nil {
		return login.SchoolIdentity{}, err
	}

	return login.SchoolIdentity{
		StudentNo:         ident.StudentNo,
		Name:              ident.Name,
		College:           ident.College,
		IDCardSuffix4:     ident.IDCardSuffix4,
		IDCardFull:        ident.IDCardFull,
		Major:             ident.Major,
		ClassName:         ident.ClassName,
		Grade:             ident.Grade,
		ProfileIncomplete: ident.ProfileIncomplete,
	}, nil
}

// 编译期断言：桥接必须满足身份域的接口。
var _ login.SchoolAuthenticator = schoolauthBridge{}

// tgtSaverBridge 把 schoolauth.TGTManager 适配成 login.TGTSaver。
//
// 为什么需要桥：login 子域要知道「按用户保管凭据」这个能力，但不能 import school；
// schoolauth 也不该知道 identity 的接口。翻译放在装配层。
type tgtSaverBridge struct {
	mgr *schoolauth.TGTManager
}

// Save 实现 login.TGTSaver。
func (b tgtSaverBridge) Save(ctx context.Context, userID int64, tgt, cookieHeader string) error {
	if b.mgr == nil {
		return nil
	}
	// 整套 cookie 里必须含 go-admin-tgt；客户端可能只交了 tgt 值而没交完整头，
	// 这时补上 tgt 本身，保证至少有一份可续期的最小凭据。
	cred := schoolauth.Credential{RawCookie: cookieHeader}
	full := cred.FullCookieHeader(tgt)
	return b.mgr.Save(ctx, strconv.FormatInt(userID, 10), schoolauth.TGTBundle{
		TGT:          tgt,
		CookieHeader: full,
	})
}

// 编译期断言。
var _ login.TGTSaver = tgtSaverBridge{}
