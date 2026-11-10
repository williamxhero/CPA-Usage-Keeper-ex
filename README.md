<img align="right" src="./ex-badge.svg" alt="EX fork" width="76" />

# CPA Usage Keeper-EX 主要能力

## 开始使用

Keeper 采集 CPA 用量，提供请求历史、实时视图、定价和成本分析。Go 服务位于 `cmd/server`，React 前端位于 `web`。

- 配置和接入步骤：[README-source.md](README-source.md)。
- 请求事件通过实时流取得首屏及新增记录，通过游标读取历史。
- 定价快照不一致时停止拼接不同基线的结果，并提供显式刷新入口。

```powershell
go test ./...
npm ci --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
go build -o cpa-usage-keeper.exe ./cmd/server
```

本项目的运行实例位于父项目的本机 Windows 栈。部署前保留数据与上一版本，并使用具备健康检查及失败回退的部署脚本。

## Fork 功能记录

本页只列相对上游 Usage Keeper 的主要功能。上游能保存 CPA 用量并展示基础统计，但当一个部署同时服务多个凭证、渠道和价格规则时，成本归属会失真；本 fork 把价格、身份和证据链补齐。

- **分层定价体系**：上游价格主要按模型或 Provider 解释，难以表达不同凭证的默认倍率、模型例外和固定资费。本 fork 支持凭证默认价、指定模型倍率、固定资费，并把规则写入请求和窗口的价格快照。对应 [issue #1](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/1)、[#3](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/3)、[#5](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/5)、[#6](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/6)、[#14](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/14)、[#17](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/17) 和 [#33](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/33)。
- **渠道与成员价格**：上游缺少面向团队或渠道的独立价格边界。本 fork 支持命名渠道、渠道成员、模型倍率和固定价格，让同一 CPA 的不同使用主体可以分别核算。对应 [issue #4](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/4)、[#7](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/7) 和 [#16](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/16)。
- **官方目录与凭证价格矩阵**：上游模型价格来源和凭证实际覆盖范围不够清晰。本 fork 引入官方模型价格目录与凭证模型价格矩阵，保留刷新、迁移和回滚证据，避免历史数据因价格更新而漂移。对应 [issue #18](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/18)、[#19](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/19)、[#20](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/20) 和 [#21](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/21)。
- **双成本估算与可解释差异**：上游通常只给出一套成本，凭证信息不完整时无法解释为什么对不上。本 fork 同时展示官方/有效规则两套估算，并说明凭证主体不足、渠道覆盖或价格缺失造成的差异。对应 [issue #8](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/8)。
- **主体同步与隐私标签**：上游凭证主体可能滞后，界面标签也容易暴露密钥片段。本 fork 自动同步凭证主体，只显示掩码后的密钥提示，并把定价规则绑定到可验证的身份迁移结果。对应 [issue #2](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/2)、[#9](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/9)、[#15](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/15) 和 [#31](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/31)。
- **持久化分析与实时视图**：上游的历史、实时和额度视图彼此割裂。本 fork 将价格矩阵接入请求明细、实时面板、窗口统计和排行榜，使一次请求在不同视图中保持同一套解释。对应 [issue #13](https://github.com/williamxhero/CPA-Usage-Keeper-ex/issues/13)。

本页只说明 EX fork 的增量；部署、配置和 CPA 接入要求仍以仓库正文为准。

> 完成 issue 归档更新时间：2026-11-09 08:34:40 SGT（UTC+08:00）。已列入上述条目的 issue 后续不再重复归档。
