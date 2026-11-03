# Minitor

独立仓库：<https://github.com/blueskyxi3/minitor>。本项目与 `ai-hub` 分开检出、构建和发布；开发时可放在同级目录 `ai-hub/`、`minitor/`。SDK 插件 ID、可执行文件和安装包前缀继续使用 `monitor`，保持与宿主登记和代理路径兼容。

独立 SDK v2 监控插件。页面通过 `console.navigation` 注册；浏览器从 `sdk.getPluginBases('monitor').apiBase` 访问 `/self`、`/series`、`/options`、`/status`、`/settings`，全部经过宿主 `/api/plugin-bridge/monitor/api/v1/*` 代理。宿主不承载监控查询业务。

一个发布物包含 Go 服务、React/MUI 页面和固定版本的 VictoriaMetrics。运行时由内部 supervisor 启停数据库；数据库仅监听 loopback，不发布其端口。父进程监督管道用于回收意外退出后留下的数据库进程，数据目录使用独占文件锁。运行时不下载二进制。

0.0.1 的安装参数、POC 镜像和持久化配置见 [部署指南](DEPLOYMENT.md)。发布页：<https://github.com/blueskyxi3/minitor/releases/tag/0.0.1>。

## 构建与安装

需要 Go 1.25+、Node 20.19+/22.12+、npm、Python 3。构建时下载官方 VictoriaMetrics 发布包，校验 `versions.lock.json` 中已固定的归档和程序 SHA-256。普通依赖安装及官方 Release 下载需要网络；运行时不需要下载。

```sh
git clone https://github.com/blueskyxi3/minitor.git
cd minitor
make build
make build PLATFORM=linux-amd64
make build PLATFORM=linux-arm64
```

每次构建生成 `dist/<GOOS-GOARCH>/monitor` 和 `dist/monitor-0.0.1-<GOOS-GOARCH>.zip`。ZIP 只有程序、完整 Manifest 和 SDK 默认配置；前端、数据库、许可证已嵌入程序。解压总量受现有市场 150 MiB 限制。构建 macOS 包时执行 `make build PLATFORM=darwin-arm64`。

将 `catalog.example.json` 的定义添加到宿主后台的插件目录，通过已有 plugin-deployer 安装。本示例和 Release 附件 `catalog.json` 均使用公开的 GitHub Release ZIP 下载地址，master 可直接下载；也可改为 master 实际可读取的绝对路径。process 平台以部署器的 GOOS/GOARCH 为准。容器安装选择 docker/kubernetes，并提交实际可拉取的镜像地址、端口 `3013`、健康路径 `/healthz`。容器使用 UID/GID 10001，受管模式复用 `/data/plugin` 数据卷；Kubernetes 为单副本 Recreate。不要运行第二个副本共用同一数据库目录。

```sh
# 在本仓库根目录执行。
docker build -t ai-hub-monitor:0.0.1 .
# 多架构：发布到自己的镜像仓库。
docker buildx build --platform linux/amd64,linux/arm64 -t <registry>/ai-hub-monitor:0.0.1 --push .
```

独立运行可参考 `compose.example.yaml` 和 `deploy/kubernetes.yaml`，然后在宿主登记 external 服务（无需改变插件清单）。Kubernetes 示例里的共享密钥必须替换。独立服务须与宿主双向可达，不能将容器的 localhost 当作宿主地址。镜像和 process 包均包含同一固定数据库版本。

## 两类配置

SDK 运行配置由宿主注入，字段为 `plugin.id/server.address/host.base_url/host.secret`。`--config` 或 `AIHUB_PLUGIN_CONFIG` 指定其位置；手动运行参考 `host-config.example.yaml`，密钥不少于 32 字符。容器中宿主提供的只读运行配置与插件业务配置分开。

采集和存储参数只保存在插件自己的 `config.yaml`，格式见 `config.example.yaml`：

- process 默认 `<安装工作目录>/data/config.yaml`。
- 镜像默认 `/data/plugin/config.yaml`。
- `--monitor-config /path/config.yaml` 或 `MONITOR_CONFIG_PATH` 可覆盖其位置。
- 首次启动自动创建默认配置。配置采集凭据前服务可安装、可访问配置页，但没有采集目标。
- `storage.data_path` 的相对值以**该业务配置文件所在目录**为基准；绝对值直接使用，最后传入 VictoriaMetrics `-storageDataPath`。
- 默认 `./victoria-metrics-data`，容器中对应 `/data/plugin/victoria-metrics-data`。改变容器路径时须确保该路径由 volume/PVC 持久化；受管模式可使用已有 `/data/plugin` 挂载下的子目录。
- 路径创建或写入失败、目录被占用即报错，不改用临时目录。配置页显示解析后的目录且只读；编辑文件并重启生效，**不会自动移动或合并旧数据**。迁移时先停服务，再复制完整数据库目录并修改配置。
- 修改数据保留期会重启内置数据库；首期插件升级保持相同数据库版本，变更数据库版本前单独验证存储格式兼容性和备份。
- 数据保留默认 30 天，过期数据由数据库后台清理，并非到期瞬间回收磁盘。不要将数据库放进被升级替换的程序版本目录。

`GET/PUT /api/v1/settings` 只对拥有宿主监控权限的 root 开放，采用原子写入与 0600 文件权限。Redis URL、指标密码不返回明文，PUT 省略敏感字段表示保留；`storage.data_path` 不接受在线修改。不要把真实配置、凭据和数据库提交到 Git。

## 发现与认证

插件只读 `ai-hub:monitor:instances` 与 `ai-hub:monitor:instance:*`，不调用 SQL、不清理注册、不写宿主 Redis。Redis ACL 示例应限制这些键并允许连接认证所需命令、`SMEMBERS`、`MGET`；使用与宿主相同的 Redis 数据库。Redis 故障清空当前目标文件并明确报告发现失败，保留历史数据，恢复后重新发现，不以旧目标、内存或 SQL 替代发现。

实例的 `metrics_url` 必须指向自身 `/api/metrics`，不能填写随机负载均衡地址。所有目标保持相同 `METRICS_USER/METRICS_PASSWORD`。独立 billing/route worker 需要包含认证指标别名和注册地址的新版本。API 与 embedded worker 共进程只采集一次，纯 route worker 和 plugin-deployer 不参与本插件的采集。

页面接口校验 `X-AIHub-Identity` 的 HMAC、插件 ID、方法、完整 URI、用户 ID 和过期时间，再使用宿主提供的委托凭证请求 `/api/user/self`，核验当前 `page_access.monitor`。无监控权限拒绝查看；root 才能配置。不使用长期管理员令牌或浏览器传入角色。

## 口径与边界

四项延迟沿用宿主统一埋点。分位数是现有 Histogram 桶内插值的近似值，跨实例先合并桶；区间汇总基于整个区间增量，不平均趋势分位数。TPOT 为请求级平均值的分布。首输出之前失败、非流式及不完整请求按现有埋点范围留空，四项样本数不必相等。超过最高有限桶显示 `≥`。没有有效观测保留 null，曲线不跨缺口连线。

HTTP 指标不具备模型/渠道标签，渠道调用包含重试；并发只支持模型/实例。选择不支持的维度时对应面板提示。模型对比使用原始请求模型名，点击模型切换趋势。未新增用户、API Key 或请求内容标签。

Billing 阶段可能重叠，不能将各阶段 P99 相加。事件处理速率表示处理批次；结算订单速率来自成功提交后 `ai_hub_billing_batch_size_sum{stage="wallet_settlement"}`，不使用目前未正确累计的旧结算 Counter。阶段选择仅影响阶段延迟面板。

全局积压 Gauge 依据成功观测时间选一个最新且采集正常的实例，超出 `max(120s, 4 × 采集周期)` 不作为当前值，并提示最后观测时间；SQL backlog 使用自身采样时间且过滤未知负值，不能按 worker 数量相加。局部计数器可聚合。采集状态页区分心跳就绪、抓取状态、缺失/过期运行指标与数据库可用性。实时查询不使用数据库默认的 30 秒可见性偏移，最新样本仍可能有数秒的写入缓冲延迟。

默认周期 15 秒、超时 5 秒、保留 30 天；最长查询 30 天、每条曲线不超过 1500 点，查询最多四路并行。时序库读取响应有大小限制。历史从启用采集时积累，不回填。默认评估范围为 2–20 个实例、峰值 1000 请求/秒；实际资源与磁盘主要由活跃模型/渠道标签组合决定，需以运行负载实测。

## 验证

```sh
make test
make lint
# 浏览器集成测试需要一次性安装 Chromium（开发/测试依赖）：
npx playwright install chromium
# 完成 native 构建，并启动隔离 Redis 后运行真实进程集成测试：
python3 scripts/smoke.py --redis-url redis://127.0.0.1:16385/0
python3 scripts/smoke.py --redis-url redis://127.0.0.1:16385/0 --instances 20
python3 scripts/smoke.py --redis-url redis://127.0.0.1:16385/0 --faults
python3 scripts/storage_smoke.py
# 先构建镜像，再执行一次性容器/数据卷的真实恢复检查：
python3 scripts/docker_smoke.py
```

`--browser` 额外验证主题、四种语言、移动端、代理请求和卸载停止轮询。React、MUI 和 Playwright 均由本仓库自己的 `package-lock.json` 安装，不需要检出 AI Hub 或 agent；截图与报告写入 `dist/qa/`。模拟数据从测试开始积累，截图中的短曲线不代表回填历史。集成脚本只创建自己带随机后缀的注册键和 Redis ACL，结束后清理；多个 smoke 用例须顺序运行，避免同时发现彼此的实例。

宿主侧回归测试位于 AI Hub 仓库，隔离 Redis 的配置见该仓库 `docs/development/redis-tests.md`；不能指向生产实例。Kubernetes 的单副本、Recreate、PVC 保留及启停由宿主 `common/plugindeployer` 回归覆盖；真实集群升级和卷恢复仍须在独立测试集群验证。本模块不新增 SQL 表或列，也不修改宿主插件注册格式。
