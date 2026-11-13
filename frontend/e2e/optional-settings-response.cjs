const { matchesGatewayResponse } = require('./gateway-response.cjs');
/** 判断是否需要读取可选设置的失败正文；只读元信息，无网络或状态副作用。 */
function isOptionalSettingsRead({url,method,status}) {
 return status===501 && ['/get/ui_settings','/sso/get/ui_settings','/get/user_banner'].some(path => matchesGatewayResponse(url, method, path, 'GET'));
}
/** 校验登录时可选设置的明确不支持契约；输入为真实响应元信息和JSON，返回布尔值，无副作用。
 * 只接受三个GET、501、结构化类型及no-store；其余5xx仍让页面巡检失败，不能把失败计为功能成功。
 */
function isExpectedOptionalSettingsFailure({url, method, status, cacheControl, body}) {
  return status === 501 && cacheControl === 'no-store'
    && body?.error?.type === 'unsupported_settings_operation'
    && body?.error?.code === '501' && typeof body?.error?.message === 'string' && body.error.message.length > 0
    && isOptionalSettingsRead({url,method,status});
}
/** 跟踪已经发出的可选读取，确保页面导航前先验证真实的 501 正文。
 * bad 收集无法验证和契约不符的错误；begin/response/failed 对应浏览器网络事件，
 * drain 等待当前及等待期间新增的请求。不改变网络响应或将未知错误视为成功。 */
function createOptionalReadTracker(bad) {
  const active = new Map();
  const navigated = new WeakSet();
  const checks = [];
  function begin(request) {
    if (!isOptionalSettingsRead({url: request.url(), method: request.method(), status: 501}) || active.has(request)) return;
    let resolve;
    const check = new Promise(done => { resolve = done; });
    // service worker 请求可能没有所属 frame；不推断其页面归属。
    let frame;
    try { frame = request.frame?.(); } catch { frame = undefined; }
    active.set(request, {resolve, frame, observed: false});
    checks.push(check);
  }
  function finish(request) {
    const item = active.get(request);
    if (!item) return;
    active.delete(request);
    item.resolve();
  }
  function response(res) {
    const request = res.request();
    if (navigated.has(request)) {
      // 旧页面的响应若迟到，不能将无法核验的 501 静默判为合法降级。
      if (isOptionalSettingsRead({url: res.url(), method: request.method(), status: res.status()}))
        bad.push(`unreadable 501 ${request.method()} ${request.url()}`);
      return;
    }
    begin(request);
    const item = active.get(request);
    if (!item) return;
    if (!isOptionalSettingsRead({url: res.url(), method: request.method(), status: res.status()})) {
      finish(request);
      return;
    }
    item.observed = true;
    void (async () => {
      const body = await res.json().catch(() => null);
      if (!active.has(request)) return;
      if (!isExpectedOptionalSettingsFailure({url: res.url(), method: request.method(), status: res.status(), cacheControl: res.headers()['cache-control'], body})) {
        bad.push(`${res.status()} ${request.method()} ${res.url()}`);
      }
      finish(request);
    })();
  }
  function failed(request) {
    const item = active.get(request);
    if (!item) return;
    // 已收到 501 却未读到正文时不能判定为合法降级；未收到响应的取消不属于观察到的 5xx。
    if (item.observed) bad.push(`unreadable 501 ${request.method()} ${request.url()}`);
    finish(request);
  }
  /** 指定 frame 导航会取消其旧请求，但浏览器不保证发出 requestfailed；终止对应旧请求等待。
   * 已观察到 501 时正文不可读仍报错；其他 frame 与新文档的请求继续正常验证。 */
  function navigation(frame) {
    for (const [request, item] of active) {
      if (frame !== undefined && item.frame !== frame) continue;
      navigated.add(request);
      if (item.observed) bad.push(`unreadable 501 ${request.method()} ${request.url()}`);
      finish(request);
    }
  }
  async function drain() {
    let checked = 0;
    while (checked < checks.length) {
      const batch = checks.slice(checked);
      checked += batch.length;
      await Promise.all(batch);
    }
  }
  return {begin, response, failed, navigation, drain};
}
module.exports = { isExpectedOptionalSettingsFailure, isOptionalSettingsRead, createOptionalReadTracker };
