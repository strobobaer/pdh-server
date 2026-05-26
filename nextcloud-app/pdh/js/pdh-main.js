document.addEventListener('DOMContentLoaded', async () => {
  const el = document.getElementById('pdh-status');
  const frame = document.getElementById('pdh-frame');
  const reload = document.getElementById('pdh-reload-frame');

  if (reload && frame) {
    reload.addEventListener('click', () => {
      frame.src = frame.src;
    });
  }

  if (!el) return;

  try {
    const response = await fetch(OC.generateUrl('/apps/pdh/api/status'), {
      headers: { 'Accept': 'application/json' },
    });
    const data = await response.json();
    if (data.success && data.status === 'online') {
      el.className = 'pdh-status ok';
      el.textContent = 'PDH ist online: ' + data.baseUrl;
    } else {
      el.className = 'pdh-status warn';
      el.textContent = 'PDH ist nicht erreichbar: ' + (data.baseUrl || 'unbekannt');
    }
  } catch (err) {
    el.className = 'pdh-status error';
    el.textContent = 'Status konnte nicht geladen werden.';
  }
});
