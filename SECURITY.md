# 安全披露政策

ACR 处在 v0.1 alpha 阶段，接口与线格式仍会变化。安全修复只针对 `main` 分支最新代码。

## 报告漏洞

请不要在公开 Issue 中披露漏洞。请通过代码托管平台的私密漏洞报告功能（例如 GitHub 的 "Report a vulnerability"）提交，包含：

- 受影响的版本或提交；
- 宿主 Agent 及版本（Claude Code / Codex CLI）与运行模式（Native / Guarded）；
- 复现步骤和最小示例；
- 你观察到的影响，例如绕过契约、篡改事件链、路径逃逸、命令注入或敏感信息泄露。

维护者会在 7 天内确认收到，并在修复发布后公开说明。

## 保证范围

ACR 的保证等级见 `docs/02-architecture.md` 第 6 节。Native 模式（L1）依赖宿主 Hook，宿主失败开放的情况不属于 ACR 漏洞，但 ACR 未如实报告降级属于漏洞。Guarded 模式只对 `docs/capability-*.md` 中认证过的宿主版本作出承诺。
