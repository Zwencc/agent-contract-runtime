# ACR Console frontend

Vue 3 frontend for the local ACR runtime console.

```text
pnpm install
pnpm dev
pnpm typecheck
pnpm test
pnpm build
```

默认连接本机 Go API。开发时先运行 `acr ui --no-open`，Vite 会把 `/api` 代理到 `127.0.0.1:7480`。如需独立查看确定性演示数据：

```text
VITE_ACR_DATA_SOURCE=mock pnpm dev
```

所有页面都依赖 `AcrDataSource`，切换数据源不需要修改页面组件。生产构建位于 `dist/`，由 Go 二进制直接嵌入并通过 `acr ui` 提供。
