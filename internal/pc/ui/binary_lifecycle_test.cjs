const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path');
const {spawn} = require('node:child_process');
const readline = require('node:readline');
const crypto = require('node:crypto');
const {harness} = require('./host_harness.cjs');
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(fn) {for(let i=0;i<1000;i++){if(fn())return;await pause(5);}throw Error('live UI timed out');}
test('built 1.1.8 binary renders stdout and stderr via its embedded approval bridge', async t => {
 const tmp=fs.mkdtempSync(path.join(os.tmpdir(),'pc-live-bridge-'));
 const root=path.join(tmp,'root'), state=path.join(tmp,'state');fs.mkdirSync(root);
 const env=Object.fromEntries(Object.entries(process.env).filter(([k])=>!k.startsWith('PC_MCP_')&&!k.startsWith('CONTROL_PLANE_')));
 const server=spawn(process.env.PC_MCP_UI_TEST_BINARY || './pc-mcp',['--transport','stdio','--root',root,'--state',state],{env,stdio:['pipe','pipe','pipe']});
 let stderr='';server.stderr.on('data',b=>{stderr+=b});
 const pending=new Map();let serial=0;
 readline.createInterface({input:server.stdout}).on('line',line=>{
  const m=JSON.parse(line),p=pending.get(m.id);
  if(p){pending.delete(m.id);clearTimeout(p.timer);m.error?p.reject(Error(m.error.message)):p.resolve(m.result);}
 });
 server.on('exit',code=>{for(const p of pending.values())p.reject(Error('server exited '+code+': '+stderr));pending.clear()});
 t.after(()=>{server.kill('SIGTERM');fs.rmSync(tmp,{recursive:true,force:true});});
 const rpc=(method,params)=>new Promise((resolve,reject)=>{
  const id=++serial,timer=setTimeout(()=>reject(Error('MCP RPC timed out: '+method)),10000);
  pending.set(id,{resolve,reject,timer});server.stdin.write(JSON.stringify({jsonrpc:'2.0',id,method,params})+'\n');
 });
 const call=(name,args={})=>rpc('tools/call',{name,arguments:args});
 const init=await rpc('initialize',{protocolVersion:'2025-06-18',capabilities:{},clientInfo:{name:'binary-bridge-probe',version:'1'}});
 assert.equal(init.serverInfo.version,'1.1.8');
 server.stdin.write(JSON.stringify({jsonrpc:'2.0',method:'notifications/initialized'})+'\n');
 const caps=(await call('pc_capabilities')).structuredContent;
 assert.equal(caps.server_version,'1.1.8');assert.equal(caps.tool_schema_version,6);
 const tools=await rpc('tools/list',{});
 const uri=tools.tools.find(x=>x.name==='pc_request_run')._meta.ui.resourceUri;
 assert.equal(uri,'ui://pc-mcp/approve-v13.html');assert.equal(caps.approval_uri,uri);
 const resource=await rpc('resources/read',{uri});const html=resource.contents[0].text;
 assert.equal(crypto.createHash('sha256').update(html).digest('hex'),caps.approval_html_sha256);
 const initial=await call('pc_request_run',{directory:'.',branch:'',purpose:'smoke',seconds:30,
  args:['bash','-lc',"printf 'PC_BINARY_STDOUT\\n'\nprintf 'PC_BINARY_STDERR\\n' >&2\nsleep 1\nprintf 'PC_BINARY_DONE\\n'"]});
 assert.equal(initial.structuredContent.status,'awaiting_approval');
 const states=[];
 const h=harness(t,'approve.html',initial,{
  pc_approve_run:a=>call('pc_approve_run',a),
  pc_job_status:async a=>{const r=await call('pc_job_status',a);states.push(r.structuredContent.status);return r;}
 },{resourceURI:uri,resourceCache:new Map([[uri,html]]),echoToolResults:true});
 await until(()=>!h.get('approve').disabled);
 await h.get('approve').onclick();
 assert.equal(h.get('approve').disabled,true);
 await until(()=>h.get('status').textContent.includes('(succeeded)'));
 assert.equal(h.get('output').hidden,false);
 assert.match(h.get('output').textContent,/\[stdout\] PC_BINARY_STDOUT/);
 assert.match(h.get('output').textContent,/\[stderr\] PC_BINARY_STDERR/);
 assert.match(h.get('output').textContent,/\[stdout\] PC_BINARY_DONE/);
 assert.equal(h.tools('pc_job_output').length,0);assert.equal(h.hasPoll(),false);
 const context=h.calls.filter(x=>x.method==='ui/update-model-context').at(-1).params.structuredContent.pcJob;
 assert.equal(context.uiVersion,'1.1.8');assert.equal(context.renderedRecords,3);
 assert.equal(context.renderedStdout,2);assert.equal(context.renderedStderr,1);
 assert.deepEqual(context.command,initial.structuredContent.request.args);
 assert.match(context.console,/PC_BINARY_STDOUT/);assert.match(context.console,/PC_BINARY_STDERR/);
 assert.equal(context.consoleTruncated,false);
 assert.equal(JSON.stringify(h.calls.filter(x=>x.method==='ui/update-model-context')).includes(initial._meta.approval_nonce),false);
 assert.ok(states.includes('awaiting_approval')&&states.includes('running')&&states.includes('succeeded'));
 console.log(JSON.stringify({server_version:caps.server_version,tool_schema_version:caps.tool_schema_version,
  approval_uri:uri,approval_html_sha256:caps.approval_html_sha256,states,context,console:h.get('output').textContent}));
});
