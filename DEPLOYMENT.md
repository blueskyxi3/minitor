# 部署 Minitor 0.0.1

Release：<https://github.com/blueskyxi3/minitor/releases/tag/0.0.1>。插件 ID 为 `monitor`，通过 AI Hub SDK v2 的 `console.navigation` 提供页面。

## 通过插件市场安装

1. 从 Release 下载 `catalog.json`，使用 root 在 AI Hub 后台“插件目录”新增或更新定义。Manifest 版本须为 `0.0.1`，process ZIP 地址已指向公开 Release。
2. 在“插件市场 → 性能监控”选择安装方式。process 由部署器自动选择当前平台的 ZIP；Docker/Kubernetes 填写下表参数。
3. 安装完成后启用插件，在“性能监控 → 配置”填写 Redis URL、指标用户名和密码。
4. 检查“采集状态”：Redis 发现成功、时序库可用、实例在线。没有历史观测时，图表会从启用采集后逐步积累。

| 参数 | POC 值 |
| --- | --- |
| 镜像 | `ctt-repository-cn-guangzhou.cr.volces.com/compute-power-zone/ai-hub-monitor:0.0.1` |
| 容器端口 | `3013` |
| 健康路径 | `/healthz` |
| 宿主回调 origin | `http://ai-hub-web.compute-power-zone.svc:3000` |
| 持久化挂载 | `/data/plugin` |
| 副本 / 更新策略 | `1 / Recreate` |

Docker/Kubernetes 安装需要宿主的 plugin-deployer 启用对应 backend。Kubernetes 需要预创建插件命名空间、授予部署器管理 Deployment/Service/Secret/PVC 的权限，并配置镜像仓库的 `image_pull_secrets`。POC 镜像仓库的拉取认证与 GitHub 仓库公开状态无关。宿主与插件服务须双向可达，插件还须访问 Redis 与各实例自己的 `/api/metrics`。

受管安装时，配对密钥、监听和回调配置由宿主注入。受管 PVC 大小由宿主 `plugin_deployer.kubernetes.storage_size` 设置；本项目独立部署示例为 20Gi，按实际活跃时序数量和 30 天保留量安排空间。

## 配置与持久化

- process 业务配置默认是 `<插件工作目录>/data/config.yaml`。
- 容器业务配置默认是 `/data/plugin/config.yaml`，将整个 `/data/plugin` 挂载到 volume/PVC。
- `MONITOR_CONFIG_PATH` 可指定业务配置位置。SDK 运行配置由 `AIHUB_PLUGIN_CONFIG` 或 `--config` 单独指定。
- Redis URL 使用与宿主相同的集群和数据库编号，推荐只读 ACL。指标用户名和密码与所有被采集实例的 Basic Auth 保持一致。
- 默认每 15 秒采集、保留 30 天。只有具备监控权限的 root 可以保存配置。
- 默认数据目录为配置文件旁的 `./victoria-metrics-data`，容器内解析为 `/data/plugin/victoria-metrics-data`。
- 修改 `storage.data_path` 需要编辑业务配置并重启；相对路径基于该配置所在目录。不会自动迁移旧数据，也不会回退目录。

业务配置示例见 [config.example.yaml](config.example.yaml)。独立 Billing Worker 需运行包含认证 `/api/metrics` 别名和 `metrics_url` 心跳的宿主版本。

## 独立运行后接入宿主

如果采用自行维护的容器，使用 [Compose](compose.example.yaml) 或 [Kubernetes](deploy/kubernetes.yaml) 示例，再在宿主插件管理中登记为 external 服务。配对密钥和插件地址必须与宿主登记一致；受管安装和 external 接入选择一种。

Compose 运行示例：

```sh
export MONITOR_HOST_BASE_URL=http://master:3000
read -rsp '共享插件密钥: ' MONITOR_HOST_SECRET
export MONITOR_HOST_SECRET
docker compose -f compose.example.yaml pull
docker compose -f compose.example.yaml up -d
```

将 `master` 替换为容器可达的宿主地址，并让二者加入同一可通信网络。Compose 命名卷保存数据。Kubernetes 示例需替换 Secret 中的宿主地址和配对密钥，并设置实际命名空间、StorageClass 和 imagePullSecret；应用前先确认这些值。

插件内部 VictoriaMetrics 随程序启停，只监听 loopback。用户从 AI Hub 的“性能监控”入口访问，浏览器的业务请求均经过宿主 `/api/plugin-bridge/monitor/api/v1/*`。
