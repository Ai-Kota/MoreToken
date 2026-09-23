# Sprint N Planning

**Sprint 周期**：YYYY-MM-DD ~ YYYY-MM-DD
**Sprint 目标**：[简述本 Sprint 的目标]

## Sprint Backlog

### P0 - 必须完成

| 任务 | 估时 | 负责人 | 状态 |
|------|------|--------|:---:|
| 任务 1 | Xd | | ⬜ |
| 任务 2 | Xd | | ⬜ |

### P1 - 应该完成

| 任务 | 估时 | 负责人 | 状态 |
|------|------|--------|:---:|
| 任务 3 | Xd | | ⬜ |
| 任务 4 | Xd | | ⬜ |

### P2 - 可以推迟

| 任务 | 估时 | 负责人 | 状态 |
|------|------|--------|:---:|
| 任务 5 | Xd | | ⬜ |

## 依赖项

| 依赖 | 说明 | 状态 |
|------|------|:---:|
| | | |

## 风险

| 风险 | 应对措施 |
|------|---------|
| | |

## WU 拆分（必填）

> **重要**：每个 Sprint Task 必须拆分为 ≤5 文件的 WU（Work Unit），每个 WU 对应 1 个 commit。

### WU 列表

| WU ID | Sprint Task | 验收标准 | 预计文件数 | 状态 |
|--------|-------------|----------|:----------:|:---:|
| WU-N-01 | T-XXX | `go test ./...` 通过 | 3 | ⬜ |
| WU-N-02 | T-XXX | 截图验证 | 2 | ⬜ |
| WU-N-03 | T-XXX | API 契约测试 | 4 | ⬜ |

### WU 创建规则

1. **文件数约束**：≤5 文件（wu-guard hook 会阻塞 >5 文件的 commit）
2. **单一职责**：一个 WU 只做一件事（如"添加设置 Tab"）
3. **可验证**：必须有明确的 Acceptance Criteria（测试命令 / 截图 / 契约）
4. **可回滚**：一个 WU 对应一个 git commit，可独立回滚
5. **WU 模板**：使用 `templates/sprint/work-unit.md`

### WU 命名规范

- **ID 格式**：`WU-{Sprint}-{NN}`（如 `WU-2-03` 表示 Sprint 2 第 3 个 WU）
- **Commit 格式**：`type(scope): description (WU-{Sprint}-{NN})`
  - 示例：`feat(ui): add settings tab (WU-2-03)`

## 验收标准

- [ ] 
- [ ] 
