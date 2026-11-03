# AGENTS.md — Minitor

独立的 AI Hub SDK v2 监控插件。仓库：<https://github.com/blueskyxi3/minitor>；本地目录：`/Users/zouw/work/minitor`。AI Hub 宿主位于平级的 `/Users/zouw/work/ai-hub`，构建与测试不依赖宿主源码或 node_modules。

## 边界

- SDK 插件 ID、程序名和 ZIP 前缀保持 `monitor`。仓库名称使用 `minitor`。
- Go 模块为 `github.com/blueskyxi3/minitor`；页面使用宿主注入的 React/MUI/SDK，不打包第二份运行时。
- 浏览器业务请求只能使用 `sdk.getPluginBases('monitor').apiBase` 和 `sdk.API`，由宿主通用插件代理转发。宿主身份验证和当前用户权限校验必须保留。
- 采集与存储参数只由插件自己的业务 `config.yaml` 管理，SDK 运行配置与之分离。相对数据路径基于业务配置所在目录。
- VictoriaMetrics 固定版本及 SHA-256 记录在 `versions.lock.json`，仅构建时下载，运行时不下载。数据目录独占，不自动迁移或回退目录。
- 缺失/过期观测保持空值；分位数先聚合桶再计算；Billing 全局快照取最新有效观测，不按 worker 求和。

## 构建与验证

```sh
make build                         # native 程序与 ZIP，准备前端和数据库 bundle
make build PLATFORM=linux-amd64
make build PLATFORM=linux-arm64
make test                          # Go race + 前端测试
make lint                          # ESLint + go vet
npx playwright install chromium   # 可选浏览器测试的开发依赖
python3 scripts/storage_smoke.py
python3 scripts/smoke.py --redis-url redis://127.0.0.1:16385/0 --browser
python3 scripts/smoke.py --redis-url redis://127.0.0.1:16385/0 --faults
docker build -t ai-hub-monitor:1.0.0 .
python3 scripts/docker_smoke.py
```

集成测试只能使用隔离 Redis，不能指向业务实例。smoke 用例顺序执行，避免互相发现测试注册。真实 Kubernetes 验证只能使用独立测试集群。

## 修改约定

- 仅修改任务需要的代码，Go 使用 gofmt/goimports，前端支持深浅主题及四种语言。
- 已建立 GitNexus 索引时，修改函数前运行 upstream impact；UNKNOWN 结果用文本核查补充，高风险先说明。提交前运行 detect_changes，不能将 partial/truncated 结果视为完整检查。
- 创建任何新的 Git 分支前，先说明分支名称和用途，并取得用户明确确认。
- 新增数据库表或持久化字段前，列出具体表名、字段、类型、用途和影响范围，并单独取得用户确认。
- 不提交 node_modules、dist、数据库文件、运行时配置或凭据。修改数据保留期或数据库版本时明确记录恢复与兼容性验证。
