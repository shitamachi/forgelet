# specs

forgelet 采用 spec-driven 开发：先写清 WHAT/WHY 和可验证的 acceptance criteria，批准后再决定 HOW，
最后把实现拆成可跟踪任务。

```text
specs/NNNN-<slug>/
├── spec.md     # WHAT & WHY：需求、不变量、验收标准
├── plan.md     # HOW：spec accepted 后确定的技术设计
└── tasks.md    # 实现清单，随实现更新状态
```

## Lifecycle

| Status | 含义 | 允许的工作 |
|--------|------|------------|
| `draft` | 作者探索中，内容可大幅变化 | 调研、评审、修改 spec |
| `proposed` | 已准备接受正式评审 | 评审和原型验证；不得合并产品实现 |
| `accepted` | 项目 owner 明确批准，requirements 冻结 | 创建/批准 plan、拆 tasks、实现 |
| `implemented` | acceptance criteria 已满足且交付 | 维护、回归修复 |
| `superseded` | 已被新 spec 替代 | 只保留历史和替代链接 |
| `rejected` | 决定不实施 | 只保留决策记录 |

状态变更必须修改 `spec.md` 头部，并记录日期和批准者。AI agent 可以起草或建议状态变更，但只有
项目 owner 的明确指令可以把 spec 设为 `accepted`；CI 通过不等于批准。

`accepted` 后如果需求变化：

- 不改变顶层行为的小澄清可在同一 spec 中修改，并在 PR 中说明；
- 改变安全边界、兼容承诺、外部 API 或顶层行为时，新建 spec 或显式重新评审原 spec；
- 实现与 accepted spec 冲突时先修改 spec，不允许让文档追赶既成代码。

## Plan approval

`plan.md` 使用 `draft → accepted → complete` 状态。accepted spec 只授权编写 plan；涉及产品代码或
部署行为的实现必须等 plan 也被项目 owner/maintainer 明确设为 `accepted`。纯文档、调研和用于验证
可行性的 disposable prototype 不得被当作产品实现合并。

## Writing rules

- `spec.md` 不固定 package 名、SQL、Go interface 等实现细节，除非它们本身是被批准的外部约束。
- 每条 requirement 有稳定 ID、优先级和可自动验证的 acceptance criterion。
- `plan.md` 说明状态机、接口、存储、失败恢复、安全和测试策略，并记录状态、日期与批准者。
- `tasks.md` 中每项都可在一个小 PR 内完成，并关联 requirement/plan section。
- 子 spec 必须追溯到 Spec 0001；新的顶层行为先修改或扩展 Spec 0001。

## Current specs

| 编号 | 标题 | 状态 |
|------|------|------|
| [0001](0001-platform-overview/spec.md) | Platform Overview（总纲） | accepted |
