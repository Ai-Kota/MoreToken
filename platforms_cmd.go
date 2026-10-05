package main

// platforms_cmd.go —— `-platforms` 平台发现子命令（T-037）。
//
// 与 `-check`（供给面断言）同级的**只读**子命令：不改 config、不接线、不启动服务。
// 它回答的是一个此前没人回答的问题——**我们还缺哪些平台**。
//
// 背景（T-037）：`catalog.Harvester` 只对 `cfg.Providers` 里**已声明**的 provider
// 发现新**模型**（`for i := range cfg.Providers`）——**永远不会发现一个新平台**。
// 于是池子的大小由人手写的一份 provider 列表定死，vault 里有多少平台都白搭。

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"moretoken/internal/config"
	"moretoken/internal/platform"
)

// runPlatforms 三向 diff：已知平台注册表 × vault 里有凭据的平台 × config 里已接线的平台。
func runPlatforms(cfg *config.Config, configPath string) int {
	regPath := filepath.Join(filepath.Dir(configPath), "platforms.known.json")
	reg, err := platform.Load(regPath)
	if err != nil {
		log.Printf("平台发现：%v", err)
		return 2
	}

	// 已接线：config 里全部 provider 的 base_url（按主机名归一——同上游按协议拆条不影响判定）。
	base := make([]string, 0, len(cfg.Providers))
	for i := range cfg.Providers {
		base = append(base, cfg.Providers[i].BaseURL)
	}

	// 有 key：vault 里 freellm/provider/ 下的平台。vault 不可用**不致命**——
	// 退化成"按无 key 处理"（结果偏保守：会更倾向于把平台报成"去注册"），并**明说**。
	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultVaultTimeout)
	defer cancel()
	vault, verr := platform.ListVaultPlatforms(ctx, "")
	if verr != nil {
		log.Printf("平台发现：读 vault 失败（将按「无 key」处理，结果偏保守）：%v", verr)
		vault = map[string]bool{}
	}

	rep := reg.Diff(vault, platform.WiredHosts(base))

	fmt.Printf("平台发现  %s\n", regPath)
	fmt.Printf("  注册表 %d 个平台 · 已接线 %d · 有 key 未接线 %d · 已知无 key %d\n",
		len(reg.Platforms), len(rep.Wired), len(rep.HaveKeys), len(rep.Missing))

	fmt.Println("\n✅ 已接线：")
	for _, e := range rep.Wired {
		fmt.Printf("   %-11s %s\n", e.Platform, e.Name)
	}

	fmt.Println("\n⚠️  有 key 未接线（vault 里有凭据，可跑能力测试后接入）：")
	for _, e := range rep.HaveKeys {
		fmt.Printf("   %-11s %-22s %s\n", e.Platform, e.Name, e.BaseURL)
	}

	fmt.Println("\n🆕 已知但无 key —— **去注册这些**：")
	for _, e := range rep.Missing {
		fmt.Printf("   %-11s %-22s %s\n", e.Platform, e.Name, e.Homepage)
	}
	return 0
}
