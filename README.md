# qTest Publisher

Cross-platform Harness/Drone plugin that publishes existing JUnit XML results
to qTest Manager through the documented public automation-log APIs.

The plugin does not run tests or replace Harness test reporting. A CI stage can
use `reports: {type: JUnit, spec: {paths: [...]}}` for Harness and then run this
plugin against the same workspace files to publish them to qTest.

## Behavior

1. Match workspace-relative JUnit paths using `*`, `?`, and recursive `**`.
2. Parse `testsuite` and `testsuites` documents with bounded file and text sizes.
3. Map a JUnit class (default) or method to stable qTest
   `automation_content`.
4. Submit bounded JSON batches to an explicit release, cycle, suite, or run.
5. Poll asynchronous queue jobs through `SUCCESS` or `FAILED`.
6. Append non-secret results to `DRONE_OUTPUT`.

Failed test cases are valid published results and do not fail the plugin.
Configuration, XML parsing, authentication, transport, or publication failures
do fail the plugin.

## Required settings

| Setting | Description |
| --- | --- |
| `qtest_url` | HTTPS qTest Manager base URL. |
| `bearer_token` | qTest public API bearer token. Always supply as a Harness secret. |
| `project_id` | Positive qTest project ID. |
| `destination_type` | `release`, `test-cycle`, `test-suite`, or `test-run`. Defaults to `test-cycle`. |
| `destination_id` | Positive destination ID. Type-specific aliases such as `release_id` and `test_cycle_id` are also accepted. |

A `release` destination also requires `suite_name`. The plugin finds an exact
suite name under the release when `reuse_suite` is true, otherwise it creates a
suite. For a `test-cycle`, an optional `suite_name` similarly creates or reuses
a suite under that cycle. Without a suite name, cycle mode uses qTest's
hierarchical automation-log endpoint.

## Optional settings

| Setting | Default | Description |
| --- | --- | --- |
| `result_paths` | `**/junit/*.xml` | Comma, semicolon, or newline-separated Ant-style patterns. |
| `identity_mode` | `class` | `class` groups methods as qTest steps; `method` publishes each method separately. |
| `module_names` | `Automation` | Hierarchy used by qTest cycle-mode publication. |
| `status_passed` | `PASSED` | Tenant mapping for passed results. |
| `status_failed` | `FAILED` | Tenant mapping for assertion failures. |
| `status_error` | `FAILED` | Tenant mapping for execution errors. |
| `status_skipped` | `SKIPPED` | Tenant mapping for skipped results. |
| `empty_result` | `warn` | `warn` succeeds with `SKIPPED`; `fail` fails the step. |
| `batch_size` | `100` | Maximum logs per request, from 1 through 1000. |
| `max_payload_bytes` | `4194304` | Maximum encoded request size, up to 50 MiB. |
| `max_test_cases` | `1000000` | Maximum total parsed test cases, up to 5,000,000. |
| `poll_interval` | `2s` | Queue polling interval. |
| `timeout` | `10m` | Whole-plugin timeout. |
| `ca_cert` | empty | Runtime PEM bundle appended to the system trust roots. |
| `proxy_url` | Harness/standard proxy environment | Explicit HTTP(S) proxy URL. |
| `properties` | empty | qTest property array JSON with positive `field_id` values. |
| `environment` | empty | Convenience value requiring `environment_field_id`. |
| `insecure_skip_tls` | `false` | Explicit compatibility escape hatch; not recommended. |

Input settings become uppercase `PLUGIN_` environment variables. For example,
`settings.qtest_url` is `PLUGIN_QTEST_URL`.

## Harness example

```yaml
- step:
    type: Plugin
    name: Publish JUnit to qTest
    identifier: publish_qtest
    spec:
      connectorRef: account.harnessImage
      image: harness/qtest-publisher:1.0.0
      settings:
        qtest_url: https://example.qtestnet.com
        bearer_token: <+secrets.getValue("qtestApiToken")>
        project_id: "123"
        destination_type: test-cycle
        destination_id: "456"
        result_paths: "**/junit/*.xml"
```

Do not put tokens, customer certificates, or tenant URLs in an image.

## Outputs

When `DRONE_OUTPUT` is available, the plugin emits:

- `QTEST_MATCHED_FILES`
- `QTEST_PARSED_TESTS`
- `QTEST_SUBMITTED_LOGS`
- `QTEST_DESTINATION_ID`
- `QTEST_JOB_IDS`
- `QTEST_STATE`
- `QTEST_RESULT_URL`

## Retry and duplicate policy

Safe GET and queue-poll operations retry bounded `429`, `502`, `503`, and
`504` responses. Submission POSTs are not retried. A transport error,
retryable HTTP response, or polling failure after qTest returns a queue ID is
reported as indeterminate so an operator can reconcile qTest before retrying.
Deterministic non-429 `4xx` responses are reported as rejected requests.
For a partial multi-batch submission, `DRONE_OUTPUT` records completed and
indeterminate queue IDs plus a `PARTIAL` or `INDETERMINATE` state. Stable
`automation_content` helps qTest reuse test objects but is not treated as a
submission idempotency guarantee.

## Platforms

- Linux AMD64
- Linux ARM64
- Windows AMD64 LTSC 2019 (`17763`)
- Windows AMD64 LTSC 2022 (`20348`)
- Windows AMD64 LTSC 2025 (`26100`)

Windows images use matching Nano Server final images and must run on matching
Windows workers under process isolation.
