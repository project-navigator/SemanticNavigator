// Run only via scripts/test-host.sh in an isolated VS Code profile/workspace.
const vscode = require('vscode');
const assert = require('node:assert/strict');
const http = require('node:http');
const fs = require('node:fs/promises');
const path = require('node:path');
const { setTimeout: delay } = require('node:timers/promises');

async function waitUntil(check, message) {
  const deadline=Date.now()+30000;
  while(Date.now()<deadline) { if(await check()) return; await delay(150); }
  throw new Error(message);
}
exports.run = async function() {
  let requests=0;
  const server=http.createServer((req,res)=>{
    let body='';req.on('data',chunk=>body+=chunk);req.on('end',()=>{
      assert.equal(req.headers.authorization,undefined);
      requests++;
      const {input}=JSON.parse(body);
      res.setHeader('Content-Type','application/json');
      res.end(JSON.stringify({data:input.map((text,index)=>({index,embedding:[1,text.length/10000,.5]}))}));
    });
  });
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  try {
    const extension=vscode.extensions.getExtension('kontur-team.semantic-navigator');
    assert.ok(extension,'extension missing');
    const api=await extension.activate();
    assert.equal(requests,0,'activation without enabled sent source code');
    const config=vscode.workspace.getConfiguration('semanticNavigator');
    await config.update('providerUrl',`http://127.0.0.1:${server.address().port}/v1`,vscode.ConfigurationTarget.Workspace);
    await config.update('model','mock-host',vscode.ConfigurationTarget.Workspace);
    await config.update('apiKey','',vscode.ConfigurationTarget.Workspace);
    await config.update('enabled',true,vscode.ConfigurationTarget.Workspace);
    let state;
    await waitUntil(async()=>{try {state=await api.getStatus();return state.state==='ready'&&state.indexedChunks===12;}catch{return false;}},'initial 12 chunks not indexed');
    const found=await api.search('ValidateToken');
    assert.equal(found.matches[0].symbolName,'ValidateToken');
    const first=found.matches[0];
    const document=await vscode.workspace.openTextDocument(first.filePath);
    await vscode.window.showTextDocument(document,{selection:new vscode.Range(first.startLine-1,0,first.endLine-1,0)});
    assert.equal(vscode.window.activeTextEditor.selection.start.line,first.startLine-1);
    const root=vscode.workspace.workspaceFolders[0].uri.fsPath;
    const file=path.join(root,'Added.cs');
    await fs.writeFile(file,'namespace CodeBY.Shop;\npublic class Added\n{\n public int AddedMethod() => 7;\n}\n');
    await waitUntil(async()=> (await api.getStatus()).indexedChunks===13,'create watcher missed');
    await fs.writeFile(file,'namespace CodeBY.Shop;\npublic class Added\n{\n public int ChangedMethod() => 8;\n}\n');
    await waitUntil(async()=> (await api.search('ChangedMethod')).matches.some(m=>m.symbolName==='ChangedMethod'),'change watcher missed');
    const renamed=path.join(root,'Renamed.cs'); await fs.rename(file,renamed);
    await waitUntil(async()=> (await api.search('ChangedMethod')).matches.some(m=>m.relativePath==='Renamed.cs'),'rename watcher missed');
    await fs.unlink(renamed);
    await waitUntil(async()=> (await api.getStatus()).indexedChunks===12,'delete watcher missed');
    const directory=path.join(root,'Nested'); await fs.mkdir(directory);
    await fs.writeFile(path.join(directory,'UPPER.CS'),'namespace CodeBY.Shop;\npublic class Nested\n{\n public int NestedMethod() => 9;\n}\n');
    await waitUntil(async()=> (await api.getStatus()).indexedChunks===13,'uppercase source/create directory missed');
    const moved=path.join(root,'Moved'); await fs.rename(directory,moved);
    await waitUntil(async()=> (await api.search('NestedMethod')).matches.some(m=>m.relativePath==='Moved/UPPER.CS'),'directory rename missed');
    await fs.rm(moved,{recursive:true});
    await waitUntil(async()=> (await api.getStatus()).indexedChunks===12,'directory delete missed');
    const before=requests;
    await config.update('model','mock-host-v2',vscode.ConfigurationTarget.Workspace);
    await waitUntil(async()=>{try {return requests>before&&(await api.getStatus()).indexedChunks===12;}catch{return false;}},'model change did not rebuild');
    await vscode.commands.executeCommand('semanticNavigator.search');
    await vscode.commands.executeCommand('workbench.action.closeQuickOpen');
    await vscode.commands.executeCommand('workbench.view.extension.semantic-navigator');
    const report={result:'PASS',source:process.env.SEMANTIC_HOST_SOURCE,hostVersion:vscode.version,provider:'local mock, no API key',indexedChunks:12,checks:['activation opt-in','native engine startup','indexing','exact symbol search','editor line selection','create/change/rename/delete watcher','directory rename/delete and uppercase extension','model invalidation','search command','sidebar command']};
    await fs.writeFile(process.env.SEMANTIC_HOST_REPORT,JSON.stringify(report,null,2)+'\n');
    console.log(JSON.stringify(report));
    await config.update('enabled',false,vscode.ConfigurationTarget.Workspace);
  } finally { await new Promise(resolve=>server.close(resolve)); }
};
