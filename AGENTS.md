# qTest Publisher

## Scope

This repository owns the generic qTest JUnit publisher executable and its
platform Dockerfiles. It does not own qTest tenant configuration, Harness CI
engines, delegates, runners, or customer certificates and credentials.

## Rules

- Use only documented qTest public APIs and bearer-token authentication.
- Keep all tenant-specific destinations, status mappings, proxies, and trust
  material as runtime inputs.
- Never log authorization values or blindly retry an ambiguous submission.
- Preserve Linux AMD64/ARM64 and Windows AMD64 LTSC 2019/2022/2025 builds.
- Keep the executable static and the runtime images minimal.
- Parent images and published child images must be pinned by digest.
- Failed tests are publishable data; operational publication failures fail the
  plugin.
- Run `go test -race ./...`, `go vet ./...`, and all platform cross-builds
  before release.
