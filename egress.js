'use strict';
let egressDevices=[];
let egressTrash=false;
let egressBlacklist=[];
let egressBlacklistEditing=null;
let egressRecordRequest=0,egressRecordController,egressRecordDevicesLoadedAt=0;
let egressRecordFilters={};
const egressEventNames={created:'添加电脑',setup_downloaded:'下载连接器',paired:'完成配对',connected:'连接器上线',disconnected:'心跳中断',enabled:'启用出口',disabled:'停用出口',deleted:'删除电脑',restored:'恢复电脑',ip_test:'检测出口 IP',connection:'代理连接'};
const egressRecordDetails={invalid_request:'请求格式无效',target_rejected:'目标地址或端口被拒绝',target_blacklisted:'目标主机在当前出口黑名单中',connection_failed:'网络连接失败',timeout:'连接超时',interrupted:'连接中断'};
let egressOverviewController,egressOverviewData;
const egressMetricIds=['total-traffic','day-traffic','enabled','connections'];
function egressSuccessRate(connections,succeeded){return connections?(succeeded/connections*100).toFixed(1)+'%':'—';}
function renderEgressOverview(data){
  const options='<option value="">全部电脑</option>'+data.device_options.map(device=>`<option value="${esc(device.id)}">${esc(device.name)}${device.deleted?'（已删除）':''}</option>`).join('');
  const select=$('#egress-overview-device'),selected=select.value;
  if(select.innerHTML!==options){select.innerHTML=options;select.value=selected;}
  const values=[bytes(data.total_upload+data.total_download),bytes(data.day_upload+data.day_download),data.enabled+' / '+data.available,data.day_connections.toLocaleString()];
  egressMetricIds.forEach((key,index)=>$('#egress-'+key).textContent=values[index]);
  $('#egress-total-breakdown').textContent=`↑ ${bytes(data.total_upload)} · ↓ ${bytes(data.total_download)}`;
  $('#egress-day-breakdown').textContent=`↑ ${bytes(data.day_upload)} · ↓ ${bytes(data.day_download)}`;
  $('#egress-online-note').textContent=`当前在线 ${data.online} 台`;
  $('#egress-failed').textContent=`成功率 ${egressSuccessRate(data.day_connections,data.day_succeeded)} · 失败 ${(data.day_connections-data.day_succeeded).toLocaleString()} 次`;
  renderEgressRates(data);
  $('#egress-trend-range').textContent=(data.hours===24?'24 小时':'7 天')+' · 每小时';
  renderTraffic(data.series,'egress-traffic-chart',data.hours);
  $('#egress-traffic-chart').setAttribute('aria-label',`电脑出口最近${data.hours===24?'24小时':'7天'}上传${bytes(data.upload)}，下载${bytes(data.download)}`);
  $('#egress-trend-empty').hidden=!!data.connections;
  const top=data.domains[0]?.connections||1;
  $('#egress-domain-list').innerHTML=data.domains.length?data.domains.map(domain=>`<div class="domain-row"><div class="domain-name"><span title="${esc(domain.domain)}">${esc(domain.domain)}</span><span>${domain.connections.toLocaleString()} 次 · ${bytes(domain.upload+domain.download)}</span></div><div class="domain-track"><div class="domain-fill" style="width:${domain.connections/top*100}%"></div></div></div>`).join(''):'<div class="empty">暂无出口访问目标</div>';
  $('#egress-device-usage').innerHTML=data.devices.length?'<table><thead><tr><th>电脑</th><th>当前状态</th><th>总流量</th><th>上传</th><th>下载</th><th>连接数</th><th>成功率</th><th>最近连接记录</th><th></th></tr></thead><tbody>'+data.devices.map(device=>`<tr><td class="egress-device-name">${esc(device.name)}</td><td><span class="badge ${device.online?'':'off'}"><i class="dot"></i>${device.deleted?'已删除':device.online?'在线':device.enabled?'离线':'已停用'}</span></td><td class="mono">${bytes(device.upload+device.download)}</td><td class="mono">${bytes(device.upload)}</td><td class="mono">${bytes(device.download)}</td><td class="mono">${device.connections.toLocaleString()}</td><td class="mono">${egressSuccessRate(device.connections,device.succeeded)}</td><td class="mono">${date(device.last_record)}</td><td><button class="text-button" data-egress-records="${esc(device.id)}">查看明细</button></td></tr>`).join('')+'</tbody></table>':'<div class="empty">暂无电脑出口，可在“电脑出口”添加电脑并安装连接器</div>';
}
async function refreshEgressOverview(){
  egressOverviewController?.abort();
  const controller=new AbortController();egressOverviewController=controller;
  const device=$('#egress-overview-device').value,hours=$('#egress-overview-hours').value;
  const current=()=>!controller.signal.aborted&&state.page==='egress-dashboard'&&device===$('#egress-overview-device').value&&hours===$('#egress-overview-hours').value;
  const status=$('#egress-overview-status');
  status.textContent='正在更新出口统计…';status.classList.remove('error');
  $('#egress-device-usage').setAttribute('aria-busy','true');
  try{
    const data=await api('/egress/overview?'+new URLSearchParams({device_id:device,hours}),{signal:AbortSignal.any([controller.signal,AbortSignal.timeout(10000)])});
    if(!current())return;
    egressOverviewData=data;renderEgressOverview(data);
    status.textContent=`每 15 秒更新 · ${date(data.until)}${data.last_record?' · 最近连接 '+date(data.last_record):' · 暂无已上报连接'}`;
  }catch(error){
    if(!current())return;
    egressMetricIds.forEach(key=>$('#egress-'+key).textContent='—');
    $('#egress-failed').textContent='暂不可用';
    echarts.getInstanceByDom($('#egress-traffic-chart'))?.clear();
    $('#egress-trend-empty').hidden=true;
    $('#egress-domain-list').innerHTML='<div class="empty">出口统计暂不可用</div>';
    $('#egress-device-usage').innerHTML='<div class="empty">请稍后重试，统计将在下次刷新时更新</div>';
    status.textContent='出口统计更新失败：'+(error.name==='TimeoutError'?'请求超时':error.message);status.classList.add('error');
  }finally{if(egressOverviewController===controller)$('#egress-device-usage').setAttribute('aria-busy','false');}
}
for(const id of ['egress-overview-device','egress-overview-hours'])$('#'+id).addEventListener('change',()=>{
  // Clear the previous scope immediately while the new selection is loading.
  egressMetricIds.forEach(key=>$('#egress-'+key).textContent='—');
  echarts.getInstanceByDom($('#egress-traffic-chart'))?.clear();
  $('#egress-domain-list').innerHTML='<div class="empty">正在获取统计…</div>';
  $('#egress-device-usage').innerHTML='<div class="empty">正在获取统计…</div>';
  for(const direction of ['upload','download'])$('#egress-speed-'+direction).textContent='—';
  refreshEgressOverview();refreshEgressRates();
});
function renderEgressRates(data){
  for(const direction of ['upload','download'])$('#egress-speed-'+direction).textContent=data['speed_'+direction]==null?'—':bytes(data['speed_'+direction])+'/s';
  const note=!data.online_devices?'当前无在线电脑':data.live_devices===data.online_devices?'每 5 秒采样 · 正在传输的流量':data.live_devices?`${data.live_devices} / ${data.online_devices} 台上报实时速率`:'等待采样；旧连接器需重新安装';
  document.querySelectorAll('.egress-live-note').forEach(element=>element.textContent=note);
}
async function refreshEgressRates(){
  if(state.page!=='egress-dashboard')return;
  if(state.egressRatesRefreshing){state.egressRatesPending=true;return;}
  const device=$('#egress-overview-device').value;state.egressRatesRefreshing=true;
  try{const data=await api('/egress/rates?'+new URLSearchParams({device_id:device}));if(state.page==='egress-dashboard'&&device===$('#egress-overview-device').value)renderEgressRates(data);}
  catch(error){if(state.page==='egress-dashboard'&&device===$('#egress-overview-device').value){for(const direction of ['upload','download'])$('#egress-speed-'+direction).textContent='—';document.querySelectorAll('.egress-live-note').forEach(element=>element.textContent='实时速率暂不可用');}}
  finally{state.egressRatesRefreshing=false;if(state.egressRatesPending){state.egressRatesPending=false;refreshEgressRates();}}
}
new ResizeObserver(()=>{const element=$('#egress-traffic-chart');if(element.clientWidth)echarts.getInstanceByDom(element)?.resize();}).observe($('#egress-traffic-chart'));
function openEgressOverviewRecords(deviceId){
  const options=egressOverviewData?.device_options||[];
  $('#egress-records-device').innerHTML='<option value="">全部电脑</option>'+options.map(device=>`<option value="${esc(device.id)}">${esc(device.name)}${device.deleted?'（已删除）':''}</option>`).join('');
  $('#egress-records-outcome').value='';$('#egress-records-search').value='';
  $('#egress-records-device').value=deviceId;
  $('#egress-records-kind').value='request';
  $('#egress-records-hours').value=$('#egress-overview-hours').value;
  egressRecordFilters={device_id:deviceId,kind:'request',start:egressOverviewData?.since||''};
  state.pagination['egress-records'].page=1;state.pagination['egress-records'].anchor=null;
  page('egress-records');
}
$('#egress-overview-records').addEventListener('click',()=>openEgressOverviewRecords($('#egress-overview-device').value));
document.addEventListener('click',event=>{const button=event.target.closest('[data-egress-records]');if(button)openEgressOverviewRecords(button.dataset.egressRecords);});
function egressRecordsTable(items){
  return '<table><thead><tr><th>时间</th><th>电脑</th><th>记录类型</th><th>事件</th><th>目标域名 / IP</th><th>端口</th><th>方法</th><th>结果</th><th>上传</th><th>下载</th><th>耗时</th><th>最近检测出口 IP</th><th>详情</th></tr></thead><tbody>'+ (items.length?items.map(r=>{
    const request=r.kind==='request';
    return `<tr><td class="mono">${date(r.ts)}</td><td>${esc(r.name)}</td><td>${request?'出口访问':'设备事件'}</td><td>${esc(egressEventNames[r.action]||r.action)}</td><td class="mono">${esc(r.domain||'—')}</td><td class="mono">${r.port??'—'}</td><td class="mono">${esc(r.method||'—')}</td><td><span class="badge ${r.outcome==='failed'?'failed':''}"><i class="dot"></i>${r.outcome==='success'?(request?'已连接':'成功'):'失败'}</span></td><td class="mono">${request?bytes(r.upload):'—'}</td><td class="mono">${request?bytes(r.download):'—'}</td><td class="mono">${request?(r.duration_ms<1000?r.duration_ms+' ms':(r.duration_ms/1000).toFixed(2)+' s'):'—'}</td><td class="mono">${esc(r.exit_ip||'—')}</td><td>${esc(egressRecordDetails[r.detail]||r.detail||'—')}</td></tr>`;
  }).join(''):'<tr><td class="empty" colspan="13">暂无匹配记录</td></tr>')+'</tbody></table>';
}
async function renderEgressRecords(){
  egressRecordController?.abort();
  const controller=new AbortController();egressRecordController=controller;
  const request=++egressRecordRequest,paging=state.pagination['egress-records'];
  const params=new URLSearchParams({page:paging.page,page_size:paging.size,...egressRecordFilters});
  if(paging.anchor)params.set('before_id',paging.anchor);
  try{
    const data=await api('/egress/records?'+params,{signal:AbortSignal.any([controller.signal,AbortSignal.timeout(10000)])});
    if(request!==egressRecordRequest)return;
    paging.page=data.page;paging.anchor=data.before_id;
    $('#egress-records-table').innerHTML=egressRecordsTable(data.items);
    pagination('egress-records',data.total,data.pages);
  }catch(error){if(!controller.signal.aborted)throw error;}
}
async function refreshEgressRecords(){
  const paging=state.pagination['egress-records'];
  if(paging.page===1)paging.anchor=null;
  await Promise.all([renderEgressRecords(),(async()=>{
    if(Date.now()-egressRecordDevicesLoadedAt<15000)return;
    const [active,deleted]=await Promise.all([api('/egress'),api('/egress?deleted=true')]);
    const select=$('#egress-records-device'),draft=select.value;
    select.innerHTML='<option value="">全部电脑</option>'+[...active,...deleted].map(device=>`<option value="${esc(device.id)}">${esc(device.name)}${deleted.some(item=>item.id===device.id)?'（已删除）':''}</option>`).join('');
    select.value=draft;egressRecordDevicesLoadedAt=Date.now();
  })()]);
}
function searchEgressRecords(reset=false){
  egressRecordFilters=reset?{}:{device_id:$('#egress-records-device').value,kind:$('#egress-records-kind').value,outcome:$('#egress-records-outcome').value,search:$('#egress-records-search').value.trim()};
  if(!reset&&$('#egress-records-hours').value==='24')egressRecordFilters.start=Math.floor(Date.now()/1000)-86400;
  state.pagination['egress-records'].page=1;state.pagination['egress-records'].anchor=null;
  $('#egress-records-table').scrollTop=0;
  renderEgressRecords().catch(error=>toast(error.message));
}
$('#egress-records-search-form').addEventListener('submit',event=>{event.preventDefault();searchEgressRecords();});
$('#egress-records-search-form').addEventListener('reset',()=>searchEgressRecords(true));
$('#egress-records-table').innerHTML=egressRecordsTable([]);
pagination('egress-records',0,1);
async function browserDeviceInfo(){
  const data={userAgent:navigator.userAgent,platform:navigator.platform,language:navigator.language,
    timezone:Intl.DateTimeFormat().resolvedOptions().timeZone,width:screen.width,height:screen.height};
  if(navigator.userAgentData){
    const hints=await navigator.userAgentData.getHighEntropyValues(['platformVersion','architecture','bitness']);
    Object.assign(data,{platformName:hints.platform,platformVersion:hints.platformVersion,architecture:hints.architecture,bitness:hints.bitness});
  }
  return data;
}
function renderEgressBlacklist(){
  const enabled=egressBlacklist.filter(item=>item.enabled).length;
  $('#egress-blacklist-status').textContent=`${enabled} 条启用 · ${egressBlacklist.length} 条规则`;
  $('#egress-blacklist-list').innerHTML=egressBlacklist.length?'<table><thead><tr><th>精确主机</th><th>状态</th><th>更新时间</th><th>操作</th></tr></thead><tbody>'+egressBlacklist.map(item=>`<tr><td class="mono">${esc(item.host)}</td><td>${badge(item.enabled)}</td><td class="mono">${date(item.updated)}</td><td><div class="egress-blacklist-actions"><button class="text-button" data-egress-blacklist-action="edit" data-egress-blacklist-id="${item.id}">编辑</button><button class="text-button" data-egress-blacklist-action="toggle" data-egress-blacklist-id="${item.id}">${item.enabled?'停用':'启用'}</button><button class="text-button danger-text" data-egress-blacklist-action="delete" data-egress-blacklist-id="${item.id}">删除</button></div></td></tr>`).join('')+'</tbody></table>':'<div class="empty">暂无黑名单规则</div>';
}
function resetEgressBlacklistEditor(){
  egressBlacklistEditing=null;$('#egress-blacklist-host').value='';$('#egress-blacklist-cancel').hidden=true;
  $('#egress-blacklist-form').querySelector('button[type="submit"]').textContent='添加规则';
}
async function refreshEgressBlacklist(){
  egressBlacklist=await api('/egress/blacklist');
  if(state.page==='egress')renderEgressBlacklist();
}
function setEgressBlacklistDialog(open){
  document.body.classList.toggle('egress-dialog-open',open);
}
$('#egress-blacklist-open').addEventListener('click',async()=>{
  await refreshEgressBlacklist();
  $('#egress-blacklist-dialog').showModal();
  setEgressBlacklistDialog(true);
});
$('#egress-blacklist-close').addEventListener('click',()=>$('#egress-blacklist-dialog').close());
$('#egress-blacklist-dialog').addEventListener('close',()=>setEgressBlacklistDialog(false));
async function refreshEgress(){
  const [devices,blacklist]=await Promise.all([api('/egress'+(egressTrash?'?deleted=true':'')),api('/egress/blacklist')]);
  egressDevices=devices;egressBlacklist=blacklist;
  $('#egress-trash').textContent=egressTrash?'返回电脑出口':'已删除设备';
  $('#add-egress').hidden=egressTrash;
  if(state.page!=='egress')return;
  renderEgressBlacklist();
  $('#egress-devices').innerHTML=egressDevices.length?egressDevices.map(device=>`<article class="egress-card"><div class="egress-card-head"><h3>${esc(device.name)}</h3><span class="badge ${device.online?'':'off'}"><i class="dot"></i>${device.online?'已连接':!device.enabled?'已停用':device.paired?'等待连接':'待安装'}</span></div><div class="egress-details"><span>设备<strong>${esc(device.browser.platformName||device.browser.platform||'待识别')} ${esc(device.browser.platformVersion)}</strong></span><span>出口 IP<strong>${esc(device.last_ip||'待检测')}</strong></span><span>最近在线<strong>${date(device.last_seen)}</strong></span></div><div class="egress-actions">${egressTrash?`<button class="button subtle" data-egress-action="restore" data-egress-id="${device.id}">恢复设备</button>`:`<button class="button subtle" data-egress-action="setup" data-egress-id="${device.id}" ${!device.enabled?'disabled':''}>${device.paired?'重新安装连接器':'一键连接'}</button><button class="button subtle" data-egress-action="test" data-egress-id="${device.id}" ${!device.online?'disabled':''}>检测出口 IP</button><button class="button subtle" data-egress-action="code" data-egress-id="${device.id}">复制应用连接码</button><button class="button ${device.enabled?'subtle':'primary'}" data-egress-action="toggle" data-egress-id="${device.id}">${device.enabled?'停用':'启用并自动连接'}</button><button class="button danger" data-egress-action="delete" data-egress-id="${device.id}">删除</button>`}</div><p class="egress-test">${device.online?'已连接。可将应用连接码绑定到采集账号。':device.paired?'连接器会自动重连；请保持电脑开机联网。':'首次连接：下载后解压，macOS 双击“连接.command”，Windows 双击“连接.cmd”；安装完成后开机自动加密连接。'}</p></article>`).join(''):'<div class="egress-empty">还没有电脑出口<br>添加这台电脑，完成一次安装后即可自动连接。</div>';
}
$('#egress-blacklist-form').addEventListener('submit',event=>{
  event.preventDefault();busy(event.submitter,async()=>{
    const host=$('#egress-blacklist-host').value.trim();
    if(egressBlacklistEditing){
      await api('/egress/blacklist/'+egressBlacklistEditing,{method:'PATCH',body:JSON.stringify({host})});toast('黑名单规则已更新');
    }else{
      await api('/egress/blacklist',{method:'POST',body:JSON.stringify({host})});toast('黑名单规则已添加');
    }
    resetEgressBlacklistEditor();await refreshEgress();
  });
});
$('#egress-blacklist-cancel').addEventListener('click',resetEgressBlacklistEditor);
document.addEventListener('click',event=>{
  const button=event.target.closest('[data-egress-blacklist-action]');if(!button)return;
  const item=egressBlacklist.find(entry=>String(entry.id)===button.dataset.egressBlacklistId);if(!item)return;
  busy(button,async()=>{
    const action=button.dataset.egressBlacklistAction;
    if(action==='edit'){
      egressBlacklistEditing=item.id;$('#egress-blacklist-host').value=item.host;$('#egress-blacklist-cancel').hidden=false;
      $('#egress-blacklist-form').querySelector('button[type="submit"]').textContent='保存修改';$('#egress-blacklist-host').focus();return;
    }
    if(action==='toggle')await api('/egress/blacklist/'+item.id,{method:'PATCH',body:JSON.stringify({enabled:!item.enabled})});
    if(action==='delete')await api('/egress/blacklist/'+item.id,{method:'DELETE',body:'{}'});
    await refreshEgress();toast(action==='delete'?'黑名单规则已删除':'黑名单规则状态已更新');
  });
});
$('#egress-trash').addEventListener('click',async()=>{egressTrash=!egressTrash;await refreshEgress();});
$('#add-egress').addEventListener('click',async()=>{
  const device=await browserDeviceInfo();
  $('#egress-name').value=device.platformName==='macOS'||device.platform.includes('Mac')?'我的 Mac':'我的电脑';
  $('#egress-browser').textContent='当前浏览器：'+(device.platformName||device.platform)+' '+(device.platformVersion||'');
  $('#egress-dialog').showModal();
});
$('#egress-form').addEventListener('submit',event=>{
  event.preventDefault();busy(event.submitter,async()=>{
    await api('/egress',{method:'POST',body:JSON.stringify({name:$('#egress-name').value,browser:await browserDeviceInfo()})});
    $('#egress-dialog').close();await refreshEgress();toast('电脑已添加，请下载并安装连接器');
  });
});
document.addEventListener('click',event=>{
  const button=event.target.closest('[data-egress-action]');if(!button)return;
  const device=egressDevices.find(item=>item.id===button.dataset.egressId);if(!device)return;
  busy(button,async()=>{
    const path='/egress/'+device.id;
    if(button.dataset.egressAction==='delete'){await api(path,{method:'DELETE',body:'{}'});toast('电脑出口已删除，可在“已删除设备”恢复');}
    if(button.dataset.egressAction==='restore')await api(path+'/restore',{method:'POST',body:'{}'});
    if(button.dataset.egressAction==='toggle')await api(path,{method:'PATCH',body:JSON.stringify({enabled:!device.enabled})});
    if(button.dataset.egressAction==='test'){const result=await api(path+'/test',{method:'POST',body:'{}'});toast('电脑出口 IP：'+result.ip);}
    if(button.dataset.egressAction==='code'){
      const result=await api(path+'/access-code');
      try{await navigator.clipboard.writeText(result.code);toast('连接码已复制，请粘贴到采集系统的电脑出口设置');}
      catch{toast('浏览器未允许剪贴板，请在 HTTPS 页面重试');}
    }
    if(button.dataset.egressAction==='setup'){
      const response=await fetch('/api'+path+'/setup',{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':state.csrf},body:'{}'});
      if(!response.ok){const error=await response.json();throw new Error(error.detail||'下载失败');}
      const url=URL.createObjectURL(await response.blob());const link=document.createElement('a');
      link.href=url;link.download='TreeRoot-Connector.zip';link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
      toast('解压后双击“连接.command”（Mac）或“连接.cmd”（Windows），首次安装后自动连接；配对包 10 分钟内有效');
    }
    await refreshEgress();
  });
});
