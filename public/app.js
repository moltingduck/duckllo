// ── State ────────────────────────────────────────────────────────────────
let token = localStorage.getItem('duckllo_token');
let currentUser = null;
let projects = [];
let currentProject = null;
let cards = [];

// ── API Helper ──────────────────────────────────────────────────────────
async function api(path, opts = {}) {
  const headers = { ...opts.headers };
  if (token) headers['Authorization'] = `Bearer ${token}`;
  if (opts.body && !(opts.body instanceof FormData)) {
    headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(opts.body);
  }
  const res = await fetch(`/api${path}`, { ...opts, headers, body: opts.body });
  const data = await res.json();
  if (!res.ok) throw new Error(data.error || 'Request failed');
  return data;
}

// ── Auth ─────────────────────────────────────────────────────────────────
const authScreen = document.getElementById('auth-screen');
const mainScreen = document.getElementById('main-screen');
const authError = document.getElementById('auth-error');

const authSuccess = document.getElementById('auth-success');

function showAuthForm(form) {
  document.getElementById('login-form').style.display = 'none';
  document.getElementById('register-form').style.display = 'none';
  document.getElementById('reset-form').style.display = 'none';
  document.querySelector('.auth-tabs').style.display = form === 'reset' ? 'none' : 'flex';
  if (form === 'login') document.getElementById('login-form').style.display = 'flex';
  else if (form === 'register') document.getElementById('register-form').style.display = 'flex';
  else if (form === 'reset') document.getElementById('reset-form').style.display = 'flex';
  authError.textContent = '';
  authSuccess.textContent = '';
}

document.querySelectorAll('.auth-tab').forEach(tab => {
  tab.addEventListener('click', () => {
    document.querySelectorAll('.auth-tab').forEach(t => t.classList.remove('active'));
    tab.classList.add('active');
    showAuthForm(tab.dataset.tab);
  });
});

document.getElementById('forgot-password-link').addEventListener('click', (e) => {
  e.preventDefault();
  showAuthForm('reset');
});

document.getElementById('back-to-login-link').addEventListener('click', (e) => {
  e.preventDefault();
  document.querySelectorAll('.auth-tab').forEach(t => t.classList.remove('active'));
  document.querySelector('[data-tab="login"]').classList.add('active');
  showAuthForm('login');
});

document.getElementById('login-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  try {
    const data = await api('/auth/login', {
      method: 'POST',
      body: { username: document.getElementById('login-username').value, password: document.getElementById('login-password').value }
    });
    token = data.token;
    localStorage.setItem('duckllo_token', token);
    currentUser = data.user;
    showApp();
  } catch (err) { authError.textContent = err.message; }
});

document.getElementById('register-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  try {
    const data = await api('/auth/register', {
      method: 'POST',
      body: {
        username: document.getElementById('reg-username').value,
        password: document.getElementById('reg-password').value,
        display_name: document.getElementById('reg-display').value
      }
    });
    token = data.token;
    localStorage.setItem('duckllo_token', token);
    currentUser = data.user;
    showApp();
  } catch (err) { authError.textContent = err.message; }
});

document.getElementById('reset-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  authError.textContent = '';
  authSuccess.textContent = '';
  try {
    await api('/auth/reset-password', {
      method: 'POST',
      body: {
        username: document.getElementById('reset-username').value,
        recovery_code: document.getElementById('reset-code').value,
        new_password: document.getElementById('reset-password').value
      }
    });
    authSuccess.textContent = 'Password reset successfully. You can now login with your new password.';
    document.getElementById('reset-code').value = '';
    document.getElementById('reset-password').value = '';
    setTimeout(() => {
      document.querySelectorAll('.auth-tab').forEach(t => t.classList.remove('active'));
      document.querySelector('[data-tab="login"]').classList.add('active');
      showAuthForm('login');
      document.getElementById('login-username').value = document.getElementById('reset-username').value;
      document.getElementById('reset-username').value = '';
    }, 2000);
  } catch (err) { authError.textContent = err.message; }
});

document.getElementById('logout-btn').addEventListener('click', async () => {
  try { await api('/auth/logout', { method: 'POST' }); } catch {}
  token = null;
  currentUser = null;
  localStorage.removeItem('duckllo_token');
  mainScreen.style.display = 'none';
  authScreen.style.display = 'block';
});

// ── Init ─────────────────────────────────────────────────────────────────
async function init() {
  if (!token) return;
  try {
    const data = await api('/auth/me');
    currentUser = data.user;
    showApp();
  } catch {
    token = null;
    localStorage.removeItem('duckllo_token');
  }
}

async function showApp() {
  authScreen.style.display = 'none';
  mainScreen.style.display = 'block';
  document.getElementById('user-display').textContent = currentUser.display_name || currentUser.username;
  await loadProjects();
}

// ── Projects ─────────────────────────────────────────────────────────────
const projectSelect = document.getElementById('project-select');

async function loadProjects() {
  projects = await api('/projects');
  projectSelect.innerHTML = '';
  if (projects.length === 0) {
    projectSelect.innerHTML = '<option value="">No projects</option>';
    currentProject = null;
    renderBoard();
    return;
  }
  const savedId = localStorage.getItem('duckllo_project');
  projects.forEach(p => {
    const opt = document.createElement('option');
    opt.value = p.id;
    opt.textContent = p.name;
    if (p.id === savedId) opt.selected = true;
    projectSelect.appendChild(opt);
  });
  currentProject = projects.find(p => p.id === savedId) || projects[0];
  projectSelect.value = currentProject.id;
  await loadBoard();
}

projectSelect.addEventListener('change', async () => {
  currentProject = projects.find(p => p.id === projectSelect.value);
  localStorage.setItem('duckllo_project', currentProject.id);
  await loadBoard();
});

// New project
document.getElementById('new-project-btn').addEventListener('click', () => {
  openModal('new-project-modal');
});

document.getElementById('new-project-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const p = await api('/projects', {
    method: 'POST',
    body: { name: document.getElementById('new-project-name').value, description: document.getElementById('new-project-desc').value }
  });
  closeAllModals();
  document.getElementById('new-project-name').value = '';
  document.getElementById('new-project-desc').value = '';
  localStorage.setItem('duckllo_project', p.id);
  await loadProjects();
});

// ── Board ────────────────────────────────────────────────────────────────
const board = document.getElementById('board');

async function loadBoard() {
  if (!currentProject) { renderBoard(); return; }
  cards = await api(`/projects/${currentProject.id}/cards`);
  renderBoard();
}

function renderBoard() {
  board.innerHTML = '';
  if (!currentProject) {
    board.innerHTML = '<div style="padding:40px;color:var(--text-secondary)">Create a project to get started.</div>';
    return;
  }

  const columns = currentProject.columns_config || ['Backlog', 'Todo', 'In Progress', 'Review', 'Done'];

  columns.forEach(col => {
    const colCards = cards.filter(c => c.column_name === col).sort((a, b) => a.position - b.position);
    const colEl = document.createElement('div');
    colEl.className = 'column';
    colEl.innerHTML = `
      <div class="column-header">
        <span class="column-title">${col}</span>
        <span class="column-count">${colCards.length}</span>
      </div>
      <div class="column-cards" data-column="${col}"></div>
      <button class="add-card-btn" data-column="${col}">+ Add card</button>
    `;

    const cardsContainer = colEl.querySelector('.column-cards');

    // Drag & drop
    cardsContainer.addEventListener('dragover', (e) => {
      e.preventDefault();
      cardsContainer.classList.add('drag-over');
    });
    cardsContainer.addEventListener('dragleave', () => {
      cardsContainer.classList.remove('drag-over');
    });
    cardsContainer.addEventListener('drop', async (e) => {
      e.preventDefault();
      cardsContainer.classList.remove('drag-over');
      const cardId = e.dataTransfer.getData('text/plain');
      if (!cardId) return;
      // Determine position
      const cardEls = [...cardsContainer.querySelectorAll('.card')];
      let position = cardEls.length;
      for (let i = 0; i < cardEls.length; i++) {
        const rect = cardEls[i].getBoundingClientRect();
        if (e.clientY < rect.top + rect.height / 2) {
          position = i;
          break;
        }
      }
      try {
        await api(`/projects/${currentProject.id}/cards/${cardId}/move`, {
          method: 'POST',
          body: { column_name: col, position }
        });
        await loadBoard();
      } catch (err) { console.error(err); }
    });

    colCards.forEach(card => {
      const cardEl = createCardElement(card);
      cardsContainer.appendChild(cardEl);
    });

    colEl.querySelector('.add-card-btn').addEventListener('click', () => {
      document.getElementById('new-card-column').value = col;
      openModal('new-card-modal');
    });

    board.appendChild(colEl);
  });
}

function createCardElement(card) {
  const el = document.createElement('div');
  el.className = 'card';
  el.draggable = true;
  el.dataset.id = card.id;

  el.addEventListener('dragstart', (e) => {
    e.dataTransfer.setData('text/plain', card.id);
    el.classList.add('dragging');
  });
  el.addEventListener('dragend', () => el.classList.remove('dragging'));
  el.addEventListener('click', () => openCardDetail(card));

  const labels = (card.labels || []).map(l => `<span class="card-label">${escHtml(l)}</span>`).join('');
  const hasGif = card.demo_gif_url ? '<span class="card-has-gif">GIF</span>' : '';

  el.innerHTML = `
    <div class="card-top-row">
      <span class="card-type ${card.card_type}">${card.card_type}</span>
      <span class="card-priority ${card.priority}">${card.priority}</span>
    </div>
    <div class="card-title">${escHtml(card.title)}</div>
    <div class="card-meta">
      <span class="card-testing ${card.testing_status}">${card.testing_status}</span>
      ${hasGif}
      ${labels}
    </div>
  `;
  return el;
}

// ── New Card ─────────────────────────────────────────────────────────────
document.getElementById('new-card-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  await api(`/projects/${currentProject.id}/cards`, {
    method: 'POST',
    body: {
      title: document.getElementById('new-card-title').value,
      description: document.getElementById('new-card-desc').value,
      card_type: document.getElementById('new-card-type').value,
      priority: document.getElementById('new-card-priority').value,
      column_name: document.getElementById('new-card-column').value
    }
  });
  closeAllModals();
  document.getElementById('new-card-title').value = '';
  document.getElementById('new-card-desc').value = '';
  await loadBoard();
});

// ── Card Detail ──────────────────────────────────────────────────────────
let currentCard = null;

async function openCardDetail(card) {
  currentCard = card;
  const columns = currentProject.columns_config || [];

  document.getElementById('detail-title').value = card.title;
  document.getElementById('detail-description').value = card.description || '';
  document.getElementById('detail-testing-status').value = card.testing_status || 'untested';
  document.getElementById('detail-testing-result').value = card.testing_result || '';
  document.getElementById('detail-priority').value = card.priority || 'medium';
  document.getElementById('detail-card-type').value = card.card_type || 'feature';
  document.getElementById('detail-labels').value = (card.labels || []).join(', ');

  const typeBadge = document.getElementById('detail-type');
  typeBadge.textContent = card.card_type;
  typeBadge.className = `card-type-badge card-type ${card.card_type}`;

  // Column select
  const colSelect = document.getElementById('detail-column');
  colSelect.innerHTML = '';
  columns.forEach(c => {
    const opt = document.createElement('option');
    opt.value = c;
    opt.textContent = c;
    if (c === card.column_name) opt.selected = true;
    colSelect.appendChild(opt);
  });

  // GIF preview
  const gifPreview = document.getElementById('detail-gif-preview');
  if (card.demo_gif_url) {
    const ext = card.demo_gif_url.split('.').pop().toLowerCase();
    if (ext === 'mp4') {
      gifPreview.innerHTML = `<video src="${escHtml(card.demo_gif_url)}" controls autoplay loop muted></video>`;
    } else {
      gifPreview.innerHTML = `<img src="${escHtml(card.demo_gif_url)}" alt="Demo">`;
    }
  } else {
    gifPreview.innerHTML = '<span style="color:var(--text-secondary);font-size:0.85rem">No demo media uploaded</span>';
  }

  // Load comments
  await loadComments(card.id);

  openModal('card-modal');
}

async function loadComments(cardId) {
  const comments = await api(`/projects/${currentProject.id}/cards/${cardId}/comments`);
  const list = document.getElementById('detail-comments');
  list.innerHTML = '';
  comments.forEach(c => {
    const div = document.createElement('div');
    div.className = `comment comment-type-${c.comment_type}`;
    div.innerHTML = `
      <div class="comment-header">
        <span class="comment-author">${escHtml(c.display_name || c.username || 'Agent')}</span>
        <span class="comment-time">${new Date(c.created_at).toLocaleString()}</span>
      </div>
      <div class="comment-body">${escHtml(c.content)}</div>
    `;
    list.appendChild(div);
  });
  list.scrollTop = list.scrollHeight;
}

// Save card
document.getElementById('save-card-btn').addEventListener('click', async () => {
  if (!currentCard) return;
  const labelsStr = document.getElementById('detail-labels').value;
  const labels = labelsStr ? labelsStr.split(',').map(s => s.trim()).filter(Boolean) : [];

  const updates = {
    title: document.getElementById('detail-title').value,
    description: document.getElementById('detail-description').value,
    testing_status: document.getElementById('detail-testing-status').value,
    testing_result: document.getElementById('detail-testing-result').value,
    priority: document.getElementById('detail-priority').value,
    card_type: document.getElementById('detail-card-type').value,
    column_name: document.getElementById('detail-column').value,
    labels
  };

  await api(`/projects/${currentProject.id}/cards/${currentCard.id}`, { method: 'PATCH', body: updates });
  closeAllModals();
  await loadBoard();
});

// Delete card
document.getElementById('delete-card-btn').addEventListener('click', async () => {
  if (!currentCard) return;
  if (!confirm('Delete this card?')) return;
  await api(`/projects/${currentProject.id}/cards/${currentCard.id}`, { method: 'DELETE' });
  closeAllModals();
  await loadBoard();
});

// Upload GIF
document.getElementById('detail-gif-upload').addEventListener('change', async (e) => {
  if (!currentCard || !e.target.files[0]) return;
  const form = new FormData();
  form.append('file', e.target.files[0]);
  const res = await api(`/projects/${currentProject.id}/cards/${currentCard.id}/upload`, { method: 'POST', body: form });
  currentCard.demo_gif_url = res.url;
  const ext = res.url.split('.').pop().toLowerCase();
  const gifPreview = document.getElementById('detail-gif-preview');
  if (ext === 'mp4') {
    gifPreview.innerHTML = `<video src="${escHtml(res.url)}" controls autoplay loop muted></video>`;
  } else {
    gifPreview.innerHTML = `<img src="${escHtml(res.url)}" alt="Demo">`;
  }
  e.target.value = '';
});

// Add comment
document.getElementById('add-comment-btn').addEventListener('click', async () => {
  const content = document.getElementById('new-comment').value.trim();
  if (!content || !currentCard) return;
  await api(`/projects/${currentProject.id}/cards/${currentCard.id}/comments`, {
    method: 'POST',
    body: { content }
  });
  document.getElementById('new-comment').value = '';
  await loadComments(currentCard.id);
});

// ── Settings ─────────────────────────────────────────────────────────────
document.getElementById('settings-btn').addEventListener('click', async () => {
  if (!currentProject) return;
  const projectData = await api(`/projects/${currentProject.id}`);
  const membersList = document.getElementById('members-list');
  membersList.innerHTML = '';
  projectData.members.forEach(m => {
    const div = document.createElement('div');
    div.className = 'member-row';
    div.innerHTML = `<span>${escHtml(m.display_name || m.username)} <small style="color:var(--text-secondary)">(${m.role})</small></span>`;
    membersList.appendChild(div);
  });

  const keys = await api(`/projects/${currentProject.id}/api-keys`);
  const keysList = document.getElementById('api-keys-list');
  keysList.innerHTML = '';
  keys.forEach(k => {
    const div = document.createElement('div');
    div.className = 'key-row';
    div.innerHTML = `
      <span><strong>${escHtml(k.label)}</strong> <small style="color:var(--text-secondary)">${k.key_prefix}</small></span>
      <button class="btn btn-sm btn-danger" onclick="deleteApiKey('${k.id}')">Revoke</button>
    `;
    keysList.appendChild(div);
  });

  document.getElementById('new-key-display').style.display = 'none';
  openModal('settings-modal');
});

document.getElementById('add-member-btn').addEventListener('click', async () => {
  const username = document.getElementById('add-member-username').value.trim();
  if (!username) return;
  try {
    await api(`/projects/${currentProject.id}/members`, { method: 'POST', body: { username } });
    document.getElementById('add-member-username').value = '';
    document.getElementById('settings-btn').click();
  } catch (err) { alert(err.message); }
});

document.getElementById('create-key-btn').addEventListener('click', async () => {
  const label = document.getElementById('new-key-label').value.trim() || 'Agent Key';
  const data = await api(`/projects/${currentProject.id}/api-keys`, { method: 'POST', body: { label } });
  document.getElementById('new-key-value').textContent = data.key;
  document.getElementById('new-key-display').style.display = 'block';
  document.getElementById('new-key-label').value = '';
  // Reload keys list
  const keys = await api(`/projects/${currentProject.id}/api-keys`);
  const keysList = document.getElementById('api-keys-list');
  keysList.innerHTML = '';
  keys.forEach(k => {
    const div = document.createElement('div');
    div.className = 'key-row';
    div.innerHTML = `
      <span><strong>${escHtml(k.label)}</strong> <small style="color:var(--text-secondary)">${k.key_prefix}</small></span>
      <button class="btn btn-sm btn-danger" onclick="deleteApiKey('${k.id}')">Revoke</button>
    `;
    keysList.appendChild(div);
  });
});

window.deleteApiKey = async (keyId) => {
  await api(`/projects/${currentProject.id}/api-keys/${keyId}`, { method: 'DELETE' });
  document.getElementById('settings-btn').click();
};

// ── Modals ───────────────────────────────────────────────────────────────
function openModal(id) {
  document.getElementById(id).style.display = 'flex';
}

function closeAllModals() {
  document.querySelectorAll('.modal').forEach(m => m.style.display = 'none');
  currentCard = null;
}

document.querySelectorAll('.modal-backdrop, .modal-close').forEach(el => {
  el.addEventListener('click', closeAllModals);
});

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') closeAllModals();
});

// Stop propagation on modal content
document.querySelectorAll('.modal-content').forEach(el => {
  el.addEventListener('click', (e) => e.stopPropagation());
});

// ── Utils ────────────────────────────────────────────────────────────────
function escHtml(str) {
  if (!str) return '';
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}

// ── Boot ─────────────────────────────────────────────────────────────────
init();
