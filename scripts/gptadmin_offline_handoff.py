#!/usr/bin/env python3
import argparse,hashlib,json,os,shutil,sqlite3,stat
from pathlib import Path
DOMAINS=('auth','membership','registry','virtual_mcp','instruction_sets_named','instruction_sets_default','secret_ingress','webhooks','webhook_routes','security','webauthn')
KEYS={'task_records':('kind','key'),'task_controls':('task_id',),'task_requests':('key'),'task_quorum_revisions':('namespace','row_key'),'task_quorum_authority':('namespace','row_key')}
ALLOWED=set(KEYS)|{'task_store_meta','task_status_counts','task_quorum_meta','task_quorum_chain'}
def reg(p):
 p=Path(p); s=p.lstat()
 if not stat.S_ISREG(s.st_mode) or s.st_uid!=os.geteuid() or s.st_mode&0o077: raise ValueError('owner-only regular file required: '+str(p))
 return p
def sha(p):
 h=hashlib.sha256();
 with open(p,'rb') as f:
  for b in iter(lambda:f.read(1<<20),b''): h.update(b)
 return h.hexdigest()
def manifest(root):
 r=Path(root); m=json.loads(reg(r/'manifest.json').read_text())
 if m.get('schema')!='gptadmin.offline-handoff/v1' or not m.get('fenced'): raise ValueError('bundle must be fenced v1')
 if set(m.get('domains',{}))!=set(DOMAINS): raise ValueError('all 11 domains required')
 for n,p in m['domains'].items():
  if sha(reg(r/p))!=m['domain_sha256'][n]: raise ValueError('domain digest mismatch '+n)
 ident=json.loads(reg(r/'node-identity.json').read_text())
 if ident.get('node_id')!=m.get('source_node_id') or ident.get('executor_host')!=m.get('source_executor_host'): raise ValueError('physical identity mismatch')
 return m
def merge(a,b,out):
 out=Path(out); out.parent.mkdir(mode=0o700,parents=True,exist_ok=True)
 if out.exists(): raise ValueError('output exists')
 x=sqlite3.connect('file:'+str(reg(a))+'?mode=ro',uri=True); d=sqlite3.connect(out); x.backup(d); x.close(); d.execute('attach database ? as other',('file:'+str(reg(b))+'?mode=ro',))
 names={r[0] for r in d.execute("select name from main.sqlite_master where type='table' and name not like 'sqlite_%'")}; other={r[0] for r in d.execute("select name from other.sqlite_master where type='table' and name not like 'sqlite_%'")}
 if names!=other or not names<=ALLOWED: raise ValueError('modern task schema mismatch')
 report={}
 with d:
  for t,keys in KEYS.items():
   cols=[r[1] for r in d.execute('pragma table_info('+t+')')]
   sel=','.join('\"'+k+'\"' for k in keys)
   ma={tuple(row) for row in d.execute('select '+sel+' from main.'+t)}
   mb={tuple(row) for row in d.execute('select '+sel+' from other.'+t)}
   for key in ma & mb:
    ra=d.execute('select * from main.'+t+' where '+" and ".join('"'+k+'"=?' for k in keys),key).fetchone(); rb=d.execute('select * from other.'+t+' where '+" and ".join('"'+k+'"=?' for k in keys),key).fetchone()
    if ra!=rb: raise ValueError('conflict '+t)
   before=d.execute('select count(*) from main.'+t).fetchone()[0]; d.execute('insert or ignore into main.'+t+' select * from other.'+t); added=d.execute('select changes()').fetchone()[0]; report[t]=(before,added)
  for t in ('task_store_meta','task_status_counts','task_quorum_meta','task_quorum_chain'):
   if t in names and d.execute('select * from main.'+t).fetchall()!=d.execute('select * from other.'+t).fetchall(): raise ValueError('metadata conflict '+t)
 if d.execute('pragma quick_check').fetchone()[0]!='ok': raise ValueError('integrity failure')
 d.close(); return report
def main():
 ap=argparse.ArgumentParser(); ap.add_argument('--source',required=True); ap.add_argument('--other',required=True); ap.add_argument('--output',required=True); ap.add_argument('--routing-only',action='store_true'); a=ap.parse_args(); m=manifest(a.source); out=Path(a.output); out.mkdir(mode=0o700,parents=True,exist_ok=True);
 if a.routing_only:
  ident=json.loads(reg(Path(a.other)/'node-identity.json').read_text());
  if ident.get('node_id')==m.get('source_node_id'): raise ValueError('target physical identity must differ')
  result={'schema':'gptadmin.offline-routing/v1','writer_id':ident.get('node_id'),'source_writer_id':m.get('source_node_id'),'domains':list(DOMAINS),'task_ledgers':'node_owned_preserved','controller_handles':{'tasks_get':'local_owner','mcp_content':'local_exec_content_store','op_handles':'runtime_local_ttl_no_replay'}}; (out/'routing-result.json').write_text(json.dumps(result,sort_keys=True)+'\n'); os.chmod(out/'routing-result.json',0o600); print(json.dumps(result,sort_keys=True)); return
 report=merge(Path(a.source)/'tasks.sqlite',Path(a.other)/'tasks.sqlite',out/'tasks.sqlite'); print(json.dumps({'domains':list(DOMAINS),'source_node_id':m['source_node_id'],'task_report':report,'task_sha256':sha(out/'tasks.sqlite')},sort_keys=True))
if __name__=='__main__': main()