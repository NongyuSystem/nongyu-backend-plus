package jiaowu

import "testing"

// ValidateLoginRequest 的表驱动测试：必填、空白、超长、合法。
func TestValidateLoginRequest(t *testing.T) {
	cases := []struct {
		name    string
		in      loginRequest
		wantErr bool
	}{
		{"合法", loginRequest{Code: "5401", UUID: "3rEZGj93MYhmCxAAKmrU"}, false},
		{"带前后空白", loginRequest{Code: "  5401  ", UUID: "  uuid-1  "}, false},
		{"缺 code", loginRequest{UUID: "uuid-1"}, true},
		{"code 全空白", loginRequest{Code: "   ", UUID: "uuid-1"}, true},
		{"缺 uuid", loginRequest{Code: "5401"}, true},
		{"uuid 全空白", loginRequest{Code: "5401", UUID: "  "}, true},
		{"code 超长", loginRequest{Code: string(make([]byte, maxLoginCodeLen+1)), UUID: "uuid-1"}, true},
		{"uuid 超长", loginRequest{Code: "5401", UUID: string(make([]byte, maxLoginUUIDLen+1))}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLoginRequest(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got err=%v", tc.wantErr, err)
			}
		})
	}
}
