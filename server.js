const express = require('express');
const { Pool } = require('pg');
const bcrypt = require('bcryptjs');
const { v4: uuidv4 } = require('uuid');
const multer = require('multer');
const path = require('path');
const fs = require('fs');
const Anthropic = require('@anthropic-ai/sdk');

const app = express();
const PORT = process.env.PORT || 3000;

// Ensure uploads directory exists
const uploadsDir = path.join(__dirname, 'uploads');
if (!fs.existsSync(uploadsDir)) fs.mkdirSync(uploadsDir);

// Multer config for GIF uploads
const storage = multer.diskStorage({
  destination: (req, file, cb) => cb(null, uploadsDir),
  filename: (req, file, cb) => {
    const ext = path.extname(file.originalname);
    cb(null, `${uuidv4()}${ext}`);
  }
});
const upload = multer({
  storage,
  limits: { fileSize: 50 * 1024 * 1024 }, // 50MB
  fileFilter: (req, file, cb) => {
    const allowed = ['.gif', '.png', '.jpg', '.jpeg', '.webp', '.mp4'];
    const ext = path.extname(file.originalname).toLowerCase();
    cb(null, allowed.includes(ext));
  }
});

app.use(express.json());
app.use((err, req, res, next) => {
  if (err.type === 'entity.parse.failed') {
    return res.status(400).json({ error: 'Invalid JSON in request body' });
  }
  next(err);
});
app.use(express.static(path.join(__dirname, 'public')));
app.use('/uploads', express.static(uploadsDir));

// ── Database Setup ──────────────────────────────────────────────────────

const pool = new Pool({
  host: process.env.DB_HOST || 'localhost',
  port: parseInt(process.env.DB_PORT) || 5432,
  database: process.env.DB_NAME || 'duckllo',
  user: process.env.DB_USER || 'duckllo',
  password: process.env.DB_PASSWORD || 'duckllo',
});

async function initDB() {
  await pool.query(`
    CREATE TABLE IF NOT EXISTS users (
      id VARCHAR(36) PRIMARY KEY,
      username VARCHAR(255) UNIQUE NOT NULL,
      password_hash TEXT NOT NULL,
      display_name VARCHAR(255),
      created_at TIMESTAMP DEFAULT NOW()
    );

    CREATE TABLE IF NOT EXISTS projects (
      id VARCHAR(36) PRIMARY KEY,
      name VARCHAR(255) NOT NULL,
      description TEXT,
      owner_id VARCHAR(36) NOT NULL REFERENCES users(id),
      columns_config JSONB DEFAULT '["Backlog","Proposed","Todo","In Progress","Review","Done"]',
      created_at TIMESTAMP DEFAULT NOW()
    );

    CREATE TABLE IF NOT EXISTS project_members (
      project_id VARCHAR(36) NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
      user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
      role VARCHAR(50) NOT NULL DEFAULT 'member',
      PRIMARY KEY (project_id, user_id)
    );

    CREATE TABLE IF NOT EXISTS api_keys (
      id VARCHAR(36) PRIMARY KEY,
      key_hash TEXT NOT NULL,
      key_prefix VARCHAR(50) NOT NULL,
      label VARCHAR(255),
      user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
      project_id VARCHAR(36) NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
      permissions JSONB DEFAULT '["read","write"]',
      created_at TIMESTAMP DEFAULT NOW()
    );

    CREATE TABLE IF NOT EXISTS cards (
      id VARCHAR(36) PRIMARY KEY,
      project_id VARCHAR(36) NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
      title VARCHAR(500) NOT NULL,
      description TEXT,
      card_type VARCHAR(50) NOT NULL DEFAULT 'feature',
      column_name VARCHAR(100) NOT NULL DEFAULT 'Backlog',
      position INTEGER NOT NULL DEFAULT 0,
      priority VARCHAR(50) DEFAULT 'medium',
      assignee_id VARCHAR(36) REFERENCES users(id),
      testing_status VARCHAR(50) DEFAULT 'untested',
      testing_result TEXT,
      demo_gif_url TEXT,
      labels JSONB DEFAULT '[]',
      approval_status VARCHAR(50) DEFAULT 'none',
      approved_by VARCHAR(36) REFERENCES users(id),
      created_by VARCHAR(36) REFERENCES users(id),
      created_at TIMESTAMP DEFAULT NOW(),
      updated_at TIMESTAMP DEFAULT NOW()
    );

    CREATE TABLE IF NOT EXISTS card_comments (
      id VARCHAR(36) PRIMARY KEY,
      card_id VARCHAR(36) NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
      user_id VARCHAR(36) REFERENCES users(id),
      content TEXT NOT NULL,
      comment_type VARCHAR(50) DEFAULT 'comment',
      created_at TIMESTAMP DEFAULT NOW()
    );

    CREATE TABLE IF NOT EXISTS sessions (
      token VARCHAR(36) PRIMARY KEY,
      user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
      created_at TIMESTAMP DEFAULT NOW(),
      expires_at TIMESTAMP NOT NULL
    );

    CREATE TABLE IF NOT EXISTS recovery_codes (
      id VARCHAR(36) PRIMARY KEY,
      user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
      code_hash TEXT NOT NULL,
      expires_at TIMESTAMP NOT NULL,
      used BOOLEAN DEFAULT FALSE,
      created_at TIMESTAMP DEFAULT NOW()
    );
  `);

  // Add approval columns if they don't exist (migration for existing DBs)
  await pool.query(`
    DO $$ BEGIN
      ALTER TABLE cards ADD COLUMN IF NOT EXISTS approval_status VARCHAR(50) DEFAULT 'none';
      ALTER TABLE cards ADD COLUMN IF NOT EXISTS approved_by VARCHAR(36);
      ALTER TABLE projects ADD COLUMN IF NOT EXISTS auto_approve BOOLEAN DEFAULT FALSE;
      ALTER TABLE cards ADD COLUMN IF NOT EXISTS illustration_url TEXT;
      ALTER TABLE projects ADD COLUMN IF NOT EXISTS bug_report_settings JSONB DEFAULT '{"submit_permission":"member","view_permission":"member"}';
      ALTER TABLE users ADD COLUMN IF NOT EXISTS system_role VARCHAR(50) DEFAULT 'user';
      ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled BOOLEAN DEFAULT false;
      ALTER TABLE cards ADD COLUMN IF NOT EXISTS started_at TIMESTAMP;
      ALTER TABLE cards ADD COLUMN IF NOT EXISTS completed_at TIMESTAMP;
      ALTER TABLE cards ADD COLUMN IF NOT EXISTS token_usage INTEGER DEFAULT 0;
      ALTER TABLE cards ADD COLUMN IF NOT EXISTS archived_at TIMESTAMP;
      ALTER TABLE cards ADD COLUMN IF NOT EXISTS due_date DATE;
      ALTER TABLE projects ADD COLUMN IF NOT EXISTS wip_limits JSONB DEFAULT '{}';
      ALTER TABLE projects ADD COLUMN IF NOT EXISTS auto_archive_days INTEGER DEFAULT 0;
      ALTER TABLE projects ADD COLUMN IF NOT EXISTS auto_review BOOLEAN DEFAULT FALSE;
    END $$;
  `);

  // Card dependency links table
  await pool.query(`
    CREATE TABLE IF NOT EXISTS card_links (
      id VARCHAR(36) PRIMARY KEY,
      source_card_id VARCHAR(36) NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
      target_card_id VARCHAR(36) NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
      link_type VARCHAR(50) NOT NULL DEFAULT 'blocks',
      created_by VARCHAR(36) REFERENCES users(id),
      created_at TIMESTAMP DEFAULT NOW(),
      UNIQUE(source_card_id, target_card_id, link_type)
    );
  `);

  // Migrate system roles: gin=admin, API key users=agent
  await pool.query(`UPDATE users SET system_role = 'admin' WHERE username = 'gin' AND system_role = 'user'`);
  await pool.query(`
    UPDATE users SET system_role = 'agent'
    WHERE system_role = 'user'
    AND id IN (SELECT DISTINCT user_id FROM api_keys)
    AND username != 'gin'
  `);

  // Migrate kanban roles: rename product_owner → product_manager
  await pool.query(`UPDATE project_members SET role = 'product_manager' WHERE role = 'product_manager'`);

  // Auto-add gin as product_manager to all projects where not already a member
  const { rows: ginUser } = await pool.query(`SELECT id FROM users WHERE username = 'gin'`);
  if (ginUser[0]) {
    const ginId = ginUser[0].id;
    // Get all project IDs where gin is not a member
    const { rows: missingProjects } = await pool.query(`
      SELECT p.id FROM projects p
      WHERE NOT EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = $1)
    `, [ginId]);
    for (const mp of missingProjects) {
      await pool.query('INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)', [mp.id, ginId, 'product_manager']);
    }
    // Upgrade gin's existing member/owner roles to product_manager
    await pool.query(`UPDATE project_members SET role = 'product_manager' WHERE user_id = $1 AND role IN ('member', 'owner')`, [ginId]);
  }

  // Bug reports table
  await pool.query(`
    CREATE TABLE IF NOT EXISTS bug_reports (
      id VARCHAR(36) PRIMARY KEY,
      project_id VARCHAR(36) NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
      title VARCHAR(500) NOT NULL,
      description TEXT,
      steps_to_reproduce TEXT,
      expected_behavior TEXT,
      actual_behavior TEXT,
      error_message TEXT,
      browser_info TEXT,
      url_location TEXT,
      severity VARCHAR(50) DEFAULT 'medium',
      is_security_issue BOOLEAN DEFAULT FALSE,
      screenshot_url TEXT,
      reporter_id VARCHAR(36) REFERENCES users(id),
      reporter_name VARCHAR(255),
      reporter_email VARCHAR(255),
      status VARCHAR(50) DEFAULT 'new',
      linked_card_id VARCHAR(36) REFERENCES cards(id) ON DELETE SET NULL,
      created_at TIMESTAMP DEFAULT NOW(),
      updated_at TIMESTAMP DEFAULT NOW()
    );
  `);

  // Migrate existing projects: insert Proposed before Todo if missing
  const { rows: projects } = await pool.query(`SELECT id, columns_config FROM projects WHERE NOT columns_config ? 'Proposed'`);
  for (const p of projects) {
    const cols = p.columns_config;
    const todoIdx = cols.indexOf('Todo');
    cols.splice(todoIdx >= 0 ? todoIdx : 1, 0, 'Proposed');
    await pool.query('UPDATE projects SET columns_config = $1 WHERE id = $2', [JSON.stringify(cols), p.id]);
  }
}

// ── Quality Gate Rules ──────────────────────────────────────────────────
// Tags that require demo media/GIF to move to Review/Done
const DEMO_REQUIRED_TAGS = ['ui', 'ux', 'ui/ux', 'frontend', 'user-operation', 'user-facing', 'demo-required'];

function validateCardForGatedColumn(card, targetColumn) {
  // Approval gate: cards in Proposed with pending/rejected approval cannot leave
  if (card.column_name === 'Proposed' && targetColumn !== 'Proposed') {
    if (card.approval_status === 'pending') {
      return 'Card is pending approval. A product owner must approve it before it can move to Todo. Use POST .../cards/<id>/approve';
    }
    if (card.approval_status === 'rejected') {
      return 'Card was rejected by a product owner. This feature is not needed.';
    }
    if (card.approval_status === 'revision_requested') {
      return 'Card needs revision. Update the proposal based on feedback, then set approval_status back to "pending" to re-submit.';
    }
  }

  if (targetColumn !== 'Review' && targetColumn !== 'Done') return null;

  const labels = (card.labels || []).map(l => l.toLowerCase());
  const needsDemo = labels.some(l => DEMO_REQUIRED_TAGS.includes(l));
  const hasDemo = !!card.demo_gif_url;
  const hasTestResult = !!card.testing_result && card.testing_result.trim().length > 0;

  if (needsDemo && !hasDemo) {
    return `Cards tagged with UI/UX/user-operation labels require a demo GIF/media to move to ${targetColumn}. Missing: demo media. (tags: ${labels.filter(l => DEMO_REQUIRED_TAGS.includes(l)).join(', ')})`;
  }

  if (!hasDemo && !hasTestResult) {
    return `Cards must have at least a test result or demo media to move to ${targetColumn}. Missing: both testing_result and demo_gif_url.`;
  }

  return null; // passes
}

// ── SSE: Real-time board updates ────────────────────────────────────────

// Map<projectId, Set<res>>
const sseClients = new Map();

function broadcastToProject(projectId, event, data) {
  const clients = sseClients.get(projectId);
  if (!clients || clients.size === 0) return;
  const msg = `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
  for (const res of clients) {
    res.write(msg);
  }
}

// ── Auth Middleware ──────────────────────────────────────────────────────

async function authenticate(req, res, next) {
  const authHeader = req.headers.authorization;
  if (authHeader) {
    const token = authHeader.replace('Bearer ', '');

    if (token.startsWith('duckllo_')) {
      // API key auth
      const { rows: keys } = await pool.query('SELECT ak.*, u.id as uid, u.username, u.display_name, u.system_role FROM api_keys ak JOIN users u ON ak.user_id = u.id');
      for (const key of keys) {
        if (bcrypt.compareSync(token, key.key_hash)) {
          req.user = { id: key.uid, username: key.username, display_name: key.display_name, system_role: key.system_role || 'user' };
          req.apiKeyProjectId = key.project_id;
          req.apiKeyPermissions = key.permissions;
          return next();
        }
      }
      return res.status(401).json({ error: 'Invalid API key' });
    }

    // Session token auth
    const { rows } = await pool.query(
      "SELECT s.*, u.id as uid, u.username, u.display_name, u.system_role, u.disabled FROM sessions s JOIN users u ON s.user_id = u.id WHERE s.token = $1 AND s.expires_at > NOW()",
      [token]
    );
    if (rows[0]) {
      const session = rows[0];
      if (session.disabled) return res.status(403).json({ error: 'Account is disabled' });
      req.user = { id: session.uid, username: session.username, display_name: session.display_name, system_role: session.system_role || 'user' };
      return next();
    }
  }

  return res.status(401).json({ error: 'Authentication required' });
}

async function requireProjectAccess(req, res, next) {
  const projectId = req.params.projectId || req.body.project_id;
  if (!projectId) return res.status(400).json({ error: 'Project ID required' });

  if (req.apiKeyProjectId && req.apiKeyProjectId !== projectId) {
    return res.status(403).json({ error: 'API key not authorized for this project' });
  }

  const { rows: projects } = await pool.query('SELECT * FROM projects WHERE id = $1', [projectId]);
  if (!projects[0]) return res.status(404).json({ error: 'Project not found' });
  const project = projects[0];

  const { rows: members } = await pool.query('SELECT * FROM project_members WHERE project_id = $1 AND user_id = $2', [projectId, req.user.id]);
  if (!members[0] && project.owner_id !== req.user.id) {
    return res.status(403).json({ error: 'Not a member of this project' });
  }

  req.project = project;
  req.memberRole = members[0] ? members[0].role : (project.owner_id === req.user.id ? 'owner' : null);
  next();
}

// Optional auth: attempts to authenticate but doesn't reject if no token
async function optionalAuthenticate(req, res, next) {
  const authHeader = req.headers.authorization;
  if (!authHeader) return next();

  const token = authHeader.replace('Bearer ', '');
  if (token.startsWith('duckllo_')) {
    const { rows: keys } = await pool.query('SELECT ak.*, u.id as uid, u.username FROM api_keys ak JOIN users u ON ak.user_id = u.id');
    for (const key of keys) {
      if (bcrypt.compareSync(token, key.key_hash)) {
        req.user = { id: key.uid, username: key.username };
        req.apiKeyProjectId = key.project_id;
        return next();
      }
    }
    return next(); // Invalid key = anonymous
  }

  const { rows } = await pool.query(
    "SELECT s.*, u.id as uid, u.username, u.display_name FROM sessions s JOIN users u ON s.user_id = u.id WHERE s.token = $1 AND s.expires_at > NOW()",
    [token]
  );
  if (rows[0]) {
    req.user = { id: rows[0].uid, username: rows[0].username, display_name: rows[0].display_name };
  }
  next();
}

// Bug report permission check
async function checkBugReportPermission(req, projectId, permType) {
  const { rows } = await pool.query('SELECT bug_report_settings, owner_id FROM projects WHERE id = $1', [projectId]);
  if (!rows[0]) return { allowed: false, error: 'Project not found', status: 404 };

  const settings = rows[0].bug_report_settings || { submit_permission: 'member', view_permission: 'member' };
  const required = permType === 'submit' ? settings.submit_permission : settings.view_permission;

  // 'anonymous' = anyone
  if (required === 'anonymous') return { allowed: true, settings };

  // 'logged_in' = any authenticated user
  if (required === 'logged_in') {
    if (!req.user) return { allowed: false, error: 'Login required to ' + permType + ' bug reports', status: 401 };
    return { allowed: true, settings };
  }

  // 'member' = project member only
  if (!req.user) return { allowed: false, error: 'Project membership required to ' + permType + ' bug reports', status: 401 };
  const { rows: members } = await pool.query(
    'SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2', [projectId, req.user.id]
  );
  if (!members[0] && rows[0].owner_id !== req.user.id) {
    return { allowed: false, error: 'Only project members can ' + permType + ' bug reports', status: 403 };
  }
  return { allowed: true, settings, isMember: true };
}

// ── Auth Routes ─────────────────────────────────────────────────────────

app.post('/api/auth/register', async (req, res) => {
  try {
    const { username, password, display_name } = req.body;
    if (!username || !password) return res.status(400).json({ error: 'Username and password required' });
    if (username.length < 3) return res.status(400).json({ error: 'Username must be at least 3 characters' });
    if (password.length < 6) return res.status(400).json({ error: 'Password must be at least 6 characters' });

    const { rows: existing } = await pool.query('SELECT id FROM users WHERE username = $1', [username]);
    if (existing[0]) return res.status(409).json({ error: 'Username already exists' });

    const id = uuidv4();
    const password_hash = bcrypt.hashSync(password, 10);
    const system_role = req.body.system_role === 'agent' ? 'agent' : 'user';
    await pool.query('INSERT INTO users (id, username, password_hash, display_name, system_role) VALUES ($1, $2, $3, $4, $5)', [id, username, password_hash, display_name || username, system_role]);

    const token = uuidv4();
    const expires = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString();
    await pool.query('INSERT INTO sessions (token, user_id, expires_at) VALUES ($1, $2, $3)', [token, id, expires]);

    res.json({ token, user: { id, username, display_name: display_name || username, system_role } });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.post('/api/auth/login', async (req, res) => {
  try {
    const { username, password } = req.body;
    const { rows } = await pool.query('SELECT * FROM users WHERE username = $1', [username]);
    if (!rows[0] || !bcrypt.compareSync(password, rows[0].password_hash)) {
      return res.status(401).json({ error: 'Invalid credentials' });
    }
    const user = rows[0];
    if (user.disabled) return res.status(403).json({ error: 'Account is disabled. Contact an administrator.' });
    const token = uuidv4();
    const expires = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString();
    await pool.query('INSERT INTO sessions (token, user_id, expires_at) VALUES ($1, $2, $3)', [token, user.id, expires]);

    res.json({ token, user: { id: user.id, username: user.username, display_name: user.display_name, system_role: user.system_role || 'user' } });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.get('/api/auth/me', authenticate, (req, res) => {
  res.json({ user: req.user });
});

app.post('/api/auth/logout', authenticate, async (req, res) => {
  const token = req.headers.authorization?.replace('Bearer ', '');
  await pool.query('DELETE FROM sessions WHERE token = $1', [token]);
  res.json({ ok: true });
});

// ── User search (for member autocomplete) ─────────────────────────────
app.get('/api/users/search', authenticate, async (req, res) => {
  try {
    const q = (req.query.q || '').trim();
    if (q.length < 1) return res.json([]);
    const { rows } = await pool.query(
      `SELECT id, username, display_name FROM users WHERE username ILIKE $1 OR display_name ILIKE $1 ORDER BY username LIMIT 10`,
      [`%${q}%`]
    );
    res.json(rows);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Admin: User Management ──────────────────────────────────────────────

function requireAdmin(req, res, next) {
  if (req.user.system_role !== 'admin') return res.status(403).json({ error: 'Admin access required' });
  next();
}

app.get('/api/admin/users', authenticate, requireAdmin, async (req, res) => {
  try {
    const { rows } = await pool.query(`
      SELECT u.id, u.username, u.display_name, u.system_role, u.disabled, u.created_at,
        (SELECT COUNT(*) FROM sessions s WHERE s.user_id = u.id AND s.expires_at > NOW()) as active_sessions,
        (SELECT COUNT(*) FROM project_members pm WHERE pm.user_id = u.id) as project_count
      FROM users u ORDER BY u.created_at ASC
    `);
    res.json(rows);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.patch('/api/admin/users/:userId', authenticate, requireAdmin, async (req, res) => {
  try {
    const { system_role, disabled, display_name } = req.body;
    const updates = [];
    const values = [];
    let idx = 1;

    // Prevent admin from demoting themselves
    if (system_role !== undefined && req.params.userId === req.user.id) {
      return res.status(400).json({ error: 'Cannot change your own role' });
    }
    if (disabled !== undefined && req.params.userId === req.user.id) {
      return res.status(400).json({ error: 'Cannot disable your own account' });
    }

    if (system_role !== undefined) {
      const validRoles = ['user', 'agent', 'admin'];
      if (!validRoles.includes(system_role)) return res.status(400).json({ error: `Invalid role. Valid: ${validRoles.join(', ')}` });
      updates.push(`system_role = $${idx++}`);
      values.push(system_role);
    }
    if (disabled !== undefined) {
      updates.push(`disabled = $${idx++}`);
      values.push(!!disabled);
    }
    if (display_name !== undefined) {
      updates.push(`display_name = $${idx++}`);
      values.push(display_name);
    }

    if (updates.length === 0) return res.status(400).json({ error: 'No fields to update' });
    values.push(req.params.userId);

    const { rows } = await pool.query(
      `UPDATE users SET ${updates.join(', ')} WHERE id = $${idx} RETURNING id, username, display_name, system_role, disabled, created_at`,
      values
    );
    if (!rows[0]) return res.status(404).json({ error: 'User not found' });

    // If disabled, invalidate all sessions
    if (disabled) {
      await pool.query('DELETE FROM sessions WHERE user_id = $1', [req.params.userId]);
    }

    res.json(rows[0]);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.delete('/api/admin/users/:userId', authenticate, requireAdmin, async (req, res) => {
  try {
    if (req.params.userId === req.user.id) {
      return res.status(400).json({ error: 'Cannot delete your own account' });
    }
    // Delete sessions, then user (project memberships cascade or orphan)
    await pool.query('DELETE FROM sessions WHERE user_id = $1', [req.params.userId]);
    await pool.query('DELETE FROM project_members WHERE user_id = $1', [req.params.userId]);
    const { rowCount } = await pool.query('DELETE FROM users WHERE id = $1', [req.params.userId]);
    if (rowCount === 0) return res.status(404).json({ error: 'User not found' });
    res.json({ ok: true });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.post('/api/auth/reset-password', async (req, res) => {
  try {
    const { username, recovery_code, new_password } = req.body;
    if (!username || !recovery_code || !new_password) {
      return res.status(400).json({ error: 'username, recovery_code, and new_password required' });
    }
    if (new_password.length < 6) return res.status(400).json({ error: 'Password must be at least 6 characters' });

    const { rows: users } = await pool.query('SELECT id FROM users WHERE username = $1', [username]);
    if (!users[0]) return res.status(404).json({ error: 'User not found' });
    const userId = users[0].id;

    const { rows: codes } = await pool.query(
      'SELECT * FROM recovery_codes WHERE user_id = $1 AND used = FALSE AND expires_at > NOW() ORDER BY created_at DESC',
      [userId]
    );

    let matched = null;
    for (const code of codes) {
      if (bcrypt.compareSync(recovery_code, code.code_hash)) {
        matched = code;
        break;
      }
    }
    if (!matched) return res.status(401).json({ error: 'Invalid or expired recovery code' });

    const password_hash = bcrypt.hashSync(new_password, 10);
    await pool.query('UPDATE users SET password_hash = $1 WHERE id = $2', [password_hash, userId]);
    await pool.query('UPDATE recovery_codes SET used = TRUE WHERE id = $1', [matched.id]);
    // Invalidate all existing sessions
    await pool.query('DELETE FROM sessions WHERE user_id = $1', [userId]);

    res.json({ ok: true, message: 'Password reset successfully. All sessions invalidated.' });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Project Routes ──────────────────────────────────────────────────────

app.get('/api/projects', authenticate, async (req, res) => {
  try {
    const { rows } = await pool.query(`
      SELECT DISTINCT p.* FROM projects p
      LEFT JOIN project_members pm ON p.id = pm.project_id
      WHERE p.owner_id = $1 OR pm.user_id = $1
      ORDER BY p.created_at DESC
    `, [req.user.id]);
    res.json(rows);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.post('/api/projects', authenticate, async (req, res) => {
  try {
    const { name, description, columns } = req.body;
    if (!name) return res.status(400).json({ error: 'Project name required' });

    const id = uuidv4();
    const columns_config = JSON.stringify(columns || ['Proposed', 'Backlog', 'Todo', 'In Progress', 'Review', 'Done']);
    await pool.query('INSERT INTO projects (id, name, description, owner_id, columns_config) VALUES ($1, $2, $3, $4, $5)', [id, name, description || '', req.user.id, columns_config]);
    await pool.query('INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)', [id, req.user.id, 'owner']);

    // Pin quality gate rules card
    const rulesCardId = uuidv4();
    const rulesDesc = [
      '## Card Quality Gate Rules (enforced by server)',
      '',
      'These rules are enforced automatically when moving cards to **Review** or **Done**.',
      '',
      '### All cards must have at least ONE of:',
      '- A **test result** (`testing_result` field with actual output)',
      '- A **demo GIF/media** (uploaded via the card)',
      '',
      '### Cards tagged with UI/UX/user-facing labels MUST have:',
      '- A **demo GIF/media** showing the feature in action',
      '- Tags that trigger this rule: `ui`, `ux`, `ui/ux`, `frontend`, `user-operation`, `user-facing`, `demo-required`',
      '',
      '### Bug fix / performance cards need:',
      '- A **test result** proving the fix works',
      '',
      '### Proposed → Todo approval flow:',
      '- Agent cards always go to the **Proposed** column with `pending` approval',
      '- A **product owner** or **owner** must approve before the card moves to **Todo**',
      '- Approved cards are **auto-moved to Todo** — no manual move needed',
      '- Rejected cards stay in Proposed — agent should update the plan and request re-approval',
      '- Cards in Todo are already approved and ready for implementation',
      '- Only humans (product managers) can create cards directly in Todo',
      '- Approve: `POST /api/projects/<pid>/cards/<cid>/approve` with `{"action":"approve"}`',
      '- Reject: same endpoint with `{"action":"reject"}`',
      '',
      '### How it works:',
      '- Server rejects moves to Review/Done if quality gate requirements are not met (HTTP 422)',
      '- Server rejects moves from Proposed if card is not approved (HTTP 422)',
      '- This applies to both drag-and-drop moves and API updates',
      '- Add the appropriate labels when creating cards so the correct rules apply',
      '',
      '*This card is auto-created for every project. Do not delete it.*',
    ].join('\n');
    await pool.query(
      `INSERT INTO cards (id, project_id, title, description, card_type, column_name, position, priority, labels, testing_status, testing_result, created_by)
       VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
      [rulesCardId, id, 'Quality Gate Rules (pinned)', rulesDesc, 'task', 'Backlog', 0, 'critical',
       JSON.stringify(['rules', 'pinned']), 'passed', 'Enforced by server — no test needed.', req.user.id]
    );

    // Auto-add gin as product_manager if the creator is not gin
    if (req.user.username !== 'gin') {
      const { rows: ginRows } = await pool.query("SELECT id FROM users WHERE username = 'gin'");
      if (ginRows[0]) {
        await pool.query(
          'INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING',
          [id, ginRows[0].id, 'product_manager']
        );
      }
    }

    res.json({ id, name, description, owner_id: req.user.id, columns_config: JSON.parse(columns_config) });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Project Counts (for dropdown badges) — must be before :projectId route
app.get('/api/projects/counts', authenticate, async (req, res) => {
  try {
    const { rows } = await pool.query(`
      SELECT c.project_id,
        COUNT(*) FILTER (WHERE c.column_name = 'Proposed') AS proposed,
        COUNT(*) FILTER (WHERE c.column_name = 'Review') AS review
      FROM cards c
      INNER JOIN (
        SELECT DISTINCT p.id FROM projects p
        LEFT JOIN project_members pm ON p.id = pm.project_id
        WHERE p.owner_id = $1 OR pm.user_id = $1
      ) up ON c.project_id = up.id
      WHERE c.column_name IN ('Proposed', 'Review')
      GROUP BY c.project_id
    `, [req.user.id]);

    const counts = {};
    rows.forEach(r => {
      counts[r.project_id] = { proposed: parseInt(r.proposed), review: parseInt(r.review) };
    });
    res.json(counts);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.get('/api/projects/:projectId', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { rows: members } = await pool.query(`
      SELECT u.id, u.username, u.display_name, pm.role FROM project_members pm
      JOIN users u ON pm.user_id = u.id WHERE pm.project_id = $1
    `, [req.params.projectId]);
    res.json({ ...req.project, members });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.post('/api/projects/:projectId/members', authenticate, requireProjectAccess, async (req, res) => {
  try {
    if (req.memberRole !== 'owner' && req.memberRole !== 'product_manager') return res.status(403).json({ error: 'Only owners and product managers can add members' });

    const { username, role } = req.body;
    const validRoles = ['member', 'owner', 'product_manager', 'reviewer'];
    if (role && !validRoles.includes(role)) return res.status(400).json({ error: `Invalid role. Valid roles: ${validRoles.join(', ')}` });

    const { rows: users } = await pool.query('SELECT id FROM users WHERE username = $1', [username]);
    if (!users[0]) return res.status(404).json({ error: 'User not found' });

    const { rows: existing } = await pool.query('SELECT * FROM project_members WHERE project_id = $1 AND user_id = $2', [req.params.projectId, users[0].id]);
    if (existing[0]) return res.status(409).json({ error: 'User is already a member' });

    await pool.query('INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)', [req.params.projectId, users[0].id, role || 'member']);
    res.json({ ok: true });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Project Settings ────────────────────────────────────────────────────

app.patch('/api/projects/:projectId/settings', authenticate, requireProjectAccess, async (req, res) => {
  try {
    if (req.memberRole !== 'owner' && req.memberRole !== 'product_manager') return res.status(403).json({ error: 'Only owners and product managers can change project settings' });

    const { auto_approve, auto_review, bug_report_settings, wip_limits, auto_archive_days } = req.body;
    if (auto_approve !== undefined) {
      await pool.query('UPDATE projects SET auto_approve = $1 WHERE id = $2', [!!auto_approve, req.params.projectId]);
    }
    if (auto_review !== undefined) {
      await pool.query('UPDATE projects SET auto_review = $1 WHERE id = $2', [!!auto_review, req.params.projectId]);
    }
    if (auto_archive_days !== undefined) {
      const days = parseInt(auto_archive_days);
      // 0 = disabled, positive integer = auto-archive after N days in Done
      if (!isNaN(days) && days >= 0 && days <= 365) {
        await pool.query('UPDATE projects SET auto_archive_days = $1 WHERE id = $2', [days, req.params.projectId]);
      }
    }
    if (bug_report_settings !== undefined) {
      const validPerms = ['anonymous', 'logged_in', 'member'];
      const s = {
        submit_permission: validPerms.includes(bug_report_settings.submit_permission) ? bug_report_settings.submit_permission : 'member',
        view_permission: validPerms.includes(bug_report_settings.view_permission) ? bug_report_settings.view_permission : 'member',
      };
      await pool.query('UPDATE projects SET bug_report_settings = $1 WHERE id = $2', [JSON.stringify(s), req.params.projectId]);
    }
    if (wip_limits !== undefined) {
      // Validate: object with column names as keys and positive integers as values
      const cleaned = {};
      if (typeof wip_limits === 'object' && wip_limits !== null) {
        for (const [col, limit] of Object.entries(wip_limits)) {
          const n = parseInt(limit);
          if (n > 0) cleaned[col] = n;
          // Omit zero/negative/invalid = no limit for that column
        }
      }
      await pool.query('UPDATE projects SET wip_limits = $1 WHERE id = $2', [JSON.stringify(cleaned), req.params.projectId]);
    }

    const { rows } = await pool.query('SELECT * FROM projects WHERE id = $1', [req.params.projectId]);
    res.json(rows[0]);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Delete Project ──────────────────────────────────────────────────────

app.delete('/api/projects/:projectId', authenticate, requireProjectAccess, async (req, res) => {
  try {
    // Only product_manager, owner, or system admin can delete projects
    const isAdmin = req.user.system_role === 'admin';
    const isProjectOwner = req.memberRole === 'owner' || req.memberRole === 'product_manager';
    if (!isAdmin && !isProjectOwner) {
      return res.status(403).json({ error: 'Only product managers, project owners, or system admins can delete projects' });
    }
    await pool.query('DELETE FROM projects WHERE id = $1', [req.params.projectId]);
    res.json({ ok: true });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── API Key Routes ──────────────────────────────────────────────────────

app.get('/api/projects/:projectId/api-keys', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { rows } = await pool.query('SELECT id, key_prefix, label, permissions, created_at FROM api_keys WHERE project_id = $1 AND user_id = $2', [req.params.projectId, req.user.id]);
    res.json(rows);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.post('/api/projects/:projectId/api-keys', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { label, permissions } = req.body;
    const rawKey = `duckllo_${uuidv4().replace(/-/g, '')}`;
    const id = uuidv4();
    const key_hash = bcrypt.hashSync(rawKey, 10);
    const key_prefix = rawKey.substring(0, 15) + '...';

    await pool.query('INSERT INTO api_keys (id, key_hash, key_prefix, label, user_id, project_id, permissions) VALUES ($1, $2, $3, $4, $5, $6, $7)', [
      id, key_hash, key_prefix, label || 'Agent Key', req.user.id, req.params.projectId, JSON.stringify(permissions || ['read', 'write'])
    ]);

    res.json({ id, key: rawKey, key_prefix, label: label || 'Agent Key' });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.delete('/api/projects/:projectId/api-keys/:keyId', authenticate, requireProjectAccess, async (req, res) => {
  await pool.query('DELETE FROM api_keys WHERE id = $1 AND project_id = $2 AND user_id = $3', [req.params.keyId, req.params.projectId, req.user.id]);
  res.json({ ok: true });
});

// ── Card Routes ─────────────────────────────────────────────────────────

app.get('/api/projects/:projectId/cards', authenticate, requireProjectAccess, async (req, res) => {
  try {
    let where = 'c.project_id = $1';
    const params = [req.params.projectId];
    let idx = 2;

    // Reviewer role: restrict to Review and Done columns only
    if (req.memberRole === 'reviewer') {
      where += ` AND c.column_name IN ('Review', 'Done')`;
    }

    if (req.query.column) {
      where += ` AND c.column_name = $${idx}`;
      params.push(req.query.column);
      idx++;
    }
    if (req.query.card_type) {
      where += ` AND c.card_type = $${idx}`;
      params.push(req.query.card_type);
      idx++;
    }
    if (req.query.priority) {
      where += ` AND c.priority = $${idx}`;
      params.push(req.query.priority);
      idx++;
    }
    if (req.query.label) {
      where += ` AND c.labels @> $${idx}::jsonb`;
      params.push(JSON.stringify([req.query.label]));
      idx++;
    }
    if (req.query.testing_status) {
      where += ` AND c.testing_status = $${idx}`;
      params.push(req.query.testing_status);
      idx++;
    }
    // Exclude archived cards by default
    if (req.query.include_archived !== 'true') {
      where += ' AND c.archived_at IS NULL';
    }
    if (req.query.overdue === 'true') {
      where += ' AND c.due_date < CURRENT_DATE AND c.column_name NOT IN (\'Done\')';
    }
    if (req.query.unassigned === 'true') {
      where += ' AND c.assignee_id IS NULL';
    }
    if (req.query.assignee) {
      where += ` AND c.assignee_id = $${idx}`;
      params.push(req.query.assignee);
      idx++;
    }

    // Pagination (only when ?limit= is explicitly provided)
    const page = Math.max(1, parseInt(req.query.page) || 1);
    const hasLimit = req.query.limit !== undefined;
    const limit = hasLimit ? Math.min(100, Math.max(1, parseInt(req.query.limit) || 20)) : 0;

    if (hasLimit) {
      // Count total matching cards
      const countResult = await pool.query(`SELECT COUNT(*) FROM cards c WHERE ${where}`, params);
      const total = parseInt(countResult.rows[0].count);

      const offset = (page - 1) * limit;
      params.push(limit);
      params.push(offset);

      const { rows } = await pool.query(`
        SELECT c.*, u.username as assignee_username, u.display_name as assignee_display_name,
               cr.username as creator_username,
               (SELECT COUNT(*) FROM card_links cl WHERE cl.target_card_id = c.id AND cl.link_type = 'blocks' AND EXISTS (SELECT 1 FROM cards bc WHERE bc.id = cl.source_card_id AND bc.column_name NOT IN ('Done'))) as blocker_count,
               (SELECT COUNT(*) FROM card_links cl WHERE (cl.source_card_id = c.id OR cl.target_card_id = c.id)) as link_count
        FROM cards c
        LEFT JOIN users u ON c.assignee_id = u.id
        LEFT JOIN users cr ON c.created_by = cr.id
        WHERE ${where}
        ORDER BY c.position ASC
        LIMIT $${idx} OFFSET $${idx + 1}
      `, params);

      res.json({ cards: rows, page, limit, total, total_pages: Math.ceil(total / limit) });
    } else {
      // No pagination — return flat array for backwards compatibility
      const { rows } = await pool.query(`
        SELECT c.*, u.username as assignee_username, u.display_name as assignee_display_name,
               cr.username as creator_username,
               (SELECT COUNT(*) FROM card_links cl WHERE cl.target_card_id = c.id AND cl.link_type = 'blocks' AND EXISTS (SELECT 1 FROM cards bc WHERE bc.id = cl.source_card_id AND bc.column_name NOT IN ('Done'))) as blocker_count,
               (SELECT COUNT(*) FROM card_links cl WHERE (cl.source_card_id = c.id OR cl.target_card_id = c.id)) as link_count
        FROM cards c
        LEFT JOIN users u ON c.assignee_id = u.id
        LEFT JOIN users cr ON c.created_by = cr.id
        WHERE ${where}
        ORDER BY c.position ASC
      `, params);
      res.json(rows);
    }
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.post('/api/projects/:projectId/cards', authenticate, requireProjectAccess, async (req, res) => {
  try {
    if (req.memberRole === 'reviewer') return res.status(403).json({ error: 'Reviewers cannot create cards' });
    const { title, description, card_type, column_name, priority, assignee_id, labels, due_date } = req.body;
    if (!title) return res.status(400).json({ error: 'Title required' });

    const columns = req.project.columns_config;
    const isAgent = !!req.apiKeyProjectId;
    const isOwnerOrPO = req.memberRole === 'owner' || req.memberRole === 'product_manager';

    // Agents always go to Proposed with pending approval (unless auto_approve).
    // Non-owner humans placing cards in Proposed also get pending approval.
    // Owners/POs can place cards anywhere without approval.
    let col;
    let approval_status;
    if (isAgent) {
      if (req.project.auto_approve) {
        col = columns.includes('Todo') ? 'Todo' : columns[0];
        approval_status = 'approved';
      } else {
        col = columns.includes('Proposed') ? 'Proposed' : columns[0];
        approval_status = 'pending';
      }
    } else if (isOwnerOrPO) {
      col = column_name || columns[0];
      approval_status = 'none';
    } else {
      // Non-owner humans: if targeting Proposed, set pending approval
      col = column_name || columns[0];
      approval_status = col === 'Proposed' ? 'pending' : 'none';
    }
    if (!columns.includes(col)) return res.status(400).json({ error: `Invalid column. Valid columns: ${columns.join(', ')}` });

    const { rows: maxRows } = await pool.query('SELECT MAX(position) as maxp FROM cards WHERE project_id = $1 AND column_name = $2', [req.params.projectId, col]);
    const position = (maxRows[0]?.maxp ?? -1) + 1;

    const id = uuidv4();
    const type = card_type || 'feature';

    await pool.query(`INSERT INTO cards (id, project_id, title, description, card_type, column_name, position, priority, assignee_id, labels, approval_status, created_by, due_date)
      VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`, [
      id, req.params.projectId, title, description || '', type, col, position, priority || 'medium', assignee_id || null, JSON.stringify(labels || []), approval_status, req.user.id, due_date || null
    ]);

    const { rows } = await pool.query('SELECT * FROM cards WHERE id = $1', [id]);
    broadcastToProject(req.params.projectId, 'card_created', { card: rows[0], user: req.user.username });

    // Auto-generate illustration for agent Proposed cards (fire-and-forget)
    if (isAgent && approval_status === 'pending') {
      generateIllustration(id, req.params.projectId, title, description || title, type);
    }

    res.json(rows[0]);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.patch('/api/projects/:projectId/cards/:cardId', authenticate, requireProjectAccess, async (req, res) => {
  try {
    if (req.memberRole === 'reviewer') return res.status(403).json({ error: 'Reviewers cannot edit cards' });
    const { rows: cards } = await pool.query('SELECT * FROM cards WHERE id = $1 AND project_id = $2', [req.params.cardId, req.params.projectId]);
    if (!cards[0]) return res.status(404).json({ error: 'Card not found' });

    // Quality gate: check if column_name is changing to a gated column
    if (req.body.column_name) {
      // Merge current card with incoming changes to check against final state
      const merged = { ...cards[0] };
      if (req.body.labels !== undefined) merged.labels = req.body.labels;
      if (req.body.testing_result !== undefined) merged.testing_result = req.body.testing_result;
      if (req.body.demo_gif_url !== undefined) merged.demo_gif_url = req.body.demo_gif_url;
      const gateError = validateCardForGatedColumn(merged, req.body.column_name);
      if (gateError) return res.status(422).json({ error: gateError });
    }

    const allowedFields = ['title', 'description', 'card_type', 'column_name', 'position', 'priority', 'assignee_id', 'testing_status', 'testing_result', 'demo_gif_url', 'illustration_url', 'labels', 'token_usage', 'due_date'];
    const updates = [];
    const values = [];
    let paramIdx = 1;

    for (const field of allowedFields) {
      if (req.body[field] !== undefined) {
        updates.push(`${field} = $${paramIdx++}`);
        values.push(field === 'labels' ? JSON.stringify(req.body[field]) : req.body[field]);
      }
    }

    if (updates.length === 0) return res.status(400).json({ error: 'No fields to update' });

    updates.push(`updated_at = NOW()`);
    values.push(req.params.cardId, req.params.projectId);

    await pool.query(`UPDATE cards SET ${updates.join(', ')} WHERE id = $${paramIdx++} AND project_id = $${paramIdx}`, values);

    const { rows } = await pool.query(`
      SELECT c.*, u.username as assignee_username, u.display_name as assignee_display_name
      FROM cards c LEFT JOIN users u ON c.assignee_id = u.id WHERE c.id = $1
    `, [req.params.cardId]);
    broadcastToProject(req.params.projectId, 'card_updated', { card: rows[0], user: req.user.username });
    res.json(rows[0]);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.delete('/api/projects/:projectId/cards/:cardId', authenticate, requireProjectAccess, async (req, res) => {
  if (req.memberRole === 'reviewer') return res.status(403).json({ error: 'Reviewers cannot delete cards' });
  await pool.query('DELETE FROM cards WHERE id = $1 AND project_id = $2', [req.params.cardId, req.params.projectId]);
  broadcastToProject(req.params.projectId, 'card_deleted', { cardId: req.params.cardId, user: req.user.username });
  res.json({ ok: true });
});

// ── Card archiving ───────────────────────────────────────────────────

app.post('/api/projects/:projectId/cards/:cardId/archive', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { rows } = await pool.query(
      'UPDATE cards SET archived_at = NOW(), updated_at = NOW() WHERE id = $1 AND project_id = $2 AND archived_at IS NULL RETURNING *',
      [req.params.cardId, req.params.projectId]
    );
    if (!rows[0]) return res.status(404).json({ error: 'Card not found or already archived' });
    broadcastToProject(req.params.projectId, 'card_archived', { card: rows[0], user: req.user.username });
    res.json(rows[0]);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.post('/api/projects/:projectId/cards/:cardId/unarchive', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { rows } = await pool.query(
      'UPDATE cards SET archived_at = NULL, updated_at = NOW() WHERE id = $1 AND project_id = $2 AND archived_at IS NOT NULL RETURNING *',
      [req.params.cardId, req.params.projectId]
    );
    if (!rows[0]) return res.status(404).json({ error: 'Card not found or not archived' });
    broadcastToProject(req.params.projectId, 'card_unarchived', { card: rows[0], user: req.user.username });
    res.json(rows[0]);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.get('/api/projects/:projectId/cards/archived', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const page = Math.max(1, parseInt(req.query.page) || 1);
    const limit = Math.min(100, Math.max(1, parseInt(req.query.limit) || 20));
    const offset = (page - 1) * limit;

    const countResult = await pool.query(
      'SELECT COUNT(*) FROM cards WHERE project_id = $1 AND archived_at IS NOT NULL',
      [req.params.projectId]
    );
    const total = parseInt(countResult.rows[0].count);

    const { rows } = await pool.query(`
      SELECT c.*, u.username as assignee_username, u.display_name as assignee_display_name
      FROM cards c LEFT JOIN users u ON c.assignee_id = u.id
      WHERE c.project_id = $1 AND c.archived_at IS NOT NULL
      ORDER BY c.archived_at DESC
      LIMIT $2 OFFSET $3
    `, [req.params.projectId, limit, offset]);

    res.json({ cards: rows, page, limit, total, total_pages: Math.ceil(total / limit) });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Card move (drag & drop) ────────────────────────────────────────────

app.post('/api/projects/:projectId/cards/:cardId/move', authenticate, requireProjectAccess, async (req, res) => {
  try {
    if (req.memberRole === 'reviewer') return res.status(403).json({ error: 'Reviewers cannot move cards' });
    const { column_name, position } = req.body;
    const { rows: cards } = await pool.query('SELECT * FROM cards WHERE id = $1 AND project_id = $2', [req.params.cardId, req.params.projectId]);
    if (!cards[0]) return res.status(404).json({ error: 'Card not found' });

    const columns = req.project.columns_config;
    if (!columns.includes(column_name)) return res.status(400).json({ error: 'Invalid column' });

    // Quality gate check
    const gateError = validateCardForGatedColumn(cards[0], column_name);
    if (gateError) return res.status(422).json({ error: gateError });

    const client = await pool.connect();
    try {
      await client.query('BEGIN');
      await client.query('UPDATE cards SET position = position + 1 WHERE project_id = $1 AND column_name = $2 AND position >= $3', [req.params.projectId, column_name, position]);
      // Time tracking: set started_at when entering In Progress, completed_at when entering Done
      let timeExtra = '';
      const timeParams = [column_name, position, req.params.cardId];
      if (column_name === 'In Progress' && !cards[0].started_at) {
        timeExtra = ', started_at = NOW()';
      }
      if (column_name === 'Done') {
        timeExtra += ', completed_at = NOW()';
      } else if (cards[0].column_name === 'Done') {
        // Moving back from Done — clear completed_at
        timeExtra += ', completed_at = NULL';
      }
      await client.query(`UPDATE cards SET column_name = $1, position = $2, updated_at = NOW()${timeExtra} WHERE id = $3`, timeParams);
      await client.query('COMMIT');
    } catch (e) {
      await client.query('ROLLBACK');
      throw e;
    } finally {
      client.release();
    }

    const { rows } = await pool.query('SELECT * FROM cards WHERE id = $1', [req.params.cardId]);
    broadcastToProject(req.params.projectId, 'card_moved', { card: rows[0], user: req.user.username });

    // WIP limit advisory warning
    const wipLimits = req.project.wip_limits || {};
    const colLimit = wipLimits[column_name];
    const result = { ...rows[0] };
    if (colLimit) {
      const { rows: countRows } = await pool.query('SELECT COUNT(*) FROM cards WHERE project_id = $1 AND column_name = $2 AND archived_at IS NULL', [req.params.projectId, column_name]);
      const count = parseInt(countRows[0].count);
      if (count > colLimit) {
        result.wip_warning = `Column "${column_name}" is over its WIP limit (${count}/${colLimit})`;
      }
    }
    res.json(result);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Card Pickup (agent claims a card) ─────────────────────────────────

app.post('/api/projects/:projectId/cards/:cardId/pickup', authenticate, requireProjectAccess, async (req, res) => {
  try {
    if (req.memberRole === 'reviewer') return res.status(403).json({ error: 'Reviewers cannot pick up cards' });
    const { rows: cards } = await pool.query('SELECT * FROM cards WHERE id = $1 AND project_id = $2', [req.params.cardId, req.params.projectId]);
    if (!cards[0]) return res.status(404).json({ error: 'Card not found' });

    const card = cards[0];
    if (card.column_name !== 'Todo') {
      return res.status(422).json({ error: `Card must be in Todo to pick up. Current column: ${card.column_name}` });
    }
    if (card.assignee_id && card.assignee_id !== req.user.id) {
      return res.status(409).json({ error: 'Card is already assigned to another agent' });
    }

    // Atomic: assign + move to In Progress
    const client = await pool.connect();
    try {
      await client.query('BEGIN');
      // Re-check assignee inside transaction to prevent race conditions
      const { rows: check } = await client.query('SELECT assignee_id FROM cards WHERE id = $1 FOR UPDATE', [req.params.cardId]);
      if (check[0].assignee_id && check[0].assignee_id !== req.user.id) {
        await client.query('ROLLBACK');
        return res.status(409).json({ error: 'Card was claimed by another agent' });
      }

      const { rows: maxRows } = await client.query('SELECT MAX(position) as maxp FROM cards WHERE project_id = $1 AND column_name = $2', [req.params.projectId, 'In Progress']);
      const newPos = (maxRows[0]?.maxp ?? -1) + 1;

      await client.query('UPDATE cards SET assignee_id = $1, column_name = $2, position = $3, updated_at = NOW(), started_at = COALESCE(started_at, NOW()) WHERE id = $4',
        [req.user.id, 'In Progress', newPos, req.params.cardId]);

      // Auto-comment
      const commentId = require('uuid').v4();
      await client.query('INSERT INTO card_comments (id, card_id, user_id, content, comment_type) VALUES ($1, $2, $3, $4, $5)',
        [commentId, req.params.cardId, req.user.id, `Card picked up by ${req.user.username}`, 'system']);

      await client.query('COMMIT');
    } catch (e) {
      await client.query('ROLLBACK');
      throw e;
    } finally {
      client.release();
    }

    const { rows } = await pool.query('SELECT * FROM cards WHERE id = $1', [req.params.cardId]);
    broadcastToProject(req.params.projectId, 'card_updated', { card: rows[0], user: req.user.username });
    res.json(rows[0]);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Card Approval ──────────────────────────────────────────────────────

app.post('/api/projects/:projectId/cards/:cardId/approve', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const isPrivileged = req.memberRole === 'owner' || req.memberRole === 'product_manager' || req.memberRole === 'reviewer';
    const isAgent = req.user && req.user.system_role === 'agent';
    const autoReviewEnabled = !!req.project.auto_review;

    if (!isPrivileged && !(isAgent && autoReviewEnabled)) {
      return res.status(403).json({ error: autoReviewEnabled ? 'Only product owners, reviewers, and agents (auto-review) can approve cards' : 'Only product owners and reviewers can approve cards' });
    }

    const { rows: cards } = await pool.query('SELECT * FROM cards WHERE id = $1 AND project_id = $2', [req.params.cardId, req.params.projectId]);
    if (!cards[0]) return res.status(404).json({ error: 'Card not found' });

    // Reviewers and agents (auto-review) can only approve/reject cards in Review column
    if ((req.memberRole === 'reviewer' || (isAgent && !isPrivileged)) && cards[0].column_name !== 'Review') {
      return res.status(403).json({ error: 'Can only approve or reject cards in the Review column' });
    }

    const { action, comment } = req.body; // action: 'approve', 'reject', or 'revise'
    if (!action || !['approve', 'reject', 'revise'].includes(action)) {
      return res.status(400).json({ error: 'action must be "approve", "reject", or "revise"' });
    }

    const statusMap = { approve: 'approved', reject: 'rejected', revise: 'revision_requested' };
    const status = statusMap[action];
    await pool.query('UPDATE cards SET approval_status = $1, approved_by = $2, updated_at = NOW() WHERE id = $3',
      [status, req.user.id, req.params.cardId]);

    // Auto-move approved cards from Proposed to Todo
    if (action === 'approve' && cards[0].column_name === 'Proposed') {
      const { rows: maxRows } = await pool.query('SELECT MAX(position) as maxp FROM cards WHERE project_id = $1 AND column_name = $2', [req.params.projectId, 'Todo']);
      const newPos = (maxRows[0]?.maxp ?? -1) + 1;
      await pool.query('UPDATE cards SET column_name = $1, position = $2, updated_at = NOW() WHERE id = $3',
        ['Todo', newPos, req.params.cardId]);
    }

    // Add auto-comment
    const commentId = uuidv4();
    let commentText;
    if (action === 'approve') {
      commentText = `Card approved and moved to Todo by ${req.user.username}`;
    } else if (action === 'revise') {
      commentText = comment
        ? `Revision requested by ${req.user.username}: ${comment}`
        : `Revision requested by ${req.user.username}. Please update the proposal and re-submit.`;
    } else {
      commentText = comment
        ? `Proposal rejected by ${req.user.username}: ${comment}`
        : `Proposal rejected by ${req.user.username}. This feature is not needed.`;
    }
    await pool.query('INSERT INTO card_comments (id, card_id, user_id, content, comment_type) VALUES ($1, $2, $3, $4, $5)',
      [commentId, req.params.cardId, req.user.id, commentText, 'system']);

    const { rows } = await pool.query('SELECT * FROM cards WHERE id = $1', [req.params.cardId]);
    broadcastToProject(req.params.projectId, 'card_updated', { card: rows[0], user: req.user.username });
    res.json(rows[0]);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Auto-review status ──────────────────────────────────────────────────

app.get('/api/projects/:projectId/auto-review', authenticate, requireProjectAccess, async (req, res) => {
  try {
    if (!req.project.auto_review) {
      return res.json({ enabled: false, cards: [] });
    }
    // Return cards in Review that haven't been reviewed yet (approval_status not set or 'approved' from Proposed stage)
    const { rows } = await pool.query(`
      SELECT c.*, u.username as assignee_username, u.display_name as assignee_display_name
      FROM cards c
      LEFT JOIN users u ON c.assignee_id = u.id
      WHERE c.project_id = $1 AND c.column_name = 'Review' AND c.archived_at IS NULL
      ORDER BY c.priority DESC, c.updated_at ASC
    `, [req.params.projectId]);
    res.json({ enabled: true, cards: rows });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Re-propose (agent re-submits after revision) ────────────────────────

app.post('/api/projects/:projectId/cards/:cardId/repropose', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { rows: cards } = await pool.query('SELECT * FROM cards WHERE id = $1 AND project_id = $2', [req.params.cardId, req.params.projectId]);
    if (!cards[0]) return res.status(404).json({ error: 'Card not found' });

    if (cards[0].approval_status !== 'revision_requested') {
      return res.status(422).json({ error: `Card can only be re-proposed when revision is requested. Current status: ${cards[0].approval_status}` });
    }

    await pool.query('UPDATE cards SET approval_status = $1, updated_at = NOW() WHERE id = $2', ['pending', req.params.cardId]);

    const commentId = uuidv4();
    const commentText = req.body.comment
      ? `Card re-proposed by ${req.user.username}: ${req.body.comment}`
      : `Card re-proposed by ${req.user.username} after revision.`;
    await pool.query('INSERT INTO card_comments (id, card_id, user_id, content, comment_type) VALUES ($1, $2, $3, $4, $5)',
      [commentId, req.params.cardId, req.user.id, commentText, 'system']);

    const { rows } = await pool.query('SELECT * FROM cards WHERE id = $1', [req.params.cardId]);
    broadcastToProject(req.params.projectId, 'card_updated', { card: rows[0], user: req.user.username });
    res.json(rows[0]);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Upload demo GIF ─────────────────────────────────────────────────────

app.post('/api/projects/:projectId/cards/:cardId/upload', authenticate, requireProjectAccess, upload.single('file'), async (req, res) => {
  if (!req.file) return res.status(400).json({ error: 'No file uploaded' });

  const url = `/uploads/${req.file.filename}`;
  await pool.query('UPDATE cards SET demo_gif_url = $1, updated_at = NOW() WHERE id = $2 AND project_id = $3', [url, req.params.cardId, req.params.projectId]);
  broadcastToProject(req.params.projectId, 'card_updated', { cardId: req.params.cardId, user: req.user.username });

  res.json({ url });
});

// ── Card Comments ───────────────────────────────────────────────────────

app.get('/api/projects/:projectId/cards/:cardId/comments', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { rows } = await pool.query(`
      SELECT cc.*, u.username, u.display_name FROM card_comments cc
      LEFT JOIN users u ON cc.user_id = u.id
      WHERE cc.card_id = $1 ORDER BY cc.created_at ASC
    `, [req.params.cardId]);
    res.json(rows);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.post('/api/projects/:projectId/cards/:cardId/comments', authenticate, requireProjectAccess, async (req, res) => {
  try {
    // Reviewers can only comment on cards in Review
    if (req.memberRole === 'reviewer') {
      const { rows: cardCheck } = await pool.query('SELECT column_name FROM cards WHERE id = $1 AND project_id = $2', [req.params.cardId, req.params.projectId]);
      if (!cardCheck[0] || cardCheck[0].column_name !== 'Review') {
        return res.status(403).json({ error: 'Reviewers can only comment on cards in the Review column' });
      }
    }
    const { content, comment_type } = req.body;
    if (!content) return res.status(400).json({ error: 'Content required' });

    const id = uuidv4();
    await pool.query('INSERT INTO card_comments (id, card_id, user_id, content, comment_type) VALUES ($1, $2, $3, $4, $5)', [
      id, req.params.cardId, req.user.id, content, comment_type || 'comment'
    ]);

    const comment = { id, card_id: req.params.cardId, user_id: req.user.id, content, comment_type: comment_type || 'comment' };
    broadcastToProject(req.params.projectId, 'comment_added', { comment, user: req.user.username });
    res.json(comment);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Card Links (Dependencies) ────────────────────────────────────────

app.get('/api/projects/:projectId/cards/:cardId/links', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { rows } = await pool.query(`
      SELECT cl.*,
        sc.title as source_title, sc.column_name as source_column, sc.labels as source_labels,
        tc.title as target_title, tc.column_name as target_column, tc.labels as target_labels,
        u.username as created_by_username
      FROM card_links cl
      JOIN cards sc ON cl.source_card_id = sc.id
      JOIN cards tc ON cl.target_card_id = tc.id
      LEFT JOIN users u ON cl.created_by = u.id
      WHERE cl.source_card_id = $1 OR cl.target_card_id = $1
      ORDER BY cl.created_at ASC
    `, [req.params.cardId]);
    res.json(rows);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.post('/api/projects/:projectId/cards/:cardId/links', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { target_card_id, link_type } = req.body;
    if (!target_card_id) return res.status(400).json({ error: 'target_card_id required' });
    const validTypes = ['blocks', 'blocked_by', 'related'];
    if (link_type && !validTypes.includes(link_type)) {
      return res.status(400).json({ error: `Invalid link_type. Valid: ${validTypes.join(', ')}` });
    }

    // Verify target card exists in same project
    const { rows: targetCards } = await pool.query('SELECT id FROM cards WHERE id = $1 AND project_id = $2', [target_card_id, req.params.projectId]);
    if (!targetCards[0]) return res.status(404).json({ error: 'Target card not found in this project' });

    // Cannot link to self
    if (target_card_id === req.params.cardId) return res.status(400).json({ error: 'Cannot link a card to itself' });

    const type = link_type || 'blocks';
    const id = uuidv4();

    // For blocks/blocked_by, store as a normalized "blocks" relationship
    let sourceId = req.params.cardId;
    let targetId = target_card_id;
    let storedType = type;

    if (type === 'blocked_by') {
      // "A blocked_by B" means "B blocks A"
      sourceId = target_card_id;
      targetId = req.params.cardId;
      storedType = 'blocks';
    }

    await pool.query(
      'INSERT INTO card_links (id, source_card_id, target_card_id, link_type, created_by) VALUES ($1, $2, $3, $4, $5)',
      [id, sourceId, targetId, storedType, req.user.id]
    );

    const { rows } = await pool.query(`
      SELECT cl.*,
        sc.title as source_title, sc.column_name as source_column,
        tc.title as target_title, tc.column_name as target_column,
        u.username as created_by_username
      FROM card_links cl
      JOIN cards sc ON cl.source_card_id = sc.id
      JOIN cards tc ON cl.target_card_id = tc.id
      LEFT JOIN users u ON cl.created_by = u.id
      WHERE cl.id = $1
    `, [id]);

    broadcastToProject(req.params.projectId, 'card_link_added', { link: rows[0], user: req.user.username });
    res.json(rows[0]);
  } catch (err) {
    if (err.code === '23505') return res.status(409).json({ error: 'This link already exists' });
    res.status(500).json({ error: err.message });
  }
});

app.delete('/api/projects/:projectId/cards/:cardId/links/:linkId', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { rows } = await pool.query(
      'DELETE FROM card_links WHERE id = $1 AND (source_card_id = $2 OR target_card_id = $2) RETURNING *',
      [req.params.linkId, req.params.cardId]
    );
    if (!rows[0]) return res.status(404).json({ error: 'Link not found' });
    broadcastToProject(req.params.projectId, 'card_link_removed', { linkId: req.params.linkId, user: req.user.username });
    res.json({ deleted: true });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── SSE Endpoint ────────────────────────────────────────────────────────

// SSE auth: EventSource can't send headers, so accept token from query string
app.get('/api/projects/:projectId/events', (req, res, next) => {
  if (req.query.token && !req.headers.authorization) {
    req.headers.authorization = `Bearer ${req.query.token}`;
  }
  next();
}, authenticate, requireProjectAccess, (req, res) => {
  const projectId = req.params.projectId;

  res.writeHead(200, {
    'Content-Type': 'text/event-stream',
    'Cache-Control': 'no-cache',
    'Connection': 'keep-alive',
    'X-Accel-Buffering': 'no',
  });
  res.write(`event: connected\ndata: {"project":"${projectId}"}\n\n`);

  if (!sseClients.has(projectId)) sseClients.set(projectId, new Set());
  sseClients.get(projectId).add(res);

  req.on('close', () => {
    sseClients.get(projectId)?.delete(res);
    if (sseClients.get(projectId)?.size === 0) sseClients.delete(projectId);
  });
});

// ── Project Stats (cycle time, velocity) ─────────────────────────────────

app.get('/api/projects/:projectId/stats', authenticate, requireProjectAccess, async (req, res) => {
  try {
    // Average cycle time per card type (In Progress → Done)
    const { rows: cycleTime } = await pool.query(`
      SELECT card_type,
        ROUND(AVG(EXTRACT(EPOCH FROM (completed_at - started_at)) / 3600)::numeric, 1) as avg_hours,
        COUNT(*) as count
      FROM cards
      WHERE project_id = $1 AND started_at IS NOT NULL AND completed_at IS NOT NULL
      GROUP BY card_type
    `, [req.params.projectId]);

    // Cards completed this week
    const { rows: weekRow } = await pool.query(`
      SELECT COUNT(*) as count FROM cards
      WHERE project_id = $1 AND completed_at >= NOW() - INTERVAL '7 days'
    `, [req.params.projectId]);

    // Cards completed this month
    const { rows: monthRow } = await pool.query(`
      SELECT COUNT(*) as count FROM cards
      WHERE project_id = $1 AND completed_at >= NOW() - INTERVAL '30 days'
    `, [req.params.projectId]);

    // WIP count per column
    const { rows: wip } = await pool.query(`
      SELECT column_name, COUNT(*) as count FROM cards
      WHERE project_id = $1
      GROUP BY column_name
    `, [req.params.projectId]);

    // Token usage stats
    const { rows: tokenTotal } = await pool.query(`
      SELECT COALESCE(SUM(token_usage), 0) as total_tokens,
             COUNT(*) FILTER (WHERE token_usage > 0) as cards_with_tokens
      FROM cards WHERE project_id = $1
    `, [req.params.projectId]);

    const { rows: tokenByType } = await pool.query(`
      SELECT card_type,
             COALESCE(SUM(token_usage), 0) as tokens,
             COUNT(*) FILTER (WHERE token_usage > 0) as card_count
      FROM cards WHERE project_id = $1
      GROUP BY card_type
    `, [req.params.projectId]);

    res.json({
      cycle_time: cycleTime,
      completed_this_week: parseInt(weekRow[0].count),
      completed_this_month: parseInt(monthRow[0].count),
      columns: wip.reduce((acc, r) => { acc[r.column_name] = parseInt(r.count); return acc; }, {}),
      token_usage: {
        total: parseInt(tokenTotal[0].total_tokens),
        cards_with_tokens: parseInt(tokenTotal[0].cards_with_tokens),
        by_type: tokenByType.reduce((acc, r) => { acc[r.card_type] = { tokens: parseInt(r.tokens), cards: parseInt(r.card_count) }; return acc; }, {})
      }
    });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Export ──────────────────────────────────────────────────────────────

app.get('/api/projects/:projectId/export', authenticate, requireProjectAccess, async (req, res) => {
  try {
    let where = 'c.project_id = $1 AND c.archived_at IS NULL';
    const params = [req.params.projectId];
    let idx = 2;

    if (req.query.column) { where += ` AND c.column_name = $${idx}`; params.push(req.query.column); idx++; }
    if (req.query.card_type) { where += ` AND c.card_type = $${idx}`; params.push(req.query.card_type); idx++; }
    if (req.query.priority) { where += ` AND c.priority = $${idx}`; params.push(req.query.priority); idx++; }

    const { rows } = await pool.query(`
      SELECT c.title, c.card_type, c.priority, c.column_name, c.description,
             c.labels, c.due_date, c.testing_status, c.testing_result,
             c.created_at, c.started_at, c.completed_at,
             u.username as assignee_username, u.display_name as assignee_name
      FROM cards c
      LEFT JOIN users u ON c.assignee_id = u.id
      WHERE ${where}
      ORDER BY c.column_name, c.position ASC
    `, params);

    const format = req.query.format || 'json';

    if (format === 'csv') {
      const headers = ['title','type','priority','column','assignee','labels','due_date','testing_status','created_at','started_at','completed_at'];
      const csvEscape = (val) => {
        if (val === null || val === undefined) return '';
        const s = String(val);
        if (s.includes(',') || s.includes('"') || s.includes('\n')) return '"' + s.replace(/"/g, '""') + '"';
        return s;
      };
      let csv = headers.join(',') + '\n';
      for (const r of rows) {
        csv += [
          csvEscape(r.title), csvEscape(r.card_type), csvEscape(r.priority),
          csvEscape(r.column_name), csvEscape(r.assignee_name || r.assignee_username || ''),
          csvEscape((r.labels || []).join('; ')),
          csvEscape(r.due_date ? new Date(r.due_date).toISOString().split('T')[0] : ''),
          csvEscape(r.testing_status),
          csvEscape(r.created_at ? new Date(r.created_at).toISOString() : ''),
          csvEscape(r.started_at ? new Date(r.started_at).toISOString() : ''),
          csvEscape(r.completed_at ? new Date(r.completed_at).toISOString() : '')
        ].join(',') + '\n';
      }
      res.setHeader('Content-Type', 'text/csv');
      res.setHeader('Content-Disposition', `attachment; filename="${req.project.name.replace(/[^a-zA-Z0-9]/g, '_')}_export.csv"`);
      res.send(csv);
    } else {
      const data = rows.map(r => ({
        title: r.title, type: r.card_type, priority: r.priority, column: r.column_name,
        description: r.description, assignee: r.assignee_name || r.assignee_username || null,
        labels: r.labels || [], due_date: r.due_date, testing_status: r.testing_status,
        created_at: r.created_at, started_at: r.started_at, completed_at: r.completed_at
      }));
      res.setHeader('Content-Type', 'application/json');
      res.setHeader('Content-Disposition', `attachment; filename="${req.project.name.replace(/[^a-zA-Z0-9]/g, '_')}_export.json"`);
      res.json(data);
    }
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Activity Feed ───────────────────────────────────────────────────────

app.get('/api/projects/:projectId/activity', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const since = req.query.since || new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString();
    const limit = Math.min(parseInt(req.query.limit) || 50, 200);

    const { rows: updatedCards } = await pool.query(`
      SELECT c.id, c.title, c.card_type, c.column_name, c.priority, c.testing_status,
             c.updated_at, c.created_at, cr.username as creator_username,
             'card_updated' as event_type
      FROM cards c
      LEFT JOIN users cr ON c.created_by = cr.id
      WHERE c.project_id = $1 AND c.updated_at > $2
      ORDER BY c.updated_at DESC
      LIMIT $3
    `, [req.params.projectId, since, limit]);

    const { rows: newComments } = await pool.query(`
      SELECT cc.id, cc.card_id, cc.content, cc.comment_type, cc.created_at,
             u.username, u.display_name,
             c.title as card_title,
             'comment_added' as event_type
      FROM card_comments cc
      JOIN cards c ON cc.card_id = c.id
      LEFT JOIN users u ON cc.user_id = u.id
      WHERE c.project_id = $1 AND cc.created_at > $2
      ORDER BY cc.created_at DESC
      LIMIT $3
    `, [req.params.projectId, since, limit]);

    const events = [
      ...updatedCards.map(c => ({ ...c, timestamp: c.updated_at })),
      ...newComments.map(c => ({ ...c, timestamp: c.created_at }))
    ].sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp)).slice(0, limit);

    res.json({ since, count: events.length, events });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── AI Illustration Generation ───────────────────────────────────────────

async function generateIllustration(cardId, projectId, title, description, cardType) {
  try {
    let svg;

    if (process.env.ANTHROPIC_API_KEY) {
      // Use AI to generate a detailed wireframe
      try {
        const client = new Anthropic();
        const msg = await client.messages.create({
          model: 'claude-haiku-4-5-20251001',
          max_tokens: 4000,
          messages: [{ role: 'user', content: `Generate an SVG wireframe/UI mockup illustration for this proposed feature. The SVG should be a clean, professional wireframe that visualizes what this feature would look like in a web application UI. Use a light gray background, simple shapes, placeholder text, and a modern minimal style. The SVG must be exactly 600x400 pixels.

Title: ${title}
Type: ${cardType}
Description: ${description}

Return ONLY the SVG markup starting with <svg and ending with </svg>. No other text, no code fences, no explanation.` }]
        });

        const tokensUsed = (msg.usage?.input_tokens || 0) + (msg.usage?.output_tokens || 0);
        if (tokensUsed > 0) {
          await pool.query('UPDATE cards SET token_usage = COALESCE(token_usage, 0) + $1 WHERE id = $2', [tokensUsed, cardId]);
        }
        svg = msg.content[0].text.trim();
        const svgMatch = svg.match(/<svg[\s\S]*<\/svg>/i);
        if (svgMatch) svg = svgMatch[0];
        if (!svg.startsWith('<svg')) svg = null;
      } catch (aiErr) {
        console.log('AI illustration failed, using heuristic:', aiErr.message);
      }
    }

    // Heuristic fallback: generate a basic wireframe SVG from keywords
    if (!svg) {
      svg = generateHeuristicIllustration(title, description, cardType);
    }

    if (!svg) return;

    // Save SVG to uploads
    const filename = `${uuidv4()}.svg`;
    const filepath = path.join(uploadsDir, filename);
    fs.writeFileSync(filepath, svg);

    // Update card with illustration URL
    await pool.query('UPDATE cards SET illustration_url = $1, updated_at = NOW() WHERE id = $2 AND project_id = $3',
      [`/uploads/${filename}`, cardId, projectId]);

    // Broadcast update
    const { rows } = await pool.query('SELECT * FROM cards WHERE id = $1', [cardId]);
    if (rows[0]) broadcastToProject(projectId, 'card_updated', { card: rows[0] });
  } catch (err) {
    console.log('Illustration generation failed:', err.message);
  }
}

function generateHeuristicIllustration(title, description, cardType) {
  const text = `${title} ${description}`.toLowerCase();
  const escSvg = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  const shortTitle = title.length > 50 ? title.substring(0, 47) + '...' : title;

  // Detect UI elements from keywords
  const hasButton = /button|toggle|switch|click|btn/.test(text);
  const hasForm = /form|input|field|search|filter|login|register/.test(text);
  const hasTable = /table|list|grid|column|row|data/.test(text);
  const hasModal = /modal|dialog|popup|overlay|dropdown/.test(text);
  const hasNav = /nav|menu|sidebar|header|topbar|toolbar/.test(text);
  const hasCard = /card|tile|panel|widget/.test(text);
  const hasChart = /chart|graph|stats|analytics|dashboard/.test(text);
  const hasNotif = /notif|alert|badge|bell|toast/.test(text);

  // Color scheme based on card type
  const colors = {
    feature: { primary: '#6366f1', bg: '#eef2ff', accent: '#818cf8' },
    bug: { primary: '#ef4444', bg: '#fef2f2', accent: '#f87171' },
    task: { primary: '#0ea5e9', bg: '#f0f9ff', accent: '#38bdf8' },
    improvement: { primary: '#10b981', bg: '#ecfdf5', accent: '#34d399' }
  };
  const c = colors[cardType] || colors.feature;

  let elements = '';
  let y = 100;

  // Header bar
  elements += `
    <rect x="20" y="20" width="560" height="50" rx="8" fill="${c.bg}" stroke="${c.primary}" stroke-width="1.5"/>
    <text x="40" y="50" font-size="14" font-weight="bold" fill="${c.primary}" font-family="system-ui, sans-serif">${escSvg(shortTitle)}</text>
    <text x="40" y="64" font-size="10" fill="#94a3b8" font-family="system-ui, sans-serif">${escSvg(cardType.toUpperCase())} — Proposed Wireframe</text>
  `;

  if (hasNav) {
    elements += `
      <rect x="20" y="${y}" width="120" height="260" rx="6" fill="#f1f5f9" stroke="#e2e8f0"/>
      <rect x="30" y="${y+12}" width="100" height="8" rx="3" fill="#cbd5e1"/>
      <rect x="30" y="${y+30}" width="80" height="8" rx="3" fill="${c.accent}"/>
      <rect x="30" y="${y+48}" width="90" height="8" rx="3" fill="#cbd5e1"/>
      <rect x="30" y="${y+66}" width="70" height="8" rx="3" fill="#cbd5e1"/>
      <rect x="30" y="${y+84}" width="85" height="8" rx="3" fill="#cbd5e1"/>
    `;
    y = 100;
    // Shift main content right
    elements += `<rect x="156" y="${y}" width="424" height="260" rx="6" fill="white" stroke="#e2e8f0"/>`;
    y += 20;

    if (hasButton) {
      elements += `<rect x="176" y="${y}" width="100" height="32" rx="6" fill="${c.primary}"/>
        <text x="200" y="${y+20}" font-size="11" fill="white" font-family="system-ui, sans-serif">Action</text>`;
      y += 48;
    }
    if (hasForm) {
      elements += `
        <rect x="176" y="${y}" width="200" height="28" rx="4" fill="white" stroke="#e2e8f0"/>
        <text x="186" y="${y+18}" font-size="10" fill="#94a3b8" font-family="system-ui, sans-serif">Input field...</text>
        <rect x="176" y="${y+38}" width="200" height="28" rx="4" fill="white" stroke="#e2e8f0"/>
        <text x="186" y="${y+56}" font-size="10" fill="#94a3b8" font-family="system-ui, sans-serif">Another field...</text>
      `;
      y += 80;
    }
  } else {
    // Main content area
    elements += `<rect x="20" y="${y}" width="560" height="260" rx="6" fill="white" stroke="#e2e8f0"/>`;
    y += 20;

    if (hasForm) {
      elements += `
        <text x="40" y="${y+4}" font-size="11" fill="#475569" font-family="system-ui, sans-serif">Form</text>
        <rect x="40" y="${y+12}" width="240" height="30" rx="4" fill="white" stroke="#e2e8f0"/>
        <text x="50" y="${y+32}" font-size="10" fill="#94a3b8" font-family="system-ui, sans-serif">Input field...</text>
        <rect x="40" y="${y+52}" width="240" height="30" rx="4" fill="white" stroke="#e2e8f0"/>
        <text x="50" y="${y+72}" font-size="10" fill="#94a3b8" font-family="system-ui, sans-serif">Another field...</text>
        <rect x="40" y="${y+92}" width="100" height="30" rx="6" fill="${c.primary}"/>
        <text x="65" y="${y+112}" font-size="11" fill="white" font-family="system-ui, sans-serif">Submit</text>
      `;
      y += 140;
    }

    if (hasTable) {
      const ty = Math.max(y, 140);
      elements += `
        <line x1="40" y1="${ty}" x2="560" y2="${ty}" stroke="#e2e8f0"/>
        <rect x="40" y="${ty+4}" width="520" height="24" rx="2" fill="#f8fafc"/>
        <text x="50" y="${ty+20}" font-size="10" font-weight="bold" fill="#475569" font-family="system-ui, sans-serif">Name</text>
        <text x="200" y="${ty+20}" font-size="10" font-weight="bold" fill="#475569" font-family="system-ui, sans-serif">Status</text>
        <text x="350" y="${ty+20}" font-size="10" font-weight="bold" fill="#475569" font-family="system-ui, sans-serif">Priority</text>
        <text x="480" y="${ty+20}" font-size="10" font-weight="bold" fill="#475569" font-family="system-ui, sans-serif">Actions</text>
      `;
      for (let i = 0; i < 3; i++) {
        const ry = ty + 32 + i * 28;
        elements += `
          <rect x="40" y="${ry}" width="520" height="24" rx="2" fill="${i % 2 ? '#f8fafc' : 'white'}"/>
          <rect x="50" y="${ry+8}" width="${60+i*20}" height="8" rx="3" fill="#cbd5e1"/>
          <rect x="200" y="${ry+6}" width="50" height="12" rx="6" fill="${c.bg}" stroke="${c.primary}" stroke-width="0.5"/>
          <rect x="350" y="${ry+8}" width="40" height="8" rx="3" fill="#fde68a"/>
        `;
      }
      y = ty + 120;
    }

    if (hasCard && !hasTable) {
      const cy = Math.max(y, 140);
      for (let i = 0; i < 3; i++) {
        const cx = 40 + i * 180;
        elements += `
          <rect x="${cx}" y="${cy}" width="160" height="100" rx="6" fill="white" stroke="#e2e8f0"/>
          <rect x="${cx+10}" y="${cy+12}" width="100" height="10" rx="3" fill="#cbd5e1"/>
          <rect x="${cx+10}" y="${cy+30}" width="140" height="8" rx="3" fill="#e2e8f0"/>
          <rect x="${cx+10}" y="${cy+44}" width="120" height="8" rx="3" fill="#e2e8f0"/>
          <rect x="${cx+10}" y="${cy+66}" width="60" height="22" rx="4" fill="${c.bg}" stroke="${c.primary}" stroke-width="0.5"/>
        `;
      }
      y = cy + 120;
    }

    if (hasButton && !hasForm) {
      const by = Math.max(y, 140);
      elements += `
        <rect x="40" y="${by}" width="120" height="36" rx="6" fill="${c.primary}"/>
        <text x="66" y="${by+23}" font-size="12" fill="white" font-family="system-ui, sans-serif">Primary</text>
        <rect x="176" y="${by}" width="120" height="36" rx="6" fill="white" stroke="${c.primary}"/>
        <text x="196" y="${by+23}" font-size="12" fill="${c.primary}" font-family="system-ui, sans-serif">Secondary</text>
      `;
      y = by + 52;
    }

    if (hasModal) {
      elements += `
        <rect x="100" y="120" width="400" height="220" rx="10" fill="white" stroke="#e2e8f0" stroke-width="2" filter="url(#shadow)"/>
        <rect x="100" y="120" width="400" height="44" rx="10" fill="${c.bg}"/>
        <rect x="100" y="150" width="400" height="14" fill="${c.bg}"/>
        <text x="120" y="148" font-size="14" font-weight="bold" fill="${c.primary}" font-family="system-ui, sans-serif">Dialog Title</text>
        <text x="482" y="146" font-size="16" fill="#94a3b8" font-family="system-ui, sans-serif">✕</text>
        <rect x="120" y="180" width="360" height="8" rx="3" fill="#e2e8f0"/>
        <rect x="120" y="196" width="280" height="8" rx="3" fill="#e2e8f0"/>
        <rect x="120" y="220" width="200" height="30" rx="4" fill="white" stroke="#e2e8f0"/>
        <rect x="370" y="290" width="110" height="32" rx="6" fill="${c.primary}"/>
        <text x="394" y="310" font-size="11" fill="white" font-family="system-ui, sans-serif">Confirm</text>
      `;
    }

    if (hasNotif) {
      elements += `
        <circle cx="540" cy="40" r="12" fill="${c.bg}" stroke="${c.primary}"/>
        <text x="534" y="44" font-size="12" fill="${c.primary}" font-family="system-ui, sans-serif">🔔</text>
        <circle cx="548" cy="32" r="7" fill="#ef4444"/>
        <text x="545" y="35" font-size="8" fill="white" font-family="system-ui, sans-serif">3</text>
      `;
    }

    if (hasChart) {
      const cy = Math.max(y, 140);
      elements += `
        <rect x="40" y="${cy}" width="520" height="180" rx="6" fill="white" stroke="#e2e8f0"/>
        <text x="56" y="${cy+24}" font-size="12" fill="#475569" font-family="system-ui, sans-serif">Analytics</text>
      `;
      // Bar chart
      const bars = [0.6, 0.8, 0.4, 0.9, 0.7, 0.5, 0.85];
      bars.forEach((h, i) => {
        const bx = 70 + i * 66;
        const bh = h * 120;
        elements += `<rect x="${bx}" y="${cy+150-bh}" width="40" height="${bh}" rx="3" fill="${c.accent}" opacity="0.7"/>`;
      });
    }

    // Generic placeholder if no specific elements detected
    if (!hasForm && !hasTable && !hasCard && !hasButton && !hasModal && !hasChart && !hasNotif) {
      elements += `
        <rect x="40" y="120" width="520" height="12" rx="4" fill="#e2e8f0"/>
        <rect x="40" y="142" width="400" height="12" rx="4" fill="#e2e8f0"/>
        <rect x="40" y="164" width="460" height="12" rx="4" fill="#e2e8f0"/>
        <rect x="40" y="200" width="180" height="36" rx="6" fill="${c.primary}"/>
        <text x="88" y="224" font-size="12" fill="white" font-family="system-ui, sans-serif">Action</text>
        <rect x="40" y="260" width="250" height="80" rx="6" fill="#f8fafc" stroke="#e2e8f0"/>
        <rect x="310" y="260" width="250" height="80" rx="6" fill="#f8fafc" stroke="#e2e8f0"/>
      `;
    }
  }

  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 600 400" width="600" height="400">
  <defs>
    <filter id="shadow" x="-2%" y="-2%" width="104%" height="104%">
      <feDropShadow dx="0" dy="2" stdDeviation="4" flood-opacity="0.1"/>
    </filter>
  </defs>
  <rect width="600" height="400" rx="12" fill="#f8fafc" stroke="#e2e8f0"/>
  ${elements}
</svg>`;
}

// ── AI-Assisted Card Generation ──────────────────────────────────────────

app.post('/api/projects/:projectId/cards/ai-generate', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { description } = req.body;
    if (!description || description.trim().length < 5) {
      return res.status(400).json({ error: 'Please provide a description (at least 5 characters)' });
    }

    const text = description.trim();
    let result;

    // Try Anthropic API if key is available
    if (process.env.ANTHROPIC_API_KEY) {
      try {
        const client = new Anthropic();
        const msg = await client.messages.create({
          model: 'claude-haiku-4-5-20251001',
          max_tokens: 500,
          messages: [{ role: 'user', content: `Parse this feature/task description into a structured kanban card. Return ONLY valid JSON with these fields:
- title: short title (max 80 chars)
- description: detailed description
- card_type: one of "feature", "bug", "task", "improvement"
- priority: one of "low", "medium", "high", "critical"
- labels: array of relevant labels (e.g. "ui", "backend", "frontend", "api", "security", "performance", "ux", "database")

Input: "${text}"` }]
        });
        const tokensUsed = (msg.usage?.input_tokens || 0) + (msg.usage?.output_tokens || 0);
        result = JSON.parse(msg.content[0].text);
        result._token_usage = tokensUsed;
      } catch (aiErr) {
        // Fall through to heuristic parser
        console.log('AI generation failed, using heuristic:', aiErr.message);
      }
    }

    // Heuristic fallback
    if (!result) {
      result = heuristicCardParse(text);
    }

    // Ensure valid values
    const validTypes = ['feature', 'bug', 'task', 'improvement'];
    const validPriorities = ['low', 'medium', 'high', 'critical'];
    result.card_type = validTypes.includes(result.card_type) ? result.card_type : 'feature';
    result.priority = validPriorities.includes(result.priority) ? result.priority : 'medium';
    result.labels = Array.isArray(result.labels) ? result.labels.slice(0, 10) : [];
    result.title = (result.title || '').substring(0, 200);
    result.description = (result.description || '').substring(0, 5000);

    res.json(result);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

function heuristicCardParse(text) {
  const lower = text.toLowerCase();

  // Detect card type
  let card_type = 'feature';
  if (/\b(bug|fix|broken|crash|error|issue|wrong|fail)\b/.test(lower)) card_type = 'bug';
  else if (/\b(refactor|clean|improve|optimize|performance|speed)\b/.test(lower)) card_type = 'improvement';
  else if (/\b(task|setup|config|install|migrate|update|upgrade|deploy)\b/.test(lower)) card_type = 'task';

  // Detect priority
  let priority = 'medium';
  if (/\b(critical|urgent|asap|emergency|blocker|p0)\b/.test(lower)) priority = 'critical';
  else if (/\b(important|high priority|high.pri|p1)\b/.test(lower)) priority = 'high';
  else if (/\b(low priority|low.pri|nice.to.have|minor|p3)\b/.test(lower)) priority = 'low';

  // Detect labels
  const labels = [];
  if (/\b(ui|button|modal|page|layout|css|style|theme|dark.mode|light.mode|responsive)\b/.test(lower)) labels.push('ui');
  if (/\b(frontend|client|browser|dom|html|javascript|js)\b/.test(lower)) labels.push('frontend');
  if (/\b(backend|server|api|endpoint|route|express|node)\b/.test(lower)) labels.push('backend');
  if (/\b(database|db|sql|postgres|query|migration|schema)\b/.test(lower)) labels.push('database');
  if (/\b(auth|login|password|token|permission|security|role)\b/.test(lower)) labels.push('security');
  if (/\b(performance|speed|slow|cache|optimize|fast)\b/.test(lower)) labels.push('performance');
  if (/\b(test|testing|e2e|unit|coverage)\b/.test(lower)) labels.push('testing');
  if (/\b(ux|user.experience|usability|workflow|user.facing)\b/.test(lower)) labels.push('ux');

  // Generate title: first sentence or first 80 chars
  let title = text.split(/[.\n]/)[0].trim();
  if (title.length > 80) title = title.substring(0, 77) + '...';
  // Capitalize first letter
  title = title.charAt(0).toUpperCase() + title.slice(1);

  // Description is the full text
  const description = text;

  return { title, description, card_type, priority, labels };
}

// ── Bug Report Routes ────────────────────────────────────────────────────

// Submit a bug report (permission-gated)
app.post('/api/projects/:projectId/bugs', optionalAuthenticate, async (req, res) => {
  try {
    const perm = await checkBugReportPermission(req, req.params.projectId, 'submit');
    if (!perm.allowed) return res.status(perm.status).json({ error: perm.error });

    const { title, description, steps_to_reproduce, expected_behavior, actual_behavior,
            error_message, browser_info, url_location, severity, is_security_issue,
            reporter_name, reporter_email } = req.body;
    if (!title || title.trim().length < 3) return res.status(400).json({ error: 'Title is required (min 3 chars)' });

    const validSeverities = ['low', 'medium', 'high', 'critical'];
    const id = uuidv4();
    await pool.query(`
      INSERT INTO bug_reports (id, project_id, title, description, steps_to_reproduce,
        expected_behavior, actual_behavior, error_message, browser_info, url_location,
        severity, is_security_issue, reporter_id, reporter_name, reporter_email)
      VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
    `, [id, req.params.projectId, title.trim(), description || null, steps_to_reproduce || null,
        expected_behavior || null, actual_behavior || null, error_message || null,
        browser_info || null, url_location || null,
        validSeverities.includes(severity) ? severity : 'medium',
        !!is_security_issue,
        req.user ? req.user.id : null,
        req.user ? (req.user.display_name || req.user.username) : (reporter_name || 'Anonymous'),
        reporter_email || null]);

    const { rows } = await pool.query('SELECT * FROM bug_reports WHERE id = $1', [id]);
    res.status(201).json(rows[0]);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// List bug reports (permission-gated; security bugs only visible to members)
app.get('/api/projects/:projectId/bugs', optionalAuthenticate, async (req, res) => {
  try {
    const perm = await checkBugReportPermission(req, req.params.projectId, 'view');
    if (!perm.allowed) return res.status(perm.status).json({ error: perm.error });

    // Check if user is a project member (for security bug visibility)
    let isMember = perm.isMember || false;
    if (!isMember && req.user) {
      const { rows: proj } = await pool.query('SELECT owner_id FROM projects WHERE id = $1', [req.params.projectId]);
      if (proj[0] && proj[0].owner_id === req.user.id) isMember = true;
      if (!isMember) {
        const { rows: mem } = await pool.query('SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2', [req.params.projectId, req.user.id]);
        if (mem[0]) isMember = true;
      }
    }

    let query = 'SELECT * FROM bug_reports WHERE project_id = $1';
    const params = [req.params.projectId];

    // Non-members cannot see security bugs
    if (!isMember) {
      query += ' AND is_security_issue = FALSE';
    }

    // Optional filters
    if (req.query.status) {
      params.push(req.query.status);
      query += ` AND status = $${params.length}`;
    }
    if (req.query.severity) {
      params.push(req.query.severity);
      query += ` AND severity = $${params.length}`;
    }

    query += ' ORDER BY created_at DESC';
    const { rows } = await pool.query(query, params);
    res.json(rows);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// Get single bug report
app.get('/api/projects/:projectId/bugs/:bugId', optionalAuthenticate, async (req, res) => {
  try {
    const perm = await checkBugReportPermission(req, req.params.projectId, 'view');
    if (!perm.allowed) return res.status(perm.status).json({ error: perm.error });

    const { rows } = await pool.query('SELECT * FROM bug_reports WHERE id = $1 AND project_id = $2', [req.params.bugId, req.params.projectId]);
    if (!rows[0]) return res.status(404).json({ error: 'Bug report not found' });

    // Security bugs only visible to members
    if (rows[0].is_security_issue) {
      let isMember = false;
      if (req.user) {
        const { rows: proj } = await pool.query('SELECT owner_id FROM projects WHERE id = $1', [req.params.projectId]);
        if (proj[0]?.owner_id === req.user.id) isMember = true;
        if (!isMember) {
          const { rows: mem } = await pool.query('SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2', [req.params.projectId, req.user.id]);
          if (mem[0]) isMember = true;
        }
      }
      if (!isMember) return res.status(403).json({ error: 'Security bugs are only visible to project members' });
    }

    res.json(rows[0]);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// Update bug report (members only — status, link to card)
app.patch('/api/projects/:projectId/bugs/:bugId', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { status, linked_card_id } = req.body;
    const updates = [];
    const params = [];
    let idx = 1;

    if (status) {
      const validStatuses = ['new', 'triaged', 'in_progress', 'resolved', 'closed', 'wont_fix'];
      if (!validStatuses.includes(status)) return res.status(400).json({ error: 'Invalid status' });
      updates.push(`status = $${idx++}`);
      params.push(status);
    }
    if (linked_card_id !== undefined) {
      updates.push(`linked_card_id = $${idx++}`);
      params.push(linked_card_id || null);
    }
    if (updates.length === 0) return res.status(400).json({ error: 'No valid fields to update' });

    updates.push(`updated_at = NOW()`);
    params.push(req.params.bugId, req.params.projectId);
    const { rows } = await pool.query(
      `UPDATE bug_reports SET ${updates.join(', ')} WHERE id = $${idx++} AND project_id = $${idx} RETURNING *`,
      params
    );
    if (!rows[0]) return res.status(404).json({ error: 'Bug report not found' });
    res.json(rows[0]);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// Upload screenshot for bug report (permission-gated same as submit)
app.post('/api/projects/:projectId/bugs/:bugId/screenshot', optionalAuthenticate, upload.single('file'), async (req, res) => {
  try {
    const perm = await checkBugReportPermission(req, req.params.projectId, 'submit');
    if (!perm.allowed) return res.status(perm.status).json({ error: perm.error });
    if (!req.file) return res.status(400).json({ error: 'No file uploaded' });

    const url = `/uploads/${req.file.filename}`;
    const { rows } = await pool.query(
      'UPDATE bug_reports SET screenshot_url = $1, updated_at = NOW() WHERE id = $2 AND project_id = $3 RETURNING *',
      [url, req.params.bugId, req.params.projectId]
    );
    if (!rows[0]) return res.status(404).json({ error: 'Bug report not found' });
    res.json({ url, bug: rows[0] });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// Get bug report settings (public — needed by the submit form)
app.get('/api/projects/:projectId/bug-settings', async (req, res) => {
  try {
    const { rows } = await pool.query('SELECT id, name, bug_report_settings FROM projects WHERE id = $1', [req.params.projectId]);
    if (!rows[0]) return res.status(404).json({ error: 'Project not found' });
    res.json({ project_name: rows[0].name, settings: rows[0].bug_report_settings || { submit_permission: 'member', view_permission: 'member' } });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Start Server ────────────────────────────────────────────────────────

async function generateRecoveryCode(username) {
  await initDB();
  const { rows } = await pool.query('SELECT id FROM users WHERE username = $1', [username]);
  if (!rows[0]) {
    console.error(`Error: User "${username}" not found.`);
    process.exit(1);
  }

  const crypto = require('crypto');
  const code = crypto.randomBytes(16).toString('hex');
  const code_hash = bcrypt.hashSync(code, 10);
  const id = uuidv4();
  const expires_at = new Date(Date.now() + 60 * 60 * 1000).toISOString(); // 1 hour

  await pool.query(
    'INSERT INTO recovery_codes (id, user_id, code_hash, expires_at) VALUES ($1, $2, $3, $4)',
    [id, rows[0].id, code_hash, expires_at]
  );

  console.log(`Recovery code for "${username}":`);
  console.log(`  Code: ${code}`);
  console.log(`  Expires: ${new Date(expires_at).toLocaleString()}`);
  console.log(`\nUser resets password with:`);
  console.log(`  POST /api/auth/reset-password`);
  console.log(`  {"username":"${username}","recovery_code":"${code}","new_password":"<new>"}`);
  await pool.end();
}

async function start() {
  const args = process.argv.slice(2);
  const recoverIdx = args.indexOf('--recover');
  if (recoverIdx !== -1) {
    const username = args[recoverIdx + 1];
    if (!username) {
      console.error('Usage: node server.js --recover <username>');
      process.exit(1);
    }
    return generateRecoveryCode(username);
  }

  await initDB();

  // Auto-archive: archive Done cards older than N days per project setting
  async function autoArchiveDoneCards() {
    try {
      const { rows: projects } = await pool.query(
        'SELECT id, name, auto_archive_days FROM projects WHERE auto_archive_days > 0'
      );
      for (const project of projects) {
        const { rows: cards } = await pool.query(
          `UPDATE cards SET archived_at = NOW(), updated_at = NOW()
           WHERE project_id = $1 AND column_name = 'Done' AND archived_at IS NULL
             AND updated_at < NOW() - ($2 || ' days')::INTERVAL
           RETURNING id, title`,
          [project.id, project.auto_archive_days]
        );
        if (cards.length > 0) {
          console.log(`[auto-archive] Archived ${cards.length} card(s) in project "${project.name}" (>${project.auto_archive_days} days in Done)`);
          for (const card of cards) {
            broadcastToProject(project.id, 'card_archived', { card, user: 'system' });
          }
        }
      }
    } catch (err) {
      console.error('[auto-archive] Error:', err.message);
    }
  }

  // Run on startup and every hour
  await autoArchiveDoneCards();
  setInterval(autoArchiveDoneCards, 60 * 60 * 1000);

  app.listen(PORT, () => {
    console.log(`Duckllo running on http://localhost:${PORT}`);
  });
}

start().catch(err => {
  console.error('Failed to start:', err.message);
  process.exit(1);
});
