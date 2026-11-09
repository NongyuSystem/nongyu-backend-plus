package schoolauth

// 本文件把适配器的结果接到身份域需要的形状上，而不让 schoolauth 直接 import identity。
//
// 做法：用**泛型化的小接口**做桥——装配层把 login.SchoolCredential 的字段
// 以参数形式传进来，适配器不依赖 identity 包的具体类型。
// 这样 school 与 identity 两个域之间没有直接 import；桥接逻辑放在装配层
// （internal/app），符合「业务域不互相 import」的约束。

// DomainIdentity 是身份域能接受的身份形状。
// 由装配层把 schoolauth.Identity 映射成 identity/login.SchoolIdentity。
type DomainIdentity struct {
	StudentNo     string
	Name          string
	College       string
	IDCardSuffix4 string
	// IDCardFull 是完整号（空表示只有掩码）。由装配层加密后写入画像。
	IDCardFull        string
	Major             string
	ClassName         string
	Grade             string
	ProfileIncomplete bool
}

// AsDomain 把适配器结果转成身份域形状。
func (i Identity) AsDomain() DomainIdentity {
	return DomainIdentity{
		StudentNo:         i.StudentNo,
		Name:              i.Name,
		College:           i.College,
		IDCardSuffix4:     i.IDCardSuffix4,
		IDCardFull:        i.IDCardFull,
		Major:             i.Major,
		ClassName:         i.ClassName,
		Grade:             i.Grade,
		ProfileIncomplete: i.ProfileIncomplete,
	}
}

// 已删除（#25 / Spec M10）：原 httpSource / HTTPSourceOptions / NewHTTPSource 骨架实现。
//
// 删除理由：
//  1. **零生产消费者**：全仓 grep 确认只有它自己的测试在用；装配层实际注入的是
//     YWTBSource（ywtb.go，基于 Bearer + laravel_session 的真实协议）。
//  2. **它是「等实测确认后再填」的占位**：ParseIdentity / ParseAcademic 需外部注入，
//     而真实协议早已通过 ywtb 与教务三跳链路实测确认并落地，占位已无意义。
//  3. **注释与代码曾长期不一致**（标着「当前为骨架」），留在仓库里只会误导后续接手者
//     以为「还有一条待补的第二取数路径」。
//
// 若将来真的需要第二条取数路径，应基于实测协议新写实现，而不是复活这个占位骨架。
