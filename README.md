# cpa-plugin-step2api (StepFun AI Studio for CLIProxyAPI)

<p align="center">
  <strong>StepFun AI Studio (studio.stepfun.com) Native CLIProxyAPI Plugin</strong>
</p>

本项目仿照 `cpa-plugin-aistudio2api` 的 C ABI v1 / Schema v10 插件架构，并参考 `minc-nice-100/step2api` 的逆向协议实现，专为 **CLIProxyAPI** 打造的 **阶跃星辰 AI Studio (studio.stepfun.com)** 原生动态库插件。

---

## 核心特性

- **轻量高效，无重型虚拟机负担**：StepFun Studio 通过 `Oasis-Token`（Access + Refresh JWT）做身份认证，插件自动调用 Passport RPC 实现无感续期保活，无需 WAA 或浏览器驱动。
- **全模型流式接入**：完整支持 `step-3.5-flash`、`step-3.7-flash`、`step-5-preview`、`step-router-v1` 与 `step-audio-*`。
- **深度思考与思维链透传**：原生抓取 `reasoning_delta` 并转译为 OpenAI `reasoning_content`，支持 `low`、`medium`、`high`、`xhigh` 思考档位。
- **工具调用与联网检索**：支持将 `tools` 协议注入并抽取结构化 `tool_calls`，支持 `web_search` 开关。
- **真实图片输入**：`image_url` / `input_image`（支持 `data:image/...;base64` 与 http(s) URL）会上传到 Studio 资产接口并以 `asset_ids` 随本轮对话发送，模型可直接识图。
- **用量归因**：通过 provider 路由复用 core 的 `executorAdapter`，用量记录可正确标记 `auth_type=oauth`；目录同时暴露 `step2api/<model>` 前缀别名，确保插件执行器被 core 注册。
- **上下文缓存与用量如实上报**：回传 `prompt_tokens_details.cached_tokens`，真实反映缓存命中情况。
- **CLIProxyAPI 九大能力完整接入**：
  -c`auth_provider`：支持可视化登录与 `oauth/step2api-<account>.json` 批量凭据解析。
  - `model_provider` & `model_router`：动态目录发现，支持 `step2api/*` 前缀与 `step-*` 无前缀自动路由。
  - `executor`：原生 SSE 流式高效转发。
  - `quota_provider`：账号有效期与续期倒计时可视化。
  - `management_api`：内置 WebUI 管理页面与 API 接口。

---

## 配置说明 (config.yaml)

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
```

---

## 账号导入方式

1. 在浏览器中打开 [studio.stepfun.com](https://studio.stepfun.com) 并完成登录。
2. 打开开发者工具 (F12) -> Application -> Cookies -> 找到 `Oasis-Token` 并复制其值。
3. 在 `oauth/` 目录下创建 `step2api-<account>.json`，或在插件管理页面中粘贴导入：

```json
{
  "type": "step2api",
  "account_id": "my-step-account",
  "oasis_token": "eyJhbGciOi..."
}
```
