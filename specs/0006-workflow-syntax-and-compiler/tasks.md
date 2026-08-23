# Tasks — Spec 0006 Workflow Syntax and Compiler

- [x] T1 `internal/workflow/syntax`：Diagnostic/Error、AST 类型、yaml.Node 白名单遍历
      （M0 子集 + 位置报错 + 原文保留 `${{ }}`）（FR-W1）
- [x] T2 `internal/workflow/compiler`：Compiled IR、语义校验、文档顺序保持（FR-W2.1/2.2）
- [x] T3 触发匹配：branches/branches-ignore glob + `!` 排除优先（FR-W2.3）
- [x] T4 `model.JobIntent` 桥接（FR-W2.4）
- [x] T5 AC-M0 测试矩阵（fixture + 精确行列断言 + import 纪律）
- [ ] T6 V1 切片：`needs` DAG + 环检测、`pull_request/workflow_dispatch/schedule` 触发（P1）
- [ ] T7 V1 切片：`matrix` 展开与稳定 ID、DisplayName 分离（FR-2.5）
- [ ] T8 表达式钩子接入 0007 evaluator（raw `${{ }}` → 求值）
