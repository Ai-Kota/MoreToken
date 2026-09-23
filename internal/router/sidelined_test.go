package router

import (
	"testing"

	"moretoken/internal/config"
)

// SelectSidelined 的契约：探活范围由**逐池的隔离状态**决定，与档位无关（T-015）。
//
// 旧口径是"停在付费档才探全部"，于是整体还在免费档、只有某个池被长禁（401/403）时，
// 那个池没有任何提前解除的途径，只能干等 1h——免费池随时间被慢慢啃掉。

func prov(id string, tier config.Tier) config.Provider {
	return config.Provider{
		ID: id, BaseURL: "http://x", Format: "anthropic", Tier: tier,
		Keys: []string{"sk-" + id}, Models: []config.Model{{ID: "m"}},
	}
}

// TestSelectSidelined_PicksBackedOffAndCooling —— 退避中的、有 key 冷却的，都要被挑出来。
func TestSelectSidelined_PicksBackedOffAndCooling(t *testing.T) {
	free := []config.Provider{prov("backed-off", "free"), prov("cooling", "free"), prov("healthy", "free")}
	hs := []PoolHealth{
		{Provider: "backed-off", BackedOff: true},
		{Provider: "cooling", Cooling: 2},
		{Provider: "healthy"}, // 无退避无冷却 → 不该被探（"在用就是健康"）
	}

	got := SelectSidelined(free, hs)

	if len(got) != 2 {
		t.Fatalf("挑出 %d 个，want 2：%+v", len(got), got)
	}
	ids := map[string]bool{got[0].ID: true, got[1].ID: true}
	if !ids["backed-off"] || !ids["cooling"] {
		t.Errorf("应挑出 backed-off 与 cooling，实际 %v", ids)
	}
	if ids["healthy"] {
		t.Error("健康的池不该被探（免费层'在用就是健康'，别烧免费额度）")
	}
}

// TestSelectSidelined_NoneSidelinedReturnsNil —— 一个都没被隔离 → nil（调用方据此跳过这轮探测）。
func TestSelectSidelined_NoneSidelinedReturnsNil(t *testing.T) {
	free := []config.Provider{prov("a", "free"), prov("b", "free")}
	hs := []PoolHealth{{Provider: "a"}, {Provider: "b"}}

	if got := SelectSidelined(free, hs); got != nil {
		t.Errorf("无隔离池时应返回 nil，实际 %+v", got)
	}
}

// TestSelectSidelined_PaidNeverProbed —— 只隔离了付费池 → 不探。
//
// 探活的目的是"给被隔离的池一条复活通道"，而免费池是主力；付费是兜底，
// 它挂了不该触发任何免费侧的探测动作。
func TestSelectSidelined_PaidNeverProbed(t *testing.T) {
	free := []config.Provider{prov("free-a", "free")}
	hs := []PoolHealth{
		{Provider: "free-a"},                              // 免费池健康
		{Provider: "paid-a", BackedOff: true, Cooling: 3}, // 付费池被隔离
	}

	if got := SelectSidelined(free, hs); got != nil {
		t.Errorf("只隔离了付费池时不该探免费池，实际 %+v", got)
	}
}

// TestSelectSidelined_CarriesKeys —— 返回项必须**带 key**。
//
// 这是防回归到"从 Router 内部那份 providers 取材"的坑：router.New 会把副本的
// `Keys` 置空（明文只留在 keyVal，见 New 里的注释），从 Router 里捞出来的 provider
// 没有凭据，拿去做探测只会换来一堆"无凭据"失败——而这会**静默**地把
// "探活解禁"这条通道废掉，症状离病灶极远。
func TestSelectSidelined_CarriesKeys(t *testing.T) {
	free := []config.Provider{prov("sidelined", "free")}
	hs := []PoolHealth{{Provider: "sidelined", BackedOff: true}}

	got := SelectSidelined(free, hs)

	if len(got) != 1 {
		t.Fatalf("挑出 %d 个，want 1", len(got))
	}
	if len(got[0].Keys) == 0 || got[0].Keys[0] == "" {
		t.Errorf("返回的 provider 丢了 key（%+v）——探测会变成'无凭据'，解禁通道静默失效", got[0])
	}
}
