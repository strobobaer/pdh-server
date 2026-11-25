async function uploadFiles(input, refType, refId) {
  if(!input.files.length) return;
  const token = document.cookie.split(';').find(c=>c.trim().startsWith('pdh_token='))?.split('=')[1]||'';
  const fd = new FormData();
  for(const f of input.files) fd.append('files', f);
  
  const res = await fetch('/api/v1/attachments/'+refType+'/'+refId, {
    method:'POST', headers:{'Authorization':'Bearer '+token}, body:fd
  });
  const data = await res.json();
  if(data.data) {
    const container = document.getElementById('attachments-'+refId);
    if(container) {
      data.data.forEach(a => {
        container.insertAdjacentHTML('beforeend',
          `<div style="position:relative">
            <img src="${a.url}" style="width:80px;height:80px;object-fit:cover;border-radius:8px;border:1px solid var(--border)">
            <button onclick="deleteAttachment('${a.id}','${refType}','${refId}')"
              style="position:absolute;top:-6px;right:-6px;background:var(--red);border:0;color:#fff;border-radius:50%;width:18px;height:18px;font-size:11px;cursor:pointer;display:flex;align-items:center;justify-content:center">×</button>
          </div>`
        );
      });
    }
  }
}

async function deleteAttachment(id, refType, refId) {
  const token = document.cookie.split(';').find(c=>c.trim().startsWith('pdh_token='))?.split('=')[1]||'';
  await fetch('/api/v1/attachments/'+id, {method:'DELETE',headers:{'Authorization':'Bearer '+token}});
  htmx.trigger('#attachments-'+refId, 'load');
}
