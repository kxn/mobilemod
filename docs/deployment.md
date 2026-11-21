# 部署与维护

准备已刷入 `sms-bridge` 程序的 Air780EPM/106、可用 SIM、电脑正常互联网连接和已发布的飞书自建机器人。无需启用模块 RNDIS 网卡或配置数据 APN。

发信需要桥接程序 0.3.1/协议 3。0.5 默认启用 USB 自动发现，模块可在程序启动之后插入，拔插或端口变化不需要重启；配置模板无需填写串口。已有数据库的模块名和号码绑定继续保留。升级 Go 服务时先停机备份数据库；从 0.4 升级无需重新刷 Lua。Web 管理默认 `http://127.0.0.1:8765/`，支持局域网绑定和管理口令；首次设置、号码及飞书配置见 [管理页说明](web-admin.md)。

## Windows

先把私有配置放到运行目录，填写密钥或在启动环境设置它：

```powershell
.\mobilemod.exe check -config .\private\config.json
.\scripts\start.ps1 -Binary .\mobilemod.exe -Config .\private\config.json -RunDirectory .\private
Invoke-RestMethod http://127.0.0.1:8765/readyz
.\scripts\stop.ps1 -RunDirectory .\private
```

脚本隐藏运行窗口，日志和 PID 保存于运行目录；停止时核对 PID 对应的可执行路径，避免误杀复用 PID 的程序。运行目录 ACL 只授权当前用户。Windows 停止是进程终止，持久化发送意图和任务恢复策略处理这种情况。需要随登录启动时，可由部署管理员设置登录任务；项目不会自动修改系统启动项。

COM 号可能随 USB 插口变化。用设备管理器核对 `MI_06` 用户串口，或 `mobilemod devices` 枚举。端口变化只修改 `port`，保持模块 `id`。主程序会再次检查物理绑定。

## Linux systemd

路径与 [mobilemod.service](../deploy/mobilemod.service) 一致：

1. 创建专用无登录账户 `mobilemod`，加入串口访问组（通常 `dialout`），准备 `/var/lib/mobilemod` 为该用户独占。
2. 安装二进制至 `/opt/mobilemod/mobilemod`；配置 `/etc/mobilemod/config.json`，数据库设为 `/var/lib/mobilemod/gateway.db`，端口使用 `/dev/serial/by-id/...` 稳定路径。
3. `/etc/mobilemod/environment` 写入 `MOBILEMOD_FEISHU_APP_SECRET=自己的密钥`，权限 0600；配置权限 0640，组为 mobilemod。
4. 安装 unit，执行 `systemctl daemon-reload`、`systemctl enable --now mobilemod`。
5. 查看 `journalctl -u mobilemod`、本机 `/readyz` 和飞书 `/status`；检查所有模块数量。

服务需要串口访问和出站 HTTPS/WebSocket，不需要 root。串口组名不同时修改 unit。`ProtectHome=true`，生产配置和数据库不要放在用户家目录。

## 固件更新

从合宙官方下载固件与 CLI，保留下载校验值。V2044 的 106 组合包含电信短信支持。[官方固件矩阵](https://docs.openluat.com/air780epm/luatos/firmware/780epm_version/)

本次验证的 [V2044/106 官方 core 下载](https://cdn18.luatos.com/files/Air780EPM/LuatOS_Air780EPM/LuatOS-SoC_V2044_Air780EPM/LuatOS-SoC_V2044_Air780EPM_106.soc)。全新设备先用 CLI 的 `flash run --soc ... --script sms-bridge --port auto --luac-debug 1` 刷 core 和程序；现有设备按下面方式只更新脚本。不要添加 `--clear-kv`。

已有 core 可仅更新脚本，先停主服务：

```text
luatos-cli --format jsonl flash script --soc LuatOS-SoC_V2044_Air780EPM_106.soc --port auto --script sms-bridge --luac-debug 1
```

刷写时只连接目标模块，防止 `auto` 选到其他模块。不要清除 FSKV 或用户文件，其中保存未 ACK 短信和发送意图。刷新后检查协议、SIM、号码、短信就绪和飞书连接。刷 core 或清空用户分区可能删除记录，须先处理队列并备份。

## 备份、升级与故障

停服务后复制整个数据库，替换二进制再启动。不要普通文件热拷贝运行中数据库；当前没有在线快照命令。备份也含短信和号码，沿用私有权限。

“发送结果未知”表示请求可能已提交，不会自动重发；`submitted` 表示短信中心已接受。模块超时后保持忙状态，等待回调或管理员检查后重启。飞书事件按消息 ID 去重；超过五分钟的离线/排队命令不执行。

飞书断线时接收和本地落盘继续，通知等待恢复。电脑离线但模块有供电时只保留有限 64 片。存储告警时检查磁盘、USB 和模块 Flash；不要删数据库解决，这会丢失防重记录及未投递通知。

正常日志不含正文和密钥，数据库为明文。应关注磁盘余量、日志轮转、USB 供电与短信就绪状态。可在 Web 中验证并修改飞书密钥，成功后立即保存并重连，无需重新刷模块；Web 凭据优先于文件/环境变量。管理口令也在 Web 修改，忘记口令时应由服务器管理员离线恢复数据库备份，不能由未登录访客重置。
