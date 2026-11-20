'use strict';
const cards = new Map();
let busy = false;
let nextCard = 0;
let setupRequired = false;
let feishuDirty = false;
const el = (tag, cls, text) => {
  const node = document.createElement(tag);
  if (cls) node.className = cls;
  if (text !== undefined) node.textContent = text;
  return node;
};
const setNotice = text => { document.querySelector('#notice').textContent = text; };
function makeCard(id) {
  const card = el('article', 'module');
  const head = el('div', 'module-head');
  const title = el('div');
  title.append(el('h3', '', id));
  const port = el('div', 'port'); title.append(port);
  const badge = el('span', 'badge');head.append(title, badge);
  const details = el('div', 'details');
  const number = el('div', 'number');const source = el('div', 'source');
  const facts = el('div', 'facts');
  const values = {};
  for (const [key, label] of [['network','网络注册'], ['signal','信号 CSQ'], ['sms','短信发送'], ['calls','来电通知']]) {
    const fact = el('div', 'fact'); const value = el('span');fact.append(el('span','',label),value);facts.append(fact);values[key]=value;
  }
  const form = el('form');const input = el('input');
  const inputId = 'number-' + nextCard++;input.id=inputId;input.type='tel';input.placeholder='无法自动读取时填写';input.maxLength=24;input.autocomplete='off';
  const label=el('label','','手工备用号码');label.htmlFor=inputId;
  const save=el('button','save','保存');save.type='submit';
  const row=el('div','input-row');row.append(input,save);
  const hint=el('p','small','留空可清除手工号码。自动读取正常时，备用号码不参与选卡。');
  const message=el('div','message');message.setAttribute('role','status');
  form.append(label,row,hint,message);details.append(number,source,facts,form);card.append(head,details);
  const state={card,port,badge,number,source,values,input,save,message,sim:null,dirty:false,saving:false};
  input.addEventListener('input',()=>{state.dirty=true;});
  form.addEventListener('submit',async event=>{
    event.preventDefault();if(state.saving)return;state.saving=true;save.disabled=true;message.textContent='正在保存…';
    try {
      const response=await fetch('/api/modules/'+encodeURIComponent(id)+'/number',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({sim_key:state.sim,number:input.value})});
      if(!response.ok)throw new Error(await response.text());
      state.dirty=false;message.textContent='已保存到当前 SIM';message.className='message ok';
      await refresh();
    }catch(error){message.textContent=error.message;message.className='message warn';}
    finally{state.saving=false;save.disabled=input.disabled;}
  });
  return state;
}
function render(status){
  const online=status.modules.filter(m=>m.online).length;
  const feishu=document.querySelector('#feishu');feishu.textContent=status.feishu?'已连接':'未连接';feishu.className=status.feishu?'ok':'off';
  document.querySelector('#online').textContent=online+' / '+status.modules.length;
  document.querySelector('#pending').textContent=String(status.pending_notifications);
  if(!feishuDirty)document.querySelector('#feishu-app-id').value=status.feishu_app_id||'';
  document.querySelector('#feishu-secret-state').textContent=status.feishu_secret_set?'已配置密钥；页面不会回显。更换 App ID 时须填写新密钥。':'尚未配置密钥';
  const container=document.querySelector('#modules');if(!cards.size)container.replaceChildren();
  const active=new Set(status.modules.map(m=>m.id));
  for(const [id,c] of cards){if(!active.has(id)){c.card.remove();cards.delete(id);}}
  const numberCounts=new Map();for(const m of status.modules){if(m.online&&m.number_source!=='module_id')numberCounts.set(m.receiver,(numberCounts.get(m.receiver)||0)+1);}
  for(const m of status.modules){
    let c=cards.get(m.id);if(!c){c=makeCard(m.id);cards.set(m.id,c);container.append(c.card);}
    const changed=c.sim!==m.sim_key;
    if(changed){if(c.sim!==null&&c.dirty){c.message.textContent='SIM 已变化，未保存的号码已清除';c.message.className='message warn';}c.sim=m.sim_key;c.dirty=false;}
    c.port.textContent=m.port+' · '+(m.core||'等待固件状态');
    c.badge.textContent=m.online?'在线':'离线';c.badge.className='badge'+(m.online?'':' offline');
    c.number.textContent=m.number_source==='module_id'?'号码待设置':m.receiver;
    const sources={sim:'由 SIM 自动读取',manual:'使用手工备用号码',module_id:'尚无接收号码，短信会标注模块 ID',unavailable:'号码存储暂不可用'};
    c.source.textContent=sources[m.number_source]||'—';
    if(numberCounts.get(m.receiver)>1){c.source.textContent+=' · 号码重复，发送会被拒绝';c.source.className='source warn';}else c.source.className='source';
    c.values.network.textContent=({1:'已注册',5:'已注册（漫游）'})[m.network]||'等待注册';
    c.values.signal.textContent=m.online?String(m.csq):'—';
    c.values.sms.textContent=!m.guarded_send?'需升级桥接程序':(m.sms_ready?'已就绪':'等待就绪');
    c.values.calls.textContent=m.incoming_calls?'已观察到来电事件':(m.passive_calls?'监听中，未观察到来电事件':'未启用被动监听');
    if(!c.dirty&&!c.saving)c.input.value=m.manual_number||'';
    c.input.disabled=!m.online||!m.sim_key||m.sim_key==='unknown';
    c.save.disabled=c.input.disabled||c.saving;
  }
  document.querySelector('#updated').textContent='更新于 '+new Date().toLocaleTimeString('zh-CN');
}
async function refresh(){
  if(busy)return;busy=true;
  try{
    const authResponse=await fetch('/api/auth',{cache:'no-store'});
    if(!authResponse.ok)throw new Error('暂时无法连接管理界面');
    const auth=await authResponse.json();setupRequired=auth.setup_required;
    document.querySelector('#authentication').hidden=auth.authenticated;
    document.querySelector('#gateway').hidden=!auth.authenticated;
    document.querySelector('#logout').hidden=!auth.authenticated;
    if(!auth.authenticated){
      document.querySelector('#setup-code-row').hidden=!setupRequired;
      document.querySelector('#setup-code').required=setupRequired;
      document.querySelector('#auth-title').textContent=setupRequired?'设置管理口令':'登录管理界面';
      document.querySelector('#auth-description').textContent=setupRequired?'使用服务器的一次性初始化码，为管理界面设置口令。':'输入管理口令以查看状态和修改设置。';
      document.querySelector('#login-submit').textContent=setupRequired?'设置并登录':'登录';
      document.querySelector('#login-password').minLength=setupRequired?12:1;
      document.querySelector('#login-password').autocomplete=setupRequired?'new-password':'current-password';
      document.querySelector('#updated').textContent=setupRequired?'等待初始化':'尚未登录';
      cards.clear();document.querySelector('#modules').replaceChildren();feishuDirty=false;
      document.querySelector('#feishu-app-id').value='';document.querySelector('#feishu-secret').value='';
      return;
    }
    const response=await fetch('/api/status',{cache:'no-store'});if(!response.ok)throw new Error('暂时无法读取网关状态');render(await response.json());
  }
  catch(error){setNotice(error.message);}
  finally{busy=false;}
}

async function mutate(path,method,body){
  const response=await fetch(path,{method,headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
  if(!response.ok)throw new Error((await response.text()).trim()||'请求失败');
}
document.querySelector('#auth-form').addEventListener('submit',async event=>{
  event.preventDefault();const button=document.querySelector('#login-submit');button.disabled=true;
  try{
    await mutate('/api/auth/'+(setupRequired?'setup':'login'),'POST',{password:document.querySelector('#login-password').value,setup_code:document.querySelector('#setup-code').value.trim()});
    document.querySelector('#login-password').value='';document.querySelector('#setup-code').value='';setNotice('');await refresh();
  }catch(error){setNotice(error.message);}finally{button.disabled=false;}
});
document.querySelector('#logout').addEventListener('click',async()=>{
  try{await mutate('/api/auth/logout','POST',{});await refresh();setNotice('已退出登录');}catch(error){setNotice(error.message);}
});
document.querySelector('#feishu-app-id').addEventListener('input',()=>{feishuDirty=true;});
document.querySelector('#feishu-form').addEventListener('submit',async event=>{
  event.preventDefault();const button=event.target.querySelector('button');const message=document.querySelector('#feishu-message');button.disabled=true;message.textContent='正在验证…';
  try{
    await mutate('/api/feishu','PUT',{app_id:document.querySelector('#feishu-app-id').value,app_secret:document.querySelector('#feishu-secret').value});
    document.querySelector('#feishu-secret').value='';feishuDirty=false;message.textContent='已保存，正在重新连接飞书';message.className='message ok';await refresh();
  }catch(error){message.textContent=error.message;message.className='message warn';}finally{button.disabled=false;}
});
document.querySelector('#password-form').addEventListener('submit',async event=>{
  event.preventDefault();const message=document.querySelector('#password-message');const password=document.querySelector('#new-password').value;
  if(password!==document.querySelector('#confirm-password').value){message.textContent='两次输入的新口令不一致';message.className='message warn';return;}
  const button=event.target.querySelector('button');button.disabled=true;
  try{
    await mutate('/api/auth/password','PUT',{old_password:document.querySelector('#old-password').value,password});
    event.target.reset();message.textContent='口令已更新，其他登录已失效';message.className='message ok';
  }catch(error){message.textContent=error.message;message.className='message warn';}finally{button.disabled=false;}
});
document.querySelector('#refresh').addEventListener('click',refresh);
refresh();setInterval(refresh,5000);
