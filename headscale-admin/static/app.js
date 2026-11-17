document.addEventListener('DOMContentLoaded', function () {
  const side = document.querySelector('.side');
  document.addEventListener('click', function (e) {
    if (side && side.classList.contains('show') && !e.target.closest('.side') && !e.target.closest('.menu')) {
      side.classList.remove('show');
    }
  });
  document.querySelectorAll('form[data-confirm]').forEach(function (f) {
    f.addEventListener('submit', function (e) {
      if (!window.confirm(f.dataset.confirm)) e.preventDefault();
    });
  });
});
