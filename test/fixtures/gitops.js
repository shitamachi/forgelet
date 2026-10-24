// Test-only GitHub Git Data API backed by actual Git objects and smart HTTP.
// Flux clones the same bare repository that forgelet updates through its real
// GitHub adapter. This fixture never contacts or hosts user repositories.
const fs = require('fs');
const https = require('https');
const cp = require('child_process');
const path = require('path');
const root = '/tmp/git-fixture';
const repository = root + '/test/gitops.git';
fs.mkdirSync(path.dirname(repository), {recursive:true});
if (!fs.existsSync(repository)) cp.execFileSync('git', ['init', '--bare', '--initial-branch=main', repository]);
const git = (args, options={}) => cp.execFileSync('git', ['--git-dir='+repository, ...args], {...options, env:{...process.env, GIT_AUTHOR_NAME:'fixture',GIT_AUTHOR_EMAIL:'test@example.invalid',GIT_COMMITTER_NAME:'fixture',GIT_COMMITTER_EMAIL:'test@example.invalid',...options.env}}).toString().trim();
const exists = ref => {try {return git(['rev-parse','--verify',ref]);} catch {return null;}};
const ancestor = (base,head) => {try {git(['merge-base','--is-ancestor',base,head]);return true;}catch{return false;}};
function tree(base, entries) {
  const index=root+'/index-'+Math.random().toString(16).slice(2), env={GIT_INDEX_FILE:index};
  try {
    git(['read-tree',base || '--empty'],{env});
    for (const entry of entries) {
      const sha=git(['hash-object','-w','--stdin'],{input:entry.content});
      git(['update-index','--add','--cacheinfo',entry.mode || '100644',sha,entry.path],{env});
    }
    return git(['write-tree'],{env});
  } finally {try{fs.unlinkSync(index);}catch{}}
}
let prs=[];
https.createServer({key:fs.readFileSync('/tls/tls.key'),cert:fs.readFileSync('/tls/tls.crt')}, (req,res)=>{
  const u=new URL(req.url,'https://git.forgelet.svc');
  if (u.pathname.startsWith('/test/gitops.git/')) {
    const child=cp.spawn('git',['http-backend'],{env:{...process.env,GIT_PROJECT_ROOT:root,GIT_HTTP_EXPORT_ALL:'1',PATH_INFO:u.pathname,QUERY_STRING:u.search.slice(1),REQUEST_METHOD:req.method,CONTENT_TYPE:req.headers['content-type'] || '',CONTENT_LENGTH:req.headers['content-length'] || ''}});
    let pending=Buffer.alloc(0), headers=false;
    child.stdout.on('data',chunk=>{
      if(headers){res.write(chunk);return;}
      pending=Buffer.concat([pending,chunk]);const end=pending.indexOf('\r\n\r\n');if(end<0)return;
      let status=200;const values={};for(const line of pending.subarray(0,end).toString().split('\r\n')){const at=line.indexOf(':');if(at<0)continue;const k=line.slice(0,at),v=line.slice(at+1).trim();if(k.toLowerCase()==='status')status=Number(v.split(' ')[0]);else values[k]=v;}
      res.writeHead(status,values);headers=true;res.write(pending.subarray(end+4));pending=null;
    });
    child.on('close',()=>res.end());child.on('error',()=>{res.statusCode=500;res.end();});req.pipe(child.stdin);return;
  }
  let chunks=[];req.on('data',chunk=>chunks.push(chunk));req.on('end',()=>{
    const send=(body,status=200)=>{res.writeHead(status,{'content-type':'application/json'});res.end(JSON.stringify(body));};
    let body={};try{const data=Buffer.concat(chunks);if(data.length)body=JSON.parse(data);}catch{return send({error:'json'},400);}
    try {
      if(u.pathname==='/healthz')return send({ok:true});
      if(u.pathname.startsWith('/merge/') && req.method==='POST') {
        const pr=prs[Number(u.pathname.split('/').pop())-1],ref='refs/heads/'+pr.base.ref,old=exists(ref);
        const tr=git(['show','--format=%T','-s',pr.head.sha]);
        const sha=git(['commit-tree',tr,'-p',old,'-p',pr.head.sha],{input:'Merge fixture PR\n'});
        git(['update-ref',ref,sha,old]);pr.merged=true;pr.state='closed';pr.merge_commit_sha=sha;return send({sha});
      }

      if(u.pathname==='/seed' && req.method==='POST') {
        const manifest=`apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [deployment.yaml]\nimages:\n- name: app\n  newName: ${body.image}\n  digest: ${body.digest}\n`;
        const deployment=`apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: phase3-app, namespace: forgelet}\nspec:\n  replicas: 1\n  selector: {matchLabels: {app: phase3-app}}\n  template:\n    metadata: {labels: {app: phase3-app}}\n    spec:\n      imagePullSecrets: [{name: forgelet-registry}]\n      containers:\n      - name: app\n        image: app\n        ports: [{containerPort: 8080}]\n        readinessProbe:\n          httpGet: {path: /, port: 8080}\n          periodSeconds: 1\n`;
        const tr=tree(null,[{path:'apps/kustomization.yaml',content:manifest},{path:'apps/deployment.yaml',content:deployment}]);
        const sha=git(['commit-tree',tr],{input:'initial fixture\n'});git(['update-ref','refs/heads/main',sha]);return send({sha});
      }
      const prefix='/repos/test/gitops';if(!u.pathname.startsWith(prefix))return send({},404);const p=u.pathname.slice(prefix.length);
      if(p.startsWith('/git/ref/heads/')){const sha=exists('refs/heads/'+p.slice('/git/ref/heads/'.length));return sha?send({object:{sha}}):send({},404);}
      if(p==='/git/refs' && req.method==='POST'){if(exists(body.ref))return send({},422);git(['update-ref',body.ref,body.sha]);return send({},201);}
      if(p.startsWith('/git/refs/heads/') && req.method==='PATCH'){
        const ref='refs/heads/'+p.slice('/git/refs/heads/'.length),old=exists(ref);
        if(!old || body.force || !ancestor(old,body.sha))return send({},422);
        git(['update-ref',ref,body.sha,old]);return send({object:{sha:body.sha}});
      }
      if(p.startsWith('/git/commits/'))return send({tree:{sha:git(['show','--format=%T','-s',p.slice('/git/commits/'.length)])}});
      if(p.startsWith('/contents/')){const file=p.slice('/contents/'.length),content=cp.execFileSync('git',['--git-dir='+repository,'show',u.searchParams.get('ref')+':'+file]);return send({path:file,type:'file',encoding:'base64',size:content.length,content:content.toString('base64')});}
      if(p==='/git/trees' && req.method==='POST')return send({sha:tree(body.base_tree,body.tree)},201);
      if(p==='/git/commits' && req.method==='POST')return send({sha:git(['commit-tree',body.tree,...body.parents.flatMap(p=>['-p',p])],{input:body.message,env:{GIT_AUTHOR_NAME:body.author.name,GIT_AUTHOR_EMAIL:body.author.email,GIT_AUTHOR_DATE:body.author.date,GIT_COMMITTER_NAME:body.committer.name,GIT_COMMITTER_EMAIL:body.committer.email,GIT_COMMITTER_DATE:body.committer.date}})},201);
      if(p.startsWith('/compare/')){const [base,head]=p.slice('/compare/'.length).split('...');return send({status:base===head?'identical':ancestor(base,head)?'ahead':'diverged'});}
      if(p==='/pulls'&&req.method==='POST'){const pr={number:prs.length+1,html_url:'https://example.invalid/pull/'+(prs.length+1),state:'open',merged:false,head:{sha:exists('refs/heads/'+body.head),ref:body.head},base:{ref:body.base}};prs.push(pr);return send(pr,201);}
      if(p==='/pulls')return send(prs.filter(pr=>pr.head.ref===u.searchParams.get('head').split(':')[1]&&pr.base.ref===u.searchParams.get('base')));
      if(p.startsWith('/pulls/'))return send(prs[Number(p.split('/').pop())-1]);
      return send({},404);
    } catch(error) {console.error(error.message);return send({error:'fixture operation failed'},500);}
  });
}).listen(9443,'0.0.0.0');
