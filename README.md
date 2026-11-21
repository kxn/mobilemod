# 多模块短信飞书机器人

[![CI](https://github.com/kxn/mobilemod/actions/workflows/ci.yml/badge.svg)](https://github.com/kxn/mobilemod/actions/workflows/ci.yml)
[下载发布版本](https://github.com/kxn/mobilemod/releases) · [发布流程](docs/releases.md)

Go 服务通过 USB 串口连接一个或多个 Air780EPM，把短信转发到飞书应用机器人，并通过官方 SDK 长连接接收命令。模块运行本项目的 LuatOS Lua 程序；电脑负责飞书、权限、长短信组装和持久化。不使用模块的数据上网链路。仅支持 64 位系统。

当前实机组合：Air780EPM / `LuatOS-SoC_V2044_Air780EPM_106.soc` / USB 桥接程序 0.3.1（协议 3）。这个型号官方只支持 LuatOS，不提供标准 AT 固件。[官方型号说明](https://docs.openluat.com/air780epm/)

| 能力 | 实现与边界 |
| --- | --- |
| 接收与飞书转发 | 原始分片先在模块暂存，电脑事务落盘后 ACK；飞书失败持续重试 |
| 长短信 | 电脑按模块、SIM、发送者、参考号和片数隔离组装；乱序、重复、重启恢复、缺片与冲突处理 |
| 接收号码 | SIM 自动读取 → Web 中为当前 SIM 保存的备用号码 → 模块 ID；换卡不继承旧卡的手工号码 |
| 发短信 | 按发送号码动态选卡；支持国内手机号省略 +86；保留正文连续空格、换行、末尾空格；只发短短信 |
| 引用回复 | 回复已转发的短信，发给原发送者；按原接收号码找当前模块，找不到或号码重复就拒绝 |
| Web 管理 | 支持本机或局域网访问、号码配置、飞书凭据验证和重连；无需登录、前端构建或外部 CDN |
| 来电提醒 | 被动监听 CC_IND，收到事件后持久化并转发；不自动接听 |
| 多模块 | 独立连接、状态、发送锁与发送限额；持久化绑定物理设备，串口交换不会静默发错设备 |
| USB 热插拔 | 默认自动枚举应用串口并按设备 ID 纳管；启动无模块、拔出、重新插入或端口号改变无需重启 |
| 呼叫转移 | 有查询、设置、取消命令；默认没有呼转控制 API，返回不支持；可配置运营商短信模板 |
| 彩信 | 官方 SMS API 不支持彩信；没有 WAP Push 原始载荷或彩信 APN 下载接口 |

## 运行

从 Releases 下载匹配系统和架构的包，校验 `SHA256SUMS` 后解压，包内已含 `mobilemod`（Windows 为 `mobilemod.exe`）、配置模板、部署文件和 Lua 程序，无需安装 Go。Linux 支持 `amd64` 和 `arm64`；Windows/macOS 也提供 64 位预编译包。源码构建需要 Go 1.25 以上，`go.mod` 指定的已验证工具链会自动获取。健康检查与 Web 管理默认监听本机地址，可显式改为局域网。

```powershell
go build -trimpath -o mobilemod.exe ./cmd/mobilemod
Copy-Item config.example.json config.local.json
$env:MOBILEMOD_FEISHU_APP_SECRET = '填入自己的密钥'
# 编辑 config.local.json 的 app_id 和 recipients；allowed_users 可选（仅群命令）
.\mobilemod.exe check -config config.local.json
.\mobilemod.exe devices
.\mobilemod.exe run -config config.local.json
```

Linux/macOS 使用 `./mobilemod`。数据库路径相对于配置文件目录解析。默认 `auto_discover: true`，`modules: []`，无需填写 COM 或 tty 号。程序识别 USB 应用接口，再通过桥接 status 握手读取设备 ID；新模块自动新增、拔出标记离线、换端口后按原身份恢复。`devices` 仅枚举，不会发送短信。详见 [USB 自动发现](docs/usb-discovery.md)。

插入新模块即可自动添加。启动后打开 **http://127.0.0.1:8765/**，可查看状态及设置号码。SIM 能读出号码时自动使用；读不出时，在该模块卡片填写备用号码，可省略国内手机号的 +86，保存立即生效。备用号码按 SIM 身份保存在数据库，不按 USB 端口保存；换卡不会沿用。配置文件的旧 `number` 字段仅在该模块首次识别 SIM 时导入，之后使用 Web 管理。

管理页直接打开即可查看状态、配置号码及飞书 App ID/App Secret，没有登录步骤。飞书凭据先验证再保存，成功后自动重连，页面不回显密钥。Linux 局域网部署可将 `health_listen` 设为 `0.0.0.0:8765`，再通过服务器 IP 访问。HTTP 不加密，仅建议在可信局域网直接使用；远程访问可走 SSH 隧道。详见 [管理页说明](docs/web-admin.md)。

可选在 `modules` 中填写 `id` 和 `expected_device_id` 为指定设备命名。既有模块名会沿用数据库绑定；自动模式不依赖配置中的端口号。若要手工指定串口，设置 `auto_discover: false` 并填写各模块 port。一个数据库只能运行一个实例。换 SIM 卡请先断开模块 USB 电源，换好后重新插入；USB 热插拔与带电换 SIM 不同。

示例配置从环境变量读取密钥；也支持私有配置的 `app_secret`。配置和数据库含短信、号码与凭据，必须限制为服务用户可读写，不应提交到 Git。Windows 启停脚本会为运行目录设置当前用户私有 ACL。Linux 部署示例见 [部署说明](docs/deployment.md)。

## 飞书应用设置

使用企业自建应用，启用机器人，将事件接收方式设为“使用长连接接收事件”，订阅 `im.message.receive_v1` 并发布应用版本。开通机器人发送消息、接收用户单聊消息权限；如要用群聊，还需接收群内 @ 机器人消息权限。应用可用范围必须包含使用者。

`recipients` 是首次启动时的转发目的地，可为用户 `open_id` 或群 `chat_id`，至少填一个。程序会把最近一次给机器人发单聊消息的用户自动记为转发接收人并持久化，因此更换飞书应用或用户后无需修改配置；`allowed_users` 只用于群命令鉴权，可为空。群命令同时要求用户在 `allowed_users`、群在 `allowed_chats`。普通短信正文只作为文本转发，不能成为命令。

```text
/help
/modules
/status sim-1
/status <发送号码>
/sms <发送号码> <目标号码> 这是一条短信
/forward <发送号码> query
/forward <发送号码> set unconditional <目标号码>
/forward <发送号码> cancel all
```

发送号码必须能唯一匹配一个在线模块的当前接收号码，不再用模块 ID 发送。模块 ID 保留用于显示、状态查询及本地维护。国内手机号可以带或不带 +86；短业务号码保留原样，其他国际号码保留明确的 + 国别号。正文从目标号码后的第一个分隔空白之后开始，后续字符不再 Trim；额外的前导空格、连续空格、换行及末尾空格都会参与发送和长度校验。GSM 7-bit 最多 160 单位，UCS-2 中文最多 70 字，不接受非 BMP emoji。

在飞书中直接引用回复机器人转发的短信，回复文本会作为短信正文。原 SIM 换到另一个模块后仍按号码寻找；原号码不在线、出现重复号码或发送前换卡，都会拒绝。若原消息接收号码当时未知，仅在能确认同一张 SIM 且现已配置号码时允许回复。引用消息必须有本地保存的短信关联并属于当前会话；0.4 之前没有关联的旧消息或记录已过期时，使用显式 `/sms`。

呼转类型：`unconditional`（无条件）、`busy`（遇忙）、`no_reply`（无人接听）、`unreachable`（不可及）。Air780EPM/106 没有可用的呼转控制 API，当前返回“不支持”。配置运营商确认的短信业务模板后，可按 [适配说明](docs/architecture.md) 启用 SMS 后端。

本地 `send` 命令用于停机维护测试，复用同一发送记录与限额，必须指定唯一 `-id`。服务运行时数据库独占会阻止第二个发送进程：

```text
mobilemod send -config config.local.json -module sim-1 -to <目标号码> -text 测试 -id maintenance-001
```

## 可靠性与运行边界

发送前先记录“结果未知”，模块也先保存发送意图。主机只记录最终提交结果（提交成功、拒绝或未知），不单独记录本地 API 接受；超时、重启、断线不会触发自动重发。默认每模块每小时最多 20 次提交尝试，可配置目的号码白名单。

模块队列最多 64 条事件；离线过久、存储满或掉电会丢失未保存事件。队列满时保留已有记录并报告丢弃计数。电脑数据库事务提交后才 ACK；飞书转发为至少一次，稳定 UUID 抑制重复。

被动来电事件也使用这条持久化队列。它不初始化、接听、拨号或挂断电话；只有底层上报 INCOMINGCALL 才发送通知。固件没有提供号码时显示未知，设备时间无效时使用电脑收到事件的时间。管理页“监听中，未观察到来电事件”表示本次开机尚无 INCOMINGCALL 事件，详见 [来电能力调查](docs/incoming-calls.md)。

长短信默认等 5 分钟，最多 32 片；到期按缺片消息转发。短信参考号空间有限，同一发送者短时间重用相同参考号且没有可辨冲突时，协议无法区分；已检测的冲突会显式标记，原始分片保留在本地数据库。

默认保留 30 天本地记录；未成功送到飞书的队列不会因到期删除。数据库文件删除记录后不会自动缩小；应监控磁盘，离线备份和维护。电脑网络仍走原有网络，模块 RNDIS 网卡保持禁用。

`GET http://127.0.0.1:8765/healthz` 表示进程在运行；`/readyz` 表示飞书长连接已就绪且至少一个模块短信就绪，并返回在线/配置数量。

## 开发与验证

```text
go test -race ./... -count=1 -coverprofile coverage.out
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.10.0 ./...
```

自动测试使用模拟串口和飞书服务器，不需要飞书凭据、不访问真实设备、不发收费短信；测试中的号码和标识均为虚构数据。固件测试执行实际 `gateway.lua`，模拟 Flash、重启、串口和 SMS 回调。Windows 的 race 检测需要 MinGW-w64 GCC。CI 在一个 Linux runner 上运行测试，不在多个系统重复执行。

统一跨平台打包：`go run ./tools/release -version v0.4.1`。Windows 也可使用 `scripts/build.ps1`。打包器按公开文件清单生成五个 64 位平台包及校验和。main 的 CI 跑一次测试、静态检查、Gitleaks 和漏洞检查；版本 tag 只打包发布，不重复跑整套 CI。

## 从 0.3 升级

停服务并备份数据库，然后更新二进制和随包的 Lua 脚本。发信需要协议 3 的 `guarded_send` 能力，旧桥接仍可读短信，但新版会拒绝通过它发送。新增数据库 buckets 自动创建；不要清空旧数据库、FSKV 或模块文件系统。脚本刷写方式见部署文档。Web 页面使用已有 `health_listen` 端口；清空该配置会同时关闭 Web 和健康接口。

代码划分、协议、部署和实测证据见 [架构](docs/architecture.md)、[USB 协议](sms-bridge-protocol.txt)、[部署](docs/deployment.md)、[验收记录](docs/validation.md)。固件目录的 `sys.lua` 来自 [官方 LuatOS corelib](https://github.com/openLuat/LuatOS/blob/master/script/corelib/sys.lua)，对应许可、来源与哈希保存在 `third_party/luatos`；其他依赖许可也随包附带。Lua 主程序需用官方工具编译刷入，不是复制到 U 盘。macOS 的 `devices` 仅列端口名称，避免引入 IOKit/CGO；串口收发功能保持一致。
