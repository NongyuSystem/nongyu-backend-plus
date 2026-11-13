package user

import "testing"

func strPtr(s string) *string { return &s }

func TestValidateExtras(t *testing.T) {
	cases := []struct {
		name      string
		qq        *string
		avatarURL *string
		wantErr   bool
	}{
		{"都不改（两个 nil）", nil, nil, false},
		{"合法 QQ", strPtr("1473443474"), nil, false},
		{"QQ 带前后空白", strPtr("  1473443474  "), nil, false},
		{"合法头像 URL", nil, strPtr("https://cdn.example.com/a.png"), false},
		{"http 也可", nil, strPtr("http://cdn.example.com/a.png"), false},
		{"两个都合法", strPtr("12345"), strPtr("https://x.com/a.png"), false},
		{"QQ 空白", strPtr("   "), nil, true},
		{"QQ 含字母", strPtr("123abc"), nil, true},
		{"QQ 超长", strPtr(string(make([]byte, maxQQLen+1))), nil, true},
		{"头像空白", nil, strPtr("  "), true},
		{"头像相对路径", nil, strPtr("/a.png"), true},
		{"头像 javascript:", nil, strPtr("javascript:alert(1)"), true},
		{"头像 data:", nil, strPtr("data:image/png;base64,AAA"), true},
		{"头像无 host", nil, strPtr("https:///a.png"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateExtras(tc.qq, tc.avatarURL)
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got err=%v", tc.wantErr, err)
			}
		})
	}
}
