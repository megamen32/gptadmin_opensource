#!/usr/bin/env python3
"""Compare real old direct-MCP and new exec on identical bounded fixtures.
Counts serialized model-facing call arguments/results and reports wire counts
separately. Token counts are not a claim about any vendor's billing/caching.
"""
from __future__ import annotations
import argparse, contextlib, getpass, hashlib, json, os, re, secrets, socket
import subprocess, tempfile, time, urllib.error
from pathlib import Path
import tiktoken
from exec_context_e2e import MCP, jwt, MARKERS, summary_result
ENC = tiktoken.get_encoding('o200k_base')

class MeasuredMCP(MCP):
    @staticmethod
    def record(label, request, response, elapsed):
        wire=MCP.record(label,request,response,elapsed)
        params=request.get('params',{})
        req = ({'name':params.get('name'),'arguments':params.get('arguments',{})}
               if request.get('method')=='tools/call' else {'method':request.get('method'),'params':params})
        result=response.get('result',response.get('error',response))
        # Structured content is what this connector forwards as its JSON payload.
        # Preserve inner MCP content/metadata; do not just count the chosen value.
        if isinstance(result,dict) and isinstance(result.get('structuredContent'),dict):
            result=result['structuredContent']
        a=json.dumps(req,ensure_ascii=False,separators=(',',':'))
        b=json.dumps(result,ensure_ascii=False,separators=(',',':'))
        wire.update(model_request_bytes=len(a.encode()),model_response_bytes=len(b.encode()),
                    model_request_tokens=len(ENC.encode(a)),model_response_tokens=len(ENC.encode(b)))
        return wire

def totals(rows):
    keys=['model_request_bytes','model_response_bytes','model_request_tokens','model_response_tokens',
          'request_bytes','response_bytes','request_tokens','response_tokens']
    out={k:sum(x[k] for x in rows) for k in keys}
    out.update(calls=len(rows),model_total_tokens=out['model_request_tokens']+out['model_response_tokens'],
               wire_total_tokens=out['request_tokens']+out['response_tokens'],
               elapsed_ms=round(sum(x['elapsed_ms'] for x in rows),3))
    return out

def metric(rows, boot):
    return {'warm':totals(rows),'including_bootstrap':totals(boot+rows)}

def fixture_text(i):
    lines=[f'module={i:02d} locale=Привет task=exact-score']
    lines.extend(f'record={j:03d} status=OK payload=deterministic-content-{i:02d}-{j:03d} '+('abcdef0123456789'*4) for j in range(150))
    lines.append(f'SCORE={i*i}')
    return '\n'.join(lines)+ ('\n' if i%2==0 else '')

def walk(obj):
    if isinstance(obj,dict):
        yield obj
        for key in ('response','result','structuredContent','data'):
            if key in obj:yield from walk(obj[key])
    elif isinstance(obj,list):
        for v in obj:yield from walk(v)

def compact_error(value):
    return json.dumps(value,ensure_ascii=False,default=str)[:1800]

@contextlib.contextmanager
def candidate(binary, fixture, root):
    config=root/'config';config.mkdir(mode=0o700)
    (config/'mcp.json').write_text(json.dumps({'mcpServers':{'bench-fixture':{
        'ref':'bench-fixture','name':'bench fixture','transport':'stdio','command':str(fixture),'args':[],'enabled':True}}}))
    with socket.socket() as s:s.bind(('127.0.0.1',0));port=s.getsockname()[1]
    origin=f'http://127.0.0.1:{port}';secret=secrets.token_urlsafe(32)
    env={k:os.environ[k] for k in ('PATH','HOME','LANG','TZ') if k in os.environ}
    env.update(GPTADMIN_ROOT=str(root),GPTADMIN_CONFIG_DIR=str(config),GPTADMIN_HUB_HOST='127.0.0.1',
               GPTADMIN_HUB_PORT=str(port),GPTADMIN_AUTH_MODE='legacy',GPTADMIN_INSTRUCTION_CONTEXT_REQUIRED='1',
               PUBLIC_ORIGIN=origin,MCP_RESOURCE=origin,OAUTH_CLIENT_SECRET=secret,SHELL_TOKEN=secrets.token_urlsafe(32),
               SHELL_NAME='benchmark',SHELL_HOST='127.0.0.1',SHELL_PORT='0',SHELL_DEFAULT_USER=getpass.getuser(),
               SHELL_DEFAULT_CWD=str(root),SHELL_IDENTITY_DIR=str(config/'identity'),SHELL_SPOOL_DIR=str(config/'spool'),
               SHELLMCP_MCP_CONFIG=str(config/'mcp.json'),MCP_RELAY_DEFAULT_TIMEOUT='60',MCP_RELAY_POLL_MAX_TIMEOUT='60')
    with (root/'node.log').open('wb') as log:
        process=subprocess.Popen([str(binary)],cwd=root,env=env,stdout=log,stderr=subprocess.STDOUT)
        client=MeasuredMCP(origin,jwt(secret,origin))
        try:
            deadline=time.monotonic()+20
            while True:
                if process.poll() is not None:raise RuntimeError('candidate process exited: '+(root/'node.log').read_text(errors='replace')[-1500:])
                try:client.initialize();break
                except (OSError,urllib.error.URLError,TimeoutError):
                    if time.monotonic()>deadline:raise
                    time.sleep(.1)
            yield client
        finally:
            if process.poll() is None:
                process.terminate()
                try:process.wait(timeout=8)
                except subprocess.TimeoutExpired:process.kill();process.wait(timeout=5)

def schema(client,target,tool):
    value=client.call('schema',{'target':target,'tool':tool,'detail':'full'},'bootstrap:schema:'+tool)
    if isinstance(value.get('context'),str):client.context=value['context']
    if value.get('status')!='completed':raise RuntimeError('schema failed: '+compact_error(value))
    return value

def direct(client,target,tool,args,label):
    value=client.call('execute',{'target':target,'tool':tool,'args':args,'detail':'full'},label)
    deadline=time.monotonic()+70
    while value.get('status')=='running' and value.get('job_id'):
        if time.monotonic()>deadline:raise TimeoutError('direct read did not complete')
        time.sleep(.1)
        value=client.call('job',{'id':value['job_id'],'owner_target':target,'detail':'full'},label+':job')
    if value.get('status')=='failed' or 'rpc_error' in value:raise RuntimeError('direct failed: '+compact_error(value))
    return value

def benchmark(binary,fixture,mode,parent):
    root=parent/mode;root.mkdir();sources=root/'sources';sources.mkdir()
    texts=[fixture_text(i) for i in range(50)]
    for i,text in enumerate(texts):(sources/f'source-{i:02d}.txt').write_text(text)
    file_target='file:benchmark';child='mcp:shell:benchmark:bench-fixture'
    with candidate(binary,fixture,root) as c:
        names=[t['name'] for t in c.tools()]
        if mode=='direct':
            assert 'execute' in names,names
            # The baseline has no FTS search: one discovery and exact schemas.
            discovered=c.call('discover',{'query':'benchmark','detail':'compact'},'bootstrap:discover')
            if isinstance(discovered.get('context'),str):c.context=discovered['context']
            for tool,target in [('file_editor',file_target),('large_log',child)]:schema(c,target,tool)
        else:
            assert 'exec' in names and 'execute' not in names,names
            c.search('file editor',file_target,'bootstrap:search-file')
            found=c.search('large_log',child,'bootstrap:search-log')
            if not any(t.get('name')=='large_log' for t in found.get('results',[])):
                raise RuntimeError('cold child search incomplete: '+compact_error(found))
        boot=list(c.records);start=len(c.records)
        if mode=='direct':
            score=0
            for i,text in enumerate(texts):
                value=direct(c,file_target,'file_editor',{'action':'view','path':str(sources/f'source-{i:02d}.txt'),
                    'start_line':1,'end_line':1000,'max_bytes':262144},f'files:read-{i}')
                view=next((v for v in walk(value) if v.get('action')=='view' and isinstance(v.get('view'),str)),None)
                if not view or view.get('truncated'):raise RuntimeError('baseline file incomplete: '+compact_error(value))
                plain='\n'.join(re.sub(r'^\d+:[0-9a-f]{4}\|','',line) for line in view['view'].split('\n'))
                assert plain==text.rstrip('\n'),f'baseline text mismatch {i}'
                score+=int(re.search(r'^SCORE=(\d+)$',plain,re.M)[1])
            answer={'files':50,'score':score}
        else:
            code=f"const f=file('{file_target}'); let score=0; for(let i=0;i<50;i++){{const p={json.dumps(str(sources))}+'/source-'+String(i).padStart(2,'0')+'.txt';const r=await f('read',p,{{end_line:1000,max_bytes:262144}});if(r.truncated||r.text_exact!==true)throw Error('inexact file');const m=r.text.match(/^SCORE=(\\d+)$/m);if(!m)throw Error('missing score');score+=Number(m[1]);}} return {{files:50,score}};"
            answer=summary_result(c.exec(code,'files:aggregate',max_calls=64,max_output_bytes=1024,timeout_ms=60000))
        assert answer=={'files':50,'score':sum(i*i for i in range(50))},answer
        files=metric(c.records[start:],boot);files['answer']=answer;files['fixture_bytes']=sum(len(t.encode()) for t in texts)
        start=len(c.records)
        if mode=='direct':
            value=direct(c,child,'large_log',{},'log:full-direct')
            raw=next((v['text'] for v in walk(value) if isinstance(v.get('text'),str) and len(v['text'])>1000000),None)
            if raw is None:
                for v in walk(value):
                    rows=v.get('content',[])
                    if isinstance(rows,list):
                        raw=next((x.get('text') for x in rows if isinstance(x,dict) and isinstance(x.get('text'),str) and len(x['text'])>1000000),None)
                        if raw:break
            if raw is None:raise RuntimeError('baseline large log is not complete: '+compact_error(value))
            answers=[next(line for line in raw.splitlines() if marker in line) for marker in MARKERS]
            rawbytes=len(raw.encode());followup={'additional_tool_calls':0,'note':'Full log is already retained in model context; no artificial refetch baseline.'}
        else:
            source=summary_result(c.exec(f"return await call('{child}','large_log',{{}},{{index:true,text:'text',name:'benchmark-log'}});",'log:index'))
            rawbytes=source['bytes'];id=source['source']
            answers=[]
            # Separate execs prove durable handle reuse without another MCP fetch.
            for marker in MARKERS:
                code=f"const s={json.dumps(id)};const q={json.dumps(marker)};const h=await mcp.content.search(q,{{source:s,limit:2}});if(!h.hits.length)throw Error('missing marker');const r=await mcp.content.read(s,h.hits[0].chunk_id);return r.text.split('\\n').find(x=>x.includes(q));"
                answers.append(summary_result(c.exec(code,'log:followup:'+marker,max_output_bytes=1024)))
            followup={'separate_execs':3,'refetches':0,'source_bytes':source['bytes'],'chunks':source['chunks']}
        assert answers==list(MARKERS.values()),answers
        log=metric(c.records[start:],boot);log.update(answer=answers,raw_log_bytes=rawbytes,followup=followup)
        stats_start=len(c.records)
        stats=(direct(c,child,'fixture_stats',{},'verify:stats') if mode=='direct' else summary_result(c.exec(f"return await call('{child}','fixture_stats',{{}});",'verify:stats')))
        count=next((v['large_log_calls'] for v in walk(stats) if 'large_log_calls'in v),None)
        assert count==1,('fixture log calls',count)
        start=len(c.records)
        if mode=='direct':tiny=c.call('demo',{},'tiny:direct')
        else:tiny=summary_result(c.exec("return await call('hub','demo',{});",'tiny:exec'))
        assert tiny.get('status')=='ok',tiny
        tiny_result=metric(c.records[start:],boot)
        return {'binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(),'tools':names,'bootstrap':totals(boot),
                'fifty_files':files,'large_log':log,'tiny_full_demo':tiny_result,'large_log_fetch_count':count,
                'verification_overhead':totals(c.records[stats_start:start]),'records':c.records}

def main():
    p=argparse.ArgumentParser();p.add_argument('--baseline',type=Path,required=True);p.add_argument('--candidate',type=Path,required=True);p.add_argument('--fixture',type=Path,required=True);p.add_argument('--report',type=Path,required=True);a=p.parse_args()
    report={'tokenizer':{'name':'tiktoken','version':tiktoken.__version__,'encoding':'o200k_base'},
            'method':'Two real isolated Node processes; same file/log tasks; complete serialized model-facing name+arguments and structured result, including nested MCP envelopes. Bootstrap separately and included in cold totals. Wire counts also reported. Not provider billing, cached-prompt billing, or competitor benchmark.',
            'isolated_name_tokens':{x:len(ENC.encode(x)) for x in ['exec','code_exec','call','mcp.call']}}
    with tempfile.TemporaryDirectory(prefix='gptadmin-token-bench-') as tmp:
        parent=Path(tmp)
        for mode,binary in [('direct',a.baseline),('exec',a.candidate)]:
            report[mode]=benchmark(binary.resolve(),a.fixture.resolve(),mode,parent)
            a.report.parent.mkdir(parents=True,exist_ok=True);a.report.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
    ratios={}
    for task in ['fifty_files','large_log','tiny_full_demo']:
        old=report['direct'][task]['including_bootstrap']['model_total_tokens'];new=report['exec'][task]['including_bootstrap']['model_total_tokens']
        ratios[task]={'direct_tokens':old,'exec_tokens':new,'ratio':round(old/new,3),'saved_percent':round((1-new/old)*100,3)}
    report.update(status='PASS',comparison=ratios);a.report.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
    print(json.dumps({'status':'PASS','comparison':ratios,'report':str(a.report)},ensure_ascii=False))
if __name__=='__main__':main()
