const {test}=require('node:test');const assert=require('node:assert/strict');
const pause=ms=>new Promise(r=>setTimeout(r,ms));async function until(fn){for(let i=0;i<200;i++){if(fn())return;await pause(5)}throw Error('UI condition timed out')}
const {harness}=require('./host_harness.cjs');
const request={directory:'project',branch:'feature',cwd:'.',args:['bash','-lc','echo hello'],purpose:'smoke',network:false,seconds:30,credential:false};
test('approval never starts a job automatically and keeps nonce out of model context',async t=>{const job={id:'job-one',request,status:'awaiting_approval'};let approved=false;const h=harness(t,'approve.html',{structuredContent:job,_meta:{approval_nonce:'private-nonce'}},{pc_approve_run:a=>{assert.equal(a.approval_nonce,'private-nonce');approved=true;return {...job,status:'running'}},pc_job_status:()=>({...job,status:approved?'succeeded':'awaiting_approval',exit_code:approved?0:undefined,output:{records:approved?[{sequence:1,stream:'stdout',text:'ok'}]:[],records_cursor:approved?1:0,more:false,cursor:1,head:[{sequence:1,stream:'stdout',text:'ok'}],tail:[],omitted:0,total_records:1,bytes:2}})});await until(()=>!h.get('approve').disabled);assert.equal(h.tools('pc_approve_run').length,0);await h.get('approve').onclick();await until(()=>h.get('status').textContent.includes('Команда выполнена успешно'));assert.equal(h.tools('pc_approve_run').length,1);for(const c of h.calls.filter(x=>x.method==='ui/update-model-context'))assert.equal(JSON.stringify(c).includes('private-nonce'),false);assert.match(h.get('output').textContent,/00001 \[stdout\] ok/);assert.match(h.get('metrics').textContent,/exit code: 0/)});
test('missing private metadata fails closed',async t=>{const h=harness(t,'approve.html',{structuredContent:{id:'job',request,status:'awaiting_approval'}},{pc_job_status:()=>({id:'job',request,status:'awaiting_approval'})});await until(()=>h.get('status').textContent.includes('приватные'));assert.equal(h.get('approve').disabled,true);assert.equal(h.tools('pc_approve_run').length,0)});
test('picker restores directory without switching branch; new branch selection is explicit',async t=>{const s={directory:'project',branch:'feature',git:true,branches:['main','feature'],dirty:false};const h=harness(t,'picker.html',{structuredContent:{selection:s}},{pc_list_directory:()=>({entries:[],next_offset:-1}),pc_inspect_workspace:()=>s,pc_select_workspace:a=>({...s,branch:a.branch})});await until(()=>h.get('saved').textContent.includes('project'));await until(()=>h.tools('pc_list_directory').length===1);assert.equal(h.tools('pc_select_workspace').length,0);await h.get('candidate').onclick();h.get('branch').value='__pc_new__';h.get('branch').onchange();h.get('new-branch').value='new-feature';await h.get('apply').onclick();const q=h.tools('pc_select_workspace')[0].params.arguments;assert.equal(q.directory,'project');assert.equal(q.branch,'new-feature');assert.equal(q.create,true);assert.equal(q.base_branch,'feature');assert.match(h.get('saved').textContent,/new-feature/)});
test('non-Git remembered directory is published to the new chat',async t=>{const s={available:true,directory:'plain',branch:'',git:false,branches:[]};const h=harness(t,'picker.html',{structuredContent:{selection:s}},{pc_list_directory:()=>({entries:[],next_offset:-1})});await until(()=>h.calls.some(c=>c.method==='ui/update-model-context'));const c=h.calls.find(c=>c.method==='ui/update-model-context');assert.equal(c.params.structuredContent.pcWorkspace.directory,'plain');assert.equal(c.params.structuredContent.pcWorkspace.branch,'')});

test('new chat accepts the prefilled workspace when picker opens without user interaction',async t=>{
 const proposed={available:true,remembered:true,session_bound:false,directory:'project',branch:'main',git:true,branches:['main']};
 const accepted={...proposed,session_bound:true};
 const h=harness(t,'picker.html',{structuredContent:{entries:[],next_offset:-1},_meta:{pc_browser_path:'project'}},{
  pc_get_workspace:()=>proposed,
  pc_open_workspace_picker:()=>({selection:accepted,directories:{entries:[],next_offset:-1},browser_path:'project'}),
  pc_list_directory:()=>({entries:[],next_offset:-1})
 });
 await until(()=>h.tools('pc_open_workspace_picker').length===1);
 await until(()=>h.calls.some(c=>c.method==='ui/update-model-context'));
 assert.equal(h.tools('pc_select_workspace').length,0,'accepting the prefilled workspace must not require an explicit apply');
 assert.match(h.get('saved').textContent,/Этот чат: project · main/);
 const c=h.calls.filter(x=>x.method==='ui/update-model-context').at(-1);
 assert.equal(c.params.structuredContent.pcWorkspace.directory,'project');
 assert.equal(c.params.structuredContent.pcWorkspace.branch,'main');
 assert.equal(c.params.structuredContent.pcWorkspace.sessionBound,true);
});

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


test('running local job can stop without a nonce and cannot become approval again',async t=>{
 const job={id:'local-job',request,status:'running'};let state='running';
 const h=harness(t,'approve.html',{structuredContent:job},{
  pc_job_status:()=>({...job,status:state,output:{records:[],records_cursor:0,more:false,cursor:0,head:[],tail:[],omitted:0,total_records:0,bytes:0}}),
  pc_cancel_job:()=>{state='cancelled';return {...job,status:state}}
 });
 await until(()=>!h.get('cancel').disabled);
 h.emitResult({structuredContent:{...job,status:'awaiting_approval'},_meta:{approval_nonce:'old-nonce'}});
 await h.get('approve').onclick();assert.equal(h.tools('pc_approve_run').length,0);
 assert.equal(h.get('approve').disabled,true);
 await h.get('cancel').onclick();
 assert.match(h.get('status').textContent,/Команда отменена/);
 assert.equal(h.get('cancel').disabled,true);
});
test('workspace card stops all plugin jobs on explicit click',async t=>{
 const selected={available:true,remembered:true,directory:'plain',branch:'',git:false};let stops=0;
 const h=harness(t,'picker.html',{structuredContent:{selection:selected}},{pc_list_directory:()=>({entries:[],next_offset:-1}),pc_cancel_all_jobs:()=>{stops++;return {jobs:[]}},pc_list_jobs:()=>({jobs:[]})});
 await until(()=>h.tools('pc_list_directory').length===1);
 assert.equal(stops,0);await h.get('jobs-stop-all').onclick();assert.equal(stops,1);
});

test('approval immediately enters an unambiguous starting state and disables actions',async t=>{
 const job={id:'job-starting',request,status:'awaiting_approval'};let release,approved=false;
 const gate=new Promise(r=>{release=r});
 const h=harness(t,'approve.html',{structuredContent:job,_meta:{approval_nonce:'nonce'}},{
  pc_approve_run:async()=>{await gate;approved=true;return {...job,status:'running'}},
  pc_job_status:()=>({...job,status:approved?'succeeded':'awaiting_approval',exit_code:approved?0:undefined,output:{records:[],records_cursor:0,more:false,cursor:0,head:[],tail:[],omitted:0,total_records:0,bytes:0}})
 });
 await until(()=>!h.get('approve').disabled);
 const pending=h.get('approve').onclick();
 await until(()=>h.get('status').textContent.includes('Команда выполняется'));
 assert.equal(h.get('approve').disabled,true);
 assert.equal(h.get('approve').textContent,'Запуск разрешён');
 assert.equal(h.get('cancel').disabled,true);
 assert.equal(h.get('stop-all').hidden,true);
 release();await pending;
 await until(()=>h.get('status').textContent.includes('Команда выполнена успешно'));
 assert.equal(h.get('approve').disabled,true);
 assert.equal(h.get('approve').textContent,'Запуск разрешён');
 assert.equal(h.get('cancel').disabled,true);
 assert.equal(h.get('stop-all').hidden,true);
});
test('command card renders detailed records directly from pc_job_status',async t=>{
 const job={id:'job-output',request,status:'running'};let calls=0;
 const h=harness(t,'approve.html',{structuredContent:job},{
  pc_job_status:()=>{calls++;return {...job,status:'failed',exit_code:2,error:'exit status 2',started:'2026-10-07T01:00:00Z',finished:'2026-10-07T01:00:01Z',output:{records:[{sequence:1,stream:'stdout',text:'=== test A ==='},{sequence:2,stream:'stdout',text:'PASS test A'},{sequence:3,stream:'stderr',text:'FAIL test B'}],records_cursor:3,more:false,cursor:3,head:[],tail:[],omitted:1,total_records:3,bytes:42}}}
 });
 await until(()=>h.get('status').textContent.includes('Команда завершилась с ошибкой'));
 assert.ok(calls>=1);
 assert.equal(h.tools('pc_job_output').length,0);
 assert.match(h.get('output').textContent,/00001 \[stdout\] === test A ===/);
 assert.match(h.get('output').textContent,/00003 \[stderr\] FAIL test B/);
 assert.match(h.get('metrics').textContent,/exit code: 2/);
 assert.match(h.get('metrics').textContent,/3 записей/);
});

test('stale awaiting approval result never re-enables consumed approval',async t=>{
 const job={id:'job-stale',request,status:'awaiting_approval'};let approved=false;
 const initial={structuredContent:job,_meta:{approval_nonce:'one-use-nonce'}};
 const h=harness(t,'approve.html',initial,{
  pc_approve_run:a=>{assert.equal(a.approval_nonce,'one-use-nonce');approved=true;return {...job,status:'running'}},
  pc_job_status:()=>({...job,status:approved?'succeeded':'awaiting_approval',exit_code:approved?0:undefined,output:{records:[],records_cursor:0,more:false,cursor:0,head:[],tail:[],omitted:0,total_records:0,bytes:0}})
 });
 await until(()=>!h.get('approve').disabled);
 await h.get('approve').onclick();
 await until(()=>h.get('status').textContent.includes('Команда выполнена успешно'));
 assert.equal(h.tools('pc_approve_run').length,1);
 assert.equal(h.get('approve').disabled,true);
 assert.equal(h.get('approve').textContent,'Запуск разрешён');
 assert.equal(h.get('cancel').disabled,true);
 h.emitResult(initial);
 await pause(20);
 assert.match(h.get('status').textContent,/Команда выполнена успешно/);
 assert.equal(h.get('approve').disabled,true);
 assert.equal(h.get('approve').textContent,'Запуск разрешён');
 await h.get('approve').onclick();
 assert.equal(h.tools('pc_approve_run').length,1,'stale result must not trigger a duplicate approval');
});

test('command card falls back to compact status output without pc_job_output',async t=>{
 const job={id:'job-compact',request,status:'running'};
 const h=harness(t,'approve.html',{structuredContent:job},{
  pc_job_status:()=>({...job,status:'succeeded',exit_code:0,output:{cursor:12,head:[{sequence:1,stream:'stdout',text:'first'}],tail:[{sequence:12,stream:'stderr',text:'last'}],omitted:10,total_records:12,bytes:64}})
 });
 await until(()=>h.get('status').textContent.includes('Команда выполнена успешно'));
 assert.equal(h.tools('pc_job_output').length,0);
 assert.match(h.get('output').textContent,/00001 \[stdout\] first/);
 assert.match(h.get('output').textContent,/00012 \[stderr\] last/);
 assert.match(h.get('metrics').textContent,/показаны начало\/конец; пропущено записей: 10/);
 assert.equal(h.get('status').classList?.contains?.('error')||false,false);
});


test('workspace command history can expand retained console output',async t=>{
 const selected={available:true,remembered:true,directory:'project',branch:'main',git:true,branches:['main']};
 const job={id:'history-job',status:'succeeded',request:{directory:'project',branch:'main',args:['go','test','./...']}};
 const h=harness(t,'picker.html',{structuredContent:{selection:selected}},{pc_list_directory:()=>({entries:[],next_offset:-1}),pc_list_jobs:()=>({jobs:[job]}),pc_job_status:()=>({...job,exit_code:0,output:{records:[{sequence:1,stream:'stdout',text:'ok package'},{sequence:2,stream:'stderr',text:'warning'}],records_cursor:2,more:false,evicted:0}})});
 await until(()=>h.get('jobs-refresh'));
 await h.get('jobs-refresh').onclick();
 const row=h.get('jobs').children[0];assert.ok(row);
 const show=row.children.find(c=>c.textContent==='Показать вывод');assert.ok(show);
 await show.onclick();
 const pre=row.children.at(-1);assert.equal(pre.hidden,false);
 assert.match(pre.textContent,/00001 \[stdout\] ok package/);
 assert.match(pre.textContent,/00002 \[stderr\] warning/);
 assert.equal(h.tools('pc_job_status').length,1);
});

test('cards report host context only when operator diagnostics are enabled', async t => {
  const fs = require('node:fs'), path = require('node:path');
  for (const enabled of [false, true]) {
    let source = fs.readFileSync(path.join(__dirname, 'picker.html'), 'utf8');
    if (enabled) source = source.replace('<script>', '<script>window.PC_DEBUG_CONTEXT=true;');
    const cache = new Map([['debug-' + enabled, source]]);
    const reported = [];
    const h = harness(t, 'picker.html', undefined, {
      pc_debug_client_context: args => {reported.push(args); return {};}
    }, {resourceCache: cache, resourceURI: 'debug-' + enabled});
    for (let i = 0; i < 50 && h.calls.length < 2; i++) await new Promise(r => setTimeout(r, 5));
    await new Promise(r => setTimeout(r, 20));
    assert.equal(reported.length, enabled ? 1 : 0);
    if (enabled) assert.ok(reported[0].context.initialize, 'ui/initialize result is reported');
    h.destroy();
  }
});

test('cards keep the server-issued chat key in their own tool calls', async t => {
  const request = {directory: 'project', branch: 'main', cwd: '.', args: ['make'], purpose: 'build', seconds: 30};
  const seen = [];
  const h = harness(t, 'approve.html', {structuredContent: {id: 'job-chat', request, status: 'awaiting_approval'},
    _meta: {approval_nonce: 'nonce-chat', pc_chat: 'c_0123456789abcdef0123456789abcdef'}}, {
    pc_approve_run: args => {seen.push(args); return {id: 'job-chat', request, status: 'running'};},
    pc_job_status: args => {seen.push(args); return {id: 'job-chat', request, status: 'running',
      output: {records: [], records_cursor: 0, more: false, cursor: 0, head: [], tail: [], omitted: 0, evicted: 0, total_records: 0, bytes: 0}};}
  });
  for (let i = 0; i < 200 && h.get('approve').disabled; i++) await new Promise(r => setTimeout(r, 5));
  await h.get('approve').onclick();
  assert.ok(seen.length > 0);
  for (const args of seen) assert.equal(args.chat, 'c_0123456789abcdef0123456789abcdef');
  h.destroy();
});

test('a key issued to the card itself reaches the model context', async t => {
  const key = 'c_fedcba9876543210fedcba9876543210';
  const proposed = {available: true, remembered: true, session_bound: false, directory: 'project', branch: 'main', git: true, branches: ['main']};
  const h = harness(t, 'picker.html', {structuredContent: {entries: [], next_offset: -1}, _meta: {pc_browser_path: 'project'}}, {
    pc_get_workspace: () => proposed,
    pc_open_workspace_picker: () => ({structuredContent: {chat: key, selection: {...proposed, session_bound: true, chat: key},
      directories: {entries: [], next_offset: -1}, browser_path: 'project'}, _meta: {pc_chat: key}}),
    pc_list_directory: args => {assert.equal(args.chat, key); return {entries: [], next_offset: -1};}
  });
  for (let i = 0; i < 200 && !h.calls.some(c => c.method === 'ui/update-model-context' && c.params.structuredContent.chat); i++)
    await new Promise(r => setTimeout(r, 5));
  const context = h.calls.filter(c => c.method === 'ui/update-model-context').pop();
  assert.equal(context.params.structuredContent.chat, key);
  assert.match(context.params.content[0].text, new RegExp(key));
  h.destroy();
});

test('picker waits for a late tool result and applies the choice to the rendering chat', async t => {
  const key = 'c_00112233445566778899aabbccddeeff';
  const bound = {available: true, remembered: true, session_bound: true, directory: 'AI/srt', branch: 'main', git: true, branches: ['main'], chat: key};
  const target = {available: true, directory: 'AI/mcp', branch: 'dev', git: true, branches: ['dev'], dirty: false};
  const calls = [];
  const record = (name, result) => args => {calls.push({name, args}); return result;};
  const h = harness(t, 'picker.html', {structuredContent: {chat: key, selection: bound, directories: {entries: [], next_offset: -1}, browser_path: 'AI'},
    _meta: {pc_chat: key}}, {
    pc_get_workspace: record('pc_get_workspace', {...bound, chat: 'c_ffffffffffffffffffffffffffffffff', session_bound: false}),
    pc_open_workspace_picker: record('pc_open_workspace_picker', {selection: bound}),
    pc_list_directory: record('pc_list_directory', {entries: [], next_offset: -1}),
    pc_inspect_workspace: record('pc_inspect_workspace', target),
    pc_select_workspace: record('pc_select_workspace', {...target, session_bound: true})
  }, {initialDelayMs: 300, toolInput: {chat: key}});
  await until(() => calls.some(c => c.name === 'pc_list_directory'));
  assert.equal(calls.some(c => c.name === 'pc_get_workspace' || c.name === 'pc_open_workspace_picker'), false,
    'the card must use the delivered selection instead of starting its own chat');
  h.get('search').value = '';
  await h.get('candidate').onclick();
  await h.get('apply').onclick();
  const select = calls.find(c => c.name === 'pc_select_workspace');
  assert.ok(select, 'apply calls pc_select_workspace');
  for (const c of calls) assert.equal(c.args.chat, key, c.name + ' must stay in the rendering chat');
  h.destroy();
});

test('a card that never learns its chat key does not mint a new one', async t => {
  const seen = [];
  const h = harness(t, 'picker.html', undefined, {
    pc_get_workspace: args => {seen.push(args); return {available: true, session_bound: true, directory: 'p', branch: '', git: false, branches: []};},
    pc_list_directory: args => {seen.push(args); return {entries: [], next_offset: -1};}
  });
  for (let i = 0; i < 100 && seen.length < 2; i++) await new Promise(r => setTimeout(r, 50)); // settle waits 3 s
  assert.ok(seen.length >= 2);
  for (const args of seen) assert.equal(args.chat, 'none');
  h.destroy();
});

test('a choice in the picker reaches the model as authoritative for its chat', async t => {
  const s = {available: true, session_bound: true, directory: 'AI/mcp', branch: 'dev', git: true, branches: ['dev', 'main'], dirty: false};
  const target = {...s, directory: 'AI/srt', branch: 'main', branches: ['main']};
  const h = harness(t, 'picker.html', {structuredContent: {selection: s}}, {
    pc_list_directory: () => ({entries: [], next_offset: -1}),
    pc_inspect_workspace: () => target,
    pc_select_workspace: a => ({...target, branch: a.branch})
  });
  await until(() => h.tools('pc_list_directory').length === 1);
  await h.get('candidate').onclick();
  await h.get('apply').onclick();
  const last = h.calls.filter(c => c.method === 'ui/update-model-context').pop();
  assert.equal(last.params.structuredContent.pcWorkspace.chosenInCard, true);
  assert.equal(last.params.structuredContent.pcWorkspace.directory, 'AI/srt');
  assert.match(last.params.content[0].text, /call pc_select_workspace with exactly these values/);
});
