#!/usr/bin/env python3
"""Explicit operator tools for the dedicated test repositories and homelab sandbox.

No business checkout is read. Private configuration and credentials stay in the
caller-selected state directory and Kubernetes Secrets, never in this repository.
"""
import argparse
import base64
import datetime
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import re
import subprocess
import time
import urllib.request

APP = "shitamachi/forgelet-e2e-app"
SDK = "shitamachi/forgelet-onboarding-e2e"
INSTALLATION = 174462839
NAMESPACE = "forgelet-lab"
BASE = "https://forgelet.k.mygo.rocks"

def shell(args, data=None):
    return subprocess.check_output(args, input=data)

def kubectl(*args, data=None):
    return shell(["kubectl", *args], data)

def save(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    data = value if isinstance(value, bytes) else json.dumps(value, indent=2).encode()+b"\n"
    descriptor = os.open(path, os.O_WRONLY|os.O_CREAT|os.O_TRUNC, 0o600)
    os.fchmod(descriptor, 0o600)
    with os.fdopen(descriptor,"wb") as file:
        file.write(data)

def github(path, token, body=None):
    request = urllib.request.Request("https://api.github.com"+path,
        data=None if body is None else json.dumps(body).encode(),
        headers={"Authorization":"Bearer "+token,"Accept":"application/vnd.github+json","User-Agent":"forgelet-production-like-lab","Content-Type":"application/json"})
    with urllib.request.urlopen(request, timeout=45) as response:
        return json.load(response)

def read_token(root, repository_ids=None):
    encode=lambda data:base64.urlsafe_b64encode(data).decode().rstrip("=")
    payload=encode(b'{"alg":"RS256","typ":"JWT"}')+"."+encode(json.dumps({"iat":int(time.time())-60,"exp":int(time.time())+300,"iss":"5441016"}).encode())
    signature=shell(["openssl","dgst","-sha256","-sign",str(root.parent/"github-app-private-key.pem")],payload.encode())
    if repository_ids is None: repository_ids=[1430960904,1431908513]
    result=github(f"/app/installations/{INSTALLATION}/access_tokens",payload+"."+encode(signature),{"repository_ids":repository_ids,"permissions":{"contents":"read"}})
    if sorted(repo["id"] for repo in result["repositories"]) != sorted(repository_ids):
        raise ValueError("GitHub returned a broader repository scope than requested")
    allowed={"contents":"read","metadata":"read"}
    if result["permissions"].get("contents") != "read" or any(allowed.get(key)!=value for key,value in result["permissions"].items()):
        raise ValueError("GitHub returned unexpected repository permissions")
    return result["token"]

def registry(root):
    config=json.loads((root/"docker/config.json").read_text())
    auth=config["auths"]["registry.shitamchi.com"]
    if "auth" in auth:
        return base64.b64decode(auth["auth"]).decode().split(":",1)
    return auth["username"],auth["password"]

def forgelet(root,path,body=None,method=None):
    jar=http.cookiejar.MozillaCookieJar(str(root/"cookies.txt"));jar.load(ignore_discard=True,ignore_expires=True)
    client=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    request=urllib.request.Request(BASE+path,method=method or ("POST" if body is not None else "GET"),data=None if body is None else json.dumps(body).encode(),headers={"Content-Type":"application/json","Origin":BASE})
    with client.open(request,timeout=45) as response:
        return json.load(response)

def apply(obj):
    print(kubectl("apply","-f","-",data=json.dumps(obj).encode()).decode().strip(),flush=True)

def event_inputs(inputs):
    # github.event.inputs carries strings. The runner derives the typed inputs
    # context using workflow_dispatch's declarations.
    return {key: str(value).lower() if isinstance(value,bool) else str(value) for key,value in inputs.items()}

def validate_acceptance(result,run,logs):
    if result.get("state") != "success": return
    if run["inputs"].get("force_failure"):
        result.update(state="failure",error="requested failure probe did not fail")
        return
    if run["inputs"].get("postgres"):
        executed=False
        for line in logs.decode(errors="replace").splitlines():
            try: entry=json.loads(line)
            except json.JSONDecodeError: continue
            if entry.get("step")=="Required PostgreSQL integration" and entry.get("stepResult")=="success" and entry.get("stage")=="Main": executed=True
        if not executed: result.update(state="failure",error="requested PostgreSQL acceptance did not execute successfully")

def setup(root):
    metadata=json.loads((root/"profile-image.json").read_text())
    image="registry.shitamchi.com/forgelet/act-lab@"+metadata["containerimage.digest"]
    save(root/"profile.json",{"image":image})
    apply({"apiVersion":"v1","kind":"Namespace","metadata":{"name":NAMESPACE,"labels":{"app.kubernetes.io/part-of":"forgelet-experiments"}}})
    source=json.loads(kubectl("-n","forgelet","get","secret","forgelet-registry","-o","json"))
    apply({"apiVersion":"v1","kind":"Secret","metadata":{"name":"registry","namespace":NAMESPACE},"type":"kubernetes.io/dockerconfigjson","data":source["data"]})
    apply({"apiVersion":"v1","kind":"PersistentVolumeClaim","metadata":{"name":"kernel-cache","namespace":NAMESPACE},"spec":{"storageClassName":"local-path","accessModes":["ReadWriteOnce"],"resources":{"requests":{"storage":"5Gi"}}}})
    print("Configured test-only namespace and persistent cache; setup does not start an engine",flush=True)

def start(root,label,workflow,postgres,unhealthy,version,force_failure,cache_scope):
    if not re.fullmatch(r"[a-z][a-z0-9-]{0,35}",label):
        raise ValueError("label must be a short Kubernetes identifier")
    if not re.fullmatch(r"[a-z][a-z0-9-]{0,35}",cache_scope):
        raise ValueError("cache scope must be a short identifier")
    profile=json.loads((root/"profile.json").read_text())["image"]
    token=read_token(root,[1430960904])
    module_token=read_token(root,[1431908513])
    repo=github("/repos/"+APP,token)
    commit=github("/repos/"+APP+"/commits/main",token)["sha"]
    request=urllib.request.Request("https://api.github.com/repos/"+APP+"/tarball/"+commit,headers={"Authorization":"Bearer "+token,"User-Agent":"forgelet-production-like-lab"})
    with urllib.request.urlopen(request,timeout=45) as response:
        snapshot=response.read(1<<20)
        if len(snapshot)>=1<<20: raise ValueError("fixture snapshot exceeds the Secret budget")
    username,password=registry(root)
    inputs={"postgres":postgres,"unhealthy":unhealthy,"version":version,"force_failure":force_failure}
    event={"ref":"refs/heads/main","after":commit,"repository":repo,"inputs":event_inputs(inputs),"sender":{"login":"shitamachi"}}
    req={"mode":"workflow","workdir":"/workspace/source","workflow":".github/workflows/"+workflow,"event_name":"workflow_dispatch","event_path":"/config/event.json","cache_dir":"/cache/kernel","cache":True,"docker":True,"runner_image":profile,"engine_socket":"/run/probe/docker.sock","action_sources":"/opt/probe/action-sources","timeout_seconds":1500,
      "secrets":{"GITHUB_TOKEN":token,"PRIVATE_MODULE_TOKEN":module_token,"REGISTRY_USERNAME":username,"REGISTRY_PASSWORD":password,"DOCKER_USERNAME":username,"DOCKER_PASSWORD":password},
      "env":{"GITHUB_REPOSITORY":APP,"GITHUB_REPOSITORY_OWNER":"shitamachi","SHA_REF":commit,"GITHUB_SHA":commit,"GITHUB_REF":"refs/heads/main","GITHUB_RUN_ID":str(int(time.time()*1000)),"GITHUB_RUN_NUMBER":"1","DOCKER_HOST":"unix:///var/run/docker.sock","TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE":"/run/probe/docker.sock","GOMODCACHE":"/cache/gomod","GOCACHE":"/cache/gobuild"}}
    # Cache destinations belong to the fresh job filesystem. Only the cache
    # service archive persists, so a hit must restore bytes over HTTP.
    req["cache_dir"]="/cache/kernel/"+cache_scope
    req["env"]["GOMODCACHE"]="/workspace/.cache/gomod"
    req["env"]["GOCACHE"]="/workspace/.cache/gobuild"
    name="lab-"+label+"-"+str(int(time.time()))
    secret={"apiVersion":"v1","kind":"Secret","metadata":{"name":name,"namespace":NAMESPACE},"type":"Opaque","data":{"request.json":base64.b64encode(json.dumps(req).encode()).decode(),"event.json":base64.b64encode(json.dumps(event).encode()).decode(),"source.tgz":base64.b64encode(snapshot).decode()}}
    apply(secret)
    mounts=[{"name":"workspace","mountPath":"/workspace"},{"name":"cache","mountPath":"/cache"},{"name":"socket","mountPath":"/run/probe"}]
    bootstrap='''set -eu
mkdir -p /workspace/source
tar xzf /config/source.tgz --strip-components=1 -C /workspace/source
cd /workspace/source
git init -q -b main
git config user.email lab@example.invalid
git config user.name forgelet-lab
git add .
git commit -qm "fixed source snapshot"
git remote add origin https://github.com/shitamachi/forgelet-e2e-app
for i in $(seq 1 90); do docker info >/dev/null 2>&1 && break; sleep 1; done
docker info >/dev/null
node -e 'const fs=require("fs");const r=JSON.parse(fs.readFileSync("/config/request.json"));r.cache_listen_ip=process.env.POD_IP;r.env.TESTCONTAINERS_HOST_OVERRIDE=process.env.POD_IP;fs.writeFileSync("/workspace/request.json",JSON.stringify(r),{mode:0o600})'
exec /usr/local/bin/act-kernel < /workspace/request.json
'''
    pod={"restartPolicy":"Never","automountServiceAccountToken":False,"enableServiceLinks":False,"nodeSelector":{"kubernetes.io/hostname":"node1"},"imagePullSecrets":[{"name":"registry"}],"terminationGracePeriodSeconds":45,
      "initContainers":[{"name":"engine","image":"docker:28.5.1-dind","restartPolicy":"Always","command":["dockerd","--host=unix:///run/probe/docker.sock","--storage-driver=overlay2"],"securityContext":{"privileged":True},"resources":{"requests":{"cpu":"200m","memory":"256Mi"},"limits":{"cpu":"3","memory":"4Gi"}},"volumeMounts":mounts+[{"name":"engine-data","mountPath":"/var/lib/docker"}]}],
      "containers":[{"name":"worker","image":profile,"command":["sh","-c",bootstrap],"env":[{"name":"HOME","value":"/workspace/home"},{"name":"DOCKER_HOST","value":"unix:///run/probe/docker.sock"},{"name":"POD_IP","valueFrom":{"fieldRef":{"fieldPath":"status.podIP"}}}],"resources":{"requests":{"cpu":"100m","memory":"192Mi"},"limits":{"cpu":"2","memory":"1Gi"}},"volumeMounts":mounts+[{"name":"request","mountPath":"/config","readOnly":True}]}],
      "volumes":[{"name":"workspace","emptyDir":{}},{"name":"cache","persistentVolumeClaim":{"claimName":"kernel-cache"}},{"name":"socket","emptyDir":{}},{"name":"engine-data","emptyDir":{"sizeLimit":"12Gi"}},{"name":"request","secret":{"secretName":name,"defaultMode":0o400}}]}
    apply({"apiVersion":"batch/v1","kind":"Job","metadata":{"name":name,"namespace":NAMESPACE,"labels":{"app.kubernetes.io/part-of":"forgelet-experiments"}},"spec":{"backoffLimit":0,"activeDeadlineSeconds":1600,"ttlSecondsAfterFinished":3600,"template":{"metadata":{"labels":{"lab-run":name}},"spec":pod}}})
    save(root/(label+"-run.json"),{"job":name,"sha":commit,"workflow":workflow,"inputs":inputs,"cache_scope":cache_scope,"started_at":datetime.datetime.now(datetime.timezone.utc).isoformat()})
    print("Started",name,"at",commit,flush=True)

def collect(root,label):
    item=json.loads((root/(label+"-run.json")).read_text());name=item["job"]
    pods=json.loads(kubectl("-n",NAMESPACE,"get","pod","-l","lab-run="+name,"-o","json"))["items"]
    if not pods: raise ValueError("job Pod not found")
    pod=pods[0]
    status=pod["status"]
    if status.get("reason") == "Evicted":
        result={"state":"failure","pod_phase":status["phase"],"reason":status["reason"],"message":status.get("message",""),"engine_running":False,"pod_uid":pod["metadata"]["uid"]}
        save(root/(label+"-result.json"),result)
        print(json.dumps(result,indent=2));return
    logs=kubectl("-n",NAMESPACE,"logs",pod["metadata"]["name"],"-c","worker")
    secret=json.loads(kubectl("-n",NAMESPACE,"get","secret",name,"-o","json"))
    private_request=json.loads(base64.b64decode(secret["data"]["request.json"]))
    for value in private_request["secrets"].values():
        if value and value.encode() in logs:
            raise ValueError("a credential escaped the kernel log boundary")
    save(root/(label+".log"),logs)
    result=None
    for line in logs.decode(errors="replace").splitlines():
        try: parsed=json.loads(line)
        except json.JSONDecodeError: continue
        if "state" in parsed and "msg" not in parsed: result=parsed
    if result is None:
        print("running",pod["status"]["phase"],"log bytes",len(logs));return
    validate_acceptance(result,item,logs)
    result["pod_uid"]=pod["metadata"]["uid"]
    result["pod_phase"]=pod["status"]["phase"]
    result["engine_running"]=any(status.get("state",{}).get("running") for status in pod["status"].get("initContainerStatuses",[]))
    if result["engine_running"] or result["pod_phase"] not in ["Succeeded","Failed"]:
        print("finishing: waiting for Pod-owned engine shutdown");return
    save(root/(label+"-result.json"),result)
    print(json.dumps(result,indent=2))

def deliver(root,label):
    result=json.loads((root/(label+"-result.json")).read_text());run=json.loads((root/(label+"-run.json")).read_text())
    digest=result.get("job_outputs",{}).get("build-push",{}).get("digest","")
    if result["state"]!="success" or result.get("pod_phase")!="Succeeded" or result.get("engine_running") is not False or not re.fullmatch(r"sha256:[0-9a-f]{64}",digest): raise ValueError("refusing delivery without a completed image workflow, stopped engine and immutable digest")
    management=forgelet(root,"/api/management")
    response=forgelet(root,"/api/github/run",{"repository_id":1430960904,"version":management["version"],"sha":run["sha"],"path":".github/workflows/homelab.yml","ref":"refs/heads/main","event_name":"workflow_dispatch","inputs":{"digest":digest},"request_id":"lab-"+label+"-"+str(int(time.time()))})
    save(root/(label+"-delivery-run.json"),response)
    print(json.dumps(response,indent=2))

def cancel(root,label):
    item=json.loads((root/(label+"-run.json")).read_text());name=item["job"]
    job=json.loads(kubectl("-n",NAMESPACE,"get","job",name,"-o","json"))
    if job["metadata"].get("labels",{}).get("app.kubernetes.io/part-of")!="forgelet-experiments":
        raise ValueError("refusing to delete a Job not owned by this experiment")
    pods=json.loads(kubectl("-n",NAMESPACE,"get","pods","-l","lab-run="+name,"-o","json"))["items"]
    before=[{"uid":p["metadata"]["uid"],"phase":p["status"]["phase"],"engine_running":any(s.get("state",{}).get("running") for s in p["status"].get("initContainerStatuses",[]))} for p in pods]
    kubectl("-n",NAMESPACE,"delete","job",name,"--cascade=foreground","--wait=true","--timeout=90s")
    remaining=json.loads(kubectl("-n",NAMESPACE,"get","pods","-l","lab-run="+name,"-o","json"))["items"]
    if remaining: raise ValueError("cancelled Job still has Pods")
    kubectl("-n",NAMESPACE,"delete","secret",name)
    result={"cancelled":True,"job":name,"before":before,"remaining_pods":0}
    save(root/(label+"-cancel.json"),result)
    print(json.dumps(result,indent=2))

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state-dir",required=True,type=Path)
    commands=parser.add_subparsers(dest="command",required=True)
    commands.add_parser("setup")
    run=commands.add_parser("run");run.add_argument("label");run.add_argument("--workflow",default="image.yml",choices=["ci.yml","image.yml","reliability.yml"]);run.add_argument("--postgres",action="store_true");run.add_argument("--unhealthy",action="store_true");run.add_argument("--force-failure",action="store_true");run.add_argument("--version",default="production-like-v1")
    run.add_argument("--cache-scope",default="shared")
    for name in ["collect","deliver","cancel"]:commands.add_parser(name).add_argument("label")
    args=parser.parse_args();root=args.state_dir.resolve();root.mkdir(parents=True,exist_ok=True,mode=0o700);root.chmod(0o700)
    if args.command=="setup":setup(root)
    elif args.command=="run":start(root,args.label,args.workflow,args.postgres,args.unhealthy,args.version,args.force_failure,args.cache_scope)
    elif args.command=="collect":collect(root,args.label)
    elif args.command=="deliver":deliver(root,args.label)
    elif args.command=="cancel":cancel(root,args.label)

if __name__=="__main__":main()
