package flow

// 请求体字段合法性校验。
//
// 为什么单独成文件：loginReq 是一个「四方式共用」的扁平结构，之前绑定后直接
// 透传给编排层，method 非法值、各方式必填字段缺失、格式错误全都要等走到
// service / 学校侧才暴露。把校验收成纯函数后：① 传输层就能挡掉明显非法请求，
// ② 表驱动单测可以脱离 gin 直接跑，③ 规则集中一处，新增登录方式时不会漏。
//
// 风格对齐 internal/content/post/post.go 的 ValidateText 等：独立纯函数、
// 返回 error、不依赖 HTTP 上下文。不引入新依赖，也不改用 binding tag——
// binding tag 只能表达「必填/长度」这类静态约束，表达不了「按 method 分支」。

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	maxDeviceIDLen  = 128
	maxStudentNoLen = 32
	maxEmailLen     = 254
	maxCodeLen      = 16
	maxNameInitials = 64
	maxPasswordLen  = 256
)

var (
	reStudentNo     = regexp.MustCompile(`^[0-9]{9}$`)
	reIDCardSuffix4 = regexp.MustCompile(`^[0-9]{4}$`)
	reCode          = regexp.MustCompile(`^[0-9A-Za-z]{4,8}$`)
	reEmail         = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	reNameInitials  = regexp.MustCompile(`^[\p{L}\s]{1,64}$`)
)

// ValidateLoginReq 按 method 分支校验登录请求体。
//
// 各方式必填：
//   - school   → goAdminTgt（xsgzToken 可不填，后端会自己换）
//   - password → studentNo + password
//   - mailcode → email + code
//   - idcard   → studentNo + idCardSuffix4 + nameInitials
//
// 另外统一校验：deviceId 长度、学号 9 位数字、邮箱形态、验证码 4-8 位字母数字、
// 身份证后四位 4 位数字、姓名首字母只含字母/空白。
func ValidateLoginReq(req loginReq) error {
	// 公共字段：deviceId 可空，但给了就不能超长。
	if len(req.DeviceID) > maxDeviceIDLen {
		return fmt.Errorf("deviceId 超过 %d 字符", maxDeviceIDLen)
	}

	switch req.Method {
	case "school":
		// 客户端可以只交整套 cookie（旧契约的 CookieHeader），也可以单独交
		// goAdminTgt；两者都没带就没有可换票的凭据。cookie 形态下要求确实含
		// go-admin-tgt，避免把无关的 cookie 串当成有效凭据放过去。
		if strings.TrimSpace(req.GoAdminTgt) == "" && strings.TrimSpace(req.Cookie) == "" {
			return fmt.Errorf("school 登录缺少 goAdminTgt 或 cookie")
		}
		if strings.TrimSpace(req.GoAdminTgt) == "" && !strings.Contains(req.Cookie, "go-admin-tgt=") {
			return fmt.Errorf("cookie 中缺少 go-admin-tgt")
		}
		// studentNo 在 school 方式下不是必需的（换票响应会给），但给了就校验格式。
		if req.StudentNo != "" {
			if err := validateStudentNo(req.StudentNo); err != nil {
				return err
			}
		}
	case "password":
		if err := validateStudentNo(req.StudentNo); err != nil {
			return err
		}
		if strings.TrimSpace(req.Password) == "" {
			return fmt.Errorf("password 登录缺少 password")
		}
		if len(req.Password) > maxPasswordLen {
			return fmt.Errorf("password 超过 %d 字符", maxPasswordLen)
		}
	case "mailcode":
		if err := validateEmail(req.Email); err != nil {
			return err
		}
		if err := validateCode(req.Code); err != nil {
			return err
		}
	case "idcard":
		if err := validateStudentNo(req.StudentNo); err != nil {
			return err
		}
		if !reIDCardSuffix4.MatchString(req.IDCardSuffix4) {
			return fmt.Errorf("idCardSuffix4 必须是 4 位数字")
		}
		if strings.TrimSpace(req.NameInitials) == "" {
			return fmt.Errorf("idcard 登录缺少 nameInitials")
		}
		if len(req.NameInitials) > maxNameInitials || !reNameInitials.MatchString(req.NameInitials) {
			return fmt.Errorf("nameInitials 只能是字母/空白且不超过 %d 字符", maxNameInitials)
		}
	default:
		return fmt.Errorf("不支持的登录方式 %q（只允许 school / password / mailcode / idcard）", req.Method)
	}
	return nil
}

// ValidateAdminLoginReq 校验管理台登录：只认学号 + 密码。
func ValidateAdminLoginReq(req loginReq) error {
	if err := validateStudentNo(req.StudentNo); err != nil {
		return err
	}
	if strings.TrimSpace(req.Password) == "" {
		return fmt.Errorf("管理台登录缺少 password")
	}
	if len(req.Password) > maxPasswordLen {
		return fmt.Errorf("password 超过 %d 字符", maxPasswordLen)
	}
	return nil
}

// ValidateMailCodeReq 校验发送邮箱验证码的请求体。
func ValidateMailCodeReq(email string) error {
	return validateEmail(email)
}

func validateStudentNo(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("缺少学号")
	}
	if len(s) > maxStudentNoLen {
		return fmt.Errorf("学号超过 %d 字符", maxStudentNoLen)
	}
	if !reStudentNo.MatchString(s) {
		return fmt.Errorf("学号必须是 9 位数字")
	}
	return nil
}

func validateEmail(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("缺少邮箱")
	}
	if len(s) > maxEmailLen || !reEmail.MatchString(s) {
		return fmt.Errorf("邮箱格式非法")
	}
	return nil
}

func validateCode(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("缺少验证码")
	}
	if len(s) > maxCodeLen || !reCode.MatchString(s) {
		return fmt.Errorf("验证码必须是 4-8 位字母或数字")
	}
	return nil
}
