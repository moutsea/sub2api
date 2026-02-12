package antigravity

import "sync"

const signatureMinLength = 50

var signatureStore sync.Map // map[int64]string — keyed by account ID

// StoreThoughtSignature 缓存上游返回的有效 thought signature（按账号隔离）。
// 只接受长度 >= 50 的签名；更长的签名会替换已有的。
func StoreThoughtSignature(accountID int64, sig string) {
	if len(sig) < signatureMinLength {
		return
	}
	existing, loaded := signatureStore.Load(accountID)
	if loaded && len(sig) <= len(existing.(string)) {
		return
	}
	signatureStore.Store(accountID, sig)
}

// GetThoughtSignature 返回缓存的 thought signature（可能为空）。
func GetThoughtSignature(accountID int64) string {
	if v, ok := signatureStore.Load(accountID); ok {
		return v.(string)
	}
	return ""
}

// ClearThoughtSignature 清除指定账号缓存的 thought signature。
func ClearThoughtSignature(accountID int64) {
	signatureStore.Delete(accountID)
}
