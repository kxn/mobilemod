sys = require "sys"
local port = uart.VUART_0
local input, inbox = "", {}
local pending, sms_ready = nil, false
local sms_ready_iccid
local incoming_observed, call_active, call_started = false, false, 0
local MAX_QUEUE = 64
local JOURNAL = "/sms-gateway-inbox.jsonl"
local journal_writes = 0
local durable = fskv and fskv.init() == true
local function get(k) if durable then return fskv.get(k) end end
local function put(k,v) if durable then return fskv.set(k,v) == true end return false end
local boot_count = (get("gw_boot") or 0) + 1
put("gw_boot",boot_count)
local next_message = get("gw_next") or 0
local device_id = mobile.imei() or rtos.bsp()
local boot_id = tostring(boot_count)
local drops = get("gw_drops") or 0

-- No RNDIS, HTTP, cloud hooks, carrier queries, dialing or periodic restart.
if mobile.CONF_USB_ETHERNET then pcall(mobile.config,mobile.CONF_USB_ETHERNET,0) end
local function emit(value)
    local ok,data=pcall(json.encode,value)
    if ok and data then pcall(uart.write,port,data.."\r\n") end
end
local function api(fn,...)
    if type(fn)~="function" then return nil end
    local ok,value=pcall(fn,...)
    if ok then return value end
end
local function safe_number()
    local n=api(mobile.number)
    if type(n)=="string" and n:match("^%+?%d%d%d%d%d%d+") then return n end
end
-- FSKV uses a whole 4KB block for values above 255 bytes. Use a compact
-- append/close journal for SMS payloads and keep only small counters/intents in
-- FSKV. Acknowledgements are committed before removing an in-memory record.
local function append_record(record)
    local f=io.open(JOURNAL,"ab")
    if not f then return false end
    local ok=f:write("\n",json.encode(record),"\n")
    local closed=f:close()
    journal_writes=journal_writes+1
    return ok~=nil and closed~=nil
end
local function compact()
    local tmp=JOURNAL..".tmp"
    local f=io.open(tmp,"wb")
    if not f then return false end
    for _,event in ipairs(inbox)do
        if not f:write(json.encode(event),"\n")then f:close();return false end
    end
    if f:close()==nil then return false end
    if not os.rename(tmp,JOURNAL)then return false end
    journal_writes=0
    return true
end
do
    local records={}
    local f=io.open(JOURNAL,"rb")
    if f then
        for line in f:lines()do
            if #line>0 then
                local ok,event=pcall(json.decode,line)
                if ok and type(event)=="table"then
                    if event.event=="ack_record"then records[event.message_id]=nil
                    elseif event.message_id and (event.event=="sms_received" or event.event=="call_received")then
                        records[event.message_id]=event
                        next_message=math.max(next_message,event.message_id)
                    end
                end
            end
        end
        f:close()
    end
    for _,event in pairs(records)do inbox[#inbox+1]=event end
    table.sort(inbox,function(a,b)return a.message_id<b.message_id end)
end
local function status()
    return {event="status",protocol=3,project=PROJECT,version=VERSION,
        core=rtos.version(),bsp=rtos.bsp(),device_id=device_id,boot_id=boot_id,
        number=safe_number(),iccid=api(mobile.iccid),sim_present=api(mobile.iccid)~=nil,
        network=api(mobile.status),csq=api(mobile.csq),rsrp=api(mobile.rsrp),
        sms_ready_event_seen=sms_ready and sms_ready_iccid~=nil and sms_ready_iccid~="" and sms_ready_iccid==api(mobile.iccid),inbox_count=#inbox,durable=durable,
        drops=drops,pending_id=pending and pending.id or nil,
        capabilities={sms=true,fragments=true,persistent_inbox=durable,
            number_from_sim=type(mobile.number)=="function",guarded_send=true,incoming_calls=incoming_observed,passive_call_listener=true,
            call_api_present=type(cc)=="table" and type(cc.lastNum)=="function",call_forwarding=false,mms=false}}
end
local function valid_id(id)
    return type(id)=="string" and #id>=1 and #id<=40 and id:match("^[%w_-]+$")
end
local function save_send(id,result)
    local ids=get("gw_sends") or {}
    local key="gw_send_"..id
    if get(key)==nil then
        ids[#ids+1]=id
        if #ids>32 then fskv.del("gw_send_"..table.remove(ids,1)) end
    end
    if not put(key,json.encode(result)) then return false end
    put("gw_sends",ids)
    return true
end
local function handle(r)
    if type(r)~="table" or type(r.cmd)~="string" then emit({event="error",error="invalid request"});return end
    local id=r.id or ""
    if r.cmd=="ping" then emit({event="pong",id=id,protocol=3,version=VERSION})
    elseif r.cmd=="status" then local s=status();s.id=id;emit(s)
    elseif r.cmd=="inbox" then
        local items={}
        for i=1,math.min(#inbox,4)do items[i]=inbox[i]end
        emit({event="inbox",id=id,messages=#items>0 and items or nil})
    elseif r.cmd=="ack" then
        for i,event in ipairs(inbox)do
            if event.message_id==r.message_id and event.boot_id==r.boot_id then
                if event.durable and not append_record({event="ack_record",message_id=event.message_id})then
                    emit({event="error",id=id,error="ack persistence failed"});return
                end
                table.remove(inbox,i);break
            end
        end
        if journal_writes>32 or #inbox==0 then compact()end
        emit({event="ack",id=id})
    elseif r.cmd=="sms_send" then
        if not valid_id(id)then emit({event="error",id=id,error="invalid request id"});return end
        local cached=get("gw_send_"..id)
        if cached then local ok,v=pcall(json.decode,cached);if ok then emit(v);return end end
        if not durable then emit({event="error",id=id,error="persistent send journal unavailable"});return end
        if pending then emit({event="error",id=id,error="send pending; do not retry"});return end
        local digits=type(r.to)=="string" and r.to:gsub("^%+","")or ""
        if not digits:match("^%d+$")or #digits<5 or #digits>20 then emit({event="error",id=id,error="invalid number"});return end
        if type(r.expected_iccid)~="string" or r.expected_iccid=="" or r.expected_iccid~=api(mobile.iccid) then
            emit({event="error",id=id,error="SIM changed or expected SIM missing"});return
        end
        if not sms_ready or sms_ready_iccid~=r.expected_iccid then emit({event="error",id=id,error="SMS not ready for current SIM"});return end
        if type(r.text)~="string"or #r.text<1 or #r.text>480 then emit({event="error",id=id,error="invalid SMS text"});return end
        if not save_send(id,{event="sms_result",id=id,state="unknown",error="interrupted send; not retried"})then
            emit({event="error",id=id,error="cannot persist send intent"});return
        end
        pending={id=id,started=mcu.ticks()}
        -- Legacy cores remove '+' only with auto_phone_fix enabled. Service
        -- and national numbers must retain their exact digits.
        local international=r.to:sub(1,1)=="+"
        local ok,accepted=pcall(sms.send,r.to,r.text,international)
        if not ok or not accepted then
            pending=nil
            local v={event="sms_result",id=id,submitted=false,error="SMS API rejected request"}
            save_send(id,v);emit(v)
        else emit({event="sms_accepted",id=id})end
    else emit({event="error",id=id,error="unsupported command"})end
end
uart.setup(port,115200,8,1)
uart.on(port,"receive",function(id,length)
    input=input..(uart.read(id,length)or "")
    if #input>8192 then input="";emit({event="error",error="frame too large"});return end
    while true do
        local stop=input:find("\n",1,true)
        if not stop then break end
        local line=input:sub(1,stop-1):gsub("\r$","")
        input=input:sub(stop+1)
        if #line>0 then
            local ok,r=pcall(json.decode,line)
            if ok then
                local handled=pcall(handle,r)
                if not handled then emit({event="error",id=type(r)=="table"and r.id or nil,error="handler failed"})end
            else emit({event="error",error="invalid JSON"})end
        end
    end
end)
sms.autoLong(false)
local function enqueue(kind,number,text,metadata,occurred)
    if #inbox>=MAX_QUEUE then
        drops=drops+1;put("gw_drops",drops);emit({event="overflow",drops=drops});return
    end
    next_message=next_message+1
    put("gw_next",next_message)
    local event={event=kind,message_id=next_message,boot_id=boot_id,
        device_id=device_id,from=number,text=text,metadata=metadata,
        number=safe_number(),iccid=api(mobile.iccid),durable=durable,occurred_at=occurred}
    if durable and not append_record(event)then event.durable=false end
    inbox[#inbox+1]=event
    emit(event)
end
sms.setNewSmsCb(function(number,text,metadata)enqueue("sms_received",number,text,metadata)end)
-- Passive observation only. No cc.init/accept/dial/hangUp and no VoLTE settings.
-- Some builds publish CC_IND from mobile even when the cc Lua library is absent.
-- This listener does not prove that the modem/carrier will emit incoming events.
sys.subscribe("CC_IND",function(state,...)
    if state=="INCOMINGCALL"then
        incoming_observed=true
        local now=mcu.ticks()
        if call_active and now-call_started>=0 and now-call_started<120000 then return end
        call_active=true;call_started=now
        local caller
        for _,value in ipairs({...})do
            if type(value)=="string" and #value>=5 and #value<=21 and value:match("^%+?%d+$")then caller=value;break end
        end
        if not caller and type(cc)=="table"then
            local last=api(cc.lastNum)
            if type(last)=="string" and #last>=5 and #last<=21 and last:match("^%+?%d+$")then caller=last end
        end
        enqueue("call_received",caller or "","",nil,api(os.time))
    elseif state=="DISCONNECTED" or state=="HANGUP_CALL_DONE"then call_active=false end
end)
sys.subscribe("SMS_READY",function()sms_ready=true;sms_ready_iccid=api(mobile.iccid);emit({event="sms_ready"})end)
sys.subscribe("SMS_SENT",function(success,rp_cause,cause_text,reference,error_code)
    if not pending then return end
    local r={event="sms_result",id=pending.id,submitted=success==true,
        rp_cause=rp_cause,reference=reference,error_code=error_code}
    save_send(pending.id,r);pending=nil;emit(r)
end)
sys.timerLoopStart(function()
    if pending and not pending.timed_out and mcu.ticks()-pending.started>90000 then
        pending.timed_out=true
        local r={event="sms_result",id=pending.id,state="unknown",error="timeout; not retried"}
        save_send(pending.id,r);emit(r)
    end
end,1000)
log.info(PROJECT,VERSION,"protocol 3 ready; no automatic SMS or answering")
sys.run()
