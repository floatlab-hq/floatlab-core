import { expect, test } from "bun:test";
import { CommandEngine, createApiClient, type Alert, type AlertRule, type AlertsApi, type CommandRuntime, type FloatLabApi } from "../src";

const incident = {id:"alert-one", rule_id:"rule-one", severity:"warning", state:"active", message:"Disk full", created_at:"2026-11-01T00:00:00Z"} as Alert;
const rule = {id:"rule-one", name:"Disk capacity", condition:"disk > 90", severity:"warning", enabled:true, evaluations:[{state:"firing"}]} as AlertRule;
function fixture(interactive = false) {
  const writes: unknown[][] = [];
  const api = {
    listAlerts: async (state?: string) => [incident].filter(alert => !state || alert.state === state),
    getAlert: async () => incident,
    silenceAlert: async (id: string, until: string, key: string) => {writes.push(["mute",id,until,key]);return {...incident,state:"silenced",silenced_until:until} as Alert;},
    listAlertRules: async () => [rule],
    getAlertRule: async () => rule,
    updateAlertRule: async (id: string, changes: object, key: string) => {writes.push(["rule",id,changes,key]);return {...rule,...changes};},
  } as FloatLabApi & AlertsApi;
  const runtime: CommandRuntime = {interactive,confirm:async()=>true,editCompose:async()=>undefined,present:async()=>undefined};
  return {api,runtime,writes,engine:new CommandEngine(api,runtime)};
}

test("alert transport preserves filters, escaping, changes, and idempotency",async()=>{
  const requests: {url:string; init:RequestInit}[]=[];
  const api=createApiClient({baseUrl:"http://host",token:"token",fetch:(async(input,init)=>{
    requests.push({url:String(input),init:init??{}});
    return Response.json({});
  }) as typeof fetch});
  await api.listAlerts("active");
  await api.getAlert("alert/a");
  await api.silenceAlert("alert/a","2026-11-02T00:00:00Z","mute-key");
  await api.listAlertRules();
  await api.getAlertRule("rule/a");
  await api.updateAlertRule("rule/a",{enabled:false},"rule-key");
  expect(requests.map(request=>request.url)).toEqual([
    "http://host/api/v1/alerts?state=active","http://host/api/v1/alerts/alert%2Fa","http://host/api/v1/alerts/alert%2Fa/silence",
    "http://host/api/v1/alert-rules","http://host/api/v1/alert-rules/rule%2Fa","http://host/api/v1/alert-rules/rule%2Fa",
  ]);
  expect(requests[2]!.init.method).toBe("POST");
  expect(JSON.parse(requests[2]!.init.body as string)).toEqual({until:"2026-11-02T00:00:00Z"});
  expect(new Headers(requests[2]!.init.headers).get("idempotency-key")).toBe("mute-key");
  expect(requests[5]!.init.method).toBe("PUT");
  expect(JSON.parse(requests[5]!.init.body as string)).toEqual({enabled:false});
  expect(new Headers(requests[5]!.init.headers).get("authorization")).toBe("Bearer token");
});

test("lists alerts and rules with incident identity and evaluation status",async()=>{
  const {engine,writes}=fixture();
  expect((await engine.dispatch("alerts")).result).toMatchObject({kind:"table",rows:[["alert-one","rule-one","active","warning","Disk full",""]]});
  expect((await engine.dispatch("alerts list resolved")).result).toMatchObject({rows:[]});
  expect((await engine.dispatch("alerts show alert-o")).result).toMatchObject({kind:"text",text:JSON.stringify(incident,null,2)});
  expect((await engine.dispatch("alert-rules")).result).toMatchObject({rows:[["rule-one","Disk capacity","true","warning","firing","disk > 90"]]});
  expect((await engine.dispatch('alert-rules show "Disk capacity"')).result).toMatchObject({kind:"text",text:JSON.stringify(rule,null,2)});
  expect(writes).toEqual([]);
});

test("mutes an incident for a bounded duration without disabling its rule",async()=>{
  for (const [duration,ms] of [["30m",1_800_000],["1h",3_600_000],["1d",86_400_000]] as const) {
    const {engine,writes}=fixture(); const before=Date.now();
    expect((await engine.dispatch(`alerts mute alert-o ${duration}`)).code).toBe(0);
    expect(writes).toHaveLength(1);
    expect(writes[0]![0]).toBe("mute"); expect(writes[0]![1]).toBe("alert-one");
    expect(Date.parse(writes[0]![2] as string)).toBeGreaterThanOrEqual(before+ms);
    expect(Date.parse(writes[0]![2] as string)).toBeLessThanOrEqual(Date.now()+ms);
    expect(writes[0]![3]).toEqual(expect.any(String));
  }
});

test("edits and enables or disables rules with partial updates",async()=>{
  const {engine,writes}=fixture();
  expect((await engine.dispatch("alert-rules disable rule-o")).code).toBe(0);
  expect((await engine.dispatch("alert-rules enable rule-one")).code).toBe(0);
  expect((await engine.dispatch(`alert-rules edit rule-one '{"condition":"disk > 95","for_duration":"5m"}'`)).code).toBe(0);
  expect(writes.map(write=>write.slice(0,3))).toEqual([["rule","rule-one",{enabled:false}],["rule","rule-one",{enabled:true}],["rule","rule-one",{condition:"disk > 95",for_duration:"5m"}]]);
});

test("invalid commands, resolved alerts, and cancelled changes never mutate",async()=>{
  const {engine,api,writes}=fixture();
  for(const command of ["alerts list bogus","alerts mute alert-one","alerts mute missing 1h","alerts mute alert-one 0h","alerts mute alert-one -1h","alerts mute alert-one 1s","alerts mute alert-one 999999999999999999999d","alert-rules edit rule-one nope","alert-rules edit rule-one null","alert-rules edit rule-one '[]'","alert-rules edit rule-one '{}'","alert-rules edit rule-one '{\"id\":\"other\"}'","alert-rules disable missing","alert-rules show rule-one extra","alerts --start"]) expect((await engine.dispatch(command)).code).not.toBe(0);
  api.listAlerts=async()=>[{...incident,state:"resolved"}];
  expect((await engine.dispatch("alerts mute alert-one 1h")).code).not.toBe(0);
  expect(writes).toEqual([]);
  const cancelled=fixture(true); cancelled.runtime.confirm=async()=>false;
  expect((await cancelled.engine.dispatch("alerts mute alert-one 1h")).result).toMatchObject({text:"Mute cancelled."});
  expect((await cancelled.engine.dispatch("alert-rules disable rule-one")).result).toMatchObject({text:"Rule change cancelled."});
  expect(cancelled.writes).toEqual([]);
});

test("completes alert actions, states, incident IDs, rules, and mute durations",async()=>{
  const {engine}=fixture();
  expect((await engine.complete("alerts ")).map(choice=>choice.value)).toEqual(["list","show","mute"]);
  expect((await engine.complete("alert-rules ")).map(choice=>choice.value)).toEqual(["list","show","enable","disable","edit"]);
  expect(await engine.complete("alerts list si")).toEqual([{value:"silenced"}]);
  expect((await engine.complete("alerts mute alert-")).map(choice=>choice.value)).toEqual(["alert-one"]);
  expect((await engine.complete("alert-rules edit rule-")).map(choice=>choice.value)).toEqual(["rule-one"]);
  expect(await engine.complete("alerts mute alert-one 1")).toEqual([{value:"1h"},{value:"1d"}]);
});
