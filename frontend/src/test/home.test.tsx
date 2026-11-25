import { afterEach, expect, it, vi } from "vitest";
import { render, screen, waitFor, within, fireEvent } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { HomePage } from "../pages/Home";
import { UIProvider } from "../components";
import { setCSRF } from "../api";

afterEach(() => vi.unstubAllGlobals());

it("saves the selected device identity and a device-specific denial while showing pending application", async () => {
  const a = "52:54:00:77:01:01", b = "52:54:00:77:01:02";
  let config = {revision:1,default:"direct",default_node:"",devices:[] as any[],rules:[] as any[],nodes:[]};
  const saved: typeof config[] = [];
  setCSRF("home-test-csrf");
  vi.stubGlobal("fetch",vi.fn(async(input:string,options:RequestInit={})=>{
    const path = new URL(input,"https://example.test").pathname;
    let data:unknown = [];
    if(path==="/api/home")data=[{id:"test",name:"测试网关",online:false,paired:true,enabled:true,last_seen:0,version:"test",applied:1,revision:config.revision,error:"",status:{proxy_ready:true},capabilities:{mode:"isolated",wifi:false,limits:false,upgrade:false}}];
    if(path.endsWith("/nodes"))data=[];
    if(path.endsWith("/devices"))data=[{mac:a,name:"设备 A",ip:"192.168.77.101",online:false,last_seen:0,upload:10,download:20},{mac:b,name:"设备 B",ip:"192.168.77.102",online:false,last_seen:0,upload:30,download:40}];
    if(path.endsWith("/config")){
      if(options.method==="PUT"){
        expect(new Headers(options.headers).get("X-CSRF-Token")).toBe("home-test-csrf");
        saved.push(JSON.parse(String(options.body)));
        config={...saved[saved.length-1],revision:config.revision+1};
        data={revision:config.revision,state:"pending"};
      }else data=config;
    }
    if(path.endsWith("/overview"))data={started:0,until:0,totals:[],series:[],destinations:[]};
    return new Response(JSON.stringify(data),{headers:{"Content-Type":"application/json"}});
  }));
  const client=new QueryClient({defaultOptions:{queries:{retry:false,refetchOnWindowFocus:false}}});
  const view=render(<QueryClientProvider client={client}><UIProvider theme="light"><HomePage active/></UIProvider></QueryClientProvider>);
  const user=userEvent.setup();
  await user.click(await screen.findByRole("tab",{name:"家庭设备"}));
  const row=(await screen.findByText("设备 A")).closest("tr")!;
  await waitFor(()=>expect((within(row).getByRole("button",{name:"设置"}) as HTMLButtonElement).disabled).toBe(false));
  await user.click(within(row).getByRole("button",{name:"设置"}));
  const dialog=await screen.findByRole("dialog",{name:"家庭设备设置"});
  await user.clear(within(dialog).getByLabelText("名称"));
  await user.type(within(dialog).getByLabelText("名称"),"手机 A");
  await user.type(within(dialog).getByLabelText("备注"),"仅改这一台");
  expect((within(dialog).getByLabelText("速率上限（bit/s）") as HTMLInputElement).disabled).toBe(true);
  await user.click(within(dialog).getByRole("button",{name:"保存"}));
  await waitFor(()=>expect(saved).toHaveLength(1));
  expect(saved[0].devices).toHaveLength(1);
  expect(saved[0].devices[0]).toMatchObject({mac:a,name:"手机 A",note:"仅改这一台"});
  expect(saved[0].devices.some(p=>p.mac===b)).toBe(false);
  await user.click(screen.getByRole("tab",{name:"访问规则"}));
  await user.click(screen.getByRole("button",{name:"添加规则"}));
  const rule=await screen.findByRole("dialog",{name:"访问规则设置"});
  fireEvent.mouseDown(within(rule).getByLabelText("设备范围"));
  await user.click(await screen.findByTitle("指定设备"));
  fireEvent.mouseDown(within(rule).getByLabelText("设备"));
  await user.click(await screen.findByTitle("设备 A · 192.168.77.101"));
  await user.type(within(rule).getByLabelText("目标地址"),"example.com");
  await user.click(within(rule).getByRole("button",{name:"保存"}));
  await waitFor(()=>expect(saved).toHaveLength(2));
  expect(saved[1].rules[0]).toMatchObject({scope:"device",subject:a,target:"example.com",action:"reject",enabled:true});
  expect(saved[1].devices[0].note).toBe("仅改这一台");
  view.unmount();client.clear();
},20000);

it("previews advanced YAML without saving and invalidates the preview after editing",async()=>{
 const requests:{path:string;body:any}[]=[];
 const base={revision:1,default:"direct",default_node:"",devices:[],rules:[],nodes:[]};
 vi.stubGlobal("fetch",vi.fn(async(input:string,options:RequestInit={})=>{
  const path=new URL(input,"https://example.test").pathname;let data:unknown=[];
  if(path==="/api/home")data=[{id:"advanced",name:"网关",online:true,paired:true,enabled:true,last_seen:1,version:"test",applied:1,revision:1,error:"",status:{},capabilities:{mode:"physical",wifi:false,limits:false,upgrade:false}}];
  if(path.endsWith("/config"))data=base;
  if(path.endsWith("/rules/preview")){requests.push({path,body:JSON.parse(String(options.body))});data={config:{...base,rules:[{id:"preview-rule",scope:"all",subject:"",target_type:"keyword",target:"treeroot.top",action:"direct",priority:100,enabled:true}]},changes:[{operation:"prepend",line:"DOMAIN-KEYWORD,treeroot.top,DIRECT"}]};}
  if(path.endsWith("/overview"))data={started:0,until:0,totals:[],series:[],destinations:[]};
    return new Response(JSON.stringify(data),{headers:{"Content-Type":"application/json"}});
 }));
 const client=new QueryClient({defaultOptions:{queries:{retry:false,refetchOnWindowFocus:false}}});
 const view=render(<QueryClientProvider client={client}><UIProvider theme="light"><HomePage active/></UIProvider></QueryClientProvider>);
 const user=userEvent.setup();await user.click(await screen.findByRole("tab",{name:"访问规则"}));
 await user.click(await screen.findByRole("button",{name:"高级模式"}));
 const dialog=await screen.findByRole("dialog",{name:"规则高级模式"});
 const apply=within(dialog).getByRole("button",{name:/^应用$/});
 expect((apply as HTMLButtonElement).disabled).toBe(true);
 fireEvent.change(within(dialog).getByLabelText("规则 YAML"),{target:{value:"prepend: ['DOMAIN-KEYWORD,treeroot.top,DIRECT']\nappend: []\ndelete: []"}});
 await user.click(within(dialog).getByRole("button",{name:/^预览$/}));
 await within(dialog).findByText("DOMAIN-KEYWORD,treeroot.top,DIRECT");
 await waitFor(()=>expect((apply as HTMLButtonElement).disabled).toBe(false));
 expect(requests).toHaveLength(1);expect(requests[0].body).toMatchObject({scope:"all",subject:"",revision:1});
 fireEvent.change(within(dialog).getByLabelText("规则 YAML"),{target:{value:"prepend: ['DOMAIN,example.com,DIRECT']"}});
 await waitFor(()=>expect((apply as HTMLButtonElement).disabled).toBe(true));
 view.unmount();client.clear();
},20000);

it("preserves the hidden 5G settings and requires confirmation before submitting a Wi-Fi change",async()=>{
 const submissions:any[]=[];
 vi.stubGlobal("fetch",vi.fn(async(input:string,options:RequestInit={})=>{
  const path=new URL(input,"https://example.test").pathname;let data:unknown=[];
  if(path==="/api/home")data=[{id:"wifi",name:"网关",online:true,paired:true,enabled:true,last_seen:1,version:"test",applied:1,revision:1,error:"",status:{wifi:{online:true,unified:true,band2:{enabled:true,ssid:"fixture",secured:true,channel:0},band5:{enabled:true,ssid:"fixture",secured:true,channel:36},guest_supported:false}},capabilities:{mode:"isolated",wifi:true,limits:false,upgrade:false}}];
  if(path.endsWith("/config"))data={revision:1,default:"direct",default_node:"",devices:[],rules:[],nodes:[]};
  if(path.endsWith("/tasks")&&options.method==="POST"){submissions.push(JSON.parse(String(options.body)));data={state:"pending"}}
  if(path.endsWith("/overview"))data={started:0,until:0,totals:[],series:[],destinations:[]};
    return new Response(JSON.stringify(data),{headers:{"Content-Type":"application/json"}});
 }));
 const client=new QueryClient({defaultOptions:{queries:{retry:false,refetchOnWindowFocus:false}}});
 const view=render(<QueryClientProvider client={client}><UIProvider theme="light"><HomePage active/></UIProvider></QueryClientProvider>);
 const user=userEvent.setup();await user.click(await screen.findByRole("button",{name:"Wi-Fi 设置"}));
 const dialog=await screen.findByRole("dialog",{name:"Wi-Fi 设置"});
 await user.clear(within(dialog).getByLabelText("Wi-Fi 名称"));await user.type(within(dialog).getByLabelText("Wi-Fi 名称"),"fixture-next");
 await user.type(within(dialog).getByLabelText("新密码（留空保留）"),"fixture-only-key");
 await user.click(within(dialog).getByRole("button",{name:"保存"}));
 const confirm=await screen.findByRole("dialog",{name:"修改 Wi-Fi「fixture」"});expect(submissions).toHaveLength(0);
 await user.click(within(confirm).getByRole("button",{name:/^修改 Wi-Fi$/}));
 await waitFor(()=>expect(submissions).toHaveLength(1));
 const body=submissions[0];expect(body.kind).toBe("wifi.set");expect(JSON.parse(body.payload.settings)).toMatchObject({unified:true,band2:{ssid:"fixture-next"},band5:{ssid:"fixture",channel:36},key2:"fixture-only-key"});
 view.unmount();client.clear();
},20000);
