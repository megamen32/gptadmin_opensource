import json,os,sqlite3,tempfile,unittest
from pathlib import Path
import gptadmin_offline_handoff as h
class T(unittest.TestCase):
 def bundle(self,root,node,rows):
  root.mkdir(); domains={}; dig={}
  for n in h.DOMAINS:
   p=root/(n+'.json'); p.write_text(json.dumps({'domain':n,'value':node})); os.chmod(p,0o600); domains[n]=p.name; dig[n]=h.sha(p)
  (root/'node-identity.json').write_text(json.dumps({'node_id':node,'executor_host':node})); os.chmod(root/'node-identity.json',0o600)
  m={'schema':'gptadmin.offline-handoff/v1','fenced':True,'source_node_id':node,'source_executor_host':node,'domains':domains,'domain_sha256':dig}; (root/'manifest.json').write_text(json.dumps(m)); os.chmod(root/'manifest.json',0o600)
  db=sqlite3.connect(root/'tasks.sqlite'); db.executescript('create table task_records(kind text,key text,payload text); create table task_controls(task_id text,payload text); create table task_requests(key text,payload text); create table task_quorum_revisions(namespace text,row_key text,payload text); create table task_quorum_authority(namespace text,row_key text,payload text); create table task_store_meta(id integer,version integer); create table task_status_counts(kind text,status text,server text,n integer); create table task_quorum_meta(id integer,cluster_id text,applied_index integer); create table task_quorum_chain(id integer,sequence integer,digest blob);')
  for k,v in rows.items(): db.execute('insert into task_records values(?,?,?)',('shell',k,v))
  db.commit(); db.close(); os.chmod(root/"tasks.sqlite",0o600)
 def test_union_and_conflict_fail_closed(self):
  with tempfile.TemporaryDirectory() as td:
   a=Path(td)/'a'; b=Path(td)/'b'; out=Path(td)/'out'; self.bundle(a,'44',{'a':'one'}); self.bundle(b,'88',{'b':'two'}); r=h.merge(a/'tasks.sqlite',b/'tasks.sqlite',out/'tasks.sqlite'); self.assertEqual(r['task_records'][1],1)
   c=Path(td)/'c'; self.bundle(c,'88',{'a':'different'}); self.assertRaises(ValueError,h.merge,a/'tasks.sqlite',c/'tasks.sqlite',Path(td)/'bad'/'tasks.sqlite')
if __name__=='__main__': unittest.main()