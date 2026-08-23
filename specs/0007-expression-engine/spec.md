# Spec 0007 — Expression Engine

- **Status**: proposed
- **Date**: 2026-08-23
- **Parent**: `specs/0001-platform-overview/spec.md`（accepted）
- **Covers**: FR-3.1、FR-3.4（P0）；FR-3.2/3.3 为本 spec 的 V1 切片任务
- **Depends on**: 无（纯逻辑，无内部依赖）；0006 保留的 `${{ }}` 原文由本引擎求值
- **Out of scope here**: 模板插值（`${{ }}` 展开 into 字符串，随 0008 executor）、
  V1 全量 contexts/functions（本 spec 任务化）、hashFiles 的 workspace capability

## 1. Problem

表达式引擎是控制面（scheduler-time）与 Executor（runtime）共用的唯一语义权威。M0 需要它的
最小可用核心：字面量、布尔/比较运算、`github`/`env` context 读取；同时必须从第一天就固定
两阶段共用、类型化错误与纯函数纪律——这些性质决定 V1 能否平滑扩展到全量 contexts/functions。

## 2. Requirements

### FR-E1 语法（M0 子集）

- FR-E1.1 `[P0]` 字面量：`null`、`true`、`false`、十进制数字（整数/小数）、单引号字符串
  （`''` 转义单引号）。
- FR-E1.2 `[P0]` 运算符：`!`、`==`、`!=`、`<`、`<=`、`>`、`>=`、`&&`、`||`、括号；
  优先级与结合性遵循 GitHub Actions 文档（`!` > 比较 > `&&` > `||`，均左结合）。
- FR-E1.3 `[P0]` context 访问：标识符开头（`github`/`env`），支持 `.` 属性链、`[expr]`
  下标（字符串下标于对象、数字下标于数组）、`['name']` 形式；context 名与属性键大小写不敏感。
- FR-E1.4 `[P0]` 不在 M0 子集内的语法（算术、函数调用、`*` filter、双引号字符串）产生**类型化
  ParseError，含位置**；函数调用允许解析但求值返回类型化「not supported in this build」（V1 落地）。

### FR-E2 求值语义

- FR-E2.1 `[P0]` 求值是纯函数：无时钟/随机/IO/网络；不依赖 K8s、数据库、provider SDK（FR-3.4）。
- FR-E2.2 `[P0]` 语义对齐 GitHub Actions：
  - 缺失属性/越界/负下标 → `null`；对标量取属性 → `null`。
  - truthiness：`false`、`null`、空串、`0`、`NaN` 为假。
  - 相等：同型直接比较（字符串大小写不敏感）；任一方为数字时按数字比较（字符串不可解析为
    数字则不相等）；`null` 与空串等价；布尔按 `true`/`false` 字符串参与字符串比较。
  - 大小比较：双方可转数字按数字，否则字符串（大小写不敏感）比较。
  - `&&`/`||` 返回操作数（非布尔化），`!` 返回布尔。
- FR-E2.3 `[P0]` 两阶段共用：同一 `Env` 结构在控制面与 Executor 传入不同 context 集合；
  访问未注册 context 返回类型化 `ContextUnavailableError`（列出可用 contexts），绝不静默 null。
- FR-E2.4 `[P0]` 求值错误（未知 context、对 null 索引数字、函数未支持等）一律返回错误，
  不得折算成 `false`。

### FR-E3 API 形态

- FR-E3.1 `[P0]` `Eval(expr string, env *Env) (Value, error)` 为唯一入口；Value 为带标签联合
  （Null/Bool/Number/String/Array/Object），可转换为 Go 值与显示字符串（数字无多余小数）。
- FR-E3.2 `[P0]` Env 构建为显式注册（`With("github", obj)`），注册名大小写不敏感；
  Env 不可变（With 返回新 Env）。

## 3. Acceptance criteria

**AC-M0**（自动化）：

1. 逐运算符表驱动矩阵（`==/!=/</<=/>/>=/&&/||/!`、括号、优先级、真值表）通过，含 GitHub
   特有 coercion 案例（`null == ''`、`1 == '1'`、字符串大小写不敏感、`0` 为假、`'abc' == true`
   为假）。
2. context 访问矩阵：嵌套属性、数字下标、字符串下标、大小写不敏感、缺失→null、越界→null。
3. 两阶段一致性：同一表达式在「仅 github」与「github+env」两个 Env 下结果一致；
   scheduler-time 访问 `env` 未注册 → ContextUnavailableError 且信息含可用 context 列表。
4. 语法拒绝矩阵：`a + b`、双引号字符串、`a.*.b`、空表达式、悬空运算符 → 类型化 ParseError 含位置。
5. 函数调用 `success()` → 类型化 not-supported 错误（解析通过）。

**AC-V1**：全量 contexts 与 functions 逐个用例（FR-3.2/3.3）、hashFiles capability 注入。

## 4. Design notes（非约束）

- 实现参考 act exprparser 的行为语义，但代码独立维护（架构文档既定决策）。
- lexer/parser/eval 分文件；AST 不导出。
