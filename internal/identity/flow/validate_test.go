package flow

import "testing"

// ValidateLoginReq 的表驱动测试：每个 method 的合法体、必填缺失、格式非法、
// 以及 method 非法值都要覆盖。风格对齐 internal/content/post/post_test.go。

func TestValidateLoginReq_School(t *testing.T) {
	cases := []struct {
		name    string
		req     loginReq
		wantErr bool
	}{
		{"合法（只交 tgt）", loginReq{Method: "school", GoAdminTgt: "201690:TGT:xxx"}, false},
		{"合法（带学号）", loginReq{Method: "school", GoAdminTgt: "tgt", StudentNo: "202502204"}, false},
		{"合法（带 deviceId）", loginReq{Method: "school", GoAdminTgt: "tgt", DeviceID: "dev-1"}, false},
		{"合法（只交整套 cookie）", loginReq{Method: "school", Cookie: "go-admin-tgt=fake; laravel_session=fake"}, false},
		{"缺 goAdminTgt 且缺 cookie", loginReq{Method: "school"}, true},
		{"goAdminTgt 全空白且 cookie 为空", loginReq{Method: "school", GoAdminTgt: "   "}, true},
		{"cookie 里没有 go-admin-tgt", loginReq{Method: "school", Cookie: "laravel_session=fake"}, true},
		{"学号格式错", loginReq{Method: "school", GoAdminTgt: "tgt", StudentNo: "abc"}, true},
		{"deviceId 超长", loginReq{Method: "school", GoAdminTgt: "tgt", DeviceID: string(make([]byte, maxDeviceIDLen+1))}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLoginReq(tc.req)
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got err=%v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateLoginReq_Password(t *testing.T) {
	cases := []struct {
		name    string
		req     loginReq
		wantErr bool
	}{
		{"合法", loginReq{Method: "password", StudentNo: "202502204", Password: "pw"}, false},
		{"缺学号", loginReq{Method: "password", Password: "pw"}, true},
		{"缺密码", loginReq{Method: "password", StudentNo: "202502204"}, true},
		{"学号非9位", loginReq{Method: "password", StudentNo: "20250220", Password: "pw"}, true},
		{"密码超长", loginReq{Method: "password", StudentNo: "202502204", Password: string(make([]byte, maxPasswordLen+1))}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLoginReq(tc.req)
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got err=%v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateLoginReq_Mailcode(t *testing.T) {
	cases := []struct {
		name    string
		req     loginReq
		wantErr bool
	}{
		{"合法", loginReq{Method: "mailcode", Email: "a@b.com", Code: "a1b2"}, false},
		{"缺邮箱", loginReq{Method: "mailcode", Code: "a1b2"}, true},
		{"邮箱格式错", loginReq{Method: "mailcode", Email: "not-an-email", Code: "a1b2"}, true},
		{"缺验证码", loginReq{Method: "mailcode", Email: "a@b.com"}, true},
		{"验证码含符号", loginReq{Method: "mailcode", Email: "a@b.com", Code: "ab!2"}, true},
		{"验证码过短", loginReq{Method: "mailcode", Email: "a@b.com", Code: "a1"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLoginReq(tc.req)
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got err=%v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateLoginReq_IDCard(t *testing.T) {
	cases := []struct {
		name    string
		req     loginReq
		wantErr bool
	}{
		{"合法", loginReq{Method: "idcard", StudentNo: "202502204", IDCardSuffix4: "2614", NameInitials: "Y Y F"}, false},
		{"缺学号", loginReq{Method: "idcard", IDCardSuffix4: "2614", NameInitials: "YYF"}, true},
		{"后四位非数字", loginReq{Method: "idcard", StudentNo: "202502204", IDCardSuffix4: "26x4", NameInitials: "YYF"}, true},
		{"缺姓名首字母", loginReq{Method: "idcard", StudentNo: "202502204", IDCardSuffix4: "2614"}, true},
		{"姓名首字母含数字", loginReq{Method: "idcard", StudentNo: "202502204", IDCardSuffix4: "2614", NameInitials: "Y1F"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLoginReq(tc.req)
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got err=%v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateLoginReq_UnknownMethod(t *testing.T) {
	for _, m := range []string{"", "wechat", "SCHOOL", "bearer"} {
		if err := ValidateLoginReq(loginReq{Method: m}); err == nil {
			t.Fatalf("method=%q 应被拒绝", m)
		}
	}
}

func TestValidateAdminLoginReq(t *testing.T) {
	cases := []struct {
		name    string
		req     loginReq
		wantErr bool
	}{
		{"合法", loginReq{StudentNo: "202502204", Password: "pw"}, false},
		{"缺学号", loginReq{Password: "pw"}, true},
		{"缺密码", loginReq{StudentNo: "202502204"}, true},
		{"学号错", loginReq{StudentNo: "x", Password: "pw"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAdminLoginReq(tc.req)
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got err=%v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateMailCodeReq(t *testing.T) {
	if err := ValidateMailCodeReq("a@b.com"); err != nil {
		t.Fatalf("合法邮箱被拒: %v", err)
	}
	for _, bad := range []string{"", "   ", "no-at", "a@", "@b.com"} {
		if err := ValidateMailCodeReq(bad); err == nil {
			t.Fatalf("邮箱 %q 应被拒绝", bad)
		}
	}
}
