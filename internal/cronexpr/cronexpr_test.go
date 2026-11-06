package cronexpr

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"5字段每分钟", "* * * * *", "0 * * * * *", true},
		{"5字段每5分钟", "*/5 * * * *", "0 */5 * * * *", true},
		{"5字段带多余空白", "  0   1 * * *  ", "0 0 1 * * *", true},
		{"6字段原样", "*/10 * * * * *", "*/10 * * * * *", true},
		{"6字段问号", "*/1 * * * * ?", "*/1 * * * * ?", true},
		{"描述符daily", "@daily", "@daily", true},
		{"描述符every", "@every 1m", "@every 1m", true},
		{"单星号历史坏数据", "*", "", false},
		{"空表达式", "", "", false},
		{"4字段", "* * * *", "", false},
		{"7字段", "* * * * * * *", "", false},
		{"非法描述符", "@nope", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.in)
			if tc.ok {
				if err != nil {
					t.Fatalf("Normalize(%q) 意外报错: %v", tc.in, err)
				}
				if got != tc.want {
					t.Errorf("Normalize(%q) = %q, 期望 %q", tc.in, got, tc.want)
				}
			} else if err == nil {
				t.Errorf("Normalize(%q) 应当报错，却返回 %q", tc.in, got)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	bad := []string{
		"0 99 * * * *",  // 分钟越界
		"60 * * * * *",  // 秒越界
		"0 * * * FOO *", // 月份非法
		"* * * * * * *", // 7 字段
		"*",             // 旧版 CLI 截断出来的脏数据
		"",              // 空
	}
	for _, expr := range bad {
		if err := Validate(expr); err == nil {
			t.Errorf("Validate(%q) 应当报错", expr)
		}
	}

	good := []string{
		"* * * * *",
		"0 * * * * *",
		"*/1 * * * * ?",
		"@daily",
		"@every 30s",
		"0 0 2 * * *",
	}
	for _, expr := range good {
		if err := Validate(expr); err != nil {
			t.Errorf("Validate(%q) 不应当报错: %v", expr, err)
		}
	}
}

func TestParseAlwaysReturnsSixFieldsForFieldExpressions(t *testing.T) {
	expr, err := Parse("* * * * *")
	if err != nil {
		t.Fatalf("Parse 报错: %v", err)
	}
	if expr != "0 * * * * *" {
		t.Fatalf("5 字段未被归一化成 6 字段: %q", expr)
	}
}
