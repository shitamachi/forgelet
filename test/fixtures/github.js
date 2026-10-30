// Test-only GitHub API. The real forgelet GitHub adapter talks HTTPS to this
// sidecar; no account, repository or external Check is modified.
const fs = require('fs');
const https = require('https');
const stateFile = '/fixture-state/github-checks.json';
const checks = fs.existsSync(stateFile) ? JSON.parse(fs.readFileSync(stateFile)) : [];
const grantsFile = '/fixture-state/github-oauth-grants.json';

let check = Math.max(100, ...checks.map(c => c.id));
const workflow = `on:
  push:
    branches: [main]
    paths: ['src/**']
  pull_request:
    branches: [main]
jobs:
  test:
    runs-on: homelab
    steps:
      - run: echo 'event=\${{ github.event_name }} sha=\${{ github.sha }}'
`;
https.createServer({key: fs.readFileSync('/tls/tls.key'), cert: fs.readFileSync('/tls/tls.crt')}, (req, res) => {
  const url = new URL(req.url, 'https://localhost');
  const oauthGranted = fs.existsSync(grantsFile);
  const authorizationMode = fs.existsSync('/fixture-state/github-authorization-mode') ? fs.readFileSync('/fixture-state/github-authorization-mode','utf8').trim() : 'normal';
  const send = (body, status = 200) => {res.writeHead(status, {'content-type': 'application/json'}); res.end(JSON.stringify(body));};
  if (authorizationMode === 'failed' && url.pathname === '/installation/repositories') return send({message:'temporary fixture failure'},503);
  if (url.pathname === '/login/oauth/access_token') {
    let body='';req.on('data',chunk=>{body+=chunk;});req.on('end',()=>{const value=JSON.parse(body);if(value.client_id!=='integration-client'||value.client_secret!=='integration-oauth-client-secret'||!value.code_verifier||value.code_verifier.length<43)return send({error:'invalid_grant'},400);send({access_token:'integration-user-token',token_type:'bearer',scope:'repo,read:user'});});return;
  }
  if (req.headers.authorization === 'Bearer integration-user-token') {
    if (url.pathname === '/user')return send({id:987654,login:'test'});
    if (url.pathname === '/user/repos')return send([{id:14,full_name:'test/oauth',default_branch:'main',owner:{login:'test'},permissions:{admin:true}}]);
    if (req.method === 'PUT' && url.pathname === '/user/installations/7/repositories/14') return send({message:'OAuth App tokens cannot manage GitHub App installations'},403);
    return send({message:'unknown OAuth endpoint'},404);
  }
  if (url.pathname.startsWith('/repos/test/gitops/')) {
    const upstream=https.request({hostname:'git.forgelet.svc',port:9443,path:req.url,method:req.method,headers:req.headers,ca:fs.readFileSync('/tls/tls.crt')},r=>{res.writeHead(r.statusCode,r.headers);r.pipe(res);});
    upstream.on('error',()=>{res.writeHead(503);res.end();});req.pipe(upstream);return;
  }
  if (url.pathname === '/app') return send({id:7,slug:'forgelet-integration'});
  if (url.pathname === '/app/installations') return send([
    {id:7,account:{login:'test',type:'User'},repository_selection:'selected',permissions:{contents:'write',checks:'write'},suspended_at:authorizationMode==='suspended'?new Date().toISOString():null},
    ...(fs.existsSync('/fixture-state/github-cross-account')?[{id:8,account:{login:'test-other',type:'Organization'},repository_selection:'selected',permissions:{contents:'read',checks:'write'},suspended_at:null}]:[]),
  ]);
  if (url.pathname.endsWith('/access_tokens')) {
    let raw='';req.on('data',chunk=>raw+=chunk);req.on('end',()=>{
      const b=JSON.parse(raw), file='/fixture-state/credential-failures';
      if (b.repositories?.[0]==='project' && b.permissions?.contents==='read' && !b.permissions.pull_requests && fs.existsSync(file)) {
        const n=Number(fs.readFileSync(file,'utf8'));
        if(n>0){fs.writeFileSync(file,String(n-1));return send({},503);}
        if(n<0)return send({},403);
      }
      send({token:url.pathname.includes('/installations/8/')?'integration-installation-token-8':'integration-installation-token',expires_at:new Date(Date.now()+3600000).toISOString()},201);
    });return;
  }
  if (url.pathname === '/repos/test/project/git/ref/heads/main') return send({object:{sha:fs.existsSync('/fixture-state/source-head')?fs.readFileSync('/fixture-state/source-head','utf8').trim():'a'.repeat(40)}});
  if (!/^(Bearer|token) integration-installation-token(?:-8)?$/i.test(req.headers.authorization || '')) return send({message: 'test auth required'}, 401);
  const businessRepo=url.pathname.match(/^\/repos\/shitamachi\/(steam-admin-web|steam-admin|steam-platform-web|steam-login|steam-api)$/)?.[1];
  if(businessRepo)return send({id:1001,name:businessRepo,full_name:'shitamachi/'+businessRepo,private:true,html_url:'https://github.com/shitamachi/'+businessRepo,default_branch:'main',description:'Original business workflow acceptance',license:null,owner:{login:'shitamachi'}});
  if (url.pathname === '/installation/repositories') {
    if (/integration-installation-token-8$/i.test(req.headers.authorization || '')) return send({total_count:1,repositories:[{id:21,full_name:'test-other/project',default_branch:'main'}]});
    const repositories=[...(authorizationMode==='removed'?[]:[{id:11,full_name:'test/project',default_branch:'main'}]),{id:12,full_name:'test/console',default_branch:'main'},{id:13,full_name:'test/onboarding',default_branch:'main'},...(oauthGranted?[{id:14,full_name:'test/oauth',default_branch:'main'}]:[]),...['steam-admin-web','steam-admin','steam-platform-web','steam-login','steam-api'].map((name,i)=>({id:1001+i,full_name:'shitamachi/'+name,default_branch:'main'}))];
    return send({total_count:repositories.length,repositories});
  }
  if (url.pathname === '/repos/test/project/pulls/12') {
    const ready = !fs.existsSync('/fixture-state/pr-merge-pending');
    return send({number:12,state:'open',mergeable:ready?true:null,
      head:{sha:'e'.repeat(40),repo:{full_name:'test/project',fork:false}},
      base:{sha:'b'.repeat(40),ref:'main',repo:{full_name:'test/project'}}});
  }
  if (url.pathname === '/repos/test/project/git/ref/pull/12/merge') return send({object:{type:'commit',sha:'d'.repeat(40)}});
  if (url.pathname === '/repos/test/project/git/commits/'+'d'.repeat(40)) return send({sha:'d'.repeat(40),parents:[{sha:'b'.repeat(40)},{sha:'e'.repeat(40)}]});
  if (url.pathname.endsWith('/commits')) return send([{sha:'b'.repeat(40)}]);
  const onboarding = 'name: Onboarding integration\non: push\njobs:\n  check:\n    runs-on: homelab\n    steps:\n      - run: echo onboarding-real-k3s\n';
  const oauthWorkflow = 'name: OAuth integration\non: push\njobs:\n  check:\n    runs-on: homelab\n    steps:\n      - run: echo oauth-first-run-real-k3s\n';
  if (url.pathname === '/repos/test/oauth/contents/.github/workflows') return send([{name:'ci.yml',path:'.github/workflows/ci.yml',type:'file',size:Buffer.byteLength(oauthWorkflow)}]);
  if (url.pathname === '/repos/test/oauth/contents/.github/workflows/ci.yml') return send({path:'.github/workflows/ci.yml',type:'file',size:Buffer.byteLength(oauthWorkflow),encoding:'base64',content:Buffer.from(oauthWorkflow).toString('base64')});
  if (url.pathname === '/repos/test/onboarding/contents/.github/workflows') return send([{name:'ci.yml',path:'.github/workflows/ci.yml',type:'file',size:Buffer.byteLength(onboarding)}]);
  if (url.pathname === '/repos/test/onboarding/contents/.github/workflows/ci.yml') return send({path:'.github/workflows/ci.yml',type:'file',size:Buffer.byteLength(onboarding),encoding:'base64',content:Buffer.from(onboarding).toString('base64')});
  if (url.pathname.includes('/compare/')) return send({files: [{filename: 'src/main.go'}]});
  if (url.pathname.endsWith('/contents/.github/workflows')) return send([{name:'events.yml', path:'.github/workflows/events.yml', type:'file', size:Buffer.byteLength(workflow)}]);
  if (url.pathname.endsWith('/contents/.github/workflows/events.yml')) return send({path:'.github/workflows/events.yml',type:'file',size:Buffer.byteLength(workflow),encoding:'base64',content:Buffer.from(workflow).toString('base64')});
  if (req.method === 'GET' && url.pathname.endsWith('/check-runs')) {
    const sha = url.pathname.split('/').at(-2);
    return send({check_runs: checks.filter(c => c.head_sha === sha)});
  }
  if (req.method === 'GET' && url.pathname.includes('/check-runs/')) {
    const found = checks.find(c => c.id === Number(url.pathname.split('/').pop()));
    return send(found || {message: 'unknown check'}, found ? 200 : 404);
  }
  if ((req.method === 'POST' && url.pathname.endsWith('/check-runs')) || (req.method === 'PATCH' && url.pathname.includes('/check-runs/'))) {
    let body = '';
    req.on('data', chunk => {body += chunk;});
    req.on('end', () => {
      const input = JSON.parse(body);
      const result = req.method === 'POST' ? {id: ++check, app: {id: 7}} : checks.find(c => c.id === Number(url.pathname.split('/').pop()));
      if (!result) return send({message:'unknown check'},404);
      Object.assign(result, input);
      if (req.method === 'POST') checks.push(result);
      fs.writeFileSync(stateFile, JSON.stringify(checks));
      send(result, req.method === 'POST' ? 201 : 200);
    });
    return;
  }
  return send({message:'unknown test endpoint'},404);
}).listen(9443, '0.0.0.0');
