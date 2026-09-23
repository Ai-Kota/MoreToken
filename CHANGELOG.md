# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
