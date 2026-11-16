package skin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/platform"
	"nongyu-backend-plus/internal/platform/pgstore"
	"nongyu-backend-plus/internal/skin"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// newTestService 造一个不起 PG / 不起 MinIO 的 Service。
func newTestService(t *testing.T) (*skin.Service, *platform.MemoryObjectStore) {
	t.Helper()
	store := platform.NewMemoryObjectStore()
	svc := skin.New(skin.Options{
		Repo:        pgstore.NewSkinMemoryRepo(func() time.Time { return time.Unix(1700000000, 0).UTC() }),
		ObjectStore: store,
		Clock:       fixedClock{t: time.Unix(1700000000, 0).UTC()},
	})
	return svc, store
}

func multipartUpload(t *testing.T, version, activate string, pack []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("version", version)
	if activate != "" {
		_ = mw.WriteField("activate", activate)
	}
	fw, err := mw.CreateFormFile("file", "skin.zip")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(pack)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/admin/skins", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func newRouter(t *testing.T, svc *skin.Service) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	skin.RegisterPublic(r, svc)
	skin.RegisterAdmin(r, svc, func(*gin.Context) (string, bool) { return "admin-1", true })
	return r
}

func serve(t *testing.T, r *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ---------- Service 层 ----------

func TestService_UploadThenCatalogEmpty(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	// 还没上传/激活：目录应如实回 hasPack=false（客户端回落内置默认）
	cat, err := svc.Catalog(ctx, skin.CatalogBasePath)
	if err != nil {
		t.Fatal(err)
	}
	if cat.HasPack {
		t.Fatal("无当前指针时 hasPack 必须为 false")
	}
	if cat.DownloadURL != "" || cat.PackID != "" {
		t.Fatal("无包时不得编造 URL / packId")
	}
}

func TestService_UploadActivateCatalogDownload(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	raw := validPack(t)

	p, err := svc.Upload(ctx, skin.UploadInput{
		PackBytes: raw, Version: "1.0.0", CreatedBy: "admin-1", Activate: true,
	})
	if err != nil {
		t.Fatalf("上传失败：%v", err)
	}
	if p.PackID != "default-green" || p.Version != "1.0.0" {
		t.Fatalf("pack 元数据不符：%+v", p)
	}
	if store.PutCount() != 1 {
		t.Fatalf("应写入 1 个对象，实际 %d", store.PutCount())
	}
	// 对象确实可按 Upload 返回的 ObjectKey 取出，且字节与上传一致
	rc, err := store.Get(ctx, p.ObjectKey)
	if err != nil {
		t.Fatalf("对象取不到：%v", err)
	}
	got, _ := readAll(rc)
	if !bytes.Equal(got, raw) {
		t.Fatal("对象存储里的字节与上传不一致")
	}

	// 目录接口：hasPack + downloadUrl + manifest 原文
	cat, err := svc.Catalog(ctx, skin.CatalogBasePath)
	if err != nil {
		t.Fatal(err)
	}
	if !cat.HasPack || cat.PackID != "default-green" || cat.Version != "1.0.0" {
		t.Fatalf("目录不符：%+v", cat)
	}
	if cat.DownloadURL != "/skins/default-green/1.0.0/pack" {
		t.Fatalf("downloadUrl 不符：%q", cat.DownloadURL)
	}
	if !json.Valid(cat.Manifest) {
		t.Fatal("manifest 应是合法 JSON 原文")
	}
	if cat.PackSHA256 != skin.Sha256Hex(raw) {
		t.Fatal("目录里的 packSha256 不符")
	}

	// Download 流式取回
	meta, drc, err := svc.Download(ctx, "default-green", "1.0.0")
	if err != nil {
		t.Fatalf("Download 失败：%v", err)
	}
	defer drc.Close()
	if meta.PackSize != int64(len(raw)) {
		t.Fatalf("meta.PackSize 不符")
	}
	body, _ := readAll(drc)
	if !bytes.Equal(body, raw) {
		t.Fatal("下载字节与上传不一致")
	}
}

func TestService_UploadInvalidPackDoesNotWriteStore(t *testing.T) {
	svc, store := newTestService(t)
	_, err := svc.Upload(context.Background(), skin.UploadInput{
		PackBytes: []byte("not a zip"), Version: "1.0.0",
	})
	assertCode(t, err, "validation_failed")
	if store.PutCount() != 0 {
		t.Fatal("校验失败的包绝不许写进对象存储")
	}
}

func TestService_UploadBadVersionRejected(t *testing.T) {
	svc, _ := newTestService(t)
	for _, v := range []string{"", "../evil", "a/b", "1 0"} {
		_, err := svc.Upload(context.Background(), skin.UploadInput{
			PackBytes: validPack(t), Version: v,
		})
		assertCode(t, err, "validation_failed")
	}
}

func TestService_DuplicateVersionIsConflict(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	first, err := svc.Upload(ctx, skin.UploadInput{PackBytes: validPack(t), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	// 同一 (packId, version) 再传一次：不可变，返回 conflict
	_, err = svc.Upload(ctx, skin.UploadInput{PackBytes: validPack(t), Version: "1.0.0"})
	assertCode(t, err, "conflict")

	// 关键回归：重复上传**不得**写对象、更不得删既有包体。
	// 早先实现会 Put 再回滚删除同 key，把线上已生效那版的包体删掉。
	if store.PutCount() != 1 {
		t.Fatalf("重复上传不应再写对象，PutCount=%d", store.PutCount())
	}
	if store.DeleteCount() != 0 {
		t.Fatalf("重复上传不应删除任何对象（否则会误删既有包体），DeleteCount=%d", store.DeleteCount())
	}
	// 既有包体仍然可取且内容正确
	rc, err := store.Get(ctx, first.ObjectKey)
	if err != nil {
		t.Fatalf("既有包体被误删：%v", err)
	}
	defer rc.Close()
	if !bytes.Equal(mustReadAll(t, rc), validPack(t)) {
		t.Fatal("既有包体内容被破坏")
	}
}

func mustReadAll(t *testing.T, rc interface{ Read([]byte) (int, error) }) []byte {
	t.Helper()
	b, err := readAll(rc)
	if err != nil {
		t.Fatalf("读流失败：%v", err)
	}
	return b
}

func TestService_ListAndDetail(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.Upload(ctx, skin.UploadInput{PackBytes: validPack(t), Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Version != "1.0.0" {
		t.Fatalf("List 不符：%+v", list)
	}
	d, err := svc.Detail(ctx, "default-green", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(d.Manifest) {
		t.Fatal("详情里的 manifest 应可直接 parseJson")
	}
	_, err = svc.Detail(ctx, "default-green", "9.9.9")
	assertCode(t, err, "not_found")
}

func TestService_ActivateRollbackAndNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	// 未激活前目录为空
	cat, _ := svc.Catalog(ctx, skin.CatalogBasePath)
	if cat.HasPack {
		t.Fatal("未激活前应无当前包")
	}
	if _, err := svc.Upload(ctx, skin.UploadInput{PackBytes: validPack(t), Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	// 激活不存在的版本 → 404
	assertCode(t, svc.Activate(ctx, "default-green", "9.9.9"), "not_found")
	// 激活 v1
	if err := svc.Activate(ctx, "default-green", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	cat, _ = svc.Catalog(ctx, skin.CatalogBasePath)
	if !cat.HasPack || cat.Version != "1.0.0" {
		t.Fatal("激活后目录应指向 v1")
	}
	// 上传 v2 并激活，再把指针回滚到 v1（ADR-0003 回滚 = 指针指回上一版）
	if _, err := svc.Upload(ctx, skin.UploadInput{PackBytes: validPack(t), Version: "2.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Activate(ctx, "default-green", "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Activate(ctx, "default-green", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	cat, _ = svc.Catalog(ctx, skin.CatalogBasePath)
	if cat.Version != "1.0.0" {
		t.Fatalf("回滚后应指向 v1，实际 %s", cat.Version)
	}
}

func TestService_DownloadMissingMaps404(t *testing.T) {
	svc, _ := newTestService(t)
	_, _, err := svc.Download(context.Background(), "nope", "1.0.0")
	assertCode(t, err, "not_found")
	_, _, err = svc.Download(context.Background(), "../x", "1.0.0")
	assertCode(t, err, "validation_failed")
}

func TestService_ObjectMissingButMetaExistsMaps404(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	p, err := svc.Upload(ctx, skin.UploadInput{PackBytes: validPack(t), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	// 人为把对象从存储里删掉，模拟「库里有、MinIO 里没了」
	if err := store.Delete(ctx, p.ObjectKey); err != nil {
		t.Fatal(err)
	}
	_, _, err = svc.Download(ctx, "default-green", "1.0.0")
	assertCode(t, err, "not_found")
}

// ---------- HTTP handler 端到端 ----------

func TestHandler_UploadCurrentDownloadActivate(t *testing.T) {
	svc, _ := newTestService(t)
	r := newRouter(t, svc)
	raw := validPack(t)

	// 上传（含 activate）
	w := serve(t, r, multipartUpload(t, "1.0.0", "true", raw))
	if w.Code != http.StatusCreated {
		t.Fatalf("上传应 201，实际 %d body=%s", w.Code, w.Body.String())
	}

	// 目录接口
	w = serve(t, r, httptest.NewRequest(http.MethodGet, "/skins/current", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("目录应 200，实际 %d", w.Code)
	}
	var cat map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &cat); err != nil {
		t.Fatal(err)
	}
	if cat["hasPack"] != true {
		t.Fatalf("目录 hasPack 应为 true：%v", cat)
	}
	if cat["downloadUrl"] != "/skins/default-green/1.0.0/pack" {
		t.Fatalf("downloadUrl 不符：%v", cat["downloadUrl"])
	}
	// manifest 原文可解析（客户端 parseJson 的输入）
	if _, ok := cat["manifest"].(map[string]any); !ok {
		t.Fatal("目录应内嵌 manifest 对象")
	}

	// 包体下载
	w = serve(t, r, httptest.NewRequest(http.MethodGet, "/skins/default-green/1.0.0/pack", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("下载应 200，实际 %d", w.Code)
	}
	if w.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("Content-Type 不符：%q", w.Header().Get("Content-Type"))
	}
	if w.Header().Get("X-Nongyu-Pack-Sha256") != skin.Sha256Hex(raw) {
		t.Fatal("响应头应带整包 sha256")
	}
	if !bytes.Equal(w.Body.Bytes(), raw) {
		t.Fatal("下载字节与上传不一致")
	}

	// 详情
	w = serve(t, r, httptest.NewRequest(http.MethodGet, "/skins/default-green/1.0.0", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("详情应 200，实际 %d", w.Code)
	}

	// 列表
	w = serve(t, r, httptest.NewRequest(http.MethodGet, "/admin/skins", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d", w.Code)
	}

	// 激活另一版本（先传 v2）
	w = serve(t, r, multipartUpload(t, "2.0.0", "", raw))
	if w.Code != http.StatusCreated {
		t.Fatalf("v2 上传应 201，实际 %d body=%s", w.Code, w.Body.String())
	}
	w = serve(t, r, httptest.NewRequest(http.MethodPost, "/admin/skins/default-green/2.0.0/activate", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("激活应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	w = serve(t, r, httptest.NewRequest(http.MethodGet, "/skins/current", nil))
	_ = json.Unmarshal(w.Body.Bytes(), &cat)
	if cat["version"] != "2.0.0" {
		t.Fatalf("激活后目录应指向 v2，实际 %v", cat["version"])
	}
}

func TestHandler_UploadInvalidReturns400(t *testing.T) {
	svc, _ := newTestService(t)
	r := newRouter(t, svc)
	w := serve(t, r, multipartUpload(t, "1.0.0", "", []byte("not a zip")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法包应 400，实际 %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_UploadMissingFileReturns400(t *testing.T) {
	svc, _ := newTestService(t)
	r := newRouter(t, svc)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("version", "1.0.0")
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/admin/skins", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := serve(t, r, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 file 应 400，实际 %d", w.Code)
	}
}

func TestHandler_DownloadMissingReturns404(t *testing.T) {
	svc, _ := newTestService(t)
	r := newRouter(t, svc)
	w := serve(t, r, httptest.NewRequest(http.MethodGet, "/skins/nope/1.0.0/pack", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在的包下载应 404，实际 %d", w.Code)
	}
}

func TestHandler_MethodIsolation(t *testing.T) {
	// 公开侧的写操作不应被注册（装配层靠 publicReadablePaths 只放行 GET/HEAD）
	svc, _ := newTestService(t)
	r := newRouter(t, svc)
	w := serve(t, r, httptest.NewRequest(http.MethodPost, "/skins/current", nil))
	if w.Code == http.StatusOK {
		t.Fatal("POST /skins/current 不应存在")
	}
}

func readAll(rc interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(rc)
	return buf.Bytes(), err
}
