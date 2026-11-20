# 多模块短信飞书机器人

[![CI](https://github.com/kxn/mobilemod/actions/workflows/ci.yml/badge.svg)](https://github.com/kxn/mobilemod/actions/workflows/ci.yml)
[下载发布版本](https://github.com/kxn/mobilemod/releases) · [发布流程](docs/releases.md)

Go 服务通过 USB 串口连接一个或多个 Air780EPM，把短信转发到飞书应用机器人，并通过官方 SDK 长连接接收命令。模块运行本项目的 LuatOS Lua 程序；电脑负责飞书、权限、长短信组装和持久化。不使用模块的数据上网链路。仅支持 64 位系统。

当前实机组合：Air780EPM / `LuatOS-SoC_V2044_Air780EPM_106.soc` / USB 桥接程序 0.2.0。这个型号官方只支持 LuatOS，不提供标准 AT 固件。[官方型号说明](https://docs.openluat.com/air780epm/)

| 能力 | 实现与边界 |
| --- | --- |
| 接收与飞书转发 | 原始分片先在模块暂存，电脑事务落盘后 ACK；飞书失败持续重试 |
| 长短信 | 电脑按模块、SIM、发送者、参考号和片数隔离组装；乱序、重复、重启恢复、缺片与冲突处理 |
| 接收号码 | SIM API 实际号码 → 配置 `number` → 模块 `id`；每条消息同时标明发送者、接收号码和模块 |
| 发短信 | 飞书命令发送一条短短信；GSM 7-bit 最多 160 单位，中文 UCS-2 最多 70 字；不接受 emoji 等非 BMP 字符 |
| 多模块 | 独立连接、状态、发送锁与发送限额；持久化绑定物理设备，串口交换不会静默发错设备 |
| 呼叫转移 | 有查询、设置、取消命令及运营商短信适配器；**当前 EPM/106 无可用的呼转控制 API，默认明确拒绝**，不能承诺用拨号代码实现 |
| 彩信 | **当前官方 SMS API 不支持彩信**。没有 WAP Push 原始载荷或彩信 APN 下载接口；未来驱动如提供通知会保留并转发，但本固件无法保证收到通知或下载附件 |

## 运行

从 Releases 下载匹配系统和架构的包，校验 `SHA256SUMS` 后解压，包内已含 `mobilemod`（Windows 为 `mobilemod.exe`）、配置模板、部署文件和 Lua 程序，无需安装 Go。Linux 支持 `amd64` 和 `arm64`；Windows/macOS 也提供 64 位预编译包。源码构建需要 Go 1.25 以上，`go.mod` 指定的已验证工具链会自动获取。服务只在本机监听健康检查，不开放外部控制端口。

```powershell
go build -trimpath -o mobilemod.exe ./cmd/mobilemod
Copy-Item config.example.json config.local.json
$env:MOBILEMOD_FEISHU_APP_SECRET = '填入自己的密钥'
# 编辑 config.local.json 的 app_id、allowed_users、recipients 和串口
.\mobilemod.exe check -config config.local.json
.\mobilemod.exe devices
.\mobilemod.exe run -config config.local.json
```

Linux/macOS 使用 `./mobilemod`。数据库路径相对于配置文件目录解析；Linux 端口应使用 `/dev/serial/by-id/...` 等稳定路径。Windows 使用 USB `MI_06` 的用户串口，COM 号以本机枚举结果为准；其他日志口不是短信控制端口。`devices` 仅枚举，不会试探或发送短信。

复制 `modules` 项即可添加模块，`id` 和串口必须各不相同。`number` 留空会优先自动读取；如 SIM 没写号码，填入完整号码。可选 `expected_device_id` 填 IMEI 作额外绑定检查。一个数据库只能运行一个实例；换物理设备或改模块 ID 应停机备份数据库并进行受控迁移，不要直接把旧串口指向别的模块。

示例配置从环境变量读取密钥；也支持私有配置的 `app_secret`。配置和数据库含短信、号码与凭据，必须限制为服务用户可读写，不应提交到 Git。Windows 启停脚本会为运行目录设置当前用户私有 ACL。Linux 部署示例见 [部署说明](docs/deployment.md)。

## 飞书应用设置

使用企业自建应用，启用机器人，将事件接收方式设为“使用长连接接收事件”，订阅 `im.message.receive_v1` 并发布应用版本。开通机器人发送消息、接收用户单聊消息权限；如要用群聊，还需接收群内 @ 机器人消息权限。应用可用范围必须包含使用者。

`allowed_users` 填允许控制设备的用户 **open_id**，不能为空。`recipients` 指定转发目的地，可为用户 `open_id` 或群 `chat_id`；这与控制权限分开配置。群命令同时要求用户在 `allowed_users`、群在 `allowed_chats`。默认只允许白名单用户单聊控制。普通短信正文只作为文本转发，不能成为命令。

```text
/help
/modules
/status sim-1
/sms sim-1 <目标号码> 这是一条短信
/forward sim-1 query
/forward sim-1 set unconditional <目标号码>
/forward sim-1 cancel all
```

呼转类型：`unconditional`（无条件）、`busy`（遇忙）、`no_reply`（无人接听）、`unreachable`（不可及）。当前配置会明确回复“不支持”，不会拨号或假报成功。只有拿到运营商确认可用的短信业务模板后，才能按 [适配说明](docs/architecture.md) 开启 SMS 后端；业务状态以运营商回复为准。

本地 `send` 命令用于停机维护测试，复用同一发送记录与限额，必须指定唯一 `-id`。服务运行时数据库独占会阻止第二个发送进程：

```text
mobilemod send -config config.local.json -module sim-1 -to <目标号码> -text 测试 -id maintenance-001
```

## 可靠性与运行边界

发送前先记录“结果未知”，模块也先保存发送意图。API 接受、短信中心提交成功和手机送达是不同状态；本固件没有可用的送达回执。超时、重启、断线不会触发自动重发，须检查手机和发送记录后自行决定是否发一条新请求。默认每模块每小时最多 20 次提交尝试，可配置目的号码白名单。

模块暂存最多 64 个分片，离线时间过长、存储满或掉电期间收到的消息仍可能丢失。队列满时保留已有记录并报告丢弃计数；模块 Flash 没有企业级掉电保证。电脑数据库事务提交后才 ACK；飞书转发为至少一次，稳定 UUID 尽量抑制重复，但平台去重窗口外仍可能重复。消息 ID 便于核对。

长短信默认等 5 分钟，最多 32 片；到期按缺片消息转发。短信参考号空间有限，同一发送者在短时间重用相同参考号且没有可辨冲突时，协议本身不能完全区分，不能声称绝对不会误并。已检测的冲突会显式标记，原始分片保留在本地数据库。

默认保留 30 天本地记录；未成功送到飞书的队列不会因到期删除。数据库文件删除记录后不会自动缩小；应监控磁盘，离线备份和维护。电脑网络仍走原有网络，模块 RNDIS 网卡保持禁用。

`GET http://127.0.0.1:8765/healthz` 表示进程在运行；`/readyz` 表示飞书长连接已就绪且至少一个模块短信就绪，并返回在线/配置数量。它不代表所有模块健康，也不表示运营商已送达短信。

## 开发与验证

```text
go test ./... -count=1 -coverprofile=coverage.out
go test -race ./... -count=1
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.10.0 ./...
```

自动测试使用模拟串口和飞书服务器，不需要飞书凭据、不访问真实设备、不发收费短信；测试中的号码和标识均为虚构数据。固件测试执行实际 `gateway.lua`，模拟 Flash、重启、串口和 SMS 回调。Windows 的 race 检测需要 MinGW-w64 GCC；Linux CI 在原生 amd64/arm64 执行 race，同时测试 Windows/macOS。

统一跨平台打包：`go run ./tools/release -version v0.3.0`。Windows 也可使用 `scripts/build.ps1`。打包器通过明确的公开文件清单组包，保留 Linux 可执行权限，生成五个 64 位平台包及校验和；不会递归打包工作区或带入私有配置。CI 对 Git 全历史执行 Gitleaks，额外检查个人号码、飞书标识和私有文件路径，tag 发版需整套 CI 通过。

代码划分、协议、部署和实测证据见 [架构](docs/architecture.md)、[USB 协议](sms-bridge-protocol.txt)、[部署](docs/deployment.md)、[验收记录](docs/validation.md)。固件目录的 `sys.lua` 来自 [官方 LuatOS corelib](https://github.com/openLuat/LuatOS/blob/master/script/corelib/sys.lua)，对应许可、来源与哈希保存在 `third_party/luatos`；其他依赖许可也随包附带。Lua 主程序需用官方工具编译刷入，不是复制到 U 盘。macOS 的 `devices` 仅列端口名称，避免引入 IOKit/CGO；串口收发功能保持一致。
