import {test,expect} from "@playwright/test";
import {createServer} from "node:http";
import {once} from "node:events";
import {GATEWAY,loginAdmin,sessionBearer,stableGoto} from "../../helpers";
import { translate } from "../../../src/i18n/translate";

// 双语真实链路：管理员创建隔离模型，HTTP 上游返回首帧/中途错误或截断正文，客户端和日志抽屉保留同一诊断。
// 验证 Chat、Responses 转换和原生灰度的报错与恢复；finally 删除模型、清除配置并关闭上游，schema 由运行器清理。
export function registerUpstreamStreamErrors(locale: "en" | "zh-CN") {
test("upstream stream error details "+locale,async({page})=>{
 test.setTimeout(120000);
 let partial=false,fail=true,truncated=false,truncated4xx=false,truncated4xxEmpty=false,calls=0;
 const marker="Vendor capacity exhausted: 保留原文";
 const nativeMarker = "Native capacity exhausted";
 const interruptedMessage = "The upstream response ended before it was complete.";
 let nativeTruncated = false;
 // 本地HTTP供应商夹具：接收真实请求，返回协议流或长度不足的完整 JSON，验证流错误及非流式读取错误；资源在finally回收。
 const upstream=createServer((req,res)=>{
  calls++;
  req.resume();
  if (req.url === "/v1/responses") {
   if (nativeTruncated) {
    const body = JSON.stringify({ id: "resp_native_partial", status: "completed", output: [] });
    res.writeHead(200, { "Content-Type": "application/json", "Content-Length": String(Buffer.byteLength(body) + 100), "Connection": "close" });
    res.end(body);
   } else {
    const event = { type: "response.failed", request_id: "req-native-sse", response: {
     id: "resp_native_failed", error: { type: "api_error", code: "native_capacity", message: nativeMarker },
    } };
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    res.end("data: " + JSON.stringify(event) + "\n\n");
   }
   return;
  }
  if(truncated4xxEmpty){res.writeHead(400,{"Content-Type":"application/json","Content-Length":"100","Connection":"close"});res.end();return;}
  if(truncated4xx){const full=JSON.stringify({error:{code:"context_length_exceeded",message:"Input is too long"}});res.writeHead(400,{"Content-Type":"application/json","Content-Length":String(Buffer.byteLength(full)+100),"Connection":"close"});res.end(full);return;}
  if(truncated){const full=JSON.stringify({id:"r-trunc",object:"chat.completion",choices:[{index:0,message:{role:"assistant",content:"ok"},finish_reason:"stop"}]});res.writeHead(200,{"Content-Type":"application/json","Content-Length":String(Buffer.byteLength(full)+100),"Connection":"close"});res.end(full);return;}
  res.writeHead(200,{"Content-Type":"text/event-stream","X-Request-Id":"req-upstream-header"});
  if(partial||!fail)res.write('data: {"id":"r-stream","choices":[{"index":0,"delta":{"content":"Partial answer"}}]}\n\n');
  if(fail)res.end("data: "+JSON.stringify({type:"error",error:{code:"vendor_capacity",type:"overloaded_error",message:marker,details:{retryable:true}},request_id:"req-upstream-body"})+"\n\n");
  else res.end('data: {"id":"r-stream","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}\n\ndata: [DONE]\n\n');
 });
 upstream.listen(0,"127.0.0.1");await once(upstream,"listening");
 const port=(upstream.address() as {port:number}).port;
 let modelID="", nativeModelID="";
 let nativeEnabled = false;
 await loginAdmin(page);
 const headers={Authorization:"Bearer "+await sessionBearer(page)};
 const model="stream-detail-"+locale+"-"+Date.now();
 try{
  await page.getByRole("button",{name:locale==="en"?"English":"中文",exact:true}).click();
  const created=await page.request.post(GATEWAY+"/model/new",{headers,data:{model_name:model,litellm_params:{model,custom_llm_provider:"openai",api_base:`http://127.0.0.1:${port}/v1`,api_key:"local-fixture",input_cost_per_token:0.000001,output_cost_per_token:0.000002},model_info:{transport:"bypass_openai_chat",pricing_source:"manual",endpoint_types:["chat","responses"]}}});
  expect(created.status(),await created.text()).toBe(200);modelID=(await created.json()).model_info.id;
  for(const path of ["/chat/completions","/responses"]){for(const midway of [false,true]){
   partial=midway;const before=calls;
   const data=path==="/responses"?{model,input:"Explain the failure.",stream:true}:{model,messages:[{role:"user",content:"Explain the failure."}],stream:true};
   const response=await page.request.post(GATEWAY+path,{headers,data});
   const text=await response.text();
   expect([200,502]).toContain(response.status());
   for(const value of [marker,"vendor_capacity","overloaded_error","req-upstream-body"])expect(text).toContain(value);
   expect(calls-before,"不能重放已失败的供应商请求").toBe(1);
   const call=response.headers()["x-litellm-call-id"];
   await expect.poll(async()=> (await page.request.get(GATEWAY+"/spend/logs/ui/"+call,{headers})).status()).toBe(200);
   const log=await (await page.request.get(GATEWAY+"/spend/logs/ui/"+call,{headers})).json();
   expect(log.status).toBe("error");expect(log.metadata.upstream_response.status_code).toBe(200);
   expect(log.metadata.upstream_response.error.error.message).toBe(marker);
   await stableGoto(page,"/logs?log_id="+call);
   const dialog=page.getByRole("dialog");await expect(dialog).toBeVisible();
   const details=dialog.getByRole("region",{name:/完整错误详情|全部错误详情|Full Error Details/});
   await expect(details).toContainText(marker);
   await expect(details).toContainText("req-upstream-body");
   await expect(details).toContainText("vendor_capacity");
   await expect(dialog.getByRole("alert")).toContainText("502");
   await page.keyboard.press("Escape");
  }}
  fail=false;
  const beforeSuccess=calls;
  const success=await page.request.post(GATEWAY+"/responses",{headers,data:{model,input:"Respond successfully.",stream:true}});
  expect(success.status()).toBe(200);expect(await success.text()).toContain("response.completed");
  expect(calls-beforeSuccess,"恢复请求只应调用一次上游").toBe(1);
  const successID=success.headers()["x-litellm-call-id"];
  await expect.poll(async()=> (await page.request.get(GATEWAY+"/spend/logs/ui/"+successID,{headers})).status()).toBe(200);
  const successLog=await (await page.request.get(GATEWAY+"/spend/logs/ui/"+successID,{headers})).json();
  expect(successLog.status).toBe("success");
  truncated=true;
  const failedBody=await page.request.post(GATEWAY+"/chat/completions",{headers,data:{model,messages:[{role:"user",content:"Test a truncated response."}],stream:false}});
  expect(failedBody.status()).toBe(502);
  const failedID=failedBody.headers()["x-litellm-call-id"];
  await expect.poll(async()=> (await page.request.get(GATEWAY+"/spend/logs/ui/"+failedID,{headers})).status()).toBe(200);
  const failedLog=await (await page.request.get(GATEWAY+"/spend/logs/ui/"+failedID,{headers})).json();
  expect(failedLog.status).toBe("error");
  expect(failedLog.metadata.upstream_response.read_error).toContain("unexpected EOF");
  await stableGoto(page,"/logs?log_id="+failedID);
  await expect(page.getByRole("dialog").getByRole("alert")).toContainText("502");
  await page.keyboard.press("Escape");
  truncated=false;truncated4xx=true;
  const before4xx=calls;
  const clientError=await page.request.post(GATEWAY+"/chat/completions",{headers,data:{model,messages:[{role:"user",content:"Test an incomplete provider error."}],stream:false}});
  expect(clientError.status()).toBe(400);expect(calls-before4xx).toBe(1);
  const errorID=clientError.headers()["x-litellm-call-id"];
  await expect.poll(async()=> (await page.request.get(GATEWAY+"/spend/logs/ui/"+errorID,{headers})).status()).toBe(200);
  const errorLog=await (await page.request.get(GATEWAY+"/spend/logs/ui/"+errorID,{headers})).json();
  expect(errorLog.status).toBe("error");expect(errorLog.metadata.upstream_response.read_error).toContain("unexpected EOF");
  await stableGoto(page,"/logs?log_id="+errorID);
  await expect(page.getByRole("dialog").getByRole("alert")).toContainText("400");
  await page.keyboard.press("Escape");
  // 空正文截断的流式 4xx 不可重试；日志保留读取错误并记零费用。
  truncated4xx=false;truncated4xxEmpty=true;
  const beforeEmpty=calls;
  const emptyError=await page.request.post(GATEWAY+"/chat/completions",{headers,data:{model,messages:[{role:"user",content:"Test an empty provider error."}],stream:true}});
  expect(emptyError.status()).toBe(400);expect(calls-beforeEmpty).toBe(1);
  const emptyID=emptyError.headers()["x-litellm-call-id"];
  await expect.poll(async()=> (await page.request.get(GATEWAY+"/spend/logs/ui/"+emptyID,{headers})).status()).toBe(200);
  const emptyLog=await (await page.request.get(GATEWAY+"/spend/logs/ui/"+emptyID,{headers})).json();
  expect(emptyLog.status).toBe("error");expect(emptyLog.spend).toBe(0);expect(emptyLog.metadata.upstream_response.read_error).toContain("unexpected EOF");
  await stableGoto(page,"/logs?log_id="+emptyID);
  await expect(page.getByRole("dialog").getByRole("alert")).toContainText("400");
  await page.keyboard.press("Escape");

  const nativeModel = model + "-native";
  const nativeDeployment = await page.request.post(GATEWAY + "/model/new", { headers, data: {
   model_name: nativeModel,
   litellm_params: { model: nativeModel, custom_llm_provider: "openai", api_base: `http://127.0.0.1:${port}/v1`,
    api_key: "local-fixture", input_cost_per_token: 0.000001, output_cost_per_token: 0.000002 },
   model_info: { transport: "bypass_openai_responses", endpoint_types: ["responses"], pricing_source: "manual" },
  } });
  expect(nativeDeployment.status(), await nativeDeployment.text()).toBe(200);
  nativeModelID = (await nativeDeployment.json()).model_info.id;
  const me = await page.request.get(GATEWAY + "/auth/me", { headers });
  const userID = (await me.json()).user_id;
  expect(userID).toBeTruthy();
  const config = await page.request.post(GATEWAY + "/config/update", { headers, data: {
   general_settings: { native_responses: { enabled: true, user_ids: [userID] } },
  } });
  expect(config.status(), await config.text()).toBe(200);
  nativeEnabled = true;

  for (const stream of [true, false]) {
   nativeTruncated = !stream;
   const before = calls;
   const response = await page.request.post(GATEWAY + "/v1/responses", {
    headers, data: { model: nativeModel, input: "Check an interrupted native response.", stream },
   });
   expect(response.status(), await response.text()).toBe(502);
   expect(response.headers()["x-xhub-responses-path"]).toBe("native");
   expect(response.headers()["content-type"]).toContain("application/json");
   expect(calls - before, "原生响应失败后应禁止重新生成").toBe(1);
   const message = stream ? nativeMarker : interruptedMessage;
   const error = await response.json();
   expect(error).toMatchObject(stream ? {
    error: { type: "api_error", code: "native_capacity", message },
    request_id: "req-native-sse", response_id: "resp_native_failed",
   } : { error: { reason: "upstream_stream_interrupted", message } });
   const id = response.headers()["x-litellm-call-id"];
   expect(id).toBeTruthy();
   const detailURL = GATEWAY + "/spend/logs/ui/" + id;
   await expect.poll(async () => (await page.request.get(detailURL, { headers })).status()).toBe(200);
   const detail = await (await page.request.get(detailURL, { headers })).json();
   expect(detail).toMatchObject({ status: "error", spend: 0 });
   expect(JSON.parse(detail.error)).toEqual(error);
   if (!stream) expect(detail.metadata.upstream_response.read_error).toContain("unexpected EOF");
   const query = new URLSearchParams({ request_id: id, error_message: message });
   const list = await page.request.get(GATEWAY + "/spend/logs/ui?" + query, { headers });
   expect((await list.json()).data).toEqual([expect.objectContaining({ request_id: id })]);
   await stableGoto(page, "/logs?log_id=" + id);
   const dialog = page.getByRole("dialog");
   await expect(dialog.getByRole("alert")).toContainText("502");
   await expect(dialog.getByRole("alert")).toContainText(message);
   if (!stream) await expect(dialog.getByRole("alert")).toContainText(translate(locale, "logsUX.upstreamStreamInterrupted"));
   await page.keyboard.press("Escape");
  }
 }finally{
  try {
   if (nativeEnabled) expect((await page.request.post(GATEWAY + "/config/field/delete", {
    headers, data: { config_type: "general_settings", field_name: "native_responses" },
   })).status()).toBe(200);
   for (const id of [nativeModelID, modelID]) {
    if (id) expect((await page.request.post(GATEWAY + "/model/delete", { headers, data: { id } })).status()).toBe(200);
   }
  } finally {
   upstream.closeAllConnections();await new Promise<void>((resolve,reject)=>upstream.close(err=>err?reject(err):resolve()));
  }
 }
});
}
