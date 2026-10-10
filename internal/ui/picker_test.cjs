// Dependency-free behavior tests for the actual embedded UI script/bridge.
// These tests do not replace an end-to-end check inside a ChatGPT MCP Apps host.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const html=fs.readFileSync(path.join(__dirname,'picker.html'),'utf8');
const script=html.match(/<script>([\s\S]*?)<\/script>/)[1];
const pause=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(fn){for(let i=0;i<200;i++){if(fn())return;await pause(5)}throw new Error('UI condition timed out')}
function deferred(){let resolve;const promise=new Promise(r=>{resolve=r});return {promise,resolve}}
const selection=(repo='one',branch='main')=>({owner:'owner',repo,branch,repository_id:repo==='one'?1:2});
const repo=(name)=>({name,full_name:'owner/'+name,owner:{login:'owner'},private:true,permissions:{push:true}});

function harness(t,options={}){
 const elements=new Map(),calls=[],timers=new Set();let receive;
 const document={activeElement:null,documentElement:{getBoundingClientRect:()=>({height:360})}};
 function element(id=''){
  const e={id,value:'',textContent:'',hidden:false,disabled:false,children:[],attrs:{},classes:new Set(),setAttribute(k,v){this.attrs[k]=v},replaceChildren(){this.children=[]},append(child){this.children.push(child)},focus(){document.activeElement=this},querySelectorAll(){return this.children.filter(x=>!x.disabled&&x.attrs.role==='option')},classList:{toggle(k,on){if(on)e.classes.add(k);else e.classes.delete(k)}}};return e;
 }
 for(const match of html.matchAll(/id="([^"]+)"/g)){const e=element(match[1]);e.hidden=match[1].endsWith('popover')||match[1].endsWith('more');elements.set(match[1],e)}
 document.getElementById=id=>elements.get(id);document.createElement=()=>element();document.body=element();
 const get=id=>elements.get(id);
 const defaults={
  get_selection:()=>({available:true,selection:selection()}),
  list_repositories:()=>({repositories:[repo('one'),repo('two')],next_page:-1}),
  list_branches:()=>({branches:[{name:'main'},{name:'feature',protected:true}],next_page:-1}),
  select_repository:args=>({available:true,selection:selection(args.repo,args.branch||(args.repo==='two'?'feature':'main'))})
 };
 const parent={postMessage:async msg=>{
  calls.push(msg);if(msg.id===undefined)return;
  try{let result={};if(msg.method==='tools/call'){const name=msg.params.name;const out=await (options[name]||defaults[name]||(()=>({})))(msg.params.arguments);result={structuredContent:out}};
   queueMicrotask(()=>receive({source:parent,data:{jsonrpc:'2.0',id:msg.id,result}}));
  }catch(error){queueMicrotask(()=>receive({source:parent,data:{jsonrpc:'2.0',id:msg.id,error:{message:error.message}}}))}
 }};
 const window={parent,addEventListener:(type,fn)=>{if(type==='message')receive=fn},GHF_DEBUG_CONTEXT:options.debug===true};
 // Host waits (3 s result settle, 60 s RPC timeout) run 100 times faster here.
 const scaled=ms=>ms>=1000?ms/100:ms;
 vm.runInNewContext(script,{document,window,Map,Set,JSON,Promise,Math,Error,Object,console,setTimeout:(fn,ms)=>{const id=setTimeout(()=>{timers.delete(id);fn()},scaled(ms));timers.add(id);return id},clearTimeout:id=>{timers.delete(id);clearTimeout(id)}});
 t.after(()=>{for(const timer of timers)clearTimeout(timer)});
 // send delivers a host notification, e.g. ui/notifications/tool-result.
 const send=(method,params)=>receive({source:parent,data:{jsonrpc:'2.0',method,params}});
 return {get,calls,document,send,toolCalls:name=>calls.filter(x=>x.method==='tools/call'&&x.params.name===name)};
}

test('restores server preference and publishes explicit per-chat context',async t=>{
 const h=harness(t);await until(()=>h.get('repo-label').textContent==='owner/one');await until(()=>h.calls.some(x=>x.method==='ui/update-model-context'));
 assert.equal(h.get('branch-label').textContent,'main');const update=h.calls.find(x=>x.method==='ui/update-model-context');assert.equal(update.params.structuredContent.githubSelection.repo,'one');
 assert.equal(h.toolCalls('open_repository_picker').length,0,'UI must not reopen its own tool');
 assert.equal(h.calls.filter(x=>x.method==='ui/notifications/size-changed').length,1,'unchanged height must not spam host');
});

test('selecting another repository restores its branch and persists only via server',async t=>{
 const h=harness(t);await until(()=>h.get('repo-label').textContent==='owner/one');await h.get('repo-results').children[1].onclick();
 assert.equal(h.get('repo-label').textContent,'owner/two');assert.equal(h.get('branch-label').textContent,'feature');assert.equal(h.toolCalls('select_repository').length,1);
 const args=h.toolCalls('select_repository')[0].params.arguments;assert.equal(args.owner,'owner');assert.equal(args.repo,'two');assert.equal('branch' in args,false);
});

test('failed save keeps previous choice and reports the error',async t=>{
 const h=harness(t,{select_repository:()=>{throw new Error('repository is no longer accessible')}});await until(()=>h.get('repo-label').textContent==='owner/one');await h.get('repo-results').children[1].onclick();
 assert.equal(h.get('repo-label').textContent,'owner/one');assert.match(h.get('status').textContent,/no longer accessible/);assert.equal(h.get('repo-toggle').disabled,false);
});

test('concurrent clicks cannot submit two selections',async t=>{
 const saved=deferred();const h=harness(t,{select_repository:()=>saved.promise});await until(()=>h.get('repo-label').textContent==='owner/one');const a=h.get('repo-results').children[1].onclick();const b=h.get('repo-results').children[0].onclick();await b;
 assert.equal(h.toolCalls('select_repository').length,1);saved.resolve({available:true,selection:selection('two')});await a;
});

test('newer search wins over a slow previous result; old options cannot be clicked while loading',async t=>{
 const slow=deferred();const h=harness(t,{list_repositories:args=>args.query==='old'?slow.promise:{repositories:[repo(args.query||'one')],next_page:-1}});await until(()=>h.get('repo-label').textContent==='owner/one');
 h.get('repo-search').value='old';h.get('repo-search').oninput();assert.equal(h.get('repo-results').children[0].disabled,true);await until(()=>h.toolCalls('list_repositories').some(x=>x.params.arguments.query==='old'));
 h.get('repo-search').value='new';h.get('repo-search').oninput();await until(()=>h.get('repo-results').children[0]?.textContent==='owner/new');slow.resolve({repositories:[repo('old')],next_page:-1});await pause(20);
 assert.equal(h.get('repo-results').children[0].textContent,'owner/new');
});

test('repository names are rendered as text and keyboard navigation works',async t=>{
 const malicious='<img src=x onerror=alert(1)>';const h=harness(t,{list_repositories:()=>({repositories:[repo(malicious),repo('two')],next_page:-1})});await until(()=>h.get('repo-label').textContent==='owner/one');
 const options=h.get('repo-results').children;assert.equal(options[0].textContent,'owner/'+malicious);assert.equal(options[0].children.length,1,'only a static metadata hint is appended');
 const event=key=>({key,preventDefault(){}});h.get('repo-popover').onkeydown(event('ArrowDown'));assert.equal(h.document.activeElement,options[0]);h.get('repo-popover').onkeydown(event('End'));assert.equal(h.document.activeElement,options[1]);h.get('repo-popover').onkeydown(event('Escape'));assert.equal(h.document.activeElement,h.get('repo-toggle'));assert.equal(h.get('repo-popover').hidden,true);
});

const KEY='c_0123456789abcdef0123456789abcdef',OTHER='c_fedcba9876543210fedcba9876543210';
const pickerResult=(chat)=>({structuredContent:{available:true,selection:selection(),chat,repositories:{repositories:[repo('one'),repo('two')],next_page:-1},branches:{branches:[{name:'main'}],next_page:-1}},_meta:chat?{ghf_chat:chat}:undefined});

test('late tool-result: the card waits for it and its choice carries the chat key',async t=>{
 const h=harness(t);
 await until(()=>h.calls.some(x=>x.method==='ui/notifications/initialized'));
 await pause(10);h.send('ui/notifications/tool-result',pickerResult(KEY));
 await until(()=>h.get('repo-label').textContent==='owner/one');
 assert.equal(h.toolCalls('get_selection').length,0,'card must wait for the rendering result instead of calling get_selection');
 await h.get('repo-results').children[1].onclick();
 const args=h.toolCalls('select_repository')[0].params.arguments;assert.equal(args.chat,KEY);
 const update=h.calls.filter(x=>x.method==='ui/update-model-context').at(-1).params;
 assert.equal(update.structuredContent.repositoryChosenInCard,true);assert.equal(update.structuredContent.chat,KEY);
 assert.match(update.content[0].text,/select_repository/);assert.match(update.content[0].text,new RegExp(KEY));
 for(const call of h.calls.filter(x=>x.method==='tools/call'))assert.equal(call.params.arguments.chat,KEY,call.params.name+' left the chat');
});

test('chat key from tool-input wins over a later result key',async t=>{
 const h=harness(t);
 await until(()=>h.calls.some(x=>x.method==='ui/notifications/initialized'));
 h.send('ui/notifications/tool-input',{arguments:{chat:KEY}});
 h.send('ui/notifications/tool-result',pickerResult(OTHER));
 await until(()=>h.get('repo-label').textContent==='owner/one');
 await h.get('repo-results').children[1].onclick();
 assert.equal(h.toolCalls('select_repository')[0].params.arguments.chat,KEY);
});

test('without any key the card sends chat "none" and the model applies the choice',async t=>{
 const h=harness(t);await until(()=>h.get('repo-label').textContent==='owner/one');
 assert.equal(h.toolCalls('get_selection')[0].params.arguments.chat,'none');
 await h.get('repo-results').children[1].onclick();
 assert.equal(h.toolCalls('select_repository')[0].params.arguments.chat,'none');
 const update=h.calls.filter(x=>x.method==='ui/update-model-context').at(-1).params;
 assert.equal(update.structuredContent.repositoryChosenInCard,true);assert.equal('chat' in update.structuredContent,false);
});

test('diagnostics report host messages and the key source, never the key',async t=>{
 const h=harness(t,{debug:true});
 await until(()=>h.calls.some(x=>x.method==='ui/notifications/initialized'));
 h.send('ui/notifications/tool-input',{arguments:{chat:KEY}});
 h.send('ui/notifications/tool-result',pickerResult(KEY));
 await until(()=>h.toolCalls('ghf_debug_client_context').some(x=>x.params.arguments.context.event==='settled'));
 const report=h.toolCalls('ghf_debug_client_context').find(x=>x.params.arguments.context.event==='settled').params.arguments.context;
 assert.equal(report.key_source,'tool-input');assert.equal(report.has_key,true);assert.equal(report.has_result,true);
 assert.equal(JSON.stringify(report.received),JSON.stringify(['ui/notifications/tool-input','ui/notifications/tool-result']));
 assert.equal(JSON.stringify(h.toolCalls('ghf_debug_client_context')).includes(KEY),false,'diagnostics must not carry the chat key');
});

test('without diagnostics the card never calls the diagnostics tool',async t=>{
 const h=harness(t);await until(()=>h.get('repo-label').textContent==='owner/one');
 assert.equal(h.toolCalls('ghf_debug_client_context').length,0);
});
