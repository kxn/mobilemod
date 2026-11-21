# 来电通知

当前目标为 Air780EPM、V2044/106。官方 [cc 支持说明](https://docs.openluat.com/osapi/core/cc/) 和 [EPM 固件矩阵](https://docs.openluat.com/air780epm/luatos/firmware/780epm_version/) 均未提供 EPM 的 cc 库，因此无法通过公开 Lua API 初始化通话、读取 `cc.lastNum`、接听或拨号。

通用移动层会把底层通话指示发布为 CC_IND，包括 INCOMINGCALL。本程序只注册被动监听，不调用 `cc.init`、`accept`、`dial`、`hangUp`，也不修改 VoLTE 设置。底层上报时，通知进入与短信相同的持久化队列并送飞书；同一通话的重复振铃去重。来电号码使用事件中的号码或可用的 `cc.lastNum`，都没有时显示未知。

管理页“监听中，未观察到来电事件”表示本次开机尚无 INCOMINGCALL；“已观察到来电事件”表示本次开机收到过 INCOMINGCALL 事件。通知时间优先使用模块提供的有效事件时间，没有时使用电脑收到通知的时间。

联通卡和电信卡拨测时模块在线、已驻网、短信就绪，但未观察到 INCOMINGCALL；电信卡已自动读取本机号码。运营商另发的来电提醒短信按普通短信转发。终端未注册 VoLTE 语音时，网络可能给主叫返回不可达。
