# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed（行为变化，升级须知）
- **T-043：`agnes-3.0-flash` 剔出免费池（排除名单）**（2026-10-25）：config 新增
  `exclude_models`；`MergeAuto`、`ApplyAdmission`、`-harvest`（继承旧清单 + 实测纳新）
  三处共同跳过名单内模型，堵住"删一次清单、下一轮 harvest 又写回"的复发路径。
  生产配置写入 `"exclude_models": ["agnes-3.0-flash"]`。
  判据：部署后池内无该模型；`/decisions` 不再出现它承接 `auto:*` 流量。
- **T-048：修复观测档位卡在 `paid`**（2026-10-25）：`state.TickOnce` 在 `tier==paid`
  时继续探活（即便没有任何被隔离的池），使 `free-healthy>=T` 的回归分支可达。
  此前 NATS status 可能长期显示 `tier=paid`，而所有池实际健康（`backed_off=false,
  cooling=0`）——档位是观测值，不影响路由。
- **付费 key 并入免费池轮换**（用户定性 2026-10-12）：`agnes-paid` 付费 key 追加进
  `agnes`(openai) / `agnes-anthropic`(anthropic) 两个**免费** provider 的 `keys` 末尾
  （各 33→34 把，付费占 1 把 ≈ 3% 份额）；原 `agnes-paid-anthropic` /
  `agnes-paid-openai` 两个独立付费 provider **删除** ⇒ 池内**无 paid 档**
  （启动日志 `(5 providers: 5 free, 0 paid)`）。定性：
  *"付费兜底只是个伪命题，但付费档的算力要用到循环中去……只是不作为主要算力，
  因为 cc 在外面也要重度使用。"*
  ⚠️ **本变更取代下方 T-042 的"付费层退出虚拟模型兜底链"**（原条目保留存史）。
  ⚠️ 并入后 `allow_paid_fallback`（回滚闸）**不再适用**——它只对**独立 paid provider**
  有意义；如今付费 key 只是免费 provider 里的一把 key。日后若要回退到"付费独立档"，
  需重建 provider 块并把 key 从免费池摘出。
- **T-042 付费层退出虚拟模型兜底链**（用户定性：免费池自循环兜底）：默认起
  `auto:*` 不再 fallback 进付费 provider；免费全挂 = 503（reason 含 T-040 的
  `daily-quota banned` 标注）+ 等 24:00 日额度重置。显式配
  `"allow_paid_fallback": true` 可恢复"免费挂转付费"旧行为（回滚闸）。
  字面模型点名付费档不受影响（基建维护用途保留）。
- T-039 起 `admission_path` 生效后，`auto:reasoning` / `auto:coding` 只在**实测过
  `admission_min_ratio`（当前 0.8）线**的模型里挑；付费档 `agnes-3.0-flash`
  补了 reasoning kind（字面直连可用，虚拟模型默认不再消费它）。
- T-040 新增 429 日账本：某 key 当日 429 ≥ `rate_limit_daily_ban`（未配置=缺省 4；
  显式 0=回归闸关闭）⇒ 该 key 摘除至当日 24:00。

### Added
- Free-first deterministic state machine routing
- Dual protocol support (OpenAI + Anthropic)
- Virtual model routing (`auto:reasoning`, `auto:coding`, etc.)
- Multi-key rotation with account-level rate limit absorption
- 429/401/403 cooling and ban mechanisms
- `/health` endpoint for pool status
- `/decisions` endpoint for routing decision audit trail
- `/doctor` self-diagnosis endpoint
- Config validation via `-check` flag
- Model auto-harvest via `-harvest` flag
- NATS status publishing (graceful no-op when absent)
- Vault key resolution (graceful no-op when absent)
- English README
- Chinese README (`README.zh-CN.md`)
- Quick Start guide (`QUICKSTART.md`)
- Contributing guidelines (`CONTRIBUTING.md`)
- GitHub Actions CI workflow
- Example configuration (`config/config.example.json`)
- `.mailmap` for consistent author display

### Changed
- Renamed from `free-tier-aggregator` to `MoreToken`
- Module path: `moretoken`

### Fixed
- Deterministic free→paid→free regression (no more flapping)
- Request body limit enforcement (32 MiB, returns 413 instead of silent truncation)
- Format isolation between Anthropic and OpenAI paths

---

## [1.0.0] - 2026-09-23

### Initial Release

- First public release of MoreToken
- Core routing logic complete and tested
- 10 internal packages, all with tests passing
- 79 keys loaded and verified via vault pointers
- Both protocol paths (Anthropic + OpenAI) tested end-to-end

[Unreleased]: https://github.com/Ai-Kota/MoreToken/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/Ai-Kota/MoreToken/releases/tag/v1.0.0
