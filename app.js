'use strict';
const $ = s => document.querySelector(s);
const state = {csrf:'',users:[],page:'overview',filter:'',userFilter:'',activityFilter:'',activitySearch:'',usersLoadedAt:0,lastFullRefresh:0,edit:null,refreshing:false,pagination:{users:{page:1,size:6},usage:{page:1,size:6},activity:{page:1,size:25,anchor:null},'egress-records':{page:1,size:25,anchor:null}},activityRequest:0};
const titles = {overview:'运行概览','egress-dashboard':'出口流量大盘','proxy-dashboard':'代理流量大盘',users:'用户与订阅',activity:'访问记录',egress:'电脑出口','egress-records':'电脑出口详细记录',settings:'管理设置'};
const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
function bytes(value){value=Number(value)||0;if(value<1024)return Math.round(value)+' B';const n=Math.min(Math.floor(Math.log(value)/Math.log(1024)),4);return (value/1024**n).toFixed(n>1?2:1)+' '+['B','KB','MB','GB','TB'][n];}
function date(value){return value?new Date(value*1000).toLocaleString('zh-CN',{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false}):'—';}
let toastTimer;
function toast(message){$('#toast').textContent=message;$('#toast').classList.add('visible');clearTimeout(toastTimer);toastTimer=setTimeout(()=>$('#toast').classList.remove('visible'),4000);}
function showLogin(){state.csrf='';$('#app').classList.add('hidden');$('#login').classList.remove('hidden');}
async function api(path,options={}){const response=await fetch('/api'+path,{...options,signal:options.signal||AbortSignal.timeout(options.method?60000:10000),headers:{'Content-Type':'application/json','X-CSRF-Token':state.csrf,...options.headers}});let data;try{data=await response.json();}catch{throw new Error('服务器暂时无法响应');}if(!response.ok){if(response.status===401&&path!='/login')showLogin();const message=Array.isArray(data.detail)?data.detail.map(x=>x.msg).join('；'):data.detail;throw new Error(message||'请求失败，请稍后重试');}return data;}
async function busy(button,work){if(button.disabled)return;button.disabled=true;try{return await work();}catch(error){toast(error.message);}finally{button.disabled=false;}}
function page(name){state.page=name;$('#app').classList.toggle('records-view',name==='egress-records');document.querySelectorAll('.page').forEach(x=>x.classList.add('hidden'));$('#page-'+name).classList.remove('hidden');document.querySelectorAll('.nav-item').forEach(x=>x.classList.toggle('active',x.dataset.page===name));document.title='TreeRoot · '+titles[name];if(name==='proxy-dashboard'&&trafficChart)requestAnimationFrame(()=>trafficChart.resize());refresh();}
function badge(enabled){return `<span class="badge ${enabled?'':'off'}"><i class="dot"></i>${enabled?'已启用':'已禁用'}</span>`;}
function avatar(name){return `<span class="mini-avatar">${esc(name.slice(0,1))}</span>`;}
function pagination(name,total,pages){
  const paging=state.pagination[name];
  paging.pages=pages;
  const records=name==='activity'||name==='egress-records',sizes=records?[25,50,100]:[6,12,24];
  $('#'+name+'-pagination').innerHTML=`<span class="pagination-summary" aria-live="polite">共 ${total.toLocaleString()} ${records?'条':'位用户'}</span><div class="pagination-controls"><select data-page-size="${name}" aria-label="每页数量">${sizes.map(size=>`<option value="${size}" ${size===paging.size?'selected':''}>${size} / 页</option>`).join('')}</select><button class="button subtle" data-pagination="${name}" data-step="-1" ${paging.page<=1?'disabled':''} aria-label="上一页">上一页</button><span class="page-count" aria-live="polite">${paging.page} / ${pages}</span><button class="button subtle" data-pagination="${name}" data-step="1" ${paging.page>=pages?'disabled':''} aria-label="下一页">下一页</button></div>`;
}
function cardPage(name,users){const paging=state.pagination[name];const pages=Math.max(1,Math.ceil(users.length/paging.size));paging.page=Math.min(paging.page,pages);pagination(name,users.length,pages);return users.slice((paging.page-1)*paging.size,paging.page*paging.size);}
function renderUserOptions(){
  const options='<option value="">全部用户</option>'+state.users.map(u=>`<option value="${u.id}">${esc(u.name)}</option>`).join('');
  for(const selector of ['#overview-user-filter','#users-user-filter','#activity-user-filter']){const select=$(selector);if(select.innerHTML!==options){const draft=select.value;select.innerHTML=options;select.value=draft;}}
}
function renderUsers(){
  const selected=state.page==='users'?state.userFilter:state.filter;
  const visible=state.users.filter(u=>!selected||u.id===Number(selected));
  if(state.page==='users'){
  $('#user-cards').innerHTML=cardPage('users',visible).map(u=>`<article class="user-card"><div class="user-card-head"><div class="user-card-name">${avatar(u.name)}<div><h2>${esc(u.name)}</h2><small>${date(u.created)}</small></div></div>${badge(u.enabled)}</div><div class="user-stats"><div><span>累计流量</span><b>${bytes(u.upload+u.download)}</b></div><div><span>访问域名</span><b>${u.domains}</b></div><div><span>累计连接</span><b>${u.connections.toLocaleString()}</b></div></div><div class="user-card-actions"><button class="button primary" data-action="subscription" data-id="${u.id}">订阅链接</button><button class="button subtle" data-action="view" data-id="${u.id}">查看统计</button><button class="text-button" data-action="rename" data-id="${u.id}">重命名</button><button class="text-button" data-action="toggle" data-id="${u.id}">${u.enabled?'禁用':'启用'}</button><button class="text-button" data-action="reset" data-id="${u.id}">重置订阅</button></div></article>`).join('')||'<div class="empty">暂无用户</div>';
  }
  if(state.page==='proxy-dashboard'){
  $('#overview-user-filter').value=state.filter;
  $('#usage-title').textContent=selected?(state.users.find(u=>String(u.id)===selected)?.name||'用户')+' · 使用情况':'用户使用情况';
  $('#overview-reset').classList.toggle('hidden',!selected);
  $('#usage-cards').innerHTML=cardPage('usage',visible).map(u=>`<article class="usage-card"><div class="user-card-head"><div class="user-card-name">${avatar(u.name)}<h3>${esc(u.name)}</h3></div>${badge(u.enabled)}</div><div class="user-stats"><div><span>上传</span><b>${bytes(u.upload)}</b></div><div><span>下载</span><b>${bytes(u.download)}</b></div><div><span>访问域名</span><b>${u.domains.toLocaleString()}</b></div><div><span>累计连接</span><b>${u.connections.toLocaleString()}</b></div></div></article>`).join('')||'<div class="empty">暂无用户</div>';
  }
}
let trafficChart;
function renderTraffic(points,target='traffic-chart',hours=24){
  if((target==='traffic-chart'&&state.page!=='proxy-dashboard')||(target==='egress-traffic-chart'&&state.page!=='egress-dashboard'))return;
  const element=$('#'+target),chart=echarts.getInstanceByDom(element)||echarts.init(element,null,{renderer:'svg'});
  if(target==='traffic-chart')trafficChart=chart;
  const axis={show:true,lineStyle:{color:'#3a4a63',width:0.5}};
  chart.setOption({
    animation:!matchMedia('(prefers-reduced-motion: reduce)').matches,
    color:['#729dff','#5addc0'],textStyle:{fontFamily:'-apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif'},
    grid:{left:16,right:22,top:42,bottom:18,containLabel:true},
    legend:{top:0,right:18,itemWidth:12,itemHeight:6,textStyle:{color:'#98a8c1',fontSize:10}},
    tooltip:{trigger:'axis',backgroundColor:'#152238',borderColor:'#354762',textStyle:{color:'#edf2fb',fontSize:12},valueFormatter:value=>bytes(value)},
    xAxis:{type:'category',boundaryGap:false,data:points.map(p=>new Date(p.ts*1000).toLocaleString('zh-CN',{...(hours>24?{month:'2-digit',day:'2-digit'}:{}),hour:'2-digit',minute:'2-digit',hour12:false})),axisLine:axis,axisTick:{show:false},axisLabel:{color:'#98a8c1',fontSize:10,hideOverlap:true}},
    yAxis:{type:'value',min:0,axisLine:axis,axisTick:{show:false},axisLabel:{color:'#98a8c1',fontSize:10,formatter:value=>bytes(value)},splitLine:{lineStyle:{color:'#26344a',width:0.5}}},
    series:[{name:'下载',type:'line',data:points.map(p=>p.download),showSymbol:false,lineStyle:{width:1.5},areaStyle:{opacity:0.08}},{name:'上传',type:'line',data:points.map(p=>p.upload),showSymbol:false,lineStyle:{width:1.5},areaStyle:{opacity:0.04}}]
  });
}
new ResizeObserver(()=>{if(trafficChart&&$('#traffic-chart').clientWidth)trafficChart.resize();}).observe($('#traffic-chart'));
function renderOverview(data){
  $('#total-traffic').textContent=bytes(data.upload+data.download);$('#total-breakdown').textContent=`↑ ${bytes(data.upload)} · ↓ ${bytes(data.download)}`;$('#day-traffic').textContent=bytes(data.day_upload+data.day_download);$('#active-users').textContent=data.active+' / '+data.users;$('#connections').textContent=data.connections.toLocaleString();
  renderRates(data);
  const now=Math.floor(Date.now()/3600000)*3600;const hourly=new Map(data.series.map(x=>[x.ts,x]));const points=Array.from({length:24},(_,i)=>hourly.get(now-(23-i)*3600)||{ts:now-(23-i)*3600,upload:0,download:0});renderTraffic(points);$('#traffic-chart').setAttribute('aria-label',`最近24小时上传${bytes(data.day_upload)}，下载${bytes(data.day_download)}`);
  const top=data.domains[0]?.connections||1;$('#domain-list').innerHTML=data.domains.length?data.domains.map(d=>`<div class="domain-row"><div class="domain-name"><span title="${esc(d.domain)}">${esc(d.domain)}</span><span>${d.connections.toLocaleString()} 次</span></div><div class="domain-track"><div class="domain-fill" style="width:${d.connections/top*100}%"></div></div></div>`).join(''):'<div class="empty">暂无记录</div>';
}
function renderRates(data){
  $('#speed-upload').textContent=data.speed_upload==null?'—':bytes(data.speed_upload)+'/s';$('#speed-download').textContent=data.speed_download==null?'—':bytes(data.speed_download)+'/s';
  const latest=data.collector.last_poll,healthy=latest&&Date.now()/1000-latest<30&&!data.collector.last_error;$('#health').classList.toggle('warning',!healthy);$('#health-text').textContent=healthy?'统计采集中':'等待采集恢复';
}
function renderResources(data){
  for(const [name,percent,note] of [
    ['cpu',data.cpu_percent,data.load_average?'1 分钟负载 '+data.load_average[0].toFixed(2):'负载不可用'],
    ['memory',data.memory?.percent,data.memory?`${bytes(data.memory.used)} / ${bytes(data.memory.total)}`:'暂不可用'],
    ['disk',data.disk?.percent,data.disk?`${bytes(data.disk.used)} / ${bytes(data.disk.total)}`:'暂不可用']
  ]){
    $('#'+name+'-usage').textContent=percent==null?'—':percent.toFixed(1)+'%';
    const meter=$('#'+name+'-meter');meter.hidden=percent==null;meter.value=percent??0;
    $('#'+name+'-note').textContent=note;
  }
  $('#cpu-cores').textContent=data.cpu_cores?data.cpu_cores+' 核':'—';
  const uptime=data.uptime_seconds;
  $('#server-uptime').textContent=uptime==null?'—':uptime>=86400?`${Math.floor(uptime/86400)} 天 ${Math.floor(uptime%86400/3600)} 小时`:uptime>=3600?`${Math.floor(uptime/3600)} 小时 ${Math.floor(uptime%3600/60)} 分`:Math.floor(uptime/60)+' 分钟';
  const unavailable=data.cpu_percent==null||!data.memory||!data.disk||uptime==null;
  $('#resource-status').classList.toggle('warning',unavailable);
  $('#resource-status').textContent=unavailable?'部分数据等待采样 · 每 5 秒刷新':'每 5 秒刷新 · '+new Date(data.sampled_at*1000).toLocaleTimeString('zh-CN',{hour12:false});
}
async function refreshResources(){
  if(!state.csrf||state.page!=='overview'||state.resourcesRefreshing)return;
  state.resourcesRefreshing=true;
  try{renderResources(await api('/resources'));}catch(error){if(state.csrf){$('#resource-status').textContent='更新失败，正在重试';$('#resource-status').classList.add('warning');}}finally{state.resourcesRefreshing=false;}
}
async function refreshRates(){
  if(!state.csrf||state.ratesRefreshing)return;
  state.ratesRefreshing=true;
  const filter=state.page==='proxy-dashboard'?state.filter:'';
  try{const data=await api('/rates'+(filter?'?user_id='+filter:''));if(filter===(state.page==='proxy-dashboard'?state.filter:''))renderRates(data);}catch(error){if(state.csrf){$('#health').classList.add('warning');$('#health-text').textContent='连接异常';}}finally{state.ratesRefreshing=false;}
}
async function refreshServerInformation(){
  if(state.page!=='overview')return;
  try{
    const data=await api('/server');if(state.page!=='overview')return;
    const fields=[['主机名',data.hostname],['操作系统',data.os],['内核版本',data.kernel],['系统架构',data.architecture],['CPU 型号',data.cpu_model],['服务器时区',data.timezone],['网站域名',data.domain],['代理协议',data.protocol],['Python 版本',data.python],['FastAPI / uvicorn',[data.versions.fastapi,data.versions.uvicorn].filter(Boolean).join(' / ')],['系统进程数',data.processes],['数据库大小',data.database_bytes==null?null:bytes(data.database_bytes)]];
    $('#server-details').innerHTML=fields.map(([name,value])=>`<div class="server-detail"><span>${esc(name)}</span><strong>${esc(value??'暂不可用')}</strong></div>`).join('');
    const names={'xray.service':'Xray 代理','caddy.service':'Caddy HTTPS','treeroot-panel.service':'TreeRoot 面板','treeroot-backup.timer':'每日备份定时器'};
    $('#server-services').innerHTML=data.services.length?'<table><thead><tr><th>服务</th><th>状态</th><th>进程</th><th>内存</th></tr></thead><tbody>'+data.services.map(item=>`<tr><td>${esc(names[item.name]||item.name)}<small class="service-start">${esc(item.started_at||'')}</small></td><td><span class="badge ${item.state==='active'?'':'off'}"><i class="dot"></i>${item.state==='active'?'运行中':esc(item.state||'未知')}</span></td><td class="mono">${item.pid||'—'}</td><td class="mono">${item.memory==null?'—':bytes(item.memory)}</td></tr>`).join('')+'</tbody></table>':'<div class="empty">暂时无法读取服务状态</div>';
    $('#server-network').innerHTML=data.interfaces.length?'<table><thead><tr><th>网卡</th><th>累计接收</th><th>累计发送</th></tr></thead><tbody>'+data.interfaces.map(item=>`<tr><td class="mono">${esc(item.name)}</td><td class="mono">${bytes(item.received)}</td><td class="mono">${bytes(item.sent)}</td></tr>`).join('')+'</tbody></table>':'<div class="empty">暂时无法读取网卡信息</div>';
  }catch(error){if(state.page==='overview')$('#server-details').innerHTML='<div class="empty">服务器信息读取失败，将自动重试</div>';}
}
async function renderActivity(){
  state.activityController?.abort();
  const controller=new AbortController();state.activityController=controller;
  const paging=state.pagination.activity,request=++state.activityRequest;
  const params=new URLSearchParams({page:paging.page,page_size:paging.size});
  if(state.activityFilter)params.set('user_id',state.activityFilter);if(state.activitySearch)params.set('search',state.activitySearch);if(paging.anchor)params.set('before_id',paging.anchor);
  try{
    const data=await api('/activity?'+params,{signal:AbortSignal.any([controller.signal,AbortSignal.timeout(10000)])});if(request!==state.activityRequest)return;
    paging.page=data.page;paging.anchor=data.before_id;
    $('#activity-table').innerHTML=data.items.length?'<table><thead><tr><th>连接时间</th><th>用户</th><th>目标域名 / IP</th><th>端口</th><th>传输</th></tr></thead><tbody>'+data.items.map(r=>`<tr><td class="mono">${date(r.ts)}</td><td>${esc(r.name)}</td><td class="mono">${esc(r.domain)}</td><td class="mono">${r.port}</td><td><span class="tag">${esc(r.network.toUpperCase())}</span></td></tr>`).join('')+'</tbody></table>':'<div class="empty">暂无匹配记录</div>';
    pagination('activity',data.total,data.pages);
  }catch(error){if(!controller.signal.aborted)throw error;}
}
function resetPagination(name){state.pagination[name].page=1;if(name==='activity'){state.pagination.activity.anchor=null;state.activityRequest++;state.activityController?.abort();}}
async function loadUsers(){
  if(Date.now()-state.usersLoadedAt>=15000){state.users=await api('/users');state.usersLoadedAt=Date.now();renderUserOptions();}
}
async function refresh(){
  if(!state.csrf)return;
  if(state.refreshing){state.pending=true;return;}
  state.refreshing=true;
  const pageAtStart=state.page,filterAtStart=state.filter;
  try{
    if(pageAtStart==='egress'){await refreshEgress();state.lastFullRefresh=Date.now();return;}
    if(pageAtStart==='egress-records'){await refreshEgressRecords();state.lastFullRefresh=Date.now();return;}
    if(pageAtStart==='settings'){await refreshRates();return;}
    if(pageAtStart==='overview'){await Promise.all([refreshResources(),refreshServerInformation()]);state.lastFullRefresh=Date.now();return;}
    if(pageAtStart==='egress-dashboard'){await Promise.all([refreshEgressOverview(),refreshEgressRates()]);state.lastFullRefresh=Date.now();return;}
    if(pageAtStart==='proxy-dashboard'){
      const [,overview]=await Promise.all([loadUsers(),api('/overview'+(filterAtStart?'?user_id='+filterAtStart:''))]);
      if(pageAtStart!==state.page||filterAtStart!==state.filter){state.pending=true;return;}
      renderUsers();renderOverview(overview);
    }else{
      await loadUsers();if(pageAtStart!==state.page){state.pending=true;return;}
      if(pageAtStart==='users')renderUsers();
      if(pageAtStart==='activity'){if(state.pagination.activity.page===1)state.pagination.activity.anchor=null;await renderActivity();}
    }
    state.lastFullRefresh=Date.now();
  }catch(error){toast(error.name==='TimeoutError'?'请求超时，请稍后重试':error.message);}
  finally{state.refreshing=false;if(state.pending){state.pending=false;refresh();}}
}
async function enter(data){state.csrf=data.csrf;$('#account-name').textContent=data.username;$('#login').classList.add('hidden');$('#app').classList.remove('hidden');$('#password').value='';await refresh();}
function subscription(user){$('#subscription-title').textContent=user.name+' · 订阅';$('#clash-link').value=user.clash_url;$('#rocket-link').value=user.shadowrocket_url;$('#subscription-dialog').showModal();}
async function copy(selector){try{await navigator.clipboard.writeText($(selector).value);toast('订阅链接已复制');}catch{$(selector).focus();$(selector).select();toast('请复制已选中的链接');}}
document.addEventListener('click',event=>{const button=event.target.closest('[data-pagination]');if(!button||button.disabled)return;const name=button.dataset.pagination,paging=state.pagination[name];paging.page+=Number(button.dataset.step);if(name==='activity'||name==='egress-records'){if(paging.page===1)paging.anchor=null;$('#'+name+'-table').scrollTop=0;(name==='activity'?renderActivity():renderEgressRecords()).catch(e=>toast(e.message));}else{renderUsers();if(name==='users')$('#user-cards').parentElement.scrollTop=0;}});
document.addEventListener('change',event=>{const name=event.target.dataset.pageSize;if(!name)return;const paging=state.pagination[name];paging.size=Number(event.target.value);paging.page=1;if(name==='activity'||name==='egress-records'){paging.anchor=null;$('#'+name+'-table').scrollTop=0;(name==='activity'?renderActivity():renderEgressRecords()).catch(e=>toast(e.message));}else{renderUsers();if(name==='users')$('#user-cards').parentElement.scrollTop=0;}});
$('#login-form').addEventListener('submit',async event=>{event.preventDefault();const button=event.submitter;button.disabled=true;$('#login-error').textContent='';try{await enter(await api('/login',{method:'POST',body:JSON.stringify({username:$('#username').value,password:$('#password').value})}));}catch(error){$('#login-error').textContent=error.message;}finally{button.disabled=false;}});
document.addEventListener('click',async event=>{const nav=event.target.closest('[data-page]');if(nav){if(nav.dataset.page==='proxy-dashboard'){state.filter='';resetPagination('usage');}page(nav.dataset.page);return;}const close=event.target.closest('.close-dialog');if(close){close.closest('dialog').close();return;}const button=event.target.closest('[data-action]');if(!button)return;const user=state.users.find(u=>u.id===Number(button.dataset.id));if(!user)return;const action=button.dataset.action;if(action==='subscription')subscription(user);if(action==='view'){state.filter=String(user.id);resetPagination('usage');page('proxy-dashboard');}if(action==='rename'){state.edit=user.id;$('#user-modal-title').textContent='重命名用户';$('#user-name').value=user.name;$('#user-dialog').showModal();}if(action==='toggle')await busy(button,async()=>{await api('/users/'+user.id,{method:'PATCH',body:JSON.stringify({enabled:!user.enabled})});toast(user.enabled?'用户已禁用，新连接将被拒绝':'用户已启用');state.usersLoadedAt=0;await refresh();});if(action==='reset'){$('#confirm-title').textContent='重置「'+user.name+'」的订阅';$('#confirm-description').textContent='这会更换订阅链接与节点凭据，原链接将失效。已导入该用户的设备需要重新导入新链接。累计统计会保留。';$('#confirm-action').dataset.id=user.id;$('#confirm-dialog').showModal();}});
$('#add-user').addEventListener('click',()=>{state.edit=null;$('#user-modal-title').textContent='新增用户';$('#user-name').value='';$('#user-dialog').showModal();});
$('#user-form').addEventListener('submit',async event=>{event.preventDefault();await busy(event.submitter,async()=>{const user=await api(state.edit?'/users/'+state.edit:'/users',{method:state.edit?'PATCH':'POST',body:JSON.stringify({name:$('#user-name').value})});$('#user-dialog').close();state.usersLoadedAt=0;await refresh();if(!state.edit)subscription(user);toast('用户已保存');});});
$('#confirm-action').addEventListener('click',event=>busy(event.currentTarget,async()=>{const user=await api('/users/'+event.currentTarget.dataset.id+'/reset',{method:'POST',body:'{}'});$('#confirm-dialog').close();state.usersLoadedAt=0;await refresh();subscription(user);toast('订阅已重置，请重新导入新链接');}));
$('#copy-clash').addEventListener('click',()=>copy('#clash-link'));$('#copy-rocket').addEventListener('click',()=>copy('#rocket-link'));
$('#users-search-form').addEventListener('submit',event=>{event.preventDefault();state.userFilter=$('#users-user-filter').value;resetPagination('users');renderUsers();$('#user-cards').parentElement.scrollTop=0;});
$('#users-search-form').addEventListener('reset',()=>{state.userFilter='';resetPagination('users');renderUsers();});
$('#activity-search-form').addEventListener('submit',event=>{event.preventDefault();busy(event.submitter,async()=>{state.activityFilter=$('#activity-user-filter').value;state.activitySearch=$('#domain-search').value.trim();resetPagination('activity');$('#activity-table').scrollTop=0;await renderActivity();});});
$('#activity-search-form').addEventListener('reset',()=>{state.activityFilter='';state.activitySearch='';resetPagination('activity');$('#activity-table').scrollTop=0;renderActivity().catch(error=>toast(error.message));});
$('#overview-reset').addEventListener('click',()=>{state.filter='';resetPagination('usage');refresh();});
$('#overview-user-filter').addEventListener('change',event=>{state.filter=event.target.value;resetPagination('usage');refresh();refreshRates();});
$('#logout').addEventListener('click',event=>busy(event.currentTarget,async()=>{await api('/logout',{method:'POST',body:'{}'});showLogin();}));
$('#password-form').addEventListener('submit',event=>{event.preventDefault();busy(event.submitter,async()=>{await api('/password',{method:'POST',body:JSON.stringify({current:$('#old-password').value,password:$('#new-password').value})});event.target.reset();toast('管理员密码已更新');});});
setInterval(()=>{if(document.hidden||!state.csrf)return;refreshRates();refreshResources();if(state.page==='egress-dashboard')refreshEgressRates();if(state.page!=='settings'&&Date.now()-state.lastFullRefresh>=15000)refresh();},5000);
document.addEventListener('DOMContentLoaded',()=>api('/me').then(data=>data.authenticated?enter(data):showLogin()).catch(()=>showLogin()));
