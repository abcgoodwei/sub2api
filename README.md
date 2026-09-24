# Sub2API Fork

<div align="center">

**AI API gateway for Codex, Claude, Gemini and other model providers**

English | [中文](README_CN.md)

</div>

This repository is the personal fork of [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api). This README focuses on fork-specific features, operations, and contributors. Use the [upstream README](https://github.com/Wei-Shaw/sub2api#readme) for the standard installation, configuration, and project structure documentation.

## Fork features

### Codex tickets and harvesting

- Independent ticket switches and harvest scopes for `gpt-5.3-codex`, `gpt-5.3-codex-spark`, `gpt-5.2-codex`, and `gpt-5.1-codex-mini`.
- 292 ticket lifecycle management, validity probes, failure cooldowns, post-recovery wakeups, and subscription node visibility.
- 780 harvest flow, selected edge-IP routing, configurable harvest parameters, and per-account targeting.
- Admin workbench for harvesting, account quality operations, scheduled-test isolation, and recovery.

### OpenAI Responses and scheduling

- Improved reasoning-content and usage-detail conversion from Chat Completions to Responses.
- Compatibility handling for `function_call_output`, `previous_response_id`, and tool history.
- Account, group, and user model restrictions, including stream-only groups.
- Latest-turn admission checks, sticky scheduling, Cyber session identity isolation, and WebSocket binding.
- Excel Basispoints forwarding for OpenAI OAuth accounts.

### Administration and observability

- Smart account operations, automatic quality recovery, and Pelican test-result presentation.
- Usage views with request phases, time-to-first-token, TPS, and health interpretation.
- User-scoped announcements and `banana*` / `gemini-*image*` image model allowlists.
- Mihomo management, dynamic harvesting egress, the Codex 292 Keeper, and production deployment helpers.

## Deployment and development

The upstream project documents the standard script installer, Docker Compose deployment, source build, configuration files, and directory structure:

- [Upstream installation and deployment](https://github.com/Wei-Shaw/sub2api#deployment)
- [Fork releases](https://github.com/ranxi2001/sub2api/releases)
- [Dependency security](docs/dependency-security.md)
- [Production and operations docs](docs/)

This fork keeps the upstream build layout and commands. For fork development, read [CONTRIBUTING.md](CONTRIBUTING.md) when present and run the relevant tests in `backend/` and `frontend/`.

## Contributing

Contributions to account scheduling, Codex tickets, Responses compatibility, operations, observability, and the admin UI are welcome. PRs should describe the affected path, configuration or migration impact, compatibility considerations, validation commands, and any credential, ticket, proxy, or deployment implications.

## Contributors

Thank you to everyone whose pull request has been merged:

<p>
  <a href="https://github.com/ranxi2001"><img src="https://avatars.githubusercontent.com/u/77790009?v=4" width="56" height="56" alt="Onefly" title="Onefly" /></a>
  <a href="https://github.com/blackdm666"><img src="https://avatars.githubusercontent.com/u/67053678?v=4" width="56" height="56" alt="老黑" title="老黑" /></a>
  <a href="https://github.com/akihitohyh"><img src="https://avatars.githubusercontent.com/u/79531840?v=4" width="56" height="56" alt="akihitohyh" title="akihitohyh" /></a>
  <a href="https://github.com/buluw"><img src="https://avatars.githubusercontent.com/u/45087912?v=4" width="56" height="56" alt="buluw" title="buluw" /></a>
  <a href="https://github.com/spake404"><img src="https://avatars.githubusercontent.com/u/123435269?v=4" width="56" height="56" alt="spake404" title="spake404" /></a>
  <a href="https://github.com/Mickey0811"><img src="https://avatars.githubusercontent.com/u/49522921?v=4" width="56" height="56" alt="Mickey0811" title="Mickey0811" /></a>
  <a href="https://github.com/mracry"><img src="https://avatars.githubusercontent.com/u/112537993?v=4" width="56" height="56" alt="mracry" title="mracry" /></a>
</p>

Merged PRs: [#1](https://github.com/ranxi2001/sub2api/pull/1), [#2](https://github.com/ranxi2001/sub2api/pull/2), [#3](https://github.com/ranxi2001/sub2api/pull/3), [#5](https://github.com/ranxi2001/sub2api/pull/5), [#7](https://github.com/ranxi2001/sub2api/pull/7), [#8](https://github.com/ranxi2001/sub2api/pull/8), [#9](https://github.com/ranxi2001/sub2api/pull/9), [#10](https://github.com/ranxi2001/sub2api/pull/10), [#12](https://github.com/ranxi2001/sub2api/pull/12), [#13](https://github.com/ranxi2001/sub2api/pull/13), [#14](https://github.com/ranxi2001/sub2api/pull/14), [#15](https://github.com/ranxi2001/sub2api/pull/15), [#17](https://github.com/ranxi2001/sub2api/pull/17), [#18](https://github.com/ranxi2001/sub2api/pull/18), [#20](https://github.com/ranxi2001/sub2api/pull/20), [#22](https://github.com/ranxi2001/sub2api/pull/22), [#23](https://github.com/ranxi2001/sub2api/pull/23), [#24](https://github.com/ranxi2001/sub2api/pull/24), [#31](https://github.com/ranxi2001/sub2api/pull/31), [#32](https://github.com/ranxi2001/sub2api/pull/32), [#33](https://github.com/ranxi2001/sub2api/pull/33), [#35](https://github.com/ranxi2001/sub2api/pull/35), [#36](https://github.com/ranxi2001/sub2api/pull/36), [#37](https://github.com/ranxi2001/sub2api/pull/37), [#38](https://github.com/ranxi2001/sub2api/pull/38), [#39](https://github.com/ranxi2001/sub2api/pull/39), [#41](https://github.com/ranxi2001/sub2api/pull/41), [#43](https://github.com/ranxi2001/sub2api/pull/43), [#45](https://github.com/ranxi2001/sub2api/pull/45), [#48](https://github.com/ranxi2001/sub2api/pull/48), [#50](https://github.com/ranxi2001/sub2api/pull/50), [#51](https://github.com/ranxi2001/sub2api/pull/51), [#52](https://github.com/ranxi2001/sub2api/pull/52), [#54](https://github.com/ranxi2001/sub2api/pull/54), [#55](https://github.com/ranxi2001/sub2api/pull/55), [#56](https://github.com/ranxi2001/sub2api/pull/56), [#57](https://github.com/ranxi2001/sub2api/pull/57), [#58](https://github.com/ranxi2001/sub2api/pull/58), [#59](https://github.com/ranxi2001/sub2api/pull/59).

## License

MIT License
