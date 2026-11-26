// 主题必须在样式生效前定下来，否则页面会先闪一下默认配色再切过去。
//
// 这段原本是 <head> 里的一段内联脚本。内容安全策略（CSP）不允许内联脚本，
// 所以挪成独立文件：脚本放在 <head> 里且不带 defer/async，
// 浏览器会等它执行完再往下解析，闪烁问题跟原来的内联脚本一样没有。
document.documentElement.dataset.theme = localStorage.getItem('hsa-theme') || 'light';
