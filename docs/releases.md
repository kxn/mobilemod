# CI 与发布

main 和 Pull Request 跑一次 CI；版本 tag 只触发打包发布，不重复测试。不使用个人测试账号或飞书 secrets。

## 平台

| 发布包后缀 | Go 设置 | 说明 |
| --- | --- | --- |
| linux-amd64.tar.gz | GOOS=linux GOARCH=amd64 | x86 64位 |
| linux-arm64.tar.gz | GOOS=linux GOARCH=arm64 | ARM 64位 |
| windows-amd64.zip | GOOS=windows GOARCH=amd64 | Windows x86 64位 |
| darwin-amd64.tar.gz | GOOS=darwin GOARCH=amd64 | Intel Mac |
| darwin-arm64.tar.gz | GOOS=darwin GOARCH=arm64 | Apple Silicon |

仅支持 64 位系统。所有构建 CGO_ENABLED=0，不依赖目标系统的 glibc 动态库。CI 使用一个 Linux amd64 runner；平台包由同一打包器交叉编译。

## 发版

在 main 上更新代码及文档，推送符合语义版本格式的 tag：

```sh
git tag v0.4.1
git push origin v0.4.1
```

CI 将普通测试、race 和覆盖率合并为一次 go test，另运行 go vet、一套 Gitleaks（含个人号码规则）及依赖漏洞检查。不再在多个 runner 重复执行同一套测试，也不在每次 push 自动做模糊测试和五目标打包。

在 main 的 CI 通过后打 tag。Release 直接交叉编译五目标、生成 SHA256SUMS、上传并发布；不重复跑 CI。已有版本不覆盖，修复后发新版本。带预发布后缀的 tag 标为 prerelease。PR 流程只读；发布使用 GitHub 自动提供的 token。

Actions 固定到完整 commit SHA，Dependabot 提出版本更新 PR。

## 本地生成相同格式的包

从仓库根目录运行：

```sh
go run ./tools/release -version v0.4.1 -output dist/releases/v0.4.1
go run ./tools/release -version v0.4.1 -targets linux/amd64,linux/arm64 -output dist/linux
```

包内仅包含二进制、README、LICENSE、配置模板、部署文件、文档、USB 协议、Lua 程序和第三方许可。不包含源码测试中的虚构凭据、私有配置、联调记录、数据库或下载的厂商固件。完整源码可从仓库及 GitHub 自动生成的 source archive 获取。

Linux 解压前校验 `sha256sum --check SHA256SUMS`；tar 包保留可执行位，解压后可直接执行 `./mobilemod version`。配置模板不含可使用的 app ID、密钥、接收人或个人号码，需自行配置。
