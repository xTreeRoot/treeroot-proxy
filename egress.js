'use strict';
let egressDevices=[];
let egressTrash=false;
async function browserDeviceInfo(){
  const data={userAgent:navigator.userAgent,platform:navigator.platform,language:navigator.language,
    timezone:Intl.DateTimeFormat().resolvedOptions().timeZone,width:screen.width,height:screen.height};
  if(navigator.userAgentData){
    const hints=await navigator.userAgentData.getHighEntropyValues(['platformVersion','architecture','bitness']);
    Object.assign(data,{platformName:hints.platform,platformVersion:hints.platformVersion,architecture:hints.architecture,bitness:hints.bitness});
  }
  return data;
}
async function refreshEgress(){
  egressDevices=await api('/egress'+(egressTrash?'?deleted=true':''));
  $('#egress-trash').textContent=egressTrash?'返回电脑出口':'已删除设备';
  $('#add-egress').hidden=egressTrash;
  if(state.page!=='egress')return;
  $('#egress-devices').innerHTML=egressDevices.length?egressDevices.map(device=>`<article class="egress-card"><div class="egress-card-head"><h3>${esc(device.name)}</h3><span class="badge ${device.online?'':'off'}"><i class="dot"></i>${device.online?'已连接':!device.enabled?'已停用':device.paired?'等待连接':'待安装'}</span></div><div class="egress-details"><span>设备<strong>${esc(device.browser.platformName||device.browser.platform||'待识别')} ${esc(device.browser.platformVersion)}</strong></span><span>出口 IP<strong>${esc(device.last_ip||'待检测')}</strong></span><span>最近在线<strong>${date(device.last_seen)}</strong></span></div><div class="egress-actions">${egressTrash?`<button class="button subtle" data-egress-action="restore" data-egress-id="${device.id}">恢复设备</button>`:`<button class="button subtle" data-egress-action="setup" data-egress-id="${device.id}" ${!device.enabled?'disabled':''}>${device.paired?'重新安装连接器':'一键连接'}</button><button class="button subtle" data-egress-action="test" data-egress-id="${device.id}" ${!device.online?'disabled':''}>检测出口 IP</button><button class="button subtle" data-egress-action="code" data-egress-id="${device.id}">复制应用连接码</button><button class="button ${device.enabled?'subtle':'primary'}" data-egress-action="toggle" data-egress-id="${device.id}">${device.enabled?'停用':'启用并自动连接'}</button><button class="button danger" data-egress-action="delete" data-egress-id="${device.id}">删除</button>`}</div><p class="egress-test">${device.online?'已连接。可将应用连接码绑定到采集账号。':device.paired?'连接器会自动重连；请保持电脑开机联网。':'首次连接：下载后解压，macOS 双击“连接.command”，Windows 双击“连接.cmd”；安装完成后开机自动加密连接。'}</p></article>`).join(''):'<div class="egress-empty">还没有电脑出口<br>添加这台电脑，完成一次安装后即可自动连接。</div>';
}
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
