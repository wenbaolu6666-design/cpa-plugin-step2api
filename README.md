<p align="center">
  <img src="assets/logo.png" width="112" height="112" alt="Step2API logo">
</p>

<h1 align="center">Step2API</h1>

<p align="center">
  StepFun AI Studio（studio.stepfun.com）原生 CLIProxyAPI 插件
</p>

<p align="center">
  <a href="https://github.com/wenbaolu6666-design/cpa-plugin-step2api/releases"><img src="https://img.shields.io/github/v/release/wenbaolu6666-design/cpa-plugin-step2api?label=release" alt="Release"></a>
  <a href="https://github.com/wenbaolu6666-design/cpa-plugin-step2api/actions/workflows/release.yml"><img src="https://github.com/wenbaolu6666-design/cpa-plugin-step2api/actions/workflows/release.yml/badge.svg" alt="Release build"></a>
  <img src="https://img.shields.io/badge/CLIProxyAPI-v8.0.49%2B-4f46e5" alt="CLIProxyAPI v8.0.49+">
  <img src="https://img.shields.io/badge/license-MIT-blue" alt="MIT license">
</p>

<p align="center">
  Changelog: <a href="CHANGELOG.md">CHANGELOG.md</a>
</p>

把阶跃星辰 AI Studio 的网页会话接进 CLIProxyAPI：一个账号池、一条 OpenAI 兼容接口，就能用上
`step-5-preview`、`step-3.7-flash` 等模型，并保留流式输出、思维链、工具调用、图片输入和用量统计。

> [!WARNING]
> 本项目仅用于开源学习与技术交流，禁止商用或违法用途。与阶跃星辰及相关公司、商标无隶属或背书关系，
> 使用产生的一切后果由使用者自行承担。完整声明见文末。

## 这个插件解决什么

| 需求 | Step2API 的做法 |
| --- | --- |
| 用 Studio 的网页会话直接调模型 | 复用 `Oasis-Token`（Access + Refresh JWT），不依赖浏览器驱动或重型虚拟机 |
| 不想手动复制 Cookie | 官方登录：独立无痕窗口 + 自动抓取凭据，登录完即可用 |
| 多账号轮询与续期 | 账号池自动刷新 token，按可用状态调度，额度与到期时间可视化 |
| 客户端工具调用 | Studio 内置工具自动改写为客户端已声明的 shell / plan 工具，回合不会中断 |
| 图片输入 | `image_url` / `input_image` 上传为 Studio 资产并随本轮对话发送 |

## 核心特性

- **全模型流式接入**：`step-3.5-flash`、`step-3.7-flash`、`step-5-preview`、`step-router-v1` 与 `step-audio-*`。
- **思维链透传**：原生抓取 `reasoning_delta` 并转成 OpenAI `reasoning_content`，支持 `low` / `medium` / `high` / `xhigh`。
- **工具调用**：`tools` 协议注入 + 结构化 `tool_calls` 抽取；文本形式的工具调用也会被识别并转译。
- **内置工具自动改写**：`python`、`file_write`、`file_read`、`document_parser` 会映射到客户端实际声明的命令工具
  （`exec_command`、`bash`、`shell`、`execute_command`、`run_command`、`terminal` 等），`todo` 映射到
  `update_plan` / `todo_write`；无法映射时给出可读提示而不是直接中断。
- **真实图片输入**：支持 `data:image/...;base64` 与 http(s) 图片地址。
- **用量与缓存如实上报**：回传 `prompt_tokens_details.cached_tokens`，用量记录标记 `auth_type=oauth`。
- **并发保护**：本地 Studio 槽位（2）与上游并发上限（5）双重闸门；已满时短暂等待后返回 `409`，
  避免 CPA 核心按 429/503 连续重试 5 分钟。
- **九大能力接入**：`auth_provider`、`model_provider`、`model_router`、`executor`、`quota_provider`、
  `management_api`、`scheduler`、`command_line_plugin`、`request_lifecycle_plugin`。

## 安装

### 插件商店

在 CPA 控制台的插件商店搜索 `step2api` 并安装，随后在插件配置里启用即可。

### 手动安装

1. 从 [Releases](https://github.com/wenbaolu6666-design/cpa-plugin-step2api/releases) 下载对应平台的压缩包。
2. 解压出动态库（Windows `step2api-*.dll`、Linux `.so`、macOS `.dylib`），放入 CPA 的
   `cpa-core/plugins/<os>/<arch>/` 目录。
3. 在 `config.yaml` 的 `plugins.configs` 下添加配置（见下方配置说明），保存后等待热加载或重启核心。

## 登录与账号管理

### 官方登录（推荐）

1. 控制台 → 插件 → Step2API → 账号页，点击 **官方登录（无痕窗口）**。
2. 插件启动一个独立临时 Profile 的浏览器窗口（`--user-data-dir=<临时目录>`），打开 `studio.stepfun.com`。
3. 在窗口内完成登录。插件通过本机 DevTools 连接读取 `Oasis-Token` / `Oasis-Webid`，自动写入账号并关闭窗口。
4. 临时目录随即删除，不读取也不污染你日常浏览器的任何数据。

> OAuth 入口本身不会自动弹窗：控制台点击「添加账号」只打开账号页，只有点击「官方登录（无痕窗口）」才会启动浏览器。

### 粘贴 Cookie（兜底）

1. 在浏览器打开 [studio.stepfun.com](https://studio.stepfun.com) 并登录。
2. F12 → Network → 任意 `/api/` 请求 → 复制完整 Cookie，或直接复制 `Oasis-Token` 的值。
3. 在账号页粘贴并保存，或写入 `oauth/step2api-<account>.json`：

```json
{
  "type": "step2api",
  "account_id": "my-step-account",
  "oasis_token": "eyJhbGciOi...",
  "web_id": "optional"
}
```

`Oasis-Token` 由 `access...refresh` 两段 JWT 组成，插件会按 access 的过期时间自动续期。

## 配置说明

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    step2api:
      enabled: true
      base_url: "https://studio.stepfun.com"
      model_prefix: "step2api/"
      route_unprefixed_models: true
      default_thinking: true
      default_effort: "medium"
      sync_auth_files: true
      browser_path: ""            # 可选：指定 Chrome/Edge 可执行文件
      login_timeout_seconds: 300  # 可选：无痕登录等待时间
      studio_max_concurrency: 2            # 可选：本机同时请求 Studio 的上限
      studio_slot_wait_seconds: 45         # 可选：本机槽位排队时间
      upstream_concurrency_wait_seconds: 90 # 可选：上游并发已满时的重试预算
```

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `enabled` | `true` | 是否启用插件 |
| `base_url` | `https://studio.stepfun.com` | Studio 站点地址 |
| `proxy` | 空 | 访问 Studio 使用的代理 |
| `model_prefix` | `step2api/` | 对外模型前缀 |
| `route_unprefixed_models` | `true` | 是否同时接受无前缀模型名 |
| `plan_weight` / `studio_weight` | `3` / `1` | Plan 渠道与 Studio 渠道的调度权重 |
| `default_thinking` | `true` | 默认开启思考 |
| `default_effort` | `medium` | 默认思考档位 |
| `sync_auth_files` | `true` | 是否把账号同步到 CPA oauth 目录 |
| `data_dir` | `~/.step2api` | 插件数据目录 |
| `browser_path` | 空 | 官方登录使用的浏览器路径，留空自动探测 |
| `login_timeout_seconds` | `300` | 官方登录等待超时 |
| `studio_max_concurrency` | `2` | 本机同时发给 Studio 的最大请求数；设为 `1` 可完全串行 |
| `studio_slot_wait_seconds` | `45` | 本机槽位占用时最多排队多久，超时才返回 409 |
| `upstream_concurrency_wait_seconds` | `90` | 上游返回并发已满时的继续重试预算 |

浏览器自动探测顺序：Chrome → Chromium → Edge → Brave；也可用环境变量
`STEP2API_CHROME_PATH` / `CHROME_PATH` / `BROWSER_PATH` 指定。

## 管理接口

所有接口位于 `http://127.0.0.1:8317/v0/management/step2api/`，需要 CPA 管理密钥：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/state` | 账号、额度、用量概览 |
| POST | `/api/accounts` | 通过 Cookie / Token 添加账号 |
| POST | `/api/accounts/browser-login` | 启动官方无痕登录 |
| GET | `/api/accounts/browser-login` | 查询登录状态（含已识别的 Cookie 名） |
| POST | `/api/accounts/browser-login/cancel` | 取消登录并关闭窗口 |
| POST | `/api/accounts/refresh` | 刷新全部账号 token |
| POST | `/api/accounts/test` | 测试账号连通性 |
| POST | `/api/accounts/disable` | 启用 / 停用账号 |
| POST | `/api/accounts/delete` | 删除账号 |
| POST | `/api/sync` | 同步账号到 CPA auth 文件 |

账号页：`http://127.0.0.1:8317/v0/resource/plugins/step2api/accounts`。

## 常见问题

**并发已满（HTTP 409）**
本地槽位（2）或上游并发（5）已满时，插件会短暂等待再返回 409。继续重试没有意义，换模型或稍后再试；
若长期出现，说明 Studio 侧槽位没有释放，需要等待其 TTL 或减少并发。

**模型说“本环境没有这些工具”**
说明客户端没有声明可执行命令的工具。插件会列出当前会话已声明的工具名；在客户端启用 shell / 命令执行类工具后重试即可。

**官方登录窗口没有出现**
- 确认本机安装了 Chrome / Edge / Brave，或在 `browser_path` 中指定路径。
- 远程服务器或无桌面环境无法弹窗，请改用粘贴 Cookie。
- 登录超过 `login_timeout_seconds` 会自动关闭窗口。

**图标显示成头像或空白**
插件元数据与商店条目都指向仓库内的 `assets/logo.png`；若控制台仍显示旧图标，刷新插件商店缓存或重新安装插件。

## 构建

需要 Go 1.27+ 与 CGO 工具链（Windows 下使用 mingw-w64）：

```bash
make test    # go vet + go test ./...
make build   # 生成 bin/step2api.dll（c-shared）
```

正式发布通过 GitHub Actions 构建 5 个平台（windows/linux/darwin × amd64/arm64），
推送 `v*` 标签即自动产出 Release 与校验文件。

## 免责声明

本项目为个人开源的协议适配实现，仅用于学习与技术交流：

- 与阶跃星辰（StepFun）及其关联公司、商标无任何隶属、授权或背书关系。
- 不得用于商业转售、批量账号滥用、绕过服务条款或其他违法用途。
- 使用本项目可能违反上游服务条款，账号风控、封禁、数据与费用损失由使用者自行承担。
- 作者不对因使用本项目产生的任何直接或间接损失负责。

## License

MIT