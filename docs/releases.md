# CI 与发布

CI 在 push main、Pull Request 或手动触发时运行；发布 workflow 复用同一套检查和打包任务，不使用个人测试账号或飞书 secrets。

## 平台

| 发布包后缀 | Go 设置 | 说明 |
| --- | --- | --- |
| linux-amd64.tar.gz | GOOS=linux GOARCH=amd64 | x86 64位 |
| linux-arm64.tar.gz | GOOS=linux GOARCH=arm64 | ARM 64位 |
| windows-amd64.zip | GOOS=windows GOARCH=amd64 | Windows x86 64位 |
| darwin-amd64.tar.gz | GOOS=darwin GOARCH=amd64 | Intel Mac |
| darwin-arm64.tar.gz | GOOS=darwin GOARCH=arm64 | Apple Silicon |

仅支持 64 位系统。所有构建 CGO_ENABLED=0，不依赖目标系统的 glibc 动态库。Linux 仍须满足 Go 工具链支持的内核要求、串口驱动和访问权限。CI 在 Linux amd64/arm64 原生 runner 上运行单元测试及 race，并执行打包后的 amd64 二进制 `version` 冒烟测试。跨编译成功不等于目标平台已接入实机验证。

## 发版

在 main 上更新代码及文档，推送符合语义版本格式的 tag：

```sh
git tag v0.4.0
git push origin v0.4.0
```

Release workflow 会先运行：公开文件与全历史敏感信息扫描、四种 runner 测试、Linux race、静态检查、漏洞检查、模糊测试，以及五目标打包。通过后才创建 draft Release、上传包和 `SHA256SUMS`，最后公开发布。带预发布后缀的 tag（如 `v0.4.1-rc.1`）标为 prerelease。

失败可在 GitHub 重新运行 workflow；已公开的同版本 Release 不会被自动覆盖，须发新 tag。只有 publish job 获得 contents:write；PR 流程不授予写权限、不使用个人凭据。Actions 固定到完整 commit SHA，Dependabot 会为版本更新提出 PR。

手动触发 Release 时必须选择已有的版本 tag；选择 main 不会发布。CI 生成的开发快照可在对应 Actions run 的 packages artifact 下载。

## 本地生成相同格式的包

从仓库根目录运行：

```sh
go run ./tools/release -version v0.4.0 -output dist/releases/v0.4.0
go run ./tools/release -version v0.4.0 -targets linux/amd64,linux/arm64 -output dist/linux
```

包内仅包含二进制、README、LICENSE、配置模板、部署文件、文档、USB 协议、Lua 程序和第三方许可。不包含源码测试中的虚构凭据、私有配置、联调记录、数据库或下载的厂商固件。完整源码可从仓库及 GitHub 自动生成的 source archive 获取。

Linux 解压前校验 `sha256sum --check SHA256SUMS`；tar 包保留可执行位，解压后可直接执行 `./mobilemod version`。配置模板不含可使用的 app ID、密钥、接收人或个人号码，需自行配置。
