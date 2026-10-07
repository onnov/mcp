/* MCP Apps bridge; private tool response metadata never enters model context. */
(() => {
 'use strict';
 const version = '1.1.6';
 const pending = new Map(), listeners = new Set(), teardownListeners = new Set();
 let serial = 0, last = null, lastHeight = 0, closed = false;
 const notify = (method, params) => {
  if (!closed) parent.postMessage({jsonrpc: '2.0', method, params}, '*');
 };
 const rpc = (method, params) => new Promise((resolve, reject) => {
  if (closed) return reject(Error('Карточка закрыта'));
  const id = ++serial, timer = setTimeout(() => {
   pending.delete(id);
   reject(Object.assign(Error('Нет ответа от ChatGPT. Откройте карточку заново.'), {rpcID: id}));
  }, 60000);
  pending.set(id, {resolve, reject, timer});
  parent.postMessage({jsonrpc: '2.0', id, method, params}, '*');
 });
 function decode(result) {
  if (result?.isError) throw Error((result.content || []).filter(x => x.type === 'text')
   .map(x => x.text).join('\n') || 'Ошибка MCP');
  if (result?.structuredContent) return result.structuredContent;
  const text = (result?.content || []).find(x => x.type === 'text')?.text;
  if (text) return JSON.parse(text);
  throw Error('Нет результата');
 }
 function deliver(result) {
  last = result;
  for (const fn of listeners) fn(result);
 }
 window.addEventListener('message', e => {
  if (e.source !== parent || e.data?.jsonrpc !== '2.0') return;
  const m = e.data;
  // A host request can reuse an outbound request ID. Only responses resolve
  // pending promises; they never invoke onResult or replace the bootstrap result.
  if (!m.method && m.id !== undefined && pending.has(m.id)) {
   const p = pending.get(m.id);
   pending.delete(m.id); clearTimeout(p.timer);
   m.error ? p.reject(Object.assign(Error(m.error.message || 'Ошибка'), {code: m.error.code, rpcID: m.id}))
    : p.resolve(m.result);
   return;
  }
  if (m.method === 'ui/resource-teardown') {
   closed = true;
   for (const fn of teardownListeners) fn();
   for (const p of pending.values()) {clearTimeout(p.timer); p.reject(Error('Карточка закрыта'));}
   pending.clear(); listeners.clear();
   parent.postMessage({jsonrpc: '2.0', id: m.id, result: {}}, '*');
   return;
  }
  if (!closed && m.method === 'ui/notifications/tool-result') deliver(m.params);
 });
 window.PC = {
  rpc, notify, decode, version,
  tool: async (name, args = {}) => {
   try {return decode(await rpc('tools/call', {name, arguments: args}));}
   catch (e) {
    // Keep the tool name and correlation ID; never log argv or approval nonce.
    throw Object.assign(Error('tools/call ' + name
     + (e.rpcID !== undefined ? ' (#' + e.rpcID + ')' : '') + ': ' + e.message),
     {code: e.code, rpcID: e.rpcID});
   }
  },
  onResult: fn => {listeners.add(fn); if (last) fn(last); return () => listeners.delete(fn);},
  onTeardown: fn => {teardownListeners.add(fn); return () => teardownListeners.delete(fn);},
  ready: async () => {
   await rpc('ui/initialize', {appInfo: {name: 'pc-mcp', version},
    appCapabilities: {}, protocolVersion: '2026-01-26'});
   notify('ui/notifications/initialized', {});
  },
  context: async (data, text) => rpc('ui/update-model-context', {
   structuredContent: data, content: [{type: 'text', text}]
  }),
  resize: () => {
   const height = Math.ceil(document.documentElement.getBoundingClientRect().height);
   if (height !== lastHeight) {lastHeight = height; notify('ui/notifications/size-changed', {height});}
  }
 };
 function hydrate() {
  if (closed) return;
  const meta = window.openai?.toolResponseMetadata;
  const result = meta?.mcp_tool_result || meta?.call_tool_result;
  if (result?.structuredContent) deliver(result);
  else if (window.openai?.toolOutput) deliver({structuredContent: window.openai.toolOutput,
   _meta: meta?._meta || meta});
 }
 window.addEventListener('openai:set_globals', e => {
  const globals = e.detail?.globals;
  // Theme/layout/widget-state updates do not contain new tool data. Re-reading
  // toolOutput on those events used to replay the original command snapshot.
  if (globals && !Object.hasOwn(globals, 'toolOutput') && !Object.hasOwn(globals, 'toolResponseMetadata')) return;
  hydrate();
 });
 hydrate();
 if (window.ResizeObserver) new ResizeObserver(() => PC.resize()).observe(document.body);
})();
