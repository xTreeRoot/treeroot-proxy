import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Button, Descriptions, Dropdown, Empty, Form, Input, InputNumber, Modal as AntModal, Select, Space, Switch, Table, Tabs, Tag, Tooltip } from "antd";
import type { ModalProps } from "antd";
import { Plus, RotateCcw, Settings2 } from "lucide-react";
import { api, write } from "../api";
import { Feedback, useRemote, useTask, useUI } from "../components";
import { bytes } from "../lib";
import { HomeDashboard } from "./HomeDashboard";

type Device = {mac:string;name:string;ip:string;online:boolean;last_seen:number;lease_expires:number;upload:number;download:number};
type Policy = {mac:string;name:string;note:string;group:string;fixed_ip:string;blocked:boolean;speed_bps:number;total_bytes:number;daily_bytes:number};
type Rule = {id:string;scope:string;subject:string;target_type:string;target:string;port:string;protocol:string;action:string;node:string;priority:number;enabled:boolean;no_resolve?:boolean};
type Config = {revision:number;default:string;default_node:string;devices:Policy[];rules:Rule[];nodes:{id:string;name:string}[]};
type Gateway = {id:string;name:string;proxy_user_id:number|null;proxy_user_name:string|null;online:boolean;paired:boolean;enabled:boolean;last_seen:number;version:string;applied:number;revision:number;error:string;status:Record<string,unknown>;capabilities:{limits:boolean;wifi:boolean;upgrade:boolean;mode:string}};
type HomeEvent={id:string;ts:number;mac:string;source:string;target:string;port:number;protocol:string;rule:string;action:string;outcome?:string};
type Task={id:string;kind:string;created:number;expires:number;state:string;detail:string};
type WiFiBand={enabled:boolean;ssid:string;secured:boolean;channel:number};
type WiFiSettings={unified:boolean;band2:WiFiBand;band5:WiFiBand;key2?:string;key5?:string};
type WiFiStatus=WiFiSettings&{online:boolean;guest_supported:boolean};
type RulePreview={config:Config;changes:{operation:string;line:string}[];source?:string};
const time=(value:number)=>value?new Date(value*1000).toLocaleString():"—";
const actions=[{label:"直连",value:"direct"},{label:"VPS 节点",value:"proxy"},{label:"禁止访问",value:"reject"}];
const resultNames:Record<string,string>={pending:"等待网关",running:"执行中",success:"成功",failed:"失败",expired:"已过期",cancelled:"已取消"};
const taskNames:Record<string,string>={"proxy.restart":"重启家庭代理","gateway.reboot":"重启家庭网关","diagnostics":"刷新诊断","backup.create":"备份家庭配置","backup.restore":"恢复家庭配置","wifi.set":"修改 Wi-Fi","wifi.restart":"重启无线","ap.reboot":"重启 Wi-Fi AP"};
function Modal({"aria-label":label,...props}:ModalProps&{"aria-label":string}) {
 return <AntModal {...props} className={`tree-modal home-modal ${props.className||""}`} mask={{closable:false}} keyboard={!props.confirmLoading} closable={{disabled:!!props.confirmLoading,"aria-label":"关闭"}} panelRef={node=>{
  const dialog=node?.closest('[role="dialog"]');
  dialog?.setAttribute("aria-label",label);
  dialog?.setAttribute("aria-busy",String(!!props.confirmLoading));
 }}/>
}
export function HomePage({active}:{active:boolean}) {
 const client=useQueryClient(),ui=useUI(),{pending,run}=useTask();
 const [selected,setSelected]=useState(""),[tab,setTab]=useState("dashboard"),[filter,setFilter]=useState(""),[page,setPage]=useState(1),[routeFilter,setRouteFilter]=useState("");
 const [waiting,setWaiting]=useState<{id:string;revision:number}|null>(null);
 const [adding,setAdding]=useState(false),[pair,setPair]=useState<{code:string;expires:number}|null>(null),[editDevice,setEditDevice]=useState<Policy|null>(null),[editRule,setEditRule]=useState<Rule|null>(null),[history,setHistory]=useState(false);
 const [deviceForm]=Form.useForm<Policy>(),[ruleForm]=Form.useForm<Rule>(),[gatewayForm]=Form.useForm<{name:string}>();
 const [wifiOpen,setWiFiOpen]=useState(false),[wifiForm]=Form.useForm<WiFiSettings>();
 const [advanced,setAdvanced]=useState(false),[preview,setPreview]=useState<RulePreview|null>(null),[advancedForm]=Form.useForm<{scope:string;subject:string;source:string}>();
 const advancedScope=Form.useWatch("scope",advancedForm);
 const gateways=useRemote<Gateway[]>(["home"],"/home",active,5000);
 const gateway=(gateways.data||[]).find(g=>g.id===selected)||(gateways.data||[])[0];
 const id=gateway?.id||"",base=`/home/${id}`;
 const config=useRemote<Config>(["home",id,"config"],base+"/config",active&&!!id);
 const devices=useRemote<Device[]>(["home",id,"devices"],base+"/devices",active&&!!id,5000);
 const nodes=useRemote<{id:number;name:string}[]>(["home",id,"nodes"],base+"/nodes",active&&!!id);
 const events=useRemote<{items:HomeEvent[];total:number}>(["home",id,"events",filter,routeFilter,String(page)],base+`/events?page=${page}&mac=${encodeURIComponent(filter)}&action=${routeFilter}`,active&&!!id&&tab==="events");
 const tasks=useRemote<Task[]>(["home",id,"tasks"],base+"/tasks",active&&!!id&&tab==="tasks",5000);
 const versions=useRemote<{revision:number;created:number}[]>(["home",id,"history"],base+"/history",active&&!!id&&history);
 const c=config.data;
 const wifi=gateway?.status.wifi as WiFiStatus|undefined,unified=Form.useWatch("unified",wifiForm);
 useEffect(()=>{setFilter("");setRouteFilter("");setPage(1);setEditDevice(null);setEditRule(null);setPair(null);setHistory(false);setAdvanced(false);setPreview(null)},[id]);
 useEffect(()=>{if(!waiting||waiting.id!==id||!gateway)return;if(gateway.applied>=waiting.revision){setWaiting(null);ui.notify(`家庭网关已生效 r${waiting.revision}`)}else if(Number(gateway.status.failed_revision)>=waiting.revision){setWaiting(null);ui.notify(`r${waiting.revision} 应用失败，已保留原配置`,true)}},[waiting,gateway,id,ui]);
 const invalidate=()=>client.invalidateQueries({queryKey:["home"]});
 async function save(next:Config){const result=await write<{revision:number}>(base+"/config","PUT",next);setWaiting({id,revision:result.revision});await invalidate()}
 function deviceEdit(d:Device){const p=c?.devices.find(p=>p.mac===d.mac)||{mac:d.mac,name:d.name,note:"",group:"",fixed_ip:"",blocked:false,speed_bps:0,total_bytes:0,daily_bytes:0};deviceForm.setFieldsValue(p);setEditDevice(p)}
 function ruleEdit(r?:Rule){const v=r||{id:crypto.randomUUID(),scope:"all",subject:"",target_type:"domain",target:"",port:"",protocol:"",action:"reject",node:"",priority:100,enabled:true};ruleForm.setFieldsValue(v);setEditRule(v)}
 const nodeOptions=(nodes.data||[]).map(n=>({label:n.name,value:`user-${n.id}`}));
 function withNodes(next:Config):Config{const used=new Set([...(next.default==="proxy"?[next.default_node]:[]),...next.rules.filter(r=>r.action==="proxy").map(r=>r.node)]);return {...next,nodes:[...used].map(id=>({id,name:nodeOptions.find(n=>n.value===id)?.label||id}))}}
 async function operation(kind:string){if(!await ui.confirm(gateway!.name,taskNames[kind],["gateway.reboot","wifi.restart","ap.reboot"].includes(kind)?"家庭设备会暂时断网":""))return;await write(base+"/tasks","POST",{kind});setTab("tasks");await invalidate();ui.notify("操作已排队，等待家庭网关回报")}
 async function download(){const value=await api<Config>(base+"/config");const blob=new Blob([JSON.stringify(value,null,2)],{type:"application/json"}),url=URL.createObjectURL(blob),a=document.createElement("a");a.href=url;a.download=`TreeRoot-home-${id}-r${value.revision}.json`;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000)}
 const subject=Form.useWatch("scope",ruleForm),targetType=Form.useWatch("target_type",ruleForm),action=Form.useWatch("action",ruleForm);
 return <div className="home-page">
  <div className="home-toolbar"><Select aria-label="家庭网关" value={id||undefined} options={(gateways.data||[]).map(g=>({label:g.name,value:g.id}))} onChange={setSelected} style={{minWidth:200}}/><Button icon={<Plus size={16}/>} onClick={()=>{gatewayForm.resetFields();setAdding(true)}}>添加网关</Button></div>
  <Feedback loading={gateways.isPending} error={gateways.error} retry={()=>gateways.refetch()}/>
  {!gateways.isPending&&!gateway&&<Empty description="尚未接入家庭网关"/>}
  {gateway&&<>
   <article className="panel home-status" aria-label="家庭网关状态">
    <Space wrap><Tag color={gateway.online?"green":"default"}>{gateway.online?"在线":"离线"}</Tag><Tag color={gateway.capabilities.mode==="isolated"?"orange":"blue"}>{gateway.capabilities.mode==="isolated"?"隔离测试":gateway.capabilities.mode==="physical"?"物理接入":"未接入"}</Tag><Tag>{gateway.paired?"已配对":"未配对"}</Tag><Tag role="status" color={gateway.applied===gateway.revision?"green":gateway.error?"error":"processing"}>{gateway.applied===gateway.revision?`已生效 r${gateway.applied}`:`${gateway.online?"正在应用":"网关离线，等待应用"} r${gateway.revision} · 当前 r${gateway.applied}`}</Tag></Space>
    <Space wrap><Button disabled={!!pending||gateway.paired} onClick={()=>run("pair",async()=>setPair(await write(base+"/pair-code","POST")))}>生成配对码</Button><Button disabled={!!pending||!c} onClick={()=>run("download",download)}>下载配置备份</Button><Button onClick={()=>setHistory(true)} icon={<RotateCcw size={15}/>}>恢复配置</Button>{gateway.capabilities.wifi&&<Button disabled={!!pending||!gateway.online||!wifi?.online} onClick={()=>{if(wifi){wifiForm.setFieldsValue({...wifi,key2:"",key5:""});setWiFiOpen(true)}}}>Wi-Fi 设置</Button>}<Dropdown menu={{items:[{key:"diagnostics",label:"刷新诊断",disabled:!gateway.online},{key:"proxy.restart",label:"重启家庭代理",disabled:!gateway.online},{key:"gateway.reboot",label:"重启家庭网关",disabled:!gateway.online},...(gateway.capabilities.wifi?[{key:"wifi.restart",label:"重启无线",disabled:!gateway.online},{key:"ap.reboot",label:"重启 Wi-Fi AP",disabled:!gateway.online}]:[]),{type:"divider"},{key:"revoke",label:"撤销配对",danger:true,disabled:!gateway.paired}],onClick:({key})=>run(key,async()=>{if(key==="revoke"){if(!await ui.confirm(gateway.name,"撤销配对","远程管理停止，家庭网络保留最后有效配置"))return;await write(base+"/revoke","POST");await invalidate()}else await operation(key)})}}><Button icon={<Settings2 size={16}/>}>操作</Button></Dropdown></Space>
    <Descriptions column={{xs:1,sm:2,lg:4}} size="small" items={[
     {key:"version",label:"网关版本",children:gateway.version||"—"},{key:"last",label:"上次连接",children:time(gateway.last_seen)},
     {key:"proxy-account",label:"VPS 统计账号",children:gateway.proxy_user_id?gateway.proxy_user_name:<Button size="small" disabled={!!pending} onClick={()=>run("proxy-account",async()=>{if(!await ui.confirm(gateway.name,"创建专属统计账号","后续 VPS 流量记入新账号，已有历史保留"))return;await write(base+"/proxy-user","POST");await invalidate()})}>创建统计账号</Button>},
     {key:"proxy",label:"家庭代理",children:gateway.online?(gateway.status.proxy_ready===true?"运行中":"未就绪"):"—"},{key:"lan",label:"LAN",children:String(gateway.status.lan||"—")},
     {key:"exit",label:"默认策略出口",children:String(gateway.status.default_policy_exit||"—")},{key:"exit-time",label:"出口检测时间",children:time(Number(gateway.status.exit_checked)||0)},
     {key:"spool",label:"待上报记录",children:Number(gateway.status.events_pending)||0},{key:"dropped",label:"缓冲溢出记录",children:Number(gateway.status.events_dropped)||0},
     {key:"ipv6",label:"IPv6",children:gateway.capabilities.mode?"LAN 拒绝转发":"未验证"},{key:"wifi",label:"Wi-Fi AP 控制",children:gateway.capabilities.wifi?"可用":"接口未验证"},
     {key:"upgrade",label:"远程升级",children:gateway.capabilities.upgrade?"可用":"尚未验证"},{key:"devices",label:"在线设备",children:(devices.data||[]).filter(d=>d.online).length},
     ...(gateway.capabilities.wifi?[{key:"ssid",label:"Wi-Fi",children:wifi?.online?wifi.band2.ssid:"AP 离线"},{key:"wifi-security",label:"无线加密",children:wifi?.online?(wifi.band2.secured?"已加密":"未加密"):"—"}]:[]),
    ]}/>
    {gateway.error&&<div className="feedback error" role="alert">{gateway.error}</div>}
   </article>
   <Feedback loading={config.isPending} error={config.error} retry={()=>config.refetch()}/>
   <Tabs activeKey={tab} onChange={setTab} items={[
    {key:"dashboard",label:"流量大盘",children:<HomeDashboard id={id} active={active&&tab==="dashboard"} records={action=>{setFilter("");setRouteFilter(action);setPage(1);setTab("events")}}/>},
    {key:"devices",label:"家庭设备",children:<article className="panel home-table" aria-label="家庭设备"><Table<Device> rowKey="mac" loading={devices.isPending} dataSource={devices.data||[]} scroll={{x:900}} pagination={{pageSize:12,showSizeChanger:false}} columns={[
     {title:"设备",key:"name",render:(_,d)=>{const p=c?.devices.find(p=>p.mac===d.mac);return <div><strong>{p?.name||d.name||"未命名设备"}</strong><div className="muted">{p?.note||"—"}</div></div>}},
     {title:"状态",key:"online",render:(_,d)=><Tag color={d.online?"green":"default"}>{d.online?"在线":"离线"}</Tag>},
     {title:"IP / MAC",key:"identity",render:(_,d)=><div>{d.ip}<div className="mono muted">{d.mac}</div></div>},
     {title:"分组",key:"group",render:(_,d)=>c?.devices.find(p=>p.mac===d.mac)?.group||"—"},
     {title:"上传 / 下载",key:"bytes",render:(_,d)=><div>{bytes(d.upload)} / {bytes(d.download)}</div>},
     {title:"上次在线",dataIndex:"last_seen",render:time},
     {title:"操作",key:"actions",render:(_,d)=><Space><Button size="small" disabled={!c||!!pending} onClick={()=>deviceEdit(d)}>设置</Button><Button size="small" onClick={()=>{setFilter(d.mac);setRouteFilter("");setPage(1);setTab("events")}}>连接记录</Button></Space>},
    ]}/></article>},
    {key:"rules",label:"访问规则",children:<article className="panel home-table" aria-label="访问规则"><div className="home-toolbar"><Button type="primary" disabled={!c||!!pending} icon={<Plus size={15}/>} onClick={()=>ruleEdit()}>添加规则</Button><Button disabled={!c||!!pending} onClick={()=>{advancedForm.setFieldsValue({scope:"all",subject:"",source:`prepend: []
append: []
delete: []
`});setPreview(null);setAdvanced(true)}}>高级模式</Button><Button disabled={!c||!!pending} loading={pending==="china-preview"} onClick={()=>run("china-preview",async()=>{const result=await write<RulePreview>(base+"/rules/preview","POST",{revision:c!.revision,template:"china-direct"});advancedForm.setFieldsValue({scope:"all",subject:"",source:result.source||""});setPreview(result);setAdvanced(true)})}>中国流量直连</Button><span>默认策略</span><Select aria-label="默认策略" value={c?.default} options={actions} disabled={!c||!!pending} onChange={value=>{if(c)run("default",()=>save(withNodes({...c,default:value,default_node:value==="proxy"?c.default_node||nodeOptions[0]?.value||"":""})))}}/>{c?.default==="proxy"&&<Select aria-label="默认 VPS 节点" value={c.default_node} options={nodeOptions} disabled={!!pending} onChange={value=>run("default-node",()=>save(withNodes({...c,default_node:value})))}/>}</div><Table<Rule> rowKey="id" dataSource={c?.rules||[]} scroll={{x:950}} pagination={{pageSize:12,showSizeChanger:false}} columns={[
     {title:"启用",dataIndex:"enabled",render:(v,r)=><Switch aria-label={`启用规则 ${r.target||"全部目标"}`} checked={v} disabled={!!pending} onChange={enabled=>c&&run("toggle",()=>save({...c,rules:c.rules.map(v=>v.id===r.id?{...v,enabled}:v)}))}/>},
     {title:"优先级",dataIndex:"priority",sorter:(a,b)=>a.priority-b.priority},
     {title:"设备范围",key:"scope",render:(_,r)=>r.scope==="all"?"全部设备":r.scope==="group"?r.subject:(devices.data||[]).find(d=>d.mac===r.subject)?.name||r.subject},
     {title:"目标",key:"target",render:(_,r)=><div>{r.target_type==="any"?"全部目标":r.target}<div className="muted">{r.target_type} · {r.protocol||"TCP/UDP"}{r.port?` :${r.port}`:""}</div></div>},
     {title:"动作",key:"action",render:(_,r)=><Tag color={r.action==="reject"?"red":r.action==="proxy"?"blue":"default"}>{actions.find(a=>a.value===r.action)?.label}{r.action==="proxy"?` · ${c?.nodes.find(n=>n.id===r.node)?.name||r.node}`:""}</Tag>},
     {title:"操作",key:"actions",render:(_,r)=><Space><Button size="small" disabled={!!pending} onClick={()=>ruleEdit(r)}>编辑</Button><Button size="small" danger disabled={!!pending} onClick={()=>run("delete-rule",async()=>{if(c&&await ui.confirm(r.target||"全部目标","删除规则"))await save(withNodes({...c,rules:c.rules.filter(v=>v.id!==r.id)}))})}>删除</Button></Space>},
    ]}/></article>},
    {key:"events",label:"连接记录",children:<article className="panel home-table" aria-label="家庭连接记录"><div className="home-toolbar"><Select allowClear aria-label="筛选家庭设备" placeholder="全部设备" value={filter||undefined} options={(devices.data||[]).map(d=>({label:d.name||d.mac,value:d.mac}))} onChange={v=>{setFilter(v||"");setPage(1)}} style={{minWidth:220}}/><Select aria-label="筛选连接路径" value={routeFilter} onChange={v=>{setRouteFilter(v);setPage(1)}} options={[{label:"全部路径",value:""},{label:"经过 VPS",value:"proxy"},{label:"直连 · 未经过 VPS",value:"direct"},{label:"已禁止",value:"reject"}]} style={{minWidth:210}}/></div><Feedback error={events.error} retry={()=>events.refetch()}/><Table<HomeEvent> rowKey="id" loading={events.isPending} dataSource={events.data?.items||[]} scroll={{x:850}} pagination={{current:page,total:events.data?.total||0,pageSize:50,showSizeChanger:false,onChange:setPage}} columns={[{title:"时间",dataIndex:"ts",render:time},{title:"设备",dataIndex:"mac"},{title:"目标",dataIndex:"target"},{title:"端口",dataIndex:"port"},{title:"协议",dataIndex:"protocol"},{title:"流量路径",dataIndex:"action",render:a=><Tag color={a==="proxy"?"blue":a==="direct"?"green":"red"}>{a==="proxy"?"经过 VPS":a==="direct"?"直连 · 未经过 VPS":a==="reject"?"已禁止":a}</Tag>},{title:"连接结果",dataIndex:"outcome",render:v=>v==="failed"?"连接失败":v==="blocked"?"已拒绝":"已匹配规则"},{title:"命中记录",dataIndex:"rule",ellipsis:true}]}/></article>},
    {key:"tasks",label:"操作结果",children:<article className="panel home-table" aria-label="家庭操作结果"><Feedback error={tasks.error} retry={()=>tasks.refetch()}/><Table<Task> rowKey="id" loading={tasks.isPending} dataSource={tasks.data||[]} scroll={{x:700}} pagination={{pageSize:12,showSizeChanger:false}} columns={[{title:"操作",dataIndex:"kind",render:v=>taskNames[v]||v},{title:"提交时间",dataIndex:"created",render:time},{title:"状态",dataIndex:"state",render:v=><Tag color={v==="success"?"green":v==="failed"?"red":"default"}>{resultNames[v]||v}</Tag>},{title:"执行结果",dataIndex:"detail"}]}/></article>},
   ]}/>
  </>}
  <Modal open={adding} title={null} aria-label="添加家庭网关" onCancel={()=>setAdding(false)} confirmLoading={!!pending} okText="添加" cancelText="取消" onOk={()=>run("create",async()=>{const v=await gatewayForm.validateFields(),g=await write<{id:string}>("/home","POST",v);setAdding(false);setSelected(g.id);await invalidate()})}><Form form={gatewayForm} layout="vertical"><Form.Item label="网关名称" name="name" rules={[{required:true,max:64}]}><Input autoFocus maxLength={64}/></Form.Item></Form></Modal>
  <Modal open={!!pair} title={null} aria-label="家庭网关配对码" footer={<Button onClick={()=>setPair(null)}>关闭</Button>} onCancel={()=>setPair(null)}><Descriptions column={1} items={[{key:"code",label:"配对码",children:<Input.Password readOnly value={pair?.code} aria-label="配对码"/>},{key:"expires",label:"过期时间",children:time(pair?.expires||0)}]}/></Modal>
  <Modal open={!!editDevice} title={null} aria-label="家庭设备设置" onCancel={()=>setEditDevice(null)} confirmLoading={!!pending} okText="保存" cancelText="取消" onOk={()=>run("device",async()=>{const v=await deviceForm.validateFields();if(c&&editDevice){await save({...c,devices:[...c.devices.filter(p=>p.mac!==editDevice.mac),{...editDevice,...v}]});setEditDevice(null)}})}><Form form={deviceForm} layout="vertical"><Form.Item label="名称" name="name" rules={[{max:64}]}><Input maxLength={64}/></Form.Item><Form.Item label="备注" name="note"><Input.TextArea maxLength={300}/></Form.Item><Form.Item label="分组" name="group"><Input maxLength={40}/></Form.Item><Form.Item label="固定 IPv4 地址" name="fixed_ip"><Input placeholder="192.168.77.100"/></Form.Item><Form.Item label="禁止上网" name="blocked" valuePropName="checked"><Switch/></Form.Item><Tooltip title={gateway?.capabilities.limits?undefined:"流量限制尚未通过实机验证"}><div className="home-limit-fields">{[["speed_bps","速率上限（bit/s）"],["total_bytes","累计额度（字节）"],["daily_bytes","每日额度（字节）"]].map(([name,label])=><Form.Item key={name} label={label} name={name}><InputNumber min={0} precision={0} disabled={!gateway?.capabilities.limits} style={{width:"100%"}}/></Form.Item>)}</div></Tooltip></Form></Modal>
  <Modal open={!!editRule} title={null} aria-label="访问规则设置" onCancel={()=>setEditRule(null)} confirmLoading={!!pending} okText="保存" cancelText="取消" onOk={()=>run("rule",async()=>{const v=await ruleForm.validateFields();if(c&&editRule){const r={...editRule,...v,no_resolve:["ip","cidr","geoip"].includes(v.target_type)?!!v.no_resolve:false,subject:v.scope==="all"?"":v.subject,target:v.target_type==="any"?"":v.target_type==="geoip"?v.target.toUpperCase():v.target,node:v.action==="proxy"?v.node:""};await save(withNodes({...c,rules:[...c.rules.filter(p=>p.id!==editRule.id),r]}));setEditRule(null)}})}><Form form={ruleForm} layout="vertical"><Form.Item label="设备范围" name="scope"><Select options={[{label:"全部设备",value:"all"},{label:"指定设备",value:"device"},{label:"设备组",value:"group"}]}/></Form.Item>{subject!=="all"&&<Form.Item label={subject==="group"?"设备组":"设备"} name="subject" rules={[{required:true}]}>{subject==="group"?<Select options={[...new Set(c?.devices.map(p=>p.group).filter(Boolean))].map(g=>({label:g,value:g}))}/>:<Select options={(devices.data||[]).map(d=>({label:`${d.name||d.mac} · ${d.ip}`,value:d.mac}))}/>}</Form.Item>}<Form.Item label="目标类型" name="target_type"><Select options={[{label:"域名",value:"domain"},{label:"域名及子域名",value:"suffix"},{label:"域名关键字",value:"keyword"},{label:"IP 地址",value:"ip"},{label:"IPv4/IPv6 网段",value:"cidr"},{label:"国家 IP（GEOIP）",value:"geoip"},{label:"全部目标",value:"any"}]}/></Form.Item>{targetType!=="any"&&<Form.Item label="目标地址" name="target" rules={[{required:true}]}><Input maxLength={253}/></Form.Item>}{["cidr","ip","geoip"].includes(targetType)&&<Form.Item label="不额外解析域名（no-resolve）" name="no_resolve" valuePropName="checked"><Switch/></Form.Item>}<div className="home-form-grid"><Form.Item label="端口" name="port"><Input placeholder="443 或 8000-9000"/></Form.Item><Form.Item label="协议" name="protocol"><Select options={[{label:"TCP / UDP",value:""},{label:"TCP",value:"tcp"},{label:"UDP",value:"udp"}]}/></Form.Item></div><Form.Item label="动作" name="action"><Select options={actions}/></Form.Item>{action==="proxy"&&<Form.Item label="VPS 节点" name="node" rules={[{required:true}]}><Select options={nodeOptions}/></Form.Item>}<Form.Item label="优先级" name="priority"><InputNumber min={0} max={99999} precision={0}/></Form.Item><Form.Item label="启用" name="enabled" valuePropName="checked"><Switch/></Form.Item></Form></Modal>
  <Modal open={wifiOpen} title={null} aria-label="Wi-Fi 设置" onCancel={()=>{setWiFiOpen(false);wifiForm.resetFields()}} confirmLoading={!!pending} okText="保存" cancelText="取消" onOk={()=>run("wifi",async()=>{const values=await wifiForm.validateFields();const settings={...values,band5:values.band5||wifi!.band5};if(!await ui.confirm(wifi?.band2.ssid||gateway?.name||"Wi-Fi","修改 Wi-Fi","名称或密码改变后，需要重新连接 Wi-Fi"))return;await write(base+"/tasks","POST",{kind:"wifi.set",payload:{settings:JSON.stringify(settings)}});setWiFiOpen(false);wifiForm.resetFields();setTab("tasks");await invalidate();ui.notify("无线修改已提交，等待 AP 实际确认")})}>
   <Form form={wifiForm} layout="vertical">
    <Form.Item label="双频合一" name="unified" valuePropName="checked"><Switch/></Form.Item>
    {(["band2",...(unified?[]:["band5"])] as ("band2"|"band5")[]).map((band,index)=><div key={band} className="home-form-grid">
     <Form.Item label={unified?"无线开关":index===0?"2.4G 开关":"5G 开关"} name={[band,"enabled"]} valuePropName="checked"><Switch/></Form.Item>
     <Form.Item label={unified?"Wi-Fi 名称":index===0?"2.4G 名称":"5G 名称"} name={[band,"ssid"]} rules={[{required:true,validator:(_,v)=>new TextEncoder().encode(v||"").length>0&&new TextEncoder().encode(v||"").length<=32?Promise.resolve():Promise.reject(new Error("名称须为 1–32 字节"))}]}><Input autoComplete="off" maxLength={32}/></Form.Item>
     <Form.Item label={unified?"新密码（留空保留）":index===0?"2.4G 新密码（留空保留）":"5G 新密码（留空保留）"} name={index===0?"key2":"key5"} rules={[{pattern:/^$|^[\x20-\x7e]{8,63}$/,message:"密码须为 8–63 个英文、数字或符号"}]}><Input.Password autoComplete="new-password" maxLength={63}/></Form.Item>
     <Form.Item label={index===0?"2.4G 信道":"5G 信道"} name={[band,"channel"]}><Select options={(index===0?[0,1,2,3,4,5,6,7,8,9,10,11,12,13]:[0,36,40,44,48,149,153,157,161,165]).map(n=>({label:n===0?"自动":String(n),value:n}))}/></Form.Item>
    </div>)}
    <Descriptions column={1} items={[{key:"guest",label:"访客网络",children:wifi?.guest_supported?"可用":"桥接模式下尚未验证"}]}/>
   </Form>
  </Modal>
  <Modal open={advanced} width={780} confirmLoading={!!pending} title={null} aria-label="规则高级模式" onCancel={()=>{setAdvanced(false);setPreview(null)}} footer={<Space><Button disabled={!!pending} onClick={()=>{setAdvanced(false);setPreview(null)}}>取消</Button><Button loading={pending==="preview-rules"} disabled={!!pending||!c} onClick={()=>run("preview-rules",async()=>{const v=await advancedForm.validateFields();setPreview(null);setPreview(await write<RulePreview>(base+"/rules/preview","POST",{...v,subject:v.scope==="all"?"":v.subject,revision:c!.revision}))})}>预览</Button><Button type="primary" loading={pending==="apply-rules"} disabled={!!pending||!preview||!preview.changes.length||preview.config.revision!==c?.revision} onClick={()=>run("apply-rules",async()=>{if(!preview)return;if(!await ui.confirm(gateway!.name,"应用高级规则",`新增 ${preview.changes.filter(v=>v.operation!=="delete").length} 条，删除 ${preview.changes.filter(v=>v.operation==="delete").length} 条`))return;await save(preview.config);setAdvanced(false);setPreview(null)})}>应用</Button></Space>}>
   <Form form={advancedForm} disabled={!!pending} layout="vertical" onValuesChange={()=>setPreview(null)}>
    <Form.Item label="设备范围" name="scope"><Select onChange={()=>advancedForm.setFieldValue("subject","")} options={[{label:"全部设备",value:"all"},{label:"指定设备",value:"device"},{label:"设备组",value:"group"}]}/></Form.Item>
    {advancedScope!=="all"&&<Form.Item label={advancedScope==="group"?"设备组":"设备"} name="subject" rules={[{required:true}]}><Select options={advancedScope==="group"?[...new Set(c?.devices.map(p=>p.group).filter(Boolean))].map(g=>({label:g,value:g})):(devices.data||[]).map(d=>({label:`${d.name||d.mac} · ${d.ip}`,value:d.mac}))}/></Form.Item>}
    <Form.Item label="规则 YAML" name="source" rules={[{required:true}]}><Input.TextArea className="home-rule-source" autoComplete="off" spellCheck={false} rows={12} maxLength={65536}/></Form.Item>
   </Form>
   {preview&&<Table rowKey={r=>r.operation+r.line} size="small" dataSource={preview.changes} locale={{emptyText:"没有规则变更"}} scroll={{x:440}} pagination={{pageSize:8,showSizeChanger:false}} columns={[{title:"操作",dataIndex:"operation",render:v=>({prepend:"前置",append:"追加",delete:"删除"}[v as string]||v)},{title:"规则",dataIndex:"line",render:v=><span className="mono">{v}</span>}]}/>}
  </Modal>
  <Modal open={history} title={null} aria-label="恢复家庭网关配置" footer={null} onCancel={()=>setHistory(false)}><Table rowKey="revision" dataSource={versions.data||[]} pagination={{pageSize:8}} columns={[{title:"配置版本",dataIndex:"revision",render:v=>`r${v}`},{title:"保存时间",dataIndex:"created",render:time},{title:"操作",key:"restore",render:(_,v)=><Button disabled={!!pending||v.revision===c?.revision} onClick={()=>run("restore",async()=>{if(c&&await ui.confirm(`r${v.revision}`,"恢复家庭配置")){await write(base+"/rollback","POST",{revision:v.revision,current:c.revision});await invalidate();setHistory(false);setWaiting({id,revision:c.revision+1})}})}>恢复</Button>}]}/></Modal>
 </div>
}
