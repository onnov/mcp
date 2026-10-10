/* MCP Apps bridge; private tool response metadata never enters model context. */
(() => {
 'use strict';
 const version = '1.1.8';
 const pending = new Map(), listeners = new Set(), teardownListeners = new Set();
 let serial = 0, last = null, lastHeight = 0, closed = false;
 // Server-issued chat key (clients without chat metadata, e.g. Claude): the
 // card's own tool calls must stay in the chat that rendered it.
 let chat = '';
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
 // The first key wins: it comes from the model's own call that rendered this
 // card (tool input, then result). Later results cannot move the card to
 // another chat.
 let keySource = '';
 const received = [];
 function rememberKey(key, source) {
  if (!chat && typeof key === 'string' && /^c_[0-9a-f]{32}$/.test(key)) {chat = key; keySource = source;}
 }
 function remember(result, source) {
  rememberKey(result?._meta?.pc_chat, source + '._meta');
  let data = result?.structuredContent;
  // Some hosts pass only the text content; it is the same JSON.
  if (!data) try {data = JSON.parse((result?.content || []).find(x => x.type === 'text')?.text || 'null');} catch (_) {}
  rememberKey(data?.chat, source + '.chat');
  rememberKey(data?.selection?.chat, source + '.selection.chat');
 }
 const waiters = new Set();
 function deliver(result) {
  remember(result, 'tool-result');
  last = result;
  for (const fn of listeners) fn(result);
  for (const fn of waiters) fn();
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
  if (m.method && received.length < 20) received.push(m.method);
  if (!closed && m.method === 'ui/notifications/tool-input') rememberKey(m.params?.arguments?.chat, 'tool-input');
  if (!closed && m.method === 'ui/notifications/tool-result') deliver(m.params);
 });
 window.PC = {
  rpc, notify, decode, version,
  tool: async (name, args = {}) => {
   // Without a key the card must not get a new one: "none" keeps the
   // shared session instead of starting an orphan chat binding.
   if (args.chat === undefined) args = {...args, chat: chat || 'none'};
   try {
    const result = await rpc('tools/call', {name, arguments: args});
    remember(result, name);
    return decode(result);
   }
   catch (e) {
    // Keep the tool name and correlation ID; never log argv or approval nonce.
    throw Object.assign(Error('tools/call ' + name
     + (e.rpcID !== undefined ? ' (#' + e.rpcID + ')' : '') + ': ' + e.message),
     {code: e.code, rpcID: e.rpcID});
   }
  },
  // Hosts may deliver the rendering tool's result after the handshake. Wait
  // for it (bounded) before a card makes its own calls.
  settle: ms => last ? Promise.resolve(last) : new Promise(resolve => {
   const done = () => {waiters.delete(done); clearTimeout(timer); resolve(last);};
   const timer = setTimeout(done, ms);
   waiters.add(done);
  }),
  onResult: fn => {listeners.add(fn); if (last) fn(last); return () => listeners.delete(fn);},
  onTeardown: fn => {teardownListeners.add(fn); return () => teardownListeners.delete(fn);},
  ready: async () => {
   const init = await rpc('ui/initialize', {appInfo: {name: 'pc-mcp', version},
    appCapabilities: {}, protocolVersion: '2026-01-26'});
   notify('ui/notifications/initialized', {});
   // Operator diagnostics only (PC_MCP_DEBUG_REQUESTS): report what the host
   // tells this card, so the server log can show whether it identifies a chat.
   if (window.PC_DEBUG_CONTEXT) {
    try {
     rpc('tools/call', {name: 'pc_debug_client_context', arguments: {context: {
      initialize: init, location_href: globalThis.location?.href, referrer: globalThis.document?.referrer,
      ancestor_origins: Array.from(globalThis.location?.ancestorOrigins || []), window_name: window.name}}}).catch(() => {});
    } catch (_) {}
   }
  },
  // Operator diagnostics only: which host messages arrived and where the chat
  // key came from. Never sends the key, arguments or tool results.
  debug: (event, extra = {}) => {
   if (!window.PC_DEBUG_CONTEXT) return;
   rpc('tools/call', {name: 'pc_debug_client_context', arguments: {context: {
    event, received: [...received], key_source: keySource || 'none', has_key: !!chat, ...extra}}}).catch(() => {});
  },
  // The model must keep using the chat key this card works in.
  context: async (data, text) => rpc('ui/update-model-context', {
   structuredContent: chat ? {...data, chat} : data,
   content: [{type: 'text', text: chat ? text + ' Pass chat="' + chat + '" in every pc_* call of this chat.' : text}]
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
