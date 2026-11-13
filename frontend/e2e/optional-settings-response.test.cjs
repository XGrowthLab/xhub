const test = require('node:test');
const assert = require('node:assert/strict');
const { isExpectedOptionalSettingsFailure: matches, isOptionalSettingsRead, createOptionalReadTracker } = require('./optional-settings-response.cjs');
const base = {url:'http://localhost/gw/get/ui_settings',method:'GET',status:501,cacheControl:'no-store',body:{error:{type:'unsupported_settings_operation',code:'501',message:'Unavailable'}}};
/** 仅精确读取和完整失败契约属于可选设置降级；纯函数，无资源需清理。 */
test('optional settings require an explicit unavailable contract', () => {
 for(const prefix of ['', '/gw']) for(const path of ['/get/ui_settings','/sso/get/ui_settings','/get/user_banner']) assert.equal(matches({...base,url:'http://localhost'+prefix+path}),true);
 for(const change of [{status:500},{status:503},{status:200},{method:'PATCH'},{cacheControl:'public'},{body:null},{body:{error:{type:'unavailable'}}},{url:'http://localhost/get/ui_theme_settings'},{url:'http://localhost/get/ui_settings/extra'},{url:'http://localhost/config/list'},{url:'http://localhost/model/available'},{url:'not-a-url'},{body:{error:{...base.body.error,message:''}}},{body:{error:{...base.body.error,code:'500'}}}]) assert.equal(matches({...base,...change}),false,JSON.stringify(change));
});
/** 所有页面响应巡检必须被等待，不能让异步校验逃逸；只读本地源码，无环境副作用。 */
test('every page guard assertion is awaited', () => {
 const fs=require('node:fs'), path=require('node:path');
 for(const file of fs.readdirSync(__dirname, {recursive:true}).filter(x=>x.endsWith('.ts'))) {
  const source=fs.readFileSync(path.join(__dirname,file),'utf8');
  for(const line of source.split('\n').filter(x=>x.includes('.assertOk('))) assert.match(line,/await \w+\.assertOk\(/,file+': '+line);
 }
});

/** 其他错误应立即失败，不等待无关响应正文；纯函数无清理。 */
test('only optional reads need asynchronous body checks', () => {
 assert.equal(isOptionalSettingsRead(base),true);
 for(const change of [{status:500},{method:'PATCH'},{url:'http://localhost/config/list'}]) assert.equal(isOptionalSettingsRead({...base,...change}),false);
});

/** 已发出的可选请求必须在导航前完成正文校验；模拟延迟响应与正文，无网络数据需清理。 */
test('optional request checkpoint waits for response and validates the original body', async () => {
 const bad=[], tracker=createOptionalReadTracker(bad);
 const request={url:()=>base.url,method:()=>base.method};
 let respond;
 const body=new Promise(resolve=>{respond=resolve;});
 tracker.begin(request);
 let drained=false;
 const checkpoint=tracker.drain().then(()=>{drained=true;});
 await Promise.resolve();
 assert.equal(drained,false,'响应尚未返回时不能开始导航');
 tracker.response({request:()=>request,url:()=>base.url,status:()=>501,headers:()=>({'cache-control':'no-store'}),json:()=>body});
 await Promise.resolve();
 assert.equal(drained,false,'正文未完成时不能开始导航');
 respond(base.body);
 await checkpoint;
 assert.deepEqual(bad,[]);
});

/** 501 正文错误或收到响应后被取消均不能假通过；不涉及真实请求或资源清理。 */
test('optional response errors and interrupted bodies are reported without hanging', async () => {
 for(const interrupted of [false,true]) {
  const bad=[], tracker=createOptionalReadTracker(bad);
  const request={url:()=>base.url,method:()=>base.method};
  tracker.begin(request);
  tracker.response({request:()=>request,url:()=>base.url,status:()=>501,headers:()=>({'cache-control':'no-store'}),
   json:()=>interrupted ? new Promise(()=>{}) : Promise.resolve({error:{type:'unexpected'}})});
  if(interrupted) tracker.failed(request);
  await tracker.drain();
  assert.equal(bad.length,1,'异常 501 正文必须报告');
 }
});

/** 发送后、收到响应前取消只解除等待；没有收到 5xx，不能伪造错误正文或遗留挂起请求。 */
test('optional request cancelled before response releases the checkpoint', async () => {
 const bad=[], tracker=createOptionalReadTracker(bad);
 const request={url:()=>base.url,method:()=>base.method};
 tracker.begin(request);
 tracker.failed(request);
 await tracker.drain();
 assert.deepEqual(bad,[]);
});

/** 模拟主页面导航时浏览器未报告取消；旧请求不挂起，新页面 501 仍须验证原始正文。 */
test('navigation releases unanswered reads and still checks later page responses', async () => {
 const bad=[], tracker=createOptionalReadTracker(bad);
 const old={url:()=>base.url,method:()=>base.method};
 tracker.begin(old);
 tracker.navigation();
 const current={url:()=>base.url,method:()=>base.method};
 tracker.begin(current);
 tracker.response({request:()=>current,url:()=>base.url,status:()=>501,headers:()=>({'cache-control':'no-store'}),json:()=>Promise.resolve(base.body)});
 await tracker.drain();
 assert.deepEqual(bad,[]);
});

/** 导航时已有 501 但正文未完成，或导航后收到旧请求 501，均应报告而非视为成功。 */
test('navigation never hides an observed or late 501 response', async () => {
 for(const observed of [false,true]) {
  const bad=[], tracker=createOptionalReadTracker(bad);
  const request={url:()=>base.url,method:()=>base.method};
  const response={request:()=>request,url:()=>base.url,status:()=>501,headers:()=>({'cache-control':'no-store'}),json:()=>new Promise(()=>{})};
  tracker.begin(request);
  if(observed) tracker.response(response);
  tracker.navigation();
  if(!observed) tracker.response(response);
  await tracker.drain();
  assert.equal(bad.length,1,'不可核验的 501 必须报告');
 }
});

/** 一个 frame 的导航只释放该 frame 的旧请求；其他 frame 返回的错误仍会严格报告。 */
test('navigation preserves outstanding requests from other frames', async () => {
 const bad=[], tracker=createOptionalReadTracker(bad), first={}, second={};
 const old={url:()=>base.url,method:()=>base.method,frame:()=>first};
 const other={url:()=>base.url,method:()=>base.method,frame:()=>second};
 tracker.begin(old);
 tracker.begin(other);
 tracker.navigation(first);
 tracker.response({request:()=>other,url:()=>base.url,status:()=>501,headers:()=>({'cache-control':'no-store'}),json:()=>Promise.resolve({error:{type:'unexpected'}})});
 await tracker.drain();
 assert.equal(bad.length,1,'其他 frame 的错误正文不得被导航掩盖');
});
