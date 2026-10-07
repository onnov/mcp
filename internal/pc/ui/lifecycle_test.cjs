const {test} = require('node:test');
const assert = require('node:assert/strict');
const {harness} = require('./host_harness.cjs');
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(fn) {
  for (let i = 0; i < 200; i++) {if (fn()) return; await pause(5);}
  throw Error('UI condition timed out');
}
const request = {directory: 'project', branch: 'feature', cwd: '.',
  args: ['bash', '-lc', 'echo hello'], purpose: 'smoke', seconds: 30};
const empty = {records: [], records_cursor: 0, more: false, cursor: 0, head: [], tail: [],
  omitted: 0, evicted: 0, total_records: 0, bytes: 0};
const result = (id, nonce = 'nonce') => ({structuredContent: {id, request, status: 'awaiting_approval'},
  _meta: {approval_nonce: nonce}});
const deferred = () => {let resolve; const promise = new Promise(r => {resolve = r;}); return {promise, resolve};};

test('MCP Apps approval lifecycle survives internal echoes, stale notifications and all globals envelopes', async t => {
  for (const kind of ['mcp_tool_result', 'call_tool_result', 'toolOutput']) {
    const initial = result('lifecycle-' + kind, 'private-' + kind);
    let state = 'awaiting_approval';
    const h = harness(t, 'approve.html', initial, {
      pc_approve_run: args => {
        assert.equal(args.id, initial.structuredContent.id);
        assert.equal(args.approval_nonce, initial._meta.approval_nonce);
        state = 'running'; return {...initial.structuredContent, status: state};
      },
      pc_job_status: () => ({...initial.structuredContent, status: state,
        exit_code: state === 'succeeded' ? 0 : undefined,
        output: state === 'succeeded' ? {...empty,
          records: [{sequence: 1, stream: 'stdout', text: 'hello'}, {sequence: 2, stream: 'stderr', text: 'details'}],
          records_cursor: 2, cursor: 2, total_records: 2, bytes: 10} : empty})
    }, {echoToolResults: true});
    await until(() => !h.get('approve').disabled);
    assert.match(h.get('status').textContent, /Команда ещё не запущена/);
    assert.equal(h.get('cancel').disabled, true);
    assert.match(h.get('command').textContent, /echo hello/);
    assert.match(h.get('target').textContent, /Каталог: project.*Ветка: feature/);
    assert.match(h.get('policy').textContent, /Назначение: smoke.*30 сек/);
    const consumedAt = h.get('approve').enabled.length;
    const click = h.get('approve').onclick();
    assert.equal(h.get('approve').disabled, true, 'consume synchronously before tools/call replies');
    assert.match(h.get('status').textContent, /Команда выполняется/);
    await h.get('approve').onclick(); // Programmatic duplicate while the first call is pending.
    await click;
    assert.equal(h.get('cancel').disabled, false, 'authoritative running enables Stop');
    await h.poll(); // A separate pc_job_status -> running, after pc_approve_run -> running.
    h.emitResult(initial); // Unsolicited replay between running and completion.
    assert.match(h.get('status').textContent, /Команда выполняется/);
    assert.equal(h.get('cancel').disabled, false);
    state = 'succeeded'; await h.poll();
    const globals = kind === 'toolOutput'
      ? {toolOutput: initial.structuredContent, toolResponseMetadata: {_meta: initial._meta}}
      : {toolOutput: initial.structuredContent, toolResponseMetadata: {[kind]: initial}};
    h.emitGlobals(globals); // Hydrate from the old request after terminal state.
    h.emitGlobals({theme: 'dark'});
    h.emitGlobals({widgetState: h.widget.state});
    h.emitResult(initial);
    await h.get('approve').onclick(); await h.get('cancel').onclick();
    assert.equal(h.tools('pc_approve_run').length, 1);
    assert.equal(h.tools('pc_cancel_job').length, 0);
    assert.ok(h.get('approve').enabled.slice(consumedAt).every(enabled => !enabled));
    assert.equal(h.get('cancel').disabled, true);
    assert.match(h.get('status').textContent, /Команда выполнена успешно.*succeeded/);
    assert.match(h.get('output').textContent, /00001 \[stdout\] hello/);
    assert.match(h.get('output').textContent, /00002 \[stderr\] details/);
    assert.match(h.get('metrics').textContent, /exit code: 0/);
    assert.equal(h.hasPoll(), false, 'terminal state and fully drained output stop polling');
    for (const call of h.calls.filter(c => c.method === 'ui/update-model-context')) {
      assert.equal(JSON.stringify(call).includes(initial._meta.approval_nonce), false);
    }
    assert.equal(JSON.stringify(h.widget.state).includes(initial._meta.approval_nonce), false);
    assert.equal(h.calls[0].method, 'ui/initialize');
    assert.ok(h.events.findIndex(e => e.direction === 'out' && e.message.method === 'ui/notifications/initialized')
      < h.events.findIndex(e => e.direction === 'in' && e.event.data?.method === 'ui/notifications/tool-result'));
    h.destroy();
  }
});

test('two cards and a new job using an old widget snapshot keep independent approvals', async t => {
  const states = new Map([['first', 'awaiting_approval'], ['second', 'awaiting_approval'], ['third', 'awaiting_approval']]);
  const mount = (id, options) => harness(t, 'approve.html', result(id, 'nonce-' + id), {
    pc_approve_run: args => {
      assert.equal(args.approval_nonce, 'nonce-' + id); states.set(id, 'succeeded');
      return {id, request, status: 'running'};
    },
    pc_job_status: () => ({id, request, status: states.get(id), output: empty})
  }, options);
  const first = mount('first'), second = mount('second');
  await until(() => !first.get('approve').disabled && !second.get('approve').disabled);
  await first.get('approve').onclick();
  second.emitResult(result('first')); first.emitResult(result('second'));
  assert.equal(first.get('approve').disabled, true);
  assert.equal(second.get('approve').disabled, false);
  assert.match(second.get('status').textContent, /Команда ещё не запущена.*second/);
  await second.get('approve').onclick();
  const third = mount('third', {widget: first.widget});
  await until(() => !third.get('approve').disabled);
  await third.get('approve').onclick();
  for (const h of [first, second, third]) assert.equal(h.tools('pc_approve_run').length, 1);
});

test('an old in-flight status read cannot reset starting or create overlapping polls', async t => {
  const initial = result('read-race'), statusGate = deferred(), approveGate = deferred();
  let reads = 0, activeReads = 0, maximumReads = 0, state = 'awaiting_approval';
  const h = harness(t, 'approve.html', initial, {
    pc_job_status: async () => {
      const snapshot = {...initial.structuredContent, status: state};
      activeReads++; maximumReads = Math.max(maximumReads, activeReads);
      if (++reads === 2) await statusGate.promise;
      activeReads--; return snapshot;
    },
    pc_approve_run: async () => {await approveGate.promise; state = 'running'; return {...initial.structuredContent, status: state};}
  });
  await until(() => !h.get('approve').disabled && h.hasPoll());
  const read = h.poll(); await until(() => reads === 2);
  const click = h.get('approve').onclick();
  h.emitGlobals({toolOutput: initial.structuredContent, toolResponseMetadata: {mcp_tool_result: initial}});
  h.emitResult({structuredContent: {...initial.structuredContent, status: 'running'}});
  statusGate.resolve(); await read;
  assert.match(h.get('status').textContent, /Команда выполняется/);
  assert.equal(h.get('approve').disabled, true);
  assert.equal(h.get('cancel').disabled, true);
  await h.get('approve').onclick();
  approveGate.resolve(); await click;
  assert.equal(maximumReads, 1);
  assert.equal(h.tools('pc_approve_run').length, 1);
  assert.equal(h.get('cancel').disabled, false);
});

test('context acknowledgement is independent of polling and terminal controls', async t => {
  const initial = result('independent-context'), contextGate = deferred();
  let state = 'awaiting_approval';
  const h = harness(t, 'approve.html', initial, {
    pc_approve_run: () => {state = 'running'; return {...initial.structuredContent, status: state};},
    pc_job_status: () => ({...initial.structuredContent, status: state, output: empty})
  }, {contextReply: () => contextGate.promise});
  await until(() => !h.get('approve').disabled);
  await h.get('approve').onclick();
  assert.equal(h.get('cancel').disabled, false);
  assert.ok(h.hasPoll(), 'pending model context must not delay next poll');
  state = 'succeeded';
  await h.poll();
  assert.match(h.get('status').textContent, /Команда выполнена успешно/);
  assert.equal(h.get('cancel').disabled, true);
  assert.equal(h.get('approve').disabled, true);
  contextGate.resolve({});
  assert.equal(h.hasPoll(), false);
});

test('server read failure never enables approval from an initial snapshot', async t => {
  const initial = result('unknown-job');
  const h = harness(t, 'approve.html', initial, {pc_job_status: () => {throw Error('unknown job');}});
  await until(() => h.get('status').textContent.includes('unknown job'));
  h.emitResult(initial); h.emitGlobals({toolOutput: initial.structuredContent, toolResponseMetadata: initial._meta});
  await h.get('approve').onclick();
  assert.ok(h.get('approve').enabled.every(enabled => !enabled));
  assert.equal(h.tools('pc_approve_run').length, 0);
});

test('every terminal status keeps approval and Stop disabled on globals and notification replay', async t => {
  for (const state of ['failed', 'cancelled', 'timed_out', 'expired']) {
    const initial = result('terminal-' + state);
    const h = harness(t, 'approve.html', initial, {
      pc_job_status: () => ({...initial.structuredContent, status: state, exit_code: state === 'failed' ? 2 : undefined, output: empty})
    });
    await until(() => h.get('status').textContent.includes('(' + state + ')'));
    h.emitResult(initial); h.emitGlobals({toolOutput: initial.structuredContent, toolResponseMetadata: {mcp_tool_result: initial}});
    await h.get('approve').onclick(); await h.get('cancel').onclick();
    assert.ok(h.get('approve').enabled.every(enabled => !enabled));
    assert.equal(h.get('cancel').disabled, true);
    assert.equal(h.tools('pc_approve_run').length, 0);
    assert.equal(h.tools('pc_cancel_job').length, 0);
    if (state === 'failed') assert.match(h.get('metrics').textContent, /exit code: 2/);
    h.destroy();
  }
});

test('pc_job_status pages retained output through terminal state without pc_job_output', async t => {
  const initial = result('paged-output');
  const total = 4205;
  const h = harness(t, 'approve.html', initial, {
    pc_job_status: args => {
      const start = Math.max(6, args.after + 1);
      const end = Math.min(total, start + (args.after === 0 ? 29 : 199));
      const records = start <= total ? Array.from({length: end - start + 1}, (_, i) => ({
        sequence: start + i, stream: i % 2 ? 'stderr' : 'stdout', text: 'line-' + (start + i)
      })) : [];
      const cursor = records.length ? records.at(-1).sequence : args.after;
      return {...initial.structuredContent, status: 'succeeded', exit_code: 0,
        started: '2026-10-06T00:00:00Z', finished: '2026-10-06T00:00:02Z',
        output: {records, records_cursor: cursor, more: cursor < total, evicted: args.after === 0 ? 5 : 0,
          cursor: total, head: [], tail: [], omitted: total - 110, total_records: total, bytes: 2048}};
    }
  });
  await until(() => h.hasPoll());
  assert.equal(h.get('cancel').disabled, true);
  for (let i = 0; i < 30 && h.hasPoll(); i++) await h.poll();
  assert.match(h.get('output').textContent, /line-1000/);
  assert.match(h.get('output').textContent, /line-4205/);
  assert.equal(h.get('output').textContent.split('\n').length, total - 5);
  assert.match(h.get('metrics').textContent, /удалено из retained-лога до чтения: 5/);
  assert.match(h.get('metrics').textContent, /время: 2\.00 с/);
  assert.ok(h.tools('pc_job_status').length > 1, 'terminal output pages through status cursor');
  assert.equal(h.tools('pc_job_output').length, 0);
  assert.equal(h.hasPoll(), false);
});

test('bridge keeps RPC replies separate and ignores unrelated globals updates', async t => {
  const initial = result('bridge-streams');
  const h = harness(t, 'approve.html', initial, {
    pc_job_status: () => initial.structuredContent, inspect: () => ({id: 'other-job', status: 'running'})
  }, {globals: {toolOutput: initial.structuredContent, toolResponseMetadata: {mcp_tool_result: initial}}});
  await until(() => !h.get('approve').disabled);
  const received = [];
  const unsubscribe = h.window.PC.onResult(r => received.push(r));
  const count = received.length;
  await h.window.PC.tool('inspect', {});
  assert.equal(received.length, count, 'tools/call reply does not feed onResult');
  h.emitGlobals({theme: 'dark', maxHeight: 900});
  h.emitGlobals({widgetState: {selectedId: 'anything'}});
  assert.equal(received.length, count, 'unrelated globals must not hydrate cached data');
  h.emitResult(initial); assert.equal(received.length, count + 1);
  unsubscribe(); h.emitResult(initial); assert.equal(received.length, count + 1);
});

test('resource teardown acknowledges a host request even when its ID matches an in-flight RPC', async t => {
  const initial = result('teardown'), gate = deferred();
  const h = harness(t, 'approve.html', initial, {pc_job_status: () => gate.promise});
  await until(() => h.tools('pc_job_status').length);
  const id = h.tools('pc_job_status')[0].id;
  h.message({jsonrpc: '2.0', id, method: 'ui/resource-teardown', params: {reason: 'resource re-allocation'}});
  await pause(5);
  assert.ok(h.calls.some(c => c.id === id && !c.method && c.result));
  assert.equal(h.get('approve').disabled, true);
  assert.equal(h.hasPoll(), false);
  const count = h.calls.length;
  h.emitGlobals({toolOutput: initial.structuredContent}); h.emitResult(initial);
  await h.get('approve').onclick();
  assert.equal(h.calls.length, count);
  gate.resolve(initial.structuredContent);
});

test('a remounted completed card checks the server before offering stale approval', async t => {
  const initial = result('remounted-complete');
  const h = harness(t, 'approve.html', initial, {
    pc_job_status: () => ({...initial.structuredContent, status: 'succeeded', exit_code: 0, output: empty})
  }, {globals: {toolOutput: initial.structuredContent, toolResponseMetadata: {mcp_tool_result: initial}}});
  await until(() => h.tools('pc_job_status').length || !h.get('approve').disabled);
  assert.equal(h.get('approve').disabled, true, 'cached request snapshot must never enable approval');
  await until(() => h.get('status').textContent.includes('Команда выполнена успешно'));
  assert.ok(h.get('approve').enabled.every(enabled => !enabled), 'even a transient enable is unsafe');
  await h.get('approve').onclick();
  assert.equal(h.tools('pc_approve_run').length, 0);
  assert.equal(h.get('cancel').disabled, true);
});

test('late unsolicited running snapshots cannot overwrite a terminal server state', async t => {
  const initial = result('late-running');
  let approved = false;
  const h = harness(t, 'approve.html', initial, {
    pc_approve_run: () => {approved = true; return {...initial.structuredContent, status: 'running'};},
    pc_job_status: () => ({...initial.structuredContent, status: approved ? 'succeeded' : 'awaiting_approval', output: empty})
  });
  await until(() => !h.get('approve').disabled);
  await h.get('approve').onclick();
  await until(() => h.get('status').textContent.includes('Команда выполнена успешно'));
  h.emitResult({structuredContent: {...initial.structuredContent, status: 'running'}});
  assert.match(h.get('status').textContent, /Команда выполнена успешно/);
  assert.equal(h.get('cancel').disabled, true);
});

test('a rejected approval stays consumed when ChatGPT recreates the iframe', async t => {
  const initial = result('remounted-rejected');
  const handlers = {
    pc_job_status: () => ({...initial.structuredContent, output: empty}),
    pc_approve_run: () => {throw Error('start rejected');}
  };
  const first = harness(t, 'approve.html', initial, handlers);
  await until(() => !first.get('approve').disabled);
  await first.get('approve').onclick();
  const widget = first.widget;
  first.destroy();
  const second = harness(t, 'approve.html', initial, handlers, {widget});
  await until(() => second.tools('pc_job_status').length || !second.get('approve').disabled);
  assert.equal(second.get('approve').disabled, true);
  assert.ok(second.get('approve').enabled.every(enabled => !enabled));
  await second.get('approve').onclick();
  assert.equal(second.tools('pc_approve_run').length, 0);
});

test('approval descriptor bypasses cached pre-records UI and renders both streams through the real bridge', async t => {
  const fs = require('node:fs');
  const path = require('node:path');
  const resource = fs.readFileSync(path.join(__dirname, 'resource.go'), 'utf8');
  const uri = resource.match(/const ApprovalURI = "([^"]+)"/)[1];
  // The exact historical output implementation is checked in as a fixture.
  // The live 1.1.4 test card published its old "via pc_job_output" context,
  // although the 1.1.4 binary already served records-based HTML at this URI.
  // We do not have the complete host's cached asset or RPC error envelope.
  const cache = new Map([['ui://pc-mcp/approve-v7.html',
    fs.readFileSync(path.join(__dirname, 'fixtures/approve-2ad8918.html'), 'utf8')]]);
  const initial = result('cached-output');
  let state = 'awaiting_approval';
  const h = harness(t, 'approve.html', initial, {
    pc_approve_run: () => {
      state = 'running';
      return {structuredContent: {...initial.structuredContent, status: state}, content: []};
    },
    pc_job_status: () => ({structuredContent: {...initial.structuredContent, status: state,
      exit_code: state === 'succeeded' ? 0 : undefined,
      output: state === 'succeeded' ? {...empty,
        records: [{sequence: 1, stream: 'stdout', text: 'cache-stdout'},
          {sequence: 2, stream: 'stderr', text: 'cache-stderr'}],
        records_cursor: 2, cursor: 2, total_records: 2, bytes: 26} : empty}, content: []})
    // pc_job_output is absent in the connection's observed tool catalogue.
  }, {resourceURI: uri, resourceCache: cache});
  await until(() => !h.get('approve').disabled);
  assert.equal(h.tools('pc_approve_run').length, 0);
  await h.get('approve').onclick();
  assert.equal(h.tools('pc_job_output').length, 0,
    'cached HTML attempted tools/call pc_job_output; the old output path prevents polling/render');
  assert.match(h.get('status').textContent, /Команда выполняется/);
  assert.equal(h.get('approve').disabled, true);
  state = 'succeeded';
  await h.poll();
  assert.match(h.get('status').textContent, /Команда выполнена успешно/);
  assert.equal(h.get('output').hidden, false);
  assert.match(h.get('output').textContent, /00001 \[stdout\] cache-stdout/);
  assert.match(h.get('output').textContent, /00002 \[stderr\] cache-stderr/);
  assert.equal(h.get('cancel').disabled, true);
  assert.equal(h.hasPoll(), false);
  const published = h.calls.filter(c => c.method === 'ui/update-model-context').at(-1).params.structuredContent.pcJob;
  assert.equal(published.uiVersion, '1.1.5');
  assert.equal(published.outputSource, 'pc_job_status.output.records');
  assert.equal(published.renderedStdout, 1);
  assert.equal(published.renderedStderr, 1);
  assert.equal(published.renderedRecords, 2);
});

test('tools/call errors identify the failing tool and RPC without exposing private arguments', async t => {
  const initial = result('rpc-diagnostic');
  const h = harness(t, 'approve.html', initial, {
    pc_job_status: () => ({...initial.structuredContent, output: empty}),
    pc_approve_run: () => {throw Error('host rejected call');}
  });
  await until(() => !h.get('approve').disabled);
  await assert.rejects(h.window.PC.tool('pc_approve_run',
    {id: initial.structuredContent.id, approval_nonce: 'private-diagnostic-nonce'}), e => {
      assert.match(e.message, /tools\/call pc_approve_run/);
      assert.equal(e.code, -32602);
      assert.ok(Number.isInteger(e.rpcID));
      assert.equal(e.message.includes('private-diagnostic-nonce'), false);
      return true;
    });
});
