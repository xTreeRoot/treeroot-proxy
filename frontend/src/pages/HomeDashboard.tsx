import { useState } from "react";
import { Button, Descriptions, Segmented, Select, Table, Tag } from "antd";
import { ArrowDownUp, Cable, Globe2, Network, Route, Server } from "lucide-react";
import { Feedback, Metric, TrafficChart, Transfer, useRemote } from "../components";
import { bytes, date, speed } from "../lib";

type Total={route:string;upload:number;download:number;day_upload:number;day_download:number;speed_upload:number|null;speed_download:number|null;last_seen:number};
type Destination={target:string;action:string;connections:number};
type Overview={started:number;until:number;totals:Total[];series:{ts:number;direct:number;proxy:number}[];destinations:Destination[]};
const routeName=(route:string)=>route==="proxy"?"经过 VPS":route==="direct"?"直连 · 未经过 VPS":route==="reject"?"已禁止":"未分类";

export function HomeDashboard({id,active,records}:{id:string;active:boolean;records:(action:string)=>void}) {
 const [hours,setHours]=useState("24"),[route,setRoute]=useState("");
 const query=useRemote<Overview>(["home",id,"overview",hours],`/home/${id}/overview?hours=${hours}`,active,5000),data=query.data;
 const proxy=data?.totals.find(v=>v.route==="proxy"),direct=data?.totals.find(v=>v.route==="direct");
 const total=(v?:Total)=>v?bytes(v.upload+v.download):"—";
 const live=(v?:Total)=>v?.speed_upload!=null&&v.speed_download!=null?speed(v.speed_upload+v.speed_download):"—";
 const routePoints=data?.series.map(v=>({ts:v.ts,download:v.proxy,upload:v.direct}))||[];
 return <div className="page-stack home-dashboard">
  <Feedback loading={query.isPending} error={query.error} retry={()=>query.refetch()}/>
  <div className="metric-grid">
   <Metric icon={Server} label="累计经过 VPS" value={total(proxy)} note={proxy&&<Transfer upload={proxy.upload} download={proxy.download}/>}/>
   <Metric icon={Route} label="累计直连" value={total(direct)} color="green" note={direct&&<Transfer upload={direct.upload} download={direct.download}/>}/>
   <Metric icon={ArrowDownUp} label="24 小时上报流量" value={data?.totals.length?bytes(data.totals.reduce((n,v)=>n+v.day_upload+v.day_download,0)):"—"} color="purple"/>
   <Metric icon={Network} label="VPS 实时收发" value={live(proxy)}/>
   <Metric icon={Globe2} label="直连实时收发" value={live(direct)} color="green"/>
   <Metric icon={Cable} label="24 小时经过 VPS" value={proxy?bytes(proxy.day_upload+proxy.day_download):"—"} color="orange"/>
  </div>
  <article className="panel chart-panel" aria-label="家庭分流趋势">
   <div className="home-toolbar"><Segmented aria-label="家庭趋势范围" value={hours} onChange={v=>setHours(String(v))} options={[{label:"最近 24 小时",value:"24"},{label:"最近 7 天",value:"168"}]}/><span className="muted">按上报时间 · 每小时</span></div>
   {data?.started?<TrafficChart points={routePoints} seriesNames={["经过 VPS","直连"]} label="家庭经过 VPS 与直连流量趋势"/>:<div className="traffic-chart empty">等待家庭网关流量采样</div>}
   <Descriptions size="small" column={{xs:1,sm:2,lg:3}} items={[{key:"basis",label:"统计口径",children:"WAN IP 字节（含协议开销）"},{key:"start",label:"统计起始",children:data?.started?date(data.started):"尚未采样"},{key:"updated",label:"最后上报",children:data?.totals.length?date(Math.max(...data.totals.map(v=>v.last_seen))):"—"}]}/>
  </article>
  <article className="panel home-table" aria-label="家庭流量路径">
   <Table<Total> rowKey="route" pagination={false} dataSource={data?.totals||[]} scroll={{x:680}} columns={[{title:"流量路径",dataIndex:"route",render:v=><Tag color={v==="proxy"?"blue":"green"}>{routeName(v)}</Tag>},{title:"累计上传",dataIndex:"upload",render:bytes},{title:"累计下载",dataIndex:"download",render:bytes},{title:"24 小时上报",key:"day",render:(_,v)=>bytes(v.day_upload+v.day_download)},{title:"实时上传",dataIndex:"speed_upload",render:speed},{title:"实时下载",dataIndex:"speed_download",render:speed},{title:"操作",key:"actions",render:(_,v)=><Button size="small" onClick={()=>records(v.route)}>连接记录</Button>}]}/>
  </article>
  <article className="panel home-table" aria-label="家庭访问目标与路径">
   <div className="home-toolbar"><Select aria-label="筛选流量路径" value={route} onChange={setRoute} options={[{label:"全部路径",value:""},{label:"经过 VPS",value:"proxy"},{label:"直连 · 未经过 VPS",value:"direct"},{label:"已禁止",value:"reject"}]} style={{minWidth:210}}/><Button onClick={()=>records(route)}>查看连接记录</Button></div>
   <Table<Destination> rowKey={v=>v.action+v.target} loading={query.isPending} dataSource={(data?.destinations||[]).filter(v=>!route||v.action===route)} pagination={{pageSize:10,showSizeChanger:false}} scroll={{x:520}} columns={[{title:"目标",dataIndex:"target"},{title:"流量路径",dataIndex:"action",render:v=><Tag color={v==="proxy"?"blue":v==="direct"?"green":"red"}>{routeName(v)}</Tag>},{title:"连接数",dataIndex:"connections"}]}/>
  </article>
 </div>
}
