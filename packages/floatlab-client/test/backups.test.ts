import { expect, test } from "bun:test";
import { backupRestoreDatasets, createApiClient, CommandEngine, type BackupApi, type FloatLabApi, type CommandRuntime } from "../src";
test("backup API encodes destinations, policies and idempotency", async () => {
 const requests: {url:string;init:RequestInit}[]=[];
 const api=createApiClient({baseUrl:"http://host",fetch:(async(input,init)=>{requests.push({url:String(input),init:init??{}});return new Response("{}")}) as typeof fetch});
 await api.putBackupTarget("app/a",{dataset:"floatlab/app/data",node_id:"node/b",schedule:"h/24 d/28"},"key");
 expect(requests[0]!.url).toBe("http://host/api/v1/storage/replication/app%2Fa/targets");
 expect(JSON.parse(requests[0]!.init.body as string).schedule).toBe("h/24 d/28");
 expect(new Headers(requests[0]!.init.headers).get("idempotency-key")).toBe("key");
 await api.triggerReplication("app","node/b","floatlab/app/data","run");
 expect(requests[1]!.url).toContain("dest_node_id=node%2Fb&dataset=floatlab%2Fapp%2Fdata");
 await api.removeBackupTarget("app",{dataset:"floatlab/app/data",node_id:"node/b"},"remove");
 expect(requests[2]!.init.method).toBe("DELETE");
});
test("backup commands resolve stack and node names and preserve quoted policy", async () => {
 const calls:unknown[]=[];
 const api={listStacks:async()=>[{id:"app",name:"demo"}],listNodes:async()=>[{id:"dest",name:"Beta"}],listBackupTargets:async()=>[],listReplication:async()=>[],putBackupTarget:async(...args:unknown[])=>{calls.push(args);return{}},removeBackupTarget:async()=>({}),triggerReplication:async()=>({})} as unknown as FloatLabApi & BackupApi;
 const runtime:CommandRuntime={interactive:false,confirm:async()=>true,editCompose:async()=>undefined,present:async()=>undefined};
 const engine=new CommandEngine(api,runtime);
 expect((await engine.dispatch('backup set demo floatlab/demo/data Beta "h/24 d/28"')).code).toBe(0);
 expect((calls[0] as unknown[]).slice(0,2)).toEqual(["app",{dataset:"floatlab/demo/data",node_id:"dest",schedule:"h/24 d/28"}]);
 expect((await engine.dispatch("backup list demo")).code).toBe(0);
 expect((await engine.dispatch("backup run demo")).code).toBe(2);
});
test("snapshot protection displays current node names", async () => {
 const api={listStacks:async()=>[{id:"app",name:"demo"}],listNodes:async()=>[{id:"dest",name:"Beta"}],listSnapshots:async()=>[{name:"fl-point",hold_targets:["dest"],transfer_targets:["missing"]}]} as unknown as FloatLabApi;
 const runtime:CommandRuntime={interactive:false,confirm:async()=>true,editCompose:async()=>undefined,present:async()=>undefined};
 const result=(await new CommandEngine(api,runtime).dispatch("snapshot list demo")).result;
 expect(result.kind).toBe("table");if(result.kind==="table"){expect(result.columns.slice(-2)).toEqual(["held for","transferring to"]);expect(result.rows[0]!.slice(-2)).toEqual(["Beta","missing"])}
});

test("backup restore API preserves complete requests and reads cached Compose", async () => {
 const requests: {url:string;init:RequestInit}[]=[];
 const api=createApiClient({baseUrl:"http://host",token:"token",fetch:(async(input,init)=>{requests.push({url:String(input),init:init??{}});return new Response(String(input).includes("/config?") ? "name: demo\n" : "{}")}) as typeof fetch});
 await api.listBackups({node_id:"backup/a",source_node_id:"source/b",dataset:"floatlab/demo/data"});
 expect(requests[0]!.url).toBe("http://host/api/v1/storage/backups?node_id=backup%2Fa&source_node_id=source%2Fb&dataset=floatlab%2Fdemo%2Fdata");
 expect(await api.getBackupConfig("app/a")).toBe("name: demo\n");
 expect(requests[1]!.url).toContain("/storage/backups/config?stack_id=app%2Fa");
 expect(new Headers(requests[1]!.init.headers).get("authorization")).toBe("Bearer token");
 const request={name:"restored",source_node_id:"source",backup_node_id:"backup",destination_node_id:"destination",compose_file:"name: demo\n",snapshots:[{dataset:"floatlab/demo/data",snapshot:"fl-point"}]};
 await api.restoreBackup(request,"restore-key");
 expect(JSON.parse(requests[2]!.init.body as string)).toEqual(request);
 expect(new Headers(requests[2]!.init.headers).get("idempotency-key")).toBe("restore-key");
});

test("backup restore command loads cached config, validates all datasets and resolves nodes", async () => {
 const calls: unknown[]=[];
 const compose='name: demo\nservices:\n  app:\n    image: alpine\n    volumes: ["./data:/data", "cache:/cache"]\nvolumes:\n  cache: {}\n';
 const api={listStacks:async()=>[{id:"app",name:"demo",primary_node:"source"}],listNodes:async()=>[{id:"backup",name:"Beta"},{id:"dest",name:"Gamma"}],listBackupTargets:async()=>{throw new Error("original owner offline")},listBackups:async()=>[{name:"fl-point"}],getBackupConfig:async()=>compose,restoreBackup:async(...args: unknown[])=>{calls.push(args);return{operation_id:"restore",state:"pending"}}} as unknown as FloatLabApi & BackupApi & import("../src").BackupRestoreApi;
 const runtime:CommandRuntime={interactive:false,confirm:async()=>true,editCompose:async()=>undefined,present:async()=>undefined};
 const engine=new CommandEngine(api,runtime);
 expect((await engine.dispatch("backup restore demo restored Beta Gamma fl-point")).code).toBe(0);
 expect((calls[0] as unknown[])[0]).toEqual({name:"restored",source_node_id:"source",backup_node_id:"backup",destination_node_id:"dest",compose_file:compose,snapshots:[{dataset:"floatlab/demo/data",snapshot:"fl-point"},{dataset:"floatlab/demo/cache",snapshot:"fl-point"}]});
 api.listBackups=async()=>[];
 expect((await engine.dispatch("backup restore demo other Beta Gamma fl-missing")).code).toBe(2);
 expect(calls).toHaveLength(1);
});

test("backup restore command accepts file override and allows cancellation", async () => {
 const calls:unknown[]=[];
 const compose='name: demo\nservices:\n  app:\n    image: alpine\n    volumes: ["./data:/data"]\n';
 const api={listStacks:async()=>[{id:"app",name:"demo",primary_node:"source"}],listNodes:async()=>[{id:"backup",name:"Beta"},{id:"dest",name:"Gamma"},{id:"source",name:"Alpha"}],listBackupTargets:async()=>{throw new Error("original owner offline")},listBackups:async()=>[{name:"fl-point"}],getBackupConfig:async()=>{throw new Error("must use supplied file")},restoreBackup:async(request:unknown)=>{calls.push(request);return{}}} as unknown as FloatLabApi & BackupApi & import("../src").BackupRestoreApi;
 const runtime:CommandRuntime={interactive:true,confirm:async()=>false,readCompose:async()=>compose,editCompose:async()=>undefined,present:async()=>undefined};
 const engine=new CommandEngine(api,runtime);
 expect((await engine.dispatch("backup restore demo restored Beta Gamma fl-point --source Alpha --file original.yml")).code).toBe(0);
 expect(calls).toHaveLength(0);
 runtime.confirm=async()=>true;
 expect((await engine.dispatch("backup restore demo restored Beta Gamma fl-point --source Alpha --file original.yml")).code).toBe(0);
 expect((calls[0] as {compose_file:string}).compose_file).toBe(compose);
});

test("restore datasets cover all submitted writable mounts and skip readonly/tmpfs mounts", () => {
 const compose=`name: original
services:
  app:
    image: alpine
    volumes:
      - ./nested/data.v1:/data
      - cache:/cache
      - ./nested/data.v1:/shared
      - ./config:/config:ro,Z
      - {type: bind, source: ./another, target: /another, read_only: true}
      - {type: tmpfs, target: /tmp}
      - {type: volume, source: queue, target: /queue}
`;
 expect(backupRestoreDatasets(compose)).toEqual(["floatlab/original/data.v1","floatlab/original/cache","floatlab/original/queue"]);
 for(const source of ["services: {}", "name: original\nservices: {}", 'name: original\nservices:\n  app:\n    volumes: ["/anonymous"]']) {
  expect(()=>backupRestoreDatasets(source)).toThrow();
 }
});
