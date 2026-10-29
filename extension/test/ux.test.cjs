const { test } = require('node:test');
const assert = require('node:assert/strict');
const Module = require('node:module');
let picker;
const opened = [];
class Position { constructor(line, character) { this.line=line; this.character=character; } }
class Range { constructor(start, end) { this.start=start; this.end=end; } }
const vscode = {
  Position, Range, TextEditorRevealType: { InCenter: 1 },
  workspace: { openTextDocument: async file => ({file}) },
  window: {
    createQuickPick: () => picker = {
      items: [], activeItems: [], selectedItems: [],
      onDidChangeValue(fn) { this.change=fn; }, onDidChangeActive(fn) { this.active=fn; },
      onDidAccept(fn) { this.accept=fn; }, onDidHide(fn) { this.hidden=fn; },
      show() {}, hide() { this.hidden(); }, dispose() { this.disposed=true; },
    },
    showTextDocument: async (document, options) => { opened.push({document, options}); return {revealRange() {}}; },
  },
};
const load = Module._load;
Module._load = function(name, ...args) { return name === 'vscode' ? vscode : load.call(this, name, ...args); };
const { QuickPickSearch } = require('../out/quickPick');
const { EngineClient } = require('../out/engineClient');
Module._load = load;
const flush = () => new Promise(resolve => setImmediate(resolve));
const deferred = () => { let resolve, reject; const promise = new Promise((yes,no) => {resolve=yes;reject=no;}); return {promise,resolve,reject}; };
const match = name => ({filePath:'/demo/Shop.cs', relativePath:'Shop.cs', startLine:4,endLine:8, snippet:'return true;', symbolName:name,score:1});

test('rapid typing ignores stale responses, preserves full semantic results, and serializes requests', async t => {
  t.mock.timers.enable({apis:['setTimeout']});
  const requests=[]; const updates=[];
  const ui = new QuickPickSearch({search(query) {const request=deferred();requests.push({query,...request});return request.promise;}});
  ui.show((query, matches) => updates.push({query,matches}));
  picker.change('first'); t.mock.timers.tick(350);
  assert.equal(requests.length,1);
  picker.change('obsolete'); t.mock.timers.tick(350);
  picker.change('токен'); t.mock.timers.tick(350);
  assert.equal(requests.length,1);
  requests[0].resolve({matches:[match('Stale')]}); await flush();
  assert.deepEqual(requests.map(r=>r.query),['first','токен']);
  assert.equal(updates.length,0);
  const matches=[match('ValidateToken'),match('CheckExpiry')];
  requests[1].resolve({matches}); await flush();
  assert.equal(picker.items.length,2);
  assert.ok(picker.items.every(item=>item.alwaysShow));
  assert.deepEqual(updates,[{query:'токен',matches}]);
  picker.selectedItems=[picker.items[1]]; await picker.accept();
  assert.equal(opened.at(-1).options.selection.start.line,3);
  assert.equal(opened.at(-1).options.selection.end.line,7);
  assert.equal(opened.at(-1).options.preview,false);
  assert.ok(picker.disposed);
});

test('closed QuickPick ignores late responses and late errors', async t => {
  t.mock.timers.enable({apis:['setTimeout']});
  for (const fail of [false,true]) {
    const request=deferred(); let updates=0;
    const ui=new QuickPickSearch({search:()=>request.promise});
    ui.show(()=>updates++); picker.change('query'); t.mock.timers.tick(350); picker.hide();
    if (fail) request.reject(new Error('offline')); else request.resolve({matches:[match('Late')]});
    await flush(); assert.equal(updates,0); assert.deepEqual(picker.items,[]);
  }
});

test('empty results and provider failures have visible, non-navigable explanations', async t => {
  t.mock.timers.enable({apis:['setTimeout']});
  for (const fail of [false,true]) {
    const ui=new QuickPickSearch({search:async()=>{ if(fail) throw new Error('HTTP 429: wait and retry'); return {matches:[]}; }});
    ui.show(); picker.change('query'); t.mock.timers.tick(350); await flush();
    assert.equal(picker.items.length,1); assert.ok(picker.items[0].alwaysShow);
    assert.equal(picker.items[0].match.filePath,undefined);
    picker.hide();
  }
});

test('RPC timeout removes pending requests and late responses cannot resolve them', async t => {
  t.mock.timers.enable({apis:['setTimeout']});
  const client=new EngineClient({appendLine() {}});
  client.process={killed:false, stdin:{write(_data,callback){callback();}}};
  const request=client.search('query');
  const rejected=assert.rejects(request,/timed out/);
  t.mock.timers.tick(25000); await rejected;
  assert.equal(client.pendingRequests.size,0);
  client.handleStdoutLine(JSON.stringify({id:1,result:{matches:[]}}));
  assert.equal(client.pendingRequests.size,0);
});

test('missing engine and explicit invalid binary path fail with recovery advice', async () => {
  const client=new EngineClient({appendLine() {}});
  await assert.rejects(client.search('query'),/not running/);
  assert.throws(()=>client.resolveBinaryPath('/tmp','/does-not-exist/semantic-engine'),/enginePath/);
});
