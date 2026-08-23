# Tasks — Spec 0007 Expression Engine

- [x] T1 Value/Env：tagged union、显示转换、不可变大小写不敏感注册表（FR-E3）
- [x] T2 lexer + parser：M0 语法子集、类型化 ParseError 含位置、函数调用可解析（FR-E1）
- [x] T3 求值器：GitHub coercion/truthiness/member 语义、ContextUnavailableError（FR-E2）
- [x] T4 AC-M0 测试矩阵（运算符/context/拒绝/两阶段一致）
- [x] T5 V1：函数注册表（success/failure/cancelled/always/contains/startsWith/endsWith/
      format/join/toJSON/fromJSON）（FR-3.3）
- [x] T6 V1：hashFiles workspace capability 注入（FR-3.4）；executor 运行时接线随 `if:` 求值落地
- [x] T7 模板插值 API（`${{ }}` 展开为字符串）供 0006/0008 使用
