# Plan — Spec 0007 Expression Engine

- **Status**: accepted
- **Date**: 2026-08-23
- **Accepted**: 2026-08-23, project owner
- **Spec**: `specs/0007-expression-engine/spec.md`（proposed）
- **Boundaries**: `docs/module-boundaries.md`（workflow 纯逻辑模块）

## 1. Package layout

```
internal/workflow/expression/
  value.go      Value（tagged union）、构造与显示转换
  env.go        Env（不可变 context 注册表，大小写不敏感）
  lexer.go      tokenizer（位置保留）
  parser.go     Pratt parser → 内部 AST
  eval.go       求值器 + GitHub coercion 语义
  errors.go     ParseError / EvalError / ContextUnavailableError / ErrFunctionNotSupported
```

无内部依赖；仅标准库。

## 2. 语法与 AST

tokens：ident、number、string（单引号）、`! && || == != < <= > >= . [ ] ( )`，
EOF。拒绝：`+ - * /`、双引号、`=`,等（类型化 ParseError：offset/line/column）。

AST（不导出）：literal / ident(context) / member{obj,prop} / unary{!,x} /
binary{op,l,r} / call{name,args}（可解析、求值期拒绝）。

优先级（低→高）：`||` < `&&` < 比较（== != < <= > >=，左结合）< `!`（前缀）< 后缀
（`.`/`[expr]`）< primary。括号重排。

## 3. 求值

- `Env`：`map[string]Value`（键小写）；`With` 复制返回新 Env。
- ident 求值：Env 命中→值；未命中→ContextUnavailableError{Want, Available}。
- member：obj 为 null → null；obj 为 Object → 键大小写不敏感查找（缺失→null）；
  obj 为 Array 且 prop 为 Number → 越界/负数→null；其余组合 → null（GitHub 语义）。
- coercion 与 truthiness 按 spec FR-E2.2；数字显示：整数值无小数点，其余 `g` 格式。

## 4. V1 扩展点（任务化，不在本切片）

- call 求值注册表（success/failure/…/hashFiles）；hashFiles 经 workspace capability
  接口注入（FR-3.4）。
- Env 增加 runner/matrix/needs/steps/inputs 等 context（由调用方注册，引擎不变）。

## 5. Testing strategy

- 表驱动：运算符全矩阵 + GitHub coercion 特例（AC 1）、context 访问矩阵（AC 2）、
  语法拒绝矩阵含位置断言（AC 4）、函数拒绝（AC 5）、两阶段一致性（AC 3）。
- 覆盖率 ≥ 85%（workflow 模块约定），目标 90%+。
