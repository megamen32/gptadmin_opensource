package quorum

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

// These deliberately synthetic stores test the decoder and proof rules. They
// are not independent-voter lineage evidence for any production operation.
func terminalOriginFixture(t *testing.T)([]*raft.Log,string){
	t.Helper();key:=filepath.Join(t.TempDir(),"state.key");if err:=os.WriteFile(key,bytes.Repeat([]byte{7},32),0600);err!=nil{t.Fatal(err)}
	aead,err:=loadStateAEAD(key);if err!=nil{t.Fatal(err)}
	cfg:=raft.Configuration{Servers:[]raft.Server{{ID:"node-a",Address:"127.0.0.1:1",Suffrage:raft.Voter},{ID:"node-b",Address:"127.0.0.1:2",Suffrage:raft.Voter},{ID:"node-c",Address:"127.0.0.1:3",Suffrage:raft.Voter}}}
	logs:=[]*raft.Log{{Index:1,Term:1,Type:raft.LogConfiguration,Data:raft.EncodeConfiguration(cfg)}}
	for seq:=uint64(1);seq<=2;seq++{
		mutation,err:=sealMutation(aead,"terminal-fixture",Mutation{Namespace:"task_records/shell",Key:"fixture-job",External:true,Record:Record{Revision:seq,Value:json.RawMessage(`{"fixture":"private running record"}`)}});if err!=nil{t.Fatal(err)}
		data,_:=json.Marshal(command{ClusterID:"terminal-fixture",ExternalSequence:seq,Mutations:[]Mutation{mutation}})
		logs=append(logs,&raft.Log{Index:seq+1,Term:3,Type:raft.LogCommand,Data:data})
	}
	logs=append(logs,&raft.Log{Index:4,Term:5,Type:raft.LogNoop},&raft.Log{Index:5,Term:5,Type:raft.LogBarrier})
	return logs,key
}

func terminalOriginStore(t *testing.T,node,key string,logs []*raft.Log,currentTerm uint64)string{
	t.Helper();dir:=t.TempDir();store,err:=raftboltdb.NewBoltStore(filepath.Join(dir,"raft.db"));if err!=nil{t.Fatal(err)}
	if err=store.StoreLogs(logs);err!=nil{t.Fatal(err)};if err=store.SetUint64([]byte("CurrentTerm"),currentTerm);err!=nil{t.Fatal(err)};if err=store.Close();err!=nil{t.Fatal(err)}
	config:=Config{ClusterID:"terminal-fixture",NodeID:node,DataDir:dir,StateKeyFile:key};raw,_:=json.Marshal(config);path:=filepath.Join(dir,"quorum.json");if err=os.WriteFile(path,raw,0600);err!=nil{t.Fatal(err)};return path
}

func TestTerminalOriginMajorityPrefix(t *testing.T){
	logs,key:=terminalOriginFixture(t);aPath:=terminalOriginStore(t,"node-a",key,logs,5);bPath:=terminalOriginStore(t,"node-b",key,logs,5)
	a,err:=InspectTerminalOrigin(aPath,"fixture-job");if err!=nil{t.Fatal(err)};b,err:=InspectTerminalOrigin(bPath,"fixture-job");if err!=nil{t.Fatal(err)}
	if a.BoundaryIndex!=4||a.JobIndex!=3||a.JobRevision!=2{t.Fatalf("wrong prefix boundary: %+v",a)};if err=VerifyTerminalOriginPair(a,b);err!=nil{t.Fatal(err)}
	for name,mutate:=range map[string]func(*TerminalOriginEvidence){
		"same-voter":func(v *TerminalOriginEvidence){v.NodeID=a.NodeID},
		"wrong-membership":func(v *TerminalOriginEvidence){v.ConfigurationSHA256="different"},
		"wrong-full-prefix":func(v *TerminalOriginEvidence){v.FullPrefixSHA256="different"},
		"wrong-boundary":func(v *TerminalOriginEvidence){v.BoundaryIndex++},
		"wrong-term":func(v *TerminalOriginEvidence){v.CurrentTerm++},
		"changed-record":func(v *TerminalOriginEvidence){v.Record=append(json.RawMessage(nil),[]byte(`{"other":true}`)...)},
		"stale-evidence":func(v *TerminalOriginEvidence){v.ObservedAt=time.Now().Add(-time.Hour)},
	}{t.Run(name,func(t *testing.T){other:=b;mutate(&other);if err:=VerifyTerminalOriginPair(a,other);err==nil{t.Fatal("invalid majority evidence accepted")}})}
}

func TestTerminalOriginFullPrefixIncludesExtensions(t *testing.T){
	logs,key:=terminalOriginFixture(t);aPath:=terminalOriginStore(t,"node-a",key,logs,5);logs[1].Extensions=[]byte("different consensus extension");bPath:=terminalOriginStore(t,"node-b",key,logs,5)
	a,err:=InspectTerminalOrigin(aPath,"fixture-job");if err!=nil{t.Fatal(err)};b,err:=InspectTerminalOrigin(bPath,"fixture-job");if err!=nil{t.Fatal(err)};if err=VerifyTerminalOriginPair(a,b);err==nil{t.Fatal("different Extensions accepted as the same log prefix")}
}

func TestTerminalOriginEvidenceSerializationPreservesAuthenticatedBytes(t *testing.T){
	logs,key:=terminalOriginFixture(t)
	a,err:=InspectTerminalOrigin(terminalOriginStore(t,"node-a",key,logs,5),"fixture-job");if err!=nil{t.Fatal(err)}
	b,err:=InspectTerminalOrigin(terminalOriginStore(t,"node-b",key,logs,5),"fixture-job");if err!=nil{t.Fatal(err)}
	original:=append([]byte(nil),a.Record...)
	encoded,err:=json.MarshalIndent(a,"","  ");if err!=nil{t.Fatal(err)}
	var exported TerminalOriginEvidence;if err=json.Unmarshal(encoded,&exported);err!=nil{t.Fatal(err)}
	if !bytes.Equal(exported.Record,original)||originDigest(exported.Record)!=a.RecordSHA256{t.Fatal("evidence serialization reformatted authenticated bytes")}
	if err=VerifyTerminalOriginPair(exported,b);err!=nil{t.Fatalf("exported evidence rejected: %v",err)}
	// The prepared plan embeds both evidence objects and is independently
	// pretty-printed. Both export and plan round trips must be lossless.
	type envelope struct{Source TerminalOriginEvidence `json:"source_evidence"`;Local TerminalOriginEvidence `json:"local_evidence"`}
	plan:=envelope{Source:exported,Local:b}
	for attempt:=0;attempt<2;attempt++{
		encoded,err=json.MarshalIndent(plan,"","    ");if err!=nil{t.Fatal(err)}
		var decoded envelope;if err=json.Unmarshal(encoded,&decoded);err!=nil{t.Fatal(err)}
		if !bytes.Equal(decoded.Source.Record,original)||!bytes.Equal(decoded.Local.Record,original){t.Fatal("nested plan serialization reformatted authenticated bytes")}
		if err=VerifyTerminalOriginPair(decoded.Source,decoded.Local);err!=nil{t.Fatalf("plan evidence rejected: %v",err)}
		plan=decoded
	}
}

func TestTerminalOriginRejectsUnsupportedOrUncommittedHistory(t *testing.T){
	for _,name:=range []string{"deprecated-membership","no-current-term-entry","deleted-job","later-membership"}{t.Run(name,func(t *testing.T){
		logs,key:=terminalOriginFixture(t);term:=uint64(5)
		switch name{
		case "deprecated-membership":logs[0].Type=raft.LogAddPeerDeprecated
		case "no-current-term-entry":term=6
		case "deleted-job":data,_:=json.Marshal(command{ClusterID:"terminal-fixture",ExternalSequence:3,Mutations:[]Mutation{{Namespace:"task_records/shell",Key:"fixture-job",External:true,Record:Record{Revision:3,Deleted:true}}}});logs=append(logs,&raft.Log{Index:6,Term:5,Type:raft.LogCommand,Data:data})
		case "later-membership":logs=append(logs,&raft.Log{Index:6,Term:5,Type:raft.LogConfiguration,Data:logs[0].Data})
		}
		p:=terminalOriginStore(t,"node-a",key,logs,term);if _,err:=InspectTerminalOrigin(p,"fixture-job");err==nil{t.Fatal("unsupported or uncommitted origin accepted")}
	})}
}
