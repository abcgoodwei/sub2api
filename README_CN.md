# Sub2API 二开版

<div align="center">

**面向 Codex、Claude、Gemini 等模型的 AI API 网关**

[English](README.md) | 中文

</div>

这是 [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api) 的个人 fork。本文档只记录本 fork 的二开功能、运维入口和贡献者；基础安装、环境配置和上游功能说明请参考[上游 README](https://github.com/Wei-Shaw/sub2api#readme)。

## 二开功能

### Codex ticket 与采票

- 独立控制 `gpt-5.3-codex`、`gpt-5.3-codex-spark`、`gpt-5.2-codex` 和 `gpt-5.1-codex-mini` 的 ticket 开关与采集范围。
- 支持 292 ticket 生命周期管理、有效性探测、失败冷却、账号恢复后的后台唤醒和订阅节点展示。
- 支持 780 采票链路、指定边缘 IP 直拨、可配置打票参数和按单号定向打票。
- 管理后台提供采票工作台、账号质量运维、定时测试隔离与恢复能力。

### OpenAI Responses 与账号调度

- 完善 Chat Completions 到 Responses 的推理内容和用量明细转换。
- 对 Responses 的 `function_call_output`、`previous_response_id` 和工具历史提供兼容处理。
- 支持按账号、分组和用户限制模型；支持分组仅允许流式请求。
- 增加最新会话准入校验、粘性调度、Cyber 会话身份隔离和 WebSocket 绑定。
- 支持 Excel Basispoints 协议，使用 OpenAI OAuth 账号转发 Responses 请求。

### 管理与观测

- 管理后台支持智能账号操作、质量自动恢复和 Pelican 测智结果展示。
- 使用记录增加请求耗时分段、首字延迟、TPS 和健康状态说明。
- 公告可按用户可见，图片模型白名单支持 `banana*` 与 `gemini-*image*`。
- 保留 Mihomo 管理、动态打票出口、Codex 292 Keeper 和生产部署脚本等配套能力。

## 部署与开发

上游已经提供完整的脚本安装、Docker Compose、源码编译、配置文件和项目结构说明：

- [上游安装与部署文档](https://github.com/Wei-Shaw/sub2api#deployment)
- [本 fork 的 Release](https://github.com/ranxi2001/sub2api/releases)
- [依赖安全说明](docs/dependency-security.md)
- [生产部署记录与运维文档](docs/)

本 fork 的源码构建仍沿用上游目录和命令。二开开发时请先阅读 [CONTRIBUTING.md](CONTRIBUTING.md)（如当前分支提供），并在 `backend/` 与 `frontend/` 分别运行对应测试。

## 贡献

欢迎提交与账号调度、Codex ticket、Responses 兼容、运维观测和管理后台相关的改进。提交 PR 前请说明：

- 受影响的请求路径或管理功能；
- 配置、数据库迁移和兼容性影响；
- 已运行的测试命令及结果；
- 是否涉及凭据、ticket、代理或生产部署。

## 贡献者

感谢所有已合并 PR 的贡献者：

<p>
  <a href="https://github.com/ranxi2001"><img src="https://avatars.githubusercontent.com/u/77790009?v=4" width="56" height="56" alt="Onefly" title="Onefly" /></a>
  <a href="https://github.com/blackdm666"><img src="https://avatars.githubusercontent.com/u/67053678?v=4" width="56" height="56" alt="老黑" title="老黑" /></a>
  <a href="https://github.com/akihitohyh"><img src="https://avatars.githubusercontent.com/u/79531840?v=4" width="56" height="56" alt="akihitohyh" title="akihitohyh" /></a>
  <a href="https://github.com/buluw"><img src="https://avatars.githubusercontent.com/u/45087912?v=4" width="56" height="56" alt="buluw" title="buluw" /></a>
  <a href="https://github.com/spake404"><img src="https://avatars.githubusercontent.com/u/123435269?v=4" width="56" height="56" alt="spake404" title="spake404" /></a>
  <a href="https://github.com/Mickey0811"><img src="https://avatars.githubusercontent.com/u/49522921?v=4" width="56" height="56" alt="Mickey0811" title="Mickey0811" /></a>
  <a href="https://github.com/mracry"><img src="https://avatars.githubusercontent.com/u/112537993?v=4" width="56" height="56" alt="mracry" title="mracry" /></a>
</p>

已合并 PR：[#1](https://github.com/ranxi2001/sub2api/pull/1)、[#2](https://github.com/ranxi2001/sub2api/pull/2)、[#3](https://github.com/ranxi2001/sub2api/pull/3)、[#5](https://github.com/ranxi2001/sub2api/pull/5)、[#7](https://github.com/ranxi2001/sub2api/pull/7)、[#8](https://github.com/ranxi2001/sub2api/pull/8)、[#9](https://github.com/ranxi2001/sub2api/pull/9)、[#10](https://github.com/ranxi2001/sub2api/pull/10)、[#12](https://github.com/ranxi2001/sub2api/pull/12)、[#13](https://github.com/ranxi2001/sub2api/pull/13)、[#14](https://github.com/ranxi2001/sub2api/pull/14)、[#15](https://github.com/ranxi2001/sub2api/pull/15)、[#17](https://github.com/ranxi2001/sub2api/pull/17)、[#18](https://github.com/ranxi2001/sub2api/pull/18)、[#20](https://github.com/ranxi2001/sub2api/pull/20)、[#22](https://github.com/ranxi2001/sub2api/pull/22)、[#23](https://github.com/ranxi2001/sub2api/pull/23)、[#24](https://github.com/ranxi2001/sub2api/pull/24)、[#31](https://github.com/ranxi2001/sub2api/pull/31)、[#32](https://github.com/ranxi2001/sub2api/pull/32)、[#33](https://github.com/ranxi2001/sub2api/pull/33)、[#35](https://github.com/ranxi2001/sub2api/pull/35)、[#36](https://github.com/ranxi2001/sub2api/pull/36)、[#37](https://github.com/ranxi2001/sub2api/pull/37)、[#38](https://github.com/ranxi2001/sub2api/pull/38)、[#39](https://github.com/ranxi2001/sub2api/pull/39)、[#41](https://github.com/ranxi2001/sub2api/pull/41)、[#43](https://github.com/ranxi2001/sub2api/pull/43)、[#45](https://github.com/ranxi2001/sub2api/pull/45)、[#48](https://github.com/ranxi2001/sub2api/pull/48)、[#50](https://github.com/ranxi2001/sub2api/pull/50)、[#51](https://github.com/ranxi2001/sub2api/pull/51)、[#52](https://github.com/ranxi2001/sub2api/pull/52)、[#54](https://github.com/ranxi2001/sub2api/pull/54)、[#55](https://github.com/ranxi2001/sub2api/pull/55)、[#56](https://github.com/ranxi2001/sub2api/pull/56)、[#57](https://github.com/ranxi2001/sub2api/pull/57)、[#58](https://github.com/ranxi2001/sub2api/pull/58)、[#59](https://github.com/ranxi2001/sub2api/pull/59)。

## 许可证

MIT License
