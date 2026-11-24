package release_test

// 应用发布域的纯逻辑与编排验收（Spec #80 / T1、T2）。
//
// 只断言**外部可观察行为**：返回值、错误码、对象存储是否被写入。
// 不断言内部调用顺序或私有字段（除了用内存仓储的引用数量判断「有没有写」
// 这类契约级事实，那是本域对外的承诺：重复上传不得写对象存储）。

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"nongyu-backend-plus/internal/apperr"
	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/release"
	releasestore "nongyu-backend-plus/internal/release/store"
)

func newSvc(t *testing.T) (*release.Service, *releasestore.ReleaseMemoryRepo, *platform.MemoryObjectStore) {
	t.Helper()
	repo := releasestore.NewReleaseMemoryRepo(func() time.Time {
		return time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	})
	store := platform.NewMemoryObjectStore()
	svc := release.New(release.Options{Repo: repo, ObjectStore: store})
	return svc, repo, store
}

func validInput(code int64) release.UploadInput {
	return release.UploadInput{
		PackBytes:               []byte("PK\x03\x04fake-apk-bytes"),
		AppID:                   "com.nongyu.app",
		VersionCode:             code,
		VersionName:             "1.4.2",
		MinSupportedVersionCode: 100,
		Changelog:               "修了几个问题",
		CreatedBy:               "admin-1",
	}
}

// ---------- 纯逻辑：格式校验 ----------

func TestValidateApplicationID(t *testing.T) {
	ok := []string{"com.nongyu.app", "com.a.b", "cn.edu.sicau.app", "com.example_1.app"}
	for _, id := range ok {
		if err := release.ValidateApplicationID(id); err != nil {
			t.Fatalf("%q 应合法，却报错：%v", id, err)
		}
	}
	// 这些必须被拒：空、单段、路径分隔符、点号结尾、空白、以 .. 结尾。
	bad := []string{"", "  ", "nongyu", "com..app", "com/nongyu/app", "com.nongyu.app/",
		"com.nongyu.", "com nongyu app", "../etc/passwd", "com.nongyu.app.."}
	for _, id := range bad {
		if err := release.ValidateApplicationID(id); err == nil {
			t.Fatalf("%q 应被判非法，却通过了", id)
		}
	}
}

func TestValidateVersionCode(t *testing.T) {
	if err := release.ValidateVersionCode(1); err != nil {
		t.Fatalf("1 应合法：%v", err)
	}
	for _, c := range []int64{0, -1, -100} {
		if err := release.ValidateVersionCode(c); err == nil {
			t.Fatalf("versionCode=%d 应被判非法", c)
		}
	}
}

func TestValidateMinSupported(t *testing.T) {
	if err := release.ValidateMinSupported(0, 100); err != nil {
		t.Fatalf("0 下限应合法：%v", err)
	}
	if err := release.ValidateMinSupported(100, 100); err != nil {
		t.Fatalf("下限 == 版本码应合法：%v", err)
	}
	// 下限高于本次版本码 = 发布即被阻断的自相矛盾记录。
	if err := release.ValidateMinSupported(101, 100); err == nil {
		t.Fatal("下限高于 versionCode 应被拒")
	}
	if err := release.ValidateMinSupported(-1, 100); err == nil {
		t.Fatal("负下限应被拒")
	}
}

func TestObjectKeyShape(t *testing.T) {
	id, err := release.NewObjectID()
	if err != nil {
		t.Fatalf("NewObjectID: %v", err)
	}
	key := release.ObjectKey("com.nongyu.app", 10402, id)
	want := "releases/com.nongyu.app/10402/" + id + ".apk"
	if key != want {
		t.Fatalf("对象 key 形状不符：got %q want %q", key, want)
	}
	// 两次生成不得相同（随机后缀是「回滚只删自己那笔」的前提）。
	id2, _ := release.NewObjectID()
	if id == id2 {
		t.Fatal("两次 NewObjectID 不得相同")
	}
}

// ---------- 编排：上传登记 ----------

func TestUpload_Success(t *testing.T) {
	svc, _, store := newSvc(t)
	rec, err := svc.Upload(context.Background(), validInput(10402))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if rec.ID == 0 {
		t.Fatal("登记后应回填 ID")
	}
	if rec.SHA256 == "" || rec.Size == 0 {
		t.Fatalf("应记录 sha256 与大小，得到 sha256=%q size=%d", rec.SHA256, rec.Size)
	}
	if rec.ObjectKey == "" {
		t.Fatal("应记录对象 key")
	}
	// 对象存储里应真的有一个对象。
	info, err := store.Stat(context.Background(), rec.ObjectKey)
	if err != nil {
		t.Fatalf("对象存储里找不到刚写的包体：%v", err)
	}
	if info.Size != rec.Size {
		t.Fatalf("对象大小 %d 与记录 %d 不一致", info.Size, rec.Size)
	}
}

// 重复上传必须 409，且**不得**写对象存储——这是本域对外的承诺。
func TestUpload_DuplicateIsConflictAndDoesNotWriteObject(t *testing.T) {
	svc, _, store := newSvc(t)
	if _, err := svc.Upload(context.Background(), validInput(10402)); err != nil {
		t.Fatalf("首次上传：%v", err)
	}
	before := store.PutCount()

	_, err := svc.Upload(context.Background(), validInput(10402))
	if err == nil {
		t.Fatal("重复上传应失败")
	}
	if code, ok := apperr.CodeOf(err); !ok || code != apperr.CodeConflict {
		t.Fatalf("重复上传应是 conflict，得到 %v", err)
	}
	if got := store.PutCount(); got != before {
		t.Fatalf("重复上传不得写对象存储：写入次数从 %d 变成 %d", before, got)
	}
}

func TestUpload_ValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*release.UploadInput)
	}{
		{"空 applicationId", func(in *release.UploadInput) { in.AppID = "" }},
		{"非法 applicationId", func(in *release.UploadInput) { in.AppID = "not a package" }},
		{"versionCode 为 0", func(in *release.UploadInput) { in.VersionCode = 0 }},
		{"versionName 为空", func(in *release.UploadInput) { in.VersionName = "  " }},
		{"下限高于版本码", func(in *release.UploadInput) { in.MinSupportedVersionCode = 99999 }},
		{"包体为空", func(in *release.UploadInput) { in.PackBytes = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, store := newSvc(t)
			in := validInput(10402)
			tc.mut(&in)
			_, err := svc.Upload(context.Background(), in)
			if err == nil {
				t.Fatalf("%s 应被拒", tc.name)
			}
			if code, ok := apperr.CodeOf(err); !ok || code != apperr.CodeValidationFailed {
				t.Fatalf("%s 应是 validation_failed，得到 %v", tc.name, err)
			}
			if store.PutCount() != 0 {
				t.Fatalf("%s 校验失败时不得写对象存储", tc.name)
			}
		})
	}
}

// 对象存储未配置时必须给明确错误，不是零字节成功。
func TestUpload_NoObjectStore(t *testing.T) {
	repo := releasestore.NewReleaseMemoryRepo(nil)
	svc := release.New(release.Options{Repo: repo, ObjectStore: nil})
	_, err := svc.Upload(context.Background(), validInput(10402))
	if err == nil {
		t.Fatal("未配对象存储时上传应失败")
	}
}

// 对象写入失败时不登记元数据（不留半成品）。
func TestUpload_ObjectPutFailureLeavesNoRecord(t *testing.T) {
	repo := releasestore.NewReleaseMemoryRepo(nil)
	svc := release.New(release.Options{Repo: repo, ObjectStore: newFailingStore()})
	if _, err := svc.Upload(context.Background(), validInput(10402)); err == nil {
		t.Fatal("对象写入失败时上传应失败")
	}
	if _, err := repo.GetByRef(context.Background(), "com.nongyu.app", 10402); err == nil {
		t.Fatal("对象写入失败时不得留下元数据记录")
	}
}

// ---------- 编排：取最新 ----------

func TestLatest_PicksMaxVersionCode(t *testing.T) {
	svc, _, _ := newSvc(t)
	// 乱序上传：103 → 101 → 102。
	for _, c := range []int64{103, 101, 102} {
		in := validInput(c)
		in.MinSupportedVersionCode = 0
		if _, err := svc.Upload(context.Background(), in); err != nil {
			t.Fatalf("上传 %d：%v", c, err)
		}
	}
	got, err := svc.Latest(context.Background(), "com.nongyu.app")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got.VersionCode != 103 {
		t.Fatalf("最新应是 103，得到 %d", got.VersionCode)
	}
}

func TestLatest_NoRelease(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.Latest(context.Background(), "com.nongyu.app")
	if !errors.Is(err, release.ErrNoRelease) {
		t.Fatalf("无版本时应返回 ErrNoRelease，得到 %v", err)
	}
}

func TestLatest_InvalidAppID(t *testing.T) {
	svc, _, _ := newSvc(t)
	if _, err := svc.Latest(context.Background(), ""); err == nil {
		t.Fatal("空 applicationId 应被拒")
	}
}

// 不同应用互不干扰：A 的最新不得被 B 的更大版本码影响。
func TestLatest_IsolatedPerApplication(t *testing.T) {
	svc, _, _ := newSvc(t)
	a := validInput(100)
	a.MinSupportedVersionCode = 0
	if _, err := svc.Upload(context.Background(), a); err != nil {
		t.Fatalf("上传 A：%v", err)
	}
	b := validInput(999)
	b.AppID = "com.nongyu.merchant"
	b.MinSupportedVersionCode = 0
	if _, err := svc.Upload(context.Background(), b); err != nil {
		t.Fatalf("上传 B：%v", err)
	}
	got, err := svc.Latest(context.Background(), "com.nongyu.app")
	if err != nil {
		t.Fatalf("Latest A: %v", err)
	}
	if got.VersionCode != 100 {
		t.Fatalf("应用 A 的最新应是 100，得到 %d", got.VersionCode)
	}
}

// ---------- 编排：取指定版本与读包体 ----------

func TestGet_ByVersionCode(t *testing.T) {
	svc, _, _ := newSvc(t)
	payload := []byte("PK\x03\x04specific-version-bytes")
	in := validInput(101)
	in.PackBytes = payload
	in.MinSupportedVersionCode = 0
	if _, err := svc.Upload(context.Background(), in); err != nil {
		t.Fatalf("上传：%v", err)
	}
	rec, err := svc.Get(context.Background(), "com.nongyu.app", 101)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	rc, err := svc.OpenPack(context.Background(), rec)
	if err != nil {
		t.Fatalf("OpenPack: %v", err)
	}
	defer func() { _ = rc.Close() }()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		t.Fatalf("读取包体：%v", err)
	}
	if !bytes.Equal(buf.Bytes(), payload) {
		t.Fatal("读回的字节与上传的不一致")
	}
}

func TestGet_NotFound(t *testing.T) {
	svc, _, _ := newSvc(t)
	_, err := svc.Get(context.Background(), "com.nongyu.app", 12345)
	if !errors.Is(err, release.ErrReleaseNotFound) {
		t.Fatalf("不存在的版本应返回 ErrReleaseNotFound，得到 %v", err)
	}
}

// failingStore 是一个「Put 必失败」的对象存储，用于验证失败不留痕。
// 内嵌 MemoryObjectStore 是为了只覆盖 Put，其余方法保持真实行为。
type failingStore struct{ *platform.MemoryObjectStore }

func newFailingStore() failingStore {
	return failingStore{MemoryObjectStore: platform.NewMemoryObjectStore()}
}

func (failingStore) Put(context.Context, string, io.Reader, int64, string) (platform.ObjectInfo, error) {
	return platform.ObjectInfo{}, errors.New("对象存储写入失败（测试注入）")
}
