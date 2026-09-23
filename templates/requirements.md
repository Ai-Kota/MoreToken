# 需求文档

> 用结构化格式描述需求，供 `ai-guard test-designer` 从契约生成测试用例。
> 每条功能一行，以"功能："或"Feature:"开头，方便 `extractFeatures` 提取。

---

## 功能清单

- 功能：用户能注册账号
- 功能：用户能登录
- 功能：用户能修改密码

## 非功能需求

- 登录接口响应 < 200ms
- 密码存储必须哈希

## 边界与异常

- 空输入、超长输入、重复注册、密码错误次数限制

---

## 使用方式

```bash
# 生成测试用例（输出到 tests/generated/）
ai-guard test-designer docs/requirements.md

# 指定输出目录
ai-guard test-designer docs/requirements.md --output tests/from-req
```

## 说明

- `test-designer` 对每个功能生成 normal / edge / negative / boundary 四类用例
- 生成的用例写入 `tests/generated/test-suite.json` 与对应语言测试骨架
- 测试骨架是**占位实现**（`t.Skip`/`pass`），需按功能矩阵补全真实断言
