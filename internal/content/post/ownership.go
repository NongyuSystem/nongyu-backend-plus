package post

import (
	"sync"

	"nongyu-backend-plus/internal/apperr"
)

// AttachmentOwnership 记录「对象 key → 上传者 userID」的归属。
//
// 存在意义（安全）：附件对象 key 是公开可读的（读者侧 GET /posts/:id 会下发
// Attachments 原始 key），且 key 本身不含任何归属信息。若不记录归属：
//   - 任何人都能把别人的对象 key 填进自己帖子的 attachments 里（写入时的
//     ValidateAttachments 只校验 "posts/" 前缀）；
//   - 再删掉自己这条帖子，cleanupAttachments 就会对那个 key 调 ObjectStore.Delete，
//     把别人的附件对象清掉 —— 即「删自己的帖子清除他人附件」越权破坏。
//
// 三道闸（本修复）：
//
//	① 上传成功后 Register(key, uploaderID)；
//	② Create/Update 保存 attachments 前用 ValidateOwned 校验「每个 key 已登记且
//	   属主 == 当前操作者」，不满足 forbidden；
//	③ cleanupAttachments 用 OwnedBy 只删属主匹配的 key；查不到归属一律不删
//	   （fail-closed），避免降级成 fail-open。
//
// 实现：进程内 map + RWMutex，与 Service/ObjectStore 同生命周期，由 Service.New
// 统一构造。这样所有现有 `post.New(Options{...})` 调用点无需改动即可获得归属能力。
//
// 已知边界（写下来，不假装没有）：
//  1. 进程内记录，重启即丢；重启后历史 key 因「查不到归属」被 fail-closed 拒绝
//     引用/删除，功能上表现为老附件不能再被新帖引用、删老帖不再清理其对象。
//     安全，但需运维知晓；生产要跨实例/跨重启应把该表落到 PG 或写进对象元数据
//     （Put 带 x-amz-meta-owner，Stat/Delete 读回）——那是同一接口的另一种实现，
//     不改业务域调用点。
//  2. 它不解决「同一用户把自己一个 key 挂到多条自己帖子」：删其中一条仍会清掉
//     对象，另一条帖子的图会失效。严格做法需引用计数（key→refcount）。本修复按
//     lead 给定的「校验归属」最小稳妥口径实施；这是同用户数据完整性问题，不是越权。
type AttachmentOwnership struct {
	mu     sync.RWMutex
	owners map[string]int64
}

// NewAttachmentOwnership 构造空的归属记录。
func NewAttachmentOwnership() *AttachmentOwnership {
	return &AttachmentOwnership{owners: make(map[string]int64)}
}

// Register 登记一个 key 的属主（上传成功后调用）。ownerID<=0 视为非法，拒登记。
func (o *AttachmentOwnership) Register(key string, ownerID int64) {
	if o == nil || key == "" || ownerID <= 0 {
		return
	}
	o.mu.Lock()
	o.owners[key] = ownerID
	o.mu.Unlock()
}

// OwnerOf 返回 key 的属主；未登记返回 (0,false)。
func (o *AttachmentOwnership) OwnerOf(key string) (int64, bool) {
	if o == nil {
		return 0, false
	}
	o.mu.RLock()
	id, ok := o.owners[key]
	o.mu.RUnlock()
	return id, ok
}

// ValidateOwned 校验「这批 key 是否都可由 actorID 引用」。
//
// 逐条要求：key 已登记（上传过）、且属主就是 actorID。任一不满足即 forbidden，
// 且**不区分「key 不存在」与「属主不符」**，避免用错误码差异探测他人对象 key。
// 空列表直接放行。
func (o *AttachmentOwnership) ValidateOwned(keys []string, actorID int64) error {
	if actorID <= 0 {
		return apperr.Errorf(apperr.CodeUnauthorized, "缺少已认证身份")
	}
	for _, k := range keys {
		owner, ok := o.OwnerOf(k)
		if !ok || owner != actorID {
			return apperr.Errorf(apperr.CodeForbidden, "附件不属于当前用户或不存在")
		}
	}
	return nil
}

// OwnedBy 判断一个 key 是否属于 actorID（清理时 fail-closed 用）。
// 未登记、属主不符、actorID 非法一律 false —— 查不到归属就绝不删。
func (o *AttachmentOwnership) OwnedBy(key string, actorID int64) bool {
	owner, ok := o.OwnerOf(key)
	return ok && actorID > 0 && owner == actorID
}
