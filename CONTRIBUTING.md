# 贡献指南

## 开发

```bash
go test -count=1 ./...
go vet ./...
```

- Go 版本见 `go.mod`；数据库使用纯 Go 的 `modernc.org/sqlite`，不需要 CGO。
- 测试不得调用真实的 `claude` 或 `codex`：启动类测试一律通过 `--binary` 使用假程序。真实宿主上的验证结果记录在 `docs/capability-*.md`。
- 安全相关的改动（契约评估、Guarded 边界、事件哈希链、覆盖对账）需要附带反证：临时破坏防线时对应测试必须失败。

## 提交改动

- 一个改动只解决一个问题，并说明动机和验证方式。
- 新增依赖前先确认其许可证与 Apache-2.0 兼容。
- 贡献的 Hook 输入样例必须匿名化：去掉会话 ID、用户路径、提示词正文和任何凭据。

## 许可证

提交即表示你同意以 Apache License 2.0 授权你的贡献，见 `LICENSE`。
