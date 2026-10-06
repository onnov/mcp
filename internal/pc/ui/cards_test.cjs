const {test}=require('node:test');const assert=require('node:assert/strict');const fs=require('node:fs');const vm=require('node:vm');const path=require('node:path');
const pause=ms=>new Promise(r=>setTimeout(r,ms));async function until(fn){for(let i=0;i<200;i++){if(fn())return;await pause(5)}throw Error('UI condition timed out')}
function harness(t,file,initial,handlers={}){const html=fs.readFileSync(path.join(__dirname,file),'utf8').replace('/*BRIDGE*/',fs.readFileSync(path.join(__dirname,'bridge.js'),'utf8'));const scripts=[...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map(m=>m[1]);const elements=new Map(),calls=[],timers=new Set();let receive;
 const element=(id='')=>{const e={id,value:'',textContent:'',hidden:false,disabled:false,children:[],append(c){this.children.push(c);if(!this.value&&c.value!==undefined)this.value=c.value},replaceChildren(){this.children=[];this.value=''},focus(){},classList:{toggle(){}}};Object.defineProperty(e,'childNodes',{get:()=>e.children});return e};for(const m of html.matchAll(/<[^>]*\bid="([^"]+)"[^>]*>/g)){const e=element(m[1]);e.disabled=/\bdisabled\b/.test(m[0]);e.hidden=/\bhidden\b/.test(m[0]);elements.set(m[1],e)}const document={getElementById:id=>elements.get(id),createElement:()=>element(),documentElement:{getBoundingClientRect:()=>({height:400})},body:element()};
 const parent={postMessage:async msg=>{calls.push(msg);if(msg.id===undefined)return;try{if(msg.method==='ui/initialize'&&initial)receive({source:parent,data:{jsonrpc:'2.0',method:'ui/notifications/tool-result',params:initial}});let result={};if(msg.method==='tools/call'){if(!handlers[msg.params.name])throw Error('Unexpected tool '+msg.params.name);result={structuredContent:await handlers[msg.params.name](msg.params.arguments)}}queueMicrotask(()=>receive({source:parent,data:{jsonrpc:'2.0',id:msg.id,result}}))}catch(e){queueMicrotask(()=>receive({source:parent,data:{jsonrpc:'2.0',id:msg.id,error:{message:e.message}}}))}}};
 const window={parent,addEventListener:(type,fn)=>{if(type==='message')receive=fn}};const context={window,parent,document,console,Map,JSON,Promise,Math,Error,setTimeout:(fn,ms)=>{const id=setTimeout(()=>{timers.delete(id);fn()},ms);timers.add(id);return id},clearTimeout:id=>{clearTimeout(id);timers.delete(id)}};vm.createContext(context);for(const s of scripts){vm.runInContext(s,context);if(window.PC)context.PC=window.PC};t.after(()=>{for(const id of timers)clearTimeout(id)});return {get:id=>elements.get(id),calls,tools:name=>calls.filter(x=>x.method==='tools/call'&&x.params.name===name)}}
const request={directory:'project',branch:'feature',cwd:'.',args:['bash','-lc','echo hello'],purpose:'smoke',network:false,seconds:30,credential:false};
test('approval never starts a job automatically and keeps nonce out of model context',async t=>{const job={id:'job-one',request,status:'awaiting_approval'};const h=harness(t,'approve.html',{structuredContent:job,_meta:{approval_nonce:'private-nonce'}},{pc_approve_run:a=>{assert.equal(a.approval_nonce,'private-nonce');return {...job,status:'running'}},pc_job_status:()=>({...job,status:'succeeded',output:{cursor:1,head:[{stream:'stdout',text:'ok'}],tail:[],omitted:0}})});await until(()=>!h.get('approve').disabled);assert.equal(h.tools('pc_approve_run').length,0);await h.get('approve').onclick();await until(()=>h.get('status').textContent==='succeeded');assert.equal(h.tools('pc_approve_run').length,1);for(const c of h.calls.filter(x=>x.method==='ui/update-model-context'))assert.equal(JSON.stringify(c).includes('private-nonce'),false);assert.match(h.get('output').textContent,/stdout: ok/)});
test('missing private metadata fails closed',async t=>{const h=harness(t,'approve.html',{structuredContent:{id:'job',request,status:'awaiting_approval'}},{});await until(()=>h.get('status').textContent.includes('приватные'));assert.equal(h.get('approve').disabled,true);assert.equal(h.tools('pc_approve_run').length,0)});
test('picker restores directory without switching branch; new branch selection is explicit',async t=>{const s={directory:'project',branch:'feature',git:true,branches:['main','feature'],dirty:false};const h=harness(t,'picker.html',{structuredContent:{selection:s}},{pc_list_directory:()=>({entries:[],next_offset:-1}),pc_inspect_workspace:()=>s,pc_select_workspace:a=>({...s,branch:a.branch})});await until(()=>h.get('saved').textContent.includes('project'));await until(()=>h.tools('pc_list_directory').length===1);assert.equal(h.tools('pc_select_workspace').length,0);await h.get('candidate').onclick();h.get('branch').value='__pc_new__';h.get('branch').onchange();h.get('new-branch').value='new-feature';await h.get('apply').onclick();const q=h.tools('pc_select_workspace')[0].params.arguments;assert.equal(q.directory,'project');assert.equal(q.branch,'new-feature');assert.equal(q.create,true);assert.equal(q.base_branch,'feature');assert.match(h.get('saved').textContent,/new-feature/)});
test('non-Git remembered directory is published to the new chat',async t=>{const s={available:true,directory:'plain',branch:'',git:false,branches:[]};const h=harness(t,'picker.html',{structuredContent:{selection:s}},{pc_list_directory:()=>({entries:[],next_offset:-1})});await until(()=>h.calls.some(c=>c.method==='ui/update-model-context'));const c=h.calls.find(c=>c.method==='ui/update-model-context');assert.equal(c.params.structuredContent.pcWorkspace.directory,'plain');assert.equal(c.params.structuredContent.pcWorkspace.branch,'')});

test('directory listing opens a browser at the requested path and navigates beyond tree depth',async t=>{
 const remembered={available:true,remembered:true,directory:'WB/old-project',branch:'feature',git:true,branches:['feature']};
 let saved;
 const h=harness(t,'picker.html',{structuredContent:{entries:[],next_offset:-1},_meta:{pc_browser_path:'AI'}},{
  pc_get_workspace:()=>remembered,
  pc_list_directory:a=>({entries:[{name:'child',path:a.path+'/child',directory:true,symlink:false},{name:'alias',path:a.path+'/alias',directory:true,symlink:true}],next_offset:-1}),
  pc_inspect_workspace:a=>({available:true,directory:a.directory,branch:'',git:false,branches:[]}),
  pc_select_workspace:a=>(saved={available:true,remembered:true,directory:a.directory,branch:'',git:false,branches:[]})
 });
 await until(()=>h.get('location').textContent==='Открыто: AI'&&!h.get('candidate').disabled);
 assert.equal(h.tools('pc_list_directory')[0].params.arguments.path,'AI');
 assert.equal(h.get('rows').children.length,1,'symlink aliases must not be selectable');
 for(let i=0;i<8;i++)await h.get('rows').children[0].onclick();
 const deep='AI'+('/child'.repeat(8));
 assert.equal(h.get('location').textContent,'Открыто: '+deep);
 assert.equal(h.tools('pc_select_workspace').length,0,'browsing must not change the saved selection');
 const buttons=h.get('breadcrumbs').children.filter(x=>typeof x.onclick==='function');
 assert.equal(buttons.length,9);
 assert.equal(buttons.at(-1).disabled,true,'current folder is indicated in breadcrumbs');
 await buttons[0].onclick();assert.equal(h.get('location').textContent,'Открыто: AI');
 await h.get('rows').children[0].onclick();await h.get('candidate').onclick();
 assert.equal(h.get('branch').disabled,true,'non-Git branch control remains disabled');
 await h.get('apply').onclick();assert.equal(saved.directory,'AI/child');
 assert.equal(saved.branch,'');
 const updates=h.calls.filter(x=>x.method==='ui/update-model-context');
 assert.equal(updates.at(-1).params.structuredContent.pcWorkspace.directory,'AI/child');
});

test('tree result and explicit picker path override the remembered browsing location',async t=>{
 for(const initial of [
  {structuredContent:{directories:[{path:'SAMOKAT',depth:0}],truncated:false},_meta:{pc_browser_path:'SAMOKAT'}},
  {structuredContent:{selection:{available:true,remembered:true,directory:'WB/old',branch:'main'},browser_path:'SAMOKAT'}}
 ]){
  const h=harness(t,'picker.html',initial,{pc_get_workspace:()=>({available:true,remembered:true,directory:'WB/old',branch:'main'}),pc_list_directory:()=>({entries:[],next_offset:-1})});
  await until(()=>h.tools('pc_list_directory').length===1);
  assert.equal(h.tools('pc_list_directory')[0].params.arguments.path,'SAMOKAT');
  assert.equal(h.tools('pc_select_workspace').length,0);
 }
});
