# 工作单元（Work Unit, WU）

> **定义**：一个 Sprint 内可独立验收的最小开发单元，对应 1 个 git commit。
> **原则**：单一职责、可回滚、可验证。

---

## 基本信息

| 字段 | 说明 | 示例 |
|------|------|------|
| **WU ID** | `WU-{Sprint}-{NN}`，NN 两位数 | `WU-2-03` |
| **任务 ID** | 引用的 Sprint Task ID | `T-108` |
| **Sprint** | 所属 Sprint | `Sprint 2` |
| **创建日期** | YYYY-MM-DD | `2026-08-25` |
| **负责人** | AI 或人类 | `AI` |

---

## 验收标准（Acceptance Criteria）

> **必须**是可机读验证的硬条件（测试命令 / 截图 / 文件存在性）。
> 此表即该 WU 的**完成定义（DoD）**：
> - `ai-guard begin-task T-XXX` 将其加载为任务验收标准
> - `ai-guard verify-task T-XXX` 逐条核销；未全部核销则**阻塞完成**（exit 2）

| 类型 | 说明 | 示例 |
|------|------|------|
| **测试通过** | `go test` / `npm test` / `pytest` 命令 | `go test ./internal/api -run TestConfigHotReload` |
| **视觉验证** | 截图或 UI 状态描述 | `docs/screenshots/wu-2-03-settings-save.png` |
| **文件存在** | 文件路径 + 关键内容 | `internal/api/web/index.html 存在 "<title>Settings</title>"` |
| **契约验证** | API 响应匹配 schema | `GET /api/v1/config 返回 200 且 content-type=application/json` |

---

## 变更范围

| 字段 | 说明 | 约束 |
|------|------|------|
| **预计修改文件数** | 变更涉及的文件数 | ≤ 5 文件（hook 阻塞 >5） |
| **预计 LOC 变更** | 代码行数增删 | ≤ 200 LOC（建议） |
| **文件列表** | 具体路径 | 相对项目根目录 |

---

## 依赖与风险

| 字段 | 说明 |
|------|------|
| **依赖 WU** | 本 WU 依赖哪些 WU 先完成 | `WU-2-01, WU-2-02` |
| **阻塞 WU** | 哪些 WU 依赖本 WU | `WU-2-04` |
| **风险** | 可能的问题 | `Edge TTS 连接不稳定` |
| **缓解措施** | 如何应对风险 | `重试机制 + 超时 30s` |

---

## 实施记录

| 日期 | 操作 | 结果 |
|------|------|------|
| 2026-08-25 | 创建 WU | ✅ |
| 2026-08-25 | 实现代码 | 🔄 |
| 2026-08-25 | 验收通过 | ⬜ |

---

## 模板使用说明

1. **创建**：复制本模板到 `docs/sprints/sprint-{N}/wu-{NN}.md`
2. **填写**：按表格填写基本信息、验收标准、变更范围
3. **提交**：commit message 必须包含 `WU-{Sprint}-{NN}` 标记（如 `feat(ui): add settings tab (WU-2-03)`）
4. **验收**：运行 Acceptance 字段的验证命令，通过后标记 ✅

> **注意**：
> - 一个 WU 对应一个 git commit
> - 一个 commit 只能包含一个 WU
> - WU 验收通过后才能开始下一个 WU