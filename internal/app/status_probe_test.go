package app

// #24：探针必须在依赖 Ping 失败时如实返回 false。
// 覆盖「运行中 Redis 挂掉」的场景——/status 每次请求现查，不会缓存旧状态。

import (
	"context"
	"errors"
	"testing"
)

func TestNewStatusProbe_ReflectsPingFailure(t *testing.T) {
	pgOK := true
	redisOK := true
	probe := newStatusProbe(
		func(context.Context) error {
			if !pgOK {
				return errors.New("pg down")
			}
			return nil
		},
		func(context.Context) error {
			if !redisOK {
				return errors.New("redis down")
			}
			return nil
		},
		func() string { return "closed" },
		"postgres",
	)

	if !probe.Postgres() || !probe.Redis() {
		t.Fatal("依赖正常时两个探针都应报可达")
	}
	// 模拟运行中 Redis 挂掉
	redisOK = false
	if probe.Redis() {
		t.Fatal("Redis Ping 失败后探针必须返回 false（/status 会据此报 down）")
	}
	if !probe.Postgres() {
		t.Fatal("Redis 挂掉不应影响 Postgres 探针")
	}
}
