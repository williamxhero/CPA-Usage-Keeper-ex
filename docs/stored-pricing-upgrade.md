# 显式升级 legacy stored pricing

这是合并兼容入口：保留上游旧库的备份、固定价格、可恢复回填和验证能力，同时保持 fork 的普通启动不重放历史费用。它不是新的默认定价模式，也不是部署步骤。

## 普通启动与显式选择

- 普通 `Initialize`、`RunContext` 和不带参数的 server **不会回填 legacy 历史费用**。读取当前价格的接口继续使用 current 定价；历史 NULL 不会被解释为免费。
- `--upgrade-stored-pricing` 是单次进程调用参数，默认 false，不保存到配置。它选择真实的 `RunStoredPricingUpgradeContext`，而不是改写 current API。
- 嵌入式调用可选 `InitializeStoredPricingUpgrade(ctx)`；需要 HTTP 启动状态外壳时使用 `RunStoredPricingUpgradeContext(ctx)`。必须在该 App 尚未初始化时选择，不能把运行中的实例切换为升级模式。
- 显式 server 在升级期间保留启动状态外壳和 durable inbox；业务路由在数据完成验证之前不就绪。失败时外壳保持 `failed`，不能把 `/healthz` 返回成功当作迁移完成。应检查 `/api/v1/startup/status` 的 `phase=ready`。

## 允许升级的输入

必须使用**尚未升级、未经普通启动改造的 legacy 文件副本**，或具有可验证升级前保护证据的中断现场。先另行保存文件和运行配置；不要让第二个进程同时写同一个工作目录。

显式操作复用原升级链：M1 静止备份及指纹、结构变化前固定的价格和水位、M4 明细分页、M5 聚合重建及恢复游标、M6 数据完成验证。恢复时沿用已有受保护备份，不把中途快照伪装成旧版备份。新库和已完成库不重新执行 legacy 回填。

**普通启动后的数据库不一定仍是合法升级输入。** 若已经存在 stored 结构，却缺少结构变化前固定的 baseline/cursors，显式操作会在创建 M1 备份前拒绝。不能删除 migration 标记、伪造 baseline、清除状态或放宽 provenance 检查来强行升级；应回到可信的升级前副本。此前计划中“普通启动后可直接显式升级同一文件”的假设已被真实测试否定，不是受支持流程。

## 仅隔离副本的调用示例

在已经配置好、与现网完全隔离的副本工作目录内，使用编译出的 server 可执行文件：

```text
cpa-usage-keeper.exe --env <isolated-fixture.env> --host 127.0.0.1 --upgrade-stored-pricing
```

示例配置中的 `WORK_DIR` 必须指向隔离目录；SQLite 文件为其中的 `app.db`，备份位于其 `backups` 子目录。`SQLITE_PATH` 不是有效的路径覆盖方式。测试将 CPA、Redis 指向本地不可用端口，禁用周期备份和文件日志，不读取用户服务、配置、数据库或凭据。不得将测试的 `AUTH_ENABLED=false` 用于真实部署。

该参数会启动 server，并非执行后自动退出的单次命令；完成后按正常生命周期停止。下一次普通启动无需也不会自动开启该参数。已完成的历史费用、备份与游标在普通/显式重启中保持不变。

## 失败与恢复

- 保留原错误、状态、游标和备份证据，先解决失败原因，再显式重试同一受保护现场；不要绕过未完成状态。
- 只有原备份验证通过时，日志才可给出旧版恢复指引。拒绝无基线的 current-schema 文件时，不生成备份、不打印可退旧版的误导指引。
- 不得直接拿已迁移的数据库启动旧版程序。实际恢复还需确认备份身份、指纹、版本、配置与写入停止条件。
- 本任务仅对临时 SQLite 文件、真实本地 HTTP 和子进程验收；没有执行线上升级、部署、重启或线上数据恢复。

## 与其他维护命令的区别

这不是 SPEC-003 的 `pricing-migrate` pricing-only adoption 工具，也不是任意区间重算功能。不要混用它们的输入条件或恢复流程。此入口只用于保留双方合并时已存在的 legacy 升级能力；不能作为扩展维护功能的理由。

## 验收入口

- `internal/app/test/stored_pricing_upgrade_test.go`：普通启动不回填、无基线结构拒绝、完成后两种重启均不重算、备份指纹不变、TLS 失败前不打开数据库。
- 原 startup/failure/evidence/compatibility 测试：在显式生产入口保留全部原失败场景、回滚日志、M4/M5 恢复、HTTP 状态、版本提示和金额断言。
- `cmd/server/test/cli_test.go` 的 `TestCLI/stored_pricing_upgrade_is_explicit`：构建并运行真实二进制，等待公开启动阶段；验证默认不升级、显式生成 100 tokens 对应 0.0002 的持久费用及备份/基线/水位、无保护结构被拒绝。此局部验收不代表 Keeper 全库或六库集成已经完成。
