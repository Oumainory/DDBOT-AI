# NormalizedEvent v1

`ObservedEvent` 是 Phase 2 的持久化事实；`NormalizedEvent` 是给分类器的第二层、公开且有界的输入。Normalizer 不执行网络请求，不读取凭据，不携带 raw Bilibili/Twitter response、headers、cookies、OneBot payload 或 renderer 对象。

## 字段

| 字段 | 说明 |
| --- | --- |
| `schema_version` | 当前为 `1` |
| `id` / `normalized_event_id` | 稳定 `norm_` SHA-256 派生身份；`id` 保留旧契约兼容 |
| `observed_event_id` | Phase 2 observed row 身份 |
| `platform`, `source_id`, `external_id`, `event_type` | 来源和事件身份 |
| `source_display_name`, `author_id`, `author_name` | 非敏感展示元数据 |
| `title`, `body`, `related_body` | 规范化文本 |
| `url`, `public_urls`, `media` | 仅 http/https public URL |
| `source_event_at`, `observed_at` | 来源/观察时间 |
| `normalizer_version`, `preprocessor_version` | 可重复处理身份 |
| `truncated`, `normalization_flags` | 截断或安全标记 |
| `replay_payload` | 有界 public-only JSON；不进入 provider prompt |

## 限制

主标题最多 512 rune，正文和相关文本先分别截断，再把总 prompt 文本限制为 12,000 rune（标题和正文优先，相关文本确定性裁剪）。发生裁剪时 `truncated=true` 且策略层强制 PASS。URL 去除 userinfo、fragment 和 bearer-like query key；media 最多 32 项，公开 URL 总数最多 16 项。

当前版本：`bilibili-v1`、`twitter-v1`、预处理器 `text-v1`。不支持的来源返回安全错误，不调用模型；未来来源必须新增明确 Normalizer 版本。
