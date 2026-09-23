package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// AutoFile 自动纳新清单的文件名——与**手写的** config.json 同目录。
//
// 为什么不写回 config.json：手写的是**策略**（哪些 provider、哪些 key、哪些 tier、
// 偏好顺序），自动的是**库存**（上游此刻有什么）。混在一个文件里，
// 机器每次纳新都会重写一份入库文件，diff 全是噪音，人也就不会再读它了。
// 分开之后：策略文件保持人来写、可评审；库存文件是生成物，随时可重建。
const AutoFile = "models.auto.json"

// AutoInventory 自动纳新清单（生成物）。
type AutoInventory struct {
	GeneratedAt string                  `json:"generated_at"`
	Providers   map[string]AutoProvider `json:"providers"`
}

// AutoProvider 一个 provider 的纳新结果。
type AutoProvider struct {
	Verified []Model `json:"verified"`
	Rejected []struct {
		ID  string `json:"id"`
		Err string `json:"err"`
	} `json:"rejected,omitempty"`
}

// LoadAuto 读自动清单。
//
// 语义刻意分两档：
//   - **文件不存在** → (nil, nil)：老部署没有这个文件，必须退化成"与以前完全一致"，
//     不能因为少一个生成物就启动不了。
//   - **文件存在但读不了/解析不了** → 返回错误：生成物坏了是事实，静默忽略它
//     等于把"纳新从未生效"这件事藏起来（T-014 的坑：三层全挂一声不吭）。
func LoadAuto(dir string) (*AutoInventory, error) {
	path := filepath.Join(dir, AutoFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读自动纳新清单 %s: %w", path, err)
	}
	var inv AutoInventory
	if err := json.Unmarshal(raw, &inv); err != nil {
		return nil, fmt.Errorf("解析自动纳新清单 %s: %w", path, err)
	}
	return &inv, nil
}

// MergeAuto 把自动清单并进 config，返回追加的模型条数。
//
// 两条硬规矩：
//
//  1. **只追加**，永不改写、永不前插。config 里手写声明的顺序就是偏好顺序，
//     自动纳进来的模型排在所有手写模型之后 ⇒ 它只在前面都不匹配时才被选中。
//     否则一次纳新就可能把主力模型换掉——那是"自动的"不该有的权力。
//
//  2. **kinds 强制写成 general**，不采信清单文件里写的东西。
//     实测能证明的只有"它活着并能回一条响应"，证明不了"它会推理"；
//     把它当结构约束而不是惯例，是因为惯例会被下一次"顺手改一下文件"破掉。
//     要升格某个模型的 kinds，必须走人工实测 + 写进 config.json（手写区）。
func MergeAuto(cfg *Config, inv *AutoInventory) int {
	if cfg == nil || inv == nil {
		return 0
	}
	added := 0
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		ap, ok := inv.Providers[p.ID]
		if !ok {
			continue
		}
		have := make(map[string]bool, len(p.Models))
		for _, m := range p.Models {
			have[m.ID] = true
		}
		for _, m := range ap.Verified {
			if m.ID == "" || have[m.ID] {
				continue
			}
			p.Models = append(p.Models, Model{
				ID:    m.ID,
				Name:  m.ID,
				Kinds: []string{"general"}, // 硬写，不采信文件（见上）
			})
			have[m.ID] = true
			added++
		}
	}
	return added
}
