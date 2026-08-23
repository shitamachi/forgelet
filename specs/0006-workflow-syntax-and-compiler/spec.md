# Spec 0006 — Workflow Syntax and Compiler

- **Status**: proposed
- **Date**: 2026-08-23
- **Parent**: `specs/0001-platform-overview/spec.md`（accepted）
- **Covers**: FR-2.1、FR-2.2、FR-2.4（M0 子集 + 显式报错 + source location）
- **Depends on**: 0002（`Compiler` port / `model.JobIntent`）
- **Out of scope here**: 表达式求值（0007）、`needs/if/matrix/secrets/uses` 等 V1 子集与
  matrix 稳定 ID（FR-2.3/2.5，本 spec 后续切片）、Plan 构造与 digest 落库（0008）、
  workflow 文件发现/加载（server 装配）、actionlint 级语义校验

## 1. Problem

workflow 文件是用户输入中最自由的部分。管线必须是 YAML → syntax tree → IR → compiled
workflow → job instances，且 source syntax node 不得进入调度状态（FR-2.1）。M0 的关键行为是
**严格子集**：不在声明子集内的字段要带着文件、行、列显式报错，绝不静默忽略（FR-2.4）——这决定
用户对平台的信任边界。

## 2. Requirements

### FR-W1 语法层（internal/workflow/syntax）

- FR-W1.1 `[P0]` 以 YAML AST（yaml.v3 Node）解析，保留每个字段的 source location
  （文件、行、列）；重复 key 由 YAML 层报错并带位置。
- FR-W1.2 `[P0]` M0 声明子集：顶层 `name`、`on.push`（含 `branches`/`branches-ignore`
  过滤）、`jobs.<id>` 的 `name`、`runs-on`、`env`、`steps`；step 的 `name`、`run`、`env`。
- FR-W1.3 `[P0]` 子集外字段（如 `needs`、`if`、`matrix`、`uses`、`timeout-minutes`、
  `services`、`concurrency`、`on.pull_request` 等）产生 Diagnostic：文件/行/列/字段路径 +
  「not in the supported subset」；解析结果整体失败，不产生部分 AST。
- FR-W1.4 `[P0]` 类型错误（标量期望映射、字符串期望数组等）同样产生带位置的 Diagnostic。
- FR-W1.5 `[P0]` `${{ ... }}` 表达式占位符按原文保留，语法层不解释（求值属 0007）。

### FR-W2 编译层（internal/workflow/compiler）

- FR-W2.1 `[P0]` AST → IR → Compiled Workflow：job 保持文档顺序；M0 每个 job 恰好产生一个
  JobInstance（V1 matrix 展开在此层扩展）。
- FR-W2.2 `[P0]` 语义校验：至少一个 job；`runs-on`、`steps` 非空；step `run` 非空；
  job/step `env` 为 string→string。违反产生带 job/step 标识的错误。
- FR-W2.3 `[P0]` 触发匹配：`on.push` 无过滤 → 任意 push 匹配；有 `branches`/`branches-ignore`
  → 按 glob（`path.Match` 语义）对分支短名匹配，`!` 前缀取排除语义、排除优先；不匹配返回
  不触发（非错误）。
- FR-W2.4 `[P0]` 编译产物可转换为 `[]model.JobIntent`（jobKey=job id、runnerClass=runs-on），
  满足 0002 `Compiler` port 的输出契约；`planDigest` 由 0008 填充。

### FR-W3 管线纪律（FR-2.1）

- FR-W3.1 `[P0]` syntax 类型不得被 scheduler/controller/runtime 引用；编译输出只含 IR 与
  `run/model` 类型。
- FR-W3.2 `[P0]` workflow 模块不 import k8s.io、数据库、HTTP client、provider SDK（模块边界）。

## 3. Acceptance criteria

**AC-M0**（自动化）：

1. 合法 M0 fixture（≥2 jobs、多 step、job/step env）解析+编译出预期顺序的 JobInstance；
   env 含 `${{ }}` 时原文保留。
2. 未知字段（job 级 `needs`、step 级 `uses`、顶层 `on.schedule`）各自返回包含正确文件名与
   行列（>0、指向字段 key）的 Diagnostic，错误信息含字段路径。
3. 类型错误（`steps` 为字符串、`env` 值为整数）返回带位置 Diagnostic。
4. 空 jobs / 空 steps / 空 run / 空 runs-on → 语义错误，信息含 job/step 标识。
5. 触发匹配矩阵：无过滤全匹配；`main`/`releases/*`；`!` 排除优先；不匹配 → 不触发。
6. 编译输出可桥接 `model.JobIntent`；syntax 包不被 run/runtime 包 import（import 检查测试）。

**AC-V1**：needs DAG（环检测）、matrix 展开稳定 ID（FR-2.5）、官方 fixture 快照。

## 4. Design notes（非约束）

- 解析入口 `Parse(filename string, data []byte) (*Workflow, error)`；多文件加载与
  `.github/workflows` 发现属 server 装配。
- yaml.v3 的 `KnownFields` 无法给出未知字段的行列，故手工遍历 Node 树做白名单校验。
