# specs

forgelet 采用 spec-driven 开发：先有被认可的 spec，再有实现。

```
specs/NNNN-<slug>/
├── spec.md     # WHAT & WHY：需求、不变量、验收标准（不写内部实现）
├── plan.md     # HOW：该 spec 的技术设计（spec 批准后编写）
└── tasks.md    # 实现清单，随实现更新状态
```

规则见 `AGENTS.md → Development workflow`。当前 specs：

| 编号 | 标题 | 状态 |
|------|------|------|
| [0001](0001-platform-overview/spec.md) | Platform Overview（总纲） | draft |
