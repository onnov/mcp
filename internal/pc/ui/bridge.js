/* MCP Apps bridge; private tool response metadata never enters model context. */
(() => {
 'use strict';
 const pending=new Map(), listeners=[];let serial=0,last=null,lastHeight=0;
 const notify=(method,params)=>parent.postMessage({jsonrpc:'2.0',method,params},'*');
 const rpc=(method,params)=>new Promise((resolve,reject)=>{const id=++serial,timer=setTimeout(()=>{pending.delete(id);reject(Error('Нет ответа от ChatGPT. Откройте карточку заново.'));},60000);pending.set(id,{resolve,reject,timer});parent.postMessage({jsonrpc:'2.0',id,method,params},'*')});
 function decode(result){if(result?.isError)throw Error((result.content||[]).filter(x=>x.type==='text').map(x=>x.text).join('\n')||'Ошибка MCP');if(result?.structuredContent)return result.structuredContent;const t=(result?.content||[]).find(x=>x.type==='text')?.text;if(t)return JSON.parse(t);throw Error('Нет результата')}
 window.addEventListener('message',e=>{if(e.source!==parent||e.data?.jsonrpc!=='2.0')return;const m=e.data;if(m.id!==undefined&&pending.has(m.id)){const p=pending.get(m.id);pending.delete(m.id);clearTimeout(p.timer);m.error?p.reject(Error(m.error.message||'Ошибка')):p.resolve(m.result);return}if(m.method==='ui/notifications/tool-result'){last=m.params;for(const fn of listeners)fn(last)}});
 window.PC={rpc,notify,decode,tool:async(name,args={})=>decode(await rpc('tools/call',{name,arguments:args})),onResult:fn=>{listeners.push(fn);if(last)fn(last)},ready:async()=>{await rpc('ui/initialize',{appInfo:{name:'pc-mcp',version:'1.0.0'},appCapabilities:{},protocolVersion:'2026-01-26'});notify('ui/notifications/initialized',{})},context:async(data,text)=>rpc('ui/update-model-context',{structuredContent:data,content:[{type:'text',text}]}),resize:()=>{const height=Math.ceil(document.documentElement.getBoundingClientRect().height);if(height!==lastHeight){lastHeight=height;notify('ui/notifications/size-changed',{height})}}};
 const hydrate=()=>{const meta=window.openai?.toolResponseMetadata;const r=meta?.mcp_tool_result||meta?.call_tool_result;if(r?.structuredContent){last=r;for(const fn of listeners)fn(last)}else if(window.openai?.toolOutput){last={structuredContent:window.openai.toolOutput,_meta:meta?._meta||meta};for(const fn of listeners)fn(last)}};
 window.addEventListener('openai:set_globals',hydrate);hydrate();
 if(window.ResizeObserver)new ResizeObserver(()=>PC.resize()).observe(document.body);
})();
