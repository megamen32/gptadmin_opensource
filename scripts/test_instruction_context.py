#!/usr/bin/env python3
"""Real isolated gptadmin-node + embedded executor + issued bearer acceptance.
No production credentials, config, services, or external agents are used.
"""
from http.cookiejar import CookieJar
import argparse, hashlib, json, os, pathlib, secrets, signal, socket, subprocess, tempfile, time, urllib.request, urllib.error, urllib.parse

HERE = pathlib.Path(__file__).resolve().parents[1] / '.tmp' / 'instruction-context'
HERE.mkdir(parents=True, exist_ok=True)

def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--binary', default=str(HERE/'bin/gptadmin-node'))
    parser.add_argument('--expect-commit')
    args=parser.parse_args()
    root=pathlib.Path(tempfile.mkdtemp(prefix='lead-run-',dir=HERE))
    root.chmod(0o700)
    for name in ('config','home','spool','identity','public','artifacts','output','tmp','mcp'):
        (root/name).mkdir()
    with socket.socket() as sock:
        sock.bind(('127.0.0.1',0)); port=sock.getsockname()[1]
    base=f'http://127.0.0.1:{port}'
    rules=root/'rules.md';rules.write_text('Fixture profile instructions version one.')
    ctl=secrets.token_urlsafe(40)
    browser_password=secrets.token_urlsafe(40)
    env={'PATH':os.environ.get('PATH','/usr/bin:/bin'),'HOME':str(root/'home'),'USER':'admin',
         'TMPDIR':str(root/'tmp'),'GOMAXPROCS':'2','GPTADMIN_HUB_HOST':'127.0.0.1','GPTADMIN_HUB_PORT':str(port),
         'GPTADMIN_ROOT':str(root),'GPTADMIN_CONFIG_DIR':str(root/'config'),'GPTADMIN_PUBLIC_DIR':str(root/'public'),
         'GPTADMIN_ARTIFACT_DIR':str(root/'artifacts'),'GPTADMIN_OUTPUT_DIR':str(root/'output'),
         'GPTADMIN_STARTUP_INSTRUCTIONS_FILE':str(rules),'GPTADMIN_INSTRUCTION_CONTEXT_REQUIRED':'1',
         'GPTADMIN_ENV_FILE':str(root/'own.env'),'CTL_TOKEN':ctl,'SHELL_TOKEN':secrets.token_urlsafe(40),
         'ADMIN_PASSWORD':browser_password,'OAUTH_CLIENT_SECRET':secrets.token_urlsafe(40),'PUBLIC_ORIGIN':base,'MCP_RESOURCE':base,
         'GPTADMIN_AUTH_MODE':'legacy','SHELL_NAME':'code-mode-local-canary','SHELL_DEFAULT_USER':'admin',
         'SHELL_DEFAULT_HOME':str(root/'home'),'SHELL_DEFAULT_CWD':str(root),'SHELL_IDENTITY_DIR':str(root/'identity'),
         'SHELL_SPOOL_DIR':str(root/'spool'),'SHELL_OUTBOX_DIR':str(root/'spool/outbox'),
         'SHELLMCP_MCP_CONFIG':str(root/'mcp'),'MCP_RELAY_DEFAULT_TIMEOUT':'6','MCP_RELAY_POLL_MAX_TIMEOUT':'6'}
    report={'ok':False,'state_directory':str(root),'binary_sha256':hashlib.sha256(pathlib.Path(args.binary).read_bytes()).hexdigest(),'scenarios':{}}
    log=open(root/'node.log','wb')
    proc=subprocess.Popen([args.binary],env=env,cwd=root,stdout=log,stderr=subprocess.STDOUT,start_new_session=True)
    def http(path, token=None, body=None, method=None, headers=None):
        hs={'Content-Type':'application/json','Accept':'application/json, text/event-stream'}
        if token: hs['Authorization']='Bearer '+token
        if headers: hs.update(headers)
        req=urllib.request.Request(base+path,data=None if body is None else json.dumps(body).encode(),headers=hs,method=method)
        try:
            with urllib.request.urlopen(req,timeout=15) as r:
                raw=r.read(4*1024*1024)
                return (json.loads(raw) if raw else {}),dict(r.headers)
        except urllib.error.HTTPError as e:
            raise RuntimeError(f'HTTP {e.code} {path}: '+e.read(1500).decode(errors='replace')) from None
    class MCP:
        def __init__(self,token):
            self.token=token;self.session=None;self.protocol='2025-03-26';self.context=''
            result,hs=http('/mcp',token,{'jsonrpc':'2.0','id':1,'method':'initialize','params':{'protocolVersion':self.protocol,'capabilities':{},'clientInfo':{'name':'code-mode-local-acceptance','version':'1'}}})
            assert 'result' in result,result
            self.protocol=result['result'].get('protocolVersion',self.protocol)
            self.session=next((v for k,v in hs.items() if k.lower()=='mcp-session-id'),None)
        def call(self,name,arguments):
            arguments=dict(arguments)
            if self.context and name in ('discover','schema','execute','fleetExec'):
                arguments.setdefault('context',self.context)
            hs={'MCP-Protocol-Version':self.protocol,'Mcp-Method':'tools/call','Mcp-Name':name}
            if self.session: hs['Mcp-Session-Id']=self.session
            body,_=http('/mcp',self.token,{'jsonrpc':'2.0','id':2,'method':'tools/call','params':{'name':name,'arguments':arguments}},headers=hs)
            if 'error' in body: return {'rpc_error':body['error'],'status':'failed'}
            result=body['result']
            if isinstance(result.get('structuredContent'),dict): out=result['structuredContent']
            else:
                texts=[v['text'] for v in result.get('content',[]) if v.get('type')=='text']
                out=json.loads(texts[0]) if texts else result
            if name in ('discover','schema') and isinstance(out.get('context'),str): self.context=out['context']
            return out
        def code(self,code,**limits):
            return self.call('execute',{'target':'hub','tool':'code_mode','args':{'code':code,**limits},'detail':'full'})
    def code_result(outer):
        if isinstance(outer.get('response'),dict): return outer['response']
        error=outer.get('error')
        if isinstance(error,dict):
            if isinstance(error.get('response'),dict): return error['response']
            return error
        return outer
    def check(name,fn):
        start=time.monotonic()
        try:
            data=fn()
            report['scenarios'][name]={'pass':True,'elapsed_ms':round((time.monotonic()-start)*1000),**(data or {})}
        except Exception as exc:
            report['scenarios'][name]={'pass':False,'error':str(exc)[:2200],'elapsed_ms':round((time.monotonic()-start)*1000)}
        print(json.dumps({name:report['scenarios'][name]},ensure_ascii=False),flush=True)
    def expect_success(outer):
        result=code_result(outer)
        assert result.get('status')=='completed',result
        return result
    try:
        deadline=time.monotonic()+15
        while True:
            try: version,_=http('/version');break
            except (OSError,urllib.error.URLError):
                if proc.poll() is not None: raise RuntimeError('candidate exited: '+(root/'node.log').read_text()[-1800:])
                if time.monotonic()>deadline: raise RuntimeError('candidate startup timed out')
                time.sleep(.1)
        report['version']=version
        if args.expect_commit: assert version.get('git_commit')==args.expect_commit,version
        issued,_=http('/admin/api/mcp/issue-token',ctl,{'client_id':'code-mode-full-test','access_mode':'full','ttl_days':1})
        full=MCP(issued['access_token'])
        def first_use():
            marker=root/'before-instructions'
            blocked=full.call('execute',{'target':'shell:code-mode-local-canary','tool':'shell_exec','args':{'cmd':'touch '+str(marker),'timeout':2}})
            assert blocked.get('executed') is False and blocked.get('error',{}).get('code')=='instructions_required',blocked
            assert not marker.exists(),'command ran without current instructions'
            assert 'version one' in blocked.get('instructions','') and blocked.get('context','').startswith('ic1_')
            return {'executed':False,'marker_absent':True,'instruction_text_delivered':True}
        check('first_operation_requires_context',first_use)
        discovery=full.call('discover',{'detail':'full'})
        targets=[row['server_id'] for row in discovery.get('servers',[]) if row.get('server_id','').startswith('shell:')]
        assert len(targets)==1,discovery
        assert full.context and 'version one' in discovery.get('instructions',''),discovery
        target=targets[0];report['target']=target
        shell_schema=full.call('schema',{'target':target,'tool':'shell_exec'})
        assert shell_schema.get('call_ref'),shell_schema
        ref=shell_schema['call_ref']
        def schema_scope():
            chosen=full.call('schema',{'target':target,'tool':'shell_exec','detail':'full'})
            defs=chosen.get('response',{}).get('tools',[])
            assert len(defs)==1 and defs[0].get('name')=='shell_exec',chosen
            assert 'instructions' not in chosen and chosen.get('call_ref') and 'operation_id' not in chosen,chosen
            return {'selected_tools':1,'repeated_instructions':False,'reference_fields':1}
        check('full_schema_scope_and_no_repeat',schema_scope)
        def old_path():
            result=full.call('execute',{'ref':ref,'args':{'cmd':'printf old-path-ok','timeout':5},'detail':'full'})
            assert 'old-path-ok' in json.dumps(result),result
            assert result.get('status')=='completed',result
            report['inspection_job']=result.get('job_id')
            return {'status':'completed'}
        check('old_execute_real_shell',old_path)
        def chain():
            script='const target='+json.dumps(target)+';\n'+'''const schema=await mcp.schema(target,'shell_exec');
if(!schema.ref) throw new Error('schema did not return a call reference');
const first=await mcp.call({ref:schema.ref,args:{cmd:"python3 -c 'print(chr(120)*20000,end=str())'",timeout:5}});
const text=first.result.structuredContent.result.stdout;
const second=await mcp.call({ref:schema.ref,args:{cmd:"printf '%s' '"+text.length+"'",timeout:5}});
return {input_bytes:text.length,verified:Number(second.result.structuredContent.result.stdout)};'''
            outer=full.code(script,timeout_ms=10000,max_output_bytes=512)
            result=expect_success(outer)
            assert result['result']=={'input_bytes':20000,'verified':20000},result
            assert result['calls']==3,result
            serialized=json.dumps(outer)
            assert 'x'*100 not in serialized, 'intermediate payload escaped'
            return {'tool_calls':2,'schema_calls':1,'intermediate_bytes':20000,'outer_response_bytes':len(serialized.encode()),'selected_result':result['result']}
        check('schema_and_two_real_calls',chain)
        # This is a real bound access profile and separately issued bearer, not
        # a missing target pretending to be an authorization denial.
        profile={'id':'hub-only-test','access_mode':'full','approval_mode':'unrestricted','allowed_targets':['hub'],'allowed_tools':['execute','schema','discover','code_mode','status'],'version':1}
        http('/admin/api/access-profiles/hub-only-test',ctl,profile,method='PUT',headers={'If-Match':'*'})
        limited_token,_=http('/admin/api/mcp/issue-token',ctl,{'client_id':'code-mode-limited-test','access_mode':'full','ttl_days':1})
        http('/admin/api/client-bindings/'+limited_token['token_id'],ctl,{'profile_id':'hub-only-test'},method='PUT')
        limited=MCP(limited_token['access_token'])
        limited.call('discover',{})
        marker=root/'MUST_NOT_EXIST'
        def denied():
            pure=expect_success(limited.code('return {allowed:true};'))
            assert pure['result']=={'allowed':True},pure
            script='const target='+json.dumps(target)+'; const cmd='+json.dumps('touch '+str(marker))+';\n'+'''try { await mcp.call({target,tool:'shell_exec',args:{cmd,timeout:2}}); } catch(e) {}
try { await mcp.call({target:'hub',tool:'status',args:{}}); } catch(e) {}
return {incorrect_success:true};'''
            result=code_result(limited.code(script))
            assert result.get('status')=='failed',result
            assert not marker.exists(),'denied command actually executed'
            assert result.get('calls')==1,result
            receipt=result.get('error',{}).get('receipt',{})
            assert receipt.get('http_status')==403 and receipt.get('target')==target,result
            return {'denied_real_existing_target':target,'attempts':result['calls'],'marker_absent':True}
        check('real_bound_profile_and_sticky_failure',denied)
        def loop():
            start=time.monotonic();result=code_result(full.code('while(true){}',timeout_ms=300));elapsed=time.monotonic()-start
            assert result.get('status')=='failed' and result['error']['code']=='timeout',result
            assert elapsed<2,elapsed
            return {'elapsed_ms':round(elapsed*1000)}
        check('loop_timeout',loop)
        def host_sleep():
            start=time.monotonic();result=code_result(full.code("const os=await import('qjs:os');os.sleep(1500);return 1;",timeout_ms=300));elapsed=time.monotonic()-start
            assert result.get('status')=='failed' and result['error']['code']=='capability_denied',result
            assert elapsed<1,elapsed
            return {'elapsed_ms':round(elapsed*1000)}
        check('host_sleep_denied',host_sleep)
        def isolation():
            expect_success(full.code('globalThis.onlyThisRun=42;return [0,false,\"\"];'))
            result=expect_success(full.code('return {prior:typeof globalThis.onlyThisRun,process:typeof process,fetch:typeof fetch};'))
            assert all(v=='undefined' for v in result['result'].values()),result
            return {'fresh_runtime':True}
        check('fresh_sandbox_and_return_values',isolation)
        def compact_result():
            code='return {answer:42};'
            full_output=full.code(code)
            short=full.call('execute',{'target':'hub','tool':'code_mode','args':{'code':code},'detail':'compact'})
            assert short.get('result')=={'answer':42} and 'response' not in short and 'limits' not in short,short
            assert short.get('operation_id'),short
            expanded=full.call('expand',{'id':short['operation_id']})
            assert expanded.get('data',{}).get('response',{}).get('result')=={'answer':42},expanded
            report['inspection_ref']=short['operation_id']
            return {'full_bytes':len(json.dumps(full_output).encode()),'compact_bytes':len(json.dumps(short).encode()),'diagnostics_expandable':True}
        check('compact_result_with_full_disclosure',compact_result)
        def direct_entry():
            member=next(x for x in discovery['servers'] if x['server_id']==target)
            path=member.get('meta',{}).get('public_mcp_path','/server/shell-code-mode-local-canary/mcp')
            params={'name':'shell_exec','arguments':{'cmd':'printf direct-context-ok','timeout':3}}
            envelope={'jsonrpc':'2.0','id':3,'method':'tools/call','params':params}
            headers={'MCP-Protocol-Version':'2025-03-26','Mcp-Method':'tools/call','Mcp-Name':'shell_exec'}
            body,_=http(path,issued['access_token'],envelope,headers=headers)
            blocked=body.get('result',{}).get('structuredContent',{})
            assert blocked.get('executed') is False and blocked.get('error',{}).get('code')=='instructions_required',body
            params['_meta']={'io.gptadmin/context':full.context}
            body,_=http(path,issued['access_token'],envelope,headers=headers)
            assert 'direct-context-ok' in json.dumps(body) and 'instructions_required' not in json.dumps(body),body
            return {'missing_context_refused':True,'metadata_context_executes':True}
        check('direct_mcp_entry_context',direct_entry)
        def action_entry():
            member=next(x for x in discovery['servers'] if x['server_id']==target)
            mcp_path=member.get('meta',{}).get('public_mcp_path','/server/shell-code-mode-local-canary/mcp')
            path=mcp_path.rsplit('/mcp',1)[0]+'/actions/tools/shell_exec'
            marker=root/'actions-context-check'
            body={'cmd':'touch '+str(marker),'timeout':3}
            rejected=False
            try: http(path,issued['access_token'],body)
            except RuntimeError as exc:
                rejected='HTTP 428' in str(exc) and 'instructions_required' in str(exc)
            assert rejected and not marker.exists(),'Actions did not return the expected unexecuted precondition refusal'
            accepted,_=http(path,issued['access_token'],body,headers={'X-GPTAdmin-Context':full.context})
            assert accepted.get('status')=='completed' and marker.exists(),accepted
            return {'missing_context_refused':True,'explicit_context_executes':True}
        check('http_actions_entry_context',action_entry)
        def action_reference():
            schema=full.call('schema',{'target':target,'tool':'shell_exec','detail':'full'})
            result,_=http('/server/hub/actions/tools/execute',issued['access_token'],{'ref':schema['call_ref'],'args':{'cmd':'printf actions-ref-ok','timeout':3},'detail':'full'})
            assert 'actions-ref-ok' in json.dumps(result),result
            return {'reference_only_call_executes':True}
        check('actions_reference_without_explicit_context',action_reference)
        def relay_flat_metadata():
            result,_=http('/mcp-relay/call',issued['access_token'],{'target':'hub','tool':'secret_request','label':'flat metadata fixture','ttl_seconds':60,'_meta':{'io.gptadmin/context':full.context},'detail':'full'})
            assert result.get('response',{}).get('status')=='pending',result
            return {'flat_metadata_not_forwarded_as_arguments':True}
        check('http_relay_flat_metadata',relay_flat_metadata)
        def browser_cloudos():
            jar=CookieJar()
            opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
            form=urllib.parse.urlencode({'password':browser_password,'next':'/admin/'}).encode()
            login=urllib.request.Request(base+'/admin/login',data=form,headers={'Content-Type':'application/x-www-form-urlencoded'})
            try:
                with opener.open(login,timeout=5) as response: response.read(2048)
            except urllib.error.HTTPError:
                pass
            assert any(c.name=='gptadmin_admin_session' for c in jar),'candidate browser login did not establish its session'
            body={'target':target,'cmd':'printf browser-context-ok','timeout':3}
            request=urllib.request.Request(base+'/api/v1/cloud-os/shell/exec',data=json.dumps(body).encode(),headers={'Content-Type':'application/json'})
            with opener.open(request,timeout=15) as response: result=json.loads(response.read())
            assert 'browser-context-ok' in json.dumps(result),result
            refused=False
            try: http('/api/v1/cloud-os/shell/exec',issued['access_token'],body)
            except RuntimeError as exc: refused='HTTP 428' in str(exc) and 'instructions_required' in str(exc)
            assert refused,'bearer CloudOS request bypassed instruction delivery'
            current,_=http('/api/v1/cloud-os/shell/exec',issued['access_token'],body,headers={'X-GPTAdmin-Context':full.context})
            assert 'browser-context-ok' in json.dumps(current),current
            return {'authenticated_browser_works':True,'bearer_requires_context':True}
        check('cloudos_browser_and_bearer_compatibility',browser_cloudos)

        def human_settings():
            result,_=http('/admin/api/settings',ctl,{'tool_output_verbose':False},method='PUT')
            assert 'instructions_required' not in json.dumps(result),result
            read,_=http('/admin/api/settings',ctl)
            assert read.get('settings',{}).get('tool_output_verbose') is False,read
            return {'operator_settings_updated':True}
        check('human_settings_compatibility',human_settings)
        def specialized_metadata():
            result=full.call('secret_request',{'label':'protocol metadata fixture','ttl_seconds':60,'_meta':{'io.gptadmin/context':full.context}})
            assert result.get('status')=='pending' and result.get('secret_ref'),result
            assert 'value' not in result
            return {'metadata_only_context_accepted':True,'strict_arguments_preserved':True}
        check('specialized_facade_context_metadata',specialized_metadata)

        def fleet_entry():
            other_context=MCP(issued['access_token'])
            blocked=other_context.call('fleetExec',{'servers':[target],'cmd':'printf fleet-context-ok','timeout':3})
            assert blocked.get('executed') is False and blocked.get('error',{}).get('code')=='instructions_required',blocked
            approved_context=full.call('fleetExec',{'servers':[target],'cmd':'printf fleet-context-ok','timeout':3})
            assert 'instructions_required' not in json.dumps(approved_context),approved_context
            return {'fresh_context_refused':True,'valid_context_reaches_existing_policy':True}
        check('fleet_entry_context',fleet_entry)
        def secret_request_status():
            schema=full.call('schema',{'target':'hub','tool':'secret_request','detail':'full'})
            created=full.call('execute',{'ref':schema['call_ref'],'args':{'label':'instruction-context fixture','ttl_seconds':60},'detail':'full'})
            receipt=created.get('response',{})
            assert receipt.get('status')=='pending' and receipt.get('secret_ref') and receipt.get('input_url'),created
            polled=full.call('execute',{'target':'hub','tool':'secret_status','args':{'secret_ref':receipt['secret_ref']},'detail':'full'})
            assert polled.get('response',{}).get('status')=='pending',polled
            report['pending_secret_ref']=receipt['secret_ref']
            return {'request':'pending','status':'pending','no_value_supplied':True}
        check('secret_request_and_status',secret_request_status)
        def refresh():
            rules.write_text('Fixture profile instructions version two.')
            marker=root/'after-instruction-edit'
            blocked=full.call('execute',{'target':target,'tool':'shell_exec','args':{'cmd':'touch '+str(marker),'timeout':2}})
            assert blocked.get('executed') is False and not marker.exists() and 'version two' in blocked.get('instructions',''),blocked
            inspected=full.call('expand',{'id':report['inspection_ref']})
            assert inspected.get('data',{}).get('response',{}).get('result')=={'answer':42},inspected
            job=full.call('job',{'id':report['inspection_job'],'detail':'full'})
            assert 'old-path-ok' in json.dumps(job),job
            pending=full.call('execute',{'target':'hub','tool':'secret_status','args':{'secret_ref':report['pending_secret_ref']},'detail':'full'})
            assert pending.get('response',{}).get('status')=='pending',pending
            fresh=full.call('discover',{})
            assert 'version two' in fresh.get('instructions',''),fresh
            schema=full.call('schema',{'target':target,'tool':'shell_exec'})
            successful=full.call('execute',{'ref':schema['call_ref'],'args':{'cmd':'touch '+str(marker),'timeout':2},'detail':'full'})
            assert successful.get('status')=='completed' and marker.exists(),successful
            return {'old_context_refused':True,'job_expand_status_remain_readable':True,'new_context_executes':True}
        check('instruction_edit_refresh_and_existing_receipts',refresh)
        report['ok']=bool(report['scenarios']) and all(x['pass'] for x in report['scenarios'].values())
    except Exception as exc:
        report['fatal']=str(exc)[:2500]
    finally:
        if proc.poll() is None:
            os.killpg(proc.pid,signal.SIGTERM)
            try: proc.wait(timeout=8)
            except subprocess.TimeoutExpired: os.killpg(proc.pid,signal.SIGKILL);proc.wait(timeout=3)
        log.close()
        report['candidate_stopped']=proc.poll() is not None
        (HERE/'lead-report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2))
        print(json.dumps({'ok':report['ok'],'fatal':report.get('fatal'),'report':str(HERE/'lead-report.json')},ensure_ascii=False),flush=True)
    return 0 if report['ok'] else 1

if __name__=='__main__': raise SystemExit(main())
