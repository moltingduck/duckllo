// ── State ────────────────────────────────────────────────────────────────
let token = localStorage.getItem('duckllo_token');
let currentUser = null;
let projects = [];
let currentProject = null;
let currentMemberRole = null;
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
  if (eventSource) { eventSource.close(); eventSource = null; }
  if (countsInterval) { clearInterval(countsInterval); countsInterval = null; }
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
  document.getElementById('admin-btn').style.display = currentUser.system_role === 'admin' ? '' : 'none';
  await loadProjects();
}

// ── Projects ─────────────────────────────────────────────────────────────
const projectDropdownBtn = document.getElementById('project-dropdown-btn');
const projectDropdownMenu = document.getElementById('project-dropdown-menu');
const projectDropdownLabel = document.getElementById('project-dropdown-label');
const projectDropdownBadges = document.getElementById('project-dropdown-badges');
let projectCounts = {};
let prevProjectCounts = {};
let countsInterval = null;

async function loadProjects() {
  projects = await api('/projects');
  if (projects.length === 0) {
    projectDropdownLabel.textContent = 'No projects';
    currentProject = null;
    renderBoard();
    return;
  }
  const savedId = localStorage.getItem('duckllo_project');
  currentProject = projects.find(p => p.id === savedId) || projects[0];
  localStorage.setItem('duckllo_project', currentProject.id);
  await refreshCounts();
  renderProjectDropdown();
  await loadBoard();
  startCountsPolling();
}

function renderProjectDropdown() {
  // Update button label + badges for current project
  projectDropdownLabel.textContent = currentProject ? currentProject.name : 'No projects';
  updateCurrentBadges();

  // Build menu
  projectDropdownMenu.innerHTML = '';
  projects.forEach(p => {
    const opt = document.createElement('div');
    opt.className = `project-option${p.id === currentProject?.id ? ' active' : ''}`;
    const counts = projectCounts[p.id] || { proposed: 0, review: 0 };
    const isNew = prevProjectCounts[p.id] &&
      (counts.proposed > (prevProjectCounts[p.id]?.proposed || 0) ||
       counts.review > (prevProjectCounts[p.id]?.review || 0));

    opt.innerHTML = `
      <span class="project-option-name">${escHtml(p.name)}${isNew ? '<span class="notify-dot"></span>' : ''}</span>
      <span class="project-option-counts">
        ${counts.proposed ? `<span class="badge badge-proposed${isNew ? ' badge-new' : ''}">${counts.proposed} proposed</span>` : ''}
        ${counts.review ? `<span class="badge badge-review${isNew ? ' badge-new' : ''}">${counts.review} review</span>` : ''}
      </span>
    `;
    opt.addEventListener('click', async () => {
      currentProject = p;
      localStorage.setItem('duckllo_project', p.id);
      projectDropdownMenu.classList.remove('open');
      renderProjectDropdown();
      await loadBoard();
    });
    projectDropdownMenu.appendChild(opt);
  });
}

function updateCurrentBadges() {
  if (!currentProject) { projectDropdownBadges.innerHTML = ''; return; }
  const counts = projectCounts[currentProject.id] || { proposed: 0, review: 0 };
  const isNew = prevProjectCounts[currentProject.id] &&
    (counts.proposed > (prevProjectCounts[currentProject.id]?.proposed || 0) ||
     counts.review > (prevProjectCounts[currentProject.id]?.review || 0));
  let html = '';
  if (counts.proposed) html += `<span class="badge badge-proposed${isNew ? ' badge-new' : ''}">${counts.proposed}</span>`;
  if (counts.review) html += `<span class="badge badge-review${isNew ? ' badge-new' : ''}">${counts.review}</span>`;
  projectDropdownBadges.innerHTML = html;
}

async function refreshCounts() {
  try {
    prevProjectCounts = { ...projectCounts };
    projectCounts = await api('/projects/counts');
  } catch {}
}

function startCountsPolling() {
  if (countsInterval) clearInterval(countsInterval);
  countsInterval = setInterval(async () => {
    await refreshCounts();
    renderProjectDropdown();
  }, 15000);
}

// Toggle dropdown
projectDropdownBtn.addEventListener('click', (e) => {
  e.stopPropagation();
  projectDropdownMenu.classList.toggle('open');
});

// Close dropdown on outside click
document.addEventListener('click', () => {
  projectDropdownMenu.classList.remove('open');
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
  if (!currentProject) { currentMemberRole = null; renderBoard(); return; }
  // Fetch project detail to get member role
  try {
    const detail = await api(`/projects/${currentProject.id}`);
    const me = (detail.members || []).find(m => m.id === currentUser.id);
    currentMemberRole = me ? me.role : null;
  } catch { currentMemberRole = null; }
  cards = await api(`/projects/${currentProject.id}/cards`);
  document.getElementById('auto-approve-toggle').checked = !!currentProject.auto_approve;
  document.getElementById('auto-review-toggle').checked = !!currentProject.auto_review;
  // Only show toggles for owners/product managers
  const isManager = currentMemberRole === 'owner' || currentMemberRole === 'product_manager';
  document.getElementById('auto-approve-container').style.display = isManager ? 'flex' : 'none';
  document.getElementById('auto-review-container').style.display = isManager ? 'flex' : 'none';
  renderBoard();
  connectSSE();
}

function renderBoard() {
  board.innerHTML = '';
  if (!currentProject) {
    board.innerHTML = '<div style="padding:40px;color:var(--text-secondary)">Create a project to get started.</div>';
    return;
  }

  let columns = currentProject.columns_config || ['Backlog', 'Todo', 'In Progress', 'Review', 'Done'];

  // Reviewer role: only show Review and Done columns
  const isReviewer = currentMemberRole === 'reviewer';
  if (isReviewer) {
    columns = columns.filter(c => c === 'Review' || c === 'Done');
  }

  const wipLimits = currentProject.wip_limits || {};

  columns.forEach(col => {
    const colCards = cards.filter(c => c.column_name === col).sort((a, b) => a.position - b.position);
    const colEl = document.createElement('div');
    const wipLimit = wipLimits[col];
    let wipClass = '';
    let wipBadge = '';
    if (wipLimit) {
      const count = colCards.length;
      wipBadge = `<span class="wip-badge" title="WIP limit: ${wipLimit}">${count}/${wipLimit}</span>`;
      if (count > wipLimit) wipClass = ' column-wip-over';
      else if (count === wipLimit) wipClass = ' column-wip-at';
    }
    colEl.className = 'column' + wipClass;
    colEl.innerHTML = `
      <div class="column-header">
        <span class="column-title">${col}</span>
        ${wipBadge || `<span class="column-count">${colCards.length}</span>`}
      </div>
      <div class="column-cards" data-column="${col}"></div>
      ${isReviewer ? '' : `<button class="add-card-btn" data-column="${col}">+ Add card</button>`}
    `;

    const cardsContainer = colEl.querySelector('.column-cards');

    // Drag & drop (disabled for reviewers)
    if (isReviewer) {
      // No drag & drop for reviewers
    } else {
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
      } catch (err) { showToast(err.message); }
    });
    } // end else (not reviewer)

    colCards.forEach(card => {
      const cardEl = createCardElement(card);
      cardsContainer.appendChild(cardEl);
    });

    const addBtn = colEl.querySelector('.add-card-btn');
    if (addBtn) {
      addBtn.addEventListener('click', () => {
        document.getElementById('new-card-column').value = col;
        openModal('new-card-modal');
      });
    }

    board.appendChild(colEl);
  });
}

function createCardElement(card) {
  const el = document.createElement('div');
  el.className = 'card';
  el.draggable = currentMemberRole !== 'reviewer';
  el.dataset.id = card.id;

  el.addEventListener('dragstart', (e) => {
    e.dataTransfer.setData('text/plain', card.id);
    el.classList.add('dragging');
  });
  el.addEventListener('dragend', () => el.classList.remove('dragging'));
  el.addEventListener('click', () => openCardDetail(card));

  const labels = (card.labels || []).map(l => `<span class="card-label">${escHtml(l)}</span>`).join('');
  const hasGif = card.demo_gif_url ? '<span class="card-has-gif">GIF</span>' : '';
  const hasIllustration = card.illustration_url ? '<span class="card-has-illustration">UI</span>' : '';
  const effectiveApproval = (card.approval_status && card.approval_status !== 'none')
    ? card.approval_status
    : (card.column_name === 'Proposed' ? 'pending' : null);
  const approvalLabel = effectiveApproval === 'revision_requested' ? 'revise' : effectiveApproval;
  const approval = effectiveApproval
    ? `<span class="card-approval ${effectiveApproval}">${approvalLabel}</span>` : '';

  const assigneeName = card.assignee_display_name || card.assignee_username || '';
  const assigneeInitials = assigneeName ? assigneeName.split(' ').map(w => w[0]).join('').substring(0, 2).toUpperCase() : '';
  const assigneeBadge = assigneeInitials
    ? `<span class="card-assignee-badge" title="${escHtml(assigneeName)}">${escHtml(assigneeInitials)}</span>` : '';

  const archiveBtn = card.column_name === 'Done'
    ? `<button class="card-archive-btn" title="Archive card" data-card-id="${card.id}">&#x1f4e6;</button>` : '';

  const blockerCount = parseInt(card.blocker_count) || 0;
  const linkCount = parseInt(card.link_count) || 0;
  const blockerBadge = blockerCount > 0
    ? `<span class="card-blocker-icon" title="${blockerCount} blocker${blockerCount > 1 ? 's' : ''}">&#x1f512; ${blockerCount}</span>` : '';
  const linkBadge = (linkCount > 0 && blockerCount === 0)
    ? `<span class="card-link-icon" title="${linkCount} link${linkCount > 1 ? 's' : ''}">&#x1f517; ${linkCount}</span>` : '';

  let dueBadge = '';
  if (card.due_date && card.column_name !== 'Done') {
    const due = new Date(card.due_date + 'T00:00:00');
    const now = new Date(); now.setHours(0, 0, 0, 0);
    const daysLeft = Math.ceil((due - now) / (1000 * 60 * 60 * 24));
    const dateStr = due.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
    let dueClass = 'due-normal';
    if (daysLeft < 0) dueClass = 'due-overdue';
    else if (daysLeft <= 2) dueClass = 'due-soon';
    dueBadge = `<span class="card-due ${dueClass}" title="Due ${dateStr}">${dateStr}</span>`;
  }

  el.innerHTML = `
    <div class="card-top-row">
      <span class="card-type ${card.card_type}">${card.card_type}</span>
      <span class="card-priority ${card.priority}">${card.priority}</span>
      ${approval}
      ${archiveBtn}
    </div>
    <div class="card-title">${escHtml(card.title)}</div>
    <div class="card-meta">
      <span class="card-testing ${card.testing_status}">${card.testing_status}</span>
      ${hasGif}
      ${hasIllustration}
      ${blockerBadge}
      ${linkBadge}
      ${dueBadge}
      ${labels}
      ${assigneeBadge}
    </div>
  `;

  const archiveBtnEl = el.querySelector('.card-archive-btn');
  if (archiveBtnEl) {
    archiveBtnEl.addEventListener('click', async (e) => {
      e.stopPropagation();
      try {
        await api(`/projects/${currentProject.id}/cards/${card.id}/archive`, { method: 'POST' });
        await loadBoard();
        showToast('Card archived');
      } catch (err) { showToast(err.message); }
    });
  }

  return el;
}

// ── Filter Bar ──────────────────────────────────────────────────────────
const filterBar = document.getElementById('filter-bar');
const filterSearch = document.getElementById('filter-search');
const filterType = document.getElementById('filter-type');
const filterPriority = document.getElementById('filter-priority');
const filterLabel = document.getElementById('filter-label');
const filterClearBtn = document.getElementById('filter-clear-btn');
const filterCount = document.getElementById('filter-count');

document.getElementById('filter-toggle-btn').addEventListener('click', () => {
  const visible = filterBar.style.display !== 'none';
  filterBar.style.display = visible ? 'none' : 'flex';
  if (visible) clearFilters();
});

function getActiveFilters() {
  return {
    search: filterSearch.value.trim().toLowerCase(),
    type: filterType.value,
    priority: filterPriority.value,
    label: filterLabel.value.trim().toLowerCase(),
  };
}

function hasActiveFilters() {
  const f = getActiveFilters();
  return !!(f.search || f.type || f.priority || f.label);
}

function applyFilters() {
  const f = getActiveFilters();
  const active = hasActiveFilters();
  filterClearBtn.style.display = active ? 'inline-block' : 'none';

  let shown = 0;
  let total = 0;
  document.querySelectorAll('.column-cards .card').forEach(cardEl => {
    const card = cards.find(c => c.id === cardEl.dataset.id);
    if (!card) return;
    total++;
    let visible = true;
    if (f.search && !card.title.toLowerCase().includes(f.search) &&
        !(card.description || '').toLowerCase().includes(f.search)) visible = false;
    if (f.type && card.card_type !== f.type) visible = false;
    if (f.priority && card.priority !== f.priority) visible = false;
    if (f.label && !(card.labels || []).some(l => l.toLowerCase().includes(f.label))) visible = false;

    cardEl.style.display = visible ? '' : 'none';
    if (visible) shown++;
  });

  filterCount.textContent = active ? `${shown}/${total}` : '';
}

function clearFilters() {
  filterSearch.value = '';
  filterType.value = '';
  filterPriority.value = '';
  filterLabel.value = '';
  applyFilters();
}

filterSearch.addEventListener('input', applyFilters);
filterType.addEventListener('change', applyFilters);
filterPriority.addEventListener('change', applyFilters);
filterLabel.addEventListener('input', applyFilters);
filterClearBtn.addEventListener('click', clearFilters);

// ── New Card ─────────────────────────────────────────────────────────────

// AI-assisted card generation
document.getElementById('ai-generate-btn').addEventListener('click', async () => {
  const input = document.getElementById('ai-input').value.trim();
  if (!input || input.length < 5) {
    showToast('Please describe the card (at least 5 characters)');
    return;
  }
  const btn = document.getElementById('ai-generate-btn');
  const status = document.getElementById('ai-status');
  btn.disabled = true;
  btn.textContent = 'Generating...';
  status.style.display = 'block';
  status.textContent = 'Analyzing description...';
  status.className = 'ai-status';
  try {
    const result = await api(`/projects/${currentProject.id}/cards/ai-generate`, {
      method: 'POST',
      body: { description: input }
    });
    document.getElementById('new-card-title').value = result.title || '';
    document.getElementById('new-card-desc').value = result.description || '';
    document.getElementById('new-card-type').value = result.card_type || 'feature';
    document.getElementById('new-card-priority').value = result.priority || 'medium';
    document.getElementById('new-card-labels').value = (result.labels || []).join(', ');
    status.textContent = 'Card generated! Review and edit below, then click Create.';
    status.className = 'ai-status ai-success';
  } catch (err) {
    status.textContent = 'Generation failed: ' + (err.message || 'Unknown error');
    status.className = 'ai-status ai-error';
  }
  btn.disabled = false;
  btn.textContent = 'Generate Card';
});

document.getElementById('new-card-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const labelsStr = document.getElementById('new-card-labels').value.trim();
  const labels = labelsStr ? labelsStr.split(',').map(l => l.trim()).filter(Boolean) : [];
  await api(`/projects/${currentProject.id}/cards`, {
    method: 'POST',
    body: {
      title: document.getElementById('new-card-title').value,
      description: document.getElementById('new-card-desc').value,
      card_type: document.getElementById('new-card-type').value,
      priority: document.getElementById('new-card-priority').value,
      column_name: document.getElementById('new-card-column').value,
      labels
    }
  });
  closeAllModals();
  document.getElementById('new-card-title').value = '';
  document.getElementById('new-card-desc').value = '';
  document.getElementById('new-card-labels').value = '';
  document.getElementById('ai-input').value = '';
  document.getElementById('ai-status').style.display = 'none';
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
  document.getElementById('detail-due-date').value = card.due_date ? card.due_date.split('T')[0] : '';

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

  // Assignee dropdown
  const assigneeSelect = document.getElementById('detail-assignee');
  assigneeSelect.innerHTML = '<option value="">Unassigned</option>';
  try {
    const detail = await api(`/projects/${currentProject.id}`);
    (detail.members || []).forEach(m => {
      const opt = document.createElement('option');
      opt.value = m.id;
      opt.textContent = m.display_name || m.username;
      if (m.id === card.assignee_id) opt.selected = true;
      assigneeSelect.appendChild(opt);
    });
  } catch {}

  // Illustration preview (for Proposed cards)
  const illustrationSection = document.getElementById('detail-illustration-section');
  const illustrationPreview = document.getElementById('detail-illustration-preview');
  if (card.illustration_url) {
    illustrationSection.style.display = 'block';
    illustrationPreview.innerHTML = `<img src="${escHtml(card.illustration_url)}" alt="UI Illustration" class="illustration-img">`;
  } else {
    illustrationSection.style.display = 'none';
    illustrationPreview.innerHTML = '';
  }

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

  // Approval section — show for any card with a non-none approval status,
  // OR for any card in Proposed column (may need approval even if status is 'none')
  const approvalSection = document.getElementById('approval-section');
  const approvalStatus = document.getElementById('approval-status-display');
  const approvalActions = document.getElementById('approval-actions');
  const hasApproval = card.approval_status && card.approval_status !== 'none';
  const isProposed = card.column_name === 'Proposed';
  if (hasApproval || isProposed) {
    approvalSection.style.display = 'block';
    const displayStatus = hasApproval ? card.approval_status : 'pending';
    const displayLabel = displayStatus === 'revision_requested' ? 'revision requested' : displayStatus;
    approvalStatus.textContent = displayLabel;
    approvalStatus.className = `approval-badge approval-${displayStatus}`;
    // Show approve/reject buttons for owner/product_owner when not yet approved
    const canApprove = currentMemberRole === 'owner' || currentMemberRole === 'product_manager';
    approvalActions.style.display = canApprove && displayStatus !== 'approved' ? 'flex' : 'none';
  } else {
    approvalSection.style.display = 'none';
  }

  // Time tracking
  const timeSection = document.getElementById('time-tracking-section');
  const timeInfo = document.getElementById('time-tracking-info');
  if (card.started_at || card.completed_at) {
    timeSection.style.display = 'block';
    let html = '';
    if (card.started_at) {
      const started = new Date(card.started_at);
      const ago = formatTimeAgo(started);
      html += `<div class="time-row"><span class="time-label">Started</span><span class="time-value" title="${started.toLocaleString()}">${ago}</span></div>`;
    }
    if (card.completed_at) {
      const completed = new Date(card.completed_at);
      html += `<div class="time-row"><span class="time-label">Completed</span><span class="time-value" title="${completed.toLocaleString()}">${formatTimeAgo(completed)}</span></div>`;
    }
    if (card.started_at && card.completed_at) {
      const ms = new Date(card.completed_at) - new Date(card.started_at);
      html += `<div class="time-row cycle-time"><span class="time-label">Cycle time</span><span class="time-value">${formatDuration(ms)}</span></div>`;
    } else if (card.started_at && !card.completed_at) {
      const ms = Date.now() - new Date(card.started_at);
      html += `<div class="time-row in-progress-time"><span class="time-label">In progress</span><span class="time-value">${formatDuration(ms)}</span></div>`;
    }
    timeInfo.innerHTML = html;
  } else {
    timeSection.style.display = 'none';
  }

  // Token usage
  const tokenSection = document.getElementById('token-usage-section');
  const tokenInfo = document.getElementById('token-usage-info');
  if (card.token_usage > 0) {
    tokenSection.style.display = 'block';
    tokenInfo.innerHTML = `<div class="time-row token-row"><span class="time-label">Tokens used</span><span class="time-value">${card.token_usage.toLocaleString()}</span></div>`;
  } else {
    tokenSection.style.display = 'none';
  }

  // Show review actions (Done + Reject) only for cards in Review
  document.getElementById('review-actions').style.display = card.column_name === 'Review' ? 'flex' : 'none';

  // Load comments and links in parallel
  await Promise.all([loadComments(card.id), loadCardLinks(card.id)]);

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

// ── Card Links / Dependencies ────────────────────────────────────────

async function loadCardLinks(cardId) {
  const links = await api(`/projects/${currentProject.id}/cards/${cardId}/links`);
  const list = document.getElementById('detail-links');
  list.innerHTML = '';
  if (links.length === 0) {
    list.innerHTML = '<span style="color:var(--text-secondary);font-size:0.82rem">No dependencies</span>';
    return;
  }
  links.forEach(link => {
    const isSource = link.source_card_id === cardId;
    let typeLabel, typeClass, linkedTitle, linkedColumn, linkedCardId;
    if (link.link_type === 'blocks') {
      if (isSource) {
        typeLabel = 'blocks';
        typeClass = 'blocks';
        linkedTitle = link.target_title;
        linkedColumn = link.target_column;
        linkedCardId = link.target_card_id;
      } else {
        typeLabel = 'blocked by';
        typeClass = 'blocked-by';
        linkedTitle = link.source_title;
        linkedColumn = link.source_column;
        linkedCardId = link.source_card_id;
      }
    } else {
      typeLabel = 'related';
      typeClass = 'related';
      linkedTitle = isSource ? link.target_title : link.source_title;
      linkedColumn = isSource ? link.target_column : link.source_column;
      linkedCardId = isSource ? link.target_card_id : link.source_card_id;
    }
    const chip = document.createElement('div');
    chip.className = 'card-link-chip';
    chip.innerHTML = `
      <span class="link-type-badge ${typeClass}">${typeLabel}</span>
      <span class="link-card-title" data-card-id="${linkedCardId}">${escHtml(linkedTitle)}</span>
      <span class="link-column-badge">${escHtml(linkedColumn)}</span>
      <span class="link-remove" data-link-id="${link.id}" title="Remove link">&times;</span>
    `;
    // Click title to navigate to linked card
    chip.querySelector('.link-card-title').addEventListener('click', async () => {
      const cards = await api(`/projects/${currentProject.id}/cards`);
      const target = cards.find(c => c.id === linkedCardId);
      if (target) openCardDetail(target);
    });
    // Remove link
    chip.querySelector('.link-remove').addEventListener('click', async (e) => {
      e.stopPropagation();
      try {
        await api(`/projects/${currentProject.id}/cards/${cardId}/links/${link.id}`, { method: 'DELETE' });
        await loadCardLinks(cardId);
        showToast('Link removed');
      } catch (err) { showToast(err.message); }
    });
    list.appendChild(chip);
  });
}

// Link card search autocomplete
let linkSearchTimeout;
document.getElementById('link-card-search').addEventListener('input', (e) => {
  clearTimeout(linkSearchTimeout);
  const q = e.target.value.trim();
  if (q.length < 2) {
    document.getElementById('link-card-suggestions').innerHTML = '';
    return;
  }
  linkSearchTimeout = setTimeout(async () => {
    const cards = await api(`/projects/${currentProject.id}/cards`);
    const matches = cards.filter(c =>
      c.id !== currentCard.id && c.title.toLowerCase().includes(q.toLowerCase())
    ).slice(0, 8);
    const dropdown = document.getElementById('link-card-suggestions');
    dropdown.innerHTML = '';
    matches.forEach(c => {
      const item = document.createElement('div');
      item.className = 'autocomplete-item';
      item.textContent = `${c.title} (${c.column_name})`;
      item.addEventListener('click', () => {
        document.getElementById('link-card-search').value = c.title;
        document.getElementById('link-card-search').dataset.cardId = c.id;
        dropdown.innerHTML = '';
      });
      dropdown.appendChild(item);
    });
  }, 200);
});

document.getElementById('add-link-btn').addEventListener('click', async () => {
  if (!currentCard) return;
  const searchInput = document.getElementById('link-card-search');
  const targetId = searchInput.dataset.cardId;
  if (!targetId) { showToast('Select a card from the search results'); return; }
  const linkType = document.getElementById('link-type-select').value;
  try {
    await api(`/projects/${currentProject.id}/cards/${currentCard.id}/links`, {
      method: 'POST',
      body: { target_card_id: targetId, link_type: linkType }
    });
    searchInput.value = '';
    searchInput.dataset.cardId = '';
    document.getElementById('link-card-suggestions').innerHTML = '';
    await loadCardLinks(currentCard.id);
    showToast('Link added');
  } catch (err) { showToast(err.message); }
});

// Self-assign button
document.getElementById('self-assign-btn').addEventListener('click', () => {
  if (!currentUser) return;
  const sel = document.getElementById('detail-assignee');
  for (const opt of sel.options) {
    if (opt.value === currentUser.id) { sel.value = currentUser.id; return; }
  }
});

document.getElementById('clear-due-date-btn').addEventListener('click', () => {
  document.getElementById('detail-due-date').value = '';
});

// Save card
document.getElementById('save-card-btn').addEventListener('click', async () => {
  if (!currentCard) return;
  const labelsStr = document.getElementById('detail-labels').value;
  const labels = labelsStr ? labelsStr.split(',').map(s => s.trim()).filter(Boolean) : [];

  const assigneeVal = document.getElementById('detail-assignee').value;
  const updates = {
    title: document.getElementById('detail-title').value,
    description: document.getElementById('detail-description').value,
    testing_status: document.getElementById('detail-testing-status').value,
    testing_result: document.getElementById('detail-testing-result').value,
    priority: document.getElementById('detail-priority').value,
    card_type: document.getElementById('detail-card-type').value,
    column_name: document.getElementById('detail-column').value,
    assignee_id: assigneeVal || null,
    labels,
    due_date: document.getElementById('detail-due-date').value || null
  };

  try {
    await api(`/projects/${currentProject.id}/cards/${currentCard.id}`, { method: 'PATCH', body: updates });
    closeAllModals();
    await loadBoard();
  } catch (err) { showToast(err.message); }
});

// Delete card
document.getElementById('delete-card-btn').addEventListener('click', async () => {
  if (!currentCard) return;
  if (!confirm('Delete this card?')) return;
  await api(`/projects/${currentProject.id}/cards/${currentCard.id}`, { method: 'DELETE' });
  closeAllModals();
  await loadBoard();
});

// Approve card
document.getElementById('approve-card-btn').addEventListener('click', async () => {
  if (!currentCard) return;
  try {
    await api(`/projects/${currentProject.id}/cards/${currentCard.id}/approve`, {
      method: 'POST', body: { action: 'approve' }
    });
    showToast('Card approved', 'success');
    closeAllModals();
    await loadBoard();
  } catch (err) { showToast(err.message); }
});

// Proposal modal mode: 'reject' or 'revise'
let proposalModalMode = 'reject';

const reviseShortcuts = [
  { label: 'Unclear spec', reason: 'Description is unclear. Please rewrite with specific requirements and expected behavior.' },
  { label: 'Too large', reason: 'Scope is too large. Break this into smaller, focused cards.' },
  { label: 'Too complex', reason: 'This would introduce too much complexity. Propose a simpler approach.' },
  { label: 'Unclear illustration', reason: 'The proposed UI illustration is unclear or does not match the description. Please regenerate with a clearer layout and accurate labels.' },
  { label: 'Missing details', reason: 'Missing important implementation details. Add database schema, API endpoints, and UI behavior.' },
  { label: 'Wrong approach', reason: 'The proposed approach has issues. Please consider an alternative implementation.' },
];

const rejectShortcuts = [
  { label: 'Not needed', reason: 'This feature is not needed for the project.' },
  { label: 'Already exists', reason: 'This feature already exists or is covered by another card.' },
  { label: 'Wrong priority', reason: 'Not aligned with current project priorities.' },
  { label: 'Duplicate', reason: 'Duplicate of an existing card. Check the board before proposing.' },
  { label: 'Out of scope', reason: 'This is out of scope for this project.' },
];

function openProposalModal(mode) {
  proposalModalMode = mode;
  const isRevise = mode === 'revise';
  document.getElementById('reject-proposal-title').textContent = isRevise ? 'Request Revision' : 'Reject Proposal';
  document.getElementById('reject-proposal-help').textContent = isRevise
    ? 'The agent will modify the proposal based on your feedback and re-submit for approval.'
    : 'This proposal is not needed. The card will be permanently rejected.';
  document.getElementById('reject-proposal-comment-label').textContent = isRevise ? 'Revision Feedback' : 'Rejection Reason';
  document.getElementById('reject-proposal-reason').placeholder = isRevise
    ? 'What should the agent change or improve...'
    : 'Why this proposal is not needed...';

  // Default to illustration feedback if card has one and we're revising
  document.getElementById('reject-proposal-reason').value = (isRevise && currentCard?.illustration_url)
    ? 'The proposed UI illustration is unclear or does not match the description. Please regenerate with a clearer layout and accurate labels.'
    : '';

  const confirmBtn = document.getElementById('confirm-reject-proposal-btn');
  confirmBtn.textContent = isRevise ? 'Request Revision' : 'Reject Proposal';
  confirmBtn.className = isRevise ? 'btn btn-warning' : 'btn btn-danger';
  confirmBtn.style.width = '100%';
  confirmBtn.style.marginTop = '12px';

  // Build shortcuts
  const shortcuts = isRevise ? reviseShortcuts : rejectShortcuts;
  const container = document.getElementById('reject-proposal-shortcuts');
  container.innerHTML = '';
  shortcuts.forEach(s => {
    const btn = document.createElement('button');
    btn.className = 'btn btn-sm reject-proposal-shortcut';
    btn.textContent = s.label;
    btn.addEventListener('click', () => {
      document.getElementById('reject-proposal-reason').value = s.reason;
    });
    container.appendChild(btn);
  });

  openModal('reject-proposal-modal');
}

// Request revision
document.getElementById('revise-card-btn').addEventListener('click', () => {
  if (!currentCard) return;
  openProposalModal('revise');
});

// Reject card
document.getElementById('reject-card-btn').addEventListener('click', () => {
  if (!currentCard) return;
  openProposalModal('reject');
});

// Confirm proposal action (reject or revise)
document.getElementById('confirm-reject-proposal-btn').addEventListener('click', async () => {
  if (!currentCard) return;
  const comment = document.getElementById('reject-proposal-reason').value.trim();
  const action = proposalModalMode === 'revise' ? 'revise' : 'reject';
  try {
    await api(`/projects/${currentProject.id}/cards/${currentCard.id}/approve`, {
      method: 'POST', body: { action, comment }
    });
    showToast(action === 'revise' ? 'Revision requested' : 'Proposal rejected', 'success');
    closeAllModals();
    await loadBoard();
  } catch (err) { showToast(err.message); }
});

// Move to Done
document.getElementById('done-card-btn').addEventListener('click', async () => {
  if (!currentCard) return;
  try {
    await api(`/projects/${currentProject.id}/cards/${currentCard.id}/move`, {
      method: 'POST', body: { column_name: 'Done', position: 0 }
    });
    showToast('Card moved to Done', 'success');
    closeAllModals();
    await loadBoard();
  } catch (err) { showToast(err.message); }
});

// Reject review
document.getElementById('reject-review-btn').addEventListener('click', () => {
  document.getElementById('reject-reason').value = '';
  document.querySelectorAll('.reject-shortcut').forEach(b => b.classList.remove('active'));
  openModal('reject-review-modal');
});

document.querySelectorAll('.reject-shortcut').forEach(btn => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.reject-shortcut').forEach(b => b.classList.remove('active'));
    btn.classList.add('active');
    document.getElementById('reject-reason').value = btn.dataset.reason;
  });
});

document.getElementById('confirm-reject-btn').addEventListener('click', async () => {
  if (!currentCard) return;
  const reason = document.getElementById('reject-reason').value.trim();
  if (!reason) { showToast('Please provide a rejection reason'); return; }

  try {
    // Add rejection comment
    await api(`/projects/${currentProject.id}/cards/${currentCard.id}/comments`, {
      method: 'POST',
      body: { content: `Review rejected: ${reason}`, comment_type: 'agent_update' }
    });
    // Move back to In Progress
    await api(`/projects/${currentProject.id}/cards/${currentCard.id}/move`, {
      method: 'POST',
      body: { column_name: 'In Progress', position: 0 }
    });
    showToast('Card rejected and moved to In Progress', 'success');
    closeAllModals();
    await loadBoard();
  } catch (err) { showToast(err.message); }
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
document.getElementById('auto-approve-toggle').addEventListener('change', async (e) => {
  try {
    const updated = await api(`/projects/${currentProject.id}/settings`, {
      method: 'PATCH',
      body: { auto_approve: e.target.checked }
    });
    currentProject.auto_approve = updated.auto_approve;
    showToast(e.target.checked ? 'Auto-approve enabled' : 'Auto-approve disabled', 'success');
  } catch (err) {
    e.target.checked = !e.target.checked;
    showToast(err.message);
  }
});

document.getElementById('auto-review-toggle').addEventListener('change', async (e) => {
  try {
    const updated = await api(`/projects/${currentProject.id}/settings`, {
      method: 'PATCH',
      body: { auto_review: e.target.checked }
    });
    currentProject.auto_review = updated.auto_review;
    showToast(e.target.checked ? 'Auto-review enabled: agents can review cards' : 'Auto-review disabled', 'success');
  } catch (err) {
    e.target.checked = !e.target.checked;
    showToast(err.message);
  }
});

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
  await loadBugSettings();

  // Auto-archive days
  document.getElementById('auto-archive-days').value = projectData.auto_archive_days || 0;

  // Export column filter
  const exportColSelect = document.getElementById('export-column');
  exportColSelect.innerHTML = '<option value="">All columns</option>';
  (currentProject.columns_config || []).forEach(col => {
    const opt = document.createElement('option');
    opt.value = col;
    opt.textContent = col;
    exportColSelect.appendChild(opt);
  });

  // WIP Limits UI
  const wipSection = document.getElementById('wip-limits-section');
  const wipLimits = currentProject.wip_limits || {};
  const columns = currentProject.columns_config || [];
  wipSection.innerHTML = '';
  columns.forEach(col => {
    const row = document.createElement('div');
    row.className = 'wip-limit-row';
    row.innerHTML = `
      <span class="wip-limit-label">${escHtml(col)}</span>
      <input type="number" class="wip-limit-input" data-column="${escHtml(col)}" min="0" max="99" placeholder="No limit" value="${wipLimits[col] || ''}">
    `;
    wipSection.appendChild(row);
  });

  // Show danger zone only for product_manager, owner, or system admin
  const canDelete = currentMemberRole === 'owner' || currentMemberRole === 'product_manager' || currentUser?.system_role === 'admin';
  document.getElementById('danger-zone').style.display = canDelete ? 'block' : 'none';

  // Load project stats
  try {
    const stats = await api(`/projects/${currentProject.id}/stats`);
    const statsEl = document.getElementById('project-stats');
    let statsHtml = '<div class="stats-grid">';
    statsHtml += `<div class="stat-item"><span class="stat-value">${stats.completed_this_week}</span><span class="stat-label">Done this week</span></div>`;
    statsHtml += `<div class="stat-item"><span class="stat-value">${stats.completed_this_month}</span><span class="stat-label">Done this month</span></div>`;
    if (stats.token_usage && stats.token_usage.total > 0) {
      statsHtml += `<div class="stat-item token-stat"><span class="stat-value">${stats.token_usage.total.toLocaleString()}</span><span class="stat-label">Total tokens used</span></div>`;
      statsHtml += `<div class="stat-item token-stat"><span class="stat-value">${stats.token_usage.cards_with_tokens}</span><span class="stat-label">Cards using AI</span></div>`;
    }
    statsHtml += '</div>';
    if (stats.cycle_time && stats.cycle_time.length > 0) {
      statsHtml += '<div class="stats-detail"><strong>Avg cycle time:</strong> ';
      statsHtml += stats.cycle_time.map(ct => `${ct.card_type}: ${ct.avg_hours}h (${ct.count})`).join(', ');
      statsHtml += '</div>';
    }
    if (stats.token_usage && stats.token_usage.total > 0) {
      const byType = Object.entries(stats.token_usage.by_type).filter(([,v]) => v.tokens > 0);
      if (byType.length > 0) {
        statsHtml += '<div class="stats-detail"><strong>Tokens by type:</strong> ';
        statsHtml += byType.map(([type, v]) => `${type}: ${v.tokens.toLocaleString()} (${v.cards} cards)`).join(', ');
        statsHtml += '</div>';
      }
    }
    statsEl.innerHTML = statsHtml;
  } catch (e) {
    document.getElementById('project-stats').innerHTML = '<p class="help-text">Could not load stats.</p>';
  }

  openModal('settings-modal');
});

document.getElementById('add-member-btn').addEventListener('click', async () => {
  const username = document.getElementById('add-member-username').value.trim();
  if (!username) return;
  const role = document.getElementById('add-member-role').value;
  try {
    await api(`/projects/${currentProject.id}/members`, { method: 'POST', body: { username, role } });
    document.getElementById('add-member-username').value = '';
    document.getElementById('member-suggestions').innerHTML = '';
    document.getElementById('settings-btn').click();
  } catch (err) { alert(err.message); }
});

// Member username autocomplete
let memberSearchTimeout = null;
const memberInput = document.getElementById('add-member-username');
const memberSuggestions = document.getElementById('member-suggestions');

memberInput.addEventListener('input', () => {
  clearTimeout(memberSearchTimeout);
  const q = memberInput.value.trim();
  if (q.length < 1) { memberSuggestions.innerHTML = ''; return; }
  memberSearchTimeout = setTimeout(async () => {
    try {
      const users = await api(`/users/search?q=${encodeURIComponent(q)}`);
      memberSuggestions.innerHTML = '';
      users.forEach(u => {
        const item = document.createElement('div');
        item.className = 'autocomplete-item';
        item.textContent = u.display_name ? `${u.username} (${u.display_name})` : u.username;
        item.addEventListener('click', () => {
          memberInput.value = u.username;
          memberSuggestions.innerHTML = '';
        });
        memberSuggestions.appendChild(item);
      });
    } catch {}
  }, 200);
});

memberInput.addEventListener('blur', () => {
  setTimeout(() => { memberSuggestions.innerHTML = ''; }, 200);
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

// Delete project
document.getElementById('delete-project-btn').addEventListener('click', () => {
  if (!currentProject) return;
  document.getElementById('delete-confirm-name').textContent = currentProject.name;
  document.getElementById('delete-confirm-input').value = '';
  document.getElementById('delete-confirm-btn').disabled = true;
  document.getElementById('delete-confirm-box').style.display = 'block';
  document.getElementById('delete-project-btn').style.display = 'none';
});

document.getElementById('delete-confirm-input').addEventListener('input', (e) => {
  document.getElementById('delete-confirm-btn').disabled = e.target.value !== currentProject?.name;
});

document.getElementById('delete-cancel-btn').addEventListener('click', () => {
  document.getElementById('delete-confirm-box').style.display = 'none';
  document.getElementById('delete-project-btn').style.display = '';
});

document.getElementById('delete-confirm-btn').addEventListener('click', async () => {
  if (!currentProject || document.getElementById('delete-confirm-input').value !== currentProject.name) return;
  try {
    await api(`/projects/${currentProject.id}`, { method: 'DELETE' });
    showToast('Project deleted', 'success');
    closeAllModals();
    currentProject = null;
    currentMemberRole = null;
    await loadProjects();
  } catch (err) { showToast(err.message); }
});

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

// ── Toast Notifications ─────────────────────────────────────────────────
function showToast(message, type = 'error') {
  const toast = document.createElement('div');
  toast.className = `toast toast-${type}`;
  toast.textContent = message;
  document.body.appendChild(toast);
  requestAnimationFrame(() => toast.classList.add('show'));
  setTimeout(() => {
    toast.classList.remove('show');
    setTimeout(() => toast.remove(), 300);
  }, 5000);
}

// ── Utils ────────────────────────────────────────────────────────────────
function escHtml(str) {
  if (!str) return '';
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}

function formatTimeAgo(date) {
  const ms = Date.now() - date.getTime();
  const mins = Math.floor(ms / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  const days = Math.floor(hrs / 24);
  if (days < 30) return `${days}d ago`;
  return date.toLocaleDateString();
}

function formatDuration(ms) {
  const mins = Math.floor(ms / 60000);
  if (mins < 60) return `${mins}m`;
  const hrs = Math.floor(mins / 60);
  const remMins = mins % 60;
  if (hrs < 24) return remMins ? `${hrs}h ${remMins}m` : `${hrs}h`;
  const days = Math.floor(hrs / 24);
  const remHrs = hrs % 24;
  return remHrs ? `${days}d ${remHrs}h` : `${days}d`;
}

// ── SSE: Real-time board updates ─────────────────────────────────────────
let eventSource = null;

function connectSSE() {
  if (eventSource) { eventSource.close(); eventSource = null; }
  if (!token || !currentProject) return;

  eventSource = new EventSource(`/api/projects/${currentProject.id}/events?token=${token}`);

  let reloadTimer = null;
  function debouncedReload() {
    if (reloadTimer) clearTimeout(reloadTimer);
    reloadTimer = setTimeout(async () => {
      await loadBoard();
      await refreshCounts();
      renderProjectDropdown();
    }, 300);
  }

  eventSource.addEventListener('card_created', debouncedReload);
  eventSource.addEventListener('card_updated', debouncedReload);
  eventSource.addEventListener('card_moved', debouncedReload);
  eventSource.addEventListener('card_deleted', debouncedReload);
  eventSource.addEventListener('card_archived', debouncedReload);
  eventSource.addEventListener('card_unarchived', debouncedReload);
  eventSource.addEventListener('card_created', (e) => {
    try {
      const data = JSON.parse(e.data);
      if (data.user !== currentUser?.username) {
        addNotification('New card', `${data.user} created "${data.card?.title}"`, data.card?.id);
      }
    } catch {}
  });

  eventSource.addEventListener('card_moved', (e) => {
    try {
      const data = JSON.parse(e.data);
      if (data.user !== currentUser?.username) {
        addNotification(data.card?.title, `Moved to ${data.card?.column_name} by ${data.user}`, data.card?.id);
      }
    } catch {}
  });

  eventSource.addEventListener('comment_added', (e) => {
    try {
      const data = JSON.parse(e.data);
      if (data.user !== currentUser?.username) {
        addNotification('New comment', `${data.user}: ${(data.comment?.content || '').substring(0, 60)}`, data.comment?.card_id);
      }
    } catch {}
    // Refresh comments if the card detail modal is open
    if (currentCard) {
      const data = JSON.parse(e.data);
      if (data.comment?.card_id === currentCard.id) {
        loadComments(currentCard.id);
      }
    }
  });

  eventSource.onerror = () => {
    // Auto-reconnect is built into EventSource, but close on auth failure
    if (!token) { eventSource.close(); eventSource = null; }
  };
}

// ── Lightbox (click to expand demo media) ────────────────────────────────
document.getElementById('detail-gif-preview').addEventListener('click', (e) => {
  const el = e.target;
  if (el.tagName !== 'IMG' && el.tagName !== 'VIDEO') return;
  e.stopPropagation();
  const overlay = document.createElement('div');
  overlay.className = 'lightbox';
  if (el.tagName === 'VIDEO') {
    overlay.innerHTML = `<video src="${el.src}" controls autoplay loop muted></video>`;
  } else {
    overlay.innerHTML = `<img src="${el.src}" alt="Demo">`;
  }
  overlay.addEventListener('click', () => overlay.remove());
  document.body.appendChild(overlay);
});

// Lightbox for illustration previews
document.getElementById('detail-illustration-preview').addEventListener('click', (e) => {
  const el = e.target;
  if (el.tagName !== 'IMG') return;
  e.stopPropagation();
  const overlay = document.createElement('div');
  overlay.className = 'lightbox';
  overlay.innerHTML = `<img src="${el.src}" alt="UI Illustration">`;
  overlay.addEventListener('click', () => overlay.remove());
  document.body.appendChild(overlay);
});

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    const lb = document.querySelector('.lightbox');
    if (lb) lb.remove();
  }
});

// ── Notifications ────────────────────────────────────────────────────────
let notifications = [];
let lastNotifCheck = new Date().toISOString();
const notifBtn = document.getElementById('notif-btn');
const notifBadge = document.getElementById('notif-badge');
const notifDropdown = document.getElementById('notif-dropdown');

function renderNotifBadge() {
  if (notifications.length > 0) {
    notifBadge.textContent = notifications.length > 99 ? '99+' : notifications.length;
    notifBadge.style.display = 'flex';
  } else {
    notifBadge.style.display = 'none';
  }
}

function renderNotifDropdown() {
  let html = `<div class="notif-header"><span>Notifications</span>${notifications.length ? '<button class="notif-clear" id="notif-clear-btn">Clear all</button>' : ''}</div>`;
  if (notifications.length === 0) {
    html += '<div class="notif-empty">No new notifications</div>';
  } else {
    notifications.forEach(n => {
      const time = new Date(n.timestamp).toLocaleTimeString();
      html += `<div class="notif-item" data-card-id="${escHtml(n.cardId || '')}">
        <div class="notif-item-title">${escHtml(n.title)}</div>
        <div class="notif-item-detail">${escHtml(n.detail)}</div>
        <div class="notif-item-time">${time}</div>
      </div>`;
    });
  }
  notifDropdown.innerHTML = html;

  const clearBtn = document.getElementById('notif-clear-btn');
  if (clearBtn) {
    clearBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      notifications = [];
      renderNotifBadge();
      renderNotifDropdown();
    });
  }

  notifDropdown.querySelectorAll('.notif-item[data-card-id]').forEach(el => {
    el.addEventListener('click', () => {
      const cardId = el.dataset.cardId;
      const card = cards.find(c => c.id === cardId);
      if (card) { notifDropdown.classList.remove('open'); openCardDetail(card); }
    });
  });
}

function addNotification(title, detail, cardId) {
  notifications.unshift({ title, detail, cardId, timestamp: new Date().toISOString() });
  if (notifications.length > 50) notifications.pop();
  renderNotifBadge();
  if (notifDropdown.classList.contains('open')) renderNotifDropdown();
}

notifBtn.addEventListener('click', (e) => {
  e.stopPropagation();
  notifDropdown.classList.toggle('open');
  if (notifDropdown.classList.contains('open')) renderNotifDropdown();
});

document.addEventListener('click', () => {
  notifDropdown.classList.remove('open');
});

async function checkActivity() {
  if (!currentProject) return;
  try {
    const data = await api(`/projects/${currentProject.id}/activity?since=${encodeURIComponent(lastNotifCheck)}&limit=20`);
    lastNotifCheck = new Date().toISOString();
    (data.events || []).forEach(ev => {
      if (ev.event_type === 'card_updated') {
        addNotification(ev.title, `${ev.card_type} moved to ${ev.column_name}`, ev.id);
      } else if (ev.event_type === 'comment_added') {
        addNotification(ev.card_title, `${ev.display_name || ev.username}: ${(ev.content || '').substring(0, 60)}`, ev.card_id);
      }
    });
  } catch {}
}

// ── Theme Toggle ─────────────────────────────────────────────────────────
function applyTheme(theme) {
  document.documentElement.setAttribute('data-theme', theme);
  localStorage.setItem('duckllo_theme', theme);
  document.getElementById('theme-toggle-btn').textContent = theme === 'dark' ? 'Light' : 'Dark';
}

document.getElementById('theme-toggle-btn').addEventListener('click', () => {
  const current = localStorage.getItem('duckllo_theme') || 'dark';
  applyTheme(current === 'dark' ? 'light' : 'dark');
});

// Apply saved theme on load
applyTheme(localStorage.getItem('duckllo_theme') || 'dark');

// ── Activity Panel ───────────────────────────────────────────────────────
const activityPanel = document.getElementById('activity-panel');
const activityList = document.getElementById('activity-list');
const activityFilter = document.getElementById('activity-filter');
const activityLoadMore = document.getElementById('activity-load-more');
let activityEvents = [];
let activityOldest = null;

document.getElementById('activity-toggle-btn').addEventListener('click', () => {
  const open = activityPanel.classList.toggle('open');
  if (open) loadActivity(true);
});

document.getElementById('activity-close-btn').addEventListener('click', () => {
  activityPanel.classList.remove('open');
});

activityFilter.addEventListener('change', () => renderActivity());

activityLoadMore.addEventListener('click', () => loadActivity(false));

async function loadActivity(reset) {
  if (!currentProject) return;
  if (reset) { activityEvents = []; activityOldest = null; }

  const since = activityOldest || new Date(Date.now() - 7 * 24 * 60 * 60 * 1000).toISOString();
  try {
    const data = await api(`/projects/${currentProject.id}/activity?since=${encodeURIComponent(since)}&limit=30`);
    const newEvents = (data.events || []).filter(e => !activityEvents.find(a => a.id === e.id && a.event_type === e.event_type));
    activityEvents = reset ? newEvents : [...activityEvents, ...newEvents];
    // Sort newest first
    activityEvents.sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp));
    if (activityEvents.length > 0) {
      activityOldest = activityEvents[activityEvents.length - 1].timestamp;
    }
    activityLoadMore.style.display = newEvents.length >= 30 ? 'block' : 'none';
    renderActivity();
  } catch {}
}

function renderActivity() {
  const filter = activityFilter.value;
  const filtered = filter === 'all' ? activityEvents : activityEvents.filter(e => e.event_type === filter);

  if (filtered.length === 0) {
    activityList.innerHTML = '<div class="activity-empty">No activity yet</div>';
    return;
  }

  activityList.innerHTML = filtered.map(e => {
    const time = new Date(e.timestamp).toLocaleString();
    const timeShort = new Date(e.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    const dateStr = new Date(e.timestamp).toLocaleDateString();

    if (e.event_type === 'card_updated') {
      const user = e.creator_username || 'Unknown';
      return `<div class="activity-item" data-card-id="${e.id}">
        <div class="activity-icon activity-icon-move">&#9654;</div>
        <div class="activity-body">
          <div class="activity-text"><strong>${escHtml(user)}</strong> moved <strong>${escHtml(e.title)}</strong> to <span class="activity-col">${escHtml(e.column_name)}</span></div>
          <div class="activity-time">${dateStr} ${timeShort}</div>
        </div>
      </div>`;
    } else if (e.event_type === 'comment_added') {
      const user = e.display_name || e.username || 'Unknown';
      const preview = (e.content || '').substring(0, 80);
      return `<div class="activity-item" data-card-id="${e.card_id}">
        <div class="activity-icon activity-icon-comment">&#128172;</div>
        <div class="activity-body">
          <div class="activity-text"><strong>${escHtml(user)}</strong> on <strong>${escHtml(e.card_title || '')}</strong></div>
          <div class="activity-preview">${escHtml(preview)}${(e.content || '').length > 80 ? '...' : ''}</div>
          <div class="activity-time">${dateStr} ${timeShort}</div>
        </div>
      </div>`;
    }
    return '';
  }).join('');

  // Click to open card
  activityList.querySelectorAll('.activity-item[data-card-id]').forEach(el => {
    el.addEventListener('click', () => {
      const cardId = el.dataset.cardId;
      const card = cards.find(c => c.id === cardId);
      if (card) openCardDetail(card);
    });
  });
}

// ── Archived Cards Panel ─────────────────────────────────────────────────
const archivedPanel = document.getElementById('archived-panel');
const archivedList = document.getElementById('archived-list');
const archivedCount = document.getElementById('archived-count');
const archivedLoadMore = document.getElementById('archived-load-more');
let archivedPage = 1;

document.getElementById('archived-toggle-btn').addEventListener('click', () => {
  const open = archivedPanel.style.display === 'none';
  archivedPanel.style.display = open ? '' : 'none';
  archivedPanel.classList.toggle('open', open);
  if (open) { archivedPage = 1; loadArchived(true); }
});

document.getElementById('archived-close-btn').addEventListener('click', () => {
  archivedPanel.style.display = 'none';
  archivedPanel.classList.remove('open');
});

archivedLoadMore.addEventListener('click', () => loadArchived(false));

async function loadArchived(reset) {
  if (!currentProject) return;
  if (reset) { archivedPage = 1; archivedList.innerHTML = ''; }

  try {
    const data = await api(`/projects/${currentProject.id}/cards/archived?page=${archivedPage}&limit=20`);
    archivedCount.textContent = `${data.total} card${data.total !== 1 ? 's' : ''}`;
    archivedLoadMore.style.display = archivedPage < data.total_pages ? 'block' : 'none';

    if (data.cards.length === 0 && reset) {
      archivedList.innerHTML = '<div class="activity-empty">No archived cards</div>';
      return;
    }

    data.cards.forEach(card => {
      const archivedDate = new Date(card.archived_at).toLocaleDateString();
      const el = document.createElement('div');
      el.className = 'archived-item';
      el.innerHTML = `
        <div class="archived-item-top">
          <span class="card-type ${card.card_type}">${card.card_type}</span>
          <span class="card-priority ${card.priority}">${card.priority}</span>
          <span class="archived-date">${archivedDate}</span>
        </div>
        <div class="archived-item-title">${escHtml(card.title)}</div>
        <button class="btn btn-sm btn-outline unarchive-btn" data-card-id="${card.id}">Unarchive</button>
      `;
      el.querySelector('.unarchive-btn').addEventListener('click', async (e) => {
        e.stopPropagation();
        try {
          await api(`/projects/${currentProject.id}/cards/${card.id}/unarchive`, { method: 'POST' });
          el.remove();
          await loadBoard();
          showToast('Card restored to Done');
          const remaining = archivedList.querySelectorAll('.archived-item').length;
          if (remaining === 0) loadArchived(true);
        } catch (err) { showToast(err.message); }
      });
      archivedList.appendChild(el);
    });

    archivedPage++;
  } catch (err) { showToast('Failed to load archived cards'); }
}

// Refresh activity when SSE events arrive (if panel is open)
const origConnectSSE = connectSSE;

// ── Bug Reports (Settings Panel) ──────────────────────────────────────────

// Copy bug report link
document.getElementById('bug-report-link-btn').addEventListener('click', () => {
  if (!currentProject) { showToast('No project selected'); return; }
  const url = `${window.location.origin}/bugs.html?project=${currentProject.id}`;
  navigator.clipboard.writeText(url).then(() => {
    showToast('Bug report link copied to clipboard!', 'success');
  }).catch(() => {
    prompt('Bug report link:', url);
  });
});

// Bug settings + list in settings modal
async function loadBugSettings() {
  if (!currentProject) return;
  const isOwner = currentMemberRole === 'owner' || currentMemberRole === 'product_manager';
  const section = document.getElementById('bug-settings-section');
  section.style.display = isOwner ? 'block' : 'none';

  if (isOwner) {
    const settings = currentProject.bug_report_settings || { submit_permission: 'member', view_permission: 'member' };
    document.getElementById('bug-submit-perm').value = settings.submit_permission;
    document.getElementById('bug-view-perm').value = settings.view_permission;
  }

  // Load bug reports
  try {
    const bugs = await api(`/projects/${currentProject.id}/bugs`);
    const list = document.getElementById('bug-reports-list');
    if (bugs.length === 0) {
      list.innerHTML = '<p class="help-text">No bug reports yet.</p>';
      return;
    }
    list.innerHTML = bugs.map(b => `
      <div class="bug-row" data-bug-id="${b.id}">
        <div class="bug-row-main">
          <span class="bug-severity-dot ${b.severity}"></span>
          <span class="bug-row-title">${escHtml(b.title)}</span>
          ${b.is_security_issue ? '<span class="bug-security-badge">SEC</span>' : ''}
        </div>
        <div class="bug-row-meta">
          <span class="bug-status-badge ${b.status}">${b.status}</span>
          <span>${escHtml(b.reporter_name || 'Anonymous')}</span>
          <span>${new Date(b.created_at).toLocaleDateString()}</span>
        </div>
      </div>
    `).join('');

    list.querySelectorAll('.bug-row').forEach(row => {
      row.addEventListener('click', () => openBugDetail(row.dataset.bugId));
    });
  } catch (err) {
    document.getElementById('bug-reports-list').innerHTML = `<p class="error-msg">${escHtml(err.message)}</p>`;
  }
}

document.getElementById('save-bug-perms-btn').addEventListener('click', async () => {
  try {
    const updated = await api(`/projects/${currentProject.id}/settings`, {
      method: 'PATCH',
      body: {
        bug_report_settings: {
          submit_permission: document.getElementById('bug-submit-perm').value,
          view_permission: document.getElementById('bug-view-perm').value,
        }
      }
    });
    currentProject.bug_report_settings = updated.bug_report_settings;
    showToast('Bug report permissions saved', 'success');
  } catch (err) { showToast(err.message); }
});

document.getElementById('export-btn').addEventListener('click', async () => {
  if (!currentProject) return;
  const format = document.getElementById('export-format').value;
  const column = document.getElementById('export-column').value;
  let url = `/api/projects/${currentProject.id}/export?format=${format}`;
  if (column) url += `&column=${encodeURIComponent(column)}`;

  try {
    const token = localStorage.getItem('duckllo_token');
    const resp = await fetch(url, { headers: { 'Authorization': `Bearer ${token}` } });
    if (!resp.ok) throw new Error((await resp.json()).error);
    const blob = await resp.blob();
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `${currentProject.name.replace(/[^a-zA-Z0-9]/g, '_')}_export.${format}`;
    a.click();
    URL.revokeObjectURL(a.href);
    showToast('Export downloaded', 'success');
  } catch (err) { showToast(err.message); }
});

document.getElementById('save-wip-limits-btn').addEventListener('click', async () => {
  try {
    const limits = {};
    document.querySelectorAll('.wip-limit-input').forEach(input => {
      const col = input.dataset.column;
      const val = parseInt(input.value);
      if (val > 0) limits[col] = val;
    });
    const updated = await api(`/projects/${currentProject.id}/settings`, {
      method: 'PATCH',
      body: { wip_limits: limits }
    });
    currentProject.wip_limits = updated.wip_limits;
    await loadBoard();
    showToast('WIP limits saved', 'success');
  } catch (err) { showToast(err.message); }
});

document.getElementById('save-auto-archive-btn').addEventListener('click', async () => {
  try {
    const days = parseInt(document.getElementById('auto-archive-days').value) || 0;
    const updated = await api(`/projects/${currentProject.id}/settings`, {
      method: 'PATCH',
      body: { auto_archive_days: days }
    });
    currentProject.auto_archive_days = updated.auto_archive_days;
    showToast(days > 0 ? `Auto-archive set to ${days} days` : 'Auto-archive disabled', 'success');
  } catch (err) { showToast(err.message); }
});

let currentBug = null;

async function openBugDetail(bugId) {
  try {
    const bug = await api(`/projects/${currentProject.id}/bugs/${bugId}`);
    currentBug = bug;

    document.getElementById('bug-detail-title').textContent = bug.title;
    document.getElementById('bug-detail-severity').textContent = bug.severity;
    document.getElementById('bug-detail-severity').className = `bug-severity ${bug.severity}`;
    document.getElementById('bug-detail-status').textContent = bug.status;
    document.getElementById('bug-detail-status').className = `bug-status ${bug.status}`;
    document.getElementById('bug-detail-security').style.display = bug.is_security_issue ? 'inline' : 'none';
    document.getElementById('bug-detail-reporter').textContent = `by ${bug.reporter_name || 'Anonymous'}`;
    document.getElementById('bug-detail-date').textContent = new Date(bug.created_at).toLocaleString();

    // Sections
    const sections = [
      ['description', bug.description],
      ['steps', bug.steps_to_reproduce],
      ['expected', bug.expected_behavior],
      ['actual', bug.actual_behavior],
      ['error', bug.error_message],
      ['browser', bug.browser_info],
      ['url', bug.url_location],
    ];
    sections.forEach(([key, val]) => {
      const sec = document.getElementById(`bug-detail-${key}-section`);
      const pre = document.getElementById(`bug-detail-${key}`);
      if (val) { sec.style.display = 'block'; pre.textContent = val; }
      else { sec.style.display = 'none'; }
    });

    // Screenshot
    const screenshotSec = document.getElementById('bug-detail-screenshot-section');
    const screenshotEl = document.getElementById('bug-detail-screenshot');
    if (bug.screenshot_url) {
      screenshotSec.style.display = 'block';
      screenshotEl.innerHTML = `<img src="${escHtml(bug.screenshot_url)}" alt="Screenshot" class="bug-screenshot-img">`;
    } else {
      screenshotSec.style.display = 'none';
    }

    document.getElementById('bug-detail-status-select').value = bug.status;
    openModal('bug-detail-modal');
  } catch (err) { showToast(err.message); }
}

document.getElementById('bug-update-status-btn').addEventListener('click', async () => {
  if (!currentBug) return;
  try {
    await api(`/projects/${currentProject.id}/bugs/${currentBug.id}`, {
      method: 'PATCH',
      body: { status: document.getElementById('bug-detail-status-select').value }
    });
    showToast('Bug status updated', 'success');
    closeAllModals();
    document.getElementById('settings-btn').click();
  } catch (err) { showToast(err.message); }
});

document.getElementById('bug-create-card-btn').addEventListener('click', async () => {
  if (!currentBug) return;
  try {
    const card = await api(`/projects/${currentProject.id}/cards`, {
      method: 'POST',
      body: {
        title: `[Bug] ${currentBug.title}`,
        description: [
          currentBug.description,
          currentBug.steps_to_reproduce ? `\n**Steps to Reproduce:**\n${currentBug.steps_to_reproduce}` : '',
          currentBug.expected_behavior ? `\n**Expected:** ${currentBug.expected_behavior}` : '',
          currentBug.actual_behavior ? `\n**Actual:** ${currentBug.actual_behavior}` : '',
          currentBug.error_message ? `\n**Error:**\n${currentBug.error_message}` : '',
        ].filter(Boolean).join('\n'),
        card_type: 'bug',
        priority: currentBug.severity,
        column_name: 'Backlog',
        labels: ['bug-report']
      }
    });
    // Link bug to card
    await api(`/projects/${currentProject.id}/bugs/${currentBug.id}`, {
      method: 'PATCH',
      body: { linked_card_id: card.id, status: 'triaged' }
    });
    showToast('Card created and bug linked', 'success');
    closeAllModals();
    await loadBoard();
  } catch (err) { showToast(err.message); }
});

// ── Admin User Management ────────────────────────────────────────────────

document.getElementById('admin-btn').addEventListener('click', async () => {
  await loadAdminUsers();
  openModal('admin-modal');
});

async function loadAdminUsers() {
  try {
    const users = await api('/admin/users');
    const list = document.getElementById('admin-users-list');
    list.innerHTML = '';

    users.forEach(u => {
      const row = document.createElement('div');
      row.className = `admin-user-row${u.disabled ? ' admin-user-disabled' : ''}`;
      const createdDate = new Date(u.created_at).toLocaleDateString();
      const isSelf = u.id === currentUser.id;

      row.innerHTML = `
        <div class="admin-user-info">
          <div class="admin-user-name">
            <strong>${escHtml(u.display_name || u.username)}</strong>
            <span class="admin-user-username">@${escHtml(u.username)}</span>
            ${u.disabled ? '<span class="admin-badge admin-badge-disabled">disabled</span>' : ''}
            ${isSelf ? '<span class="admin-badge admin-badge-you">you</span>' : ''}
          </div>
          <div class="admin-user-meta">
            Joined ${createdDate} &middot; ${u.project_count} project${u.project_count != 1 ? 's' : ''} &middot; ${u.active_sessions} session${u.active_sessions != 1 ? 's' : ''}
          </div>
        </div>
        <div class="admin-user-actions">
          <select class="admin-role-select" data-user-id="${u.id}" ${isSelf ? 'disabled' : ''}>
            <option value="user" ${u.system_role === 'user' ? 'selected' : ''}>User</option>
            <option value="agent" ${u.system_role === 'agent' ? 'selected' : ''}>Agent</option>
            <option value="admin" ${u.system_role === 'admin' ? 'selected' : ''}>Admin</option>
          </select>
          ${!isSelf ? `
            <button class="btn btn-sm ${u.disabled ? 'btn-primary' : 'btn-warning'} admin-toggle-btn" data-user-id="${u.id}" data-disabled="${u.disabled}">
              ${u.disabled ? 'Enable' : 'Disable'}
            </button>
            <button class="btn btn-sm btn-danger admin-delete-btn" data-user-id="${u.id}" data-username="${escHtml(u.username)}">Delete</button>
          ` : ''}
        </div>
      `;
      list.appendChild(row);
    });

    // Role change handlers
    list.querySelectorAll('.admin-role-select').forEach(sel => {
      sel.addEventListener('change', async () => {
        try {
          await api(`/admin/users/${sel.dataset.userId}`, { method: 'PATCH', body: { system_role: sel.value } });
          showToast('Role updated');
        } catch (err) { showToast(err.message); await loadAdminUsers(); }
      });
    });

    // Toggle disable handlers
    list.querySelectorAll('.admin-toggle-btn').forEach(btn => {
      btn.addEventListener('click', async () => {
        const newState = btn.dataset.disabled === 'false';
        try {
          await api(`/admin/users/${btn.dataset.userId}`, { method: 'PATCH', body: { disabled: newState } });
          showToast(newState ? 'Account disabled' : 'Account enabled');
          await loadAdminUsers();
        } catch (err) { showToast(err.message); }
      });
    });

    // Delete handlers
    list.querySelectorAll('.admin-delete-btn').forEach(btn => {
      btn.addEventListener('click', async () => {
        const username = btn.dataset.username;
        if (!confirm(`Delete user "${username}"? This cannot be undone.`)) return;
        try {
          await api(`/admin/users/${btn.dataset.userId}`, { method: 'DELETE' });
          showToast('User deleted');
          await loadAdminUsers();
        } catch (err) { showToast(err.message); }
      });
    });
  } catch (err) { showToast(err.message); }
}

// ── Keyboard Shortcuts ──────────────────────────────────────────────────

document.addEventListener('keydown', (e) => {
  // Skip if typing in an input/textarea/select
  const tag = e.target.tagName;
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') {
    // Escape still works in inputs
    if (e.key === 'Escape') { e.target.blur(); return; }
    return;
  }

  // Never override browser shortcuts (Ctrl+R, Ctrl+T, Ctrl+W, etc.)
  // Only allow Ctrl+Enter (save card) as a custom combo
  if ((e.ctrlKey || e.metaKey || e.altKey) && !(e.key === 'Enter' && (e.ctrlKey || e.metaKey))) {
    return;
  }

  const cardModalOpen = document.getElementById('card-modal').style.display !== 'none';
  const shortcutsOpen = document.getElementById('shortcuts-modal').style.display !== 'none';

  // ? — show shortcuts help (works everywhere)
  if (e.key === '?' && !e.ctrlKey && !e.metaKey) {
    e.preventDefault();
    if (shortcutsOpen) { closeAllModals(); } else { openModal('shortcuts-modal'); }
    return;
  }

  // Escape — close any modal
  if (e.key === 'Escape') {
    closeAllModals();
    return;
  }

  if (cardModalOpen && currentCard) {
    // ── Card detail shortcuts ──
    if (e.key === 'j' || e.key === 'k') {
      // Navigate to next/prev card in same column
      e.preventDefault();
      const colCards = cards.filter(c => c.column_name === currentCard.column_name).sort((a, b) => a.position - b.position);
      const idx = colCards.findIndex(c => c.id === currentCard.id);
      const nextIdx = e.key === 'j' ? idx + 1 : idx - 1;
      if (nextIdx >= 0 && nextIdx < colCards.length) {
        openCardDetail(colCards[nextIdx]);
      }
      return;
    }
    if (e.key === 'e') {
      e.preventDefault();
      document.getElementById('detail-description')?.focus();
      return;
    }
    if (e.key === 'm') {
      e.preventDefault();
      // Assign to current user
      const assigneeSelect = document.getElementById('detail-assignee');
      if (assigneeSelect && currentUser) {
        const myOption = Array.from(assigneeSelect.options).find(o => o.textContent.includes(currentUser.username));
        if (myOption) { assigneeSelect.value = myOption.value; }
      }
      return;
    }
    if (e.key === 'l') {
      e.preventDefault();
      document.getElementById('detail-labels')?.focus();
      return;
    }
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      document.getElementById('save-card-btn')?.click();
      return;
    }
    return;
  }

  // ── Board-level shortcuts (no modal open) ──
  if (e.key === 'n') {
    e.preventDefault();
    openModal('new-card-modal');
    return;
  }
  if (e.key === 'f') {
    e.preventDefault();
    document.getElementById('filter-toggle-btn')?.click();
    return;
  }
  if (e.key === 'a') {
    e.preventDefault();
    document.getElementById('activity-toggle-btn')?.click();
    return;
  }
  if (e.key === 'r') {
    e.preventDefault();
    document.getElementById('archived-toggle-btn')?.click();
    return;
  }
  if (e.key === '/') {
    e.preventDefault();
    // Open filter bar and focus search
    const filterBar = document.getElementById('filter-bar');
    if (filterBar.style.display === 'none') {
      document.getElementById('filter-toggle-btn')?.click();
    }
    setTimeout(() => document.getElementById('filter-search')?.focus(), 100);
    return;
  }
  // 1-6: jump to column
  const num = parseInt(e.key);
  if (num >= 1 && num <= 9) {
    e.preventDefault();
    const columns = document.querySelectorAll('.column');
    if (columns[num - 1]) {
      columns[num - 1].scrollIntoView({ behavior: 'smooth', inline: 'center' });
    }
    return;
  }
});

// ── Boot ─────────────────────────────────────────────────────────────────
init();
