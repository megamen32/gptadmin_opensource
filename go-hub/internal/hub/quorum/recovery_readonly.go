package quorum

// This reader establishes a Raft log prefix. It does not establish that an
// application SQLite checkpoint was applied, and never starts Raft or replays
// a mutation. An operator must separately establish distinct voter lineage;
// copies of one voter's database are not independent quorum evidence.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
	"go.etcd.io/bbolt"
)

type TerminalOriginEvidence struct {
	Schema string `json:"schema"`
	ObservedAt time.Time `json:"observed_at"`
	ClusterID string `json:"cluster_id"`
	NodeID string `json:"node_id"`
	StorePath string `json:"store_path"`
	StoreSize int64 `json:"store_size"`
	StoreModified time.Time `json:"store_modified"`
	CurrentTerm uint64 `json:"current_term"`
	LastVoteTerm uint64 `json:"last_vote_term"`
	FirstIndex uint64 `json:"first_index"`
	LastIndex uint64 `json:"last_index"`
	ConfigurationIndex uint64 `json:"configuration_index"`
	ConfigurationTerm uint64 `json:"configuration_term"`
	ConfigurationSHA256 string `json:"configuration_sha256"`
	Voters []string `json:"voters"`
	BoundaryIndex uint64 `json:"boundary_index"`
	BoundaryTerm uint64 `json:"boundary_term"`
	BoundaryPrefixSHA256 string `json:"boundary_prefix_sha256"`
	FullPrefixSHA256 string `json:"full_prefix_sha256"`
	JobID string `json:"job_id"`
	JobIndex uint64 `json:"job_index"`
	JobTerm uint64 `json:"job_term"`
	JobExternalSequence uint64 `json:"job_external_sequence"`
	JobRevision uint64 `json:"job_revision"`
	CommandSHA256 string `json:"command_sha256"`
	RecordSHA256 string `json:"record_sha256"`
	// Opaque bytes preserve the authenticated representation across evidence
	// and plan JSON serialization, including MarshalIndent. Reformatting this
	// value as json.RawMessage would invalidate its original byte digest.
	Record []byte `json:"record"`
}

func originDigest(b []byte) string { v:=sha256.Sum256(b);return hex.EncodeToString(v[:]) }

// InspectTerminalOrigin takes a shared, read-only Bolt lock on the configured
// voter store. All external records are authenticated with the existing state
// cipher; its key and unrelated record contents are never returned.
func InspectTerminalOrigin(configFile, jobID string) (TerminalOriginEvidence,error) {
	var out TerminalOriginEvidence
	if jobID=="" || len(jobID)>128 || strings.ContainsAny(jobID,"/\\ \t\r\n") { return out,errors.New("invalid exact job ID") }
	raw,err:=os.ReadFile(configFile);if err!=nil{return out,err}
	var cfg Config;if err=json.Unmarshal(raw,&cfg);err!=nil{return out,err}
	if cfg.ClusterID==""||cfg.NodeID==""||cfg.DataDir==""||cfg.StateKeyFile==""{return out,errors.New("incomplete voter configuration")}
	path,err:=filepath.Abs(filepath.Join(cfg.DataDir,"raft.db"));if err!=nil{return out,err}
	before,err:=os.Lstat(path);if err!=nil{return out,err};if !before.Mode().IsRegular(){return out,errors.New("Raft store is not a regular file")}
	aead,err:=loadStateAEAD(cfg.StateKeyFile);if err!=nil{return out,errors.New("existing voter state cipher unavailable")}
	store,err:=raftboltdb.New(raftboltdb.Options{Path:path,BoltOptions:&bbolt.Options{ReadOnly:true,Timeout:2*time.Second}});if err!=nil{return out,err};defer store.Close()
	out.CurrentTerm,err=store.GetUint64([]byte("CurrentTerm"));if err!=nil||out.CurrentTerm==0{return out,errors.New("durable current term unavailable")}
	out.LastVoteTerm,_=store.GetUint64([]byte("LastVoteTerm"))
	out.FirstIndex,err=store.FirstIndex();if err!=nil{return out,err};out.LastIndex,err=store.LastIndex();if err!=nil{return out,err}
	if out.FirstIndex!=1||out.LastIndex==0||out.LastIndex>100000{return out,errors.New("complete bounded Raft prefix required")}
	prefix:=sha256.New();var externalSequence uint64;var otherReference uint64
	for index:=out.FirstIndex;index<=out.LastIndex;index++{
		var entry raft.Log;if err=store.GetLog(index,&entry);err!=nil{return out,err}
		if entry.Index!=index||entry.Term==0||entry.Term>out.CurrentTerm||len(entry.Data)>16<<20||len(entry.Extensions)>1<<20{return out,errors.New("invalid bounded Raft entry")}
		if entry.Type==raft.LogAddPeerDeprecated||entry.Type==raft.LogRemovePeerDeprecated||entry.Type>raft.LogConfiguration{return out,errors.New("unsupported historical membership entry")}
		// Unambiguous framing includes Extensions. AppendedAt is local storage
		// metadata and is deliberately not part of the replicated log identity.
		var header [33]byte;binary.BigEndian.PutUint64(header[0:8],entry.Index);binary.BigEndian.PutUint64(header[8:16],entry.Term);header[16]=byte(entry.Type);binary.BigEndian.PutUint64(header[17:25],uint64(len(entry.Data)));binary.BigEndian.PutUint64(header[25:33],uint64(len(entry.Extensions)))
		prefix.Write(header[:]);prefix.Write(entry.Data);prefix.Write(entry.Extensions)
		if entry.Type==raft.LogConfiguration {
			configuration:=raft.DecodeConfiguration(entry.Data);voters:=[]string{};seen:=map[string]bool{}
			for _,server:=range configuration.Servers{if seen[string(server.ID)]||server.ID==""{return out,errors.New("ambiguous membership")};seen[string(server.ID)]=true;if server.Suffrage==raft.Voter{voters=append(voters,string(server.ID))}}
			sort.Strings(voters);out.Voters=voters;out.ConfigurationIndex=index;out.ConfigurationTerm=entry.Term;out.ConfigurationSHA256=originDigest(entry.Data)
		}
		if entry.Type==raft.LogNoop&&entry.Term==out.CurrentTerm&&out.BoundaryIndex==0{out.BoundaryIndex=index;out.BoundaryTerm=entry.Term;out.BoundaryPrefixSHA256=hex.EncodeToString(prefix.Sum(nil))}
		if entry.Type!=raft.LogCommand{continue}
		var cmd command;if err=json.Unmarshal(entry.Data,&cmd);err!=nil||cmd.ClusterID!=cfg.ClusterID||len(cmd.Mutations)==0{return out,errors.New("invalid cluster command")}
		hasExternal:=false
		for _,mutation:=range cmd.Mutations{
			if !mutation.External{continue};hasExternal=true
			opened,e:=openMutation(aead,cfg.ClusterID,mutation);if e!=nil{return out,errors.New("external mutation authentication failed")}
			if mutation.Key==jobID&&strings.HasPrefix(mutation.Namespace,"task_records/"){
				if mutation.Namespace!="task_records/shell"||mutation.Record.Deleted{return out,errors.New("exact ID was deleted or changed task kind")}
				if mutation.Record.Revision<=out.JobRevision{return out,errors.New("job revision did not advance")}
				out.JobIndex=index;out.JobTerm=entry.Term;out.JobExternalSequence=cmd.ExternalSequence;out.JobRevision=mutation.Record.Revision;out.CommandSHA256=originDigest(entry.Data);out.Record=append(json.RawMessage(nil),opened.Record.Value...);out.RecordSHA256=originDigest(out.Record)
			}else if mutation.Key==jobID||bytes.Contains(opened.Record.Value,[]byte(jobID)){
				if mutation.Namespace=="task_controls"||mutation.Namespace=="task_requests"{return out,errors.New("task has a control or idempotency record requiring separate recovery")};otherReference=index
			}
		}
		if hasExternal{if cmd.ExternalSequence!=externalSequence+1{return out,errors.New("nonsequential external history")};externalSequence=cmd.ExternalSequence}
	}
	if out.JobIndex==0||out.BoundaryIndex==0||out.JobIndex>=out.BoundaryIndex||out.ConfigurationIndex>=out.JobIndex||otherReference>out.JobIndex{return out,errors.New("exact task lacks an unchanged current-term prefix boundary")}
	if len(out.Voters)!=3||!containsOriginVoter(out.Voters,cfg.NodeID){return out,errors.New("this command requires the reviewed three-voter membership")}
	after,err:=os.Lstat(path);if err!=nil{return out,err};if !os.SameFile(before,after)||before.Size()!=after.Size()||!before.ModTime().Equal(after.ModTime()){return out,errors.New("voter store changed during inspection")}
	out.Schema="gptadmin.task-result-origin/v1";out.ObservedAt=time.Now().UTC();out.ClusterID=cfg.ClusterID;out.NodeID=cfg.NodeID;out.StorePath=path;out.StoreSize=after.Size();out.StoreModified=after.ModTime();out.JobID=jobID;out.FullPrefixSHA256=hex.EncodeToString(prefix.Sum(nil))
	return out,nil
}

func containsOriginVoter(voters []string,id string)bool{for _,v:=range voters{if v==id{return true}};return false}

// VerifyTerminalOriginPair proves only an irrevocable Raft prefix under the
// separately reviewed independent-voter lineage. It makes no claim about the
// application chain, a successful Barrier future, or applied SQLite state.
func VerifyTerminalOriginPair(a,b TerminalOriginEvidence) error {
	if a.Schema!="gptadmin.task-result-origin/v1"||b.Schema!=a.Schema||a.NodeID==b.NodeID||a.NodeID==""||b.NodeID==""{return errors.New("two distinct origin voters required")}
	if len(a.Voters)!=3||len(b.Voters)!=3||!containsOriginVoter(a.Voters,a.NodeID)||!containsOriginVoter(a.Voters,b.NodeID){return errors.New("not a majority of the same three-voter membership")}
	x,_:=json.Marshal(a.Voters);y,_:=json.Marshal(b.Voters);if !bytes.Equal(x,y){return errors.New("voter membership differs")}
	if a.ClusterID==""||a.ClusterID!=b.ClusterID||a.CurrentTerm==0||a.CurrentTerm!=b.CurrentTerm||a.BoundaryTerm!=a.CurrentTerm||b.BoundaryTerm!=b.CurrentTerm||a.BoundaryIndex==0||a.BoundaryIndex!=b.BoundaryIndex||a.BoundaryPrefixSHA256!=b.BoundaryPrefixSHA256||len(a.BoundaryPrefixSHA256)!=64{return errors.New("current-term majority prefix differs")}
	if a.ConfigurationIndex!=b.ConfigurationIndex||a.ConfigurationTerm!=b.ConfigurationTerm||a.ConfigurationSHA256!=b.ConfigurationSHA256||a.ConfigurationIndex>=a.JobIndex||b.ConfigurationIndex>=b.JobIndex{return errors.New("membership changed around the exact task")}
	if a.FirstIndex!=1||b.FirstIndex!=1||a.LastIndex!=b.LastIndex||a.LastIndex<a.BoundaryIndex||a.FullPrefixSHA256!=b.FullPrefixSHA256||len(a.FullPrefixSHA256)!=64{return errors.New("full retained log prefixes differ")}
	if a.JobID==""||a.JobID!=b.JobID||a.JobIndex!=b.JobIndex||a.JobTerm!=b.JobTerm||a.JobExternalSequence!=b.JobExternalSequence||a.JobRevision!=b.JobRevision||a.JobIndex>=a.BoundaryIndex||a.RecordSHA256!=b.RecordSHA256||a.CommandSHA256!=b.CommandSHA256||!bytes.Equal(a.Record,b.Record)||originDigest(a.Record)!=a.RecordSHA256{return errors.New("authenticated exact task records differ")}
	for _,e:=range []TerminalOriginEvidence{a,b}{if e.ObservedAt.IsZero()||time.Since(e.ObservedAt)>20*time.Minute||time.Until(e.ObservedAt)>time.Minute{return fmt.Errorf("origin evidence for %s is not fresh",e.NodeID)}}
	return nil
}
