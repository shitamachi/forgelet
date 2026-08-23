# Tasks — Spec 0008 Executor Runtime

- [x] T1 `mask`：SecretMasker + slog Handler（写出前 `***`）
- [x] T2 `filecommand`：GITHUB_ENV/OUTPUT（KV+heredoc）、GITHUB_PATH 解析（纯函数）
- [x] T3 `command`：`::name params::message` 解析（含 add-mask）
- [x] T4 `engine`：step 执行、共享 ENV/PATH、进程组取消/超时、失败即止、ReportJob
- [x] T5 `httpclient`：ControlPlane HTTP adapter（Bearer、类型化错误）
- [x] T6 `cmd/executor`：PID 1 入口（token 文件、SIGTERM→cancel、退出码）
- [x] T7 AC-M0 测试矩阵（AC 1–6）
- [ ] T8 V1：GITHUB_STATE、GITHUB_STEP_SUMMARY、命令参数 properties、continue-on-error
- [ ] T9 V1：JS/Composite/Builtin action（builtin 与 0009 统一设计）
- [x] T10 V1：日志流式上报控制面 / Loki（与 0010 联动）
