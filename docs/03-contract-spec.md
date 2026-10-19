# ACR 可执行契约规范（v0.1 设计稿）

## 1. 契约生命周期

```text
draft -> validated -> approved -> active -> suspended -> retired
```

- `draft`：人工或模型生成，绝不执行。
- `validated`：Schema、表达式和示例仿真通过。
- `approved`：由明确身份批准并形成内容摘要。
- `active`：进入运行时评估。
- `suspended`：临时停止，保留历史。
- `retired`：不再使用，不删除历史版本。

任何内容修改都会产生新版本并重新审批，不允许原地修改已激活版本。

当前实现（v0.1 alpha）：这里的"审批"由事件链上的激活记录承担。人在终端执行 `acr contract activate`，或用户在对话里让 Agent 调用 `add_rule`（附用户原话，ACR 核对后记录为证据），都会写入 `contract.activated`；运行时只执行激活记录中的契约。契约的 `authorization` 字段允许用户在当轮对话里临时放行，`paths` 字段按项目相对路径匹配，使一条契约同时约束各个 Agent。详见 [让 ACR 真正干活](11-practical-runtime.md) 第 11 节。

## 2. 契约对象

```yaml
apiVersion: acr.dev/v1alpha1
kind: Contract
metadata:
  id: protect-production-users
  version: 1
  title: 禁止直接修改生产用户表
  status: draft
  severity: critical
  priority: 900
  tags: [database, production]
  provenance:
    source: human
    source_event_ids: []
scope:
  project_ids: [01JPROJECTEXAMPLE0000000000]
  workspace_globs: []
  agent_kinds: [claude-code, codex]
  tool_names: [Bash, mcp__database__execute]
applicability:
  expression: >-
    action.kind == "database.write" &&
    action.environment == "production" &&
    "prod_user" in action.resources
behavior:
  instruction: 不得直接修改生产环境 prod_user 表
  enforcement: hard
  non_overridable: true
correction:
  mode: ask
  strategies:
    - id: redirect-to-staging
      when: has(action.environment) && project.staging_enabled
      transform:
        type: set_fields
        values:
          environment: staging
      requires_confirmation: true
verifiers:
  pre:
    - type: expression
      expression: action.environment != "production"
  post: []
failure_mode: closed
examples:
  allow: []
  rewrite: []
  deny: []
```

该示例只展示结构，不承诺最终字段名。

## 3. 必需组成

### 3.1 Scope

Scope 决定契约可能在哪些上下文生效，先于 applicability 计算。至少支持：

- 项目、工作区和路径；
- Agent 种类；
- 工具名称；
- 操作类别；
- 用户/团队；
- 时间窗口和环境。

所有路径匹配必须基于 Adapter 规范化后的绝对路径和真实项目根目录，防止 `..`、符号链接和大小写差异绕过。

### 3.2 Applicability

使用受限 CEL 表达式，输入只能访问版本化的规范对象。禁止任意代码、文件读写和网络访问。Applicability 必须返回布尔值；异常按契约 `failure_mode` 处理。

### 3.3 Behavior

- `instruction`：面向人和 Agent 的解释，不承担执行语义。
- `enforcement`：`advisory`、`soft`、`hard`。
- `non_overridable`：下级 Scope 是否允许覆盖。

### 3.4 Correction

修正策略必须声明：

- 适用前提；
- 变换类型；
- 是否保持原意图；
- 是否需要人工确认；
- 修改前后风险级别；
- 可逆性；
- 修正后验证器。

v0.1 只允许有限变换：

- `set_fields`
- `remove_fields`
- `replace_path`
- `restrict_patch`
- `route_to_proxy`
- `none`

禁止契约内嵌任意 shell 或脚本作为变换器。自定义执行器属于受信插件，必须单独安装、签名和授权。

### 3.5 Verifier

验证器分为：

- 前置验证：改写后、执行前验证操作合规；
- 后置验证：工具完成后验证期望状态；
- 证据验证：确认某项前置工作在指定输入版本上真实完成。

支持类型：

- CEL 表达式；
- 文件摘要/存在性；
- 结构化命令结果断言；
- MCP 只读查询；
- 受信验证插件；
- 策略模型评分（只能用于软契约或触发 ASK）。

## 4. 规范化操作模型

Adapter 不应把宿主原始 JSON 直接暴露给全部契约，而应转换成稳定对象：

```json
{
  "schema_version": "acr.action/v1alpha1",
  "kind": "process.exec",
  "agent": {
    "kind": "codex",
    "version": "detected-at-runtime"
  },
  "tool": {
    "native_name": "Bash",
    "call_id": "native-call-id"
  },
  "workspace": {
    "project_id": "01J...",
    "cwd": "/workspace/project"
  },
  "operation": {
    "executable": "npm",
    "arguments": ["test"],
    "raw_command": "npm test"
  },
  "resources": [],
  "risk": "low"
}
```

保留两份输入：

- `native_original`：经脱敏后的原始 Hook 输入，用于诊断；
- `normalized_action`：稳定契约接口。

## 5. 意图保真规则

自动修正必须满足：

1. 不扩大资源访问范围。
2. 不提高环境风险级别。
3. 不增加不可逆副作用。
4. 不改变用户明确指定的目标，除非契约预先声明该映射且允许自动应用。
5. 修正后的动作仍能满足原始任务的主要目标。
6. 能通过确定性前置验证。

任一条件无法证明时，不能静默 `REWRITE`。

## 6. 契约编译器

编译器输出必须包括：

- 候选契约；
- 发现的歧义列表；
- 它做出的每项假设；
- 正例、反例和边界样例；
- 可自动修正与不可自动修正的边界；
- 风险评级；
- 建议审批人。

示例：输入“部署前必须运行 npm test”，编译器必须追问或标记：

- 什么算部署操作？
- 测试结果可以复用多久？
- 哪些文件变化会使旧测试证据失效？
- 测试失败时拒绝还是允许人工豁免？
- monorepo 中运行哪个目录的测试？

这些问题未解决前，契约只能保持 `draft`。

## 7. 豁免

豁免是有期限、有范围、有审计记录的独立对象，不修改原契约：

- 指定契约版本；
- 指定项目、资源、操作和身份；
- 必须有原因；
- 必须有过期时间；
- 高风险豁免要求二次确认或外部审批；
- 事件流记录申请、批准、使用和过期。
