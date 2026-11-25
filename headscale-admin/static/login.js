// 登录页专用脚本。
//
// 单独一个文件，不并进 app.js：app.js 里有十几段依赖后台页面元素的逻辑
// （节点表、网速面板、ACL 向导……），登录页根本没有那些元素。整包加载既没有意义，
// 也可能因为某段代码在登录页报错而把后面的逻辑一起带崩。
(function () {
  var img = document.getElementById('captcha-img');
  if (!img) return;
  var input = document.getElementById('cap');
  img.addEventListener('click', function () {
    // 换图等于换验证码：服务端每次都换一张新的，旧码随即作废，
    // 所以输入框里按旧图填的数字也要一起清掉。
    if (input) input.value = '';
    img.src = '/login/captcha.png?n=' + Date.now();
  });
})();
