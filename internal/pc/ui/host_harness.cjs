const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

// Execute the embedded bridge and card, never a mock PC API. Each mount gets
// a new Window/VM; only the host's widget snapshot survives a remount.
function harness(t, file, initial, handlers = {}, options = {}) {
  // A host can keep a resource's HTML even after the server is restarted.
  // Cache by the descriptor URI, just as MCP Apps hosts are allowed to do.
  let source = options.resourceCache?.get(options.resourceURI);
  if (source === undefined) {
    source = fs.readFileSync(path.join(__dirname, file), 'utf8');
    options.resourceCache?.set(options.resourceURI, source);
  }
  const html = source.replace('/*BRIDGE*/', fs.readFileSync(path.join(__dirname, 'bridge.js'), 'utf8'));
  const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map(m => m[1]);
  const elements = new Map(), calls = [], events = [], timers = new Map(), listeners = new Map();
  const widget = options.widget || {state: null};
  let destroyed = false;

  function element(id = '') {
    let disabled = false;
    const e = {id, value: '', textContent: '', hidden: false, children: [], enabled: [],
      append(c) {this.children.push(c); if (!this.value && c.value !== undefined) this.value = c.value;},
      replaceChildren() {this.children = []; this.value = '';}, focus() {}, classList: {toggle() {}}};
    Object.defineProperty(e, 'disabled', {get: () => disabled, set: value => {
      disabled = !!value; e.enabled.push(!disabled);
    }});
    Object.defineProperty(e, 'childNodes', {get: () => e.children});
    return e;
  }
  for (const m of html.matchAll(/<[^>]*\bid="([^"]+)"[^>]*>/g)) {
    const e = element(m[1]);
    e.disabled = /\bdisabled\b/.test(m[0]); e.hidden = /\bhidden\b/.test(m[0]);
    elements.set(m[1], e);
  }
  const document = {getElementById: id => elements.get(id), createElement: () => element(),
    documentElement: {getBoundingClientRect: () => ({height: 400})}, body: element()};
  function dispatch(type, event) {
    if (destroyed) return;
    events.push({direction: 'in', type, event});
    for (const fn of listeners.get(type) || []) fn(event);
  }
  const reply = (id, result, error) => queueMicrotask(() => dispatch('message', {
    source: parent, data: {jsonrpc: '2.0', id, ...(error ? {error} : {result})}
  }));
  function emitResult(result) {
    dispatch('message', {source: parent, data: {
      jsonrpc: '2.0', method: 'ui/notifications/tool-result', params: result
    }});
  }
  const parent = {postMessage: async msg => {
    if (destroyed) return;
    calls.push(msg); events.push({direction: 'out', message: msg});
    if (msg.method === 'ui/notifications/initialized') {
      // Follow the real handshake: input/result arrive after initialize's reply
      // and the View-ready notification, rather than inside ui/initialize.
      if (initial && options.deliverInitial !== false) queueMicrotask(() => {
        dispatch('message', {source: parent, data: {jsonrpc: '2.0',
          method: 'ui/notifications/tool-input', params: {arguments: initial.structuredContent?.request || {}}}});
        emitResult(initial);
      });
    }
    if (msg.id === undefined || !msg.method) return;
    try {
      let result = {};
      if (msg.method === 'ui/initialize') result = {protocolVersion: '2026-01-26',
        hostInfo: {name: 'test-host', version: '1'}, hostCapabilities: {serverTools: {}}, hostContext: {}};
      if (msg.method === 'tools/call') {
        if (!handlers[msg.params.name]) throw Error('Unexpected tool ' + msg.params.name);
        const value = await handlers[msg.params.name](msg.params.arguments);
        result = value?.structuredContent || value?.isError || value?.content ? value : {structuredContent: value};
        if (options.echoToolResults) emitResult(result);
      }
      if (msg.method === 'ui/update-model-context' && options.contextReply) {
        result = await options.contextReply(msg.params);
      }
      reply(msg.id, result);
    } catch (e) {
      reply(msg.id, undefined, {code: -32602, message: e.message});
    }
  }};
  const window = {parent,
    addEventListener(type, fn) {
      if (!listeners.has(type)) listeners.set(type, new Set());
      listeners.get(type).add(fn);
    },
    removeEventListener(type, fn) {listeners.get(type)?.delete(fn);}
  };
  if (options.openai !== false) {
    window.openai = {...options.globals, setWidgetState(state) {
      widget.state = structuredClone(state);
      window.openai.widgetState = structuredClone(state);
    }};
    window.openai.widgetState = structuredClone(widget.state);
  }
  const context = {window, parent, document, console, Map, Set, JSON, Promise, Math, Error,
    setTimeout(fn, ms) {
      const id = setTimeout(() => {timers.delete(id); fn();}, ms);
      timers.set(id, {fn, ms}); return id;
    },
    clearTimeout(id) {clearTimeout(id); timers.delete(id);}
  };
  vm.createContext(context);
  for (const script of scripts) {
    vm.runInContext(script, context);
    if (window.PC) context.PC = window.PC;
  }
  function destroy() {
    destroyed = true;
    for (const id of timers.keys()) clearTimeout(id);
    timers.clear(); listeners.clear();
  }
  t.after(destroy);
  return {get: id => elements.get(id), calls, events, window, widget, destroy, emitResult,
    tools: name => calls.filter(x => x.method === 'tools/call' && x.params.name === name),
    emitGlobals(globals) {
      Object.assign(window.openai, globals);
      dispatch('openai:set_globals', {detail: {globals}});
    },
    message(data, source = parent) {dispatch('message', {source, data});},
    async poll() {
      const entry = [...timers].find(([, value]) => value.ms === 1000);
      if (!entry) throw Error('No scheduled poll');
      const [id, value] = entry;
      clearTimeout(id); timers.delete(id); await value.fn();
    },
    hasPoll: () => [...timers.values()].some(value => value.ms === 1000)
  };
}
module.exports = {harness};
