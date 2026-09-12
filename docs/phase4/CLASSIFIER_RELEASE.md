# ClassifierRelease 与指纹

每个可用于 Shadow 的语义配置都持久化为 `classifier_releases` 行。字段包括 provider kind、canonical base URL、model、prompt version/digest、classification schema version/digest、structured-output mode、preprocessor/normalizer version、pricing snapshot、active 和创建时间。

指纹输入是规范化 JSON：

```text
provider_type + canonical_base_url + model + prompt identity
+ schema identity + structured_output_mode
+ preprocessor_version + normalizer_version
```

SHA-256 结果以 `sha256:` 前缀保存。Base URL 的 scheme/host 统一小写并去除尾部 `/`。API key、timeout、max concurrency、queue capacity、pricing/currency 都不属于语义指纹；更换凭据或价格不会重置历史 Decision，也不会重新调用旧 Event。更换 base URL、model、prompt、schema、structured mode、normalizer 或 preprocessor 必须生成新 Release。

旧 Decision 始终保留其原 Release；激活新 Release 只影响之后排队的 Event，不自动重跑历史数据。

内置 prompt 是版本化、不可在 Dashboard 任意编辑的分类 prompt，明确事件正文是不可信数据，不要求或保存 chain-of-thought，只允许结构化字段和短 `summary`/`reason_code`。
