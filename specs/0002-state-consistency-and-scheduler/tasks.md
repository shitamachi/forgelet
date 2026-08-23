# Tasks — Spec 0002 State Consistency and Scheduler

- [x] T1 `internal/run/model`：status 枚举、合法迁移、终态粘滞、run 聚合纯函数、记录类型、
      CR 名派生（FR-A、FR-B.2）；表驱动测试
- [x] T2 `internal/run/plan`：不可变 Plan、canonical digest（FR-F）；稳定性与无明文 secret 测试
- [x] T3 `internal/run/scheduler` ports 与 IDGen（FR-B.2、plan §3）
- [x] T4 Ingestor：delivery 去重 + create-or-get run（FR-B.1/B.3/B.4/B.5）；重放与 compile 失败测试
- [x] T5 内存 durable store：唯一键、原子 CreateRun、串行化领取、幂等 AckDispatched、单调
      ApplyObserved、GC ready 判定（FR-C、FR-D）
- [x] T6 Dispatcher/Projector/Collector/Canceler 用例（FR-C、FR-D、FR-G）
- [x] T7 四故障窗口 + 并发领取 + 重放收敛测试（AC-M0 1–4、6）
- [x] T8 PostgreSQL adapter：pgx 实现 DurableStore、唯一约束、事务、SKIP LOCKED 领取；
      integration test（跳过条件：无 FORGELET_TEST_POSTGRES）
- [x] T9 FR-E 内部 schedule：cron 注册、幂等键 `(repo, workflow, cron, fire time)`、
      missed fire/重叠/时区语义与测试（V1）；server 接线 + `--scheduled-repos`
