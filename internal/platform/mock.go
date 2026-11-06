package platform

import (
	"context"
	"sync"
)

// MockStore 是骨架期的内存实现：无外部依赖、零配置，可直接 go run。
// 数据库层就绪后，新增一个实现同一 Store 接口的类型并在 app 装配层替换即可，业务域代码不动。
type MockStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMockStore 构造一个空的内存存储。
func NewMockStore() *MockStore {
	return &MockStore{data: make(map[string][]byte)}
}

// Seed 预置一条数据，供骨架期联调使用。
func (m *MockStore) Seed(key string, value []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
}

// Get 实现 Store 接口。
func (m *MockStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[key]
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}
